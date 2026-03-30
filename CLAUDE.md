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
| `internal/queue/` | Redis-backed per-group message queue |
| `internal/ipc/` | Redis Streams IPC broker |
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
- **Testing:** Table-driven tests, `miniredis` for Redis, `go-sqlmock` for MySQL, `client-go/kubernetes/fake` for K8s.
- **Commit messages:** Conventional commits — `feat:`, `fix:`, `chore:`, `docs:`, `refactor:`, `test:`.
- **Errors:** Always wrap with `fmt.Errorf("<operation>: %w", err)`.
- **Ports:** gRPC (:50051), REST (:8080), credential proxy (:3001).

## Redis Key Schema

| Key | Purpose |
|-----|---------|
| `kraclaw:ipc:{group}:output` | Stream: agent → server |
| `kraclaw:ipc:{group}:input` | Stream: server → agent |
| `kraclaw:ipc:{group}:close` | Close signal (60s TTL) |
| `kraclaw:queue:{groupJID}` | Per-group message list |
| `kraclaw:active` | Set of active group JIDs |
| `kraclaw:ipc:notify` | Pub/sub: IPC notifications |
| `kraclaw:queue:notify` | Pub/sub: queue notifications |

## Architecture

Single Go binary with multiple concurrent goroutines. Redis-backed IPC and message queues. MySQL for state persistence. gRPC + REST API for external clients. Per-group isolated Kubernetes Jobs for agent execution. Channel registry pattern for pluggable messaging integrations.

### Layers

- **API** (`internal/server/`): gRPC services, REST gateway, health checks, TLS, CIDR allowlisting.
- **Orchestration** (`internal/orchestrator/`): Message polling, channel lifecycle, command handling, group state.
- **Messaging** (`internal/channel/`, `internal/router/`, `internal/queue/`): Channel implementations, message formatting, per-group queues.
- **IPC** (`internal/ipc/`): Redis Streams broker with consumer groups for server↔agent communication.
- **Storage** (`internal/store/`): MySQL interface with golang-migrate migrations.
- **Sandbox** (`internal/sandbox/`): K8s Job controller for agent lifecycle.
- **Credential Proxy** (`internal/credproxy/`): HTTP reverse proxy injecting API credentials.
- **Scheduler** (`internal/scheduler/`): Cron/interval/once task scheduling with per-group isolation.

### Key Abstractions

- **Channel interface**: Standardized messaging platform integration (Discord, Telegram, TUI).
- **Queue**: Per-group message buffering with Redis lists and pub/sub notifications.
- **Store**: Composed of `GroupStore`, `MessageStore`, `ChatStore`, `TaskStore`, `SessionStore`.
- **IPC Broker**: Redis Streams with consumer groups for asymmetric publish/subscribe.
