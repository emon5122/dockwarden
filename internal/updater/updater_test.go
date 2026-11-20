package updater

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/emon5122/dockwarden/internal/config"
	"github.com/emon5122/dockwarden/internal/docker"
	"github.com/emon5122/dockwarden/internal/registry"
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

	remoteErr   error            // returned by RemoteDigest / PullImage when set (registry outage)
	errFor      map[string]error // per-ref override of remoteErr
	remoteCalls int
	pullCalls   int
	pullErr     map[string]error // per-ref error for PullImage only (RemoteDigest succeeds)
	inflight    int              // pulls running now
	maxInflight int              // peak simultaneous pulls
	pullDelay   time.Duration
	digestDelay time.Duration // slept (unlocked) inside RemoteDigest
}

type stubContainer struct {
	id, name string
	ref      string // .Config.Image
	imageID  string // .Image
	labels   map[string]string
	state    string // "" = running
	exitCode int    // inspect State.ExitCode
}

func (c stubContainer) stateOr() string {
	if c.state == "" {
		return "running"
	}
	return c.state
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
		out = append(out, docker.Container{ID: c.id, Name: c.name, Image: image, ImageID: c.imageID, State: c.stateOr(), Labels: c.labels})
	}
	return out, nil
}

func (s *stubClient) GetSelfContainerID() string { return s.selfID }

func (s *stubClient) GetContainer(ctx context.Context, id string) (docker.Container, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.containers {
		if c.id == id {
			return docker.Container{ID: c.id, Name: c.name, Image: c.ref, ImageID: c.imageID, State: c.stateOr(), Labels: c.labels, ExitCode: c.exitCode}, nil
		}
	}
	return docker.Container{}, fmt.Errorf("no such container %s", id)
}

func (s *stubClient) errOf(ref string) error {
	if e, ok := s.errFor[ref]; ok {
		return e
	}
	return s.remoteErr
}

// RemoteDigest / LocalDigests model digests as "d:"+image id.
func (s *stubClient) RemoteDigest(ctx context.Context, imageName string) (string, error) {
	time.Sleep(s.digestDelay)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.remoteCalls++
	if err := s.errOf(imageName); err != nil {
		return "", err
	}
	return "d:" + s.registry[imageName], nil
}

func (s *stubClient) LocalDigests(ctx context.Context, imageName string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id, ok := s.local[imageName]; ok {
		return []string{"d:" + id}, nil
	}
	return nil, nil
}

