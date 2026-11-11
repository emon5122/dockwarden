package updater

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/emon5122/dockwarden/internal/config"
	"github.com/emon5122/dockwarden/internal/docker"
	"github.com/emon5122/dockwarden/internal/registry"
	"github.com/emon5122/dockwarden/internal/scheduler"
	log "github.com/sirupsen/logrus"
)

// UpdateResult represents the result of updating a single container
type UpdateResult struct {
	ContainerID   string
	ContainerName string
	OldImageID    string
	NewImageID    string
	Updated       bool
	Error         error
}

// Updater handles container image updates using Go's native concurrency
type Updater struct {
	client docker.Client
	config *config.Config

	// now is the clock used for per-container schedule windows (tests override it)
	now func() time.Time

	// Concurrency guard
	runMu sync.Mutex

	// gates track registry/DNS reachability per host; shared with the health watcher
	gates *registry.Gates
	// pullSem caps simultaneous image pulls (Config.PullConcurrency, default 1)
	pullSem chan struct{}
	// lastSelfCheck rate-limits the self-update check (Config.SelfUpdateInterval)
	lastSelfCheck time.Time
	// compose is the helper setup for compose rolls in the current pass (nil = off)
	compose *composeEnv
	// composeRolls dedupes replicas of one compose service within a pass
	composeRolls   map[string]*composeOnce
	composeRollsMu sync.Mutex

	// Statistics
	totalUpdated atomic.Int64
	totalFailed  atomic.Int64
	lastRun      time.Time
	lastRunMu    sync.RWMutex
}

// New creates a new Updater
func New(client docker.Client, cfg *config.Config) *Updater {
	n := cfg.PullConcurrency
	if n < 1 {
		n = 1
	}
	return &Updater{
		client:  client,
		config:  cfg,
		now:     time.Now,
		gates:   registry.NewGates(),
		pullSem: make(chan struct{}, n),
	}
}

// Gates returns the registry reachability gates, to share with the health watcher.
func (u *Updater) Gates() *registry.Gates { return u.gates }

// errRegistryDown marks a pull skipped because the registry is unreachable.
var errRegistryDown = errors.New("registry unreachable, pull skipped (backing off)")

// Run executes an update cycle with concurrent container processing
func (u *Updater) Run() error {
	// Prevent concurrent update cycles
	if !u.runMu.TryLock() {
		return fmt.Errorf("update cycle already in progress")
	}
	defer u.runMu.Unlock()

	ctx := context.Background()
	startTime := time.Now()

	log.Info("Starting update check...")

	// List containers
	containers, err := u.client.ListContainers(ctx, docker.ListOptions{
		All:           u.config.IncludeStopped,
		IncludeHealth: true,
	})
	if err != nil {
		return fmt.Errorf("failed to list containers: %w", err)
	}

	// Separate self container from others using actual container ID
	var selfContainer *docker.Container
	var otherContainers []docker.Container
	selfID := u.client.GetSelfContainerID()
	selfContainer, otherContainers = splitSelf(containers, selfID)
	u.prepareCompose(ctx, selfContainer, selfID)

	// Filter non-self containers
	filtered := u.filterContainers(otherContainers)
	log.Debugf("Found %d containers to check (%d total)", len(filtered), len(containers))

	if len(filtered) == 0 && selfContainer == nil {
		log.Info("No containers to update")
		u.recordRun(startTime)
		return nil
	}

	// Process non-self containers concurrently. All containers of this pass
	// share one pull cache, so each image ref is pulled exactly once.
	results := u.processContainersConcurrently(ctx, filtered, newPullCache(u))

	// Summarize results
	var updated, failed int
	for _, result := range results {
		if errors.Is(result.Error, errRegistryDown) {
			log.Debugf("%s: %v", result.ContainerName, result.Error)
		} else if result.Error != nil {
			log.Errorf("Failed to process %s: %v", result.ContainerName, result.Error)
			failed++
		} else if result.Updated {
			log.Infof("Updated container %s", result.ContainerName)
			updated++
		}
	}

	// Update stats
	u.totalUpdated.Add(int64(updated))
	u.totalFailed.Add(int64(failed))
	u.recordRun(startTime)

	duration := time.Since(startTime)
	log.Infof("Update check complete: %d updated, %d failed, took %s", updated, failed, duration.Round(time.Millisecond))

	// Clean up any stale containers left by previous failed self-updates
	// (dockwarden-old, dockwarden-old-old, ...) before doing anything else.
	if selfContainer != nil {
		u.cleanupStaleContainers(ctx, selfContainer.Name)
	}

	// Self-update LAST - after all other containers are done
	// (dockwarden.schedule applies to it too; invalid spec fails closed)
	if selfContainer != nil && selfContainer.UpdateEnabled() && u.inScheduleWindow(*selfContainer) {
		u.handleSelfUpdate(ctx, *selfContainer)
	}

	return nil
}

