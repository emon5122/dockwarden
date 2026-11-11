package updater

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/emon5122/dockwarden/internal/config"
	"github.com/emon5122/dockwarden/internal/docker"
)

// composeStub adds the calls a compose roll makes to stubClient.
type composeStub struct {
	*stubClient
	render  string // stdout of `config --format json`
	stderr  string // stderr of `config`
	upExit  int64
	upGone  bool // a failed up already removed the old container
	noSock  bool // DockWarden reaches the daemon through a socket proxy
	helpers []docker.HelperSpec
	stops   []stopCall
	starts  []string
}

type stopCall struct {
	id      string
	timeout time.Duration
	signal  string
}

func (c *composeStub) GetContainerBinds(ctx context.Context, id string) ([]string, error) {
	if c.noSock {
		return []string{"/srv/dockwarden/data:/data"}, nil
	}
	return []string{"/run/docker.sock:/var/run/docker.sock"}, nil
}

func (c *composeStub) StopContainer(ctx context.Context, id string, timeout time.Duration, signal string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.stops = append(c.stops, stopCall{id, timeout, signal})
	return nil
}

func (c *composeStub) StartContainer(ctx context.Context, id string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.starts = append(c.starts, id)
	return nil
}

func (c *composeStub) RunHelper(ctx context.Context, spec docker.HelperSpec) (docker.HelperResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.helpers = append(c.helpers, spec)
	switch {
	case slices.Contains(spec.Cmd, "config"):
		return docker.HelperResult{Stdout: c.render, Stderr: c.stderr}, nil
	case slices.Contains(spec.Cmd, "up"):
		if c.upExit != 0 {
			if c.upGone {
				c.containers = slices.DeleteFunc(c.containers, func(s stubContainer) bool { return s.id == "b1" })
			}
			return docker.HelperResult{ExitCode: c.upExit, Stderr: "boom"}, nil
		}
		// compose recreated every replica of the service on the new image
		svc := spec.Cmd[len(spec.Cmd)-1]
		for i, s := range c.containers {
			if s.labels[docker.ComposeServiceLabel] == svc {
				c.containers[i].imageID = c.local[s.ref]
			}
		}
		return docker.HelperResult{}, nil
	}
	return docker.HelperResult{}, fmt.Errorf("unexpected helper %v", spec.Cmd)
}

const bridgeRef = "ghcr.io/nexisltd/wa-gateway:latest"

func bridgeContainer(id string) stubContainer {
	return stubContainer{id: id, name: "wa-call-bridge-bridge-" + id, ref: bridgeRef, labels: map[string]string{
		docker.ComposeProjectLabel:     "wa-call-bridge",
		docker.ComposeServiceLabel:     "bridge",
		docker.ComposeConfigFilesLabel: "/srv/wa-call-bridge/compose.yaml",
		docker.ComposeWorkingDirLabel:  "/srv/wa-call-bridge",
	}}
}

func selfContainer() stubContainer {
	return stubContainer{id: "self", name: "dockwarden", ref: "emon5122/dockwarden:latest", imageID: "sha256:dw",
		labels: map[string]string{"dockwarden.update.enable": "false"}}
}

func renderBridge(extra string) string {
	return `{"services":{"bridge":{"image":"ghcr.io/nexisltd/wa-gateway:latest",` +
		`"labels":{"dockwarden.enable":"true","dockwarden.stop-timeout":"700"}` + extra + `}}}`
}

func newComposeStub(containers ...stubContainer) *composeStub {
	s := newStub(append([]stubContainer{selfContainer()}, containers...)...)
	s.local["emon5122/dockwarden:latest"] = "sha256:dw"
	s.registry["emon5122/dockwarden:latest"] = "sha256:dw"
	s.selfID = "self"
	return &composeStub{stubClient: s, render: renderBridge("")}
}

func composeConfig() *config.Config {
	return &config.Config{ComposeRoll: true, StopTimeout: 10 * time.Second}
}

