package docker

import "time"

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

// GetStopSignal returns the configured stop signal or default
func (c Container) GetStopSignal() string {
	signal := c.GetLabel("dockwarden.stop-signal")
	if signal == "" {
		return "SIGTERM"
	}
	return signal
}

// GetStopTimeout returns the configured stop timeout or default
func (c Container) GetStopTimeout(defaultTimeout time.Duration) time.Duration {
	timeoutStr := c.GetLabel("dockwarden.stop-timeout")
	if timeoutStr == "" {
		return defaultTimeout
	}

	timeout, err := time.ParseDuration(timeoutStr + "s")
	if err != nil {
		return defaultTimeout
	}
	return timeout
}

// GetScope returns the scope label value
func (c Container) GetScope() string {
	return c.GetLabel("dockwarden.scope")
}
