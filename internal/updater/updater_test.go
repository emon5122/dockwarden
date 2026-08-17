package updater

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/emon5122/dockwarden/internal/config"
	"github.com/emon5122/dockwarden/internal/docker"
)

// stubClient implements the parts of docker.Client the update cycle touches.
// The embedded interface makes any un-stubbed call panic, which is the desired
// failure mode in these tests.
type stubClient struct {
	docker.Client

	mu          sync.Mutex
	containers  []docker.Container
	digestCalls map[string]int
	pulled      []string
	recreated   []string
	startFlags  []bool
}

func (s *stubClient) ListContainers(ctx context.Context, opts docker.ListOptions) ([]docker.Container, error) {
	return s.containers, nil
}

func (s *stubClient) GetSelfContainerID() string { return "" }

// GetImageDigest returns a different digest on the second call for the same
// image, so every checked container reports an available update.
func (s *stubClient) GetImageDigest(ctx context.Context, imageName string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.digestCalls == nil {
		s.digestCalls = make(map[string]int)
	}
	s.digestCalls[imageName]++
	if s.digestCalls[imageName] == 1 {
		return "sha256:old", nil
	}
	return "sha256:new", nil
}

func (s *stubClient) PullImage(ctx context.Context, imageName string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pulled = append(s.pulled, imageName)
	return nil
}

func (s *stubClient) RecreateContainer(ctx context.Context, id string, timeout time.Duration, startStopped bool) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recreated = append(s.recreated, id)
	s.startFlags = append(s.startFlags, startStopped)
	return "new-" + id, nil
}

func (s *stubClient) GetContainer(ctx context.Context, id string) (docker.Container, error) {
	return docker.Container{ID: id}, nil
}

func runningContainer(id, name string) docker.Container {
	return docker.Container{ID: id, Name: name, Image: "example/" + name + ":latest", State: "running"}
}

// Regression: --no-restart was accepted and silently ignored, so containers
// were recreated anyway. It must pull the image but leave the container alone.
func TestRunNoRestartPullsButDoesNotRecreate(t *testing.T) {
	client := &stubClient{containers: []docker.Container{runningContainer("c1", "web")}}
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
	client := &stubClient{containers: []docker.Container{runningContainer("c1", "web")}}
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