// handleSelfUpdate handles updating dockwarden's own container.
// This must be the LAST operation in an update cycle.
// Strategy: pull new image, rename self, create new container, start it, then exit.
// Docker daemon handles the lifecycle - the new container keeps running after we exit.
func (u *Updater) handleSelfUpdate(ctx context.Context, self docker.Container) {
	if u.config.NoPull || u.config.MonitorOnly {
		return
	}

	// Defensive guard: if this container's name already ends with "-old" it is a
	// stale instance that was restarted by Docker's restart policy after a rename.
	// Proceeding would create another "-old-old" layer. Bail out immediately;
	// the real dockwarden (or the startup cleanup) will handle removal.
	if strings.HasSuffix(self.Name, "-old") {
		log.Warnf("Self-update skipped: running as renamed container %q — likely a restart-policy loop. Exiting to let the active instance take over.", self.Name)
		os.Exit(0)
	}

	// Get the original image name from the container config, not the summary.
	// When a tag is re-pushed, the summary's Image field becomes a raw sha256 digest.
	// self was resolved by splitSelf; its full ID beats the (possibly wrong) hint.
	selfID := self.ID
	inspected, err := u.client.GetContainer(ctx, selfID)
	if err != nil {
		log.Errorf("Self-update: failed to inspect self: %v", err)
		return
	}

	imageName := inspected.Image
	if imageName == "" || strings.HasPrefix(imageName, "sha256:") {
		imageName = self.Image
	}

	// Only skip digest-pinned images
	if isDigestPinned(imageName) {
		return
	}

	// Rate-limit: DockWarden's own image rarely changes, so one check per
	// SelfUpdateInterval is plenty however short the main interval is.
	if iv := u.config.SelfUpdateInterval; iv > 0 && !u.lastSelfCheck.IsZero() && u.now().Sub(u.lastSelfCheck) < iv {
		log.Debugf("Self-update check skipped: last check %s ago (interval %s)", u.now().Sub(u.lastSelfCheck).Round(time.Second), iv)
		return
	}
	u.lastSelfCheck = u.now()

	// If still a raw digest, we can't pull by tag. Skip.
	if strings.HasPrefix(imageName, "sha256:") {
		log.Debugf("Self-update: cannot determine image tag from %s, skipping", truncateID(imageName))
		return
	}

	// The running container's ImageID is the definitive source of what's currently running.
	// We compare it against the ImageID of the newly pulled image tag.
	currentImageID := inspected.ImageID

	newImageID, err := u.fetchImage(ctx, imageName)
	if errors.Is(err, errRegistryDown) {
		log.Debugf("Self-update check skipped: %v", err)
		return
	}
	if err != nil {
		log.Errorf("Self-update: failed to refresh image: %v", err)
		return
	}

	if currentImageID == newImageID {
		log.Debugf("DockWarden is up to date")
		return
	}

	if u.config.NoRestart {
		log.Infof("DockWarden update pulled (%s -> %s); self-restart skipped (no-restart mode)", truncateID(currentImageID), truncateID(newImageID))
		return
	}

	log.Infof("DockWarden update available (%s -> %s)! Performing self-update...", truncateID(currentImageID), truncateID(newImageID))

	// Self-update strategy: we CANNOT stop ourselves first (that kills our process).
	// Instead: rename self -> create new container with original name -> start new -> exit.
	timeout := self.GetStopTimeout(u.config.StopTimeout)
	if err := u.selfRecreate(ctx, selfID, imageName, timeout); err != nil {
		log.Errorf("Self-update failed: %v", err)
		return
	}

	log.Info("Self-update complete. New DockWarden container started. Exiting old instance...")

	// Clean exit - the new container is already running
	os.Exit(0)
}

