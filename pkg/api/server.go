package api

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	_ "embed"
	"encoding/hex"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"sync"
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

//go:embed templates/login.html
var loginHTML string

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

	// Session management
	sessions   map[string]time.Time
	sessionsMu sync.RWMutex
}

// NewServer creates a new API server with web UI
func NewServer(cfg *config.Config, client docker.Client, upd *updater.Updater, watcher *health.Watcher) *Server {
	if cfg.LogLevel == "debug" {
		gin.SetMode(gin.DebugMode)
	} else {
		gin.SetMode(gin.ReleaseMode)
	}

	engine := gin.New()
	engine.Use(gin.Recovery())

	engine.Use(func(c *gin.Context) {
		start := time.Now()
		c.Next()
		log.Debugf("[GIN] %s %s %d %s", c.Request.Method, c.Request.URL.Path, c.Writer.Status(), time.Since(start))
	})

	s := &Server{
		config:   cfg,
		client:   client,
		updater:  upd,
		watcher:  watcher,
		engine:   engine,
		sessions: make(map[string]time.Time),
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

	// API v1 routes (bearer token auth)
	v1 := s.engine.Group("/v1")
	if s.config.APIToken != "" {
		v1.Use(s.apiAuthMiddleware())
	}
	{
		v1.GET("/health", s.handleHealth)
		v1.GET("/info", s.handleInfo)
		v1.GET("/containers", s.handleContainers)
		v1.POST("/update", s.handleTriggerUpdate)
		v1.POST("/containers/:id/restart", s.handleRestartContainer)
		v1.POST("/containers/:id/stop", s.handleStopContainer)
		v1.POST("/containers/:id/recreate", s.handleRecreateContainer)
		v1.GET("/containers/:id/logs", s.handleContainerLogsAPI)
	}

	// Metrics endpoint
	if s.config.MetricsEnabled {
		s.engine.GET("/metrics", s.handleMetrics)
	}

	// Login/logout (no auth required)
	s.engine.GET("/login", s.handleLoginPage)
	s.engine.POST("/login", s.handleLogin)
	s.engine.POST("/logout", s.handleLogout)

	// Web UI routes (session auth required when token is configured)
	ui := s.engine.Group("/")
	if s.config.APIToken != "" {
		ui.Use(s.uiAuthMiddleware())
	}
	{
		ui.GET("/", s.handleDashboard)
		ui.GET("/ui/containers", s.handleUIContainers)
		ui.GET("/ui/stats", s.handleUIStats)
		ui.POST("/ui/update", s.handleUITriggerUpdate)
		ui.POST("/ui/containers/:id/restart", s.handleUIRestartContainer)
		ui.POST("/ui/containers/:id/stop", s.handleUIStopContainer)
		ui.POST("/ui/containers/:id/recreate", s.handleUIRecreateContainer)
		ui.GET("/ui/containers/:id/logs", s.handleUIContainerLogs)
	}
}

// apiAuthMiddleware checks for valid API bearer token
func (s *Server) apiAuthMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		token := c.GetHeader("Authorization")
		expected := "Bearer " + s.config.APIToken
		if subtle.ConstantTimeCompare([]byte(token), []byte(expected)) != 1 {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
			return
		}
		c.Next()
	}
}

// uiAuthMiddleware checks for valid session cookie
func (s *Server) uiAuthMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		sessionID, err := c.Cookie("dockwarden_session")
		if err != nil || !s.isValidSession(sessionID) {
			c.Redirect(http.StatusFound, "/login")
			c.Abort()
			return
		}
		c.Next()
	}
}

func (s *Server) createSession() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return ""
	}
	sessionID := hex.EncodeToString(b)
	s.sessionsMu.Lock()
	s.sessions[sessionID] = time.Now().Add(24 * time.Hour)
	s.sessionsMu.Unlock()
	return sessionID
}

func (s *Server) isValidSession(sessionID string) bool {
	s.sessionsMu.RLock()
	expiry, ok := s.sessions[sessionID]
	s.sessionsMu.RUnlock()
	if !ok {
		return false
	}
	if time.Now().After(expiry) {
		s.sessionsMu.Lock()
		delete(s.sessions, sessionID)
		s.sessionsMu.Unlock()
		return false
	}
	return true
}

func (s *Server) deleteSession(sessionID string) {
	s.sessionsMu.Lock()
	delete(s.sessions, sessionID)
	s.sessionsMu.Unlock()
}

// handleLoginPage renders login form
func (s *Server) handleLoginPage(c *gin.Context) {
	// If no token configured, redirect to dashboard
	if s.config.APIToken == "" {
		c.Redirect(http.StatusFound, "/")
		return
	}
	// If already logged in, redirect
	if sessionID, err := c.Cookie("dockwarden_session"); err == nil && s.isValidSession(sessionID) {
		c.Redirect(http.StatusFound, "/")
		return
	}
	tmpl := template.Must(template.New("login").Parse(loginHTML))
	c.Header("Content-Type", "text/html; charset=utf-8")
	c.Status(http.StatusOK)
	tmpl.Execute(c.Writer, gin.H{
		"Error": c.Query("error"),
	})
}

// handleLogin processes login form
func (s *Server) handleLogin(c *gin.Context) {
	token := c.PostForm("token")
	if subtle.ConstantTimeCompare([]byte(token), []byte(s.config.APIToken)) != 1 {
		c.Redirect(http.StatusFound, "/login?error=invalid")
		return
	}

	sessionID := s.createSession()
	c.SetCookie("dockwarden_session", sessionID, 86400, "/", "", false, true)
	c.Redirect(http.StatusFound, "/")
}

