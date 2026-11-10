package registry

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

var dns = errors.New("dial tcp: lookup ghcr.io: no such host")

func newTestGates(probe func(ctx context.Context, host string) error) *Gates {
	g := NewGates()
	g.SetBackoff(5*time.Millisecond, 20*time.Millisecond)
	g.Probe = probe
	return g
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for i := 0; i < 300; i++ {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestHostOf(t *testing.T) {
	for ref, want := range map[string]string{
		"emon5122/dockwarden:latest":    "docker.io",
		"nginx":                         "docker.io",
		"docker.io/library/nginx:1":     "docker.io",
		"index.docker.io/library/nginx": "docker.io",
		"ghcr.io/nexisltd/camgw:latest": "ghcr.io",
		"registry.local:5000/a/b:1":     "registry.local:5000",
		"localhost/x":                   "localhost",
		"ghcr.io/a/b@sha256:0123":       "ghcr.io",
		"sha256:0123456789abcdef":       "docker.io",
	} {
		if got := HostOf(ref); got != want {
			t.Errorf("HostOf(%q)=%q, want %q", ref, got, want)
		}
	}
}

func TestTripOnlyForNetworkErrorsAndPerHost(t *testing.T) {
	g := newTestGates(func(ctx context.Context, host string) error { return dns })
	g.SetBackoff(time.Hour, time.Hour)
	if g.Trip("ghcr.io/a/b", errors.New("pull access denied")) || g.Down("ghcr.io/a/b") {
		t.Fatal("a refusal must not trip the gate")
	}
	if !g.Trip("ghcr.io/a/b", dns) || !g.Down("ghcr.io/a/b") {
		t.Fatal("DNS failure must trip the gate")
	}
	if g.Down("emon5122/dockwarden") || !g.AnyDown() {
		t.Fatal("gate must be per host")
	}
	g.OK("ghcr.io/a/b")
	if g.Down("ghcr.io/a/b") || g.AnyDown() {
		t.Fatal("OK must reopen the gate")
	}
}

func TestProberReopensGateWhenRegistryAnswers(t *testing.T) {
	var up atomic.Bool
	var probes atomic.Int32
	g := newTestGates(func(ctx context.Context, host string) error {
		probes.Add(1)
		if up.Load() {
			return nil
		}
		return dns
	})
	g.Trip("ghcr.io/a/b", dns)
	eventually(t, "two failed probes", func() bool { return probes.Load() >= 2 })
	if !g.Down("ghcr.io/a/b") {
		t.Fatal("still unreachable, gate must stay down")
	}
	up.Store(true)
	eventually(t, "gate reopen", func() bool { return !g.Down("ghcr.io/a/b") })
	// A later outage starts a fresh prober.
	up.Store(false)
	g.Trip("ghcr.io/a/b", dns)
	if !g.Down("ghcr.io/a/b") {
		t.Fatal("second outage must trip again")
	}
	up.Store(true)
	eventually(t, "second reopen", func() bool { return !g.Down("ghcr.io/a/b") })
}

// In-flight failures admitted before the gate tripped must not escalate the
// backoff; only the prober's own failed probes do.
func TestTripWhileDownDoesNotEscalateBackoff(t *testing.T) {
	g := newTestGates(func(ctx context.Context, host string) error { return dns })
	g.SetBackoff(time.Hour, 2*time.Hour)
	g.Trip("ghcr.io/a/b", dns)
	for i := 0; i < 10; i++ {
		g.Trip("ghcr.io/a/b", dns)
	}
	g.mu.Lock()
	b := g.hosts["ghcr.io"].backoff
	g.mu.Unlock()
	if b != time.Hour {
		t.Fatalf("backoff=%s after in-flight failures, want the initial %s", b, time.Hour)
	}
}

func TestNilGatesAllowEverything(t *testing.T) {
	var g *Gates
	if g.Down("x") || g.AnyDown() || g.Trip("x", dns) {
		t.Fatal("nil gates must be a no-op")
	}
	g.OK("x")
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
		"Cannot connect to the Docker daemon at unix:///var/run/docker.sock: connection refused",
		"Get \"http://%2Fvar%2Frun%2Fdocker.sock/v1.50/containers/json\": dial unix /var/run/docker.sock: connect: connection refused",
		"context deadline exceeded",
		"unexpected EOF",
		"failed to lookup container",
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
