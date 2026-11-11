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
| `dockwarden.compose` | `true`/`false` | `true` | `false` keeps a compose container on the legacy roll (see [Compose rolls](#compose-rolls)) |

DockWarden also writes one label of its own to containers it recreates:
`dockwarden.env-overrides` records which environment variables were genuine
runtime overrides, so the next update can refresh image-provided defaults
without touching your overrides. It is internal bookkeeping — don't set it
yourself.

## Compose rolls

A container that `docker compose up` created (it carries the
`com.docker.compose.project`, `.service`, `.project.config_files` and
`.project.working_dir` labels) is rolled **through its compose project**, so a
release also applies whatever changed in the deployed compose file: labels
(`dockwarden.stop-timeout`, ...), `stop_grace_period`, `environment` /
`env_file`, `mem_limit` / `cpus`, volumes. No manual `docker compose up -d`
after a release.

How a roll works:

1. A short-lived helper container (DockWarden's own image, which ships
   `docker-compose`) renders the project with `docker compose config` from the
   files at their host paths, mounted read-only at the same paths (plus the
   docker socket). The env file named by `--env-file` at the original `up`
   (`com.docker.compose.project.environment_file`) is used again.
2. If the rendered image is not the image DockWarden just pulled, or a variable
   renders blank (the original `up` probably took it from its shell), or the
   files cannot be read, DockWarden falls back to the legacy roll: recreate from
   the old container's config, as before.
3. DockWarden stops the container itself, with the **longer** of the old and the
   new stop timeout (`dockwarden.stop-timeout` label or `stop_grace_period`) and
   the `dockwarden.stop-signal` label, so a drain is never cut short.
4. The helper runs `docker compose up -d --no-deps --force-recreate --pull never
   --no-build <service>`. If that fails while the old container still exists,
   DockWarden recreates and starts it the legacy way.

Only running containers take this path (stopped ones keep the legacy
semantics), only one roll runs per service per pass (replicas are recreated by
compose together), and DockWarden must itself run in a container with the
docker socket bind-mounted (its image is the helper image; behind a socket proxy
rolls stay legacy). Turn it off with `DOCKWARDEN_COMPOSE_ROLL=false`, or per
container with the label `dockwarden.compose=false`. Helper containers carry the
`dockwarden.helper` label and are removed after each run. End-to-end check on a
real daemon: `test/e2e/compose-roll.sh`.

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
