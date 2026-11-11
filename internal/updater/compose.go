package updater

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/emon5122/dockwarden/internal/docker"
	log "github.com/sirupsen/logrus"
)

// Compose rolls (2026-11-12): a release used to recreate a compose service
// from the OLD container's config, so anything changed in the deployed compose
// file (labels such as dockwarden.stop-timeout, stop_grace_period, env /
// env_file, mem_limit / cpus, volumes) stayed stale until a human ran
// `docker compose up -d`. Now a container that compose created is rolled
// through its own compose project:
//
//  1. a helper container (DockWarden's own image, which ships docker-compose)
//     renders the project with `config --format json` from the files at their
//     host paths (mounted read-only at the same paths);
//  2. the rendered image must be the image DockWarden just pulled and no
//     variable may be blank (the original `up` may have had it in its shell);
//     otherwise the legacy recreate runs, exactly as before;
//  3. DockWarden stops the container itself with the LONGER of the old and the
//     new stop timeout (dockwarden.stop-timeout label or stop_grace_period) and
//     the dockwarden.stop-signal label, so drains are never cut short;
//  4. the helper runs `up -d --no-deps --force-recreate --pull never <service>`.
//
// Any failure before the stop falls back to the legacy recreate; a failure of
// `up` with the old container still present falls back too (and starts it).

// composeEnv is what a helper needs: an image with docker-compose and the
// daemon socket to mount.
type composeEnv struct {
	image      string
	socketBind string
}

type composeOnce struct {
	once   sync.Once
	rolled bool
	err    error
}

// composeFallback means the compose roll did not happen and the legacy
// recreate should run. stoppedByUs: DockWarden already stopped the container,
// so the legacy recreate must start it again.
type composeFallback struct {
	err         error
	stoppedByUs bool
}

func (f *composeFallback) Error() string { return f.err.Error() }
func (f *composeFallback) Unwrap() error { return f.err }

func fallback(stopped bool, format string, args ...interface{}) error {
	return &composeFallback{err: fmt.Errorf(format, args...), stoppedByUs: stopped}
}

const (
	composeRenderTimeout = 2 * time.Minute
	composeUpExtra       = 5 * time.Minute
	helperSocketPath     = "/var/run/docker.sock"
)

// prepareCompose sets up compose rolls for this pass. They need DockWarden to
// run in a container (its image is the helper image); otherwise every
// container keeps the legacy recreate.
func (u *Updater) prepareCompose(ctx context.Context, self *docker.Container, selfHint string) {
	u.compose = nil
	u.composeRollsMu.Lock()
	u.composeRolls = map[string]*composeOnce{}
	u.composeRollsMu.Unlock()
	if !u.config.ComposeRoll {
		return
	}
	if self == nil && selfHint != "" {
		// Right after start the daemon may not list us as running yet (seen on
		// the first, run-once pass); an inspect by the hint still works.
		if c, err := u.client.GetContainer(ctx, selfHint); err == nil && isSelfContainer(c) {
			self = &c
		}
	}
	if self == nil {
		log.Debug("Compose rolls off this pass: DockWarden's own container was not found")
		return
	}
	if self.ImageID == "" {
		log.Debugf("Compose rolls off this pass: no image ID for DockWarden's container %s", self.Name)
		return
	}
	// The helper gets the same daemon socket DockWarden has. Without a
	// docker.sock bind (a socket proxy over DOCKER_HOST=tcp://...), rolls stay
	// legacy rather than hand a helper the host's full socket.
	socketSrc := ""
	if binds, err := u.client.GetContainerBinds(ctx, self.ID); err == nil {
		for _, b := range binds {
			if src, _, ok := strings.Cut(b, ":"); ok && strings.Contains(b, "docker.sock") {
				socketSrc = src
				break
			}
		}
	}
	if socketSrc == "" {
		log.Debug("Compose rolls off this pass: DockWarden has no docker.sock bind mount")
		return
	}
	u.compose = &composeEnv{image: self.ImageID, socketBind: socketSrc + ":" + helperSocketPath}
	u.removeStaleHelpers(ctx)
}

