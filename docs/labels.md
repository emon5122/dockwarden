# Container Labels

DockWarden supports per-container configuration using Docker labels.

## General Labels

| Label | Values | Default | Description |
|-------|--------|---------|-------------|
| `dockwarden.enable` | `true`/`false` | - | Enable/disable management |
| `dockwarden.scope` | `<string>` | - | Scope identifier |
| `dockwarden.stop-signal` | `SIGTERM`/`SIGKILL`/etc | `SIGTERM` | Stop signal |
| `dockwarden.stop-timeout` | `<seconds>` | `10` | Stop timeout |

## Update Labels

| Label | Values | Default | Description |
|-------|--------|---------|-------------|
| `dockwarden.update.enable` | `true`/`false` | `true` | Enable auto-updates |
| `dockwarden.update.pre-hook` | `<command>` | - | Pre-update hook |
| `dockwarden.update.post-hook` | `<command>` | - | Post-update hook |

## Health Labels

| Label | Values | Default | Description |
|-------|--------|---------|-------------|
| `dockwarden.watch.enable` | `true`/`false` | `true` | Enable health watching |
| `dockwarden.watch.action` | `restart`/`notify` | `restart` | Action on unhealthy |
| `dockwarden.watch.max-restarts` | `<number>` | `5` | Max restart attempts |

## Dependency Labels

| Label | Values | Description |
|-------|--------|-------------|
| `dockwarden.depends-on` | `<container-name>` | Update/restart after dependency |

## Examples

### Enable DockWarden for specific containers

```yaml
# docker-compose.yml
services:
  webapp:
    image: myapp:latest
    labels:
      - "dockwarden.enable=true"
```

### Disable updates but keep health monitoring

```yaml
services:
  database:
    image: postgres:15
    labels:
      - "dockwarden.enable=true"
      - "dockwarden.update.enable=false"
      - "dockwarden.watch.enable=true"
```

### Custom stop timeout for graceful shutdown

```yaml
services:
  api:
    image: myapi:latest
    labels:
      - "dockwarden.enable=true"
      - "dockwarden.stop-timeout=60"
      - "dockwarden.stop-signal=SIGTERM"
```

### Using scopes for different update schedules

```yaml
services:
  # Production containers - update weekly
  prod-app:
    image: myapp:latest
    labels:
      - "dockwarden.enable=true"
      - "dockwarden.scope=production"

  # Dev containers - update daily  
  dev-tools:
    image: devtools:latest
    labels:
      - "dockwarden.enable=true"
      - "dockwarden.scope=development"
```

Run separate DockWarden instances for each scope:
```bash
# Production updates (weekly)
docker run -e DOCKWARDEN_SCOPE=production -e DOCKWARDEN_SCHEDULE="0 3 * * 0" ...

# Development updates (every 6 hours)
docker run -e DOCKWARDEN_SCOPE=development -e DOCKWARDEN_SCHEDULE="0 */6 * * *" ...
```

### Health monitoring only (notify, don't restart)

```yaml
services:
  critical-service:
    image: important:latest
    labels:
      - "dockwarden.enable=true"
      - "dockwarden.update.enable=false"
      - "dockwarden.watch.enable=true"
      - "dockwarden.watch.action=notify"
```

### Container dependencies

```yaml
services:
  database:
    image: postgres:15
    labels:
      - "dockwarden.enable=true"
      
  api:
    image: myapi:latest
    depends_on:
      - database
    labels:
      - "dockwarden.enable=true"
      - "dockwarden.depends-on=database"
```
