# 🐙 Kraclaw

**K8s-native AI agent orchestrator.** Go rewrite of [nanoclaw](https://github.com/qwibitai/nanoclaw).

Kraclaw turns a Kubernetes cluster into a managed runtime for AI agents. It runs Claude agents in fully isolated K8s Jobs — each with its own filesystem, network policy, and resource limits — while handling the hard parts: multi-channel messaging, credential injection, scheduled tasks, and real-time observability. Think of it as a control plane for conversational AI workloads that treats every agent like a proper cloud-native citizen.

---

## 🏗️ Architecture

Messages arrive from chat platforms (Discord, Telegram), get routed through a per-group NATS JetStream queue, and trigger sandboxed K8s Jobs where Claude agents execute. Agents communicate back through a NATS JetStream IPC broker, and results are routed to the originating channel. A gRPC API exposes five services (Admin, Group, Task, Channel, Sandbox) for programmatic control, with a Bubbletea TUI as the primary operator interface.

```mermaid
%%{init: {'theme': 'base', 'themeVariables': {'primaryColor': '#dbeafe', 'primaryTextColor': '#1e3a5f', 'primaryBorderColor': '#3b82f6', 'lineColor': '#64748b', 'secondaryColor': '#f1f5f9', 'tertiaryColor': '#eff6ff', 'fontSize': '16px', 'fontFamily': 'system-ui, -apple-system, sans-serif'}, 'flowchart': {'curve': 'basis', 'nodeSpacing': 40, 'rankSpacing': 60, 'padding': 20}}}%%

graph TB
    discord["Discord"]
    telegram["Telegram"]
    tui["TUI Client"]

    subgraph server [" Kraclaw Server"]

        subgraph ingestion [" Message Ingestion"]
            channels["Channel Registry<br/>Discord · Telegram"]
            router["Message Router"]
            queue["NATS JetStream<br/>per-group WorkQueue"]
        end

        subgraph api [" API Layer"]
            grpc["gRPC Server · :50051"]
            rest["REST Gateway · :8080<br/>/healthz · /readyz · /metrics"]
        end

        subgraph execution [" Agent Execution"]
            scheduler["Scheduler<br/>cron · interval · once"]
            orchestrator["Orchestrator"]
            sandbox["Sandbox Controller"]
        end

        subgraph infra [" Infrastructure"]
            ipc["NATS JetStream · IPC Broker"]
            credproxy["Credential Proxy · :3001"]
            auth["Auth · sender allowlist"]
            metrics["Prometheus Metrics"]
        end
    end

    subgraph k8s [" Kubernetes Cluster"]
        jobs["K8s Jobs<br/>isolated agent pods"]
        netpol["Network Policy"]
        pvcs[("PVCs<br/>groups · sessions · data")]
    end

    nats[("NATS JetStream")]
    mysql[("MySQL")]
    anthropic["Anthropic API"]

    discord --> channels
    telegram --> channels
    channels --> router
    router --> queue
    queue --> orchestrator
    scheduler --> orchestrator
    orchestrator --> sandbox
    sandbox -->|launch| jobs
    jobs -.- pvcs
    orchestrator <--> mysql

    tui -->|gRPC + TLS| grpc
    grpc --> orchestrator
    orchestrator -.-> auth
    metrics -.-> rest
    ipc -->|agent results| router
    jobs <-->|input/output streams| ipc
    jobs -->|proxied requests| credproxy
    netpol -.->|restricts access| credproxy
    credproxy -->|injected credentials| anthropic
    queue <--> nats
    ipc <--> nats

    classDef client fill:#dbeafe,stroke:#3b82f6,color:#1e40af,stroke-width:2px,font-weight:bold,font-size:16px
    classDef nd fill:#fff,stroke:#3b82f6,color:#1e293b,stroke-width:1.5px,font-size:14px
    classDef orch fill:#fef3c7,stroke:#f59e0b,color:#92400e,stroke-width:2.5px,font-weight:bold,font-size:16px
    classDef k8snd fill:#ede9fe,stroke:#7c3aed,color:#4c1d95,stroke-width:2px,font-size:14px
    classDef db fill:#d1fae5,stroke:#059669,color:#065f46,stroke-width:2px,font-weight:bold,font-size:16px
    classDef ext fill:#ffedd5,stroke:#ea580c,color:#9a3412,stroke-width:2px,font-weight:bold,font-size:16px

    class discord,telegram,tui client
    class channels,router,queue,grpc,rest,scheduler,sandbox,ipc,credproxy,auth,metrics nd
    class orchestrator orch
    class jobs,netpol,pvcs k8snd
    class nats,mysql db
    class anthropic ext

    style server fill:#eff6ff,stroke:#3b82f6,stroke-width:2px,color:#1e3a5f,font-size:20px,font-weight:bold
    style ingestion fill:#dbeafe,stroke:#93c5fd,color:#1e3a5f,font-size:16px,font-weight:bold
    style api fill:#dbeafe,stroke:#93c5fd,color:#1e3a5f,font-size:16px,font-weight:bold
    style execution fill:#dbeafe,stroke:#93c5fd,color:#1e3a5f,font-size:16px,font-weight:bold
    style infra fill:#dbeafe,stroke:#93c5fd,color:#1e3a5f,font-size:16px,font-weight:bold
    style k8s fill:#f5f3ff,stroke:#7c3aed,stroke-width:2px,color:#4c1d95,font-size:18px,font-weight:bold
```

---

## ✨ Features

**Sandboxing & Isolation**
- **K8s-native sandboxing** — Every agent runs in its own Kubernetes Job with PVC-backed filesystem, resource limits, and network policies enforcing strict isolation between groups
- **Credential proxy** — A dedicated HTTP reverse proxy injects API keys into agent requests, locked down by network policy so only agent pods can reach it

**Messaging & Routing**
- **Multi-channel messaging** — Pluggable channel system with a registry pattern, shipping Discord and Telegram support out of the box
- **Per-group queuing** — NATS JetStream per-group WorkQueue streams ensure ordered delivery and prevent cross-group interference
- **NATS JetStream IPC** — Real-time bidirectional agent communication over durable consumers with exactly-once delivery semantics

**Scheduling & Control**
- **Scheduled tasks** — Cron, interval, and one-time task scheduling with drift-proof anchoring and persistent state across restarts
- **Sender allowlist** — Per-group access control stored in MySQL, restricting who can interact with each agent group
- **gRPC API** — Five services (Admin, Group, Task, Channel, Sandbox) exposed over gRPC with a REST gateway for flexibility

**Observability**
- **TUI dashboard** — Real-time Bubbletea-based monitoring of sandboxes, groups, tasks, and system events over gRPC
- **Prometheus metrics** — Built-in `/metrics` endpoint with health probes (`/healthz`, `/readyz`) for production readiness

**CI/CD**
- **GitHub Actions CI** — Split unit and Docker-based integration test workflows gate container builds with race detection on both test stages

---

## 🚀 Quick Start

### 📋 Prerequisites

- Go 1.26+
- MySQL 8+
- NATS Server 2.10+ with JetStream enabled
- Kubernetes cluster with client access
- Docker Engine (for Docker-based integration tests)

### 🔧 Build

```bash
make build          # Server binary
make build-tui      # TUI client
make test           # Run all tests
```

### ⚙️ Configure

Kraclaw is configured entirely via environment variables:

<details>
<summary>Environment variables reference</summary>

**Core**

| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `MYSQL_DSN` | Yes | — | MySQL connection string |
| `NATS_URL` | No | `nats://localhost:4222` | NATS JetStream server URL |
| `K8S_NAMESPACE` | No | `kraclaw` | Kubernetes namespace for Jobs |

**Agent images** (at least one required, paired with the matching provider key)

| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `AGENT_IMAGE_ANTHROPIC` | Conditional | — | Container image for Anthropic agent pods |
| `AGENT_IMAGE_OPENAI` | Conditional | — | Container image for OpenAI agent pods |

**gRPC / TLS**

| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `GRPC_ADDR` | No | `:50051` | gRPC listen address |
| `REST_ADDR` | No | `:8080` | REST gateway listen address |
| `GRPC_INSECURE` | No | `false` | Disable mTLS (dev only) |

**Credential proxy** (at least one provider key required)

| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `PROXY_ADDR` | No | `:3001` | Credential proxy listen address |
| `ANTHROPIC_API_KEY` | Conditional | — | Anthropic API key injected by credential proxy |
| `OPENAI_API_KEY` | Conditional | — | OpenAI API key injected by credential proxy |
| `CREDENTIAL_ENCRYPTION_KEY` | Conditional | — | 64-hex-char key for multi-provider credential store (required for OpenAI-only setups) |

**Queue / limits / scheduler**

| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `MAX_CONCURRENT` | No | `5` | Max concurrent sandbox Jobs |
| `IDLE_TIMEOUT` | No | `30m` | Sandbox idle timeout |
| `MAX_RETRIES` | No | `5` | Max retries per queued message |
| `SCHEDULER_POLL_INTERVAL` | No | `60s` | Scheduler tick interval |

**Channels / metrics / logging**

| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `DISCORD_TOKEN` | No | — | Discord bot token |
| `TELEGRAM_TOKEN` | No | — | Telegram bot token |
| `ASSISTANT_NAME` | No | `Kraclaw` | Bot display name |
| `TZ` | No | `UTC` | Timezone for message formatting |
| `METRICS_ENABLED` | No | `true` | Expose `/metrics` endpoint |
| `LOG_LEVEL` | No | `info` | Log level (debug, info, warn, error) |
| `LOG_FORMAT` | No | `json` | Log format (json, text) |

See `internal/config/config.go` for the full list, including TLS paths, CIDR allowlists, and tuning knobs.

</details>

### ▶️ Run

```bash
# Local development
export MYSQL_DSN="user:pass@tcp(localhost:3306)/kraclaw?parseTime=true"
export NATS_URL="nats://localhost:4222"
export AGENT_IMAGE_ANTHROPIC="registry.local/agent-anthropic:latest"
export AGENT_IMAGE_OPENAI="registry.local/agent-openai:latest"
make run

# TUI client
./kraclaw-tui --server localhost:50051
```

### ☸️ Deploy to Kubernetes

```bash
# Build and push container image
make docker-build docker-push

# Install via Helm (see helm/README.md for full configuration)
helm install kraclaw ./helm -n kraclaw --create-namespace -f helm/values-prod.yaml
```

---

## 📁 Project Structure

```
kraclaw/
├── cmd/
│   ├── kraclaw/              # Server binary
│   └── kraclaw-tui/          # TUI client binary
├── internal/
│   ├── config/               # envconfig-based configuration
│   ├── server/               # gRPC server + REST gateway
│   ├── store/                # Store interfaces + MySQL implementation
│   ├── queue/                # NATS JetStream per-group message queue
│   ├── ipc/                  # NATS JetStream IPC broker
│   ├── sandbox/              # K8s Job lifecycle controller
│   ├── channel/              # Channel interface + registry
│   │   ├── discord/          # Discord bot
│   │   ├── telegram/         # Telegram bot
│   │   └── tui/              # TUI channel adapter
│   ├── provider/             # LLM provider registry (Anthropic, OpenAI)
│   ├── router/               # Message formatting + outbound routing
│   ├── auth/                 # Sender allowlist
│   ├── scheduler/            # Task scheduler (cron/interval/once)
│   ├── credproxy/            # Credential proxy
│   ├── orchestrator/         # Top-level wiring + message loop
│   └── metrics/              # Prometheus metrics
├── agent/                    # TypeScript agent (runs inside K8s Jobs)
├── proto/kraclaw/v1/         # Protobuf service definitions
├── migrations/               # MySQL migrations (golang-migrate)
├── integration/              # Docker-based backend integration tests
├── helm/                     # Helm chart for Kubernetes deployment
├── argocd/                   # ArgoCD application manifest
├── .github/workflows/        # CI/CD pipeline
├── Dockerfile
└── Makefile
```

---

## 🔌 Ports

| Port | Protocol | Purpose |
|------|----------|---------|
| 50051 | gRPC | TUI client and programmatic access |
| 8080 | HTTP | REST gateway, health probes (`/healthz`, `/readyz`), metrics |
| 3001 | HTTP | Credential proxy (agent pods only, network policy restricted) |

---

## 🧪 Testing

```bash
make test           # All tests (unit + integration)
make test-short     # Unit tests only
make test-integration # Integration tests only (Docker required)
```

- **NATS JetStream tests** use an embedded [`nats-server/v2`](https://github.com/nats-io/nats-server) (in-process)
- **MySQL tests** use [go-sqlmock](https://github.com/DATA-DOG/go-sqlmock)
- **K8s tests** use [client-go/kubernetes/fake](https://pkg.go.dev/k8s.io/client-go/kubernetes/fake)
- **Backend integration tests** use [dockertest](https://github.com/ory/dockertest) to spin up an ephemeral **MySQL 8.0** container, paired with an embedded NATS JetStream server (no Docker required for NATS)

### 🐳 Docker-based Integration Tests

Kraclaw includes backend integration tests under `integration/` that validate real MySQL behavior (via dockertest) alongside an embedded NATS JetStream server.

**Prerequisites**
- Docker Engine installed and running (`docker version` succeeds)
- Ability to pull images from Docker Hub (`mysql:8.0`)

**Run integration tests**

```bash
# Integration tests only
make test-integration

# Or run all tests (includes integration)
make test
```

Notes:
- Integration tests are skipped automatically when running with `-short` (for example via `make test-short`)
- If Docker is unavailable, integration tests are skipped rather than failing the whole test run
- GitHub Actions runs both unit tests (`go test -short`) and Docker-based integration tests on pushes and pull requests to `main`

---

## 📜 License

[Apache License 2.0](LICENSE)
