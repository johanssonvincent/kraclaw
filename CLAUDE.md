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

- **Logging:** `log/slog` (stdlib). Never use logrus or other third-party loggers.
- **Config:** `envconfig` tags on config structs
- **Testing:** Table-driven tests, `miniredis` for Redis, `go-sqlmock` for MySQL, `client-go/kubernetes/fake` for K8s
- **Commit messages:** Conventional commits — `feat:`, `fix:`, `chore:`
- **Ports:** gRPC (:50051), REST (:8080), credential proxy (:3001)

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

<!-- GSD:project-start source:PROJECT.md -->
## Project

**Kraclaw — Code Review Fixes**

Kraclaw is a K8s-native AI agent orchestrator written in Go. It routes messages from chat channels (Discord, Telegram, TUI) to Claude agents running in K8s Jobs, with per-group isolated filesystems and memory. This milestone focuses on resolving issues identified during a comprehensive code review.

**Core Value:** Fix critical bugs and error handling gaps that could cause silent message loss, panics, or security vulnerabilities in production.

### Constraints

- **Tech stack**: Go, existing dependencies only — no new libraries except `golang.org/x/sync/singleflight`
- **Compatibility**: All fixes must be backward-compatible — no API or config changes
- **Testing**: Table-driven tests, miniredis for Redis, go-sqlmock for MySQL, client-go/kubernetes/fake for K8s
- **Logging**: `log/slog` only — thread injected logger through constructors
<!-- GSD:project-end -->

<!-- GSD:stack-start source:codebase/STACK.md -->
## Technology Stack

