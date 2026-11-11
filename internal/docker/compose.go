package docker

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/distribution/reference"
)

// Compose labels that Docker Compose writes on every container it creates.
const (
	ComposeProjectLabel     = "com.docker.compose.project"
	ComposeServiceLabel     = "com.docker.compose.service"
	ComposeConfigFilesLabel = "com.docker.compose.project.config_files"
	ComposeWorkingDirLabel  = "com.docker.compose.project.working_dir"
	ComposeEnvFileLabel     = "com.docker.compose.project.environment_file"
	ComposeOneoffLabel      = "com.docker.compose.oneoff"

	// ComposeRollLabel set to "false" on a container keeps it on the legacy
	// (clone the old container config) roll.
	ComposeRollLabel = "dockwarden.compose"

	// HelperLabel marks DockWarden's own short-lived helper containers.
	HelperLabel = "dockwarden.helper"

	// ComposeBinary is where the image ships the docker-compose binary.
	ComposeBinary = "/usr/local/bin/docker-compose"
)

// ComposeProject identifies the compose project and service a container was
// created from, as recorded in its com.docker.compose.* labels.
type ComposeProject struct {
	Project     string
	Service     string
	ConfigFiles []string
	WorkingDir  string
	EnvFiles    []string
}

// ComposeProjectOf reads the compose labels. ok is false when the container was
// not created by `docker compose up` (no labels, relative paths, a one-off
// `compose run` container) or opted out with dockwarden.compose=false.
func ComposeProjectOf(labels map[string]string) (ComposeProject, bool) {
	if labels == nil || strings.EqualFold(strings.TrimSpace(labels[ComposeRollLabel]), "false") {
		return ComposeProject{}, false
	}
	if strings.EqualFold(labels[ComposeOneoffLabel], "true") {
		return ComposeProject{}, false
	}
	p := ComposeProject{
		Project:     strings.TrimSpace(labels[ComposeProjectLabel]),
		Service:     strings.TrimSpace(labels[ComposeServiceLabel]),
		WorkingDir:  strings.TrimSpace(labels[ComposeWorkingDirLabel]),
		ConfigFiles: splitPaths(labels[ComposeConfigFilesLabel]),
		EnvFiles:    splitPaths(labels[ComposeEnvFileLabel]),
	}
	if p.Project == "" || p.Service == "" || p.WorkingDir == "" || len(p.ConfigFiles) == 0 {
		return ComposeProject{}, false
	}
	for _, path := range append(append([]string{p.WorkingDir}, p.ConfigFiles...), p.EnvFiles...) {
		if !filepath.IsAbs(path) {
			return ComposeProject{}, false
		}
	}
	return p, true
}

func splitPaths(raw string) []string {
	var out []string
	for _, part := range strings.Split(raw, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, filepath.Clean(part))
		}
	}
	return out
}

// GlobalArgs are the docker-compose arguments that select this project exactly
// as the original `docker compose up` saw it.
func (p ComposeProject) GlobalArgs() []string {
	args := []string{"--project-name", p.Project, "--project-directory", p.WorkingDir}
	for _, f := range p.ConfigFiles {
		args = append(args, "--file", f)
	}
	for _, f := range p.EnvFiles {
		args = append(args, "--env-file", f)
	}
	return args
}

// Binds returns read-only bind mounts that make the project's files visible to
// a helper container at their host paths: the working directory plus the
// directories of config and env files that live outside it. Paths stay
// identical, so relative paths in the compose file resolve to the same host
// paths the daemon sees.
func (p ComposeProject) Binds() []string {
	dirs := []string{p.WorkingDir}
	for _, f := range append(append([]string{}, p.ConfigFiles...), p.EnvFiles...) {
		dirs = append(dirs, filepath.Dir(f))
	}
	sort.Slice(dirs, func(i, j int) bool { return len(dirs[i]) < len(dirs[j]) })
	var kept []string
	for _, d := range dirs {
		covered := false
		for _, k := range kept {
			if d == k || k == "/" || strings.HasPrefix(d, k+"/") {
				covered = true
				break
			}
		}
		if !covered {
			kept = append(kept, d)
		}
	}
	binds := make([]string, 0, len(kept))
	for _, d := range kept {
		binds = append(binds, d+":"+d+":ro")
	}
	return binds
}

// ComposeService is the part of a rendered compose service DockWarden needs.
type ComposeService struct {
	Image           string
	Labels          map[string]string
	StopGracePeriod time.Duration
	StopSignal      string
}

// ParseComposeConfig extracts one service from `docker compose config --format json`.
func ParseComposeConfig(raw []byte, service string) (ComposeService, error) {
	var doc struct {
		Services map[string]struct {
			Image           string            `json:"image"`
			Labels          map[string]string `json:"labels"`
			StopGracePeriod json.RawMessage   `json:"stop_grace_period"`
			StopSignal      string            `json:"stop_signal"`
		} `json:"services"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return ComposeService{}, fmt.Errorf("parse compose config: %w", err)
	}
	svc, ok := doc.Services[service]
	if !ok {
		return ComposeService{}, fmt.Errorf("service %q not in compose config", service)
	}
	out := ComposeService{Image: svc.Image, Labels: svc.Labels, StopSignal: svc.StopSignal}
	if len(svc.StopGracePeriod) > 0 && string(svc.StopGracePeriod) != "null" {
		var s string
		if err := json.Unmarshal(svc.StopGracePeriod, &s); err != nil {
			return ComposeService{}, fmt.Errorf("parse stop_grace_period %s: %w", svc.StopGracePeriod, err)
		}
		d, err := time.ParseDuration(s)
		if err != nil {
			return ComposeService{}, fmt.Errorf("parse stop_grace_period %q: %w", s, err)
		}
		out.StopGracePeriod = d
	}
	return out, nil
}

// SameImageRef reports whether two image references name the same repository
// and tag after Docker's normalization ("nginx" == "docker.io/library/nginx:latest").
func SameImageRef(a, b string) bool {
	if a == b {
		return true
	}
	na, errA := reference.ParseNormalizedNamed(a)
	nb, errB := reference.ParseNormalizedNamed(b)
	if errA != nil || errB != nil {
		return false
	}
	return reference.TagNameOnly(na).String() == reference.TagNameOnly(nb).String()
}

// ComposeUnsetVariable reports whether docker-compose stderr says a variable
// was interpolated as blank: the original `compose up` probably had it in its
// shell environment, which a helper cannot reproduce.
func ComposeUnsetVariable(stderr string) bool {
	return strings.Contains(stderr, "variable is not set")
}