// The 2026-11-11 case: wa-gateway 0.7.1 added a 700 s stop label to the compose
// file, the roll kept 10 s. Now the roll goes through compose with the new value.
func TestComposeRollUsesComposeProject(t *testing.T) {
	c := newComposeStub(bridgeContainer("b1"))
	if err := New(c, composeConfig()).Run(); err != nil {
		t.Fatal(err)
	}
	if len(c.recreated) != 0 {
		t.Fatalf("legacy recreate ran: %v", c.recreated)
	}
	if len(c.helpers) != 2 {
		t.Fatalf("want config + up helpers, got %d", len(c.helpers))
	}
	render, up := c.helpers[0], c.helpers[1]
	if render.Image != "sha256:dw" || render.Entrypoint[0] != docker.ComposeBinary {
		t.Fatalf("helper must run DockWarden's own image with docker-compose: %+v", render)
	}
	wantBinds := []string{"/run/docker.sock:/var/run/docker.sock", "/srv/wa-call-bridge:/srv/wa-call-bridge:ro"}
	if !slices.Equal(render.Binds, wantBinds) {
		t.Fatalf("binds = %v, want %v", render.Binds, wantBinds)
	}
	cmd := strings.Join(up.Cmd, " ")
	for _, want := range []string{"--project-name wa-call-bridge", "--project-directory /srv/wa-call-bridge",
		"--file /srv/wa-call-bridge/compose.yaml", "up --detach --no-deps --force-recreate --pull never --no-build --timeout 700 bridge"} {
		if !strings.Contains(cmd, want) {
			t.Errorf("up command %q lacks %q", cmd, want)
		}
	}
	if len(c.stops) != 1 || c.stops[0].timeout != 700*time.Second {
		t.Fatalf("stop must use the NEW compose label's 700 s: %+v", c.stops)
	}
	if got := c.imageOf("b1"); got != "sha256:new" {
		t.Fatalf("container runs %s after the roll", got)
	}
}

func TestComposeRollStopTimeoutIsTheLongest(t *testing.T) {
	c := newComposeStub(bridgeContainer("b1"))
	c.render = renderBridge(`,"stop_grace_period":"15m","stop_signal":"SIGINT"`)
	if err := New(c, composeConfig()).Run(); err != nil {
		t.Fatal(err)
	}
	if len(c.stops) != 1 || c.stops[0].timeout != 15*time.Minute {
		t.Fatalf("stop_grace_period 15m is the longest: %+v", c.stops)
	}

	old := bridgeContainer("b2")
	old.labels["dockwarden.stop-timeout"] = "1h"
	old.labels["dockwarden.stop-signal"] = "SIGQUIT"
	c = newComposeStub(old)
	if err := New(c, composeConfig()).Run(); err != nil {
		t.Fatal(err)
	}
	if len(c.stops) != 1 || c.stops[0].timeout != time.Hour {
		t.Fatalf("the old container's longer timeout must win: %+v", c.stops)
	}
}

func TestComposeRollFallsBackBeforeStopping(t *testing.T) {
	for name, setup := range map[string]func(*composeStub){
		"image changed in compose file": func(c *composeStub) {
			c.render = strings.Replace(renderBridge(""), ":latest", ":0.8.0", 1)
		},
		"unset variable": func(c *composeStub) {
			c.stderr = `level=warning msg="The \"WCB_TAG\" variable is not set. Defaulting to a blank string."`
		},
		"unparseable render": func(c *composeStub) { c.render = "oops" },
	} {
		t.Run(name, func(t *testing.T) {
			c := newComposeStub(bridgeContainer("b1"))
			setup(c)
			if err := New(c, composeConfig()).Run(); err != nil {
				t.Fatal(err)
			}
			if len(c.stops) != 0 {
				t.Fatalf("nothing may be stopped before the render is trusted: %+v", c.stops)
			}
			if !slices.Equal(c.recreated, []string{"b1"}) || c.startFlags[0] {
				t.Fatalf("want one legacy recreate (no forced start), got %v %v", c.recreated, c.startFlags)
			}
		})
	}
}

func TestComposeRollUpFailureFallsBackAndStarts(t *testing.T) {
	c := newComposeStub(bridgeContainer("b1"))
	c.upExit = 1
	if err := New(c, composeConfig()).Run(); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(c.recreated, []string{"b1"}) || !c.startFlags[0] {
		t.Fatalf("we stopped it, so the legacy recreate must start it: %v %v", c.recreated, c.startFlags)
	}
}

