# DockWarden Configuration

This document covers all configuration options for DockWarden.

## Environment Variables

### Operation Mode

| Variable | Default | Description |
|----------|---------|-------------|
| `DOCKWARDEN_MODE` | `full` | Operation mode |
| `DOCKWARDEN_RUN_ONCE` | `false` | Run once and exit |
| `DOCKWARDEN_INTERVAL` | `1m` | Check interval (e.g., `1m`, `5m`, `1h`) |
| `DOCKWARDEN_SCHEDULE` | - | Cron expression (overrides interval) |

**Modes:**
- `full` - Both update and health monitoring
- `update` - Update checking only
- `watch` - Health monitoring only
- `monitor` - Read-only monitoring

### Update Settings

| Variable | Default | Description |
|----------|---------|-------------|
| `DOCKWARDEN_CLEANUP` | `true` | Remove old images after update |
| `DOCKWARDEN_NO_RESTART` | `false` | Only pull images, don't restart |
| `DOCKWARDEN_NO_PULL` | `false` | Don't pull new images |
| `DOCKWARDEN_MONITOR_ONLY` | `false` | Monitor mode, no changes |
| `DOCKWARDEN_ROLLING_RESTART` | `false` | Restart containers one at a time |
| `DOCKWARDEN_STOP_TIMEOUT` | `10s` | Container stop timeout |

### Container Selection

| Variable | Default | Description |
|----------|---------|-------------|
| `DOCKWARDEN_LABEL_ENABLE` | `false` | Only manage labeled containers |
| `DOCKWARDEN_LABEL_NAME` | `dockwarden.enable` | Label to check |
| `DOCKWARDEN_SCOPE` | - | Limit to containers with matching scope |
| `DOCKWARDEN_DISABLE_CONTAINERS` | - | Comma-separated list of containers to skip |
| `DOCKWARDEN_INCLUDE_STOPPED` | `false` | Include stopped containers |
| `DOCKWARDEN_INCLUDE_RESTARTING` | `false` | Include restarting containers |

### Health Monitoring

| Variable | Default | Description |
|----------|---------|-------------|
| `DOCKWARDEN_HEALTH_WATCH` | `true` | Enable health monitoring |
| `DOCKWARDEN_HEALTH_ACTION` | `restart` | Action on unhealthy: `restart`, `notify` |

### Secrets (Docker Secrets Support)

| Variable | Description |
|----------|-------------|
| `DOCKWARDEN_REGISTRY_SECRET` | Path to registry auth secret file |
| `DOCKWARDEN_REGISTRY_SECRET_FILE` | Alternative path (auto-read) |
| `DOCKWARDEN_NOTIFICATION_URL` | Notification webhook URL |
| `DOCKWARDEN_NOTIFICATION_URL_FILE` | Path to URL secret file |
| `DOCKWARDEN_API_TOKEN` | API authentication token |
| `DOCKWARDEN_API_TOKEN_FILE` | Path to token secret file |

### API Settings

| Variable | Default | Description |
|----------|---------|-------------|
| `DOCKWARDEN_API_ENABLED` | `false` | Enable REST API |
| `DOCKWARDEN_API_PORT` | `8080` | API listen port |
| `DOCKWARDEN_METRICS` | `false` | Enable Prometheus metrics |

### Logging

| Variable | Default | Description |
|----------|---------|-------------|
| `DOCKWARDEN_LOG_LEVEL` | `info` | Log level: debug/info/warn/error |
| `DOCKWARDEN_LOG_FORMAT` | `auto` | Log format: auto/json/pretty |
| `TZ` | `Asia/Dhaka` | Timezone for logging |

## Cron Schedule Examples

```bash
# Every 5 minutes
DOCKWARDEN_SCHEDULE="*/5 * * * *"

# Every hour at :00
DOCKWARDEN_SCHEDULE="0 * * * *"

# Daily at 4:00 AM
DOCKWARDEN_SCHEDULE="0 4 * * *"

# Every Sunday at 3:00 AM
DOCKWARDEN_SCHEDULE="0 3 * * 0"

# First day of month at 2:00 AM
DOCKWARDEN_SCHEDULE="0 2 1 * *"
```

## Docker Secrets

DockWarden natively supports Docker secrets for secure credential management:

### Creating Secrets

```bash
# Create registry auth secret
echo '{"auths":{"https://index.docker.io/v1/":{"auth":"base64encoded"}}}' | \
  docker secret create registry_auth -

# Create from file
docker secret create registry_auth ./registry-auth.json
```

### Using Secrets in Compose

```yaml
services:
  dockwarden:
    image: emon5122/dockwarden:latest
    secrets:
      - registry_auth
    environment:
      - DOCKWARDEN_REGISTRY_SECRET=/run/secrets/registry_auth

secrets:
  registry_auth:
    external: true
```

### Secret File Format

The registry auth secret should be in Docker config.json format:

```json
{
  "auths": {
    "https://index.docker.io/v1/": {
      "auth": "base64_encoded_username:password"
    },
    "ghcr.io": {
      "auth": "base64_encoded_username:token"
    }
  }
}
```

Generate the auth string with:
```bash
echo -n 'username:password' | base64
```
