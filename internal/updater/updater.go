package updater

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/emon5122/dockwarden/internal/config"
	"github.com/emon5122/dockwarden/internal/docker"
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

	// Statistics
	totalUpdated atomic.Int64
	totalFailed  atomic.Int64
	lastRun      time.Time
	lastRunMu    sync.RWMutex
}

// New creates a new Updater
func New(client docker.Client, cfg *config.Config) *Updater {
	return &Updater{
		client: client,
		config: cfg,
	}
}

// Run executes an update cycle with concurrent container processing
func (u *Updater) Run() error {
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

	// Filter containers
	filtered := u.filterContainers(containers)
	log.Debugf("Found %d containers to check (%d total)", len(filtered), len(containers))

	if len(filtered) == 0 {
		log.Info("No containers to update")
		u.recordRun(startTime)
		return nil
	}

	// Process containers concurrently using goroutines
	results := u.processContainersConcurrently(ctx, filtered)

	// Summarize results
	var updated, failed int
	for _, result := range results {
		if result.Error != nil {
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

	return nil
}

// processContainersConcurrently processes all containers using goroutines
func (u *Updater) processContainersConcurrently(ctx context.Context, containers []docker.Container) []UpdateResult {
	resultsChan := make(chan UpdateResult, len(containers))
	var wg sync.WaitGroup

	// Determine concurrency limit (don't overwhelm Docker daemon)
	maxConcurrency := 5
	if u.config.RollingRestart {
		maxConcurrency = 1 // Sequential for rolling restart
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

			// Acquire semaphore
			semaphore <- struct{}{}
			defer func() { <-semaphore }()

			result := u.processContainer(ctx, container)
			resultsChan <- result
		}(ctr)
	}

	// Wait for all goroutines and close channel
	go func() {
		wg.Wait()
		close(resultsChan)
	}()

	// Collect results
	var results []UpdateResult
	for result := range resultsChan {
		results = append(results, result)
	}

	return results
}

// processContainer processes a single container
func (u *Updater) processContainer(ctx context.Context, ctr docker.Container) UpdateResult {
	result := UpdateResult{
		ContainerID:   ctr.ID,
		ContainerName: ctr.Name,
		OldImageID:    ctr.ImageID,
	}

	// Check for update
	needsUpdate, err := u.checkForUpdate(ctx, ctr)
	if err != nil {
		result.Error = fmt.Errorf("failed to check for updates: %w", err)
		return result
	}

	if !needsUpdate {
		log.Debugf("Container %s is up to date", ctr.Name)
		return result
	}

	// Monitor only mode
	if u.config.MonitorOnly {
		log.Infof("Update available for %s (monitor only mode)", ctr.Name)
		return result
	}

	// Perform update
	if err := u.updateContainer(ctx, ctr); err != nil {
		result.Error = fmt.Errorf("failed to update: %w", err)
		return result
	}

	result.Updated = true

	// Get new image ID
	newCtr, err := u.client.GetContainer(ctx, ctr.ID)
	if err == nil {
		result.NewImageID = newCtr.ImageID
	}

	return result
}

// filterContainers returns containers that should be managed
func (u *Updater) filterContainers(containers []docker.Container) []docker.Container {
	filtered := make([]docker.Container, 0, len(containers))

containerLoop:
	for _, ctr := range containers {
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

		// Only running containers (unless configured otherwise)
		if !ctr.IsRunning() && !u.config.IncludeStopped {
			continue
		}

		filtered = append(filtered, ctr)
	}

	return filtered
}

// checkForUpdate checks if a container has an available update
func (u *Updater) checkForUpdate(ctx context.Context, ctr docker.Container) (bool, error) {
	if u.config.NoPull {
		return false, nil
	}

	// Skip pulling if image has a pinned tag (specific version that won't change)
	if isPinnedTag(ctr.Image) {
		log.Debugf("Skipping pull for %s: image has pinned tag", ctr.Name)
		return false, nil
	}

	// Get current image digest
	currentDigest, err := u.client.GetImageDigest(ctx, ctr.Image)
	if err != nil {
		return false, fmt.Errorf("failed to get current digest: %w", err)
	}

	// Pull latest image
	if err := u.client.PullImage(ctx, ctr.Image); err != nil {
		return false, fmt.Errorf("failed to pull image: %w", err)
	}

	// Get new image digest
	newDigest, err := u.client.GetImageDigest(ctx, ctr.Image)
	if err != nil {
		return false, fmt.Errorf("failed to get new digest: %w", err)
	}

	// Compare digests
	if currentDigest != newDigest {
		log.Debugf("Container %s has update: %s -> %s", ctr.Name, truncateID(currentDigest), truncateID(newDigest))
		return true, nil
	}

	return false, nil
}

// updateContainer updates a container to the new image
func (u *Updater) updateContainer(ctx context.Context, ctr docker.Container) error {
	oldImageID := ctr.ImageID
	timeout := ctr.GetStopTimeout(u.config.StopTimeout)

	log.Infof("Updating container %s", ctr.Name)

	// Recreate container with new image
	_, err := u.client.RecreateContainer(ctx, ctr.ID, timeout)
	if err != nil {
		return fmt.Errorf("failed to recreate container: %w", err)
	}

	// Cleanup old image if enabled
	if u.config.Cleanup && oldImageID != "" {
		log.Debugf("Cleaning up old image %s", truncateID(oldImageID))
		if err := u.client.RemoveImage(ctx, oldImageID); err != nil {
			// Not a fatal error, just log it
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

// truncateID truncates an ID to 12 characters
func truncateID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

// isPinnedTag checks if an image reference uses a pinned tag that won't change.
// Pinned tags include:
// - Digest references (image@sha256:...)
// - Semantic versions (v1.2.3, 1.2.3, 14.4-bullseye, etc.)
// - Release tags (RELEASE.2025-04-22T22-12-26Z, etc.)
// Floating tags that should be pulled:
// - latest, edge, main, master, dev, develop, nightly, stable, beta, alpha
func isPinnedTag(imageName string) bool {
	// Check if it's a digest reference - always pinned
	if strings.Contains(imageName, "@sha256:") {
		return true
	}

	// Extract the tag from the image name
	tag := extractTag(imageName)
	if tag == "" {
		return false // No tag means :latest implicitly
	}

	// Floating tags that can change - should be pulled
	floatingTags := []string{
		"latest",
		"edge",
		"main",
		"master",
		"dev",
		"develop",
		"development",
		"nightly",
		"stable",
		"beta",
		"alpha",
		"canary",
		"rc",
		"next",
		"preview",
	}

	tagLower := strings.ToLower(tag)
	for _, floating := range floatingTags {
		if tagLower == floating {
			return false
		}
	}

	// If the tag contains version-like patterns, it's likely pinned
	// Patterns: v1.2.3, 1.2.3, 14.4, RELEASE.xxx, sha-xxx, etc.
	return true
}

// extractTag extracts the tag from an image name
// Examples:
//   - nginx:1.21 -> 1.21
//   - ghcr.io/org/image:v1.0.0 -> v1.0.0
//   - nginx -> "" (empty, implies latest)
//   - nginx:latest -> latest
func extractTag(imageName string) string {
	// Handle digest references
	if idx := strings.Index(imageName, "@"); idx != -1 {
		imageName = imageName[:idx]
	}

	// Find the last colon that's not part of a port
	lastColon := strings.LastIndex(imageName, ":")
	if lastColon == -1 {
		return "" // No tag
	}

	// Check if this colon is part of a registry port (e.g., localhost:5000/image)
	afterColon := imageName[lastColon+1:]
	if strings.Contains(afterColon, "/") {
		return "" // The colon was part of registry:port, no tag
	}

	return afterColon
}
