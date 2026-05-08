# prometheus-gmail-exporter-go

A Helm chart for deploying the [Prometheus Gmail Exporter](https://github.com/yasn77/prometheus-gmail-exporter-go) on Kubernetes.

## Prerequisites

- Kubernetes >= 1.25
- Helm >= 3.8
- Prometheus Operator (optional, required for `serviceMonitor` and `prometheusRule`)

## Install

Create a single Kubernetes Secret containing both `credentials.json` and `token.json`:

```bash
kubectl create namespace monitoring
kubectl create secret generic gmail-auth \
  --from-file=credentials.json=./credentials.json \
  --from-file=token.json=./token.json \
  --namespace monitoring
```

Install the chart, referencing the existing secret:

```bash
helm install prometheus-gmail-exporter-go \
  ./helm/prometheus-gmail-exporter-go \
  --namespace monitoring \
  --set gmail.existingSecret.name=gmail-auth \
  --set serviceMonitor.enabled=true
```

## Upgrade

```bash
helm upgrade prometheus-gmail-exporter-go \
  ./helm/prometheus-gmail-exporter-go \
  --namespace monitoring \
  --set gmail.existingSecret.name=gmail-auth
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
| `gmail.existingSecret.name` | string | `""` | Existing secret containing both credentials and token |
| `gmail.existingSecret.credentialsKey` | string | `"credentials.json"` | Key in the secret for credentials.json |
| `gmail.existingSecret.tokenKey` | string | `"token.json"` | Key in the secret for token.json |
| `gmail.credentials.clientId` | string | `nil` | Inline OAuth2 client ID (not recommended) |
| `gmail.credentials.clientSecret` | string | `nil` | Inline OAuth2 client secret (not recommended) |
| `gmail.token.tokenJson` | string | `nil` | Inline OAuth2 token JSON (not recommended) |
| `serviceMonitor.enabled` | bool | `false` | Create a ServiceMonitor CRD |
| `prometheusRule.enabled` | bool | `false` | Create a PrometheusRule CRD with default alerts |
| `livenessProbe` | object | See `values.yaml` | Liveness probe configuration |
| `readinessProbe` | object | See `values.yaml` | Readiness probe configuration |

### Default Alerts

When `prometheusRule.enabled=true`, the chart creates a `PrometheusRule` with the following alerts:

| Alert | Severity | Description |
|-------|----------|-------------|
| `GmailExporterDown` | critical | Gmail API connectivity lost |
| `GmailTokenExpiringSoon` | warning | Access token expires within 5 minutes |
| `GmailTokenRefreshFailed` | warning | Token refresh errors in the last hour |
| `GmailScrapeStale` | critical | No successful scrape in 10 minutes |

## Security

This chart defaults to a secure, non-root configuration:

- Container runs as UID `65532` (distroless non-root)
- Read-only root filesystem
- All Linux capabilities are dropped
- Privilege escalation is disabled
- Gmail credentials and tokens are expected to be supplied via Kubernetes Secrets (never inline in production)
