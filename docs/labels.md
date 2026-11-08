# Container Labels

DockWarden supports per-container configuration using Docker labels.

## General Labels

| Label | Values | Default | Description |
|-------|--------|---------|-------------|
| `dockwarden.enable` | `true`/`false` | - | Enable/disable management |
| `dockwarden.scope` | `<string>` | - | Scope identifier |
| `dockwarden.schedule` | cron spec, optional `CRON_TZ=<zone>` prefix | - | Only check/update this container while the current minute matches the spec (see below) |
| `dockwarden.stop-signal` | `SIGTERM`/`SIGQUIT`/etc | container's `STOPSIGNAL` (usually `SIGTERM`) | Stop signal |
| `dockwarden.stop-timeout` | `<seconds>` or duration (`30s`, `1m`) | `10s` | Stop timeout |

DockWarden also writes one label of its own to containers it recreates:
`dockwarden.env-overrides` records which environment variables were genuine
runtime overrides, so the next update can refresh image-provided defaults
without touching your overrides. It is internal bookkeeping — don't set it
yourself.

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


## Per-container update window (`dockwarden.schedule`)

The global pass (`DOCKWARDEN_SCHEDULE` / interval) still decides *when DockWarden looks*; this label decides *whether a given container may be touched in that pass*. The value uses the same cron syntax as `DOCKWARDEN_SCHEDULE` (5 or 6 fields) with an optional `CRON_TZ=<IANA zone>` prefix; without a prefix, DockWarden's own `TZ` applies. The container is checked/updated only if the current minute matches the spec; otherwise it is skipped with an `outside schedule window` log line. Containers without the label behave as before.

```yaml
labels:
  # only recreate between 02:00 and 05:59 Dhaka time
  - "dockwarden.schedule=CRON_TZ=Asia/Dhaka * * 2-5 * * *"
```

Run the global schedule at least once inside the window (e.g. every 10 minutes), otherwise the container never gets a pass. An invalid spec logs a warning on every pass and the container is **skipped** (fail closed). `@every <duration>` descriptors are rejected (they are not wall-clock windows), also fail closed. The window also gates DockWarden's own self-update, and manual/API-triggered passes are window-gated too; a pass that runs past the window end is not interrupted. The label name can be changed with `--label-schedule` / `DOCKWARDEN_LABEL_SCHEDULE`.

> `dockwarden.update.pre-hook` / `post-hook` are listed above but are not implemented in the current code, so they cannot veto an update; use `dockwarden.schedule` or `dockwarden.update.enable=false` instead.
