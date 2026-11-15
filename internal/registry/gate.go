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

	// Rate-limit (429) holds: the first wait, doubling per consecutive 429, capped.
	// The Docker daemon API does not expose Retry-After / RateLimit-Reset, so the
	// wait cannot follow the reset header; exponential with a 1 h cap is the bound.
	RateLimitMinHold = 10 * time.Minute
	RateLimitMaxHold = time.Hour
	// Refusal (401/403/denied/not found) holds are per image ref.
	RefusedMinHold = 15 * time.Minute
	RefusedMaxHold = 6 * time.Hour
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
	// pull holds, kept apart from the network "down" state: a hold stops pulls
	// only, never the health watcher's restarts (see Down / AnyDown).
	rate    map[string]*hold // by registry host
	refused map[string]*hold // by image ref
	now     func() time.Time
	// Probe checks one registry host; nil error = reachable. Tests replace it.
	Probe func(ctx context.Context, host string) error
}

// NewGates returns Gates with everything up and the default HTTPS probe.
func NewGates() *Gates {
	return &Gates{min: DefaultMinBackoff, max: DefaultMaxBackoff, hosts: map[string]*hostState{},
		rate: map[string]*hold{}, refused: map[string]*hold{}, now: time.Now, Probe: httpProbe}
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

// Kind classifies a registry error.
type Kind int

const (
	KindNone      Kind = iota
	KindNetwork        // DNS / connectivity (see IsNetworkError)
	KindRateLimit      // HTTP 429 / toomanyrequests
	KindRefused        // 401/403/denied/unknown repository or tag
)

var rateLimitMarkers = []string{"toomanyrequests", "too many requests", "rate limit", "status code 429"}

var refusedMarkers = []string{
	"pull access denied",
	"unauthorized",
	"denied:",
	": denied", // ghcr.io: `error from registry: denied`, `Head "...": denied`
	"requested access to the resource is denied",
	"authentication required",
	"no basic auth credentials",
	"status code 401",
	"status code 403",
	"manifest unknown",
	"repository does not exist",
}

// Classify says what kind of registry failure err is. Network errors keep the
// meaning of IsNetworkError and are checked first.
func Classify(err error) Kind {
	if err == nil {
		return KindNone
	}
	if IsNetworkError(err) {
		return KindNetwork
	}
	msg := strings.ToLower(err.Error())
	for _, m := range rateLimitMarkers {
		if strings.Contains(msg, m) {
			return KindRateLimit
		}
	}
	for _, m := range refusedMarkers {
		if strings.Contains(msg, m) {
			return KindRefused
		}
	}
	return KindNone
}

type hold struct {
	until   time.Time
	backoff time.Duration
}

// SetClock replaces the clock used for holds (tests).
func (g *Gates) SetClock(now func() time.Time) {
	g.mu.Lock()
	g.now = now
	g.mu.Unlock()
}

// Held reports whether pulls of ref are on hold: its host answered 429 or the
// ref itself was refused, and the wait has not elapsed. Unlike Down it is
// never consulted by the health watcher.
func (g *Gates) Held(ref string) bool {
	if g == nil {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.now()
	if h := g.rate[HostOf(ref)]; h != nil && now.Before(h.until) {
		return true
	}
	h := g.refused[ref]
	return h != nil && now.Before(h.until)
}

// HeldCount is the number of active holds (hosts rate limited + refs refused).
func (g *Gates) HeldCount() int {
	if g == nil {
		return 0
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	now, n := g.now(), 0
	for _, h := range g.rate {
		if now.Before(h.until) {
			n++
		}
	}
	for _, h := range g.refused {
		if now.Before(h.until) {
			n++
		}
	}
	return n
}

func (g *Gates) extend(m map[string]*hold, key string, min, max time.Duration) (time.Duration, bool) {
	now := g.now()
	h := m[key]
	switch {
	case h == nil:
		h = &hold{backoff: min}
		m[key] = h
	case now.Before(h.until):
		// A failure from a call admitted before the hold took effect: no escalation.
		return h.until.Sub(now), false
	default:
		h.backoff *= 2
		if h.backoff > max {
			h.backoff = max
		}
	}
	h.until = now.Add(h.backoff)
	return h.backoff, true
}

// Hold records a 429 (per host) or a refusal (per ref) for err, per Classify,
// and returns the wait and whether this call started or escalated it (false =
// already on hold, nothing changed). Other kinds change nothing.
func (g *Gates) Hold(ref string, err error) (Kind, time.Duration, bool) {
	k := Classify(err)
	if g == nil || (k != KindRateLimit && k != KindRefused) {
		return k, 0, false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if k == KindRateLimit {
		d, first := g.extend(g.rate, HostOf(ref), RateLimitMinHold, RateLimitMaxHold)
		return k, d, first
	}
	d, first := g.extend(g.refused, ref, RefusedMinHold, RefusedMaxHold)
	return k, d, first
}

// Release records a successful registry call for ref: it clears the host's
// rate-limit state and ref's own refusal, nothing else.
func (g *Gates) Release(ref string) {
	if g == nil {
		return
	}
	g.mu.Lock()
	delete(g.rate, HostOf(ref))
	delete(g.refused, ref)
	g.mu.Unlock()
}

// ResetHolds drops every hold (a manual "update now").
func (g *Gates) ResetHolds() {
	if g == nil {
		return
	}
	g.mu.Lock()
	g.rate, g.refused = map[string]*hold{}, map[string]*hold{}
	g.mu.Unlock()
}
