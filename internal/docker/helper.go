package docker

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	dockerclient "github.com/moby/moby/client"
	log "github.com/sirupsen/logrus"
)

// HelperSpec describes a short-lived helper container that runs one command
// to completion (for example docker-compose against the host daemon).
type HelperSpec struct {
	Name       string
	Image      string
	Entrypoint []string
	Cmd        []string
	Binds      []string
	Env        []string
	Timeout    time.Duration
	// Kind is the dockwarden.helper label value ("compose" when empty).
	Kind string
	// Labels are added to the helper's own labels (never override them).
	Labels map[string]string
}

// HelperResult is what a finished helper container left behind.
type HelperResult struct {
	ExitCode int64
	Stdout   string
	Stderr   string
}

// helperLabels keep DockWarden from managing, watching or mistaking its own
// helpers for itself.
func helperLabels(kind string, extra map[string]string) map[string]string {
	if kind == "" {
		kind = "compose"
	}
	labels := map[string]string{}
	for k, v := range extra {
		labels[k] = v
	}
	labels[HelperLabel] = kind
	labels["dockwarden.enable"] = "false"
	labels["dockwarden.update.enable"] = "false"
	labels["dockwarden.watch.enable"] = "false"
	return labels
}

// createHelper creates (does not start) a helper container.
func (c *dockerClient) createHelper(ctx context.Context, spec HelperSpec) (string, error) {
	created, err := c.api.ContainerCreate(ctx, dockerclient.ContainerCreateOptions{
		Config: &container.Config{
			Image:      spec.Image,
			Entrypoint: spec.Entrypoint,
			Cmd:        spec.Cmd,
			Env:        spec.Env,
			Labels:     helperLabels(spec.Kind, spec.Labels),
			// the image's HEALTHCHECK is DockWarden's own; a helper has none
			Healthcheck: &container.HealthConfig{Test: []string{"NONE"}},
		},
		HostConfig: &container.HostConfig{
			Binds:          spec.Binds,
			NetworkMode:    "none",
			CapDrop:        []string{"ALL"},
			SecurityOpt:    []string{"no-new-privileges:true"},
			ReadonlyRootfs: true,
		},
		Name: spec.Name,
	})
	if err != nil {
		return "", fmt.Errorf("create helper %s: %w", spec.Name, err)
	}
	return created.ID, nil
}

func (c *dockerClient) removeHelper(id, name string) {
	// fresh context: the run context may already be cancelled
	rmCtx, rmCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer rmCancel()
	if _, err := c.api.ContainerRemove(rmCtx, id, dockerclient.ContainerRemoveOptions{Force: true}); err != nil {
		log.Warnf("Failed to remove helper %s: %v", name, err)
	}
}

// StartHelper creates and starts a helper container and returns at once: the
// helper outlives this call (and DockWarden itself). The caller finds it later
// by its labels, reads its exit code and removes it. A helper that fails to
// start is removed.
func (c *dockerClient) StartHelper(ctx context.Context, spec HelperSpec) (string, error) {
	id, err := c.createHelper(ctx, spec)
	if err != nil {
		return "", err
	}
	if _, err := c.api.ContainerStart(ctx, id, dockerclient.ContainerStartOptions{}); err != nil {
		c.removeHelper(id, spec.Name)
		return "", fmt.Errorf("start helper %s: %w", spec.Name, err)
	}
	return id, nil
}

// RunHelper creates, starts and waits for a helper container, returns its
// exit code and output, and always removes it.
func (c *dockerClient) RunHelper(ctx context.Context, spec HelperSpec) (HelperResult, error) {
	if spec.Timeout <= 0 {
		spec.Timeout = 5 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, spec.Timeout)
	defer cancel()

	id, err := c.createHelper(ctx, spec)
	if err != nil {
		return HelperResult{}, err
	}
	defer c.removeHelper(id, spec.Name)

	wait := c.api.ContainerWait(ctx, id, dockerclient.ContainerWaitOptions{Condition: container.WaitConditionNextExit})
	if _, err := c.api.ContainerStart(ctx, id, dockerclient.ContainerStartOptions{}); err != nil {
		return HelperResult{}, fmt.Errorf("start helper %s: %w", spec.Name, err)
	}

	var res HelperResult
	select {
	case w := <-wait.Result:
		res.ExitCode = w.StatusCode
		if w.Error != nil && w.Error.Message != "" {
			return res, fmt.Errorf("helper %s: %s", spec.Name, w.Error.Message)
		}
	case err := <-wait.Error:
		return res, fmt.Errorf("wait for helper %s: %w", spec.Name, err)
	case <-ctx.Done():
		return res, fmt.Errorf("helper %s timed out after %s", spec.Name, spec.Timeout)
	}

	logs, err := c.api.ContainerLogs(ctx, id, dockerclient.ContainerLogsOptions{ShowStdout: true, ShowStderr: true})
	if err != nil {
		return res, fmt.Errorf("read helper %s output: %w", spec.Name, err)
	}
	defer logs.Close()
	var stdout, stderr bytes.Buffer
	if _, err := stdcopy.StdCopy(&stdout, &stderr, logs); err != nil {
		return res, fmt.Errorf("read helper %s output: %w", spec.Name, err)
	}
	res.Stdout, res.Stderr = stdout.String(), strings.TrimSpace(stderr.String())
	return res, nil
}
