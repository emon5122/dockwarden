package updater

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"

	"github.com/emon5122/dockwarden/internal/config"
	"github.com/emon5122/dockwarden/internal/docker"
	"github.com/moby/moby/api/pkg/stdcopy"
	log "github.com/sirupsen/logrus"
)

// Detached rolls (2026-11-20, card #1181): a container whose stop timeout is
// longer than Config.DetachStopTimeout (titli-agent drains live calls for up
// to 4 h) used to be stopped inside the update cycle, so the cycle and, with
// rolling-restart, every other container waited for the whole drain. Now its
// roll runs in a helper container started from DockWarden's own image
// (`dockwarden roll`), and the cycle moves on at once:
//
//   - the helper does what the cycle would have done: stop with the signal and
//     timeout, then `docker-compose up` (compose rolls, same fallbacks) or the
//     legacy recreate, then the old-image cleanup;
//   - every pass lists roll helpers (label dockwarden.roll.target). A running
//     one marks its target in flight: that container (every replica of a compose
//     service) is skipped before any registry call. A finished one is logged
//     with its exit code and removed;
//   - helpers outlive DockWarden, so a self-update in the middle of a drain does
//     not lose the roll, and the new DockWarden still sees it in flight.

const (
	// RollTargetLabel holds the roll key of the container a roll helper rolls.
	RollTargetLabel = "dockwarden.roll.target"
	// RollContainerLabel holds the target container's name (for logs).
	RollContainerLabel = "dockwarden.roll.container"

	rollHelperKind = "roll"

	// Roll modes of the `dockwarden roll` helper command.
	RollModeCompose  = "compose"
	RollModeRecreate = "recreate"

	// rollHelperExtra is how long a roll helper may take beyond its drains.
	rollHelperExtra = composeUpExtra + time.Minute
	// rollHelperStartGrace: a helper still "created" after this never started
	// (DockWarden died between create and start) and is removed.
	rollHelperStartGrace = 2 * time.Minute
)

// RollHelperDeadline is how long a roll helper may run for a stop timeout:
// its own drain, then `up` (which drains any other replica with the same
// timeout), plus slack.
func RollHelperDeadline(stopTimeout time.Duration) time.Duration {
	return 2*stopTimeout + rollHelperExtra
}

// errRollDetached means the roll was handed to a helper: not a failure, not
// finished yet.
var errRollDetached = errors.New("roll handed to a detached helper")

// rollKey identifies what one roll changes: a whole compose service (compose
// `up` recreates every replica) or one container.
func rollKey(ctr docker.Container) string {
	if p, s := ctr.GetLabel(docker.ComposeProjectLabel), ctr.GetLabel(docker.ComposeServiceLabel); p != "" && s != "" {
		return "compose:" + p + "/" + s
	}
	return "container:" + ctr.Name
}

// detach reports whether a roll that stops with timeout must run in a helper.
func (u *Updater) detach(timeout time.Duration) bool {
	return u.config.DetachStopTimeout > 0 && timeout > u.config.DetachStopTimeout
}

// collectRolls runs at the start of a pass: running roll helpers mark their
// targets in flight, finished ones are reported and removed.
func (u *Updater) collectRolls(ctx context.Context) {
	inFlight := map[string]string{}
	helpers, err := u.client.ListContainers(ctx, docker.ListOptions{All: true, LabelFilter: RollTargetLabel})
	u.rollsMu.Lock()
	u.rollsInFlight, u.rollsUnknown = inFlight, err != nil
	u.rollsMu.Unlock()
	if err != nil {
		log.Warnf("Could not list roll helpers (%v): containers with a long stop timeout are not rolled this pass", err)
		return
	}
	for _, h := range helpers {
		key := h.GetLabel(RollTargetLabel)
		if key == "" {
			continue
		}
		target := h.GetLabel(RollContainerLabel)
		staleCreated := h.State == "created" && !h.Created.IsZero() && u.now().Sub(h.Created) > rollHelperStartGrace
		if h.State != "exited" && h.State != "dead" && !staleCreated {
			// running, created, restarting or paused: the roll is not over
			inFlight[key] = h.Name
			log.Infof("Container %s: roll still draining in helper %s", target, h.Name)
			continue
		}
		u.reportRoll(ctx, h, target)
		if err := u.client.RemoveContainer(ctx, h.ID); err != nil {
			log.Warnf("Remove finished roll helper %s: %v", h.Name, err)
			// keep it marked: a helper we cannot remove would otherwise be reported every pass
			inFlight[key] = h.Name
		}
	}
}

