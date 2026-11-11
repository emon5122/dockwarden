#!/usr/bin/env bash
# End-to-end check of compose rolls on a real daemon (docker-in-docker, nothing touches the host's containers).
# A compose service is created from an OLD busybox image, its compose file is then edited (label, env_file +
# environment, mem_limit, stop_grace_period), and one DockWarden pass must apply all of it while rolling to the
# new image. A second project whose compose file needs a shell-only variable must fall back to the legacy roll.
# Usage: test/e2e/compose-roll.sh   (from the repo root; needs docker + network; ~2 min)
set -euo pipefail
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
name="dw-e2e-dind-$$"
trap 'docker rm -f "$name" >/dev/null 2>&1 || true' EXIT
docker run -d --rm --privileged --name "$name" --cpus=4 --memory=3g -v "$root":/src:ro docker:29-dind >/dev/null
for _ in $(seq 60); do docker exec "$name" docker info >/dev/null 2>&1 && break; sleep 1; done
docker exec -i "$name" sh -s <<'IN'
cd /src && docker build -q -f build/Dockerfile -t dockwarden:e2e . >/dev/null && echo "image built: $(docker image inspect dockwarden:e2e --format '{{.Size}}')"
docker run --rm --entrypoint /usr/local/bin/docker-compose dockwarden:e2e version
docker pull -q busybox:1.36.1 >/dev/null && docker tag busybox:1.36.1 busybox:latest
OLD=$(docker image inspect busybox:latest --format '{{.Id}}')
mkdir -p /srv/e2e /srv/e2elegacy
cat > /srv/e2e/compose.yaml <<'Y'
services:
  app:
    image: busybox:latest
    command: ["sleep", "3600"]
    env_file: ./app.env
    labels:
      dockwarden.enable: "true"
      dockwarden.stop-timeout: "5"
Y
echo "A=1" > /srv/e2e/app.env
cat > /srv/e2elegacy/compose.yaml <<'Y'
services:
  leg:
    image: busybox:latest
    command: ["sleep", "3600"]
    environment:
      FROM_SHELL: ${FROM_SHELL}
    labels:
      dockwarden.enable: "true"
Y
docker compose -f /srv/e2e/compose.yaml up -d 2>&1 | tail -1
FROM_SHELL=x docker compose -f /srv/e2elegacy/compose.yaml up -d 2>&1 | tail -1
# the "release": compose edits a human would otherwise apply with compose up
cat > /srv/e2e/compose.yaml <<'Y'
services:
  app:
    image: busybox:latest
    command: ["sleep", "3600"]
    env_file: ./app.env
    environment:
      ADDED: "yes"
    mem_limit: 64m
    stop_grace_period: 30s
    labels:
      dockwarden.enable: "true"
      dockwarden.stop-timeout: "25"
      e2e.marker: "v2"
Y
echo "A=2" > /srv/e2e/app.env
sed -i 's/dockwarden.enable: "true"/dockwarden.enable: "true"\n      e2e.marker: "v2"/' /srv/e2elegacy/compose.yaml
docker run --name dockwarden -v /var/run/docker.sock:/var/run/docker.sock \
  -e DOCKWARDEN_LABEL_ENABLE=true -e DOCKWARDEN_RUN_ONCE=true -e DOCKWARDEN_MODE=update -e DOCKWARDEN_LOG_LEVEL=debug \
  -l dockwarden.update.enable=false dockwarden:e2e > /tmp/dw.log 2>&1 || true
grep -iE "compose|updated|error|fail" /tmp/dw.log | grep -v "level=debug" | cut -c1-300 | tail -20
fail=0
C=$(docker compose -f /srv/e2e/compose.yaml ps -q app)
img=$(docker inspect $C --format '{{.Image}}'); lab=$(docker inspect $C --format '{{index .Config.Labels "e2e.marker"}}')
mem=$(docker inspect $C --format '{{.HostConfig.Memory}}'); st=$(docker inspect $C --format '{{.Config.StopTimeout}}')
envs=$(docker inspect $C --format '{{range .Config.Env}}{{println .}}{{end}}' | grep -E '^(A|ADDED)=' | tr '\n' ' ')
echo "compose app: new_image=$([ "$img" != "$OLD" ] && echo yes || echo NO) marker=$lab mem=$mem stop_timeout=$st env=[$envs]"
[ "$img" != "$OLD" ] && [ "$lab" = v2 ] && [ "$mem" = 67108864 ] && [ "$st" = 30 ] && echo "$envs" | grep -q "A=2" && echo "$envs" | grep -q "ADDED=yes" || fail=1
L=$(docker compose -f /srv/e2elegacy/compose.yaml ps -q leg 2>/dev/null || docker ps -q -f label=com.docker.compose.service=leg)
limg=$(docker inspect $L --format '{{.Image}}'); llab=$(docker inspect $L --format '{{index .Config.Labels "e2e.marker"}}'); lenv=$(docker inspect $L --format '{{range .Config.Env}}{{println .}}{{end}}' | grep FROM_SHELL)
echo "legacy (unset var) leg: new_image=$([ "$limg" != "$OLD" ] && echo yes || echo NO) marker='$llab' env=$lenv"
[ "$limg" != "$OLD" ] && [ -z "$llab" ] && [ "$lenv" = FROM_SHELL=x ] || fail=1
left=$(docker ps -a --filter label=dockwarden.helper -q | wc -l); echo "helpers left: $left"; [ "$left" = 0 ] || fail=1
[ $fail = 0 ] && echo "E2E PASS" || { echo "E2E FAIL"; tail -40 /tmp/dw.log | cut -c1-300; exit 1; }
IN
