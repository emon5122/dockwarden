// Package registry tracks whether the image registry (and the DNS in front of
// it) is reachable, so DockWarden stops pulling and stops restarting unhealthy
// containers while the network is down instead of hammering it.
package registry

import (
	"context"
	"errors"
	"net"
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

// Gate is a circuit breaker for registry reachability. The zero value is not
// usable; use NewGate. A nil *Gate is valid and always allows everything.
type Gate struct {
	mu       sync.Mutex
	now      func() time.Time
	min, max time.Duration

	down    bool
	backoff time.Duration
	retryAt time.Time
}

// NewGate returns a Gate that is up.
func NewGate() *Gate {
	return &Gate{now: time.Now, min: DefaultMinBackoff, max: DefaultMaxBackoff}
}

// Down reports whether the registry is currently believed unreachable.
// It has no side effects.
func (g *Gate) Down() bool {
	if g == nil {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.down
}

// Allow reports whether a registry call may be made now. While up it is always
// true. While down it is false until the backoff elapsed, then true for ONE
// caller (the probe); that caller's ReportOK/ReportFailure decides what next.
func (g *Gate) Allow() bool {
	if g == nil {
		return true
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.down {
		return true
	}
	if g.now().Before(g.retryAt) {
		return false
	}
	g.retryAt = g.now().Add(g.backoff) // lease: other callers wait for the probe
	return true
}

// ReportFailure records a registry call that failed with err. Only network
// errors (see IsNetworkError) trip the gate; it returns whether err did.
func (g *Gate) ReportFailure(err error) bool {
	if g == nil || !IsNetworkError(err) {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.down {
		g.down, g.backoff = true, g.min
	} else {
		g.backoff *= 2
		if g.backoff > g.max {
			g.backoff = g.max
		}
	}
	g.retryAt = g.now().Add(g.backoff)
	return true
}

// ReportOK records a successful registry call and reopens the gate.
func (g *Gate) ReportOK() {
	if g == nil {
		return
	}
	g.mu.Lock()
	g.down, g.backoff = false, 0
	g.mu.Unlock()
}

// RetryIn is how long until the next probe is allowed (0 when up or due).
func (g *Gate) RetryIn() time.Duration {
	if g == nil {
		return 0
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.down {
		return 0
	}
	if d := g.retryAt.Sub(g.now()); d > 0 {
		return d
	}
	return 0
}

var networkMarkers = []string{
	"no such host",
	"server misbehaving",
	"temporary failure in name resolution",
	"name resolution",
	"lookup ",
	"i/o timeout",
	"tls handshake timeout",
	"connection refused",
	"connection reset",
	"network is unreachable",
	"no route to host",
	"context deadline exceeded",
	"client.timeout exceeded",
	"request canceled while waiting for connection",
	"unexpected eof",
	"dial tcp",
}

// IsNetworkError reports whether err looks like DNS / connectivity trouble on
// the way to a registry (as opposed to "image not found" or "access denied",
// which mean the registry answered). The Docker daemon returns pull errors as
// plain text, so this matches on the message as well as on net errors.
func IsNetworkError(err error) bool {
	if err == nil {
		return false
	}
	var ne net.Error
	if errors.As(err, &ne) {
		return true
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	msg := strings.ToLower(err.Error())
	// A registry that answered with a refusal is not "down".
	for _, ok := range []string{"pull access denied", "not found", "manifest unknown", "unauthorized", "denied:"} {
		if strings.Contains(msg, ok) {
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

// SetClock replaces the gate's clock (tests only).
func (g *Gate) SetClock(now func() time.Time) {
	g.mu.Lock()
	g.now = now
	g.mu.Unlock()
}
