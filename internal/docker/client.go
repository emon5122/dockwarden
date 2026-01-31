package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
	dockerclient "github.com/docker/docker/client"
	log "github.com/sirupsen/logrus"
)

// Client interface for Docker operations
type Client interface {
	Ping() error
	ListContainers(ctx context.Context, opts ListOptions) ([]Container, error)
	GetContainer(ctx context.Context, id string) (Container, error)
	StopContainer(ctx context.Context, id string, timeout time.Duration) error
	StartContainer(ctx context.Context, id string) error
	RestartContainer(ctx context.Context, id string, timeout time.Duration) error
	RemoveContainer(ctx context.Context, id string) error
	RecreateContainer(ctx context.Context, id string, timeout time.Duration) (string, error)
	PullImage(ctx context.Context, imageName string) error
	GetImageDigest(ctx context.Context, imageName string) (string, error)
	RemoveImage(ctx context.Context, imageID string) error
}

// ClientOptions configures the Docker client
type ClientOptions struct {
	IncludeStopped    bool
	IncludeRestarting bool
	RemoveVolumes     bool
}

// ListOptions for filtering containers
type ListOptions struct {
	All           bool
	LabelFilter   string
	IncludeHealth bool
}

type dockerClient struct {
	api  dockerclient.CommonAPIClient
	opts ClientOptions
}

// NewClient creates a new Docker client
func NewClient(opts ClientOptions) (Client, error) {
	cli, err := dockerclient.NewClientWithOpts(dockerclient.FromEnv, dockerclient.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("failed to create Docker client: %w", err)
	}

	return &dockerClient{
		api:  cli,
		opts: opts,
	}, nil
}

// Ping checks if Docker is reachable
func (c *dockerClient) Ping() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := c.api.Ping(ctx)
	return err
}

// ListContainers returns all containers matching the filter
func (c *dockerClient) ListContainers(ctx context.Context, opts ListOptions) ([]Container, error) {
	filterArgs := filters.NewArgs()
	if opts.LabelFilter != "" {
		filterArgs.Add("label", opts.LabelFilter)
	}

	listOpts := container.ListOptions{
		All:     opts.All || c.opts.IncludeStopped,
		Filters: filterArgs,
	}

	containers, err := c.api.ContainerList(ctx, listOpts)
	if err != nil {
		return nil, fmt.Errorf("failed to list containers: %w", err)
	}

	result := make([]Container, 0, len(containers))
	for _, ctr := range containers {
		container := containerFromAPI(ctr)
		result = append(result, container)
	}

	return result, nil
}

// GetContainer returns a single container by ID
func (c *dockerClient) GetContainer(ctx context.Context, id string) (Container, error) {
	info, err := c.api.ContainerInspect(ctx, id)
	if err != nil {
		return Container{}, fmt.Errorf("failed to inspect container %s: %w", id, err)
	}

	return containerFromInspect(info), nil
}

// StopContainer stops a container
func (c *dockerClient) StopContainer(ctx context.Context, id string, timeout time.Duration) error {
	timeoutSec := int(timeout.Seconds())
	stopOpts := container.StopOptions{
		Timeout: &timeoutSec,
	}

	if err := c.api.ContainerStop(ctx, id, stopOpts); err != nil {
		return fmt.Errorf("failed to stop container %s: %w", id, err)
	}

	log.Debugf("Stopped container %s", id[:12])
	return nil
}

// StartContainer starts a container
func (c *dockerClient) StartContainer(ctx context.Context, id string) error {
	if err := c.api.ContainerStart(ctx, id, container.StartOptions{}); err != nil {
		return fmt.Errorf("failed to start container %s: %w", id, err)
	}

	log.Debugf("Started container %s", id[:12])
	return nil
}

// RestartContainer restarts a container
func (c *dockerClient) RestartContainer(ctx context.Context, id string, timeout time.Duration) error {
	timeoutSec := int(timeout.Seconds())
	stopOpts := container.StopOptions{
		Timeout: &timeoutSec,
	}

	if err := c.api.ContainerRestart(ctx, id, stopOpts); err != nil {
		return fmt.Errorf("failed to restart container %s: %w", id, err)
	}

	log.Debugf("Restarted container %s", id[:12])
	return nil
}