## Languages
- Go 1.26.1 - Server implementation, single binary, gRPC API, TUI client
- Protobuf - gRPC service definitions and message serialization
- SQL - MySQL schema and migrations
- YAML - Kubernetes manifests, buf configuration
## Runtime
- Go 1.26-alpine (build container)
- Alpine 3.21 (runtime container)
- Kubernetes 1.23+ (for agent Job scheduling)
- Go modules (go.mod/go.sum)
- Lockfile: Present (go.sum with 100 dependencies)
## Frameworks
- gRPC 1.79.3 - RPC protocol for server-to-client communication
- Protocol Buffers 1.36.10 - Message serialization and code generation
- Bubbletea v2 2.0.2 - Terminal UI framework
- Bubbles v2 2.0.0 - Bubbletea UI components
- Lipgloss v2 2.0.2 - Terminal styling
- Glamour v2 2.0.0 - Markdown rendering
- Go standard testing package (table-driven tests)
- go-sqlmock 1.5.2 - MySQL mocking
- miniredis/v2 2.37.0 - In-memory Redis for testing
- kubernetes/fake 0.35.2 - Kubernetes fake client for testing
- Buf v2 - Protobuf code generation and linting
- golangci-lint - Code linting
- golang-migrate/v4 4.19.1 - Database migration tool
- Docker - Container build
## Key Dependencies
- redis/go-redis/v9 9.18.0 - Redis client for queuing, IPC, and caching
- go-sql-driver/mysql 1.9.3 - MySQL driver
- k8s.io/client-go 0.35.2 - Kubernetes client for Job management
- k8s.io/api 0.35.2 - Kubernetes API types
- k8s.io/apimachinery 0.35.2 - Kubernetes common utilities
- bwmarrin/discordgo 0.29.0 - Discord bot SDK
- go-telegram/bot 1.19.0 - Telegram bot SDK
- robfig/cron/v3 3.0.1 - Cron expression parsing and scheduling
- prometheus/client_golang 1.23.2 - Prometheus metrics collection
- kelseyhightower/envconfig 1.4.0 - Environment variable config parsing
## Configuration
- Configuration via environment variables using envconfig struct tags
- Required vars: MYSQL_DSN, AGENT_IMAGE, ANTHROPIC_API_KEY or ANTHROPIC_OAUTH_TOKEN
- Optional vars with defaults: GRPC_ADDR, REST_ADDR, REDIS_URL, K8S_NAMESPACE, LOG_LEVEL, etc.
- `Makefile` - Build targets for server, TUI, tests, Docker
- `buf.yaml` - Protobuf linting configuration
- `buf.gen.yaml` - Code generation config (Go, gRPC)
- Dockerfile - Multi-stage build (Alpine base, CGO_ENABLED=0 for static binary)
- .golangci.yml - Linter configuration (inferred from make lint target)
- `ANTHROPIC_API_KEY` - Direct API key for Anthropic Claude API (takes precedence)
- `ANTHROPIC_OAUTH_TOKEN` - OAuth token for Anthropic API (fallback)
- `ANTHROPIC_VERSION` - API version (default: 2023-06-01)
- `MYSQL_DSN` - Database connection string (required)
- `REDIS_URL` - Redis connection URL (default: redis://localhost:6379)
- `K8S_NAMESPACE` - Kubernetes namespace (default: kraclaw)
- `AGENT_IMAGE` - Agent container image (required)
- `GRPC_ADDR` - Server gRPC address (default: :50051)
- `REST_ADDR` - Server REST gateway address (default: :8080)
- `PROXY_ADDR` - Credential proxy address (default: :3001)
- `DISCORD_TOKEN` - Discord bot token (optional)
- `TELEGRAM_TOKEN` - Telegram bot token (optional)
## Platform Requirements
- Go 1.26.1+
- MySQL 5.7+ or 8.0
- Redis 5.0+
- Docker (for testing and building images)
- Kubernetes cluster access (for sandbox testing)
- buf CLI (for protobuf code generation)
- golangci-lint (for linting)
- Kubernetes 1.23+ cluster
- MySQL 5.7+ database (external or in-cluster)
- Redis 5.0+ (external or in-cluster)
- Container registry access (ghcr.io/johanssonvincent by default)
- TLS certificates for gRPC server (cert, key, CA)
- Service account with Job creation permissions
- Kubernetes-native deployment
- Single binary deployment model
- Three listening ports: gRPC (:50051), REST (:8080), credential proxy (:3001)
- Container image: ghcr.io/johanssonvincent/kraclaw:{VERSION}
<!-- GSD:stack-end -->

<!-- GSD:conventions-start source:CONVENTIONS.md -->
## Conventions

## Naming Patterns
- Package files use lowercase, no underscores: `queue.go`, `channel.go`, `server.go`
- Test files use `_test.go` suffix: `queue_test.go`, `config_test.go`
- No abbreviated filenames — spell out full purpose
- Exported functions use PascalCase: `NewMySQLStore()`, `Connect()`, `IsConnected()`
- Unexported helper functions use camelCase: `setupLogger()`, `connectRedis()`, `runMigrations()`
- Constructor functions follow `New<Type>()` pattern: `NewServer()`, `NewRedisQueue()`
- Test function names follow `Test<Exported><Scenario>()` pattern: `TestHealthzHandler_Returns200`, `TestReadyzHandler_DBDown`
- Package-level constants use UPPER_SNAKE_CASE: `EventEnqueued`, `EventInactive`, `labelManagedBy`, `managedByValue`
- Local variables use camelCase: `msg`, `ctx`, `rdb`, `cfg`, `mysqlStore`
- Boolean variables follow `is<PascalCase>` for clarity: `isConnected`, `isGroup`, `isMain`, `wantErr`
- Exported types use PascalCase: `Config`, `ServerConfig`, `QueueMessage`, `Channel`
- Struct fields use PascalCase: `GroupJID`, `Content`, `Sender`, `Timestamp`
- Interface types name the behavior they describe: `Channel`, `Queue`, `Factory`
- Mock types for testing use `mock<Type>` pattern: `mockStore`, matching the interface they implement
## Code Style
- Go standard formatting via `go fmt`
- Line length: no hard limit, but keep readable (typically under 120 characters)
- Run `make fmt` before committing
- `golangci-lint` enforced via `make lint`
- No custom configuration file — uses golangci-lint defaults
- All code must pass `golangci-lint run` without errors
- Tabs (Go standard)
- One level per scope
## Import Organization
- Protobuf imports use abbreviated aliases: `kraclawv1 "github.com/johanssonvincent/kraclaw/pkg/pb/kraclawv1"`
- No other aliasing — use full package names
## Error Handling
- Always wrap errors with `fmt.Errorf()` using `%w` verb to preserve error chain: `fmt.Errorf("ping mysql: %w", err)`
- Use format `fmt.Errorf("<operation>: %w", err)` where operation is the failed action
- Never discard errors — handle explicitly or propagate
- Return error as last return value
- Log errors with context using structured logging: `log.Error("operation failed", "field", value, "error", err)`
## Logging
- Configured in `internal/config/config.go` with `LoggingConfig` struct
- Logger created in `cmd/kraclaw/main.go` via `setupLogger()` function
- Logger passed as dependency: `log *slog.Logger` in struct fields
- Default logger available via `slog.Default()` or `slog.With(key, value)` for contextual logging
- Info level for lifecycle events: `log.Info("kraclaw starting", "version", version)`
- Warn level for non-fatal issues: `log.Warn("gRPC server running WITHOUT TLS — use only for in-pod access")`
- Error level with structured context: `log.Error("failed to connect to Kubernetes", "error", err)`
- Use key-value pairs, not formatted strings: `log.Info("connected", "addr", cfg.Addr)` not `log.Info("connected to " + cfg.Addr)`
- `LevelDebug`: for development/troubleshooting details
- `LevelInfo`: startup, shutdown, normal operation
- `LevelWarn`: degraded functionality, non-fatal failures
- `LevelError`: fatal or unrecoverable failures
- Configured via `LOG_LEVEL` env var (default: info)
## Comments
- Explain the "why" not the "what" — code shows what it does
- Document non-obvious algorithmic choices or workarounds
- Mark safety preconditions (e.g., mutex must be held)
- Explain business logic or domain-specific requirements
- Every package must have a package-level comment above `package` declaration
- Example: `// Package queue provides message processing queue backed by Redis`
- Exported functions should have a one-line comment starting with the function name
- Example: `// Connect establishes a connection to the Discord gateway`
- Short, imperative mood preferred
- Not applicable — Go uses plain comments
- For struct fields with non-obvious purpose, add inline comments: `connected bool // RWMutex-protected`
## Function Design
- Keep functions small and focused (typically under 50 lines)
- If a function gets complex, extract helper functions
- Use small functions to compose behavior
- Prefer passing `context.Context` as first parameter
- Group related parameters into config structs when count exceeds 3-4 parameters
- Examples from `internal/server/server.go`:
- Return errors as last value (Go convention)
- Return results before errors: `(*Type, error)`
- Don't use named return values except for defer cleanup patterns
- Empty returns acceptable for void-like functions
## Module Design
- Interfaces at package level: `type Channel interface`, `type Queue interface`
- Factory functions to create instances: `func New(cfg Config) (*Type, error)`
- Keep internal implementation types (like `MySQLStore`) unexported when possible
- Use interfaces to enable testing and swapping implementations
- Not used — each file has clear single responsibility
- No `init.go` or similar aggregation files
- Channel registry uses `init()` functions for self-registration: `func init() { channel.DefaultRegistry.Register(...) }`
- One type per file when type is complex (e.g., `Controller` in `sandbox/controller.go`)
- Related utilities in same file (e.g., `SandboxConfig` validation in same file as `Controller`)
- Test utilities in separate test files (e.g., helper mocks in `orchestrator_test.go`)
<!-- GSD:conventions-end -->

<!-- GSD:architecture-start source:ARCHITECTURE.md -->
## Architecture

## Pattern Overview
- Single Go binary with multiple concurrent goroutines
- Redis-backed IPC and message queues
- MySQL for state persistence
- gRPC + REST API for external clients
- Per-group isolated Kubernetes Jobs for agent execution
- Channel registry pattern for pluggable messaging integrations (Discord, Telegram, TUI)
- Polling-based message dispatch with notification signals for immediate handling
## Layers
- Purpose: Expose gRPC services and REST gateway for external interaction
- Location: `internal/server/`
- Contains: gRPC service implementations, REST handlers, health checks, metrics, TLS security, CIDR allowlisting
- Depends on: IPC broker, sandbox controller, store, channels
- Used by: External clients (TUI, agents, admin tools)
- Purpose: Wire all components together and manage the message processing loop
- Location: `internal/orchestrator/`
- Contains: Main orchestrator logic, message polling, channel connection lifecycle, command handling, trigger evaluation, group state management
- Depends on: Queue, IPC broker, router, scheduler, channels, store, sandbox controller
- Used by: Entry point (`cmd/kraclaw/main.go`)
- Purpose: Receive, route, format, and dispatch messages between channels and agents
- Location: `internal/channel/`, `internal/router/`, `internal/queue/`
- Contains: Channel interface implementations, message formatting for agents (XML), per-group message queues
- Depends on: Store, sandbox controller
- Used by: Orchestrator, API layer
- Purpose: Bidirectional communication between server and agent processes running in K8s Jobs
- Location: `internal/ipc/`
- Contains: Redis Streams-based message broker with consumer groups, publish/subscribe patterns
- Depends on: Redis
- Used by: Orchestrator, sandbox controller
- Purpose: Store messages, groups, chats, tasks, sessions, and orchestrator state
- Location: `internal/store/`
- Contains: MySQL interface definitions, MySQL implementation with golang-migrate migrations
- Depends on: MySQL database
- Used by: Orchestrator, router, scheduler
- **Sandbox Management:** `internal/sandbox/` - K8s Job controller for agent lifecycle, pod watcher
- **Credential Proxy:** `internal/credproxy/` - HTTP reverse proxy that injects API credentials (Anthropic) into requests from agents
- **Scheduler:** `internal/scheduler/` - Cron/interval/once task scheduling with per-group isolation
- **Authentication:** `internal/auth/` - Sender allowlist for command authorization
- **Metrics:** `internal/metrics/` - Prometheus metrics collection
- **Configuration:** `internal/config/` - envconfig-based configuration loading
## Data Flow
- **Cursor State:** `lastTimestamp` (global), `lastAgentTimestamp` (per-chat), `lastConfirmedTimestamp` (per-chat) stored in MySQL `router_state` table
- **Session State:** Per-group session IDs stored in MySQL `sessions` table for agent continuity
- **Group Registry:** Loaded from MySQL on startup, refreshed on each message poll
- **Queue State:** Per-group active/inactive status stored in Redis set `kraclaw:active`
## Key Abstractions
- Purpose: Standardize integration of messaging platforms
- Examples: `internal/channel/discord/discord.go`, `internal/channel/telegram/telegram.go`, `internal/channel/tui/tui.go`
- Pattern: Factory-based registration via `channel.DefaultRegistry.Register()`, callbacks for inbound messages and metadata updates
- Purpose: Provide per-group message buffering and active state tracking
- Location: `internal/queue/queue.go`
- Pattern: Redis lists for message storage, Redis sets for active group tracking, pub/sub for notifications
- Purpose: Abstract database operations behind interfaces for testability
- Location: `internal/store/store.go`
- Pattern: Composed of `GroupStore`, `MessageStore`, `ChatStore`, `TaskStore`, `SessionStore`, etc. - each with specific responsibilities
- Purpose: Abstract messaging between server and agents
- Location: `internal/ipc/ipc.go`
- Pattern: Redis Streams with consumer groups, asymmetric publish/subscribe (input/output streams per group)
## Entry Points
- Location: `cmd/kraclaw/main.go`
- Triggers: Invoked at container/process start
- Responsibilities: Load config, connect to MySQL/Redis/Kubernetes, create orchestrator and API servers, handle graceful shutdown
- Location: `cmd/kraclaw-tui/main.go`
- Triggers: User starts CLI tool
- Responsibilities: Parse flags, connect to gRPC server, render interactive TUI using Bubbletea v2
- Location: `internal/orchestrator/orchestrator.go#Start()`
- Triggers: Invoked by main() after component initialization
- Responsibilities: Load persisted state, connect channels, start scheduler, start IPC watcher, poll messages every 2 seconds or on notification
- Location: `internal/server/api_services.go`, `internal/server/api_channel.go`
- Triggers: gRPC client calls
- Responsibilities: Implement AdminService (status, events, metrics), GroupService (CRUD), TaskService (CRUD), etc.
## Error Handling
- **Non-Fatal Errors:** Store errors logged, orchestrator moves to next message
- **Initialization Failures:** Exit immediately during startup (MySQL, Redis, Kubernetes, channels)
- **Graceful Degradation:** Kubernetes connection optional (sandbox admin APIs degraded if unavailable)
- **Context Cancellation:** All long-running operations check `ctx.Done()` and exit cleanly
- **IPC Watcher Retry:** Retries on read errors with 5s exponential backoff
## Cross-Cutting Concerns
- Framework: `log/slog` (stdlib)
- Pattern: Structured logging with key-value pairs, log level configurable via `LOG_LEVEL` env var
- Location: Initialized in `cmd/kraclaw/main.go` with JSON or text format based on `LOG_FORMAT`
- Pattern: Validate structs in types layer (e.g., `Group.Validate()`, `SandboxConfig.Validate()`)
- Approach: Return early with descriptive errors during initialization
- Strategy: Sender allowlist per chat JID
- Pattern: `auth.Authorizer` checks sender against allowlist before permitting commands
- Location: `internal/auth/auth.go`
- Queue: Enforces `MAX_CONCURRENT` limit via active group set
- Scheduler: Runs due tasks sequentially, polls every 60s
- Message Loop: Polls every 2 seconds or on notification (no backpressure)
<!-- GSD:architecture-end -->

<!-- GSD:workflow-start source:GSD defaults -->
## GSD Workflow Enforcement

Before using Edit, Write, or other file-changing tools, start work through a GSD command so planning artifacts and execution context stay in sync.

Use these entry points:
- `/gsd:quick` for small fixes, doc updates, and ad-hoc tasks
- `/gsd:debug` for investigation and bug fixing
- `/gsd:execute-phase` for planned phase work

### Planning Gate Defaults

- For major project phases, pause after planning and do not execute file-changing actions until user approval.
- Treat work as a major phase when one or more are true: cross-package changes, architecture or API impact, migration or infra changes, or estimated runtime over 30 minutes.
- After planning, post a checkpoint with:
  - concise plan steps
  - assumptions and open questions
  - implementation options with tradeoffs
  - a recommended option and why
- End the checkpoint with a clear approval prompt: `Approve execution with: /gsd:execute-phase <phase-id>`.
- Before approval, only run read-only analysis (Read, Glob, Grep, non-mutating Bash).
- For small `/gsd:quick` tasks that do not meet major-phase criteria, execute directly after a brief plan.

Do not make direct repo edits outside a GSD workflow unless the user explicitly asks to bypass it.
<!-- GSD:workflow-end -->

<!-- GSD:profile-start -->
## Developer Profile

> Profile not yet configured. Run `/gsd:profile-user` to generate your developer profile.
> This section is managed by `generate-claude-profile` -- do not edit manually.
<!-- GSD:profile-end -->
