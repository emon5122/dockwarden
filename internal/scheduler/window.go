package scheduler

import (
	"fmt"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
)

// windowParser accepts exactly what DOCKWARDEN_SCHEDULE accepts (5 or 6 fields,
// descriptors) plus an optional leading CRON_TZ=<zone> / TZ=<zone> prefix.
var windowParser = cron.NewParser(cron.SecondOptional | cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)

// InWindow reports whether the cron spec matches the minute that contains now,
// evaluated in the spec's CRON_TZ zone (or now's own location when the spec has
// no zone prefix). With a seconds field, any matching second inside the minute
// counts. An empty or invalid spec returns an error: callers must fail closed.
func InWindow(spec string, now time.Time) (bool, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return false, fmt.Errorf("empty schedule spec")
	}
	sched, err := windowParser.Parse(spec)
	if err != nil {
		return false, fmt.Errorf("invalid schedule spec %q: %w", spec, err)
	}
	minute := now.Truncate(time.Minute)
	next := sched.Next(minute.Add(-time.Second))
	return !next.IsZero() && next.Before(minute.Add(time.Minute)), nil
}