// selfRecreate orchestrates the self-update using a helper container.
// Flow: rename self -> create clone (not started) -> launch helper -> exit.
// The helper container waits for us to stop, starts the clone, cleans up.
func (u *Updater) selfRecreate(ctx context.Context, selfID, imageName string, timeout time.Duration) error {
	// Inspect self to get the original name
	self, err := u.client.GetContainer(ctx, selfID)
	if err != nil {
		return fmt.Errorf("failed to inspect self: %w", err)
	}

	originalName := self.Name

	// Step 1: Remove any stale containers left by previous failed self-updates
	// (e.g. dockwarden-old, dockwarden-old-old, ...) so the rename below can't
	// collide with them and so the system is left in a clean state.
	u.cleanupStaleContainers(ctx, originalName)

	// Step 2 (was Step 1): Rename self to free up the original name
	tempName := originalName + "-old"
	if err := u.client.RenameContainer(ctx, selfID, tempName); err != nil {
		return fmt.Errorf("failed to rename self: %w", err)
	}
	log.Infof("Renamed self from %s to %s", originalName, tempName)

	// Step 3: Clone self as a new container with the original name (not started).
	// The clone uses inspect config, so Config.Image is the tag name which now
	// resolves to the newly pulled image.
	cloneID, err := u.client.CloneContainer(ctx, selfID, originalName)
	if err != nil {
		// Undo rename on failure
		_ = u.client.RenameContainer(ctx, selfID, originalName)
		return fmt.Errorf("failed to create clone: %w", err)
	}
	log.Infof("Created clone container %s (not started yet)", truncateID(cloneID))

	// Step 4: Find Docker socket bind mount from our own container
	dockerSocketBind := "/var/run/docker.sock:/var/run/docker.sock"
	if binds, err := u.client.GetContainerBinds(ctx, selfID); err == nil {
		for _, b := range binds {
			if strings.Contains(b, "docker.sock") {
				dockerSocketBind = b
				break
			}
		}
	}

	// Step 5: Launch helper container to complete the update after we exit.
	// The helper runs our same binary with the "take-over" command.
	helperCmd := []string{
		"take-over",
		"--start", cloneID,
		"--cleanup", selfID,
	}

	helperID, err := u.client.CreateHelperContainer(ctx, imageName, helperCmd, []string{dockerSocketBind})
	if err != nil {
		// Undo: remove clone and rename back
		_ = u.client.RemoveContainer(ctx, cloneID)
		_ = u.client.RenameContainer(ctx, selfID, originalName)
		return fmt.Errorf("failed to launch takeover helper: %w", err)
	}

	log.Infof("Launched takeover helper %s - will start clone after we exit", truncateID(helperID))
	return nil
}

// processContainersConcurrently processes all containers using goroutines
func (u *Updater) processContainersConcurrently(ctx context.Context, containers []docker.Container, pulls *pullCache) []UpdateResult {
	resultsChan := make(chan UpdateResult, len(containers))
	var wg sync.WaitGroup

	maxConcurrency := 10
	if u.config.RollingRestart {
		maxConcurrency = 1
	}
	semaphore := make(chan struct{}, maxConcurrency)

	for _, ctr := range containers {
		if !ctr.UpdateEnabled() {
			log.Debugf("Skipping %s: updates disabled", ctr.Name)
			continue
		}

		wg.Add(1)
		go func(container docker.Container) {
			defer wg.Done()

			semaphore <- struct{}{}
			defer func() { <-semaphore }()

			result := u.processContainer(ctx, container, pulls)
			resultsChan <- result
		}(ctr)
	}

	go func() {
		wg.Wait()
		close(resultsChan)
	}()

	var results []UpdateResult
	for result := range resultsChan {
		results = append(results, result)
	}

	return results
}

