#!/usr/bin/env bash
# End-to-end check of detached rolls on a real daemon (docker-in-docker, nothing touches the host's containers).
# Two fake long-drain containers (stop timeout 1 h; on SIGTERM they "drain" for 40 s, then exit), one compose
# service and one plain `docker run` container, plus a normal compose service. With
# DOCKWARDEN_DETACH_STOP_TIMEOUT=10s:
#   pass 1 rolls the normal service, hands both long drains to roll helpers and ends in seconds (not 40 s);
#   pass 2, during the drain, skips both (one roll per container) and starts no second helper;
#   after the drain the helpers exit 0 with both containers on the new image; pass 3 collects and removes them.
# Usage: test/e2e/long-drain.sh   (from the repo root; needs docker + network; ~3 min)
set -euo pipefail
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
name="dw-e2e-drain-$$"
trap 'docker rm -f "$name" >/dev/null 2>&1 || true' EXIT
docker run -d --rm --privileged --name "$name" --cpus=4 --memory=3g -v "$root":/src:ro docker:29-dind >/dev/null
for _ in $(seq 60); do docker exec "$name" docker info >/dev/null 2>&1 && break; sleep 1; done
docker exec -i "$name" sh -s <<'IN'
set -u
cd /src && docker build -q -f build/Dockerfile -t dockwarden:e2e . >/dev/null && echo "image built"
docker pull -q busybox:1.36.1 >/dev/null && docker tag busybox:1.36.1 busybox:latest
OLD=$(docker image inspect busybox:latest --format '{{.Id}}')
mkdir -p /srv/drain /srv/quick
cat > /srv/drain/compose.yaml <<'Y'
services:
  drain:
    image: busybox:latest
    command: ["sh", "-c", "trap 'echo draining; sleep 40; echo drained; exit 0' TERM; while true; do sleep 1; done"]
    stop_grace_period: 1h
    labels:
      dockwarden.enable: "true"
      dockwarden.stop-timeout: "3600"
Y
cat > /srv/quick/compose.yaml <<'Y'
services:
  quick:
    image: busybox:latest
    command: ["sleep", "3600"]
    labels:
      dockwarden.enable: "true"
Y
docker compose -f /srv/drain/compose.yaml up -d 2>&1 | tail -1
docker compose -f /srv/quick/compose.yaml up -d 2>&1 | tail -1
docker run -d --name plaindrain -l dockwarden.enable=true -l dockwarden.stop-timeout=3600 busybox:latest \
  sh -c "trap 'echo draining; sleep 40; echo drained; exit 0' TERM; while true; do sleep 1; done" >/dev/null
pass() {
  docker rm -f dockwarden >/dev/null 2>&1
  docker run --name dockwarden -v /var/run/docker.sock:/var/run/docker.sock \
    -e DOCKWARDEN_LABEL_ENABLE=true -e DOCKWARDEN_RUN_ONCE=true -e DOCKWARDEN_MODE=update -e DOCKWARDEN_DETACH_STOP_TIMEOUT=10s \
    -l dockwarden.update.enable=false dockwarden:e2e > /tmp/dw$1.log 2>&1 || true
  grep -iE "detach|roll|updated|skipped|error|fail" /tmp/dw$1.log | cut -c1-260 | tail -12
}
img() { docker inspect "$1" --format '{{.Image}}'; }
svc() { docker ps -q -f label=com.docker.compose.service=$1 | head -1; }
helpers() { docker ps -a -q -f label=dockwarden.roll.target | wc -l; }
running_helpers() { docker ps -q -f label=dockwarden.roll.target | wc -l; }
fail=0
t0=$(date +%s); echo "--- pass 1"; pass 1; took=$(( $(date +%s) - t0 ))
D=$(svc drain); Q=$(svc quick)
echo "pass 1 took ${took}s; quick new=$([ "$(img $Q)" != "$OLD" ] && echo yes || echo NO); drain old=$([ "$(img $D)" = "$OLD" ] && echo yes || echo NO); plaindrain old=$([ "$(img plaindrain)" = "$OLD" ] && echo yes || echo NO); roll helpers running=$(running_helpers)"
[ "$took" -lt 35 ] && [ "$(img $Q)" != "$OLD" ] && [ "$(running_helpers)" = 2 ] || fail=1
docker ps -a -f label=dockwarden.roll.target --format '{{.Names}} {{.Status}}'
echo "--- pass 2 (drain in progress)"; pass 2
grep -c "roll is still running in helper" /tmp/dw2.log | grep -qx 2 && [ "$(helpers)" = 2 ] || { echo "pass 2 did not skip both in-flight rolls"; fail=1; }
for _ in $(seq 120); do [ "$(running_helpers)" = 0 ] && break; sleep 1; done
docker ps -a -f label=dockwarden.roll.target --format '{{.Names}} {{.Status}}'
for h in $(docker ps -a -q -f label=dockwarden.roll.target); do docker logs "$h" 2>&1 | tail -3 | cut -c1-200; done
D=$(svc drain); P=$(docker ps -q -f name=^plaindrain$)
echo "after drain: drain new=$([ -n "$D" ] && [ "$(img $D)" != "$OLD" ] && echo yes || echo NO) plaindrain new=$([ -n "$P" ] && [ "$(img $P)" != "$OLD" ] && echo yes || echo NO)"
[ -n "$D" ] && [ "$(img $D)" != "$OLD" ] && [ -n "$P" ] && [ "$(img $P)" != "$OLD" ] || fail=1
echo "--- pass 3"; pass 3
grep -c "detached roll, helper" /tmp/dw3.log | grep -qx 2 || { echo "pass 3 did not report both detached rolls"; fail=1; }
echo "roll helpers left: $(helpers)"; [ "$(helpers)" = 0 ] || fail=1
left=$(docker ps -a --filter label=dockwarden.helper -q | wc -l); echo "any helpers left: $left"; [ "$left" = 0 ] || fail=1
[ $fail = 0 ] && echo "E2E PASS" || { echo "E2E FAIL"; tail -30 /tmp/dw1.log | cut -c1-300; exit 1; }
IN
