# DockWarden - Design Document

## Project Overview

**DockWarden** is an open-source Docker container management tool that combines the best features of Watchtower (automated image updates) and Docker Watchdog (health monitoring & auto-restart), while addressing their limitations and security concerns.

### Key Improvements Over Predecessors

| Feature | Watchtower | Docker Watchdog | DockWarden |
|---------|------------|-----------------|------------|
| Image Auto-Update | ✅ | ❌ | ✅ |
| Health Monitoring | Limited | ✅ | ✅ Enhanced |
| Docker Secrets Support | ❌ (requires config.json) | ❌ | ✅ Native |
| Multi-arch Support | ✅ | ✅ | ✅ |
| Active Maintenance | ❌ (Deprecated) | Limited | ✅ |
| Modern Docker API | Outdated | Basic | ✅ Latest |
| Minimal Permissions | ❌ (full socket) | ❌ (full socket) | ✅ Configurable |

---

## Architecture

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                              DockWarden                                      │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                              │
│  ┌──────────────┐  ┌──────────────┐  ┌──────────────┐  ┌──────────────┐    │
│  │   Updater    │  │   Watcher    │  │  Scheduler   │  │   Notifier   │    │
│  │   Module     │  │   Module     │  │   Module     │  │   Module     │    │
│  └──────┬───────┘  └──────┬───────┘  └──────┬───────┘  └──────┬───────┘    │
│         │                 │                 │                 │             │
│  ┌──────┴─────────────────┴─────────────────┴─────────────────┴──────┐     │
│  │                        Core Engine                                 │     │
│  │  ┌─────────────┐  ┌─────────────┐  ┌─────────────┐               │     │
│  │  │   Docker    │  │   Secret    │  │   Config    │               │     │
│  │  │   Client    │  │   Manager   │  │   Manager   │               │     │
│  │  └─────────────┘  └─────────────┘  └─────────────┘               │     │
│  └───────────────────────────────────────────────────────────────────┘     │
│                                    │                                        │
│  ┌─────────────────────────────────┴────────────────────────────────┐      │
│  │                       API Layer                                   │      │
│  │  ┌─────────────┐  ┌─────────────┐  ┌─────────────┐              │      │
│  │  │   REST API  │  │   Metrics   │  │  Health     │              │      │
│  │  │   /v1/*     │  │  /metrics   │  │  /health    │              │      │
│  │  └─────────────┘  └─────────────┘  └─────────────┘              │      │
│  └───────────────────────────────────────────────────────────────────┘      │
│                                                                              │
└──────────────────────────────────────────────────────────────────────────────┘
                                      │
                    ┌─────────────────┼─────────────────┐
                    ▼                 ▼                 ▼
              ┌──────────┐     ┌──────────┐     ┌──────────┐
              │ Docker   │     │ Docker   │     │ Container│
              │ Socket   │     │ Secrets  │     │ Registries│
              │ (R/O opt)│     │          │     │          │
              └──────────┘     └──────────┘     └──────────┘
```

---

## Core Components

### 1. Docker Client Module

The Docker client is responsible for all interactions with the Docker daemon.

**Key Features:**
- Uses latest Docker Engine API (v1.43+)
- Supports both full socket access and limited permissions mode
- Connection pooling and retry logic
- Context-aware operations with timeouts

**Security Improvements:**
```go
// Minimal permissions mode - read-only socket with limited operations
type ClientMode string

const (
    FullAccess    ClientMode = "full"      // Traditional /var/run/docker.sock
    ReadOnly      ClientMode = "readonly"  // Read container state only
    UpdateOnly    ClientMode = "update"    // Can pull images and restart
)
```

### 2. Secret Manager Module

**Docker Secrets Integration** - Native support for Docker secrets instead of mounting config.json

```yaml
# docker-compose.yml example
services:
  dockwarden:
    image: emon5122/dockwarden:latest
    secrets:
      - docker_registry_auth
      - notification_webhook
    environment:
      - DOCKWARDEN_REGISTRY_SECRET=/run/secrets/docker_registry_auth

secrets:
  docker_registry_auth:
    external: true
  notification_webhook:
    external: true
```

**Supported Secret Sources:**
- Docker Swarm secrets (`/run/secrets/*`)
- Environment variables with `_FILE` suffix
- Kubernetes secrets (mounted volumes)
- HashiCorp Vault (optional integration)

### 3. Updater Module

Handles automatic container image updates.

**Features:**
- Digest-based update detection (not just tag changes)
- Rolling updates with configurable strategies
- Pre/post update hooks
- Rollback capability
- Rate limiting for registry API calls

**Update Strategies:**
```yaml
update:
  strategy: rolling    # rolling | immediate | scheduled
  batch_size: 2        # For rolling updates
  delay: 30s           # Delay between batches
  rollback_on_failure: true
```

### 4. Watcher Module (from Watchdog)

Monitors container health and automatically restarts unhealthy containers.

**Features:**
- Health status monitoring via Docker health checks
- Configurable restart policies (default: 5 attempts max)
- Dead container detection
- Resource usage monitoring (optional)
- Container dependency awareness
- Concurrent processing using goroutines

**Health Actions:**
```yaml
watch:
  interval: 10s
  actions:
    unhealthy:
      action: restart
      max_restarts: 5
      cooldown: 60s
    dead:
      action: notify
```

### 5. Scheduler Module

Manages timing for update checks and health monitoring.

**Features:**
- Cron-style scheduling
- Jitter support for distributed deployments
- Timezone awareness
- Maintenance windows

### 6. Notifier Module

Sends notifications for events.

**Supported Channels:**
- Discord
- Slack
- Telegram
- Email (SMTP)
- Webhooks (generic)
- Gotify
- Ntfy
- Pushover

---

## Configuration

### Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `DOCKWARDEN_MODE` | `full` | Operation mode: `full`, `update`, `watch`, `monitor` |
| `DOCKWARDEN_INTERVAL` | `1m` | Check interval (e.g., `1m`, `5m`, `1h`) |
| `DOCKWARDEN_SCHEDULE` | - | Cron expression (overrides interval) |
| `DOCKWARDEN_CLEANUP` | `true` | Remove old images after update |
| `DOCKWARDEN_LABEL_ENABLE` | `false` | Only update containers with label |
| `DOCKWARDEN_LABEL_NAME` | `dockwarden.enable` | Label to look for |
| `DOCKWARDEN_SCOPE` | - | Limit to containers with matching scope |
| `DOCKWARDEN_NO_RESTART` | `false` | Only pull images, don't restart |
| `DOCKWARDEN_MONITOR_ONLY` | `false` | Monitor mode, no changes |
| `DOCKWARDEN_HEALTH_WATCH` | `true` | Enable health monitoring |
| `DOCKWARDEN_HEALTH_ACTION` | `restart` | Action on unhealthy: restart/notify |
| `DOCKWARDEN_LOG_LEVEL` | `info` | Log level: debug/info/warn/error |
| `DOCKWARDEN_LOG_FORMAT` | `auto` | Log format: auto/json/pretty |
| `DOCKWARDEN_API_ENABLED` | `false` | Enable REST API |
| `DOCKWARDEN_API_PORT` | `8080` | API listen port |
| `DOCKWARDEN_API_TOKEN` | - | API authentication token |
| `DOCKWARDEN_METRICS` | `false` | Enable Prometheus metrics |
| `TZ` | `Asia/Dhaka` | Timezone |

### Docker Secrets Environment Variables

| Variable | Description |
|----------|-------------|
| `DOCKWARDEN_REGISTRY_SECRET` | Path to registry auth secret |
| `DOCKWARDEN_REGISTRY_SECRET_FILE` | Alternative file path (auto-read) |
| `DOCKWARDEN_NOTIFICATION_URL_FILE` | Notification webhook URL secret |
| `DOCKWARDEN_API_TOKEN_FILE` | API token from secret |

---

## Container Labels

Labels can be used to configure per-container behavior.

| Label | Values | Description |
|-------|--------|-------------|
| `dockwarden.enable` | `true`/`false` | Enable/disable for this container |
| `dockwarden.scope` | `<string>` | Scope identifier |
| `dockwarden.stop-signal` | `SIGTERM`/`SIGKILL`/etc | Stop signal |
| `dockwarden.stop-timeout` | `<seconds>` | Stop timeout |
| `dockwarden.update.enable` | `true`/`false` | Enable auto-update |
| `dockwarden.watch.enable` | `true`/`false` | Enable health watch |
| `dockwarden.watch.action` | `restart`/`notify` | Health action |
| `dockwarden.depends-on` | `<container-name>` | Update/restart dependency |
| `dockwarden.pre-update` | `<command>` | Pre-update hook command |
| `dockwarden.post-update` | `<command>` | Post-update hook command |

---

## API Endpoints

### REST API (v1)

```
GET  /v1/health           - Service health check
GET  /v1/info             - Service information
GET  /v1/containers       - List monitored containers
GET  /v1/containers/:id   - Get container details
POST /v1/update           - Trigger update check
POST /v1/update/:id       - Update specific container
GET  /v1/status           - Current status summary
```

### Prometheus Metrics

```
GET  /metrics             - Prometheus metrics endpoint
```

**Available Metrics:**
- `dockwarden_containers_total` - Total monitored containers
- `dockwarden_updates_total` - Total updates performed
- `dockwarden_restarts_total` - Total health restarts
- `dockwarden_check_duration_seconds` - Check duration histogram
- `dockwarden_container_healthy` - Container health gauge

---

## Security Model

### 1. Minimal Socket Access

Instead of full docker socket access, DockWarden supports a proxy approach:

```yaml
services:
  dockwarden:
    image: emon5122/dockwarden:latest
    environment:
      - DOCKER_HOST=tcp://socket-proxy:2375
    
  socket-proxy:
    image: tecnativa/docker-socket-proxy
    environment:
      - CONTAINERS=1
      - IMAGES=1
      - POST=1  # For restart/pull operations
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock:ro
```

### 2. Docker Secrets for Credentials

```bash
# Create registry auth secret
echo '{"auths":{"ghcr.io":{"auth":"base64-encoded-creds"}}}' | \
  docker secret create registry_auth -

# Use in compose
secrets:
  registry_auth:
    external: true
```

### 3. Read-Only Root Filesystem

```yaml
services:
  dockwarden:
    image: emon5122/dockwarden:latest
    read_only: true
    tmpfs:
      - /tmp
    security_opt:
      - no-new-privileges:true
```

---

## Project Structure

```
dockwarden/
├── cmd/
│   └── dockwarden/
│       └── main.go
├── internal/
│   ├── actions/
│   │   ├── update.go
│   │   ├── restart.go
│   │   └── check.go
│   ├── config/
│   │   ├── config.go
│   │   ├── env.go
│   │   └── secrets.go
│   ├── docker/
│   │   ├── client.go
│   │   ├── container.go
│   │   └── image.go
│   ├── health/
│   │   ├── monitor.go
│   │   └── actions.go
│   ├── registry/
│   │   ├── auth.go
│   │   ├── digest.go
│   │   └── manifest.go
│   ├── scheduler/
│   │   └── scheduler.go
│   └── notifications/
│       ├── notifier.go
│       ├── discord.go
│       ├── slack.go
│       ├── telegram.go
│       └── webhook.go
├── pkg/
│   ├── api/
│   │   ├── server.go
│   │   ├── handlers.go
│   │   └── middleware.go
│   ├── metrics/
│   │   └── prometheus.go
│   └── types/
│       └── types.go
├── build/
│   └── Dockerfile
├── deployments/
│   ├── docker-compose.yml
│   ├── docker-compose.secrets.yml
│   └── docker-compose.proxy.yml
├── docs/
│   ├── configuration.md
│   ├── security.md
│   ├── migration.md
│   └── api.md
├── scripts/
│   ├── build.sh
│   └── release.sh
├── .github/
│   └── workflows/
│       ├── build.yml
│       ├── release.yml
│       └── test.yml
├── go.mod
├── go.sum
├── LICENSE
├── README.md
└── DESIGN.md
```

---

## Migration Guide

### From Watchtower

```yaml
# Before (Watchtower)
services:
  watchtower:
    image: containrrr/watchtower
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock
      - /root/.docker/config.json:/config.json
    environment:
      - WATCHTOWER_CLEANUP=true
      - WATCHTOWER_SCHEDULE=0 0 4 * * *

# After (DockWarden)
services:
  dockwarden:
    image: emon5122/dockwarden:latest
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock
    secrets:
      - registry_auth
    environment:
      - DOCKWARDEN_CLEANUP=true
      - DOCKWARDEN_SCHEDULE=0 0 4 * * *
      - DOCKWARDEN_REGISTRY_SECRET=/run/secrets/registry_auth

secrets:
  registry_auth:
    file: ./registry-auth.json
```

### From Docker Watchdog

```yaml
# Before (Watchdog)
services:
  watchdog:
    image: buanet/watchdog:latest
    environment:
      - WATCHDOG_CONTAINER_LABEL=all
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock

# After (DockWarden)
services:
  dockwarden:
    image: emon5122/dockwarden:latest
    environment:
      - DOCKWARDEN_MODE=watch
      - DOCKWARDEN_LABEL_ENABLE=false  # Watch all
      - DOCKWARDEN_HEALTH_WATCH=true
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock
```

---

## Roadmap

### Phase 1 (MVP)
- [ ] Core container update functionality
- [ ] Health monitoring and auto-restart
- [ ] Docker secrets support
- [ ] Basic notifications (webhook, Discord)
- [ ] Multi-arch Docker images

### Phase 2
- [ ] REST API
- [ ] Prometheus metrics
- [ ] More notification channels
- [ ] Update strategies (rolling, scheduled)
- [ ] Pre/post hooks

### Phase 3
- [ ] Web UI dashboard
- [ ] Container dependency graph
- [ ] Rollback support
- [ ] Kubernetes support
- [ ] Vault integration

---

## License

MIT License - See [LICENSE](LICENSE) for details.

## Repository

- **GitHub:** https://github.com/emon5122/dockwarden
- **Docker Hub:** https://hub.docker.com/r/emon5122/dockwarden
- **Documentation:** https://emon5122.github.io/dockwarden
