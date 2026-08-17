package docker

import (
	"testing"
	"time"
)

// The label is documented as bare seconds, but users naturally write duration
// units too. The old implementation blindly appended "s": "30s" became the
// unparseable "30ss" (label silently ignored) and "1m" became "1ms" — a
// one-millisecond grace period before SIGKILL.
func TestGetStopTimeout(t *testing.T) {
	def := 10 * time.Second

	cases := []struct {
		name  string
		label string
		want  time.Duration
	}{
		{"unset uses default", "", def},
		{"bare integer means seconds", "60", time.Minute},
		{"bare fractional", "1.5", 1500 * time.Millisecond},
		{"surrounding whitespace", "  30  ", 30 * time.Second},
		{"seconds unit", "30s", 30 * time.Second},
		{"minutes unit", "1m", time.Minute},
		{"compound duration", "1m30s", 90 * time.Second},
		{"garbage uses default", "banana", def},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctr := Container{Labels: map[string]string{"dockwarden.stop-timeout": tc.label}}
			if got := ctr.GetStopTimeout(def); got != tc.want {
				t.Errorf("GetStopTimeout(%q) = %v, want %v", tc.label, got, tc.want)
			}
		})
	}
}

// An unset stop-signal label must yield "" so the daemon falls back to the
// container's own STOPSIGNAL — a hardcoded SIGTERM default would override
// images like nginx that declare SIGQUIT.
func TestGetStopSignal(t *testing.T) {
	if got := (Container{}).GetStopSignal(); got != "" {
		t.Errorf("unset stop-signal = %q, want empty", got)
	}
	ctr := Container{Labels: map[string]string{"dockwarden.stop-signal": "SIGQUIT"}}
	if got := ctr.GetStopSignal(); got != "SIGQUIT" {
		t.Errorf("stop-signal = %q, want SIGQUIT", got)
	}
}
