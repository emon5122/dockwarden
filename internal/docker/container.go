package docker

import (
	"strconv"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
)

// Container represents a Docker container
type Container struct {
	ID           string
	Name         string
	Image        string
	ImageID      string
	State        string
	Status       string
	Labels       map[string]string
	Created      time.Time
	HealthStatus string
}

// IsRunning returns true if the container is running
func (c Container) IsRunning() bool {
	return c.State == "running"
}

// IsRestarting returns true if the container is in the restarting state
func (c Container) IsRestarting() bool {
	return c.State == "restarting"
}

// IsHealthy returns true if the container is healthy
func (c Container) IsHealthy() bool {
	return c.HealthStatus == "" || c.HealthStatus == "healthy"
}

// IsUnhealthy returns true if the container is unhealthy
func (c Container) IsUnhealthy() bool {
	return c.HealthStatus == "unhealthy"
}

// GetLabel returns a label value or empty string
func (c Container) GetLabel(key string) string {
	if c.Labels == nil {
		return ""
	}
	return c.Labels[key]
}

// HasLabel returns true if the container has the specified label
func (c Container) HasLabel(key string) bool {
	if c.Labels == nil {
		return false
	}
	_, ok := c.Labels[key]
	return ok
}

// IsEnabled returns true if the container should be managed by DockWarden
func (c Container) IsEnabled(labelName string, defaultEnabled bool) bool {
	if !c.HasLabel(labelName) {
		return defaultEnabled
	}
	return c.GetLabel(labelName) == "true"
}

// UpdateEnabled returns true if updates are enabled for this container
func (c Container) UpdateEnabled() bool {
	label := c.GetLabel("dockwarden.update.enable")
	if label == "" {
		return true // Default to enabled
	}
	return label == "true"
}

// WatchEnabled returns true if health watching is enabled for this container
func (c Container) WatchEnabled() bool {
	label := c.GetLabel("dockwarden.watch.enable")
	if label == "" {
		return true // Default to enabled
	}
	return label == "true"
}

// GetStopSignal returns the stop signal from the dockwarden.stop-signal label,
// or "" when unset. Empty means "let the daemon decide", which honours the
// container's own STOPSIGNAL (from the image or create config) — returning a
// hardcoded SIGTERM here would override images like nginx that declare SIGQUIT.
func (c Container) GetStopSignal() string {
	return c.GetLabel("dockwarden.stop-signal")
}

// GetStopTimeout returns the configured stop timeout or default.
//
// The documented label form is a bare number of seconds ("60"), but a Go
// duration ("30s", "1m") is also accepted. The bare form must be tried first:
// naively appending "s" turned "1m" into "1ms" — a one-millisecond grace
// period before SIGKILL — and turned "30s" into the unparseable "30ss",
// silently discarding the label.
func (c Container) GetStopTimeout(defaultTimeout time.Duration) time.Duration {
	raw := strings.TrimSpace(c.GetLabel("dockwarden.stop-timeout"))
	if raw == "" {
		return defaultTimeout
	}

	if seconds, err := strconv.ParseFloat(raw, 64); err == nil {
		return time.Duration(seconds * float64(time.Second))
	}
	if timeout, err := time.ParseDuration(raw); err == nil {
		return timeout
	}

	log.Warnf("Container %s: unparseable dockwarden.stop-timeout %q, using default %s", c.Name, raw, defaultTimeout)
	return defaultTimeout
}

// GetScope returns the scope label value
func (c Container) GetScope() string {
	return c.GetLabel("dockwarden.scope")
}
