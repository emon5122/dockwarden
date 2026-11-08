package updater

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/emon5122/dockwarden/internal/config"
	"github.com/emon5122/dockwarden/internal/docker"
)

// stubClient models just enough of Docker for the update cycle: local tags
// (ref -> image id), the registry (ref -> image id a pull fetches) and the
// containers (ref they were created from + the image id they run). The
// embedded interface makes any un-stubbed call panic, which is the desired
// failure mode in these tests.
type stubClient struct {
	docker.Client

	mu         sync.Mutex
	containers []stubContainer
	local      map[string]string // ref -> image id the local tag points to
	registry   map[string]string // ref -> image id a pull fetches
	pulled     []string
	recreated  []string
	startFlags []bool
	selfID     string
}

type stubContainer struct {
	id, name string
	ref      string // .Config.Image
	imageID  string // .Image
	labels   map[string]string
}

// ListContainers mimics `docker ps`: once the container's tag points to a
// different image, Docker reports the bare image id instead of the ref.
func (s *stubClient) ListContainers(ctx context.Context, opts docker.ListOptions) ([]docker.Container, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]docker.Container, 0, len(s.containers))
	for _, c := range s.containers {
		image := c.ref
		if s.local[c.ref] != c.imageID {
			image = c.imageID
		}
		out = append(out, docker.Container{ID: c.id, Name: c.name, Image: image, ImageID: c.imageID, State: "running", Labels: c.labels})
	}
	return out, nil
}

func (s *stubClient) GetSelfContainerID() string { return s.selfID }

func (s *stubClient) GetContainer(ctx context.Context, id string) (docker.Container, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.containers {
		if c.id == id {
			return docker.Container{ID: c.id, Name: c.name, Image: c.ref, ImageID: c.imageID, State: "running"}, nil
		}
	}
	return docker.Container{}, fmt.Errorf("no such container %s", id)
}

func (s *stubClient) PullImage(ctx context.Context, imageName string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pulled = append(s.pulled, imageName)
	if strings.HasPrefix(imageName, "sha256:") {
		return fmt.Errorf("failed to pull image %s: pull access denied for sha256", imageName)
	}
	if id, ok := s.registry[imageName]; ok {
		s.local[imageName] = id
	}
	return nil
}

func (s *stubClient) GetImageID(ctx context.Context, imageName string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id, ok := s.local[imageName]
	if !ok {
		return "", fmt.Errorf("no such image %s", imageName)
	}
	return id, nil
}

// RecreateContainer recreates from .Config.Image, so the new container runs
// whatever the local tag points to now.
func (s *stubClient) RecreateContainer(ctx context.Context, id string, timeout time.Duration, startStopped bool) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recreated = append(s.recreated, id)
	s.startFlags = append(s.startFlags, startStopped)
	for i, c := range s.containers {
		if c.id == id {
			s.containers[i].imageID = s.local[c.ref]
		}
	}
	return id, nil
}

// newStub returns a client whose containers run sha256:old (unless set) from
// their own ref, with the registry holding sha256:new for each ref.
func newStub(containers ...stubContainer) *stubClient {
	s := &stubClient{local: map[string]string{}, registry: map[string]string{}}
	for _, c := range containers {
		if c.imageID == "" {
			c.imageID = "sha256:old"
		}
		if _, ok := s.local[c.ref]; !ok {
			s.local[c.ref] = c.imageID
		}
		s.registry[c.ref] = "sha256:new"
		s.containers = append(s.containers, c)
	}
	return s
}

func (s *stubClient) imageOf(id string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.containers {
		if c.id == id {
			return c.imageID
		}
	}
	return ""
}

func runningContainer(id, name string) stubContainer {
	return stubContainer{id: id, name: name, ref: "example/" + name + ":latest"}
}

// Regression: --no-restart was accepted and silently ignored, so containers
// were recreated anyway. It must pull the image but leave the container alone.
func TestRunNoRestartPullsButDoesNotRecreate(t *testing.T) {
	client := newStub(runningContainer("c1", "web"))
	u := New(client, &config.Config{NoRestart: true})

	if err := u.Run(); err != nil {
		t.Fatalf("Run() = %v", err)
	}

	if len(client.pulled) == 0 {
		t.Fatal("no-restart mode must still pull images")
	}
	if len(client.recreated) != 0 {
		t.Fatalf("no-restart mode recreated containers: %v", client.recreated)
	}
}

