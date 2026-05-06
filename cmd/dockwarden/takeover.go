package main

import (
	"context"
	"fmt"
	"time"

	dockerclient "github.com/moby/moby/client"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
)

var takeOverCmd = &cobra.Command{
	Use:    "take-over",
	Short:  "Internal: force-remove old container then start new container",
	Hidden: true,
	Run:    runTakeOver,
}

var (
	// waitContainerID is kept for backward-compatibility with in-flight helpers
	// that were launched before this fix; the value is ignored.
	waitContainerID    string
	startContainerID   string
	cleanupContainerID string
)

func init() {
	rootCmd.AddCommand(takeOverCmd)
	takeOverCmd.Flags().StringVar(&waitContainerID, "wait", "", "Deprecated: no longer used")
	takeOverCmd.Flags().StringVar(&startContainerID, "start", "", "Container ID to start")
	takeOverCmd.Flags().StringVar(&cleanupContainerID, "cleanup", "", "Container ID to remove")
}

func runTakeOver(cmd *cobra.Command, args []string) {
	log.SetFormatter(&log.TextFormatter{
		FullTimestamp:   true,
		TimestampFormat: "2006-01-02 15:04:05",
	})

	if startContainerID == "" {
		log.Fatal("take-over: --start flag is required")
	}

	ctx := context.Background()

	cli, err := dockerclient.New(dockerclient.FromEnv)
	if err != nil {
		log.Fatalf("take-over: failed to create Docker client: %v", err)
	}

	// Brief delay to ensure the old dockwarden process has exited.
	time.Sleep(2 * time.Second)

	// Force-remove the old container BEFORE starting the new one.
	// This is critical: if the old container has a restart policy (always/unless-stopped),
	// simply waiting for it to stop is not enough — Docker will keep restarting it, which
	// causes it to run a new update cycle and produce dockwarden-old-old-old chains.
	// Force-removing immediately prevents any further restarts.
	if cleanupContainerID != "" {
		log.Infof("take-over: force-removing old container %s...", fmtID(cleanupContainerID))
		if _, err := cli.ContainerRemove(ctx, cleanupContainerID, dockerclient.ContainerRemoveOptions{Force: true}); err != nil {
			log.Warnf("take-over: cleanup failed (non-fatal): %v", err)
		} else {
			log.Info("take-over: old container removed")
		}
	}

	// Start the new container
	log.Infof("take-over: starting container %s...", fmtID(startContainerID))
	if _, err := cli.ContainerStart(ctx, startContainerID, dockerclient.ContainerStartOptions{}); err != nil {
		log.Fatalf("take-over: failed to start new container: %v", err)
	}
	log.Info("take-over: new container started successfully")

	log.Info("take-over: self-update complete")
}

func fmtID(id string) string {
	if len(id) > 12 {
		return fmt.Sprintf("%.12s", id)
	}
	return id
}
