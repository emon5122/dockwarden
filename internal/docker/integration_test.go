//go:build integration

package docker

import (
	"context"
	"testing"
	"time"

	"github.com/emon5122/dockwarden/internal/registry"
)

// Real-stack check (needs a Docker daemon and internet): go test -tags integration ./internal/docker
// Verifies against the real daemon + registry that (1) an unchanged digest is
// recognised so the pull can be skipped, and (2) a DNS failure comes back in a
// form the outage gate classifies as a network error.
func TestIntegrationDigestAndDNSFailure(t *testing.T) {
	c, err := NewClient(ClientOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	const ref = "hello-world:latest"
	if err := c.PullImage(ctx, ref); err != nil {
		t.Fatal(err)
	}
	remote, err := c.RemoteDigest(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	local, err := c.LocalDigests(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, d := range local {
		found = found || d == remote
	}
	if !found {
		t.Fatalf("remote digest %s not among local digests %v: the skip-pull check would never fire", remote, local)
	}

	const dead = "dns-fail.invalid/x/y:latest"
	if _, err := c.RemoteDigest(ctx, dead); !registry.IsNetworkError(err) {
		t.Fatalf("RemoteDigest on an unresolvable host: %v (not classified as a network error)", err)
	}
	if err := c.PullImage(ctx, dead); !registry.IsNetworkError(err) {
		t.Fatalf("PullImage on an unresolvable host: %v (not classified as a network error)", err)
	}
}
