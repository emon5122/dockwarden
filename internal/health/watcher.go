package health

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/emon5122/dockwarden/internal/config"
	"github.com/emon5122/dockwarden/internal/docker"
	"github.com/emon5122/dockwarden/internal/notify"
	"github.com/emon5122/dockwarden/internal/registry"
	log "github.com/sirupsen/logrus"
)

const (
	// MaxRestartAttempts is the maximum number of restarts allowed within
	// Config.RestartWindow before giving up.
	MaxRestartAttempts = 5
	// HealthCheckInterval is the interval between health checks
	HealthCheckInterval = 10 * time.Second
	// DefaultRestartWindow is used when Config.RestartWindow is unset.
	DefaultRestartWindow = time.Hour
)

// containerState tracks the state of health monitoring for a container
type containerState struct {
	// restarts holds the timestamps of restarts we initiated, pruned to the
	// rolling window. It is deliberately NOT a consecutive-failure counter: a
	// container that recovers briefly after each restart would reset such a
	// counter forever and never trip the breaker, so a flapping container got
	// restarted indefinitely and never notified. See docs/known-issues.md.
	restarts             []time.Time
	lastImageID          string
	gaveUp               bool
	lastSeenRunning      bool
	consecutiveUnhealthy int
	observationStart     time.Time
	observed             bool
	mu                   sync.Mutex
}

// pruneRestarts drops restart timestamps that have fallen out of the window.
// Callers must hold state.mu.
func (s *containerState) pruneRestarts(now time.Time, window time.Duration) {
	cutoff := now.Add(-window)
	kept := s.restarts[:0]
	for _, at := range s.restarts {
		if at.After(cutoff) {
			kept = append(kept, at)
		}
	}
	s.restarts = kept
}

// restartCount returns how many restarts occurred within the window.
// Callers must hold state.mu.
func (s *containerState) restartCount(now time.Time, window time.Duration) int {
	s.pruneRestarts(now, window)
	return len(s.restarts)
}

// recordRestart registers a restart and returns the new in-window count.
// Callers must hold state.mu.
func (s *containerState) recordRestart(now time.Time, window time.Duration) int {
	s.restarts = append(s.restarts, now)
	s.pruneRestarts(now, window)
	return len(s.restarts)
}

// Watcher monitors container health and takes action using Go's native concurrency
type Watcher struct {
	client   docker.Client
	config   *config.Config
	notifier *notify.Notifier
	stopChan chan struct{}
	wg       sync.WaitGroup

	// gates, when set, hold back unhealthy-restarts while the registry/DNS is down
	gates *registry.Gates

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

// registryDown reports whether the registry of ctr's image is unreachable. The
// list summary shows a bare image id once the tag moved; then no host is known
// and any outage counts.
func (w *Watcher) registryDown(ctr docker.Container) bool {
	if strings.HasPrefix(ctr.Image, "sha256:") {
		return w.gates.AnyDown()
	}
	return w.gates.Down(ctr.Image)
}

// SetGates makes the watcher hold restarts of unhealthy containers while the
// registry/DNS of its image is unreachable (a restart cannot fix that and DNS-dependent
// services restart-loop: camgw, 2026-11-10). Call before Start.
func (w *Watcher) SetGates(g *registry.Gates) { w.gates = g }

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

	// Process containers concurrently using goroutines (with semaphore to limit concurrency)
	maxConcurrency := 10
	semaphore := make(chan struct{}, maxConcurrency)
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
		semaphore <- struct{}{}
		go func(container docker.Container) {
			defer wg.Done()
			defer func() { <-semaphore }()
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
		state.restarts = nil
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
		// Docker's own restart policy is already bringing it back, or someone
		// paused it on purpose: neither is ours to start. Keep lastSeenRunning
		// so a container that ends up exited is still evaluated next tick.
		if ctr.IsRestarting() || ctr.IsPaused() {
			log.Debugf("Container %s is %s, leaving it to Docker", ctr.Name, ctr.State)
			return
		}
		w.handleStopped(ctx, ctr, state)
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
		// Clear the observation counters, but deliberately NOT the restart
		// history: recovering after a restart is exactly what a flapping
		// container does, and forgetting it here is what let one be restarted
		// forever. The history ages out of the rolling window on its own.
		if recent := state.restartCount(time.Now(), w.restartWindow()); recent > 0 {
			log.Infof("Container %s is healthy (%d restart(s) within the last %s)", ctr.Name, recent, w.restartWindow())
		}
		state.consecutiveUnhealthy = 0
		state.observed = false
	}
}

