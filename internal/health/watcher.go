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
	restartAttempts      int
	lastImageID          string
	gaveUp               bool
	lastSeenRunning      bool
	consecutiveUnhealthy int
	observationStart     time.Time
	observed             bool
	mu                   sync.Mutex
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
	// Fast path: read-lock check
	w.statesMu.RLock()
	if state, ok := w.states[containerID]; ok {
		w.statesMu.RUnlock()
		return state
	}
	w.statesMu.RUnlock()

	// Slow path: write-lock for creation
	w.statesMu.Lock()
	defer w.statesMu.Unlock()

	// Double-check after acquiring write lock
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

	// List ALL containers including stopped to detect crashed ones
	containers, err := w.client.ListContainers(ctx, docker.ListOptions{
		All:           true,
		IncludeHealth: true,
	})
	if err != nil {
		log.Errorf("Failed to list containers for health check: %v", err)
		return
	}

	// Build set of current container IDs to prune stale state entries
	currentIDs := make(map[string]struct{}, len(containers))
	for _, ctr := range containers {
		currentIDs[ctr.ID] = struct{}{}
	}
	w.statesMu.Lock()
	for id := range w.states {
		if _, exists := currentIDs[id]; !exists {
			delete(w.states, id)
		}
	}
	w.statesMu.Unlock()

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
		state.lastSeenRunning = false
	}
	state.lastImageID = ctr.ImageID

	// Skip if we've given up on this container version
	if state.gaveUp {
		log.Debugf("Container %s: gave up after %d attempts, waiting for new version", ctr.Name, MaxRestartAttempts)
		return
	}

	// Detect crashed/exited containers that were previously running
	if !ctr.IsRunning() && state.lastSeenRunning {
		log.Warnf("Container %s has stopped unexpectedly (state: %s)", ctr.Name, ctr.State)
		w.handleCrashed(ctx, ctr, state)
		return
	}

	// Track running state
	if ctr.IsRunning() {
		state.lastSeenRunning = true
	}

	// Handle unhealthy containers
	if ctr.IsUnhealthy() {
		w.handleUnhealthy(ctx, ctr, state)
	} else if ctr.IsHealthy() {
		// Reset attempts and observation counters if container is now healthy
		if state.restartAttempts > 0 {
			log.Infof("Container %s is now healthy after %d restart(s)", ctr.Name, state.restartAttempts)
			state.restartAttempts = 0
		}
		state.consecutiveUnhealthy = 0
		state.observed = false
	}
}

// handleUnhealthy handles an unhealthy container with retry logic
func (w *Watcher) handleUnhealthy(ctx context.Context, ctr docker.Container, state *containerState) {
	// Increment consecutive unhealthy count
	state.consecutiveUnhealthy++
	log.Warnf("Container %s is unhealthy (check %d/%d, restart %d/%d)", ctr.Name, state.consecutiveUnhealthy, w.config.UnhealthyThreshold, state.restartAttempts+1, MaxRestartAttempts)

	// If we have not yet started observation, start timer
	if !state.observed {
		state.observationStart = time.Now()
		state.observed = true
		log.Infof("Started observation period for container %s", ctr.Name)
	}

	// Check if observation period has passed and threshold reached
	if state.consecutiveUnhealthy >= w.config.UnhealthyThreshold && time.Since(state.observationStart) >= w.config.ObservationPeriod {
		// Reset observation flags
		state.observed = false
		state.consecutiveUnhealthy = 0

		// Proceed with existing restart/notify logic
		if state.restartAttempts >= MaxRestartAttempts {
			log.Errorf("Container %s: giving up after %d restart attempts. Will retry when new version is available.", ctr.Name, MaxRestartAttempts)
			state.gaveUp = true
			if w.notifier != nil {
				w.notifier.NotifyContainerGaveUp(ctr.Name, ctr.Image, MaxRestartAttempts)
			}
			return
		}

		switch w.config.HealthAction {
		case "restart":
			state.restartAttempts++
			log.Infof("Restarting unhealthy container %s (attempt %d/%d)", ctr.Name, state.restartAttempts, MaxRestartAttempts)

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
			state.restartAttempts++
			log.Infof("Notifying about unhealthy container %s (attempt %d/%d)", ctr.Name, state.restartAttempts, MaxRestartAttempts)
			if w.notifier != nil {
				w.notifier.NotifyContainerUnhealthy(ctr.Name, ctr.Image, state.restartAttempts)
			}

		default:
			log.Debugf("No action configured for unhealthy container %s", ctr.Name)
		}
	} else {
		log.Infof("Container %s unhealthy but within observation period (%d/%d) or threshold not met", ctr.Name, state.consecutiveUnhealthy, w.config.UnhealthyThreshold)
	}
}

// handleCrashed handles a container that has stopped unexpectedly
func (w *Watcher) handleCrashed(ctx context.Context, ctr docker.Container, state *containerState) {
	log.Warnf("Container %s crashed/exited (attempt %d/%d)", ctr.Name, state.restartAttempts+1, MaxRestartAttempts)

	if state.restartAttempts >= MaxRestartAttempts {
		log.Errorf("Container %s: giving up after %d restart attempts for crashed container. Will retry when new version is available.", ctr.Name, MaxRestartAttempts)
		state.gaveUp = true
		state.lastSeenRunning = false

		if w.notifier != nil {
			w.notifier.NotifyContainerGaveUp(ctr.Name, ctr.Image, MaxRestartAttempts)
		}
		return
	}

	state.restartAttempts++

	if w.notifier != nil {
		w.notifier.NotifyContainerUnhealthy(ctr.Name, ctr.Image, state.restartAttempts)
	}

	// If start fails, reset lastSeenRunning to avoid rapid retry loops
	log.Infof("Starting crashed container %s (attempt %d/%d)", ctr.Name, state.restartAttempts, MaxRestartAttempts)
	if err := w.client.StartContainer(ctx, ctr.ID); err != nil {
		log.Errorf("Failed to start crashed container %s: %v", ctr.Name, err)
		state.lastSeenRunning = false
	} else {
		log.Infof("Started crashed container %s", ctr.Name)
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
		state.lastSeenRunning = false
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