func (s *stubClient) PullImage(ctx context.Context, imageName string) error {
	s.mu.Lock()
	s.pullCalls++
	s.inflight++
	if s.inflight > s.maxInflight {
		s.maxInflight = s.inflight
	}
	delay := s.pullDelay
	s.mu.Unlock()
	if delay > 0 {
		time.Sleep(delay)
	}
	s.mu.Lock()
	defer func() { s.inflight--; s.mu.Unlock() }()
	if err, ok := s.pullErr[imageName]; ok {
		return err
	}
	if err := s.errOf(imageName); err != nil {
		return err
	}
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
	// Unchanged digest: the registry is asked, but nothing is pulled.
	if len(client.pulled) != 0 || client.remoteCalls != 1 {
		t.Fatalf("pulled = %v, digest checks = %d; want no pull and 1 digest check", client.pulled, client.remoteCalls)
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

// Regression (R610 2026-11-10): DockWarden pulled dockwarden:latest every
// minute although the digest never changed. An unchanged remote digest must not
// pull, for ordinary containers and for the self-update path alike.
func TestUnchangedDigestSkipsPull(t *testing.T) {
	c := runningContainer("c1", "app")
	c.imageID = "sha256:new"
	client := newStub(c) // local tag == registry == sha256:new
	u := New(client, &config.Config{})
	if err := u.Run(); err != nil {
		t.Fatal(err)
	}
	if len(client.pulled) != 0 {
		t.Fatalf("pulled %v although the remote digest is unchanged", client.pulled)
	}
	if len(client.recreated) != 0 {
		t.Fatalf("recreated %v although nothing changed", client.recreated)
	}
	if client.remoteCalls != 1 {
		t.Fatalf("expected 1 digest check, got %d", client.remoteCalls)
	}
}

func TestChangedDigestStillPulls(t *testing.T) {
	client := newStub(runningContainer("c1", "app")) // local old, registry new
	u := New(client, &config.Config{})
	if err := u.Run(); err != nil {
		t.Fatal(err)
	}
	if len(client.pulled) != 1 || len(client.recreated) != 1 {
		t.Fatalf("pulled=%v recreated=%v, want one of each", client.pulled, client.recreated)
	}
}

func selfStub(imageID string) *stubClient {
	self := stubContainer{id: "self1", name: "dockwarden", ref: "emon5122/dockwarden:latest", imageID: imageID}
	client := newStub(self)
	client.selfID = "self1"
	return client
}

// Self-update checks are rate-limited: with a 1-minute main interval DockWarden
// asks the registry about itself once per SelfUpdateInterval, not every pass.
func TestSelfUpdateCheckIsRateLimited(t *testing.T) {
	client := selfStub("sha256:new") // up to date: no os.Exit path
	cfg := &config.Config{SelfUpdateInterval: 10 * time.Minute}
	u := New(client, cfg)
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	u.now = func() time.Time { return now }

	for i := 0; i < 5; i++ {
		if err := u.Run(); err != nil {
			t.Fatal(err)
		}
		now = now.Add(time.Minute)
	}
	if client.remoteCalls != 1 {
		t.Fatalf("5 passes in 5 minutes made %d registry checks, want 1", client.remoteCalls)
	}
	now = now.Add(10 * time.Minute)
	if err := u.Run(); err != nil {
		t.Fatal(err)
	}
	if client.remoteCalls != 2 {
		t.Fatalf("after the interval: %d registry checks, want 2", client.remoteCalls)
	}
	if len(client.pulled) != 0 {
		t.Fatalf("self pulled %v with an unchanged digest", client.pulled)
	}
}

// Pulls never run in parallel by default (a small box hangs), and the limit is configurable.
func TestPullConcurrency(t *testing.T) {
	build := func() *stubClient {
		var cs []stubContainer
		for _, n := range []string{"a", "b", "c", "d", "e"} {
			cs = append(cs, runningContainer(n, n)) // 5 distinct refs, all outdated
		}
		c := newStub(cs...)
		c.pullDelay = 20 * time.Millisecond
		return c
	}

	client := build()
	if err := New(client, &config.Config{}).Run(); err != nil {
		t.Fatal(err)
	}
	if len(client.pulled) != 5 {
		t.Fatalf("pulled %d images, want 5", len(client.pulled))
	}
	if client.maxInflight != 1 {
		t.Fatalf("default pull concurrency: peak %d simultaneous pulls, want 1", client.maxInflight)
	}

	client = build()
	if err := New(client, &config.Config{PullConcurrency: 3}).Run(); err != nil {
		t.Fatal(err)
	}
	if client.maxInflight < 2 || client.maxInflight > 3 {
		t.Fatalf("pull-concurrency=3: peak %d simultaneous pulls, want 2..3", client.maxInflight)
	}
}

func dnsFail(host string) error {
	return fmt.Errorf("Get \"https://%s/v2/\": dial tcp: lookup %s: no such host", host, host)
}

// waitFor polls cond for up to 3s.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for i := 0; i < 300; i++ {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// While DNS/registry is unreachable: one registry call trips it, then no pulls
// and no recreates and no more registry calls; the background prober (not the
// update loop) notices when it is back and the next pass updates everything.
func TestRegistryOutageBacksOffAndResumes(t *testing.T) {
	client := newStub(runningContainer("c1", "a"), runningContainer("c2", "b"), runningContainer("c3", "c"))
	client.remoteErr = dnsFail("registry-1.docker.io")
	u := New(client, &config.Config{})
	g := u.Gates()
	g.SetBackoff(5*time.Millisecond, 20*time.Millisecond)
	var mu sync.Mutex
	dnsUp := false
	probes := 0
	g.Probe = func(ctx context.Context, host string) error {
		mu.Lock()
		defer mu.Unlock()
		probes++
		if dnsUp {
			return nil
		}
		return dnsFail(host)
	}

	if err := u.Run(); err != nil {
		t.Fatal(err)
	}
	ref := "example/a:latest"
	if !g.Down(ref) {
		t.Fatal("gate should be down after a DNS failure")
	}
	if len(client.pulled) != 0 || len(client.recreated) != 0 {
		t.Fatalf("pulled=%v recreated=%v during an outage", client.pulled, client.recreated)
	}
	if n := u.totalFailed.Load(); n != 0 {
		t.Fatalf("an outage counted %d failed updates, want 0", n)
	}

	// More passes while down never touch the registry (the prober does the probing).
	callsBefore := client.remoteCalls
	for i := 0; i < 3; i++ {
		_ = u.Run()
	}
	if client.remoteCalls != callsBefore {
		t.Fatalf("update loop made %d registry calls while the registry was down", client.remoteCalls-callsBefore)
	}

	waitFor(t, "prober to retry", func() bool { mu.Lock(); defer mu.Unlock(); return probes >= 2 })

	// DNS is back: the prober clears the gate with no update pass involved.
	client.mu.Lock()
	client.remoteErr = nil
	client.mu.Unlock()
	mu.Lock()
	dnsUp = true
	mu.Unlock()
	waitFor(t, "gate to reopen", func() bool { return !g.Down(ref) })

	if err := u.Run(); err != nil {
		t.Fatal(err)
	}
	if len(client.recreated) != 3 {
		t.Fatalf("recreated %v after recovery, want all 3", client.recreated)
	}
}

// One dead registry host must not stop updates from the others.
func TestOutageIsPerRegistryHost(t *testing.T) {
	bad := stubContainer{id: "c1", name: "bad", ref: "dead.example.invalid/x/bad:latest"}
	good := runningContainer("c2", "good")
	client := newStub(bad, good)
	client.errFor = map[string]error{bad.ref: dnsFail("dead.example.invalid")}
	u := New(client, &config.Config{})
	u.Gates().SetBackoff(time.Hour, time.Hour)
	u.Gates().Probe = func(ctx context.Context, host string) error { return dnsFail(host) }

	if err := u.Run(); err != nil {
		t.Fatal(err)
	}
	if !u.Gates().Down(bad.ref) || u.Gates().Down(good.ref) {
		t.Fatal("only the dead host's gate should be down")
	}
	if len(client.recreated) != 1 || client.recreated[0] != "c2" {
		t.Fatalf("recreated %v, want only the healthy host's container c2", client.recreated)
	}
}

const hubLimit = "toomanyrequests: You have reached your unauthenticated pull rate limit. https://www.docker.com/increase-rate-limit"

type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newClocked(client *stubClient, cfg *config.Config) (*Updater, *fakeClock) {
	u := New(client, cfg)
	clk := &fakeClock{t: time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC)}
	u.now = clk.now
	u.Gates().SetClock(clk.now)
	return u, clk
}

// A registry that answers "denied"/"not found" is reachable: it must not trip
// the network gate. The refusal is held per ref: one try, then no registry
// calls until the wait is over, and it does not count as a failure.
func TestRegistryRefusalIsNotAnOutage(t *testing.T) {
	client := newStub(runningContainer("c1", "a"))
	client.remoteErr = fmt.Errorf("pull access denied for example/a, repository does not exist")
	u, clk := newClocked(client, &config.Config{})
	u.Gates().Probe = func(ctx context.Context, host string) error { return dnsFail(host) }
	_ = u.Run()
	if u.Gates().Down("example/a:latest") {
		t.Fatal("an access-denied answer tripped the outage gate")
	}
	if !u.Gates().Held("example/a:latest") {
		t.Fatal("refused ref is not held")
	}
	calls, pulls := client.remoteCalls, client.pullCalls
	for i := 0; i < 5; i++ {
		_ = u.Run()
	}
	if client.remoteCalls != calls || client.pullCalls != pulls {
		t.Fatalf("held ref still hit the registry: digest %d->%d pulls %d->%d", calls, client.remoteCalls, pulls, client.pullCalls)
	}
	if n := u.totalFailed.Load(); n != 0 {
		t.Fatalf("failed=%d, a held refusal must not count as a failure", n)
	}
	clk.advance(registry.RefusedMinHold + time.Second)
	_ = u.Run()
	if client.remoteCalls != calls+1 {
		t.Fatalf("after the wait the ref was not retried (digest calls %d, want %d)", client.remoteCalls, calls+1)
	}
	// second refusal doubles the wait
	clk.advance(registry.RefusedMinHold + time.Second)
	_ = u.Run()
	if client.remoteCalls != calls+1 {
		t.Fatal("wait did not double after the second refusal")
	}
}

// A refusal of one ref must not hold another ref on the same host.
func TestRefusalIsPerRef(t *testing.T) {
	bad := stubContainer{id: "c1", name: "priv", ref: "ghcr.io/x/priv:latest"}
	good := stubContainer{id: "c2", name: "pub", ref: "ghcr.io/x/pub:latest"}
	client := newStub(bad, good)
	client.errFor = map[string]error{bad.ref: fmt.Errorf("unauthorized: authentication required")}
	u, _ := newClocked(client, &config.Config{})
	_ = u.Run()
	if !u.Gates().Held(bad.ref) || u.Gates().Held(good.ref) {
		t.Fatal("refusal must hold only the refused ref")
	}
	if len(client.recreated) != 1 || client.recreated[0] != "c2" {
		t.Fatalf("recreated %v, want the public image rolled", client.recreated)
	}
}

// Docker Hub 429: the host is held (digest check and pull alike), registry
// calls stop, the wait doubles per consecutive 429 up to 1h, another host is
// unaffected, and the health watcher's network gate stays up.
func TestRateLimitHoldsHostAndBacksOff(t *testing.T) {
	a := runningContainer("c1", "a")
	b := runningContainer("c2", "b")
	other := stubContainer{id: "c3", name: "g", ref: "ghcr.io/x/g:latest"}
	client := newStub(a, b, other)
	client.errFor = map[string]error{a.ref: errors.New(hubLimit), b.ref: errors.New(hubLimit)}
	u, clk := newClocked(client, &config.Config{})
	g := u.Gates()
	_ = u.Run()
	if !g.Held(a.ref) || !g.Held(b.ref) || g.Held(other.ref) {
		t.Fatal("429 must hold docker.io only")
	}
	if g.Down(a.ref) || g.AnyDown() {
		t.Fatal("a 429 must not flag the network gate (health restarts stay allowed)")
	}
	if len(client.recreated) != 1 || client.recreated[0] != "c3" {
		t.Fatalf("recreated %v, want only the ghcr container", client.recreated)
	}
	calls := client.remoteCalls
	for i := 0; i < 5; i++ {
		_ = u.Run()
	}
	if client.remoteCalls != calls+5 { // only the unheld ghcr container asks each pass
		t.Fatalf("host on hold still got registry calls (%d -> %d, want +5 from ghcr only)", calls, client.remoteCalls)
	}
	want := registry.RateLimitMinHold
	for i := 0; i < 6; i++ {
		clk.advance(want - time.Second)
		if !g.Held(a.ref) {
			t.Fatalf("round %d: hold ended early (want %s)", i, want)
		}
		clk.advance(2 * time.Second)
		if g.Held(a.ref) {
			t.Fatalf("round %d: hold lasted past %s", i, want)
		}
		_ = u.Run() // retried, 429 again, hold doubles
		want *= 2
		if want > registry.RateLimitMaxHold {
			want = registry.RateLimitMaxHold
		}
	}
	if want != registry.RateLimitMaxHold {
		t.Fatal("test did not reach the cap")
	}
}

// A hold keeps answering with the local image, so an image pulled earlier still rolls.
func TestHeldRefStillRollsPulledImage(t *testing.T) {
	c := runningContainer("c1", "a")
	client := newStub(c)
	client.errFor = map[string]error{c.ref: errors.New(hubLimit)}
	u, _ := newClocked(client, &config.Config{})
	client.local[c.ref] = "sha256:pulled-earlier"
	_ = u.Run()
	if len(client.recreated) != 1 {
		t.Fatalf("recreated %v, want the container rolled to the already pulled image", client.recreated)
	}
}

// Failures from calls admitted before the hold took effect must not escalate it.
func TestRateLimitDoesNotEscalateWithinHold(t *testing.T) {
	g := registry.NewGates()
	clk := &fakeClock{t: time.Unix(1e9, 0)}
	g.SetClock(clk.now)
	err := errors.New(hubLimit)
	_, d1, first := g.Hold("a:1", err)
	_, d2, again := g.Hold("b:1", err)
	if !first || again || d1 != registry.RateLimitMinHold || d2 != d1 {
		t.Fatalf("concurrent 429s escalated: %v/%v %v/%v", first, again, d1, d2)
	}
	g.Release("c:1")
	if g.Held("a:1") {
		t.Fatal("a success on the host must clear its 429 hold")
	}
}

// A digest refusal alone is not final (private registry with working creds):
// the pull is still tried; its refusal then holds the ref.
func TestDigestRefusalStillTriesPullThenHolds(t *testing.T) {
	client := newStub(runningContainer("c1", "a"))
	client.remoteErr = fmt.Errorf("unauthorized: authentication required")
	u, _ := newClocked(client, &config.Config{})
	_ = u.Run()
	if client.pullCalls != 1 || !u.Gates().Held("example/a:latest") {
		t.Fatalf("pull calls=%d held=%v, want 1 pull then held", client.pullCalls, u.Gates().Held("example/a:latest"))
	}
	_ = u.Run()
	if client.pullCalls != 1 {
		t.Fatal("held ref was pulled again")
	}
}

// The realistic Docker Hub shape: manifest checks succeed (not rate limited),
// the pull gets 429. A good digest check must not reset the escalation:
// 10m, 20m, 40m, then 1h.
func TestDigestOKPull429EscalatesHold(t *testing.T) {
	c := runningContainer("c1", "a")
	client := newStub(c)
	client.pullErr = map[string]error{c.ref: errors.New(hubLimit)}
	u, clk := newClocked(client, &config.Config{})
	g := u.Gates()
	_ = u.Run()
	pulls := client.pullCalls
	for _, want := range []time.Duration{registry.RateLimitMinHold, 2 * registry.RateLimitMinHold, 4 * registry.RateLimitMinHold, registry.RateLimitMaxHold, registry.RateLimitMaxHold} {
		clk.advance(want - time.Second)
		if !g.Held(c.ref) {
			t.Fatalf("hold ended before %s", want)
		}
		clk.advance(2 * time.Second)
		if g.Held(c.ref) {
			t.Fatalf("hold lasted past %s", want)
		}
		_ = u.Run() // digest OK (changed), pull 429 again
		pulls++
		if client.pullCalls != pulls {
			t.Fatalf("pull calls=%d want %d", client.pullCalls, pulls)
		}
		if u.totalFailed.Load() != 0 {
			t.Fatal("429 counted as a failure")
		}
	}
}

// A changed digest followed by a failed pull must not stamp the hub throttle:
// the next pass retries instead of waiting out hub-interval.
func TestFailedPullDoesNotStampHubThrottle(t *testing.T) {
	c := runningContainer("c1", "a")
	client := newStub(c)
	client.pullErr = map[string]error{c.ref: errors.New("Error response from daemon: 500 Internal Server Error")}
	u, _ := newClocked(client, &config.Config{HubInterval: time.Hour})
	_ = u.Run()
	_ = u.Run()
	if client.pullCalls != 2 {
		t.Fatalf("pull calls=%d, want a retry on the next pass (throttle stamped despite the failed pull?)", client.pullCalls)
	}
}

// ResetBackoff (API "update now") clears real holds.
func TestResetBackoffClearsHolds(t *testing.T) {
	c := runningContainer("c1", "a")
	client := newStub(c)
	client.remoteErr = errors.New(hubLimit)
	u, _ := newClocked(client, &config.Config{})
	_ = u.Run()
	if !u.Gates().Held(c.ref) {
		t.Fatal("not held")
	}
	u.ResetBackoff()
	if u.Gates().Held(c.ref) || u.Gates().HeldCount() != 0 {
		t.Fatal("ResetBackoff left a hold")
	}
}

// Held and no local image to fall back on: skipped quietly (wraps errRegistryDown), not a failure.
func TestHeldWithoutLocalImageIsSkippedNotFailed(t *testing.T) {
	c := runningContainer("c1", "a")
	client := newStub(c)
	client.errFor = map[string]error{c.ref: errors.New(hubLimit)}
	u, _ := newClocked(client, &config.Config{})
	_ = u.Run()
	delete(client.local, c.ref) // pruned meanwhile
	_, err := u.fetchImage(context.Background(), c.ref)
	if !errors.Is(err, errPullHeld) || !errors.Is(err, errRegistryDown) {
		t.Fatalf("err=%v, want errPullHeld", err)
	}
	if u.totalFailed.Load() != 0 {
		t.Fatal("counted as failed")
	}
}

// docker.io is checked at most every hub-interval; other hosts every pass.
func TestHubIntervalThrottlesDockerIOOnly(t *testing.T) {
	hub := runningContainer("c1", "a")
	gh := stubContainer{id: "c2", name: "g", ref: "ghcr.io/x/g:latest", imageID: "sha256:new"}
	client := newStub(hub, gh)
	client.registry[gh.ref] = "sha256:new"
	client.local[gh.ref] = "sha256:new"
	client.local[hub.ref] = "sha256:new"
	client.containers[0].imageID = "sha256:new"
	u, clk := newClocked(client, &config.Config{HubInterval: time.Hour})
	_ = u.Run()
	first := client.remoteCalls
	if first != 2 {
		t.Fatalf("first pass digest calls=%d, want 2", first)
	}
	clk.advance(10 * time.Minute)
	_ = u.Run()
	if client.remoteCalls != first+1 {
		t.Fatalf("second pass: %d new digest calls, want 1 (ghcr only)", client.remoteCalls-first)
	}
	clk.advance(time.Hour)
	_ = u.Run()
	if client.remoteCalls != first+1+2 {
		t.Fatalf("after the interval docker.io was not re-checked (%d)", client.remoteCalls)
	}
	// manual update now bypasses the throttle
	u.ResetBackoff()
	n := client.remoteCalls
	_ = u.Run()
	if client.remoteCalls != n+2 {
		t.Fatal("ResetBackoff did not clear the throttle")
	}
}

// A 429 stamps nothing: the next pass after the hold asks again.
func TestRateLimitDoesNotStampHubThrottle(t *testing.T) {
	c := runningContainer("c1", "a")
	client := newStub(c)
	client.errFor = map[string]error{c.ref: errors.New(hubLimit)}
	u, clk := newClocked(client, &config.Config{HubInterval: 3 * time.Hour})
	_ = u.Run()
	clk.advance(registry.RateLimitMinHold + time.Second)
	n := client.remoteCalls
	_ = u.Run()
	if client.remoteCalls != n+1 {
		t.Fatal("hold over but docker.io was not retried (throttle stamped on 429?)")
	}
}

// Digest checks run DigestConcurrency at a time, independent of the pull slots.
func TestDigestChecksRunInParallel(t *testing.T) {
	const n, delay = 12, 100 * time.Millisecond
	var cs []stubContainer
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("c%d", i)
		cs = append(cs, runningContainer(name, name))
	}
	client := newStub(cs...)
	client.digestDelay = delay
	client.pullDelay = 10 * time.Millisecond
	start := time.Now()
	if err := New(client, &config.Config{DigestConcurrency: 6}).Run(); err != nil {
		t.Fatal(err)
	}
	// 12 refs / 6 = 2 rounds of 100ms; serial would take 1.2s.
	if el := time.Since(start); el > 6*delay {
		t.Fatalf("digest checks took %v, want about %v (parallel)", el, 2*delay)
	}
	if len(client.pulled) != n {
		t.Fatalf("pulled %d, want %d", len(client.pulled), n)
	}
	if client.maxInflight != 1 {
		t.Fatalf("peak %d simultaneous pulls, want PullConcurrency 1", client.maxInflight)
	}
}

