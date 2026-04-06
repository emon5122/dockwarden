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
	Short:  "Internal: wait for old container to stop, then start new container",
	Hidden: true,
	Run:    runTakeOver,
}

var (
	waitContainerID    string
	startContainerID   string
	cleanupContainerID string
)

func init() {
	rootCmd.AddCommand(takeOverCmd)
	takeOverCmd.Flags().StringVar(&waitContainerID, "wait", "", "Container ID to wait for stop")
	takeOverCmd.Flags().StringVar(&startContainerID, "start", "", "Container ID to start")
	takeOverCmd.Flags().StringVar(&cleanupContainerID, "cleanup", "", "Container ID to remove after")
}

func runTakeOver(cmd *cobra.Command, args []string) {
	log.SetFormatter(&log.TextFormatter{
		FullTimestamp:   true,
		TimestampFormat: "2006-01-02 15:04:05",
	})

	if waitContainerID == "" || startContainerID == "" {
		log.Fatal("take-over: --wait and --start flags are required")
	}

	ctx := context.Background()

	cli, err := dockerclient.New(dockerclient.FromEnv)
	if err != nil {
		log.Fatalf("take-over: failed to create Docker client: %v", err)
	}

	log.Infof("take-over: waiting for container %s to stop...", fmtID(waitContainerID))

	// Poll until the old container stops or is removed
	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		result, err := cli.ContainerInspect(ctx, waitContainerID, dockerclient.ContainerInspectOptions{})
		if err != nil {
			// Container removed or not found - proceed
			log.Infof("take-over: old container gone: %v", err)
			break
		}
		if result.Container.State == nil || !result.Container.State.Running {
			log.Info("take-over: old container stopped")
			break
		}
		time.Sleep(500 * time.Millisecond)
	}

	// Brief delay to ensure Docker frees the port bindings
	time.Sleep(2 * time.Second)

	// Start the new container
	log.Infof("take-over: starting container %s...", fmtID(startContainerID))
	if _, err := cli.ContainerStart(ctx, startContainerID, dockerclient.ContainerStartOptions{}); err != nil {
		log.Fatalf("take-over: failed to start new container: %v", err)
	}
	log.Info("take-over: new container started successfully")

	// Clean up the old container
	if cleanupContainerID != "" {
		log.Infof("take-over: removing old container %s...", fmtID(cleanupContainerID))
		if _, err := cli.ContainerRemove(ctx, cleanupContainerID, dockerclient.ContainerRemoveOptions{Force: true}); err != nil {
			log.Warnf("take-over: cleanup failed (non-fatal): %v", err)
		} else {
			log.Info("take-over: old container removed")
		}
	}

	log.Info("take-over: self-update complete")
}

func fmtID(id string) string {
	if len(id) > 12 {
		return fmt.Sprintf("%.12s", id)
	}
	return id
}
