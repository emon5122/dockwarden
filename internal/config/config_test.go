package config

import (
	"testing"
	"time"

	"github.com/spf13/viper"
)

// A unitless duration used to be read as nanoseconds, because viper defers to
// spf13/cast which appends "ns" when it finds no unit. DOCKWARDEN_INTERVAL="60"
// therefore meant 60ns, and the update loop ran flat out forever while every
// pass logged "0 updated, 0 failed" and looked healthy.
func TestDurationValue(t *testing.T) {
	cases := []struct {
		name string
		set  string
		want time.Duration
	}{
		// The regression itself: bare numbers mean seconds, never nanoseconds.
		{"bare integer means seconds", "60", time.Minute},
		{"bare zero", "0", 0},
		{"bare fractional", "1.5", 1500 * time.Millisecond},
		{"surrounding whitespace is ignored", "  30  ", 30 * time.Second},

		// Properly-united values must keep working exactly as before.
		{"seconds unit", "45s", 45 * time.Second},
		{"minutes unit", "5m", 5 * time.Minute},
		{"hours unit", "2h", 2 * time.Hour},
		{"compound", "1h30m", 90 * time.Minute},
		{"milliseconds", "250ms", 250 * time.Millisecond},
		// An explicit nanosecond value is honoured — someone who writes the
		// unit means it, unlike someone who omits it entirely.
		{"explicit nanoseconds", "60ns", 60 * time.Nanosecond},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			viper.Reset()
			viper.Set("probe-duration", tc.set)
			if got := durationValue("probe-duration"); got != tc.want {
				t.Errorf("durationValue(%q) = %v, want %v", tc.set, got, tc.want)
			}
		})
	}
}

// An unset key must fall through to viper so the flag default still applies —
// the helper must not turn "absent" into zero.
func TestDurationValueUnsetFallsThrough(t *testing.T) {
	viper.Reset()
	viper.SetDefault("probe-duration", 90*time.Second)
	if got := durationValue("probe-duration"); got != 90*time.Second {
		t.Errorf("unset key = %v, want the 90s default", got)
	}
}

// Garbage must not be silently reinterpreted as seconds; it falls through to
// viper, which yields zero — the same behaviour as before this helper existed.
func TestDurationValueNonNumericFallsThrough(t *testing.T) {
	viper.Reset()
	viper.Set("probe-duration", "banana")
	if got := durationValue("probe-duration"); got != 0 {
		t.Errorf("non-numeric = %v, want 0", got)
	}
}
