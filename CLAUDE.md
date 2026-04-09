# Kraclaw

K8s-native AI agent orchestrator. Go rewrite of nanoclaw.

## Quick Context

Single Go binary, multiple goroutines. gRPC API + REST gateway + credential proxy on three ports. Channels (Discord, Telegram) are pluggable via registry pattern. Messages route to Claude agents running in K8s Jobs. Each group has isolated filesystem and memory.

## Build Commands

```bash
make build          # Build server binary
make build-tui      # Build TUI client
make test           # Run all tests
make test-short     # Unit tests only
make proto          # Generate protobuf (requires buf)
make lint           # golangci-lint
make docker-build   # Build container image
```

## Code Structure

| Package | Purpose |
|---------|---------|
| `cmd/kraclaw/` | Server entry point, config loading, signal handling |
| `cmd/kraclaw-tui/` | Bubbletea TUI client |
| `internal/config/` | envconfig-based configuration |
| `internal/server/` | gRPC server + REST gateway + health endpoints |
| `internal/store/` | Store interfaces and MySQL implementation |
| `internal/queue/` | NATS JetStream per-group message queue |
| `internal/ipc/` | NATS JetStream IPC broker |
| `internal/sandbox/` | K8s Job lifecycle controller |
| `internal/channel/` | Channel interface, registry, Discord, Telegram |
| `internal/router/` | Message formatting and outbound routing |
| `internal/auth/` | Sender allowlist |
| `internal/scheduler/` | Cron/interval/once task scheduler |
| `internal/credproxy/` | HTTP reverse proxy injecting API keys |
| `internal/orchestrator/` | Top-level wiring, message loop, channel lifecycle |
| `internal/metrics/` | Prometheus metrics |
| `proto/` | Protobuf service definitions |
| `migrations/` | MySQL migration files (golang-migrate) |

## Conventions

- **Logging:** `log/slog` (stdlib). Never use third-party loggers.
- **Config:** `envconfig` tags on config structs.
- **Testing:** Table-driven tests, embedded `nats-server/v2` for NATS JetStream, `go-sqlmock` for MySQL, `client-go/kubernetes/fake` for K8s.
- **Commit messages:** Conventional commits — `feat:`, `fix:`, `chore:`, `docs:`, `refactor:`, `test:`.
- **PR Docker images:** Tag as `{current-version}-PR-{pr-number}` (e.g., `v1.2.3-PR-42`).
- **Errors:** Always wrap with `fmt.Errorf("<operation>: %w", err)`.
- **Ports:** gRPC (:50051), REST (:8080), credential proxy (:3001).

## NATS Subject Schema

### Queue (internal/queue/)

| Stream Name | Subject | Retention | Purpose |
|-------------|---------|-----------|---------|
| `KRACLAW_QUEUE_{SANITIZED}` | `kraclaw.queue.{sanitized}` | WorkQueuePolicy | Per-group message queue |

Where `{SANITIZED}` = first 16 bytes of SHA-256 hex of groupJID (32 hex chars), uppercase for stream name, lowercase for subject.

Consumer names:
- Dequeue: `dequeue-{sanitized}`

### IPC (internal/ipc/)

| Stream Name | Subject | Retention | Purpose |
|-------------|---------|-----------|---------|
| `KRACLAW_IPC_{SANITIZED}` | `kraclaw.ipc.{sanitized}.*.input`, `kraclaw.ipc.{sanitized}.*.output` | LimitsPolicy (1h MaxAge) | Bidirectional agent↔server messages |

Where `{SANITIZED}` = first 16 bytes of SHA-256 hex of groupJID (32 hex chars), uppercase for stream name, lowercase for subjects. The `*` wildcard matches sanitized agentID.

Consumer names:
- Output subscriber (server): `kraclaw-server-{sanitized}` (durable, wildcard `kraclaw.ipc.{sanitized}.*.output`)
- Input reader (agent): `agent-{sanitized_agent_id}` (durable, filtered to `kraclaw.ipc.{sanitized}.{sanitized_agent_id}.input`)

## Architecture

Single Go binary with multiple concurrent goroutines. NATS JetStream IPC and message queues. MySQL for state persistence. gRPC + REST API for external clients. Per-group isolated Kubernetes Jobs for agent execution. Channel registry pattern for pluggable messaging integrations.

### Layers

- **API** (`internal/server/`): gRPC services, REST gateway, health checks, TLS, CIDR allowlisting.
- **Orchestration** (`internal/orchestrator/`): Message polling, channel lifecycle, command handling, group state.
- **Messaging** (`internal/channel/`, `internal/router/`, `internal/queue/`): Channel implementations, message formatting, per-group queues.
- **IPC** (`internal/ipc/`): NATS JetStream broker for server↔agent communication.
- **Storage** (`internal/store/`): MySQL interface with golang-migrate migrations.
- **Sandbox** (`internal/sandbox/`): K8s Job controller for agent lifecycle.
- **Credential Proxy** (`internal/credproxy/`): HTTP reverse proxy injecting API credentials.
- **Scheduler** (`internal/scheduler/`): Cron/interval/once task scheduling with per-group isolation.

### Key Abstractions

- **Channel interface**: Standardized messaging platform integration (Discord, Telegram, TUI).
- **Queue**: Per-group message buffering with NATS JetStream WorkQueue streams.
- **Store**: Composed of `GroupStore`, `MessageStore`, `ChatStore`, `TaskStore`, `SessionStore`.
- **IPC Broker**: NATS JetStream streams for asymmetric server↔agent publish/subscribe.