// removeStaleHelpers removes finished helpers a previous DockWarden left behind
// (it died before removing them). Running ones are left alone: an orphaned
// `up` finishes on its own and is removed on a later pass.
func (u *Updater) removeStaleHelpers(ctx context.Context) {
	helpers, err := u.client.ListContainers(ctx, docker.ListOptions{All: true, LabelFilter: docker.HelperLabel})
	if err != nil {
		return
	}
	for _, h := range helpers {
		if h.GetLabel(docker.HelperLabel) == "" || h.IsRunning() {
			continue
		}
		if err := u.client.RemoveContainer(ctx, h.ID); err != nil {
			log.Debugf("remove stale helper %s: %v", h.Name, err)
		}
	}
}

// composeRoll rolls ctr through its compose project. rolled=false with a nil
// error means "not a compose container": the caller does the legacy recreate.
func (u *Updater) composeRoll(ctx context.Context, ctr docker.Container, timeout time.Duration) (bool, error) {
	if u.compose == nil {
		return false, nil
	}
	project, ok := docker.ComposeProjectOf(ctr.Labels)
	if !ok {
		return false, nil
	}

	key := project.Project + "/" + project.Service
	u.composeRollsMu.Lock()
	once, seen := u.composeRolls[key]
	if !seen {
		once = &composeOnce{}
		u.composeRolls[key] = once
	}
	u.composeRollsMu.Unlock()

	first := false
	once.once.Do(func() {
		first = true
		once.rolled, once.err = u.composeRollOnce(ctx, ctr, project, timeout)
	})
	switch {
	case first:
		return once.rolled, once.err
	case once.rolled:
		// `up` recreated every replica of the service already
		log.Infof("Container %s: already rolled with compose service %s", ctr.Name, key)
		return true, nil
	}
	var fb *composeFallback
	if errors.As(once.err, &fb) && !fb.stoppedByUs {
		// compose did nothing (render/check refused): each replica takes the legacy roll
		return false, fallback(false, "compose roll of %s not possible: %v", key, fb.err)
	}
	// compose up ran and failed: leave the other replicas alone this pass
	return false, fmt.Errorf("compose roll of %s failed for a sibling replica: %w", key, once.err)
}

func (u *Updater) composeRollOnce(ctx context.Context, ctr docker.Container, p docker.ComposeProject, timeout time.Duration) (bool, error) {
	current, err := u.client.GetContainer(ctx, ctr.ID)
	if err != nil {
		return false, fallback(false, "inspect: %v", err)
	}
	if !current.IsRunning() {
		// stopped containers keep the legacy semantics (left stopped unless revive-stopped)
		return false, fallback(false, "container is not running")
	}

	res, err := u.runCompose(ctx, p, "render", composeRenderTimeout, "config", "--format", "json", p.Service)
	if err != nil {
		return false, fallback(false, "render compose project %s: %v", p.Project, err)
	}
	if docker.ComposeUnsetVariable(res.Stderr) {
		return false, fallback(false, "compose project %s interpolates a variable that is not set (%s)", p.Project, firstLine(res.Stderr))
	}
	svc, err := docker.ParseComposeConfig([]byte(res.Stdout), p.Service)
	if err != nil {
		return false, fallback(false, "%v", err)
	}
	if !docker.SameImageRef(svc.Image, current.Image) {
		return false, fallback(false, "compose file now names image %q, container runs %q", svc.Image, current.Image)
	}

	stopTimeout := timeout
	if t := (docker.Container{Name: ctr.Name, Labels: svc.Labels}).GetStopTimeout(0); t > stopTimeout {
		stopTimeout = t
	}
	if svc.StopGracePeriod > stopTimeout {
		stopTimeout = svc.StopGracePeriod
	}
	// new label > old label > compose stop_signal > the container's own STOPSIGNAL
	signal := svc.Labels["dockwarden.stop-signal"]
	if signal == "" {
		signal = current.GetStopSignal()
	}
	if signal == "" {
		signal = svc.StopSignal
	}

	// What the roll changes, keys only (values may be secrets). A variable the
	// original `up` took from its shell and the file defaults (${VAR:-x}) shows
	// up here as a changed key.
	if keys := docker.ChangedEnvKeys(svc.Environment, current.Env); len(keys) > 0 {
		log.Warnf("Container %s: compose roll changes env %s (from the compose file / env files on disk)", ctr.Name, strings.Join(keys, ","))
	}
	if keys := docker.ChangedLabelKeys(svc.Labels, current.Labels); len(keys) > 0 {
		log.Infof("Container %s: compose roll changes labels %s", ctr.Name, strings.Join(keys, ","))
	}

	log.Infof("Container %s: rolling through compose project %s (service %s, stop timeout %s)", ctr.Name, p.Project, p.Service, stopTimeout)
	if err := u.client.StopContainer(ctx, ctr.ID, stopTimeout, signal); err != nil {
		return false, fmt.Errorf("stop before compose roll: %w", err)
	}

	secs := strconv.Itoa(int(stopTimeout.Seconds()))
	res, err = u.runCompose(ctx, p, "up", stopTimeout+composeUpExtra,
		"up", "--detach", "--no-deps", "--force-recreate", "--pull", "never", "--no-build", "--timeout", secs, p.Service)
	if err == nil {
		return true, nil
	}

	// compose up failed after our stop: get the service back up
	if old, inspectErr := u.client.GetContainer(ctx, ctr.ID); inspectErr == nil {
		// compose renames the old container to <id>_<name> before it creates the
		// new one; give the name back (removing a replacement that never ran)
		// so the legacy recreate keeps the real name
		if old.Name != ctr.Name {
			if renameErr := u.reclaimName(ctx, ctr); renameErr != nil {
				return false, fmt.Errorf("compose up for %s/%s failed (%v) and the old container could not get its name back: %w", p.Project, p.Service, err, renameErr)
			}
		}
		return false, fallback(true, "compose up: %v", err)
	}
	u.startComposeService(ctx, p)
	return false, fmt.Errorf("compose up for %s/%s failed after the old container was replaced: %w", p.Project, p.Service, err)
}

