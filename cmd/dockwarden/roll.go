package main

import (
	"context"
	"os"
	"time"

	"github.com/emon5122/dockwarden/internal/docker"
	"github.com/emon5122/dockwarden/internal/updater"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
)

// rollCmd runs inside a detached roll helper (internal/updater/detach.go): it
// drains and rolls one container whose stop timeout is too long for the
// update cycle to wait on. Exit 0 = the container runs the new image.
var rollCmd = &cobra.Command{
	Use:    "roll",
	Short:  "Internal: drain and roll one container (run by DockWarden in a helper container)",
	Hidden: true,
	Run:    runRoll,
}

var rollArgs updater.DetachedRoll

func init() {
	rootCmd.AddCommand(rollCmd)
	f := rollCmd.Flags()
	f.StringVar(&rollArgs.ContainerID, "container", "", "Container ID to roll")
	f.StringVar(&rollArgs.Mode, "mode", updater.RollModeCompose, "compose or recreate")
	f.DurationVar(&rollArgs.Timeout, "timeout", 10*time.Second, "Stop timeout (the drain)")
	f.StringVar(&rollArgs.Signal, "signal", "", "Stop signal (empty = the container's own)")
	f.BoolVar(&rollArgs.StartStopped, "start-stopped", false, "Start the new container even if the old one was stopped (recreate mode)")
	f.StringVar(&rollArgs.CleanupImage, "cleanup-image", "", "Old image ID to remove after the roll")
}

func runRoll(cmd *cobra.Command, args []string) {
	log.SetFormatter(&log.TextFormatter{FullTimestamp: true, TimestampFormat: "2006-01-02 15:04:05"})
	log.SetOutput(os.Stderr)
	if rollArgs.ContainerID == "" {
		log.Fatal("roll: --container is required")
	}
	// --remove-volumes is the root's persistent flag (config.RegisterFlags)
	rollArgs.RemoveVolumes, _ = cmd.Flags().GetBool("remove-volumes")
	client, err := docker.NewClient(docker.ClientOptions{RemoveVolumes: rollArgs.RemoveVolumes})
	if err != nil {
		log.Fatalf("roll: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), updater.RollHelperDeadline(rollArgs.Timeout))
	defer cancel()
	if err := updater.RunDetachedRoll(ctx, client, rollArgs); err != nil {
		log.Errorf("roll: %v", err)
		cancel()
		os.Exit(1)
	}
}
