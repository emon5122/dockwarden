# Security Guide

This guide covers security best practices for running DockWarden.

## Docker Socket Access

DockWarden requires access to the Docker API to manage containers. There are several ways to provide this access, each with different security implications.

### Option 1: Direct Socket Mount (Least Secure)

```yaml
services:
  dockwarden:
    image: emon5122/dockwarden:latest
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock
```

⚠️ **Warning:** Full socket access grants root-equivalent access to the host system.

### Option 2: Docker Socket Proxy (Recommended)

Use a proxy to limit which Docker API endpoints DockWarden can access:

```yaml
services:
  dockwarden:
    image: emon5122/dockwarden:latest
    environment:
      - DOCKER_HOST=tcp://socket-proxy:2375
    networks:
      - internal
    depends_on:
      - socket-proxy

  socket-proxy:
    image: tecnativa/docker-socket-proxy:latest
    privileged: true
    environment:
      # Read operations
      - CONTAINERS=1
      - IMAGES=1
      - INFO=1
      - VERSION=1
      - EVENTS=1
      - PING=1
      
      # Write operations (required for updates/restarts)
      - POST=1
      - DISTRIBUTION=1
      - AUTH=1
      
      # Disabled for security
      - BUILD=0
      - COMMIT=0
      - EXEC=0
      - NETWORKS=0
      - NODES=0
      - PLUGINS=0
      - SECRETS=0
      - SERVICES=0
      - SYSTEM=0
      - TASKS=0
      - VOLUMES=0
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock:ro
    networks:
      - internal

networks:
  internal:
    driver: bridge
    internal: true
```

### Option 3: Read-Only Mode

For monitoring without making changes:

```yaml
services:
  dockwarden:
    image: emon5122/dockwarden:latest
    environment:
      - DOCKWARDEN_MODE=monitor
      - DOCKER_HOST=tcp://socket-proxy:2375

  socket-proxy:
    image: tecnativa/docker-socket-proxy:latest
    environment:
      - CONTAINERS=1
      - IMAGES=1
      - POST=0  # No write operations
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock:ro
```

## Registry Credentials

### ❌ Avoid: Mounting config.json

```yaml
# INSECURE - exposes credentials in container filesystem
volumes:
  - /root/.docker/config.json:/config.json
```

### ✅ Recommended: Docker Secrets

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
    file: ./secrets/registry-auth.json
```

For Docker Swarm:
```bash
# Create secret
echo '{"auths":{"ghcr.io":{"auth":"..."}}}' | docker secret create registry_auth -

# Use external secret
secrets:
  registry_auth:
    external: true
```

## Container Hardening

### Read-Only Root Filesystem

```yaml
services:
  dockwarden:
    image: emon5122/dockwarden:latest
    read_only: true
    tmpfs:
      - /tmp
```

### Security Options

```yaml
services:
  dockwarden:
    image: emon5122/dockwarden:latest
    security_opt:
      - no-new-privileges:true
    cap_drop:
      - ALL
```

### Resource Limits

```yaml
services:
  dockwarden:
    image: emon5122/dockwarden:latest
    deploy:
      resources:
        limits:
          cpus: '0.5'
          memory: 256M
        reservations:
          cpus: '0.1'
          memory: 64M
```

### Non-Root User

By default, DockWarden runs as root to ensure seamless access to the Docker socket (similar to Watchtower). For enhanced security, you can use a socket proxy or configure appropriate permissions.

## API Security

### Enable Authentication

```yaml
environment:
  - DOCKWARDEN_API_ENABLED=true
  - DOCKWARDEN_API_TOKEN_FILE=/run/secrets/api_token

secrets:
  api_token:
    file: ./secrets/api-token.txt
```

### Network Isolation

Only expose the API internally:

```yaml
services:
  dockwarden:
    image: emon5122/dockwarden:latest
    networks:
      - internal
    # Don't expose ports publicly

networks:
  internal:
    driver: bridge
    internal: true
```

### Reverse Proxy with TLS

If you need external API access:

```yaml
services:
  dockwarden:
    image: emon5122/dockwarden:latest
    networks:
      - internal

  traefik:
    image: traefik:latest
    ports:
      - "443:443"
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock:ro
    labels:
      - "traefik.http.routers.dockwarden.rule=Host(`dockwarden.example.com`)"
      - "traefik.http.routers.dockwarden.tls=true"
```

## Complete Secure Example

```yaml


services:
  dockwarden:
    image: emon5122/dockwarden:latest
    container_name: dockwarden
    restart: unless-stopped
    read_only: true
    security_opt:
      - no-new-privileges:true
    cap_drop:
      - ALL
    environment:
      - DOCKER_HOST=tcp://socket-proxy:2375
      - DOCKWARDEN_CLEANUP=true
      - DOCKWARDEN_SCHEDULE=0 4 * * *
      - DOCKWARDEN_HEALTH_WATCH=true
      - DOCKWARDEN_REGISTRY_SECRET=/run/secrets/registry_auth
      - DOCKWARDEN_LOG_LEVEL=info
      - TZ=UTC
    secrets:
      - registry_auth
    networks:
      - dockwarden
    deploy:
      resources:
        limits:
          cpus: '0.5'
          memory: 256M

  socket-proxy:
    image: tecnativa/docker-socket-proxy:latest
    container_name: dockwarden-proxy
    restart: unless-stopped
    privileged: true
    environment:
      - CONTAINERS=1
      - IMAGES=1
      - INFO=1
      - VERSION=1
      - EVENTS=1
      - PING=1
      - POST=1
      - DISTRIBUTION=1
      - AUTH=1
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock:ro
    networks:
      - dockwarden

networks:
  dockwarden:
    driver: bridge
    internal: true

secrets:
  registry_auth:
    file: ./secrets/registry-auth.json
```

## Security Checklist

- [ ] Use Docker socket proxy instead of direct socket mount
- [ ] Use Docker secrets for credentials
- [ ] Enable read-only root filesystem
- [ ] Drop all capabilities
- [ ] Set `no-new-privileges`
- [ ] Set resource limits
- [ ] Use internal networks
- [ ] Enable API authentication
- [ ] Use TLS for external API access
- [ ] Run as non-root user
- [ ] Keep DockWarden updated