// restartWindow returns the rolling window over which restarts are counted.
func (w *Watcher) restartWindow() time.Duration {
	if w.config.RestartWindow > 0 {
		return w.config.RestartWindow
	}
	return DefaultRestartWindow
}

// handleUnhealthy handles an unhealthy container with retry logic
func (w *Watcher) handleUnhealthy(ctx context.Context, ctr docker.Container, state *containerState) {
	now := time.Now()
	window := w.restartWindow()

	// Increment consecutive unhealthy count
	state.consecutiveUnhealthy++
	log.Warnf("Container %s is unhealthy (check %d/%d, restart %d/%d in %s)", ctr.Name, state.consecutiveUnhealthy, w.config.UnhealthyThreshold, state.restartCount(now, window)+1, MaxRestartAttempts, window)

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
		if state.restartCount(now, window) >= MaxRestartAttempts {
			log.Errorf("Container %s: giving up after %d restarts within %s. Restarting is not fixing it — check whether it is overloaded rather than dead. Will retry when new version is available.", ctr.Name, MaxRestartAttempts, window)
			state.gaveUp = true
			if w.notifier != nil {
				w.notifier.NotifyContainerGaveUp(ctr.Name, ctr.Image, MaxRestartAttempts)
			}
			return
		}

		if w.config.HealthAction == "restart" && w.registryDown(ctr) {
			log.Warnf("Container %s is unhealthy but the registry/DNS is unreachable: not restarting it (outage, not a container fault); will re-observe once it is back", ctr.Name)
			return
		}

		switch w.config.HealthAction {
		case "restart":
			attempt := state.recordRestart(now, window)
			log.Infof("Restarting unhealthy container %s (attempt %d/%d in %s)", ctr.Name, attempt, MaxRestartAttempts, window)

			if w.notifier != nil {
				w.notifier.NotifyContainerUnhealthy(ctr.Name, ctr.Image, attempt)
			}

			timeout := ctr.GetStopTimeout(w.config.StopTimeout)
			if err := w.client.RestartContainer(ctx, ctr.ID, timeout, ctr.GetStopSignal()); err != nil {
				log.Errorf("Failed to restart unhealthy container %s: %v", ctr.Name, err)
			} else {
				log.Infof("Restart initiated for container %s", ctr.Name)
			}

		case "notify":
			// Recorded in the same window so a flapping container stops
			// generating notifications too, instead of alerting hourly forever.
			attempt := state.recordRestart(now, window)
			log.Infof("Notifying about unhealthy container %s (attempt %d/%d in %s)", ctr.Name, attempt, MaxRestartAttempts, window)
			if w.notifier != nil {
				w.notifier.NotifyContainerUnhealthy(ctr.Name, ctr.Image, attempt)
			}

		default:
			log.Debugf("No action configured for unhealthy container %s", ctr.Name)
		}
	} else {
		log.Infof("Container %s unhealthy but within observation period (%d/%d) or threshold not met", ctr.Name, state.consecutiveUnhealthy, w.config.UnhealthyThreshold)
	}
}

// deliberateStopExitCodes are the exit codes a deliberate stop leaves behind:
// a clean exit, or death by the signals `docker stop` / `docker kill` send
// (SIGINT 130, SIGQUIT 131 e.g. nginx's STOPSIGNAL, SIGKILL 137 after the stop
// timeout, SIGTERM 143). An OOM kill also exits 137 but sets OOMKilled.
var deliberateStopExitCodes = map[int]bool{0: true, 130: true, 131: true, 137: true, 143: true}

