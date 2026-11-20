package updater

import (
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/emon5122/dockwarden/internal/config"
	"github.com/emon5122/dockwarden/internal/docker"
)

// detachStub records detached roll helpers (StartHelper) on top of composeStub.
type detachStub struct {
	*composeStub
	started  []docker.HelperSpec
	startErr error
	images   []string             // RemoveImage calls
	created  map[string]time.Time // Created of listed containers
}

func (d *detachStub) ListContainers(ctx context.Context, opts docker.ListOptions) ([]docker.Container, error) {
	out, err := d.composeStub.ListContainers(ctx, opts)
	for i := range out {
		out[i].Created = d.created[out[i].ID]
	}
	return out, err
}

func (d *detachStub) StartHelper(ctx context.Context, spec docker.HelperSpec) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.startErr != nil {
		return "", d.startErr
	}
	d.started = append(d.started, spec)
	return "h-" + spec.Name, nil
}

func (d *detachStub) ContainerLogs(ctx context.Context, id string, follow bool, tail string) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("roll: compose up failed\n")), nil
}

func (d *detachStub) RemoveImage(ctx context.Context, id string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.images = append(d.images, id)
	return nil
}

func newDetachStub(containers ...stubContainer) *detachStub {
	c := newComposeStub(containers...)
	// titli-agent: dockwarden.stop-timeout 14400 (4 h) in the compose file
	c.render = `{"services":{"bridge":{"image":"ghcr.io/nexisltd/wa-gateway:latest",` +
		`"labels":{"dockwarden.enable":"true","dockwarden.stop-timeout":"14400","dockwarden.stop-signal":"SIGUSR1"}}}}`
	return &detachStub{composeStub: c}
}

func detachConfig() *config.Config {
	cfg := composeConfig()
	cfg.DetachStopTimeout = 10 * time.Minute
	cfg.RollingRestart = true // one at a time: the long drain used to block every other container
	return cfg
}

func (d *detachStub) ups() int {
	n := 0
	for _, h := range d.helpers {
		if slices.Contains(h.Cmd, "up") {
			n++
		}
	}
	return n
}

func rollHelper(id, key, target, state string, exit int) stubContainer {
	return stubContainer{id: id, name: "dockwarden-roll-" + target + "-1", ref: "sha256:dw", imageID: "sha256:dw", state: state, exitCode: exit,
		labels: map[string]string{docker.HelperLabel: rollHelperKind, RollTargetLabel: key, RollContainerLabel: target}}
}

// The 2026-11-13 case: titli-agent drains for up to 4 h. Its roll goes to a
// helper and the pass rolls the other container and returns.
func TestLongDrainComposeRollIsDetached(t *testing.T) {
	d := newDetachStub(bridgeContainer("b1"), runningContainer("p1", "web"))
	u := New(d, detachConfig())
	if err := u.Run(); err != nil {
		t.Fatal(err)
	}
	if len(d.stops) != 0 || d.ups() != 0 {
		t.Fatalf("the pass must not drain or run up itself: stops=%+v ups=%d", d.stops, d.ups())
	}
	if !slices.Equal(d.recreated, []string{"p1"}) {
		t.Fatalf("the other container must roll in the same pass: recreated=%v", d.recreated)
	}
	if len(d.started) != 1 {
		t.Fatalf("want one roll helper, got %d", len(d.started))
	}
	h := d.started[0]
	if h.Image != "sha256:dw" || h.Entrypoint[0] != "/dockwarden" || h.Kind != rollHelperKind {
		t.Fatalf("roll helper must run DockWarden's image with `dockwarden roll`: %+v", h)
	}
	if !strings.HasPrefix(h.Name, "dockwarden-compose-up-wa-call-bridge-bridge-") {
		t.Fatalf("helper name %q", h.Name)
	}
	if cmd := strings.Join(h.Cmd, " "); !strings.Contains(cmd, "roll --container b1 --mode compose --timeout 4h0m0s --signal SIGUSR1") {
		t.Errorf("roll command %q", cmd)
	}
	if h.Labels[RollTargetLabel] != "compose:wa-call-bridge/bridge" || h.Labels[RollContainerLabel] != "wa-call-bridge-bridge-b1" {
		t.Fatalf("labels %v", h.Labels)
	}
	if !slices.Contains(h.Binds, "/srv/wa-call-bridge:/srv/wa-call-bridge:ro") || h.Binds[0] != "/run/docker.sock:/var/run/docker.sock" {
		t.Fatalf("binds %v", h.Binds)
	}
	if got := d.imageOf("b1"); got != "sha256:old" {
		t.Fatalf("b1 must still run the old image while it drains, runs %s", got)
	}
	if s := u.GetStats(); s["total_updated"] != int64(1) || s["total_failed"] != int64(0) {
		t.Fatalf("stats %v (the detached roll is counted when its helper finishes)", s)
	}
}

