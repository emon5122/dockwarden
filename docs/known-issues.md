# Known Issues

## 2. Stale env pins survive recreation (image-default "pollution")

**Status:** fixed (unreleased) · **Severity:** medium · **Component:**
`internal/docker/client.go`

### Summary

Docker's container inspect merges the image's baked-in `ENV` with the
user-supplied env into one flat list, with no record of which entry came from
where. Recreating a container from that merged list pins the old image's
defaults as explicit values, so the new image's updated defaults never take
effect.

Exact-match stripping (drop entries equal to the old image's defaults, added
earlier for this problem) has a structural blind spot: an entry pinned by an
*earlier* recreate — from an image two versions back, or by a DockWarden
version that predates stripping — no longer matches the current image's
default. It is then indistinguishable from a deliberate user override and
sticks forever. Dropping it blindly is worse: a user override that happens to
differ from the default (`NODE_ENV`, credentials, feature flags) would be
silently reverted.

### Fix: provenance

Whenever DockWarden creates a container it now records the keys of the
genuinely-overridden env entries in a `dockwarden.env-overrides` label. On the
next recreate that label is authoritative: any env entry whose key is not
listed came from *some* image and is dropped — no value comparison, no
dependency on the old image still being inspectable. An empty label is
meaningful ("zero overrides") and distinct from an absent one ("provenance
unknown").

### Remaining limitations (by design)

- Containers without the label (created by compose/`docker run`, or last
  recreated by an older DockWarden) still get the exact-match heuristic, so a
  pre-existing stale pin survives until the container is recreated outside
  DockWarden (compose rebuilds env from the compose file, which resets it).
- A user override whose value exactly equals the old image's default is
  indistinguishable from inheritance on that first, label-less recreate and
  will be refreshed rather than pinned. From the second recreate on, the label
  removes the ambiguity.
- Labels have the same pollution mechanism and still use only exact-match
  stripping; extending provenance to labels is possible but not yet done.

## 1. Restart breaker never trips for *flapping* containers

**Status:** fixed (unreleased) · **Severity:** high · **Component:**
`internal/health/watcher.go` · **Found:** 2026-08-05, production (RadioLens /
imposetech)

> **Fixed 2026-08-05.** `containerState.restartAttempts int` became
> `restarts []time.Time`, counted over a rolling `--restart-window` (default
> `1h`) via `recordRestart` / `restartCount` / `pruneRestarts`. Recovery no
> longer clears the history — only a new image does. The `notify` action is
> recorded in the same window so a flapping container stops alerting hourly
> forever. Regression tests in `internal/health/watcher_test.go`. The analysis
> below is kept as the rationale.

### Summary

`MaxRestartAttempts = 5` is documented as "Stops retry attempts after 5 failures
until new image version arrives". That holds for a container that stays
unhealthy. It does **not** hold for a container that recovers briefly after each
restart — the counter resets and DockWarden will restart it forever, without
ever notifying.

### Mechanism

`processContainer` resets the counter the moment a container reports healthy:

```go
} else if ctr.IsHealthy() {
    if state.restartAttempts > 0 {
        log.Infof("Container %s is now healthy after %d restart(s)", ctr.Name, state.restartAttempts)
        state.restartAttempts = 0        // <-- breaker disarmed
    }
    state.consecutiveUnhealthy = 0
    state.observed = false
}
```

So the cycle *unhealthy → restart → healthy → (time passes) → unhealthy* never
accumulates. `restartAttempts` is a **consecutive-failure** counter, but it is
being relied on as a **restart-rate** limiter.

### Observed in production

Three Celery worker containers, restarted on a ~63-minute period for days.
Every cycle logs `restart 1/5` — the breaker never advanced past 1:

```
01:14:55 Container celery3 is unhealthy (check 1/3, restart 1/5)
01:15:35 Restarting unhealthy container celery3 (attempt 1/5)
01:15:46 Restarting unhealthy container celery  (attempt 1/5)
01:16:15 Container celery is now healthy after 1 restart(s)      <- counter reset
02:18:35 Container celery3 is unhealthy (check 1/3, restart 1/5) <- 63 min later
02:19:05 Restarting unhealthy container celery  (attempt 1/5)
```

The underlying app fault was a long-running job that saturated the database;
each DockWarden restart aborted it, it was requeued by the broker, restarted
from scratch, saturated the database again, and went unhealthy again. DockWarden
held that loop open indefinitely, and because the breaker never tripped,
`NotifyContainerGaveUp` never fired — so nothing ever surfaced. Impact was
roughly two hours of stalled work before a human intervened, and the loop had by
then been running for at least five days.

### Why this matters beyond one app

A restart is a reasonable response to a *crashed* container. It is a poor
response to one that is **unresponsive because it is overloaded** — restarting
sheds the in-flight work and usually recreates the same overload. Queue workers
(Celery, Sidekiq, BullMQ) are the common case: their healthchecks typically
probe the broker round-trip, which degrades under exactly the load where a
restart hurts most. DockWarden currently has no way to notice it is in that
regime.

### Proposed fix

Make the limiter **rate-based over a rolling window** instead of
consecutive-failure based:

- Track restart timestamps per container (e.g. `[]time.Time`, pruned to the
  window).
- Give up when `len(restarts within window) >= MaxRestartAttempts`, regardless
  of intervening recoveries.
- New config: `--restart-window` (suggest default `1h`), alongside the existing
  `--unhealthy-threshold` (3) and `--observation-period` (30s).
- Keep the existing reset-on-new-image behaviour in `processContainer` — that
  one is correct and should still clear the window.

Worth considering alongside it:

- **Escalating backoff** between restarts (30s → 2m → 10m → 30m) so a flapping
  container is not hammered at a fixed cadence.
- **Notify on flap** even before giving up — N restarts in a window is a signal
  worth sending immediately, not only when the breaker trips.
- **Docs**: state plainly in `configuration.md` that `health-action: restart`
  (the default) is a poor fit for queue workers whose healthcheck probes a
  broker, and suggest `notify` for those.

### Workaround until fixed

Set `health-action: notify` for queue-worker containers, or disable health
watching on them via label, and fix the container's own healthcheck so that
"busy" is not reported as "dead".