func TestRunUpdatesAndPassesReviveStopped(t *testing.T) {
	client := newStub(runningContainer("c1", "web"))
	u := New(client, &config.Config{ReviveStopped: true})

	if err := u.Run(); err != nil {
		t.Fatalf("Run() = %v", err)
	}

	if len(client.recreated) != 1 || client.recreated[0] != "c1" {
		t.Fatalf("recreated = %v, want [c1]", client.recreated)
	}
	if len(client.startFlags) != 1 || !client.startFlags[0] {
		t.Fatalf("startStopped flags = %v, want [true]", client.startFlags)
	}
}

// Regression (1.2.3, home-pbx camgw/backend): two containers on one repo:tag,
// the tag moves to a new image, one pass -> both are recreated, and the ref is
// pulled once. Before, each container compared the tag before and after its
// own pull; the second one's "before" was read after the first container's
// pull had already moved the tag, so it was skipped.
func TestRunUpdatesAllContainersSharingATag(t *testing.T) {
	client := newStub(
		stubContainer{id: "c1", name: "camgw-door", ref: "ghcr.io/x/camgw:latest"},
		stubContainer{id: "c2", name: "camgw", ref: "ghcr.io/x/camgw:latest"},
	)
	u := New(client, &config.Config{})

	if err := u.Run(); err != nil {
		t.Fatalf("Run() = %v", err)
	}

	got := append([]string(nil), client.recreated...)
	sort.Strings(got)
	if len(got) != 2 || got[0] != "c1" || got[1] != "c2" {
		t.Fatalf("recreated = %v, want [c1 c2]", got)
	}
	for _, id := range []string{"c1", "c2"} {
		if img := client.imageOf(id); img != "sha256:new" {
			t.Fatalf("%s runs %s, want sha256:new", id, img)
		}
	}
	if len(client.pulled) != 1 || client.pulled[0] != "ghcr.io/x/camgw:latest" {
		t.Fatalf("pulled = %v, want one pull of ghcr.io/x/camgw:latest", client.pulled)
	}
	if n := u.totalFailed.Load(); n != 0 {
		t.Fatalf("failed = %d, want 0", n)
	}
}

// Regression (1.2.3): the tag had already moved before the pass (an earlier
// pass updated a sibling), so `docker ps` lists the container's image as a bare
// sha256 id. DockWarden pulled "sha256:..." and failed every pass with "pull
// access denied for sha256". It must pull the .Config.Image ref instead and
// roll the container to the image the tag points to.
func TestRunRollsContainerWhoseListImageIsAnID(t *testing.T) {
	client := newStub(stubContainer{id: "c1", name: "backend", ref: "ghcr.io/x/backend:latest"})
	// The tag already points to the new image locally; the container lags.
	client.local["ghcr.io/x/backend:latest"] = "sha256:new"

	listed, _ := client.ListContainers(context.Background(), docker.ListOptions{})
	if listed[0].Image != "sha256:old" {
		t.Fatalf("stub precondition: list Image = %q, want the bare id", listed[0].Image)
	}

	u := New(client, &config.Config{})
	if err := u.Run(); err != nil {
		t.Fatalf("Run() = %v", err)
	}

	for _, ref := range client.pulled {
		if strings.HasPrefix(ref, "sha256:") {
			t.Fatalf("pulled a bare image id: %v", client.pulled)
		}
	}
	if n := u.totalFailed.Load(); n != 0 {
		t.Fatalf("failed = %d, want 0", n)
	}
	if len(client.recreated) != 1 || client.recreated[0] != "c1" {
		t.Fatalf("recreated = %v, want [c1]", client.recreated)
	}
	if img := client.imageOf("c1"); img != "sha256:new" {
		t.Fatalf("c1 runs %s, want sha256:new", img)
	}
}

