package health

import (
	"context"
	"sync"
	"time"

	"github.com/emon5122/dockwarden/internal/config"
	"github.com/emon5122/dockwarden/internal/docker"
	"github.com/emon5122/dockwarden/internal/notify"
	log "github.com/sirupsen/logrus"
)

const (
	// MaxRestartAttempts is the maximum number of restart attempts before giving up
	MaxRestartAttempts = 5
	// HealthCheckInterval is the interval between health checks
	HealthCheckInterval = 10 * time.Second
)

// containerState tracks the state of health monitoring for a container
type containerState struct {
	restartAttempts int
	lastImageID     string
	gaveUp          bool
	mu              sync.Mutex
}

// Watcher monitors container health and takes action using Go's native concurrency
type Watcher struct {
	client   docker.Client
	config   *config.Config
	notifier *notify.Notifier
	stopChan chan struct{}
	wg       sync.WaitGroup

	// Track container states for retry logic
	states   map[string]*containerState
	statesMu sync.RWMutex
}

// NewWatcher creates a new health watcher
func NewWatcher(client docker.Client, cfg *config.Config) *Watcher {
	var notifier *notify.Notifier
	if cfg.NotificationURL != "" {
		notifier = notify.New(cfg.NotificationURL)
	}

	return &Watcher{
		client:   client,
		config:   cfg,
		notifier: notifier,
		stopChan: make(chan struct{}),
		states:   make(map[string]*containerState),
	}
}

// Start begins health monitoring with concurrent container checks
func (w *Watcher) Start() {
	w.wg.Add(1)
	defer w.wg.Done()

	ticker := time.NewTicker(HealthCheckInterval)
	defer ticker.Stop()

	log.Info("Health watcher started")

	for {
		select {
		case <-ticker.C:
			w.checkHealthConcurrently()
		case <-w.stopChan:
			log.Info("Health watcher stopped")
			return
		}
	}
}

// Stop stops the health watcher
func (w *Watcher) Stop() {
	close(w.stopChan)
	w.wg.Wait()
}

// getContainerState gets or creates state for a container
func (w *Watcher) getContainerState(containerID string) *containerState {
	w.statesMu.Lock()
	defer w.statesMu.Unlock()

	if state, ok := w.states[containerID]; ok {
		return state
	}

	state := &containerState{}
	w.states[containerID] = state
	return state
}

// checkHealthConcurrently checks all containers for health issues using goroutines
func (w *Watcher) checkHealthConcurrently() {
	ctx := context.Background()

	containers, err := w.client.ListContainers(ctx, docker.ListOptions{
		All:           false,
		IncludeHealth: true,
	})
	if err != nil {
		log.Errorf("Failed to list containers for health check: %v", err)
		return
	}

	// Process containers concurrently using goroutines
	var wg sync.WaitGroup
	for _, ctr := range containers {
		// Skip containers that don't want health watching
		if !ctr.WatchEnabled() {
			continue
		}

		// Check scope filter
		if w.config.Scope != "" && ctr.GetScope() != w.config.Scope {
			continue
		}

		// Check label filter
		if w.config.LabelEnable && !ctr.IsEnabled(w.config.LabelName, false) {
			continue
		}

		// Process each container in its own goroutine
		wg.Add(1)
		go func(container docker.Container) {
			defer wg.Done()
			w.processContainer(ctx, container)
		}(ctr)
	}

	// Wait for all container checks to complete
	wg.Wait()
}

// processContainer handles health check for a single container
func (w *Watcher) processContainer(ctx context.Context, ctr docker.Container) {
	state := w.getContainerState(ctr.ID)
	state.mu.Lock()
	defer state.mu.Unlock()

	// Check if container image has been updated (reset attempts if new version)
	if state.lastImageID != "" && state.lastImageID != ctr.ImageID {
		log.Infof("Container %s has new image, resetting health tracking", ctr.Name)
		state.restartAttempts = 0
		state.gaveUp = false
	}
	state.lastImageID = ctr.ImageID

	// Skip if we've given up on this container version
	if state.gaveUp {
		log.Debugf("Container %s: gave up after %d attempts, waiting for new version", ctr.Name, MaxRestartAttempts)
		return
	}

	// Handle unhealthy containers
	if ctr.IsUnhealthy() {
		w.handleUnhealthy(ctx, ctr, state)
	} else if ctr.IsHealthy() {
		// Reset attempts if container is now healthy
		if state.restartAttempts > 0 {
			log.Infof("Container %s is now healthy after %d restart(s)", ctr.Name, state.restartAttempts)
			state.restartAttempts = 0
		}
	}
}

// handleUnhealthy handles an unhealthy container with retry logic
func (w *Watcher) handleUnhealthy(ctx context.Context, ctr docker.Container, state *containerState) {
	log.Warnf("Container %s is unhealthy (attempt %d/%d)", ctr.Name, state.restartAttempts+1, MaxRestartAttempts)

	// Check if we've exceeded max attempts
	if state.restartAttempts >= MaxRestartAttempts {
		log.Errorf("Container %s: giving up after %d restart attempts. Will retry when new version is available.", ctr.Name, MaxRestartAttempts)
		state.gaveUp = true

		// Send notification about giving up
		if w.notifier != nil {
			w.notifier.NotifyContainerGaveUp(ctr.Name, ctr.Image, MaxRestartAttempts)
		}
		return
	}

	switch w.config.HealthAction {
	case "restart":
		state.restartAttempts++
		log.Infof("Restarting unhealthy container %s (attempt %d/%d)", ctr.Name, state.restartAttempts, MaxRestartAttempts)

		// Send notification about unhealthy state
		if w.notifier != nil {
			w.notifier.NotifyContainerUnhealthy(ctr.Name, ctr.Image, state.restartAttempts)
		}

		timeout := ctr.GetStopTimeout(w.config.StopTimeout)
		if err := w.client.RestartContainer(ctx, ctr.ID, timeout); err != nil {
			log.Errorf("Failed to restart unhealthy container %s: %v", ctr.Name, err)
		} else {
			log.Infof("Restart initiated for container %s", ctr.Name)
		}

	case "notify":
		log.Infof("Notifying about unhealthy container %s (attempt %d/%d)", ctr.Name, state.restartAttempts+1, MaxRestartAttempts)
		state.restartAttempts++

		// Send notification
		if w.notifier != nil {
			w.notifier.NotifyContainerUnhealthy(ctr.Name, ctr.Image, state.restartAttempts)
		}

	default:
		log.Debugf("No action configured for unhealthy container %s", ctr.Name)
	}
}

// ResetContainer resets tracking for a container (called when container is updated)
func (w *Watcher) ResetContainer(containerID string) {
	w.statesMu.Lock()
	defer w.statesMu.Unlock()

	if state, ok := w.states[containerID]; ok {
		state.mu.Lock()
		state.restartAttempts = 0
		state.gaveUp = false
		state.lastImageID = ""
		state.mu.Unlock()
	}
}

// GetStats returns current health monitoring statistics
func (w *Watcher) GetStats() map[string]interface{} {
	w.statesMu.RLock()
	defer w.statesMu.RUnlock()

	gaveUp := 0
	monitored := len(w.states)

	for _, state := range w.states {
		state.mu.Lock()
		if state.gaveUp {
			gaveUp++
		}
		state.mu.Unlock()
	}

	return map[string]interface{}{
		"monitored_containers": monitored,
		"gave_up_containers":   gaveUp,
		"max_restart_attempts": MaxRestartAttempts,
	}
}
