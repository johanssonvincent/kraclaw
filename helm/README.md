# Kraclaw Helm Chart

Helm chart for deploying Kraclaw -- a K8s-native AI agent orchestrator.

Chart version: 0.1.0
App version: 0.6

## Prerequisites

- Kubernetes 1.23+
- Helm 4.1.3+
- StorageClass available in cluster (default: `longhorn`)
- MySQL 5.7+ accessible from cluster
- Redis 5.0+ accessible from cluster

## Pre-install: Create Required Secrets

These Secrets must exist before running `helm install`:

### MySQL DSN

    kubectl create secret generic kraclaw-mysql-url \
      --from-literal=MYSQL_DSN='mysql://user:pass@mysql:3306/kraclaw' \
      -n kraclaw

### Anthropic API Keys

    kubectl create secret generic kraclaw-api-keys \
      --from-literal=ANTHROPIC_API_KEY='sk-ant-...' \
      -n kraclaw

### gRPC TLS Certificates (required unless grpcInsecure=true)

    kubectl create secret generic kraclaw-grpc-tls \
      --from-file=tls.crt=tls.crt \
      --from-file=tls.key=tls.key \
      --from-file=ca.crt=ca.crt \
      -n kraclaw

## Installation

    # Install with default values
    helm install kraclaw ./helm \
      --namespace kraclaw \
      --create-namespace \
      --set k8s.agentImage=ghcr.io/johanssonvincent/kraclaw-agent:0.6

    # Install with custom values file
    helm install kraclaw ./helm \
      --namespace kraclaw \
      --create-namespace \
      -f values-prod.yaml

## Upgrade

    helm upgrade kraclaw ./helm --namespace kraclaw -f values-prod.yaml

## Key Values Reference

| Value | Default | Description |
|-------|---------|-------------|
| `k8s.agentImage` | `""` | REQUIRED: agent container image (AGENT_IMAGE) |
| `image.tag` | Chart appVersion | Server image tag |
| `replicaCount` | `1` | Number of kraclaw replicas |
| `server.grpcInsecure` | `false` | Set true only for local dev (disables TLS) |
| `tls.secretName` | `kraclaw-grpc-tls` | Secret containing gRPC TLS certificates |
| `mysql.secretName` | `kraclaw-mysql-url` | Secret containing MYSQL_DSN |
| `credproxy.secretName` | `kraclaw-api-keys` | Secret containing ANTHROPIC_API_KEY |
| `redis.url` | `redis://redis.databases.svc.cluster.local:6379` | Redis connection URL |
| `storage.class` | `longhorn` | StorageClass for PVCs |
| `storage.groups.size` | `10Gi` | Size of groups PVC |
| `storage.sessions.size` | `5Gi` | Size of sessions PVC |
| `storage.data.size` | `1Gi` | Size of data PVC |
| `rbac.create` | `true` | Create Role and RoleBinding |
| `serviceAccount.create` | `true` | Create ServiceAccount |
| `networkPolicy.credProxy.enabled` | `true` | Restrict credproxy egress to TCP 443 |
| `secrets.create` | `false` | Create Secrets from values (dev/test only) |

## Secret Management

Sensitive values (MYSQL_DSN, ANTHROPIC_API_KEY, ANTHROPIC_OAUTH_TOKEN) are NOT stored in values.yaml.
They must be pre-created as Kubernetes Secrets. For dev/test only, set `secrets.create=true`
and provide `secrets.mysqlDsn` and `secrets.anthropicApiKey` via `--set` flags.

Never commit values files containing real credentials.

## Migrating from Kustomize

The legacy `deploy/` Kustomize directory has been removed. Helm is now the sole deployment method.

If you previously deployed with Kustomize:

1. Create required Secrets (see Pre-install above)
2. Remove Kustomize-managed resources: `kubectl delete -k deploy/` (from a checkout before removal)
3. Install via Helm: `helm install kraclaw ./helm -n kraclaw -f values-prod.yaml`

Note: PVCs are preserved across re-deploys; Helm will not delete them on `helm uninstall`.

## Uninstall

    helm uninstall kraclaw --namespace kraclaw
    # PVCs are NOT automatically deleted -- to delete storage:
    kubectl delete pvc -l app.kubernetes.io/name=kraclaw -n kraclaw