// An unscoped instance leaves scope-labelled containers to their own instance;
// a scoped one handles only its scope.
func TestScopeFiltering(t *testing.T) {
	voice := runningContainer("v1", "voice")
	voice.labels = map[string]string{"dockwarden.scope": "voice"}
	plain := runningContainer("p1", "plain")
	other := runningContainer("o1", "other")
	other.labels = map[string]string{"dockwarden.scope": "prod"}

	names := func(cfg *config.Config) []string {
		u := New(newStub(voice, plain, other), cfg)
		list, err := u.client.ListContainers(context.Background(), docker.ListOptions{})
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, c := range u.filterContainers(list) {
			out = append(out, c.Name)
		}
		return out
	}
	if got := names(&config.Config{}); len(got) != 1 || got[0] != "plain" {
		t.Fatalf("unscoped handled %v, want [plain]", got)
	}
	if got := names(&config.Config{Scope: "voice"}); len(got) != 1 || got[0] != "voice" {
		t.Fatalf("scope=voice handled %v, want [voice]", got)
	}
}

// Once one digest check trips the registry, refs still queued for a digest slot
// must not call it again (the gates are re-checked after the slot is taken).
func TestDigestGatesRecheckedAfterSlot(t *testing.T) {
	var cs []stubContainer
	for i := 0; i < 8; i++ {
		name := fmt.Sprintf("g%d", i)
		cs = append(cs, runningContainer(name, name))
	}
	client := newStub(cs...)
	client.remoteErr = dnsFail("registry-1.docker.io")
	client.digestDelay = 50 * time.Millisecond
	_ = New(client, &config.Config{DigestConcurrency: 2}).Run()
	if client.remoteCalls > 2 {
		t.Fatalf("%d registry calls after the host tripped, want <= digest concurrency (2)", client.remoteCalls)
	}
}

// With nothing to update, digest checks must not queue behind a slow pull slot.
func TestDigestChecksNotBlockedBySlowPull(t *testing.T) {
	const n, delay = 12, 100 * time.Millisecond
	var cs []stubContainer
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("u%d", i)
		cs = append(cs, runningContainer(name, name))
	}
	client := newStub(cs...)
	client.digestDelay = delay
	client.pullDelay = 500 * time.Millisecond
	for _, c := range cs {
		client.registry[c.ref] = client.local[c.ref] // remote digest == local: nothing to update
	}
	start := time.Now()
	if err := New(client, &config.Config{PullConcurrency: 1, DigestConcurrency: 6}).Run(); err != nil {
		t.Fatal(err)
	}
	if el := time.Since(start); el > 6*delay {
		t.Fatalf("0-update cycle took %v, want about %v", el, 2*delay)
	}
	if len(client.pulled) != 0 {
		t.Fatalf("pulled %v with unchanged digests", client.pulled)
	}
}