// Up-to-date containers are left alone, and a container created from a bare
// image id (nothing to follow) is skipped without an error or a pull.
func TestRunLeavesUpToDateAndIDCreatedContainers(t *testing.T) {
	const id64 = "9ebb91f2aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	client := newStub(
		stubContainer{id: "c1", name: "web", ref: "example/web:latest"},
		stubContainer{id: "c2", name: "byid", ref: "sha256:" + id64, imageID: "sha256:" + id64},
		stubContainer{id: "c3", name: "byhex", ref: id64, imageID: "sha256:" + id64},
	)
	client.registry["example/web:latest"] = "sha256:old" // nothing new upstream
	u := New(client, &config.Config{})

	if err := u.Run(); err != nil {
		t.Fatalf("Run() = %v", err)
	}
	if len(client.recreated) != 0 {
		t.Fatalf("recreated = %v, want none", client.recreated)
	}
	if n := u.totalFailed.Load(); n != 0 {
		t.Fatalf("failed = %d, want 0", n)
	}
	if len(client.pulled) != 1 || client.pulled[0] != "example/web:latest" {
		t.Fatalf("pulled = %v, want only example/web:latest", client.pulled)
	}
}

func TestIsImageID(t *testing.T) {
	const id64 = "9ebb91f2aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	cases := map[string]bool{
		"sha256:" + id64:                         true,
		id64:                                     true,
		"9ebb91f2":                               false, // could be a repo name
		"ghcr.io/nexisltd/home-pbx/camgw:latest": false,
		"nginx":                                  false,
		"nginx@sha256:" + id64:                   false, // digest-pinned ref, handled separately
	}
	for ref, want := range cases {
		if got := isImageID(ref); got != want {
			t.Errorf("isImageID(%q) = %v, want %v", ref, got, want)
		}
	}
}

// Regression: --include-restarting was accepted and silently ignored. The
// state split is: restarting needs include-restarting, stopped needs
// include-stopped — one does not imply the other.
func TestFilterContainersStateFlags(t *testing.T) {
	containers := []docker.Container{
		{ID: "run", Name: "run", State: "running"},
		{ID: "restarting", Name: "flapper", State: "restarting"},
		{ID: "stopped", Name: "stopped", State: "exited"},
	}

	cases := []struct {
		name string
		cfg  config.Config
		want []string
	}{
		{"default only running", config.Config{}, []string{"run"}},
		{"include-restarting", config.Config{IncludeRestarting: true}, []string{"run", "restarting"}},
		{"include-stopped", config.Config{IncludeStopped: true}, []string{"run", "stopped"}},
		{"both", config.Config{IncludeRestarting: true, IncludeStopped: true}, []string{"run", "restarting", "stopped"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			u := New(nil, &tc.cfg)
			var got []string
			for _, ctr := range u.filterContainers(containers) {
				got = append(got, ctr.ID)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("filtered = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("filtered = %v, want %v", got, tc.want)
				}
			}
		})
	}
}

// A container with dockwarden.schedule is only updated inside its window; an
// unlabelled one is unaffected; an invalid spec fails closed.
func TestScheduleWindowLabel(t *testing.T) {
	const win = "CRON_TZ=Asia/Dhaka * * 2-5 * * *"
	lbl := func(v string) map[string]string { return map[string]string{"dockwarden.schedule": v} }
	run := func(now time.Time) *stubClient {
		client := newStub(
			stubContainer{id: "plain", name: "plain", ref: "img/plain:latest"},
			stubContainer{id: "win", name: "win", ref: "img/win:latest", labels: lbl(win)},
			stubContainer{id: "bad", name: "bad", ref: "img/bad:latest", labels: lbl("garbage")},
		)
		u := New(client, &config.Config{})
		u.now = func() time.Time { return now }
		if err := u.Run(); err != nil {
			t.Fatal(err)
		}
		return client
	}
	dhaka := time.FixedZone("BDT", 6*3600)

	out := run(time.Date(2026, 11, 8, 12, 0, 0, 0, dhaka))
	if out.imageOf("plain") != "sha256:new" {
		t.Errorf("unlabelled container not updated: %s", out.imageOf("plain"))
	}
	if out.imageOf("win") != "sha256:old" {
		t.Errorf("labelled container updated outside its window")
	}
	if out.imageOf("bad") != "sha256:old" {
		t.Errorf("invalid spec did not fail closed")
	}

	in := run(time.Date(2026, 11, 8, 3, 0, 0, 0, dhaka))
	if in.imageOf("win") != "sha256:new" {
		t.Errorf("labelled container not updated inside its window")
	}
	if in.imageOf("bad") != "sha256:old" {
		t.Errorf("invalid spec did not fail closed inside window")
	}
}