// handleLogout destroys session
func (s *Server) handleLogout(c *gin.Context) {
	if sessionID, err := c.Cookie("dockwarden_session"); err == nil {
		s.deleteSession(sessionID)
	}
	c.SetCookie("dockwarden_session", "", -1, "/", "", false, true)
	c.Redirect(http.StatusFound, "/login")
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

// handleStopContainer stops a container
func (s *Server) handleStopContainer(c *gin.Context) {
	id := c.Param("id")
	ctx := context.Background()

	if err := s.client.StopContainer(ctx, id, s.config.StopTimeout); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "container stopped", "id": id})
}

// handleRecreateContainer recreates a container
func (s *Server) handleRecreateContainer(c *gin.Context) {
	id := c.Param("id")
	ctx := context.Background()

	newID, err := s.client.RecreateContainer(ctx, id, s.config.StopTimeout)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "container recreated", "id": id, "new_id": newID})
}

// handleContainerLogsAPI returns container logs via API
func (s *Server) handleContainerLogsAPI(c *gin.Context) {
	id := c.Param("id")
	ctx := context.Background()
	tail := c.DefaultQuery("tail", "100")

	reader, err := s.client.ContainerLogs(ctx, id, false, tail)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer reader.Close()

	c.Header("Content-Type", "text/plain; charset=utf-8")
	c.Status(http.StatusOK)
	io.Copy(c.Writer, reader)
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

	_ = watcherStats

	c.Data(http.StatusOK, "text/plain; charset=utf-8", []byte(metrics))
}

// handleDashboard serves the main web UI dashboard
func (s *Server) handleDashboard(c *gin.Context) {
	tmpl := template.Must(template.New("dashboard").Parse(dashboardHTML))
	c.Header("Content-Type", "text/html; charset=utf-8")
	c.Status(http.StatusOK)
	tmpl.Execute(c.Writer, gin.H{
		"Version":  meta.Version,
		"TZ":       s.config.TZ,
		"HasAuth":  s.config.APIToken != "",
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

	c.String(http.StatusOK, `<span class="text-green-500">&#10003; Update triggered</span>`)
}

// handleUIRestartContainer restarts container via HTMX
func (s *Server) handleUIRestartContainer(c *gin.Context) {
	id := c.Param("id")
	ctx := context.Background()

	if err := s.client.RestartContainer(ctx, id, s.config.StopTimeout); err != nil {
		c.String(http.StatusOK, `<span class="text-red-500">Failed: %s</span>`, err.Error())
		return
	}

	c.String(http.StatusOK, `<span class="text-green-500">&#10003; Restarted</span>`)
}

// handleUIStopContainer stops container via HTMX
func (s *Server) handleUIStopContainer(c *gin.Context) {
	id := c.Param("id")
	ctx := context.Background()

	if err := s.client.StopContainer(ctx, id, s.config.StopTimeout); err != nil {
		c.String(http.StatusOK, `<span class="text-red-500">Failed: %s</span>`, err.Error())
		return
	}

	c.String(http.StatusOK, `<span class="text-yellow-500">&#10003; Stopped</span>`)
}

// handleUIRecreateContainer recreates container via HTMX
func (s *Server) handleUIRecreateContainer(c *gin.Context) {
	id := c.Param("id")
	ctx := context.Background()

	if _, err := s.client.RecreateContainer(ctx, id, s.config.StopTimeout); err != nil {
		c.String(http.StatusOK, `<span class="text-red-500">Failed: %s</span>`, err.Error())
		return
	}

	c.String(http.StatusOK, `<span class="text-green-500">&#10003; Recreated</span>`)
}

// handleUIContainerLogs streams container logs via SSE for HTMX
func (s *Server) handleUIContainerLogs(c *gin.Context) {
	id := c.Param("id")
	follow := c.DefaultQuery("follow", "false") == "true"
	tail := c.DefaultQuery("tail", "100")

	reader, err := s.client.ContainerLogs(c.Request.Context(), id, follow, tail)
	if err != nil {
		c.String(http.StatusInternalServerError, "Error: %s", err.Error())
		return
	}
	defer reader.Close()

	c.Header("Content-Type", "text/plain; charset=utf-8")
	if follow {
		c.Header("X-Content-Type-Options", "nosniff")
		c.Status(http.StatusOK)
		c.Writer.Flush()

		buf := make([]byte, 4096)
		for {
			n, err := reader.Read(buf)
			if n > 0 {
				// Strip Docker multiplexing header (8 bytes)
				data := buf[:n]
				c.Writer.Write(stripDockerLogHeader(data))
				c.Writer.Flush()
			}
			if err != nil {
				break
			}
		}
	} else {
		c.Status(http.StatusOK)
		data, _ := io.ReadAll(reader)
		c.Writer.Write(stripDockerLogHeader(data))
	}
}

// stripDockerLogHeader strips the 8-byte multiplexing header from docker log lines
func stripDockerLogHeader(data []byte) []byte {
	var result []byte
	for len(data) >= 8 {
		// Docker log header: [stream_type, 0, 0, 0, size1, size2, size3, size4]
		size := int(data[4])<<24 | int(data[5])<<16 | int(data[6])<<8 | int(data[7])
		data = data[8:]
		if size > len(data) {
			size = len(data)
		}
		result = append(result, data[:size]...)
		data = data[size:]
	}
	if len(data) > 0 {
		result = append(result, data...)
	}
	return result
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
