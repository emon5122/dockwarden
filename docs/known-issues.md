# Known Issues

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