// dockwarden.schedule also gates dockwarden's own self-update.
func TestScheduleWindowSelfUpdate(t *testing.T) {
	const win = "CRON_TZ=Asia/Dhaka * * 2-5 * * *"
	dhaka := time.FixedZone("BDT", 6*3600)
	run := func(now time.Time, spec string) *stubClient {
		client := newStub(stubContainer{id: "self", name: "dockwarden", ref: "img/dockwarden:latest",
			labels: map[string]string{"dockwarden.schedule": spec}})
		u := New(client, &config.Config{NoRestart: true})
		u.now = func() time.Time { return now }
		if err := u.Run(); err != nil {
			t.Fatal(err)
		}
		return client
	}
	pulled := func(c *stubClient) bool { return len(c.pulled) > 0 }

	if pulled(run(time.Date(2026, 11, 8, 12, 0, 0, 0, dhaka), win)) {
		t.Error("self-update ran outside its window")
	}
	if !pulled(run(time.Date(2026, 11, 8, 3, 0, 0, 0, dhaka), win)) {
		t.Error("self-update did not run inside its window")
	}
	if pulled(run(time.Date(2026, 11, 8, 3, 0, 0, 0, dhaka), "garbage")) {
		t.Error("invalid spec did not fail closed for self-update")
	}
}

// Regression (VPS 2026-11-08): a wrong, non-empty self ID matched no container,
// so dockwarden updated itself as an ordinary container (stop -> SIGTERM, no
// replacement ever started). It must be recognised by name instead and never
// be recreated through the generic path.
func TestWrongSelfIDNeverRecreatesSelf(t *testing.T) {
	client := newStub(
		stubContainer{id: "aaaaaaaaaaaa1111", name: "dockwarden", ref: "img/dockwarden:latest"},
		stubContainer{id: "bbbbbbbbbbbb2222", name: "web", ref: "img/web:latest"},
	)
	client.selfID = "zzzzzzzzzzzz" // does not match any container
	u := New(client, &config.Config{MonitorOnly: false})
	func() {
		defer func() { _ = recover() }() // self path hits un-stubbed helper calls; fine
		_ = u.Run()
	}()
	for _, id := range client.recreated {
		if id == "aaaaaaaaaaaa1111" {
			t.Fatalf("dockwarden was recreated via the generic path: %v", client.recreated)
		}
	}
	if len(client.recreated) != 1 || client.recreated[0] != "bbbbbbbbbbbb2222" {
		t.Errorf("expected only web recreated, got %v", client.recreated)
	}
}

func TestSplitSelf(t *testing.T) {
	cs := []docker.Container{
		{ID: "abc123def456aaaa", Name: "dockwarden"},
		{ID: "ffff", Name: "dockwarden-old"},
		{ID: "eeee", Name: "web", Image: "nginx"},
	}
	self, others := splitSelf(cs, "abc123def456")
	if self == nil || self.Name != "dockwarden" || len(others) != 2 {
		t.Fatalf("id match: %v %v", self, others)
	}
	self, others = splitSelf(cs, "wrongwrongwr")
	if self == nil || self.Name != "dockwarden" || len(others) != 2 {
		t.Fatalf("name fallback: %v %v", self, others)
	}
}

// The self-update take-over helper shares the image and a "dockwarden" name; it must never be
// picked as self by the fallback (2026-11-08, "failed to inspect self" with the helper's ID).
func TestSplitSelfSkipsTakeoverHelper(t *testing.T) {
	ctrs := []docker.Container{
		{ID: "ae6a4aa3b145ffff", Name: "dockwarden-takeover-1762603987", Image: "emon5122/dockwarden:latest"},
		{ID: "aeeceaf518c7ffff", Name: "dockwarden", Image: "emon5122/dockwarden:latest"},
	}
	self, others := splitSelf(ctrs, "8aac7488fd2c")
	if self == nil || self.Name != "dockwarden" {
		t.Fatalf("helper picked as self: %+v", self)
	}
	if len(others) != 1 || others[0].Name != "dockwarden-takeover-1762603987" {
		t.Fatalf("others: %+v", others)
	}
}
