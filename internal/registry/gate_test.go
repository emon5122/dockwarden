package registry

import (
	"errors"
	"testing"
	"time"
)

func TestGateBackoffAndProbe(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	g := NewGate()
	g.SetClock(func() time.Time { return now })
	dns := errors.New("dial tcp: lookup ghcr.io: no such host")

	if g.Down() || !g.Allow() {
		t.Fatal("new gate must be up")
	}
	if !g.ReportFailure(dns) || !g.Down() {
		t.Fatal("network error must trip the gate")
	}
	if g.Allow() {
		t.Fatal("must not allow inside backoff")
	}
	now = now.Add(DefaultMinBackoff)
	if !g.Allow() {
		t.Fatal("first caller after backoff is the probe")
	}
	if g.Allow() {
		t.Fatal("only one probe at a time")
	}
	g.ReportFailure(dns) // probe failed: backoff doubles
	if got := g.RetryIn(); got != 2*DefaultMinBackoff {
		t.Fatalf("RetryIn=%s, want %s", got, 2*DefaultMinBackoff)
	}
	for i := 0; i < 20; i++ {
		g.ReportFailure(dns)
	}
	if got := g.RetryIn(); got != DefaultMaxBackoff {
		t.Fatalf("backoff not capped: %s", got)
	}
	g.ReportOK()
	if g.Down() || !g.Allow() {
		t.Fatal("ReportOK must reopen the gate")
	}
}

func TestNilGateAllowsEverything(t *testing.T) {
	var g *Gate
	if g.Down() || !g.Allow() || g.ReportFailure(errors.New("no such host")) {
		t.Fatal("nil gate must be a no-op")
	}
	g.ReportOK()
}

func TestIsNetworkError(t *testing.T) {
	yes := []string{
		"Get \"https://registry-1.docker.io/v2/\": dial tcp: lookup registry-1.docker.io on 127.0.0.11:53: server misbehaving",
		"dial tcp: lookup ghcr.io: no such host",
		"net/http: TLS handshake timeout",
		"Get https://x/v2/: i/o timeout",
		"dial tcp 1.2.3.4:443: connect: connection refused",
	}
	no := []string{
		"pull access denied for sha256, repository does not exist",
		"manifest unknown",
		"unauthorized: authentication required",
		"image not found",
		"invalid reference format",
	}
	for _, m := range yes {
		if !IsNetworkError(errors.New(m)) {
			t.Errorf("%q should be a network error", m)
		}
	}
	for _, m := range no {
		if IsNetworkError(errors.New(m)) {
			t.Errorf("%q should not be a network error", m)
		}
	}
	if IsNetworkError(nil) {
		t.Error("nil")
	}
}
