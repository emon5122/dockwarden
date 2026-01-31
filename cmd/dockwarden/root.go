package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/emon5122/dockwarden/internal/config"
	"github.com/emon5122/dockwarden/internal/docker"
	"github.com/emon5122/dockwarden/internal/health"
	"github.com/emon5122/dockwarden/internal/meta"
	"github.com/emon5122/dockwarden/internal/scheduler"
	"github.com/emon5122/dockwarden/internal/updater"
	"github.com/emon5122/dockwarden/pkg/api"

	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
)

var (
	cfg    *config.Config
	client docker.Client
)

var rootCmd = &cobra.Command{
	Use:   "dockwarden",
	Short: "Modern Docker Container Manager - Auto-Update & Health Monitor",
	Long: `
DockWarden automatically updates running Docker containers whenever a new image 
is released and monitors container health to restart unhealthy containers.

A spiritual successor to Watchtower and Docker Watchdog, actively maintained 
with modern security practices including native Docker secrets support.

Repository: https://github.com/emon5122/dockwarden
Docker Hub: https://hub.docker.com/r/emon5122/dockwarden
`,
	Run:    run,
	PreRun: preRun,
}

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print the version information",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Printf("DockWarden %s\n", meta.Version)
		fmt.Printf("  Commit: %s\n", meta.Commit)
		fmt.Printf("  Built:  %s\n", meta.BuildDate)
	},
}

func init() {
	rootCmd.AddCommand(versionCmd)
	config.RegisterFlags(rootCmd)
}

// Execute is the main entry point for the CLI
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		log.Fatal(err)
	}
}

func preRun(cmd *cobra.Command, args []string) {
	var err error

	// Load configuration
	cfg, err = config.Load(cmd)
	if err != nil {
		log.Fatalf("Failed to load configuration: %v", err)
	}

	// Setup logging
	setupLogging(cfg)

	// Health check mode - just exit with success if Docker is reachable
	if cfg.HealthCheck {
		if err := performHealthCheck(); err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}

	// Initialize Docker client
	client, err = docker.NewClient(docker.ClientOptions{
		IncludeStopped:    cfg.IncludeStopped,
		IncludeRestarting: cfg.IncludeRestarting,
		RemoveVolumes:     cfg.RemoveVolumes,
	})
	if err != nil {
		log.Fatalf("Failed to create Docker client: %v", err)
	}

	log.Infof("DockWarden %s starting...", meta.Version)
}

func run(cmd *cobra.Command, args []string) {
	// Setup signal handling
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// Create updater module
	upd := updater.New(client, cfg)

	// Create health watcher module
	var watcher *health.Watcher
	if cfg.HealthWatch {
		watcher = health.NewWatcher(client, cfg)
		go watcher.Start()
	}

	// Start API server if enabled
	if cfg.APIEnabled {
		go startAPIServer(upd, watcher)
	}

	// Create scheduler
	sched := scheduler.New(cfg)

	// Run once mode
	if cfg.RunOnce {
		log.Info("Running once and exiting...")
		if err := upd.Run(); err != nil {
			log.Errorf("Update failed: %v", err)
		}
		return
	}

	// Start scheduler
	sched.Start(func() {
		if err := upd.Run(); err != nil {
			log.Errorf("Update cycle failed: %v", err)
		}
	})

	// Wait for shutdown signal
	sig := <-sigChan
	log.Infof("Received signal %v, shutting down...", sig)

	// Graceful shutdown
	sched.Stop()
	if watcher != nil {
		watcher.Stop()
	}

	log.Info("DockWarden stopped")
}

func setupLogging(cfg *config.Config) {
	// Set log level
	level, err := log.ParseLevel(cfg.LogLevel)
	if err != nil {
		level = log.InfoLevel
	}
	log.SetLevel(level)

	// Set log format
	switch cfg.LogFormat {
	case "json":
		log.SetFormatter(&log.JSONFormatter{
			TimestampFormat: time.RFC3339,
		})
	default:
		log.SetFormatter(&log.TextFormatter{
			FullTimestamp:   true,
			TimestampFormat: "2006-01-02 15:04:05",
		})
	}
}

func startAPIServer(upd *updater.Updater, watcher *health.Watcher) {
	server := api.NewServer(cfg, client, upd, watcher)
	if err := server.Start(); err != nil {
		log.Errorf("API server error: %v", err)
	}
}

func performHealthCheck() error {
	client, err := docker.NewClient(docker.ClientOptions{})
	if err != nil {
		return err
	}
	return client.Ping()
}
