package config

import (
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"
)

// durationValue reads a duration setting, tolerating a value with no unit.
//
// viper delegates to spf13/cast, which appends "ns" to a duration string that
// carries no unit. So a compose file saying DOCKWARDEN_INTERVAL: "60" does not
// mean a minute — it means 60 *nanoseconds*, and the update loop spins
// continuously instead of resting between passes. That shipped to real
// deployments and was invisible except as permanent CPU load: every pass logged
// "0 updated, 0 failed" and looked perfectly healthy. Nobody writing a bare
// "60" means nanoseconds; they mean seconds.
//
// Interpreting it as seconds rather than rejecting it is deliberate. Existing
// deployments already carry the unitless form, so erroring on startup would
// turn a performance bug into a simultaneous outage across every box on the
// next image pull. Warn loudly and keep running, so the config gets corrected
// without anything breaking.
//
// This runs before logging is configured, so the warning uses logrus defaults.
// That is intentional: a misconfiguration here should surface even when the
// operator has raised the log level.
func durationValue(key string) time.Duration {
	raw := strings.TrimSpace(viper.GetString(key))
	// Go duration units are ns, us/µs, ms, s, m, h — a bare number has none.
	if raw != "" && !strings.ContainsAny(raw, "nsuµmh") {
		if seconds, err := strconv.ParseFloat(raw, 64); err == nil {
			d := time.Duration(seconds * float64(time.Second))
			logrus.Warnf("%s=%q has no time unit; interpreting it as %s. "+
				"Add a unit (e.g. %qs) — without one it would be read as nanoseconds.",
				key, raw, d, raw)
			return d
		}
	}
	return viper.GetDuration(key)
}

// Config holds all configuration for DockWarden
type Config struct {
	// Observation settings for health monitoring
	ObservationPeriod  time.Duration // duration to observe before taking action
	UnhealthyThreshold int           // number of consecutive unhealthy checks before action
	RestartWindow      time.Duration // rolling window over which restarts are counted
	// Operation mode
	Mode     string // full, update, watch, monitor
	RunOnce  bool
	Interval time.Duration
	Schedule string

	// Update settings
	Cleanup         bool
	NoRestart       bool
	NoPull          bool
	MonitorOnly     bool
	RollingRestart  bool
	StopTimeout     time.Duration
	LabelEnable     bool
	LabelName       string
	Scope           string
	LabelPrecedence bool

	// Container settings
	IncludeStopped    bool
	IncludeRestarting bool
	ReviveStopped     bool
	RemoveVolumes     bool
	DisableContainers []string

	// Health monitoring
	HealthWatch  bool
	HealthAction string // restart, notify
	HealthCheck  bool   // Internal health check mode

	// Secrets
	RegistrySecret string

	// Notifications
	NotificationURL string

	// API
	APIEnabled bool
	APIPort    int
	APIToken   string

	// Metrics
	MetricsEnabled bool

	// Logging
	LogLevel  string
	LogFormat string

	// Timezone
	TZ string
}

// RegisterFlags registers all CLI flags
func RegisterFlags(cmd *cobra.Command) {
	flags := cmd.PersistentFlags()

	// Operation mode
	flags.String("mode", "full", "Operation mode: full, update, watch, monitor")
	flags.Bool("run-once", false, "Run once and exit")
	flags.Duration("interval", 1*time.Minute, "Update check interval (time between iteration end and new start)")
	flags.String("schedule", "", "Cron expression for scheduling (overrides interval)")

	// Update settings
	flags.Bool("cleanup", true, "Remove old images after update")
	flags.Bool("no-restart", false, "Only pull images, don't restart containers")
	flags.Bool("no-pull", false, "Don't pull new images")
	flags.Bool("monitor-only", false, "Monitor mode, no changes made")
	flags.Bool("rolling-restart", false, "Restart containers one at a time")
	flags.Duration("stop-timeout", 10*time.Second, "Container stop timeout")
	flags.Bool("label-enable", false, "Only manage containers with enable label")
	flags.String("label-name", "dockwarden.enable", "Label to check for container management")
	flags.String("scope", "", "Limit to containers with matching scope label")
	flags.Bool("label-take-precedence", false, "Label values take precedence over arguments")

	// Container settings
	flags.Bool("include-stopped", false, "Include stopped containers")
	flags.Bool("include-restarting", false, "Include restarting containers")
	flags.Bool("revive-stopped", false, "Restart stopped containers if updated")
	flags.Bool("remove-volumes", false, "Remove volumes when removing containers")
	flags.StringSlice("disable-containers", nil, "Container names to exclude")

	// Health monitoring
	flags.Bool("health-watch", true, "Enable health monitoring")
	flags.String("health-action", "restart", "Action on unhealthy: restart, notify")
	flags.Bool("health-check", false, "Perform health check and exit")

	// Observation settings flags
	flags.Duration("observation-period", 30*time.Second, "Duration to observe unhealthy container before action")
	flags.Int("unhealthy-threshold", 3, "Consecutive unhealthy checks before taking action")
	flags.Duration("restart-window", time.Hour, "Rolling window over which restarts are counted before giving up")

	// Secrets
	flags.String("registry-secret", "", "Path to registry authentication secret")

	// Notifications
	flags.String("notification-url", "", "Notification webhook URL")

	// API
	flags.Bool("api-enabled", false, "Enable REST API")
	flags.Int("api-port", 8080, "API listen port")
	flags.String("api-token", "", "API authentication token")

	// Metrics
	flags.Bool("metrics", false, "Enable Prometheus metrics")

	// Logging
	flags.String("log-level", "info", "Log level: debug, info, warn, error")
	flags.String("log-format", "auto", "Log format: auto, json, pretty")

	// Bind flags to viper
	viper.SetEnvPrefix("DOCKWARDEN")
	viper.SetEnvKeyReplacer(strings.NewReplacer("-", "_"))
	viper.AutomaticEnv()

	flags.VisitAll(func(f *pflag.Flag) {
		viper.BindPFlag(f.Name, f)
	})
}

