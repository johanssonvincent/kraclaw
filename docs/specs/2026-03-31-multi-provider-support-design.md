# Multi-Provider Support Design

**Date:** 2026-03-31
**Status:** Approved
**Scope:** Add OpenAI as a second AI provider, with architecture that supports future providers (Gemini, etc.)

## Context

Kraclaw currently assumes Anthropic as the sole AI provider. The agent binary is a Node.js app importing `@anthropic-ai/claude-agent-sdk`, config env vars are named `ANTHROPIC_*`, and model validation only allows Claude models.

The goal is to support multiple providers so customers (one group = one customer) can choose which AI model powers their Discord bot. This starts with OpenAI and establishes the pattern for future providers.

## Approach

**Per-provider Go agent images.** Each provider gets its own lightweight Go binary sharing a common IPC library. The kraclaw server selects the right image based on the group's provider config. The credential proxy routes to the right upstream and injects per-group credentials.

### Why This Approach

- Groups already have per-group `ContainerConfig` — adding `Provider` extends naturally.
- K8s Jobs already support different images — no architectural change needed.
- Go agents produce ~15-20MB images vs ~200-400MB for Node.js.
- All major providers (OpenAI, Anthropic, Google) have official Go SDKs.
- Each agent stays focused on its provider's SDK idioms — no leaky abstraction.
- Adding a new provider means a new `cmd/kraclaw-agent-<provider>/` binary.

## Section 1: Provider Model & Data Layer

### ContainerConfig Changes

```go
type ContainerConfig struct {
    AdditionalMounts []AdditionalMount
    Timeout          int
    Model            string
    Provider         string // "openai", "anthropic" — defaults to "anthropic"
}
```

### Storage

No database migration needed. `ContainerConfig` is stored as a JSON column (`container_config_json` in protobuf). Adding `provider` to the JSON is backwards-compatible — absent values default to `"anthropic"`.

### Validation

The orchestrator validates that `Model` is compatible with `Provider` at group registration time. An `openai` group cannot use `claude-sonnet-4-6`.

## Section 2: Credential Proxy — Per-Group Credentials

### Credential Store

```go
type CredentialStore interface {
    GetCredential(ctx context.Context, groupJID string) (*Credential, error)
    UpsertCredential(ctx context.Context, cred *Credential) error
    DeleteCredential(ctx context.Context, groupJID string) error
}

type Credential struct {
    GroupJID   string
    Provider   string // "openai", "anthropic"
    APIKey     string // encrypted at rest
    OAuthToken string // optional, for anthropic oauth flow
}
```

### Database Migration

A new `credentials` table is required for per-group credential storage. This is separate from the ContainerConfig JSON (which needs no migration).

### Encryption

API keys are encrypted before writing to MySQL using a platform-level encryption key (`CREDENTIAL_ENCRYPTION_KEY` env var). Decrypted only in-memory at proxy time.

### Proxy Routing Flow

1. Agent sends request to proxy with `X-Kraclaw-Group: <groupJID>` header.
2. Proxy looks up credential for that group from the store (with in-memory cache + TTL).
3. Based on the credential's provider, selects upstream URL and injects the right auth header:
   - `openai` -> `https://api.openai.com`, `Authorization: Bearer <key>`
   - `anthropic` -> `https://api.anthropic.com`, `X-Api-Key: <key>`
4. Strips kraclaw headers before forwarding.

### Fallback

If a group has no stored credential, the proxy falls back to platform-level env vars (`ANTHROPIC_API_KEY`, `OPENAI_API_KEY`). This covers the initial platform credentials model and provides a path to per-group BYOK later.

### Config

```go
type ProxyConfig struct {
    Addr string `envconfig:"PROXY_ADDR" default:":3001"`

    // Anthropic (platform-level fallback)
    AnthropicUpstreamURL string `envconfig:"ANTHROPIC_UPSTREAM_URL" default:"https://api.anthropic.com"`
    AnthropicAPIKey      string `envconfig:"ANTHROPIC_API_KEY"`
    AnthropicOAuthToken  string `envconfig:"ANTHROPIC_OAUTH_TOKEN"`
    AnthropicAPIVersion  string `envconfig:"ANTHROPIC_VERSION" default:"2023-06-01"`

    // OpenAI (platform-level fallback)
    OpenAIUpstreamURL string `envconfig:"OPENAI_UPSTREAM_URL" default:"https://api.openai.com"`
    OpenAIAPIKey      string `envconfig:"OPENAI_API_KEY"`

    // Encryption
    CredentialEncryptionKey string `envconfig:"CREDENTIAL_ENCRYPTION_KEY"`
}
```

### gRPC API

New `CredentialService` RPCs:

- `SetCredential` — stores encrypted key for a group.
- `DeleteCredential` — removes a group's credential.
- `TestCredential` — validates the key works against the provider's API.

## Section 3: Go Agent Architecture

### Shared Agent Library

A new `pkg/agent/` package provides common infrastructure:

```
pkg/agent/
  ipc.go      — Redis Streams IPC client (send/receive messages)
  agent.go    — Agent loop scaffold (connect, run, shutdown)
  health.go   — Liveness signaling
```

Extracts the IPC protocol (message types, Redis Streams consumer group logic) into a reusable Go package. Each provider agent imports it and only implements the provider-specific API call.

### Per-Provider Agent Binaries

```
cmd/kraclaw-agent-openai/main.go
cmd/kraclaw-agent-anthropic/main.go   # future — replaces Node.js agent
```

