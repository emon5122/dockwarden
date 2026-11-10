// Package registry tracks, per registry host, whether the registry (and the
// DNS in front of it) is reachable, so DockWarden stops pulling and stops
// restarting unhealthy containers while the network is down instead of
// hammering it. A background prober, not the update loop, decides when it is
// back.
package registry

import (
	"context"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	// DefaultMinBackoff is the first wait after a network failure.
	DefaultMinBackoff = 30 * time.Second
	// DefaultMaxBackoff caps the exponential wait.
	DefaultMaxBackoff = 10 * time.Minute
)

// HostOf returns the registry host of an image ref ("docker.io" when the ref
// names none): "ghcr.io/x/y:1" -> "ghcr.io", "emon5122/dockwarden" -> "docker.io".
func HostOf(ref string) string {
	if i := strings.Index(ref, "@"); i >= 0 {
		ref = ref[:i]
	}
	first, _, found := strings.Cut(ref, "/")
	if !found {
		return "docker.io"
	}
	if strings.ContainsAny(first, ".:") || first == "localhost" {
		if first == "index.docker.io" {
			return "docker.io"
		}
		return first
	}
	return "docker.io"
}

type hostState struct {
	down    bool
	backoff time.Duration
	proving bool // a prober goroutine is running
}

// Gates is a set of per-registry-host circuit breakers. A nil *Gates is valid
// and never reports anything down.
type Gates struct {
	mu       sync.Mutex
	min, max time.Duration
	hosts    map[string]*hostState
	// Probe checks one registry host; nil error = reachable. Tests replace it.
	Probe func(ctx context.Context, host string) error
}

// NewGates returns Gates with everything up and the default HTTPS probe.
func NewGates() *Gates {
	return &Gates{min: DefaultMinBackoff, max: DefaultMaxBackoff, hosts: map[string]*hostState{}, Probe: httpProbe}
}

// SetBackoff overrides the backoff bounds (tests).
func (g *Gates) SetBackoff(min, max time.Duration) {
	g.mu.Lock()
	g.min, g.max = min, max
	g.mu.Unlock()
}

// Down reports whether the registry of image ref is currently believed unreachable.
func (g *Gates) Down(ref string) bool {
	if g == nil {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	h := g.hosts[HostOf(ref)]
	return h != nil && h.down
}

// AnyDown reports whether any registry is believed unreachable.
func (g *Gates) AnyDown() bool {
	if g == nil {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, h := range g.hosts {
		if h.down {
			return true
		}
	}
	return false
}

// Trip records a failed registry call for image ref. Only network errors (see
// IsNetworkError) trip a gate; it returns whether err did. Failures reported
// while the host is already down (calls admitted before it tripped) change
// nothing: only the prober escalates the backoff. The first trip starts the prober.
func (g *Gates) Trip(ref string, err error) bool {
	if g == nil || !IsNetworkError(err) {
		return false
	}
	host := HostOf(ref)
	g.mu.Lock()
	h := g.hosts[host]
	if h == nil {
		h = &hostState{}
		g.hosts[host] = h
	}
	if !h.down {
		h.down, h.backoff = true, g.min
	}
	start := !h.proving
	h.proving = true
	g.mu.Unlock()
	if start {
		go g.prober(host)
	}
	return true
}

// OK records that the registry of ref answered (any answer, including a
// refusal such as "denied"): it is reachable.
func (g *Gates) OK(ref string) {
	if g == nil {
		return
	}
	g.mu.Lock()
	if h := g.hosts[HostOf(ref)]; h != nil {
		h.down, h.backoff = false, 0
	}
	g.mu.Unlock()
}

// prober retries a down host with exponential backoff until it answers.
func (g *Gates) prober(host string) {
	for {
		g.mu.Lock()
		h := g.hosts[host]
		wait := h.backoff
		g.mu.Unlock()
		time.Sleep(wait)

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		err := g.Probe(ctx, host)
		cancel()

		g.mu.Lock()
		if err == nil || !h.down {
			h.down, h.backoff, h.proving = false, 0, false
			g.mu.Unlock()
			return
		}
		h.backoff *= 2
		if h.backoff > g.max {
			h.backoff = g.max
		}
		g.mu.Unlock()
	}
}

// httpProbe resolves host and asks its /v2/ endpoint; any HTTP answer (even
// 401) means the registry is reachable.
func httpProbe(ctx context.Context, host string) error {
	if host == "docker.io" {
		host = "registry-1.docker.io"
	}
	if _, err := net.DefaultResolver.LookupHost(ctx, strings.Split(host, ":")[0]); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+host+"/v2/", nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

var networkMarkers = []string{
	"no such host",
	"server misbehaving",
	"temporary failure in name resolution",
	"i/o timeout",
	"tls handshake timeout",
	"connection refused",
	"connection reset by peer",
	"network is unreachable",
	"no route to host",
	"client.timeout exceeded",
	"dial tcp",
}

// IsNetworkError reports whether err looks like DNS / connectivity trouble on
// the way to a registry (as opposed to "image not found" or "access denied",
// which mean the registry answered). The Docker daemon returns pull errors as
// plain text, so this matches the message. Errors about the local daemon
// socket never count.
func IsNetworkError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, no := range []string{"pull access denied", "not found", "manifest unknown", "unauthorized", "denied:", "docker daemon", "docker.sock", "dial unix"} {
		if strings.Contains(msg, no) {
			return false
		}
	}
	for _, m := range networkMarkers {
		if strings.Contains(msg, m) {
			return true
		}
	}
	return false
}
