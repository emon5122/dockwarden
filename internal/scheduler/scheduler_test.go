package scheduler

import (
	"testing"
	"time"

	"github.com/emon5122/dockwarden/internal/config"
)

// time.NewTicker panics on non-positive durations, so a zero interval (unset,
// or an unparseable value that fell through to zero) used to crash right after
// the first update pass.
func TestStartIntervalNonPositiveDoesNotPanic(t *testing.T) {
	for _, interval := range []time.Duration{0, -time.Second} {
		cfg := &config.Config{Interval: interval}
		s := New(cfg)

		ran := false
		s.Start(func() { ran = true }) // runs fn synchronously once, then arms the ticker

		if !ran {
			t.Fatalf("interval %v: initial run did not happen", interval)
		}
		s.Stop()
	}
}

// The docs show standard 5-field cron expressions, but cron.WithSeconds()
// REQUIRED a leading seconds field — every documented example was a startup
// crash. Both forms must parse. A parse failure here is log.Fatalf, so these
// pass by the test binary surviving.
func TestStartCronAcceptsFiveAndSixFieldExpressions(t *testing.T) {
	for _, schedule := range []string{"*/5 * * * *", "30 */5 * * * *", "@hourly"} {
		cfg := &config.Config{Schedule: schedule}
		s := New(cfg)
		s.Start(func() {})
		s.Stop()
	}
}