### OpenAI Agent MVP Flow

1. Starts, reads env vars (`REDIS_URL`, `KRACLAW_GROUP`, `KRACLAW_PROXY_URL`, `KRACLAW_PROVIDER`, `OPENAI_MODEL`).
2. Connects to Redis Streams IPC for its group.
3. Receives inbound messages via IPC.
4. Calls OpenAI chat completions API through the credential proxy (streaming).
5. Streams response chunks back via IPC as they arrive.
6. Maintains conversation history in-memory for the session.
7. On shutdown signal (IPC or SIGTERM), exits cleanly.

No tool use in MVP — conversation only.

### Container Image

```dockerfile
FROM golang:1.24 AS builder
# build...
FROM gcr.io/distroless/static-debian12
COPY --from=builder /app/kraclaw-agent-openai /agent
ENTRYPOINT ["/agent"]
```

~15-20MB final image.

## Section 4: Sandbox Controller — Provider-Aware Job Creation

### Image Selection

Provider-to-image mapping via server config:

```go
type K8sConfig struct {
    // existing fields...
    AgentImageAnthropic string `envconfig:"AGENT_IMAGE_ANTHROPIC"`
    AgentImageOpenAI    string `envconfig:"AGENT_IMAGE_OPENAI"`
}
```

The sandbox controller reads `group.ContainerConfig.Provider` and picks the right image. The hardcoded `node /app/dist/index.js` command is removed — each Go agent binary uses its own `ENTRYPOINT`.

### Environment Variables Passed to Container

```go
// Common (all providers)
{Name: "REDIS_URL", Value: c.redisURL},
{Name: "KRACLAW_GROUP", Value: cfg.GroupJID},
{Name: "KRACLAW_GROUP_FOLDER", Value: cfg.GroupFolder},
{Name: "KRACLAW_PROXY_URL", Value: c.proxyURL},
{Name: "KRACLAW_PROVIDER", Value: cfg.Provider},
{Name: "HOME", Value: "/home/nonroot"},

// Provider-specific
// OpenAI:
{Name: "OPENAI_MODEL", Value: cfg.Model},
// Anthropic:
{Name: "ANTHROPIC_BASE_URL", Value: c.proxyURL},
{Name: "CLAUDE_MODEL", Value: cfg.Model},
```

### Backwards Compatibility

The existing Node.js Anthropic agent keeps working. If `Provider` is empty or `"anthropic"` and `AgentImageAnthropic` points to the Node.js image, nothing changes. The Go Anthropic agent is a future swap-in replacement.

## Section 5: Model Validation & Provider Registry

### Provider Registry

```go
type ProviderInfo struct {
    ID           string
    Models       []ModelInfo
    DefaultModel string
}

type ModelInfo struct {
    ID          string
    DisplayName string
}
```

Registered at startup, not dynamically fetched. Models are added manually when support is desired.

### Initial OpenAI Models

| Model ID | Display Name | Notes |
|----------|-------------|-------|
| `gpt-5.4` | GPT-5.4 | Flagship (default) |
| `gpt-5.4-mini` | GPT-5.4 Mini | Fast, lower cost |
| `gpt-5.4-nano` | GPT-5.4 Nano | Cheapest, high-volume |
| `gpt-5.4-pro` | GPT-5.4 Pro | Max performance |
| `gpt-5.3-codex` | GPT-5.3 Codex | Agentic coding |
| `o3-mini` | o3-mini | Reasoning (math/science/code) |

### Initial Anthropic Models

Existing `oauthAllowedModels` list migrated into the registry:

| Model ID | Display Name |
|----------|-------------|
| `claude-opus-4-6` | Claude Opus 4.6 |
| `claude-sonnet-4-6` | Claude Sonnet 4.6 |
| `claude-haiku-4-5` | Claude Haiku 4.5 |

### Validation Points

- `RegisterGroup` — rejects if model doesn't match provider.
- `SetModel` IPC command — validates before forwarding to agent.

## Section 6: CI/CD

### New Workflow: `kraclaw-agent-openai.yml`

Builds and publishes `ghcr.io/johanssonvincent/kraclaw-agent-openai`.

**Triggers:**
- Push to `main` — tags `latest` + git SHA.
- Version tags `v*` — tags semver.
- PR builds — build-only, no push.

**Hardening:** Pinned actions to SHA, minimal permissions, concurrency controls. Same standards as existing workflows.

### Licensing & Distribution

| Image | SDK License | Publishable |
|-------|------------|-------------|
| `kraclaw-agent-openai` | Apache 2.0 (`openai-go`) | Yes |
| `kraclaw-agent-anthropic` (future Go) | MIT (`anthropic-sdk-go`) | Yes |
| Current Node.js agent | Proprietary (`claude-agent-sdk`) | No — workflow removed |

### Future: Anthropic Go Agent Workflow

When the Node.js agent is rewritten in Go using `anthropic-sdk-go` (MIT), it gets its own `kraclaw-agent-anthropic.yml` workflow and can be published to ghcr.io.

## Out of Scope

- Tool use in agents (follow-up iteration).
- Rewriting the Anthropic Node.js agent in Go (separate effort).
- Multi-tenancy / tenant auth layer (agent-workshop concern).
- Per-group billing / cost tracking (agent-workshop's budget system).
- BYOK / customer-provided API keys — platform credentials only for now.
- Gemini or other providers (same pattern, later).
- Session resumption for OpenAI agent.
- Frontend / TUI changes for provider selection (groups configured via gRPC API).
