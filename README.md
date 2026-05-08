# Prometheus Gmail Exporter (Go)

A Prometheus exporter that exposes Gmail label metrics (total threads and unread threads) via the Gmail API.

Heavily inspired by [jamesread/prometheus-gmail-exporter](https://github.com/jamesread/prometheus-gmail-exporter), but written in Go instead of Python.

## Table of Contents

- [Overview](#overview)
- [Features](#features)
- [Metrics](#metrics)
- [Prerequisites](#prerequisites)
- [Configuration](#configuration)
- [Running Locally](#running-locally)
- [Running with Docker](#running-with-docker)
- [Running with Docker Compose](#running-with-docker-compose)
- [Running on Kubernetes](#running-on-kubernetes)
- [GCP Setup](#gcp-setup)
- [Security Considerations](#security-considerations)
- [Development](#development)
- [License](#license)

## Overview

This exporter connects to the Gmail API, queries the labels you configure, and exposes the following data as Prometheus metrics:

- Total number of threads per label
- Number of unread threads per label

Metrics are scraped by Prometheus from the `/metrics` endpoint and can be visualised in Grafana.

## Features

- Prometheus metrics endpoint on port `2112`
- OAuth2 token refresh handling with background token lifecycle management
- Secure token storage via environment variables or mounted secrets
- HTTP server timeouts (Slowloris protection)
- Rate limiting on the `/metrics` endpoint
- Health (`/health`) and readiness (`/ready`) endpoints
- Configurable via YAML and environment variables
- Go 1.26
- Docker image built with a distroless non-root base image

## Metrics

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `gmail_threads_total` | Gauge | `Label` | Total number of threads per label |
| `gmail_threads_unread` | Gauge | `Label` | Number of unread threads per label |
| `gmail_scrape_errors_total` | Counter | none | Total number of Gmail scrape errors |
| `gmail_scrape_success_total` | Counter | none | Total number of successful Gmail scrapes |

Example:

```
gmail_threads_total{Label="gmail_inbox"} 42
gmail_threads_unread{Label="gmail_inbox"} 5
```

## Prerequisites

- A Google Cloud Platform (GCP) project with the Gmail API enabled
- OAuth2 Desktop Application credentials (Client ID and Client Secret)
- [mise](https://mise.jdx.dev/) (optional, for development)

## Configuration

### Config File

The exporter reads `config.yml` by default (override with `CONFIG_PATH`):

```yaml
---
interval: 300
labels:
  - INBOX
```

- `interval` - scrape interval in seconds (must be greater than 0)
- `labels` - list of Gmail label names to monitor (must not be empty)

### Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `CONFIG_PATH` | `config.yml` | Path to the configuration file |
| `GMAIL_CREDENTIALS_PATH` | `credentials.json` | Path to the GCP OAuth2 credentials file |
| `GMAIL_OAUTH_TOKEN` | none | OAuth2 token JSON string (for secrets management) |
| `TOKEN_SECRET_PATH` | none | Path to a mounted token secret file |
| `LISTEN_ADDRESS` | `127.0.0.1:2112` | Bind address for the HTTP server |
| `HTTP_READ_TIMEOUT` | `15s` | HTTP server read timeout |
| `HTTP_WRITE_TIMEOUT` | `15s` | HTTP server write timeout |
| `HTTP_IDLE_TIMEOUT` | `60s` | HTTP server idle timeout |

## Running Locally

### With mise

```bash
# Run tests
mise run test

# Build the binary
mise run build

# Run the application
mise run run

# CI-style verification
mise run check
```

### Without mise

```bash
go test -race -coverprofile=coverage.out ./...
go build -o prometheus-gmail-exporter-go ./...
./prometheus-gmail-exporter-go
```

Ensure `credentials.json` and `config.yml` are present in the working directory.

## Running with Docker

Build and run the container:

```bash
docker build -t prometheus-gmail-exporter-go .
docker run -p 127.0.0.1:2112:2112 \
  -v $(pwd)/config.yml:/app/config.yml:ro \
  -v $(pwd)/credentials.json:/app/credentials.json:ro \
  -e GMAIL_CREDENTIALS_PATH=/app/credentials.json \
  -e CONFIG_PATH=/app/config.yml \
  prometheus-gmail-exporter-go
```

The image uses a distroless non-root base with a read-only root filesystem.

## Running with Docker Compose

The repository includes a `docker-compose.yml` that starts the exporter alongside Prometheus and Grafana:

```bash
docker compose up
```

Services:

- **prometheus-gmail-exporter** on port `2112`
- **Prometheus** on port `9090`
- **Grafana** on port `3000`

Place your `credentials.json` in a `secrets/` directory and your `config.yml` in a `config/` directory before starting.

## Running on Kubernetes

Deploy using the included Helm chart. First create a Secret containing both `credentials.json` and `token.json`:

```bash
kubectl create secret generic gmail-auth \
  --from-file=credentials.json=./credentials.json \
  --from-file=token.json=./token.json
```

Then install the chart:

```bash
helm install prometheus-gmail-exporter ./helm/prometheus-gmail-exporter-go \
  --set gmail.existingSecret.name=gmail-auth
```

See the [Helm chart README](./helm/prometheus-gmail-exporter-go/README.md) for full configuration options.

## GCP Setup

1. Create a project in the [Google Cloud Console](https://console.cloud.google.com/).
2. Enable the **Gmail API**.
3. Go to **APIs & Services > Credentials** and create an **OAuth2 Desktop Application**.
4. Download the client credentials as `credentials.json`.
5. Run the exporter locally once to perform the interactive OAuth flow. The exporter will prompt you to visit an authorisation URL and paste the authorisation code.
6. After authorisation, the token is saved to `token.json`.
7. For production deployments, use the `GMAIL_OAUTH_TOKEN` environment variable or a mounted secret via `TOKEN_SECRET_PATH` instead of the local `token.json` file.

## Security Considerations

- **Do NOT commit `credentials.json` or `token.json` to version control.** Both are listed in `.gitignore`.
- Use `GMAIL_OAUTH_TOKEN` or `TOKEN_SECRET_PATH` for production token management instead of file-based storage.
- The default `LISTEN_ADDRESS` binds to `127.0.0.1` to prevent accidental external exposure.
- The Docker container runs as a non-root user (`nonroot:nonroot`).
- The container root filesystem is read-only.

## Development

- Go 1.26 is required.
- [mise](https://mise.jdx.dev/) is used for tool and task management.
- Run CI-style checks:

  ```bash
  mise run check
  ```

- Run tests with race detection and coverage:

  ```bash
  go test -race -coverprofile=coverage.out ./...
  ```

## License

This project inherits the licence from the original [prometheus-gmail-exporter](https://github.com/jamesread/prometheus-gmail-exporter) project.
