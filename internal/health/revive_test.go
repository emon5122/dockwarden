package health

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/emon5122/dockwarden/internal/config"
	"github.com/emon5122/dockwarden/internal/docker"
)

// fakeClient serves one container: ListContainers returns its summary (no
// restart policy / exit state, like the real list), GetContainer the full
// inspect. The embedded interface panics on any un-stubbed call.
type fakeClient struct {
	docker.Client

	mu         sync.Mutex
	ctr        docker.Container
	inspectErr error
	inspected  int
	started    []string
}

func (f *fakeClient) ListContainers(ctx context.Context, opts docker.ListOptions) ([]docker.Container, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	summary := f.ctr
	summary.RestartPolicy, summary.ExitCode, summary.OOMKilled = "", 0, false
	return []docker.Container{summary}, nil
}

func (f *fakeClient) GetContainer(ctx context.Context, id string) (docker.Container, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inspected++
	if f.inspectErr != nil {
		return docker.Container{}, f.inspectErr
	}
	return f.ctr, nil
}

func (f *fakeClient) StartContainer(ctx context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.started = append(f.started, id)
	f.ctr.State = "running"
	return nil
}

func (f *fakeClient) set(fn func(c *docker.Container)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(&f.ctr)
}

// runThenStop lets the watcher see the container running once, applies stop,
// then runs one more tick and returns the fake for assertions.
func runThenStop(t *testing.T, ctr docker.Container, stop func(c *docker.Container)) *fakeClient {
	t.Helper()
	ctr.ID, ctr.Name, ctr.ImageID, ctr.State = "c1", "svc", "sha256:img", "running"
	f := &fakeClient{ctr: ctr}
	w := NewWatcher(f, &config.Config{})
	w.checkHealthConcurrently()
	f.set(stop)
	w.checkHealthConcurrently()
	return f
}

func stoppedWith(code int, oom bool) func(c *docker.Container) {
	return func(c *docker.Container) { c.State, c.ExitCode, c.OOMKilled = "exited", code, oom }
}

// rl9 tts-bn 2026-11-09: `docker update --restart=no; docker stop` must stay stopped.
func TestManualStopWithRestartNoIsNotRevived(t *testing.T) {
	for _, policy := range []string{"no", ""} {
		f := runThenStop(t, docker.Container{RestartPolicy: policy}, stoppedWith(143, false))
		if len(f.started) != 0 {
			t.Fatalf("policy %q: container stopped on purpose was started again", policy)
		}
	}
}

// restart=no means no revive even for an error exit: nothing asked to keep it up.
func TestErrorExitWithRestartNoIsNotRevived(t *testing.T) {
	f := runThenStop(t, docker.Container{RestartPolicy: "no"}, stoppedWith(1, false))
	if len(f.started) != 0 {
		t.Fatal("restart=no container was revived")
	}
}

// `docker stop` of an unless-stopped/always container (Docker leaves it down too).
func TestDockerStopOfUnlessStoppedIsNotRevived(t *testing.T) {
	for _, code := range []int{0, 130, 131, 137, 143} {
		f := runThenStop(t, docker.Container{RestartPolicy: "unless-stopped"}, stoppedWith(code, false))
		if len(f.started) != 0 {
			t.Fatalf("exit %d: deliberately stopped unless-stopped container was revived", code)
		}
	}
}

// Current behaviour kept: a real crash of an unless-stopped container is revived.
func TestCrashWithUnlessStoppedIsRevived(t *testing.T) {
	for _, policy := range []string{"unless-stopped", "always", "on-failure"} {
		f := runThenStop(t, docker.Container{RestartPolicy: policy}, stoppedWith(1, false))
		if len(f.started) != 1 {
			t.Fatalf("policy %s: crashed container started %d times, want 1", policy, len(f.started))
		}
	}
}

// An OOM kill exits 137 like `docker kill`, but it is a crash.
func TestOOMKillIsRevived(t *testing.T) {
	f := runThenStop(t, docker.Container{RestartPolicy: "always"}, stoppedWith(137, true))
	if len(f.started) != 1 {
		t.Fatalf("OOM-killed container started %d times, want 1", len(f.started))
	}
}

// Docker is already restarting it (or it is paused): hands off, no inspect.
func TestRestartingOrPausedIsLeftToDocker(t *testing.T) {
	for _, st := range []string{"restarting", "paused"} {
		f := runThenStop(t, docker.Container{RestartPolicy: "always"}, func(c *docker.Container) { c.State, c.ExitCode = st, 1 })
		if len(f.started) != 0 || f.inspected != 0 {
			t.Fatalf("state %s: started=%d inspected=%d, want 0/0", st, len(f.started), f.inspected)
		}
	}
}

// dockwarden.watch.enable=false: never touched, even after a crash.
func TestWatchDisabledLabelIsSkipped(t *testing.T) {
	f := runThenStop(t, docker.Container{
		RestartPolicy: "unless-stopped",
		Labels:        map[string]string{"dockwarden.watch.enable": "false"},
	}, stoppedWith(1, false))
	if len(f.started) != 0 || f.inspected != 0 {
		t.Fatalf("watch-disabled container: started=%d inspected=%d, want 0/0", len(f.started), f.inspected)
	}
}

// Inspect failure fails closed and retries on the next tick.
func TestInspectErrorDoesNotReviveAndRetries(t *testing.T) {
	ctr := docker.Container{ID: "c1", Name: "svc", ImageID: "sha256:img", State: "running", RestartPolicy: "always"}
	f := &fakeClient{ctr: ctr}
	w := NewWatcher(f, &config.Config{})
	w.checkHealthConcurrently()
	f.set(stoppedWith(1, false))
	f.inspectErr = errors.New("daemon busy")
	w.checkHealthConcurrently()
	if len(f.started) != 0 {
		t.Fatal("revived a container it could not inspect")
	}
	f.inspectErr = nil
	w.checkHealthConcurrently()
	if len(f.started) != 1 {
		t.Fatalf("after inspect recovered: started %d times, want 1", len(f.started))
	}
}

// A deliberately stopped container that someone starts again is tracked again.
func TestStoppedThenStartedAgainIsTrackedAgain(t *testing.T) {
	f := runThenStop(t, docker.Container{RestartPolicy: "unless-stopped"}, stoppedWith(143, false))
	w := NewWatcher(f, &config.Config{})
	f.set(func(c *docker.Container) { c.State = "running" })
	w.checkHealthConcurrently()
	f.set(stoppedWith(2, false))
	w.checkHealthConcurrently()
	if len(f.started) != 1 {
		t.Fatalf("crash after manual restart: started %d times, want 1", len(f.started))
	}
}
