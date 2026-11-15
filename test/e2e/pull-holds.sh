#!/usr/bin/env bash
# End-to-end check of pull holds on a real daemon (docker-in-docker): real registry error strings must classify.
# Containers run from (a) busybox pulled from Docker Hub, (b) a locally built image, (c) busybox re-tagged to a
# docker.io repo that does not exist, (d) busybox re-tagged to a ghcr.io repo that needs credentials. DockWarden loops
# every 3 s for ~20 s: (b)-(d) must be refused and held after one attempt each (note the containerd store fills RepoDigests even for built/re-tagged images, so no local-only shortcut) (no "failed to pull" errors, no loop),
# (a) must keep being checked, and nothing may be recreated. (e) is served by a fake loopback registry that answers
# manifest HEAD with a new digest and everything else 429 TOOMANYREQUESTS: the real daemon's 429 text must classify,
# produce exactly one "rate limited" warning and no request storm. (The 10m->1h escalation is unit tested.)
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
mkdir -p /srv/fake
cat > /srv/fake/fake.py <<'PY'
import http.server, json
D = "sha256:" + "ab" * 32
class H(http.server.BaseHTTPRequestHandler):
    def log_message(self, *a): pass
    def _go(self):
        print("REQ", self.command, self.path, flush=True)
        if self.path.rstrip("/") == "/v2":
            self.send_response(200); self.send_header("Content-Length", "0"); self.end_headers(); return
        if self.command == "HEAD" and "/manifests/" in self.path:
            self.send_response(200)
            self.send_header("Content-Type", "application/vnd.docker.distribution.manifest.v2+json")
            self.send_header("Docker-Content-Digest", D); self.send_header("Content-Length", "1000"); self.end_headers(); return
        body = json.dumps({"errors": [{"code": "TOOMANYREQUESTS", "message": "You have reached your pull rate limit."}]}).encode()
        self.send_response(429); self.send_header("Content-Type", "application/json")
        self.send_header("Retry-After", "3600"); self.send_header("Content-Length", str(len(body))); self.end_headers()
        if self.command != "HEAD": self.wfile.write(body)
    do_GET = do_HEAD = do_POST = _go
http.server.HTTPServer(("127.0.0.1", 5999), H).serve_forever()
PY
docker run -d --name fakereg --network host -v /srv/fake:/srv/fake python:3-alpine python -u /srv/fake/fake.py >/dev/null
for _ in $(seq 30); do wget -q -O /dev/null http://127.0.0.1:5999/v2/ && break; sleep 1; done
docker tag busybox:1.36.1 localhost:5999/fake/app:1
for img in busybox:1.36.1 localhost:5999/fake/app:1 cm2:local nexisltd/dw-e2e-nonexistent-repo:1 ghcr.io/nexisltd/dw-e2e-private-no-creds:1; do
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
docker rm -f dockwarden >/dev/null; docker logs fakereg 2>&1 | head -8
fail=0
grep -h "Digest check for" /tmp/dw.log | head -3 | cut -c1-700 || true
grep -E "level=(warning|error)" /tmp/dw.log | cut -c1-700 || true
errs=$(grep -c "Failed to process" /tmp/dw.log || true); echo "failed-to-process lines: $errs"; [ "$errs" = 0 ] || fail=1
for ref in cm2:local nexisltd/dw-e2e-nonexistent-repo:1 ghcr.io/nexisltd/dw-e2e-private-no-creds:1; do
  n=$(grep -c "Pull of $ref refused by its registry" /tmp/dw.log || true)
  d=$(grep -c "Digest check for $ref failed" /tmp/dw.log || true)
  echo "$ref: refused+held $n time(s), digest attempts $d"; [ "$n" = 1 ] && [ "$d" = 1 ] || fail=1
done
rl=$(grep -c "localhost:5999.*rate limited\|rate limited.*localhost" /tmp/dw.log || true)
reqs=$(docker logs fakereg 2>&1 | grep -c '^REQ' || true)
echo "fake registry: rate-limited warnings=$rl requests=$reqs"
[ "$rl" = 1 ] && [ "$reqs" -le 6 ] || fail=1
hub=$(grep -c "Image busybox:1.36.1 unchanged" /tmp/dw.log || true); echo "busybox:1.36.1 checks=$hub"; [ "$hub" -ge 3 ] || fail=1
after=$(docker ps -q | sort | tr '\n' ' '); [ "$before" = "$after" ] || { echo "containers were recreated"; fail=1; }
[ $fail = 0 ] && echo "E2E PASS" || { echo "E2E FAIL"; tail -60 /tmp/dw.log | cut -c1-260; exit 1; }
IN