func TestLongDrainReplicasShareOneHelper(t *testing.T) {
	d := newDetachStub(bridgeContainer("b1"), bridgeContainer("b2"))
	if err := New(d, detachConfig()).Run(); err != nil {
		t.Fatal(err)
	}
	if len(d.started) != 1 || len(d.recreated) != 0 || len(d.stops) != 0 {
		t.Fatalf("replicas: helpers=%d recreated=%v stops=%v", len(d.started), d.recreated, d.stops)
	}
}

// A short stop timeout keeps the roll inside the pass (unchanged behaviour).
func TestShortDrainStaysInPass(t *testing.T) {
	d := newDetachStub(bridgeContainer("b1"))
	d.render = renderBridge("") // 700 s
	cfg := detachConfig()
	cfg.DetachStopTimeout = 15 * time.Minute
	if err := New(d, cfg).Run(); err != nil {
		t.Fatal(err)
	}
	if len(d.started) != 0 || d.ups() != 1 || len(d.stops) != 1 {
		t.Fatalf("short drain: helpers=%d ups=%d stops=%d", len(d.started), d.ups(), len(d.stops))
	}
}

// The next pass skips a container whose roll is still draining: no registry
// call, no second helper. Other containers still roll.
func TestRollInFlightSkipsTarget(t *testing.T) {
	running := rollHelper("h1", "compose:wa-call-bridge/bridge", "wa-call-bridge-bridge-b1", "", 0)
	d := newDetachStub(bridgeContainer("b1"), bridgeContainer("b2"), running, runningContainer("p1", "web"))
	if err := New(d, detachConfig()).Run(); err != nil {
		t.Fatal(err)
	}
	if len(d.started) != 0 || len(d.stops) != 0 || d.ups() != 0 {
		t.Fatalf("in-flight roll must be left alone: helpers=%d stops=%v ups=%d", len(d.started), d.stops, d.ups())
	}
	if slices.Contains(d.pulled, bridgeRef) {
		t.Fatalf("skipped container must not hit the registry: pulled=%v", d.pulled)
	}
	if !slices.Equal(d.recreated, []string{"p1"}) || slices.Contains(d.removed, "h1") {
		t.Fatalf("recreated=%v removed=%v", d.recreated, d.removed)
	}
}

// A finished helper is reported (exit code), counted and removed; then its
// target may roll again.
func TestFinishedRollHelpersAreCollected(t *testing.T) {
	d := newDetachStub(rollHelper("h-ok", "container:a", "a", "exited", 0), rollHelper("h-bad", "container:b", "b", "exited", 1))
	u := New(d, detachConfig())
	if err := u.Run(); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(d.removed, "h-ok") || !slices.Contains(d.removed, "h-bad") {
		t.Fatalf("finished helpers must be removed: %v", d.removed)
	}
	if s := u.GetStats(); s["total_updated"] != int64(1) || s["total_failed"] != int64(1) {
		t.Fatalf("stats %v", s)
	}
	if _, inFlight := u.rollInFlight(docker.Container{Name: "a"}); inFlight {
		t.Fatal("a finished roll must not keep its target in flight")
	}
}