// RemoveContainer removes a container
func (c *dockerClient) RemoveContainer(ctx context.Context, id string) error {
	opts := container.RemoveOptions{
		RemoveVolumes: c.opts.RemoveVolumes,
		Force:         true,
	}

	if err := c.api.ContainerRemove(ctx, id, opts); err != nil {
		return fmt.Errorf("failed to remove container %s: %w", id, err)
	}

	log.Debugf("Removed container %s", id[:12])
	return nil
}

// RecreateContainer stops, removes, and recreates a container with the latest image
func (c *dockerClient) RecreateContainer(ctx context.Context, id string, timeout time.Duration) (string, error) {
	// Get container config before removing
	inspect, err := c.api.ContainerInspect(ctx, id)
	if err != nil {
		return "", fmt.Errorf("failed to inspect container %s: %w", id, err)
	}

	containerName := strings.TrimPrefix(inspect.Name, "/")
	oldImageID := inspect.Image

	log.Debugf("Recreating container %s with latest image", containerName)

	// Stop container if running
	if inspect.State.Running {
		timeoutSec := int(timeout.Seconds())
		stopOpts := container.StopOptions{Timeout: &timeoutSec}
		if err := c.api.ContainerStop(ctx, id, stopOpts); err != nil {
			return "", fmt.Errorf("failed to stop container %s: %w", id, err)
		}
		log.Debugf("Stopped container %s", containerName)
	}

	// Remove the container
	if err := c.api.ContainerRemove(ctx, id, container.RemoveOptions{
		RemoveVolumes: false, // Preserve volumes
		Force:         true,
	}); err != nil {
		return "", fmt.Errorf("failed to remove container %s: %w", id, err)
	}
	log.Debugf("Removed old container %s", containerName)

	// Create new container with same config but updated image reference
	// The image should already be pulled with latest tag
	createResp, err := c.api.ContainerCreate(ctx, inspect.Config, inspect.HostConfig, nil, nil, containerName)
	if err != nil {
		return "", fmt.Errorf("failed to create container %s: %w", containerName, err)
	}
	newID := createResp.ID
	log.Debugf("Created new container %s with ID %s", containerName, newID[:12])

	// Start the new container
	if err := c.api.ContainerStart(ctx, newID, container.StartOptions{}); err != nil {
		return "", fmt.Errorf("failed to start container %s: %w", containerName, err)
	}
	log.Infof("Started new container %s", containerName)

	// Return old image ID for cleanup
	_ = oldImageID // Available for caller to clean up if needed
	return newID, nil
}

// PullImage pulls the latest version of an image
func (c *dockerClient) PullImage(ctx context.Context, imageName string) error {
	// Get registry authentication
	authStr := getRegistryAuth(imageName)

	reader, err := c.api.ImagePull(ctx, imageName, image.PullOptions{
		RegistryAuth: authStr,
	})
	if err != nil {
		return fmt.Errorf("failed to pull image %s: %w", imageName, err)
	}
	defer reader.Close()

	// Consume the reader to complete the pull
	decoder := json.NewDecoder(reader)
	for {
		var message struct {
			Status   string `json:"status"`
			Progress string `json:"progress"`
			Error    string `json:"error"`
		}

		if err := decoder.Decode(&message); err != nil {
			break
		}

		if message.Error != "" {
			return fmt.Errorf("pull error: %s", message.Error)
		}

		log.Debugf("Pull %s: %s %s", imageName, message.Status, message.Progress)
	}

	log.Infof("Pulled image %s", imageName)
	return nil
}

// GetImageDigest returns the digest for an image
func (c *dockerClient) GetImageDigest(ctx context.Context, imageName string) (string, error) {
	inspect, _, err := c.api.ImageInspectWithRaw(ctx, imageName)
	if err != nil {
		return "", fmt.Errorf("failed to inspect image %s: %w", imageName, err)
	}

	if len(inspect.RepoDigests) > 0 {
		// Extract digest from repo digest (format: repo@sha256:...)
		parts := strings.Split(inspect.RepoDigests[0], "@")
		if len(parts) == 2 {
			return parts[1], nil
		}
	}

	return inspect.ID, nil
}

// RemoveImage removes an image
func (c *dockerClient) RemoveImage(ctx context.Context, imageID string) error {
	_, err := c.api.ImageRemove(ctx, imageID, image.RemoveOptions{
		Force:         false,
		PruneChildren: true,
	})
	if err != nil {
		return fmt.Errorf("failed to remove image %s: %w", imageID, err)
	}

	log.Debugf("Removed image %s", imageID[:12])
	return nil
}