// processContainer processes a single container
func (u *Updater) processContainer(ctx context.Context, ctr docker.Container, pulls *pullCache) UpdateResult {
	result := UpdateResult{
		ContainerID:   ctr.ID,
		ContainerName: ctr.Name,
		OldImageID:    ctr.ImageID,
	}

	needsUpdate, err := u.checkForUpdate(ctx, ctr, pulls)
	if err != nil {
		result.Error = fmt.Errorf("failed to check for updates: %w", err)
		return result
	}

	if !needsUpdate {
		log.Debugf("Container %s is up to date", ctr.Name)
		return result
	}

	if u.config.MonitorOnly {
		log.Infof("Update available for %s (monitor only mode)", ctr.Name)
		return result
	}

	if u.config.NoRestart {
		log.Infof("Update pulled for %s; container left running on old image (no-restart mode)", ctr.Name)
		return result
	}

	if err := u.updateContainer(ctx, ctr); err != nil {
		result.Error = fmt.Errorf("failed to update: %w", err)
		return result
	}

	result.Updated = true

	newCtr, err := u.client.GetContainer(ctx, ctr.ID)
	if err == nil {
		result.NewImageID = newCtr.ImageID
	}

	return result
}

// filterContainers returns containers that should be managed (excludes self)
func (u *Updater) filterContainers(containers []docker.Container) []docker.Container {
	filtered := make([]docker.Container, 0, len(containers))

containerLoop:
	for _, ctr := range containers {
		// Never manage DockWarden's own helper containers
		if ctr.GetLabel(docker.HelperLabel) != "" || isTakeoverHelper(ctr) {
			continue
		}

		// Skip disabled containers
		for _, disabled := range u.config.DisableContainers {
			if ctr.Name == disabled {
				continue containerLoop
			}
		}

		// Check label filter
		if u.config.LabelEnable {
			if !ctr.IsEnabled(u.config.LabelName, false) {
				continue
			}
		}

		// Check scope
		if u.config.Scope != "" {
			if ctr.GetScope() != u.config.Scope {
				continue
			}
		}

		// Per-container update window (dockwarden.schedule label)
		if !u.inScheduleWindow(ctr) {
			continue
		}

		// Only running containers unless configured otherwise: restarting
		// containers need include-restarting, stopped ones include-stopped.
		if !ctr.IsRunning() {
			if ctr.IsRestarting() {
				if !u.config.IncludeRestarting {
					continue
				}
			} else if !u.config.IncludeStopped {
				continue
			}
		}

		filtered = append(filtered, ctr)
	}

	return filtered
}

// defaultScheduleLabel is the label holding a per-container update window.
const defaultScheduleLabel = "dockwarden.schedule"

// inScheduleWindow reports whether ctr may be checked/updated right now.
// Containers without the schedule label always may. An invalid spec fails
// closed: one warning, and the container is skipped for this pass.
func (u *Updater) inScheduleWindow(ctr docker.Container) bool {
	label := u.config.LabelSchedule
	if label == "" {
		label = defaultScheduleLabel
	}
	if !ctr.HasLabel(label) {
		return true
	}
	spec := ctr.GetLabel(label)
	now := time.Now
	if u.now != nil {
		now = u.now
	}
	ok, err := scheduler.InWindow(spec, now())
	if err != nil {
		log.Warnf("Skipping %s: label %s: %v (fail closed)", ctr.Name, label, err)
		return false
	}
	if !ok {
		log.Infof("Skipping %s: outside schedule window (%s)", ctr.Name, spec)
		return false
	}
	return true
}

