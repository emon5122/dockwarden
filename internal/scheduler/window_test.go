package scheduler

import (
	"testing"
	"time"
)

func TestInWindow(t *testing.T) {
	dhaka, err := time.LoadLocation("Asia/Dhaka")
	if err != nil {
		t.Skip("tzdata unavailable")
	}
	const spec = "CRON_TZ=Asia/Dhaka * * 2-5 * * *"
	at := func(h, m, s int) time.Time { return time.Date(2026, 11, 8, h, m, s, 0, dhaka) }

	cases := []struct {
		name string
		spec string
		now  time.Time
		want bool
	}{
		{"start of window", spec, at(2, 0, 0), true},
		{"middle", spec, at(3, 30, 15), true},
		{"last second 05:59:59", spec, at(5, 59, 59), true},
		{"06:00 is outside", spec, at(6, 0, 0), false},
		{"01:59:59 is outside", spec, at(1, 59, 59), false},
		{"same instant in UTC (23:30 prev day UTC = 05:30 Dhaka)", spec, at(5, 30, 0).UTC(), true},
		{"UTC clock reads 03h but Dhaka is 09h", spec, at(9, 0, 0).UTC(), false},
		{"5-field spec", "CRON_TZ=Asia/Dhaka * 2-5 * * *", at(5, 59, 0), true},
		{"5-field spec outside", "CRON_TZ=Asia/Dhaka * 2-5 * * *", at(6, 0, 0), false},
		{"no prefix uses now's location", "* 2-5 * * *", at(4, 0, 0), true},
		{"no prefix outside", "* 2-5 * * *", at(12, 0, 0), false},
		{"sub-minute precision not required", "0 30 3 * * *", at(3, 30, 42), true},
	}
	for _, c := range cases {
		got, err := InWindow(c.spec, c.now)
		if err != nil {
			t.Errorf("%s: unexpected error %v", c.name, err)
			continue
		}
		if got != c.want {
			t.Errorf("%s: InWindow = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestInWindowInvalidFailsClosed(t *testing.T) {
	for _, spec := range []string{"", "   ", "not a cron", "* * * *", "CRON_TZ=Mars/Base * * 2-5 * * *", "99 * * * *", "@every 1h", "@every 30s", "CRON_TZ=Asia/Dhaka @every 1h"} {
		ok, err := InWindow(spec, time.Now())
		if err == nil || ok {
			t.Errorf("spec %q: got (%v, %v), want (false, error)", spec, ok, err)
		}
	}
}
