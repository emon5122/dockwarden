package health

import (
	"context"
	"testing"

	"github.com/emon5122/dockwarden/internal/config"
	"github.com/emon5122/dockwarden/internal/docker"
)

// listClient serves a fixed container list; any other client call panics (the
// healthy containers below never reach one).
type listClient struct {
	docker.Client
	list []docker.Container
}

func (l listClient) ListContainers(context.Context, docker.ListOptions) ([]docker.Container, error) {
	return l.list, nil
}

func processed(scope string, list []docker.Container) map[string]bool {
	w := NewWatcher(listClient{list: list}, &config.Config{Scope: scope})
	w.checkHealthConcurrently()
	w.statesMu.Lock()
	defer w.statesMu.Unlock()
	got := map[string]bool{}
	for id := range w.states {
		got[id] = true
	}
	return got
}

// An unscoped watcher leaves scope-labelled containers alone; a scoped one
// touches only its own scope.
func TestWatcherScope(t *testing.T) {
	scoped := docker.Container{ID: "s", Name: "s", State: "running", HealthStatus: "healthy", Labels: map[string]string{"dockwarden.scope": "x"}}
	plain := docker.Container{ID: "p", Name: "p", State: "running", HealthStatus: "healthy"}
	list := []docker.Container{scoped, plain}

	if got := processed("", list); got["s"] || !got["p"] {
		t.Fatalf("unscoped watcher processed %v, want only p", got)
	}
	if got := processed("x", list); !got["s"] || got["p"] {
		t.Fatalf("scope=x watcher processed %v, want only s", got)
	}
}