// checkForUpdate checks if a container has an available update.
//
// The pullable ref comes from inspect (.Config.Image), never from the container
// list: once a tag moves to a newer image, Docker rewrites the list's Image
// field to the bare id of the image the container still runs, and pulling
// "sha256:..." fails with "pull access denied for sha256" on every pass.
//
// The container's running image id is then compared with the id the tag points
// to after the pull. Comparing the tag before and after our own pull would miss
// a tag that already moved (an earlier pass, a local build, or another
// container on the same ref pulling first in this pass), leaving the container
// on the old image forever.
func (u *Updater) checkForUpdate(ctx context.Context, ctr docker.Container, pulls *pullCache) (bool, error) {
	if u.config.NoPull {
		return false, nil
	}

	imageRef, runningImageID, err := u.resolveImage(ctx, ctr)
	if err != nil {
		return false, err
	}

	// Only skip digest-pinned images (sha256 references that can never change)
	// ALL tags including version tags should be pulled to check for updates
	if isDigestPinned(imageRef) {
		log.Debugf("Skipping pull for %s: image pinned by digest", ctr.Name)
		return false, nil
	}

	// Created from a bare image id: there is no tag to follow.
	if isImageID(imageRef) {
		log.Debugf("Skipping %s: created from image id %s, no tag to pull", ctr.Name, truncateID(imageRef))
		return false, nil
	}

	newImageID, err := pulls.pull(ctx, imageRef)
	if err != nil {
		return false, err
	}

	if runningImageID != newImageID {
		log.Debugf("Container %s has update (%s): %s -> %s", ctr.Name, imageRef, truncateID(runningImageID), truncateID(newImageID))
		return true, nil
	}

	return false, nil
}

// resolveImage returns the container's pullable image ref (.Config.Image) and
// the id of the image it is running (.Image), both from inspect.
func (u *Updater) resolveImage(ctx context.Context, ctr docker.Container) (string, string, error) {
	inspected, err := u.client.GetContainer(ctx, ctr.ID)
	if err != nil {
		return "", "", fmt.Errorf("failed to inspect container: %w", err)
	}

	imageRef := inspected.Image
	if imageRef == "" && !isImageID(ctr.Image) {
		imageRef = ctr.Image
	}

	runningImageID := inspected.ImageID
	if runningImageID == "" {
		runningImageID = ctr.ImageID
	}
	if runningImageID == "" {
		return "", "", fmt.Errorf("cannot determine running image id")
	}

	return imageRef, runningImageID, nil
}

// pullCache pulls each image ref at most once per update pass and remembers the
// image id the ref resolved to, so every container sharing a repo:tag is
// compared against the same freshly pulled image.
type pullCache struct {
	u *Updater

	mu      sync.Mutex
	entries map[string]*pullEntry
}

type pullEntry struct {
	once    sync.Once
	imageID string
	err     error
}

func newPullCache(u *Updater) *pullCache {
	return &pullCache{u: u, entries: make(map[string]*pullEntry)}
}

// pull pulls imageRef (once per pass) and returns the image id the ref points
// to afterwards. Concurrent callers for the same ref wait for the one pull.
func (p *pullCache) pull(ctx context.Context, imageRef string) (string, error) {
	p.mu.Lock()
	entry, ok := p.entries[imageRef]
	if !ok {
		entry = &pullEntry{}
		p.entries[imageRef] = entry
	}
	p.mu.Unlock()

	entry.once.Do(func() {
		entry.imageID, entry.err = p.u.fetchImage(ctx, imageRef)
	})

	return entry.imageID, entry.err
}

// Timeouts for registry calls: one hung call must not hold the only pull slot
// (and the update cycle lock) for ever.
const (
	digestTimeout = 30 * time.Second
	pullTimeout   = 15 * time.Minute
)