func (u *Updater) reportRoll(ctx context.Context, h docker.Container, target string) {
	info, err := u.client.GetContainer(ctx, h.ID)
	if err != nil {
		log.Warnf("Detached roll of %s (helper %s): result unknown: %v", target, h.Name, err)
		return
	}
	if info.ExitCode == 0 {
		log.Infof("Updated container %s (detached roll, helper %s)", target, h.Name)
		u.totalUpdated.Add(1)
		return
	}
	u.totalFailed.Add(1)
	why := ""
	if info.State == "created" {
		log.Errorf("Detached roll of %s never started (helper %s): it is retried on a later pass", target, h.Name)
		return
	}
	if info.ExitCode == 137 {
		why = ", killed: the container may be left stopped"
	}
	log.Errorf("Detached roll of %s failed (helper %s exit %d%s): %s", target, h.Name, info.ExitCode, why, u.helperTail(ctx, h.ID))
}

// helperTail returns the last lines a helper wrote (best effort).
func (u *Updater) helperTail(ctx context.Context, id string) string {
	rc, err := u.client.ContainerLogs(ctx, id, false, "5")
	if err != nil {
		return "no logs: " + err.Error()
	}
	defer rc.Close()
	raw, err := io.ReadAll(io.LimitReader(rc, 64<<10))
	if err != nil {
		return "no logs: " + err.Error()
	}
	var out bytes.Buffer
	if _, err := stdcopy.StdCopy(&out, &out, bytes.NewReader(raw)); err != nil {
		out.Reset()
		out.Write(raw) // a TTY or plain stream
	}
	return lastLines(out.String(), 5)
}

// rollInFlight returns the helper rolling ctr right now, if any.
func (u *Updater) rollInFlight(ctr docker.Container) (string, bool) {
	u.rollsMu.Lock()
	defer u.rollsMu.Unlock()
	h, ok := u.rollsInFlight[rollKey(ctr)]
	return h, ok
}

// rollRequest is what a detached roll needs from the pass that started it.
type rollRequest struct {
	mode         string
	project      docker.ComposeProject // RollModeCompose only
	timeout      time.Duration
	signal       string
	startStopped bool
	cleanupImage string
}

// detachRoll hands a long-drain roll to a helper. nil: roll inside the pass
// (short stop timeout, or no helper image); errRollDetached: a helper rolls it;
// any other error: do not roll it this pass.
func (u *Updater) detachRoll(ctx context.Context, ctr docker.Container, r rollRequest) error {
	if !u.detach(r.timeout) {
		return nil
	}
	u.rollsMu.Lock()
	unknown := u.rollsUnknown
	u.rollsMu.Unlock()
	if unknown {
		// a helper may be draining it right now: rolling here could roll it twice
		return fmt.Errorf("stop timeout %s is over %s and roll helpers could not be listed: not rolled this pass", r.timeout, u.config.DetachStopTimeout)
	}
	if u.compose == nil {
		log.Warnf("Container %s: stop timeout %s is over %s but no helper image (DockWarden's container or its docker.sock bind was not found): rolling inside this pass", ctr.Name, r.timeout, u.config.DetachStopTimeout)
		return nil
	}
	return u.startDetachedRoll(ctx, ctr, r)
}

// startDetachedRoll starts the roll helper for ctr and marks it in flight.
func (u *Updater) startDetachedRoll(ctx context.Context, ctr docker.Container, r rollRequest) error {
	key := rollKey(ctr)
	cmd := []string{"roll",
		"--container", ctr.ID,
		"--mode", r.mode,
		"--timeout", r.timeout.String(),
		"--signal", r.signal,
		fmt.Sprintf("--start-stopped=%t", r.startStopped),
		"--cleanup-image", r.cleanupImage,
		fmt.Sprintf("--remove-volumes=%t", u.config.RemoveVolumes),
	}
	binds := []string{u.compose.socketBind}
	name := fmt.Sprintf("dockwarden-roll-%s-%d", ctr.Name, u.now().UnixNano())
	if r.mode == RollModeCompose {
		binds = append(binds, r.project.Binds()...)
		// the same name as the compose `up` helper a short roll runs
		name = fmt.Sprintf("dockwarden-compose-up-%s-%s-%d", r.project.Project, r.project.Service, u.now().UnixNano())
	}
	spec := docker.HelperSpec{
		Name:       name,
		Image:      u.compose.image,
		Entrypoint: []string{"/dockwarden"},
		Cmd:        cmd,
		Binds:      binds,
		Env:        []string{"DOCKER_HOST=unix://" + helperSocketPath, "HOME=/"},
		Kind:       rollHelperKind,
		Labels:     map[string]string{RollTargetLabel: key, RollContainerLabel: ctr.Name},
	}
	if _, err := u.client.StartHelper(ctx, spec); err != nil {
		return fmt.Errorf("start roll helper: %w", err)
	}
	u.rollsMu.Lock()
	if u.rollsInFlight == nil {
		u.rollsInFlight = map[string]string{}
	}
	u.rollsInFlight[key] = name
	u.rollsMu.Unlock()
	log.Infof("Container %s: stop timeout %s is over %s, rolling in detached helper %s", ctr.Name, r.timeout, u.config.DetachStopTimeout, name)
	return errRollDetached
}

