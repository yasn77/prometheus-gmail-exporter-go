# prometheus-gmail-exporter-go

A Helm chart for deploying the [Prometheus Gmail Exporter](https://github.com/yasn77/prometheus-gmail-exporter-go) on Kubernetes.

## Prerequisites

- Kubernetes >= 1.25
- Helm >= 3.8
- Prometheus Operator (optional, required for `serviceMonitor` and `prometheusRule`)

## Install

Create the namespace and secrets first:

```bash
kubectl create namespace monitoring
kubectl create secret generic gmail-credentials \
  --from-file=credentials.json=./credentials.json \
  --namespace monitoring
kubectl create secret generic gmail-token \
  --from-file=token.json=./token.json \
  --namespace monitoring
```

Install the chart:

```bash
helm install prometheus-gmail-exporter-go \
  ./helm/prometheus-gmail-exporter-go \
  --namespace monitoring \
  --set gmailCredentials.existingSecret=gmail-credentials \
  --set gmailToken.existingSecret=gmail-token \
  --set serviceMonitor.enabled=true
```

## Upgrade

```bash
helm upgrade prometheus-gmail-exporter-go \
  ./helm/prometheus-gmail-exporter-go \
  --namespace monitoring \
  --set gmailCredentials.existingSecret=gmail-credentials \
  --set gmailToken.existingSecret=gmail-token
```

## Uninstall

```bash
helm uninstall prometheus-gmail-exporter-go --namespace monitoring
```

## Values

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `replicaCount` | int | `1` | Number of replicas |
| `image.repository` | string | `"ghcr.io/yasn77/prometheus-gmail-exporter-go"` | Image repository |
| `image.tag` | string | `""` | Image tag (defaults to chart appVersion) |
| `image.pullPolicy` | string | `"IfNotPresent"` | Image pull policy |
| `serviceAccount.create` | bool | `true` | Create a service account |
| `podSecurityContext` | object | `runAsNonRoot: true`, `runAsUser: 65532`, `fsGroup: 65532` | Pod security context |
| `securityContext` | object | `readOnlyRootFilesystem: true`, `allowPrivilegeEscalation: false`, `capabilities: {drop: [ALL]}` | Container security context |
| `service.type` | string | `"ClusterIP"` | Service type |
| `service.port` | int | `2112` | Service port |
| `resources` | object | See `values.yaml` | CPU/memory resources |
| `config.interval` | int | `300` | Scraping interval in seconds |
| `config.labels` | list | `["INBOX"]` | Gmail labels to monitor |
| `gmailCredentials.existingSecret` | string | `""` | Existing secret with `credentials.json` |
| `gmailToken.existingSecret` | string | `""` | Existing secret with `token.json` |
| `serviceMonitor.enabled` | bool | `false` | Create a ServiceMonitor CRD |
| `prometheusRule.enabled` | bool | `false` | Create a PrometheusRule CRD |
| `livenessProbe` | object | See `values.yaml` | Liveness probe configuration |
| `readinessProbe` | object | See `values.yaml` | Readiness probe configuration |

## Security

This chart defaults to a secure, non-root configuration:

- Container runs as UID `65532` (distroless non-root)
- Read-only root filesystem
- All Linux capabilities are dropped
- Privilege escalation is disabled
- Gmail credentials and tokens are expected to be supplied via Kubernetes Secrets (never inline in production)