// fetchImage brings the local tag for imageRef up to date and returns the image
// id it points to. It
//   - does nothing while that registry is unreachable (errRegistryDown); the
//     gate's background prober, not this loop, decides when it is back,
//   - asks the registry for the manifest digest first and skips the pull when
//     the local image already has that digest (an unchanged tag costs one tiny
//     request instead of a pull; R610 pulled dockwarden:latest every minute),
//   - runs at most Config.PullConcurrency pulls at a time.
func (u *Updater) fetchImage(ctx context.Context, imageRef string) (string, error) {
	if u.gates.Down(imageRef) {
		return "", errRegistryDown
	}

	u.pullSem <- struct{}{}
	defer func() { <-u.pullSem }()

	// Another pull may have tripped the gate while we waited for a slot.
	if u.gates.Down(imageRef) {
		return "", errRegistryDown
	}

	dctx, cancel := context.WithTimeout(ctx, digestTimeout)
	remote, err := u.client.RemoteDigest(dctx, imageRef)
	cancel()
	if err == nil {
		u.gates.OK(imageRef)
		if local, lerr := u.client.LocalDigests(ctx, imageRef); lerr == nil {
			for _, d := range local {
				if d == remote {
					if id, ierr := u.client.GetImageID(ctx, imageRef); ierr == nil {
						log.Debugf("Image %s unchanged (digest %s), pull skipped", imageRef, truncateID(strings.TrimPrefix(remote, "sha256:")))
						return id, nil
					}
				}
			}
		}
	} else if u.gates.Trip(imageRef, err) {
		return "", u.outage(imageRef, err)
	} else {
		// The registry answered but cannot tell us the digest (denied, private
		// registry, old daemon): it is reachable; fall through to a normal pull.
		u.gates.OK(imageRef)
		log.Debugf("Digest check for %s failed (%v); pulling", imageRef, err)
	}

	pctx, pcancel := context.WithTimeout(ctx, pullTimeout)
	err = u.client.PullImage(pctx, imageRef)
	pcancel()
	if err != nil {
		if u.gates.Trip(imageRef, err) {
			return "", u.outage(imageRef, err)
		}
		u.gates.OK(imageRef) // refused, not unreachable
		return "", fmt.Errorf("failed to pull image: %w", err)
	}
	u.gates.OK(imageRef)
	id, err := u.client.GetImageID(ctx, imageRef)
	if err != nil {
		return "", fmt.Errorf("failed to get new image id: %w", err)
	}
	return id, nil
}

func (u *Updater) outage(imageRef string, err error) error {
	log.Warnf("Registry %s unreachable (%v): pausing its pulls and unhealthy restarts until it answers again", registry.HostOf(imageRef), err)
	return fmt.Errorf("%w: %v", errRegistryDown, err)
}

// updateContainer updates a container to the new image
func (u *Updater) updateContainer(ctx context.Context, ctr docker.Container) error {
	oldImageID := ctr.ImageID
	timeout := ctr.GetStopTimeout(u.config.StopTimeout)

	log.Infof("Updating container %s", ctr.Name)

	startStopped := u.config.ReviveStopped
	rolled, err := u.composeRoll(ctx, ctr, timeout)
	var fb *composeFallback
	switch {
	case err == nil && rolled:
		// done through the compose project
	case err == nil:
		// not a compose container (or compose rolls are off): legacy recreate
		if _, err := u.client.RecreateContainer(ctx, ctr.ID, timeout, startStopped); err != nil {
			return fmt.Errorf("failed to recreate container: %w", err)
		}
	case errors.As(err, &fb):
		log.Warnf("Container %s: compose roll not possible (%v); recreating from the old container config", ctr.Name, fb.err)
		if _, err := u.client.RecreateContainer(ctx, ctr.ID, timeout, startStopped || fb.stoppedByUs); err != nil {
			return fmt.Errorf("failed to recreate container: %w", err)
		}
	default:
		return err
	}

	if u.config.Cleanup && oldImageID != "" {
		log.Debugf("Cleaning up old image %s", truncateID(oldImageID))
		if err := u.client.RemoveImage(ctx, oldImageID); err != nil {
			log.Debugf("Failed to remove old image %s: %v", truncateID(oldImageID), err)
		}
	}

	return nil
}

// recordRun records the time of the last run
func (u *Updater) recordRun(t time.Time) {
	u.lastRunMu.Lock()
	u.lastRun = t
	u.lastRunMu.Unlock()
}

// GetStats returns update statistics
func (u *Updater) GetStats() map[string]interface{} {
	u.lastRunMu.RLock()
	lastRun := u.lastRun
	u.lastRunMu.RUnlock()

	return map[string]interface{}{
		"total_updated": u.totalUpdated.Load(),
		"total_failed":  u.totalFailed.Load(),
		"last_run":      lastRun,
	}
}

