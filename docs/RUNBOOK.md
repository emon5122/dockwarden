# Runbook

How to release and run DockWarden. Keep this short and current.

## Release

1. Land conventional commits on `main` (`feat:`, `fix:`, `ci:`, `docs:` ...).
   A push to `main` runs tests and a multi-arch build check only. **No image is pushed.**
2. release-please (`.github/workflows/release-please.yml`) keeps a release PR open.
   **Merge it to release.** That creates the tag `vX.Y.Z` and the GitHub release.
3. The tag push runs `.github/workflows/docker.yml`:
   - tests, then a multi-arch image (`linux/amd64`, `linux/arm64`, `linux/arm/v7`) pushed to
     `docker.io/emon5122/dockwarden` and `ghcr.io/emon5122/dockwarden` with the tags
     `X.Y.Z`, `X.Y`, `X` and `latest` (`latest` only for a non-prerelease tag, i.e. no `-` in it);
   - GoReleaser attaches the binaries and checksums to the release.
   - Never `main`, branch or sha image tags.
4. Rebuild the image of an existing tag (e.g. after a failed push): Actions → *Build and Release* →
   *Run workflow*, `tag = vX.Y.Z`. This rebuilds and pushes the image only (GoReleaser does not run again).

Concurrency: release builds share the job-level group `docker-dockwarden`, so a newer release
build cancels an older one. main/PR check builds use a per-ref group, so a release-please commit
never cancels a running release build. A `cancelled` run superseded by a newer one is not a failure.

## Deploy

Hosts run `emon5122/dockwarden:latest` from compose. DockWarden **never updates itself**
(it skips its own container), so after a release a host picks up the new version only when its
DockWarden is recreated:

```bash
cd <compose dir>
docker compose pull dockwarden && docker compose up -d dockwarden
docker logs --tail 50 dockwarden    # check startup, label mode and the skip list
```

DockWarden has no database, so there are no migrations. All configuration is env vars
(see [configuration.md](configuration.md)), read at start.

### Per host

| Host | Compose | Mode | Notes |
|------|---------|------|-------|
| VPS | unknown here (see `docs/servers.md` in emon5122/titli) | label mode (`DOCKWARDEN_LABEL_ENABLE=true`) + `DOCKWARDEN_DISABLE_CONTAINERS` skip list | Only stateless app containers carry `dockwarden.enable=true`. Never DBs or stateful services (postgres, pgadmin, redis, rabbitmq, rustfs), and never call-carrying services without a call guard. |
| R610 (home) | `nexisltd/home-infra` `docker/dockwarden/docker-compose.yml` | label mode, API on, `TZ=Asia/Dhaka`, ghcr login from `config.json` (`DOCKWARDEN_REGISTRY_SECRET`) | No host port: reached through the home Caddy at `dockwarden.home.ihemon.me` (network `home-dns_default`). The skip list holds the home-pbx apps (`homepbx-backend-1 homepbx-ari-1 homepbx-housekeeping-1 homepbx-camgw-1 homepbx-camgw-door-1`). home-pbx updates those itself with its own scoped DockWarden, 02:00-06:00 Dhaka (nexisltd/home-pbx `docs/RUNBOOK.md`). |
| rl9 | unknown | unknown | Work box: report only, no changes. |
| nt14 | unknown | unknown | Not documented in this repo. |

`DOCKWARDEN_DISABLE_CONTAINERS` set from the environment is **space-separated**
(`a b c`). Viper splits an env string on whitespace, so a comma-separated value is read as a
single name and skips nothing.

## Roll back

Pin the previous release in the host's compose, then recreate:

```yaml
    image: emon5122/dockwarden:1.2.2   # was :latest
```

```bash
docker compose up -d dockwarden
```

Return to `:latest` once a fixed release is out. To keep a bad `latest` off every host, release a
fix (revert commit → release PR → merge). Never retag images by hand.

## Secrets (names only)

| Where | Name | Used for |
|-------|------|----------|
| GitHub Actions | `GH_TOKEN` | release-please (its tag push triggers the build), ghcr push, GoReleaser |
| GitHub Actions | `DOCKERHUB_TOKEN` | Docker Hub push (user `emon5122`) |
| R610 host | `/docker/dockwarden/config.json` (Bitwarden `dockwarden-read-ghcr`) | ghcr pull auth (`read:packages`) |
| Hosts (optional) | `DOCKWARDEN_API_TOKEN`, notification URLs | API auth and alerts, set in each host's compose env |