// The legacy recreate (no compose labels, or a compose fallback) is detached
// too: it would stop with the same long timeout inside the pass.
func TestLongDrainLegacyRecreateIsDetached(t *testing.T) {
	plain := runningContainer("p1", "agent")
	plain.labels = map[string]string{"dockwarden.stop-timeout": "14400"}
	d := newDetachStub(plain)
	if err := New(d, detachConfig()).Run(); err != nil {
		t.Fatal(err)
	}
	if len(d.recreated) != 0 || len(d.started) != 1 {
		t.Fatalf("recreated=%v helpers=%d", d.recreated, len(d.started))
	}
	h := d.started[0]
	if cmd := strings.Join(h.Cmd, " "); !strings.Contains(cmd, "--mode recreate --timeout 4h0m0s") || h.Labels[RollTargetLabel] != "container:agent" {
		t.Fatalf("cmd %q labels %v", cmd, h.Labels)
	}
	if len(h.Binds) != 1 {
		t.Fatalf("a recreate helper needs only the socket: %v", h.Binds)
	}
}

func TestLongDrainComposeFallbackIsDetached(t *testing.T) {
	d := newDetachStub(bridgeContainer("b1"))
	d.stderr = `WARN The "TOKEN" variable is not set. Defaulting to a blank string.`
	d.containers[1].labels["dockwarden.stop-timeout"] = "14400"
	if err := New(d, detachConfig()).Run(); err != nil {
		t.Fatal(err)
	}
	if len(d.recreated) != 0 || len(d.started) != 1 || !slices.Contains(d.started[0].Cmd, RollModeRecreate) {
		t.Fatalf("recreated=%v helpers=%+v", d.recreated, d.started)
	}
}

// Without a helper image (no self container) the roll stays in the pass, as before.
func TestLongDrainWithoutHelperImageRollsInPass(t *testing.T) {
	plain := runningContainer("p1", "agent")
	plain.labels = map[string]string{"dockwarden.stop-timeout": "14400"}
	d := &detachStub{composeStub: &composeStub{stubClient: newStub(plain)}}
	if err := New(d, detachConfig()).Run(); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(d.recreated, []string{"p1"}) || len(d.started) != 0 {
		t.Fatalf("recreated=%v helpers=%d", d.recreated, len(d.started))
	}
}

func TestRollHelperStartFailureIsAnError(t *testing.T) {
	d := newDetachStub(bridgeContainer("b1"))
	d.startErr = errors.New("no space")
	u := New(d, detachConfig())
	if err := u.Run(); err != nil {
		t.Fatal(err)
	}
	if len(d.stops) != 0 || len(d.recreated) != 0 {
		t.Fatalf("a failed helper start must leave the container alone: stops=%v recreated=%v", d.stops, d.recreated)
	}
	if u.GetStats()["total_failed"] != int64(1) {
		t.Fatalf("stats %v", u.GetStats())
	}
}

// Inside the helper: drain with the signal and timeout, then compose up.
func TestRunDetachedRollCompose(t *testing.T) {
	d := newDetachStub(bridgeContainer("b1"))
	u := New(d, &config.Config{})
	var upArgs []string
	u.composeExec = func(ctx context.Context, p docker.ComposeProject, step string, timeout time.Duration, args ...string) (docker.HelperResult, error) {
		upArgs = args
		return docker.HelperResult{}, nil
	}
	err := u.runDetachedRoll(context.Background(), DetachedRoll{ContainerID: "b1", Mode: RollModeCompose,
		Timeout: 4 * time.Hour, Signal: "SIGUSR1", CleanupImage: "sha256:old"})
	if err != nil {
		t.Fatal(err)
	}
	if len(d.stops) != 1 || d.stops[0] != (stopCall{"b1", 4 * time.Hour, "SIGUSR1"}) {
		t.Fatalf("stops %+v", d.stops)
	}
	if got := strings.Join(upArgs, " "); got != "up --detach --no-deps --force-recreate --pull never --no-build --timeout 14400 bridge" {
		t.Fatalf("up args %q", got)
	}
	if !slices.Equal(d.images, []string{"sha256:old"}) || len(d.recreated) != 0 {
		t.Fatalf("images=%v recreated=%v", d.images, d.recreated)
	}
}

