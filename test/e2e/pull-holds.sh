#!/usr/bin/env bash
# End-to-end check of pull holds on a real daemon (docker-in-docker): real registry error strings must classify.
# Containers run from (a) busybox pulled from Docker Hub, (b) a locally built image, (c) busybox re-tagged to a
# docker.io repo that does not exist, (d) busybox re-tagged to a ghcr.io repo that needs credentials. DockWarden loops
# every 3 s for ~20 s: (b)-(d) must be refused and held after one attempt each (note the containerd store fills RepoDigests even for built/re-tagged images, so no local-only shortcut) (no "failed to pull" errors, no loop),
# (a) must keep being checked, and nothing may be recreated. A real 429 cannot be provoked from CI; that path is
# covered by the stub tests in internal/updater.
# Usage: test/e2e/pull-holds.sh   (from the repo root; needs docker + network; ~1 min)
set -euo pipefail
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
name="dw-e2e-holds-$$"
trap 'docker rm -f "$name" >/dev/null 2>&1 || true' EXIT
docker run -d --rm --privileged --name "$name" --cpus=4 --memory=3g -v "$root":/src:ro docker:29-dind >/dev/null
for _ in $(seq 60); do docker exec "$name" docker info >/dev/null 2>&1 && break; sleep 1; done
docker exec -i "$name" sh -s <<'IN'
cd /src && docker build -q -f build/Dockerfile -t dockwarden:e2e . >/dev/null
docker pull -q busybox:1.36.1 >/dev/null
printf 'FROM busybox:1.36.1\nRUN echo local > /local\n' | docker build -q -t cm2:local - >/dev/null
docker tag busybox:1.36.1 nexisltd/dw-e2e-nonexistent-repo:1
docker tag busybox:1.36.1 ghcr.io/nexisltd/dw-e2e-private-no-creds:1
for img in busybox:1.36.1 cm2:local nexisltd/dw-e2e-nonexistent-repo:1 ghcr.io/nexisltd/dw-e2e-private-no-creds:1; do
  docker run -d --label dockwarden.enable=true "$img" sleep 3600 >/dev/null
done
for img in busybox:1.36.1 cm2:local nexisltd/dw-e2e-nonexistent-repo:1 ghcr.io/nexisltd/dw-e2e-private-no-creds:1; do echo "RepoDigests $img: $(docker image inspect $img --format '{{json .RepoDigests}}')"; done
docker info --format 'store: {{.DriverStatus}} {{.Driver}}'
before=$(docker ps -q | sort | tr '\n' ' ')
docker run -d --name dockwarden -v /var/run/docker.sock:/var/run/docker.sock \
  -e DOCKWARDEN_LABEL_ENABLE=true -e DOCKWARDEN_MODE=update -e DOCKWARDEN_INTERVAL=3s -e DOCKWARDEN_HUB_INTERVAL=0s \
  -e DOCKWARDEN_LOG_LEVEL=debug -l dockwarden.update.enable=false dockwarden:e2e >/dev/null
sleep 22
docker logs dockwarden > /tmp/dw.log 2>&1
docker rm -f dockwarden >/dev/null
fail=0
grep -h "Digest check for" /tmp/dw.log | head -3 | cut -c1-700 || true
grep -E "level=(warning|error)" /tmp/dw.log | cut -c1-700 || true
errs=$(grep -c "Failed to process" /tmp/dw.log || true); echo "failed-to-process lines: $errs"; [ "$errs" = 0 ] || fail=1
for ref in cm2:local nexisltd/dw-e2e-nonexistent-repo:1 ghcr.io/nexisltd/dw-e2e-private-no-creds:1; do
  n=$(grep -c "Pull of $ref refused by its registry" /tmp/dw.log || true)
  echo "$ref: refused+held $n time(s)"; [ "$n" = 1 ] || fail=1
done
hub=$(grep -c "Image busybox:1.36.1 unchanged" /tmp/dw.log || true); echo "busybox:1.36.1 checks=$hub"; [ "$hub" -ge 3 ] || fail=1
after=$(docker ps -q | sort | tr '\n' ' '); [ "$before" = "$after" ] || { echo "containers were recreated"; fail=1; }
[ $fail = 0 ] && echo "E2E PASS" || { echo "E2E FAIL"; tail -60 /tmp/dw.log | cut -c1-260; exit 1; }
IN