// Load loads configuration from flags, environment, and secrets
func Load(cmd *cobra.Command) (*Config, error) {
	cfg := &Config{
		ObservationPeriod:  durationValue("observation-period"),
		UnhealthyThreshold: viper.GetInt("unhealthy-threshold"),
		RestartWindow:      durationValue("restart-window"),
		Mode:               viper.GetString("mode"),
		RunOnce:            viper.GetBool("run-once"),
		Interval:           durationValue("interval"),
		Schedule:           viper.GetString("schedule"),
		Cleanup:            viper.GetBool("cleanup"),
		NoRestart:          viper.GetBool("no-restart"),
		NoPull:             viper.GetBool("no-pull"),
		MonitorOnly:        viper.GetBool("monitor-only"),
		RollingRestart:     viper.GetBool("rolling-restart"),
		StopTimeout:        durationValue("stop-timeout"),
		LabelEnable:        viper.GetBool("label-enable"),
		LabelName:          viper.GetString("label-name"),
		Scope:              viper.GetString("scope"),
		LabelPrecedence:    viper.GetBool("label-take-precedence"),
		IncludeStopped:     viper.GetBool("include-stopped"),
		IncludeRestarting:  viper.GetBool("include-restarting"),
		ReviveStopped:      viper.GetBool("revive-stopped"),
		RemoveVolumes:      viper.GetBool("remove-volumes"),
		DisableContainers:  viper.GetStringSlice("disable-containers"),
		HealthWatch:        viper.GetBool("health-watch"),
		HealthAction:       viper.GetString("health-action"),
		HealthCheck:        viper.GetBool("health-check"),
		RegistrySecret:     viper.GetString("registry-secret"),
		NotificationURL:    viper.GetString("notification-url"),
		APIEnabled:         viper.GetBool("api-enabled"),
		APIPort:            viper.GetInt("api-port"),
		APIToken:           viper.GetString("api-token"),
		MetricsEnabled:     viper.GetBool("metrics"),
		LogLevel:           viper.GetString("log-level"),
		LogFormat:          viper.GetString("log-format"),
		TZ:                 os.Getenv("TZ"),
	}

	// Load secrets from files
	if err := loadSecrets(cfg); err != nil {
		return nil, err
	}

	return cfg, nil
}

// loadSecrets reads secret values from files (Docker secrets support)
func loadSecrets(cfg *Config) error {
	// Registry secret
	if cfg.RegistrySecret == "" {
		cfg.RegistrySecret = os.Getenv("DOCKWARDEN_REGISTRY_SECRET")
	}
	if secretFile := os.Getenv("DOCKWARDEN_REGISTRY_SECRET_FILE"); secretFile != "" {
		if data, err := os.ReadFile(secretFile); err == nil {
			cfg.RegistrySecret = string(data)
		}
	}

	// Notification URL
	if cfg.NotificationURL == "" {
		cfg.NotificationURL = os.Getenv("DOCKWARDEN_NOTIFICATION_URL")
	}
	if secretFile := os.Getenv("DOCKWARDEN_NOTIFICATION_URL_FILE"); secretFile != "" {
		if data, err := os.ReadFile(secretFile); err == nil {
			cfg.NotificationURL = strings.TrimSpace(string(data))
		}
	}

	// API Token
	if cfg.APIToken == "" {
		cfg.APIToken = os.Getenv("DOCKWARDEN_API_TOKEN")
	}
	if secretFile := os.Getenv("DOCKWARDEN_API_TOKEN_FILE"); secretFile != "" {
		if data, err := os.ReadFile(secretFile); err == nil {
			cfg.APIToken = strings.TrimSpace(string(data))
		}
	}

	return nil
}
