package api

import (
	"context"
	_ "embed"
	"fmt"
	"html/template"
	"net/http"
	"time"

	"github.com/emon5122/dockwarden/internal/config"
	"github.com/emon5122/dockwarden/internal/docker"
	"github.com/emon5122/dockwarden/internal/health"
	"github.com/emon5122/dockwarden/internal/meta"
	"github.com/emon5122/dockwarden/internal/updater"
	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

//go:embed templates/dashboard.html
var dashboardHTML string

//go:embed templates/stats.html
var statsHTML string

//go:embed templates/containers.html
var containersHTML string

// Server is the Gin-based web server with HTMX UI
type Server struct {
	config  *config.Config
	client  docker.Client
	updater *updater.Updater
	watcher *health.Watcher
	engine  *gin.Engine
}

// NewServer creates a new API server with web UI
func NewServer(cfg *config.Config, client docker.Client, upd *updater.Updater, watcher *health.Watcher) *Server {
	// Set Gin mode based on log level
	if cfg.LogLevel == "debug" {
		gin.SetMode(gin.DebugMode)
	} else {
		gin.SetMode(gin.ReleaseMode)
	}

	engine := gin.New()
	engine.Use(gin.Recovery())

	// Custom logger that integrates with logrus
	engine.Use(func(c *gin.Context) {
		start := time.Now()
		c.Next()
		log.Debugf("[GIN] %s %s %d %s", c.Request.Method, c.Request.URL.Path, c.Writer.Status(), time.Since(start))
	})

	s := &Server{
		config:  cfg,
		client:  client,
		updater: upd,
		watcher: watcher,
		engine:  engine,
	}

	s.setupRoutes()
	return s
}

// Start starts the web server
func (s *Server) Start() error {
	addr := fmt.Sprintf(":%d", s.config.APIPort)
	log.Infof("Starting web server on http://0.0.0.0%s", addr)
	return s.engine.Run(addr)
}

// setupRoutes configures all routes
func (s *Server) setupRoutes() {
	// Health endpoint (no auth)
	s.engine.GET("/health", s.handleHealth)

	// API v1 routes
	v1 := s.engine.Group("/v1")
	if s.config.APIToken != "" {
		v1.Use(s.authMiddleware())
	}
	{
		v1.GET("/health", s.handleHealth)
		v1.GET("/info", s.handleInfo)
		v1.GET("/containers", s.handleContainers)
		v1.POST("/update", s.handleTriggerUpdate)
		v1.POST("/containers/:id/restart", s.handleRestartContainer)
	}

	// Metrics endpoint
	if s.config.MetricsEnabled {
		s.engine.GET("/metrics", s.handleMetrics)
	}

	// Web UI routes
	s.engine.GET("/", s.handleDashboard)
	s.engine.GET("/ui/containers", s.handleUIContainers)
	s.engine.GET("/ui/stats", s.handleUIStats)
	s.engine.POST("/ui/update", s.handleUITriggerUpdate)
	s.engine.POST("/ui/containers/:id/restart", s.handleUIRestartContainer)
}

// authMiddleware checks for valid API token
func (s *Server) authMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		token := c.GetHeader("Authorization")
		if token != "Bearer "+s.config.APIToken {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
			return
		}
		c.Next()
	}
}

// handleHealth handles health check requests
func (s *Server) handleHealth(c *gin.Context) {
	err := s.client.Ping()

	status := "ok"
	dockerStatus := "connected"
	httpStatus := http.StatusOK

	if err != nil {
		status = "unhealthy"
		dockerStatus = "unreachable"
		httpStatus = http.StatusServiceUnavailable
	}

	c.JSON(httpStatus, gin.H{
		"status": status,
		"docker": dockerStatus,
		"time":   time.Now().UTC().Format(time.RFC3339),
	})
}

// handleInfo handles info requests
func (s *Server) handleInfo(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"name":     "DockWarden",
		"version":  meta.Version,
		"commit":   meta.Commit,
		"built":    meta.BuildDate,
		"mode":     s.config.Mode,
		"interval": s.config.Interval.String(),
		"cleanup":  s.config.Cleanup,
	})
}

// handleContainers returns all managed containers
func (s *Server) handleContainers(c *gin.Context) {
	ctx := context.Background()
	containers, err := s.client.ListContainers(ctx, docker.ListOptions{
		All:           s.config.IncludeStopped,
		IncludeHealth: true,
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"containers": containers,
		"count":      len(containers),
	})
}

// handleTriggerUpdate triggers an update check
func (s *Server) handleTriggerUpdate(c *gin.Context) {
	if s.updater == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "updater not available"})
		return
	}

	go func() {
		if err := s.updater.Run(); err != nil {
			log.Errorf("Manual update failed: %v", err)
		}
	}()

	c.JSON(http.StatusAccepted, gin.H{"message": "update triggered"})
}

// handleRestartContainer restarts a specific container
func (s *Server) handleRestartContainer(c *gin.Context) {
	id := c.Param("id")
	ctx := context.Background()

	if err := s.client.RestartContainer(ctx, id, s.config.StopTimeout); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "container restarted", "id": id})
}