// shouldRevive decides whether a container that went from running to stopped
// is a crash DockWarden should start again. ctr must come from GetContainer
// (inspect), because a list summary carries no restart policy or exit state.
//
// A container is left stopped when:
//   - its restart policy is "no" (or unset): nothing asked for it to be kept
//     up, and `docker update --restart=no; docker stop` is how a service is
//     retired or paused (rl9 tts-bn was revived twice, 2026-11-09);
//   - it exited cleanly or by a stop signal and was not OOM-killed: that is
//     `docker stop` / `docker kill` (or a deliberate exit), which Docker itself
//     does not restart either.
//
// Everything else (always / unless-stopped / on-failure containers that died
// with an error exit code or were OOM-killed, and that Docker is not already
// restarting) is revived as before.
func shouldRevive(ctr docker.Container) (bool, string) {
	switch ctr.RestartPolicy {
	case "", "no":
		return false, `restart policy is "no"`
	}
	if ctr.OOMKilled {
		return true, "OOM-killed"
	}
	if deliberateStopExitCodes[ctr.ExitCode] {
		return false, fmt.Sprintf("exit code %d (clean exit or stop signal, not OOM-killed)", ctr.ExitCode)
	}
	return true, fmt.Sprintf("exit code %d", ctr.ExitCode)
}

// handleStopped inspects a container that is no longer running and either
// revives it (a crash) or stops tracking it as running (a deliberate stop).
func (w *Watcher) handleStopped(ctx context.Context, ctr docker.Container, state *containerState) {
	info, err := w.client.GetContainer(ctx, ctr.ID)
	if err != nil {
		// Fail closed: do not start something we could not inspect. Keep
		// lastSeenRunning so the next tick tries again.
		log.Errorf("Container %s stopped (state: %s) but could not be inspected, not reviving: %v", ctr.Name, ctr.State, err)
		return
	}
	if info.IsRunning() || info.IsRestarting() || info.IsPaused() {
		// It changed state since the list; the next tick handles it.
		return
	}

	revive, reason := shouldRevive(info)
	if !revive {
		log.Infof("Container %s stopped (state: %s, %s, restart policy %q): treated as a deliberate stop, not reviving", ctr.Name, info.State, reason, info.RestartPolicy)
		state.lastSeenRunning = false
		return
	}

	log.Warnf("Container %s has stopped unexpectedly (state: %s, %s, restart policy %q)", ctr.Name, info.State, reason, info.RestartPolicy)
	w.handleCrashed(ctx, ctr, state)
}

// handleCrashed handles a container that has stopped unexpectedly
func (w *Watcher) handleCrashed(ctx context.Context, ctr docker.Container, state *containerState) {
	now := time.Now()
	window := w.restartWindow()

	log.Warnf("Container %s crashed/exited (attempt %d/%d in %s)", ctr.Name, state.restartCount(now, window)+1, MaxRestartAttempts, window)

	if state.restartCount(now, window) >= MaxRestartAttempts {
		log.Errorf("Container %s: giving up after %d restarts within %s for crashed container. Will retry when new version is available.", ctr.Name, MaxRestartAttempts, window)
		state.gaveUp = true
		state.lastSeenRunning = false

		if w.notifier != nil {
			w.notifier.NotifyContainerGaveUp(ctr.Name, ctr.Image, MaxRestartAttempts)
		}
		return
	}

	attempt := state.recordRestart(now, window)

	if w.notifier != nil {
		w.notifier.NotifyContainerUnhealthy(ctr.Name, ctr.Image, attempt)
	}

	// If start fails, reset lastSeenRunning to avoid rapid retry loops
	log.Infof("Starting crashed container %s (attempt %d/%d in %s)", ctr.Name, attempt, MaxRestartAttempts, window)
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
		state.restarts = nil
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