func truncateID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

// isDigestPinned checks if an image is pinned by sha256 digest.
// Only digest-pinned images are skipped. ALL tagged images (including
// version tags like v1.2.3, latest-beta, etc.) are always pulled to
// check for updates, since any tag could have been re-pushed.
func isDigestPinned(imageName string) bool {
	return strings.Contains(imageName, "@sha256:")
}

// isImageID reports whether ref is a bare image id ("sha256:<hex>" or a full
// 64-char hex id, which Docker never accepts as a repo name) rather than a
// pullable repo[:tag] reference.
func isImageID(ref string) bool {
	if strings.HasPrefix(ref, "sha256:") {
		return true
	}
	if len(ref) != 64 {
		return false
	}
	for _, r := range ref {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

// splitSelf separates dockwarden's own container from the rest.
//
// The ID from GetSelfContainerID is only a hint (cgroup v2 hides it, and a
// custom hostname or host networking can make the hostname fallback return a
// string that is not our container ID). A non-empty but wrong ID used to match
// nothing, so dockwarden fell into the ordinary-container path, stopped itself
// and never started its replacement. So: match by ID first, and when that
// finds nothing fall back to name/image/label detection. Without any match
// self is nil and the caller never touches it.
func splitSelf(containers []docker.Container, selfID string) (*docker.Container, []docker.Container) {
	selfIdx := -1
	if selfID != "" {
		for i, ctr := range containers {
			if strings.HasPrefix(ctr.ID, selfID) {
				selfIdx = i
				break
			}
		}
	}
	if selfIdx < 0 {
		for i, ctr := range containers {
			// A renamed leftover (dockwarden-old) or a self-update take-over helper
			// (dockwarden-takeover-<ts>, same image) is never us for update purposes.
			if isSelfContainer(ctr) && !strings.HasSuffix(ctr.Name, "-old") && !isTakeoverHelper(ctr) && ctr.GetLabel(docker.HelperLabel) == "" {
				selfIdx = i
				break
			}
		}
	}
	others := make([]docker.Container, 0, len(containers))
	var self *docker.Container
	for i, ctr := range containers {
		if i == selfIdx {
			c := ctr
			self = &c
			continue
		}
		others = append(others, ctr)
	}
	return self, others
}

// isTakeoverHelper reports whether ctr is a self-update helper (CreateHelperContainer names
// them dockwarden-takeover-<unix ts>). On 2026-11-08 the name fallback picked a just-finished
// helper as "self" and logged "Self-update: failed to inspect self" with the helper's ID.
func isTakeoverHelper(ctr docker.Container) bool {
	return strings.Contains(strings.TrimPrefix(ctr.Name, "/"), "dockwarden-takeover-")
}

// isSelfContainer checks if a container is the dockwarden container itself.
func isSelfContainer(ctr docker.Container) bool {
	nameLower := strings.ToLower(ctr.Name)
	if strings.Contains(nameLower, "dockwarden") {
		return true
	}

	imageLower := strings.ToLower(ctr.Image)
	if strings.Contains(imageLower, "dockwarden") {
		return true
	}

	if ctr.GetLabel("dockwarden.self") == "true" {
		return true
	}

	return false
}

// cleanupStaleContainers removes stopped or running containers whose names match
// the "{originalName}-old*" pattern — the debris left behind when Docker's restart
// policy restarts an already-renamed container during a self-update cycle.
func (u *Updater) cleanupStaleContainers(ctx context.Context, originalName string) {
	prefix := originalName + "-old"

	containers, err := u.client.ListContainers(ctx, docker.ListOptions{All: true})
	if err != nil {
		log.Debugf("cleanupStaleContainers: failed to list containers: %v", err)
		return
	}

	for _, ctr := range containers {
		if !strings.HasPrefix(ctr.Name, prefix) {
			continue
		}
		log.Infof("Removing stale container %s left by a previous self-update", ctr.Name)
		if err := u.client.RemoveContainer(ctx, ctr.ID); err != nil {
			log.Warnf("Failed to remove stale container %s: %v", ctr.Name, err)
		}
	}
}