// reclaimName renames the old container back to its own name, first removing
// a not-running container compose left holding it.
func (u *Updater) reclaimName(ctx context.Context, ctr docker.Container) error {
	all, err := u.client.ListContainers(ctx, docker.ListOptions{All: true})
	if err != nil {
		return err
	}
	for _, c := range all {
		if c.Name != ctr.Name || c.ID == ctr.ID {
			continue
		}
		if c.IsRunning() {
			return fmt.Errorf("container %s (%s) is running under the name", c.Name, truncateID(c.ID))
		}
		if err := u.client.RemoveContainer(ctx, c.ID); err != nil {
			return err
		}
	}
	return u.client.RenameContainer(ctx, ctr.ID, ctr.Name)
}

// startComposeService starts any stopped container of the service (best effort
// after a failed `up` that already removed the old container).
func (u *Updater) startComposeService(ctx context.Context, p docker.ComposeProject) {
	containers, err := u.client.ListContainers(ctx, docker.ListOptions{All: true, LabelFilter: docker.ComposeProjectLabel + "=" + p.Project})
	if err != nil {
		log.Warnf("compose %s/%s: list after failed up: %v", p.Project, p.Service, err)
		return
	}
	for _, c := range containers {
		if c.GetLabel(docker.ComposeServiceLabel) != p.Service || c.IsRunning() {
			continue
		}
		if err := u.client.StartContainer(ctx, c.ID); err != nil {
			log.Warnf("compose %s/%s: start %s after failed up: %v", p.Project, p.Service, c.Name, err)
		}
	}
}

// runCompose runs docker-compose in a helper; a non-zero exit is an error
// carrying compose's stderr (never stdout: a rendered config holds env values).
func (u *Updater) runCompose(ctx context.Context, p docker.ComposeProject, step string, timeout time.Duration, args ...string) (docker.HelperResult, error) {
	binds := append([]string{u.compose.socketBind}, p.Binds()...)
	spec := docker.HelperSpec{
		Name:       fmt.Sprintf("dockwarden-compose-%s-%s-%s-%d", step, p.Project, p.Service, u.now().UnixNano()),
		Image:      u.compose.image,
		Entrypoint: []string{docker.ComposeBinary},
		Cmd:        append(append([]string{"--ansi", "never"}, p.GlobalArgs()...), args...),
		Binds:      binds,
		Env:        []string{"DOCKER_HOST=unix://" + helperSocketPath, "HOME=/"},
		Timeout:    timeout,
	}
	res, err := u.client.RunHelper(ctx, spec)
	if err != nil {
		return res, err
	}
	if res.ExitCode != 0 {
		return res, fmt.Errorf("docker-compose %s exited %d: %s", args[0], res.ExitCode, lastLines(res.Stderr, 5))
	}
	return res, nil
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return line
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, " | ")
}