func TestRunDetachedRollComposeFailureRecreates(t *testing.T) {
	d := newDetachStub(bridgeContainer("b1"))
	u := New(d, &config.Config{})
	u.composeExec = func(ctx context.Context, p docker.ComposeProject, step string, timeout time.Duration, args ...string) (docker.HelperResult, error) {
		return docker.HelperResult{ExitCode: 1, Stderr: "boom"}, errors.New("docker-compose up exited 1: boom")
	}
	if err := u.runDetachedRoll(context.Background(), DetachedRoll{ContainerID: "b1", Mode: RollModeCompose, Timeout: time.Hour}); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(d.recreated, []string{"b1"}) || !slices.Equal(d.startFlags, []bool{true}) {
		t.Fatalf("failed up must fall back to the legacy recreate and start it: recreated=%v flags=%v", d.recreated, d.startFlags)
	}
}

func TestRunDetachedRollStopFailureStops(t *testing.T) {
	d := newDetachStub(bridgeContainer("b1"))
	d.stopErr = errors.New("daemon gone")
	u := New(d, &config.Config{})
	u.composeExec = func(ctx context.Context, p docker.ComposeProject, step string, timeout time.Duration, args ...string) (docker.HelperResult, error) {
		t.Error("up must not run after a failed stop")
		return docker.HelperResult{}, nil
	}
	if err := u.runDetachedRoll(context.Background(), DetachedRoll{ContainerID: "b1", Mode: RollModeCompose, Timeout: time.Hour}); err == nil {
		t.Fatal("want an error")
	}
}

func TestRunDetachedRollRecreate(t *testing.T) {
	d := newDetachStub(runningContainer("p1", "agent"))
	u := New(d, &config.Config{})
	if err := u.runDetachedRoll(context.Background(), DetachedRoll{ContainerID: "p1", Mode: RollModeRecreate, Timeout: time.Hour, StartStopped: true}); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(d.recreated, []string{"p1"}) || !slices.Equal(d.startFlags, []bool{true}) {
		t.Fatalf("recreated=%v flags=%v", d.recreated, d.startFlags)
	}
}

// listFailDetach fails only the roll-helper listing.
type listFailDetach struct{ *detachStub }

func (l *listFailDetach) ListContainers(ctx context.Context, opts docker.ListOptions) ([]docker.Container, error) {
	if opts.LabelFilter == RollTargetLabel {
		return nil, errors.New("daemon busy")
	}
	return l.detachStub.ListContainers(ctx, opts)
}

// If the helpers cannot be listed, one may be draining the container right
// now: a long-drain container is not rolled at all (never inside the pass),
// short ones still are.
func TestRollHelperListErrorHoldsLongDrains(t *testing.T) {
	plain := runningContainer("p1", "agent")
	plain.labels = map[string]string{"dockwarden.stop-timeout": "14400"}
	d := newDetachStub(bridgeContainer("b1"), plain, runningContainer("p2", "web"))
	u := New(&listFailDetach{d}, detachConfig())
	if err := u.Run(); err != nil {
		t.Fatal(err)
	}
	if len(d.started) != 0 || len(d.stops) != 0 || d.ups() != 0 {
		t.Fatalf("long drains must be held: helpers=%d stops=%v ups=%d", len(d.started), d.stops, d.ups())
	}
	if !slices.Equal(d.recreated, []string{"p2"}) {
		t.Fatalf("recreated=%v", d.recreated)
	}
}

// A helper stuck in "created" (DockWarden died between create and start) is
// removed after a grace period instead of blocking its target forever.
func TestStaleCreatedRollHelperIsRemoved(t *testing.T) {
	d := newDetachStub(rollHelper("h-new", "container:a", "a", "created", 0), rollHelper("h-old", "container:b", "b", "created", 0))
	clock := time.Date(2026, 11, 20, 21, 0, 0, 0, time.UTC)
	d.created = map[string]time.Time{"h-new": clock.Add(-30 * time.Second), "h-old": clock.Add(-10 * time.Minute)}
	u := New(d, detachConfig())
	u.now = func() time.Time { return clock }
	u.collectRolls(context.Background())
	if _, ok := u.rollInFlight(docker.Container{Name: "a"}); !ok {
		t.Fatal("a just-created helper is in flight")
	}
	if _, ok := u.rollInFlight(docker.Container{Name: "b"}); ok || !slices.Contains(d.removed, "h-old") || slices.Contains(d.removed, "h-new") {
		t.Fatalf("stale created helper must be removed: removed=%v", d.removed)
	}
}