// handleMetrics returns Prometheus metrics
func (s *Server) handleMetrics(c *gin.Context) {
	var updaterStats, watcherStats map[string]interface{}

	if s.updater != nil {
		updaterStats = s.updater.GetStats()
	}
	if s.watcher != nil {
		watcherStats = s.watcher.GetStats()
	}

	ctx := context.Background()
	containers, _ := s.client.ListContainers(ctx, docker.ListOptions{All: true})

	running := 0
	unhealthy := 0
	for _, c := range containers {
		if c.IsRunning() {
			running++
		}
		if c.IsUnhealthy() {
			unhealthy++
		}
	}

	// Return Prometheus-style metrics
	metrics := fmt.Sprintf(`# HELP dockwarden_containers_total Total number of containers
# TYPE dockwarden_containers_total gauge
dockwarden_containers_total %d

# HELP dockwarden_containers_running Number of running containers
# TYPE dockwarden_containers_running gauge
dockwarden_containers_running %d

# HELP dockwarden_containers_unhealthy Number of unhealthy containers
# TYPE dockwarden_containers_unhealthy gauge
dockwarden_containers_unhealthy %d

# HELP dockwarden_updates_total Total number of successful updates
# TYPE dockwarden_updates_total counter
dockwarden_updates_total %d

# HELP dockwarden_update_failures_total Total number of failed updates
# TYPE dockwarden_update_failures_total counter
dockwarden_update_failures_total %d
`,
		len(containers),
		running,
		unhealthy,
		getInt64(updaterStats, "total_updated"),
		getInt64(updaterStats, "total_failed"),
	)

	_ = watcherStats // Available for future metrics

	c.Data(http.StatusOK, "text/plain; charset=utf-8", []byte(metrics))
}

// handleDashboard serves the main web UI dashboard
func (s *Server) handleDashboard(c *gin.Context) {
	tmpl := template.Must(template.New("dashboard").Parse(dashboardHTML))
	c.Header("Content-Type", "text/html; charset=utf-8")
	c.Status(http.StatusOK)
	tmpl.Execute(c.Writer, gin.H{
		"Version": meta.Version,
		"TZ":      s.config.TZ,
	})
}

// handleUIContainers returns HTMX fragment for containers table
func (s *Server) handleUIContainers(c *gin.Context) {
	ctx := context.Background()
	containers, err := s.client.ListContainers(ctx, docker.ListOptions{
		All:           true,
		IncludeHealth: true,
	})
	if err != nil {
		c.String(http.StatusInternalServerError, `<div class="text-red-500">Error loading containers: %s</div>`, err.Error())
		return
	}

	tmpl := template.Must(template.New("containers").Parse(containersHTML))
	c.Header("Content-Type", "text/html; charset=utf-8")
	c.Status(http.StatusOK)
	tmpl.Execute(c.Writer, containers)
}

// handleUIStats returns HTMX fragment for stats
func (s *Server) handleUIStats(c *gin.Context) {
	ctx := context.Background()
	containers, _ := s.client.ListContainers(ctx, docker.ListOptions{All: true})

	running := 0
	unhealthy := 0
	for _, ctr := range containers {
		if ctr.IsRunning() {
			running++
		}
		if ctr.IsUnhealthy() {
			unhealthy++
		}
	}

	var updaterStats map[string]interface{}
	if s.updater != nil {
		updaterStats = s.updater.GetStats()
	}

	tmpl := template.Must(template.New("stats").Parse(statsHTML))
	c.Header("Content-Type", "text/html; charset=utf-8")
	c.Status(http.StatusOK)
	tmpl.Execute(c.Writer, gin.H{
		"Total":     len(containers),
		"Running":   running,
		"Unhealthy": unhealthy,
		"Updated":   getInt64(updaterStats, "total_updated"),
	})
}

// handleUITriggerUpdate triggers update via HTMX
func (s *Server) handleUITriggerUpdate(c *gin.Context) {
	if s.updater == nil {
		c.String(http.StatusOK, `<span class="text-red-500">Updater not available</span>`)
		return
	}

	go func() {
		if err := s.updater.Run(); err != nil {
			log.Errorf("Manual update failed: %v", err)
		}
	}()

	c.String(http.StatusOK, `<span class="text-green-500">✓ Update triggered</span>`)
}

// handleUIRestartContainer restarts container via HTMX
func (s *Server) handleUIRestartContainer(c *gin.Context) {
	id := c.Param("id")
	ctx := context.Background()

	if err := s.client.RestartContainer(ctx, id, s.config.StopTimeout); err != nil {
		c.String(http.StatusOK, `<span class="text-red-500">Failed: %s</span>`, err.Error())
		return
	}

	c.String(http.StatusOK, `<span class="text-green-500">✓ Restarted</span>`)
}

func getInt64(m map[string]interface{}, key string) int64 {
	if m == nil {
		return 0
	}
	if v, ok := m[key].(int64); ok {
		return v
	}
	return 0
}
