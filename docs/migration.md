# Migration Guide

This guide helps you migrate from Watchtower or Docker Watchdog to DockWarden.

## From Watchtower

### Environment Variable Mapping

| Watchtower | DockWarden | Notes |
|------------|------------|-------|
| `WATCHTOWER_CLEANUP` | `DOCKWARDEN_CLEANUP` | Same behavior |
| `WATCHTOWER_SCHEDULE` | `DOCKWARDEN_SCHEDULE` | Same cron format |
| `WATCHTOWER_POLL_INTERVAL` | `DOCKWARDEN_INTERVAL` | In seconds |
| `WATCHTOWER_LABEL_ENABLE` | `DOCKWARDEN_LABEL_ENABLE` | Same behavior |
| `WATCHTOWER_INCLUDE_STOPPED` | `DOCKWARDEN_INCLUDE_STOPPED` | Same behavior |
| `WATCHTOWER_NO_RESTART` | `DOCKWARDEN_NO_RESTART` | Same behavior |
| `WATCHTOWER_NO_PULL` | `DOCKWARDEN_NO_PULL` | Same behavior |
| `WATCHTOWER_MONITOR_ONLY` | `DOCKWARDEN_MONITOR_ONLY` | Same behavior |
| `WATCHTOWER_ROLLING_RESTART` | `DOCKWARDEN_ROLLING_RESTART` | Same behavior |
| `WATCHTOWER_TIMEOUT` | `DOCKWARDEN_STOP_TIMEOUT` | Same behavior |
| `WATCHTOWER_SCOPE` | `DOCKWARDEN_SCOPE` | Same behavior |
| `WATCHTOWER_HTTP_API_UPDATE` | `DOCKWARDEN_API_ENABLED` | Enhanced API |
| `WATCHTOWER_HTTP_API_TOKEN` | `DOCKWARDEN_API_TOKEN` | Same behavior |
| `WATCHTOWER_HTTP_API_METRICS` | `DOCKWARDEN_METRICS` | Same behavior |

### Label Mapping

| Watchtower | DockWarden |
|------------|------------|
| `com.centurylinklabs.watchtower.enable` | `dockwarden.enable` |
| `com.centurylinklabs.watchtower.scope` | `dockwarden.scope` |
| `com.centurylinklabs.watchtower.stop-signal` | `dockwarden.stop-signal` |

### Configuration File Migration

**Before (Watchtower with config.json):**
```yaml
services:
  watchtower:
    image: containrrr/watchtower
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock
      - /root/.docker/config.json:/config.json
    environment:
      - WATCHTOWER_CLEANUP=true
      - WATCHTOWER_SCHEDULE=0 0 4 * * *
      - WATCHTOWER_LABEL_ENABLE=true
```

**After (DockWarden with secrets):**
```yaml
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
      - DOCKWARDEN_LABEL_ENABLE=true
      - DOCKWARDEN_REGISTRY_SECRET=/run/secrets/registry_auth

secrets:
  registry_auth:
    file: /root/.docker/config.json
```

### Notification Migration

Watchtower's notification format is compatible with DockWarden:

```yaml
# Watchtower
environment:
  - WATCHTOWER_NOTIFICATIONS=slack
  - WATCHTOWER_NOTIFICATION_SLACK_HOOK_URL=https://...

# DockWarden
environment:
  - DOCKWARDEN_NOTIFICATION_URL=https://...
```

## From Docker Watchdog

### Environment Variable Mapping

| Docker Watchdog | DockWarden | Notes |
|-----------------|------------|-------|
| `WATCHDOG_CONTAINER_LABEL` | `DOCKWARDEN_LABEL_NAME` | Default changed |
| `WATCHDOG_INTERVAL` | `DOCKWARDEN_INTERVAL` | Same behavior |
| `WATCHDOG_DEFAULT_STOP_TIMEOUT` | `DOCKWARDEN_STOP_TIMEOUT` | Same behavior |

### Configuration Migration

**Before (Docker Watchdog):**
```yaml
services:
  watchdog:
    image: buanet/watchdog:latest
    environment:
      - WATCHDOG_CONTAINER_LABEL=all
      - WATCHDOG_INTERVAL=10
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock
```

**After (DockWarden - health monitoring only):**
```yaml
services:
  dockwarden:
    image: emon5122/dockwarden:latest
    environment:
      - DOCKWARDEN_MODE=watch
      - DOCKWARDEN_LABEL_ENABLE=false  # Watch all containers
      - DOCKWARDEN_HEALTH_WATCH=true
      - DOCKWARDEN_HEALTH_ACTION=restart
      - DOCKWARDEN_INTERVAL=10
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock
```

**After (DockWarden - full features):**
```yaml
services:
  dockwarden:
    image: emon5122/dockwarden:latest
    environment:
      - DOCKWARDEN_MODE=full
      - DOCKWARDEN_LABEL_ENABLE=false
      - DOCKWARDEN_HEALTH_WATCH=true
      - DOCKWARDEN_HEALTH_ACTION=restart
      - DOCKWARDEN_CLEANUP=true
      - DOCKWARDEN_SCHEDULE=0 4 * * *
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock
```

### Label Migration

| Docker Watchdog | DockWarden |
|-----------------|------------|
| `watchdog=true` | `dockwarden.enable=true` |
| `watchdog.stop.timeout` | `dockwarden.stop-timeout` |

## Combined Setup

If you were running both Watchtower and Docker Watchdog, you can now use a single DockWarden instance:

**Before (Two containers):**
```yaml
services:
  watchtower:
    image: containrrr/watchtower
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock
      - /root/.docker/config.json:/config.json
    environment:
      - WATCHTOWER_CLEANUP=true
      - WATCHTOWER_SCHEDULE=0 0 4 * * *

  watchdog:
    image: buanet/watchdog:latest
    environment:
      - WATCHDOG_CONTAINER_LABEL=all
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock
```

**After (Single DockWarden):**
```yaml
services:
  dockwarden:
    image: emon5122/dockwarden:latest
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock
    secrets:
      - registry_auth
    environment:
      # Update features (from Watchtower)
      - DOCKWARDEN_CLEANUP=true
      - DOCKWARDEN_SCHEDULE=0 0 4 * * *
      
      # Health features (from Watchdog)
      - DOCKWARDEN_HEALTH_WATCH=true
      - DOCKWARDEN_HEALTH_ACTION=restart
      
      # Registry auth (improved security)
      - DOCKWARDEN_REGISTRY_SECRET=/run/secrets/registry_auth

secrets:
  registry_auth:
    file: /root/.docker/config.json
```

## Verification

After migrating, verify DockWarden is working:

```bash
# Check logs
docker logs dockwarden

# Check API (if enabled)
curl http://localhost:8080/v1/health

# List monitored containers
curl -H "Authorization: Bearer YOUR_TOKEN" http://localhost:8080/v1/containers
```

## Rollback

If you need to rollback, your old configuration should still work as both Watchtower and Docker Watchdog remain available (though no longer maintained).

Consider keeping your old compose files as backups until you've verified DockWarden is working correctly for your use case.