// getRegistryAuth returns the base64 encoded auth for a registry
func getRegistryAuth(imageName string) string {
	// Determine the registry from image name
	registry := getRegistryFromImage(imageName)

	// Try to find auth config from multiple sources
	configPaths := getDockerConfigPaths()

	for _, configPath := range configPaths {
		if auth := getAuthFromConfig(configPath, registry); auth != "" {
			log.Debugf("Found auth for registry %s from %s", registry, configPath)
			return auth
		}
	}

	log.Debugf("No auth found for registry %s", registry)
	return ""
}

// getDockerConfigPaths returns possible Docker config file paths in priority order
func getDockerConfigPaths() []string {
	var paths []string

	// 1. Environment variable takes highest priority
	if secretPath := os.Getenv("DOCKWARDEN_REGISTRY_SECRET"); secretPath != "" {
		paths = append(paths, secretPath)
	}

	// 2. DOCKER_CONFIG environment variable
	if dockerConfig := os.Getenv("DOCKER_CONFIG"); dockerConfig != "" {
		paths = append(paths, dockerConfig+"/config.json")
	}

	// 3. User's home directory
	if home := os.Getenv("HOME"); home != "" {
		paths = append(paths, home+"/.docker/config.json")
	}

	// 4. Root's docker config (for running as root in container)
	paths = append(paths, "/root/.docker/config.json")

	return paths
}

// getRegistryFromImage extracts the registry hostname from an image reference
func getRegistryFromImage(imageName string) string {
	// Remove tag or digest
	if idx := strings.Index(imageName, "@"); idx != -1 {
		imageName = imageName[:idx]
	}
	if idx := strings.LastIndex(imageName, ":"); idx != -1 {
		// Make sure it's not a port number followed by a path
		afterColon := imageName[idx+1:]
		if !strings.Contains(afterColon, "/") {
			imageName = imageName[:idx]
		}
	}

	// Check if image has a registry prefix
	if strings.Contains(imageName, "/") {
		parts := strings.SplitN(imageName, "/", 2)
		// If first part contains a dot or colon, it's a registry
		if strings.Contains(parts[0], ".") || strings.Contains(parts[0], ":") {
			return parts[0]
		}
	}

	// Default to Docker Hub
	return "docker.io"
}

// getAuthFromConfig reads auth from a Docker config file
func getAuthFromConfig(configPath string, registry string) string {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return ""
	}

	var dockerConfig struct {
		Auths map[string]struct {
			Auth string `json:"auth"`
		} `json:"auths"`
	}

	if err := json.Unmarshal(data, &dockerConfig); err != nil {
		log.Debugf("Failed to parse docker config %s: %v", configPath, err)
		return ""
	}

	// Try exact match first
	if auth, ok := dockerConfig.Auths[registry]; ok && auth.Auth != "" {
		return auth.Auth
	}

	// Try with https:// prefix
	if auth, ok := dockerConfig.Auths["https://"+registry]; ok && auth.Auth != "" {
		return auth.Auth
	}

	// For Docker Hub, try multiple known keys
	if registry == "docker.io" {
		dockerHubKeys := []string{
			"https://index.docker.io/v1/",
			"index.docker.io",
			"https://index.docker.io",
			"registry-1.docker.io",
		}
		for _, key := range dockerHubKeys {
			if auth, ok := dockerConfig.Auths[key]; ok && auth.Auth != "" {
				return auth.Auth
			}
		}
	}

	return ""
}

// containerFromAPI converts API container to our Container type
func containerFromAPI(c types.Container) Container {
	name := ""
	if len(c.Names) > 0 {
		name = strings.TrimPrefix(c.Names[0], "/")
	}

	return Container{
		ID:      c.ID,
		Name:    name,
		Image:   c.Image,
		ImageID: c.ImageID,
		State:   c.State,
		Status:  c.Status,
		Labels:  c.Labels,
		Created: time.Unix(c.Created, 0),
	}
}

// containerFromInspect converts inspect result to our Container type
func containerFromInspect(info types.ContainerJSON) Container {
	created, _ := time.Parse(time.RFC3339Nano, info.Created)
	healthStatus := ""
	if info.State.Health != nil {
		healthStatus = info.State.Health.Status
	}

	return Container{
		ID:           info.ID,
		Name:         strings.TrimPrefix(info.Name, "/"),
		Image:        info.Config.Image,
		ImageID:      info.Image,
		State:        info.State.Status,
		Status:       info.State.Status,
		Labels:       info.Config.Labels,
		Created:      created,
		HealthStatus: healthStatus,
	}
}