// recreate is the legacy roll, detached when its stop timeout is long.
func (u *Updater) recreate(ctx context.Context, ctr docker.Container, timeout time.Duration, startStopped bool) error {
	if err := u.detachRoll(ctx, ctr, rollRequest{mode: RollModeRecreate, timeout: timeout,
		signal: ctr.GetStopSignal(), startStopped: startStopped, cleanupImage: u.cleanupImageOf(ctr)}); err != nil {
		return err
	}
	if _, err := u.client.RecreateContainer(ctx, ctr.ID, timeout, startStopped); err != nil {
		return fmt.Errorf("failed to recreate container: %w", err)
	}
	return nil
}

func (u *Updater) cleanupImageOf(ctr docker.Container) string {
	if u.config.Cleanup {
		return ctr.ImageID
	}
	return ""
}

// DetachedRoll is one `dockwarden roll` run inside a helper container.
type DetachedRoll struct {
	ContainerID  string
	Mode         string
	Timeout      time.Duration
	Signal       string
	StartStopped bool
	CleanupImage string
	// RemoveVolumes: the recreate removes the old container's anonymous volumes
	RemoveVolumes bool
}

// RunDetachedRoll performs a roll inside a roll helper: the drain, then the
// compose `up` (with the same fallbacks as a roll inside the pass) or the
// legacy recreate. A nil error means the container runs the new image.
func RunDetachedRoll(ctx context.Context, client docker.Client, r DetachedRoll) error {
	u := New(client, &config.Config{})
	u.composeExec = execCompose
	return u.runDetachedRoll(ctx, r)
}

func (u *Updater) runDetachedRoll(ctx context.Context, r DetachedRoll) error {
	ctr, err := u.client.GetContainer(ctx, r.ContainerID)
	if err != nil {
		return fmt.Errorf("inspect %s: %w", r.ContainerID, err)
	}
	switch r.Mode {
	case RollModeRecreate:
		log.Infof("roll: recreating %s (stop timeout %s)", ctr.Name, r.Timeout)
		if _, err := u.client.RecreateContainer(ctx, ctr.ID, r.Timeout, r.StartStopped); err != nil {
			return fmt.Errorf("recreate %s: %w", ctr.Name, err)
		}
	case RollModeCompose:
		p, ok := docker.ComposeProjectOf(ctr.Labels)
		if !ok {
			return fmt.Errorf("%s has no usable compose labels", ctr.Name)
		}
		log.Infof("roll: stopping %s (signal %q, timeout %s), then compose up %s/%s", ctr.Name, r.Signal, r.Timeout, p.Project, p.Service)
		if err := u.client.StopContainer(ctx, ctr.ID, r.Timeout, r.Signal); err != nil {
			return fmt.Errorf("stop before compose roll: %w", err)
		}
		if _, err := u.composeUp(ctx, ctr, p, r.Timeout); err != nil {
			var fb *composeFallback
			if !errors.As(err, &fb) {
				return err
			}
			log.Warnf("roll: compose roll of %s not possible (%v); recreating from the old container config", ctr.Name, fb.err)
			if _, err := u.client.RecreateContainer(ctx, ctr.ID, r.Timeout, true); err != nil {
				return fmt.Errorf("recreate %s after failed compose up: %w", ctr.Name, err)
			}
		}
	default:
		return fmt.Errorf("unknown roll mode %q", r.Mode)
	}
	log.Infof("roll: %s rolled", ctr.Name)
	if r.CleanupImage != "" {
		if err := u.client.RemoveImage(ctx, r.CleanupImage); err != nil {
			log.Debugf("roll: remove old image %s: %v", truncateID(r.CleanupImage), err)
		}
	}
	return nil
}

// execCompose runs docker-compose in this process's container (the roll helper
// ships it). Like a compose helper, stdout is dropped: it may hold env values.
func execCompose(ctx context.Context, p docker.ComposeProject, _ string, timeout time.Duration, args ...string) (docker.HelperResult, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, docker.ComposeBinary, append(append([]string{"--ansi", "never"}, p.GlobalArgs()...), args...)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()
	res := docker.HelperResult{Stderr: strings.TrimSpace(stderr.String())}
	var exitErr *exec.ExitError
	switch {
	case errors.As(err, &exitErr):
		res.ExitCode = int64(exitErr.ExitCode())
	case err != nil:
		return res, fmt.Errorf("run docker-compose %s: %w", args[0], err)
	}
	if res.ExitCode != 0 {
		return res, fmt.Errorf("docker-compose %s exited %d: %s", args[0], res.ExitCode, lastLines(res.Stderr, 5))
	}
	return res, nil
}