func TestComposeRollUpFailureAfterRemovalStartsService(t *testing.T) {
	c := newComposeStub(bridgeContainer("b1"))
	c.upExit, c.upGone = 1, true
	// a stopped replacement compose left behind
	c.containers = append(c.containers, stubContainer{id: "b1-new", name: "wa-call-bridge-bridge-1", ref: bridgeRef,
		imageID: "sha256:new", labels: bridgeContainer("x").labels})
	listed := &stoppedLister{composeStub: c}
	u := New(listed, composeConfig())
	u.prepareCompose(context.Background(), &docker.Container{ID: "self", ImageID: "sha256:dw"})
	ctr := docker.Container{ID: "b1", Name: "b1", Labels: bridgeContainer("b1").labels}
	if err := u.updateContainer(context.Background(), ctr); err == nil {
		t.Fatal("a failed up after the old container is gone must be reported")
	}
	if len(c.recreated) != 0 || !slices.Equal(c.starts, []string{"b1-new"}) {
		t.Fatalf("recreated=%v starts=%v", c.recreated, c.starts)
	}
}

// stoppedLister reports every container except self as stopped (for the
// post-failure restart) once the compose up has failed.
type stoppedLister struct{ *composeStub }

func (s *stoppedLister) ListContainers(ctx context.Context, opts docker.ListOptions) ([]docker.Container, error) {
	out, err := s.composeStub.ListContainers(ctx, opts)
	for i := range out {
		if out[i].ID != "self" {
			out[i].State = "exited"
		}
	}
	return out, err
}

func TestComposeRollOffOrNotCompose(t *testing.T) {
	c := newComposeStub(bridgeContainer("b1"))
	cfg := composeConfig()
	cfg.ComposeRoll = false
	if err := New(c, cfg).Run(); err != nil {
		t.Fatal(err)
	}
	if len(c.helpers) != 0 || !slices.Equal(c.recreated, []string{"b1"}) {
		t.Fatalf("compose-roll=false: helpers=%d recreated=%v", len(c.helpers), c.recreated)
	}

	plain := runningContainer("p1", "web")
	c = newComposeStub(plain)
	if err := New(c, composeConfig()).Run(); err != nil {
		t.Fatal(err)
	}
	if len(c.helpers) != 0 || !slices.Equal(c.recreated, []string{"p1"}) {
		t.Fatalf("non-compose container: helpers=%d recreated=%v", len(c.helpers), c.recreated)
	}
}

func TestComposeRollWithoutSelfContainerIsLegacy(t *testing.T) {
	s := newStub(bridgeContainer("b1"))
	c := &composeStub{stubClient: s, render: renderBridge("")}
	if err := New(c, composeConfig()).Run(); err != nil {
		t.Fatal(err)
	}
	if len(c.helpers) != 0 || !slices.Equal(c.recreated, []string{"b1"}) {
		t.Fatalf("no self container: helpers=%d recreated=%v", len(c.helpers), c.recreated)
	}
}

func TestComposeRollBehindSocketProxyIsLegacy(t *testing.T) {
	c := newComposeStub(bridgeContainer("b1"))
	c.noSock = true
	if err := New(c, composeConfig()).Run(); err != nil {
		t.Fatal(err)
	}
	if len(c.helpers) != 0 || !slices.Equal(c.recreated, []string{"b1"}) {
		t.Fatalf("no docker.sock bind: helpers=%d recreated=%v", len(c.helpers), c.recreated)
	}
}

func TestComposeRollReplicasRollOnce(t *testing.T) {
	c := newComposeStub(bridgeContainer("b1"), bridgeContainer("b2"))
	if err := New(c, composeConfig()).Run(); err != nil {
		t.Fatal(err)
	}
	ups := 0
	for _, h := range c.helpers {
		if slices.Contains(h.Cmd, "up") {
			ups++
		}
	}
	if ups != 1 || len(c.recreated) != 0 {
		t.Fatalf("two replicas: ups=%d recreated=%v", ups, c.recreated)
	}
}

func TestHelpersAreNeverManagedOrSelf(t *testing.T) {
	helper := docker.Container{ID: "h1", Name: "dockwarden-compose-up-1", Image: "sha256:dw", State: "running",
		Labels: map[string]string{docker.HelperLabel: "compose"}}
	self := docker.Container{ID: "self", Name: "dockwarden", Image: "emon5122/dockwarden:latest", State: "running"}
	got, _ := splitSelf([]docker.Container{helper, self}, "")
	if got == nil || got.ID != "self" {
		t.Fatalf("self = %+v", got)
	}
	u := New(newStub(), &config.Config{})
	if f := u.filterContainers([]docker.Container{helper}); len(f) != 0 {
		t.Fatalf("helper must not be managed: %+v", f)
	}
}
