package docker

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/api/types/registry"
	dockerclient "github.com/moby/moby/client"
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
	RenameContainer(ctx context.Context, id string, newName string) error
	RecreateContainer(ctx context.Context, id string, timeout time.Duration) (string, error)
	PullImage(ctx context.Context, imageName string) error
	GetImageDigest(ctx context.Context, imageName string) (string, error)
	GetImageID(ctx context.Context, imageName string) (string, error)
	RemoveImage(ctx context.Context, imageID string) error
	ContainerLogs(ctx context.Context, id string, follow bool, tail string) (io.ReadCloser, error)
	CloneContainer(ctx context.Context, id string, newName string) (string, error)
	GetSelfContainerID() string
	GetContainerBinds(ctx context.Context, id string) ([]string, error)
	CreateHelperContainer(ctx context.Context, image string, cmd []string, binds []string) (string, error)
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
	api  *dockerclient.Client
	opts ClientOptions
}

// NewClient creates a new Docker client
func NewClient(opts ClientOptions) (Client, error) {
	cli, err := dockerclient.New(dockerclient.FromEnv)
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
	_, err := c.api.Ping(ctx, dockerclient.PingOptions{})
	return err
}

// ListContainers returns all containers matching the filter
func (c *dockerClient) ListContainers(ctx context.Context, opts ListOptions) ([]Container, error) {
	listOpts := dockerclient.ContainerListOptions{
		All: opts.All || c.opts.IncludeStopped,
	}

	if opts.LabelFilter != "" {
		listOpts.Filters = make(dockerclient.Filters).Add("label", opts.LabelFilter)
	}

	result, err := c.api.ContainerList(ctx, listOpts)
	if err != nil {
		return nil, fmt.Errorf("failed to list containers: %w", err)
	}

	containers := make([]Container, 0, len(result.Items))
	for _, ctr := range result.Items {
		containers = append(containers, containerFromSummary(ctr))
	}

	return containers, nil
}

// GetContainer returns a single container by ID
func (c *dockerClient) GetContainer(ctx context.Context, id string) (Container, error) {
	result, err := c.api.ContainerInspect(ctx, id, dockerclient.ContainerInspectOptions{})
	if err != nil {
		return Container{}, fmt.Errorf("failed to inspect container %s: %w", id, err)
	}

	return containerFromInspect(result.Container), nil
}

// StopContainer stops a container
func (c *dockerClient) StopContainer(ctx context.Context, id string, timeout time.Duration) error {
	timeoutSec := int(timeout.Seconds())
	if _, err := c.api.ContainerStop(ctx, id, dockerclient.ContainerStopOptions{
		Timeout: &timeoutSec,
	}); err != nil {
		return fmt.Errorf("failed to stop container %s: %w", id, err)
	}

	log.Debugf("Stopped container %s", truncateID(id))
	return nil
}

// StartContainer starts a container
func (c *dockerClient) StartContainer(ctx context.Context, id string) error {
	if _, err := c.api.ContainerStart(ctx, id, dockerclient.ContainerStartOptions{}); err != nil {
		return fmt.Errorf("failed to start container %s: %w", id, err)
	}

	log.Debugf("Started container %s", truncateID(id))
	return nil
}

// RestartContainer restarts a container
func (c *dockerClient) RestartContainer(ctx context.Context, id string, timeout time.Duration) error {
	timeoutSec := int(timeout.Seconds())
	if _, err := c.api.ContainerRestart(ctx, id, dockerclient.ContainerRestartOptions{
		Timeout: &timeoutSec,
	}); err != nil {
		return fmt.Errorf("failed to restart container %s: %w", id, err)
	}

	log.Debugf("Restarted container %s", truncateID(id))
	return nil
}

// RemoveContainer removes a container
func (c *dockerClient) RemoveContainer(ctx context.Context, id string) error {
	if _, err := c.api.ContainerRemove(ctx, id, dockerclient.ContainerRemoveOptions{
		RemoveVolumes: c.opts.RemoveVolumes,
		Force:         true,
	}); err != nil {
		return fmt.Errorf("failed to remove container %s: %w", id, err)
	}

	log.Debugf("Removed container %s", truncateID(id))
	return nil
}

// RenameContainer renames a container
func (c *dockerClient) RenameContainer(ctx context.Context, id string, newName string) error {
	if _, err := c.api.ContainerRename(ctx, id, dockerclient.ContainerRenameOptions{
		NewName: newName,
	}); err != nil {
		return fmt.Errorf("failed to rename container %s: %w", id, err)
	}

	log.Debugf("Renamed container %s to %s", truncateID(id), newName)
	return nil
}

// RecreateContainer stops, removes, and recreates a container with the latest image
func (c *dockerClient) RecreateContainer(ctx context.Context, id string, timeout time.Duration) (string, error) {
	// Get container config before removing
	inspectResult, err := c.api.ContainerInspect(ctx, id, dockerclient.ContainerInspectOptions{})
	if err != nil {
		return "", fmt.Errorf("failed to inspect container %s: %w", id, err)
	}
	inspect := inspectResult.Container

	containerName := strings.TrimPrefix(inspect.Name, "/")
	oldImageID := inspect.Image
	createConfig := cloneContainerConfig(inspect.Config)
	c.refreshImageConfigDefaults(ctx, oldImageID, createConfig)

	log.Debugf("Recreating container %s with latest image", containerName)

	// Build NetworkingConfig from current network settings
	networkingConfig := &network.NetworkingConfig{
		EndpointsConfig: make(map[string]*network.EndpointSettings),
	}
	if inspect.NetworkSettings != nil && inspect.NetworkSettings.Networks != nil {
		for netName, netSettings := range inspect.NetworkSettings.Networks {
			if netSettings != nil {
				endpointConfig := &network.EndpointSettings{
					Aliases:    netSettings.Aliases,
					Links:      netSettings.Links,
					DriverOpts: netSettings.DriverOpts,
					IPAMConfig: netSettings.IPAMConfig,
					NetworkID:  netSettings.NetworkID,
				}
				networkingConfig.EndpointsConfig[netName] = endpointConfig
				log.Debugf("Preserving network %s for container %s", netName, containerName)
			}
		}
	}

	// Stop container if running
	if inspect.State.Running {
		timeoutSec := int(timeout.Seconds())
		if _, err := c.api.ContainerStop(ctx, id, dockerclient.ContainerStopOptions{
			Timeout: &timeoutSec,
		}); err != nil {
			return "", fmt.Errorf("failed to stop container %s: %w", id, err)
		}
		log.Debugf("Stopped container %s", containerName)
	}

	// Remove the container
	if _, err := c.api.ContainerRemove(ctx, id, dockerclient.ContainerRemoveOptions{
		RemoveVolumes: false,
		Force:         true,
	}); err != nil {
		return "", fmt.Errorf("failed to remove container %s: %w", id, err)
	}
	log.Debugf("Removed old container %s", containerName)

	// Create new container with same config
	createResult, err := c.api.ContainerCreate(ctx, dockerclient.ContainerCreateOptions{
		Config:           createConfig,
		HostConfig:       inspect.HostConfig,
		NetworkingConfig: networkingConfig,
		Name:             containerName,
	})
	if err != nil {
		return "", fmt.Errorf("failed to create container %s: %w", containerName, err)
	}
	newID := createResult.ID
	log.Debugf("Created new container %s with ID %s", containerName, truncateID(newID))

	// Connect to additional networks
	networkCount := 0
	for netName, endpointConfig := range networkingConfig.EndpointsConfig {
		networkCount++
		if networkCount == 1 {
			continue
		}
		if _, err := c.api.NetworkConnect(ctx, endpointConfig.NetworkID, dockerclient.NetworkConnectOptions{
			Container:      newID,
			EndpointConfig: endpointConfig,
		}); err != nil {
			log.Warnf("Failed to connect container %s to network %s: %v", containerName, netName, err)
		} else {
			log.Debugf("Connected container %s to network %s", containerName, netName)
		}
	}

	// Start the new container
	if _, err := c.api.ContainerStart(ctx, newID, dockerclient.ContainerStartOptions{}); err != nil {
		return "", fmt.Errorf("failed to start container %s: %w", containerName, err)
	}
	log.Infof("Started new container %s", containerName)

	_ = oldImageID
	return newID, nil
}

// PullImage pulls the latest version of an image
func (c *dockerClient) PullImage(ctx context.Context, imageName string) error {
	authStr := getRegistryAuth(imageName)

	pullResp, err := c.api.ImagePull(ctx, imageName, dockerclient.ImagePullOptions{
		RegistryAuth: authStr,
	})
	if err != nil {
		return fmt.Errorf("failed to pull image %s: %w", imageName, err)
	}

	if err := pullResp.Wait(ctx); err != nil {
		pullResp.Close()
		return fmt.Errorf("failed to pull image %s: %w", imageName, err)
	}

	log.Debugf("Pulled image %s", imageName)
	return nil
}

// GetImageDigest returns the digest for an image
func (c *dockerClient) GetImageDigest(ctx context.Context, imageName string) (string, error) {
	inspect, err := c.api.ImageInspect(ctx, imageName)
	if err != nil {
		return "", fmt.Errorf("failed to inspect image %s: %w", imageName, err)
	}

	if len(inspect.RepoDigests) > 0 {
		parts := strings.Split(inspect.RepoDigests[0], "@")
		if len(parts) == 2 {
			return parts[1], nil
		}
	}

	return inspect.ID, nil
}

// GetImageID returns the image config ID for an image (sha256:...)
func (c *dockerClient) GetImageID(ctx context.Context, imageName string) (string, error) {
	inspect, err := c.api.ImageInspect(ctx, imageName)
	if err != nil {
		return "", fmt.Errorf("failed to inspect image %s: %w", imageName, err)
	}

	return inspect.ID, nil
}

// RemoveImage removes an image
func (c *dockerClient) RemoveImage(ctx context.Context, imageID string) error {
	if _, err := c.api.ImageRemove(ctx, imageID, dockerclient.ImageRemoveOptions{
		Force:         false,
		PruneChildren: true,
	}); err != nil {
		return fmt.Errorf("failed to remove image %s: %w", imageID, err)
	}

	log.Debugf("Removed image %s", truncateID(imageID))
	return nil
}

// ContainerLogs returns log stream for a container
func (c *dockerClient) ContainerLogs(ctx context.Context, id string, follow bool, tail string) (io.ReadCloser, error) {
	if tail == "" {
		tail = "100"
	}
	result, err := c.api.ContainerLogs(ctx, id, dockerclient.ContainerLogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Follow:     follow,
		Tail:       tail,
		Timestamps: true,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get logs for container %s: %w", id, err)
	}
	return result, nil
}

// CloneContainer creates a new container based on an existing container's config.
// Does NOT stop or remove the source container. Used for self-update where we
// can't stop ourselves.
func (c *dockerClient) CloneContainer(ctx context.Context, id string, newName string) (string, error) {
	inspectResult, err := c.api.ContainerInspect(ctx, id, dockerclient.ContainerInspectOptions{})
	if err != nil {
		return "", fmt.Errorf("failed to inspect container %s: %w", id, err)
	}
	inspect := inspectResult.Container
	createConfig := cloneContainerConfig(inspect.Config)
	c.refreshImageConfigDefaults(ctx, inspect.Image, createConfig)

	log.Debugf("Cloning container %s as %s", strings.TrimPrefix(inspect.Name, "/"), newName)

	// Build NetworkingConfig from current settings
	networkingConfig := &network.NetworkingConfig{
		EndpointsConfig: make(map[string]*network.EndpointSettings),
	}
	if inspect.NetworkSettings != nil && inspect.NetworkSettings.Networks != nil {
		for netName, netSettings := range inspect.NetworkSettings.Networks {
			if netSettings != nil {
				networkingConfig.EndpointsConfig[netName] = &network.EndpointSettings{
					Aliases:    netSettings.Aliases,
					Links:      netSettings.Links,
					DriverOpts: netSettings.DriverOpts,
					IPAMConfig: netSettings.IPAMConfig,
					NetworkID:  netSettings.NetworkID,
				}
			}
		}
	}

	// Create new container with same config
	createResult, err := c.api.ContainerCreate(ctx, dockerclient.ContainerCreateOptions{
		Config:           createConfig,
		HostConfig:       inspect.HostConfig,
		NetworkingConfig: networkingConfig,
		Name:             newName,
	})
	if err != nil {
		return "", fmt.Errorf("failed to create container %s: %w", newName, err)
	}
	newID := createResult.ID
	log.Debugf("Created clone container %s with ID %s", newName, truncateID(newID))

	// Connect to additional networks
	networkCount := 0
	for netName, endpointConfig := range networkingConfig.EndpointsConfig {
		networkCount++
		if networkCount == 1 {
			continue
		}
		if _, err := c.api.NetworkConnect(ctx, endpointConfig.NetworkID, dockerclient.NetworkConnectOptions{
			Container:      newID,
			EndpointConfig: endpointConfig,
		}); err != nil {
			log.Warnf("Failed to connect container %s to network %s: %v", newName, netName, err)
		}
	}

	return newID, nil
}

// GetContainerBinds returns the bind mounts of a container from its HostConfig
func (c *dockerClient) GetContainerBinds(ctx context.Context, id string) ([]string, error) {
	result, err := c.api.ContainerInspect(ctx, id, dockerclient.ContainerInspectOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to inspect container %s: %w", id, err)
	}
	if result.Container.HostConfig != nil {
		return result.Container.HostConfig.Binds, nil
	}
	return nil, nil
}

// CreateHelperContainer creates and starts a short-lived helper container.
// The container is set to auto-remove after exit.
func (c *dockerClient) CreateHelperContainer(ctx context.Context, image string, cmd []string, binds []string) (string, error) {
	name := fmt.Sprintf("dockwarden-takeover-%d", time.Now().Unix())

	createResult, err := c.api.ContainerCreate(ctx, dockerclient.ContainerCreateOptions{
		Config: &container.Config{
			Image: image,
			Cmd:   cmd,
		},
		HostConfig: &container.HostConfig{
			Binds:      binds,
			AutoRemove: true,
		},
		Name: name,
	})
	if err != nil {
		return "", fmt.Errorf("failed to create helper container: %w", err)
	}

	if _, err := c.api.ContainerStart(ctx, createResult.ID, dockerclient.ContainerStartOptions{}); err != nil {
		// Clean up the created container on start failure
		c.api.ContainerRemove(ctx, createResult.ID, dockerclient.ContainerRemoveOptions{Force: true})
		return "", fmt.Errorf("failed to start helper container: %w", err)
	}

	log.Debugf("Started helper container %s (%s)", name, truncateID(createResult.ID))
	return createResult.ID, nil
}

// refreshImageConfigDefaults strips the environment variables and labels that the
// container inherited verbatim from its OLD image. Docker's inspect merges an
// image's baked-in ENV and LABELs with the user-supplied ones into a single flat
// set, with no record of which came from where. If we recreate the container with
// that merged set, the old image's defaults become pinned as explicit values, so
// the daemon never lets the NEW image's updated defaults take effect — leaving the
// container advertising a stale image version (e.g. org.opencontainers.image.version).
// Removing the entries that exactly match the old image's defaults leaves only the
// genuine runtime overrides, so the new image's defaults flow through on recreate.
func (c *dockerClient) refreshImageConfigDefaults(ctx context.Context, oldImageID string, config *container.Config) {
	if config == nil || oldImageID == "" {
		return
	}

	inspect, err := c.api.ImageInspect(ctx, oldImageID)
	if err != nil {
		log.Debugf("Keeping existing container env/labels: failed to inspect old image %s: %v", truncateID(oldImageID), err)
		return
	}
	if inspect.Config == nil {
		return
	}

	if len(inspect.Config.Env) > 0 {
		originalLen := len(config.Env)
		config.Env = stripInheritedImageEnv(config.Env, inspect.Config.Env)
		if removed := originalLen - len(config.Env); removed > 0 {
			log.Debugf("Removed %d inherited image environment entries before recreate", removed)
		}
	}

	if len(inspect.Config.Labels) > 0 {
		originalLen := len(config.Labels)
		config.Labels = stripInheritedImageLabels(config.Labels, inspect.Config.Labels)
		if removed := originalLen - len(config.Labels); removed > 0 {
			log.Debugf("Removed %d inherited image label entries before recreate", removed)
		}
	}
}

func cloneContainerConfig(config *container.Config) *container.Config {
	if config == nil {
		return &container.Config{}
	}

	clone := *config
	clone.Env = append([]string(nil), config.Env...)
	if config.Labels != nil {
		clone.Labels = make(map[string]string, len(config.Labels))
		for key, value := range config.Labels {
			clone.Labels[key] = value
		}
	}

	return &clone
}

func stripInheritedImageEnv(containerEnv []string, oldImageEnv []string) []string {
	if len(containerEnv) == 0 || len(oldImageEnv) == 0 {
		return containerEnv
	}

	oldDefaults := envMap(oldImageEnv)
	refreshed := make([]string, 0, len(containerEnv))
	for _, entry := range containerEnv {
		key, value, ok := splitEnv(entry)
		if !ok {
			refreshed = append(refreshed, entry)
			continue
		}

		if oldValue, inherited := oldDefaults[key]; inherited && value == oldValue {
			continue
		}

		refreshed = append(refreshed, entry)
	}

	return refreshed
}

// stripInheritedImageLabels removes labels that the container inherited verbatim
// from the old image's metadata. Whatever remains is a genuine runtime/compose
// label, so the new image's labels (including version labels like
// org.opencontainers.image.version) are applied on recreate instead of being
// shadowed by the old image's frozen values.
func stripInheritedImageLabels(containerLabels, oldImageLabels map[string]string) map[string]string {
	if len(containerLabels) == 0 || len(oldImageLabels) == 0 {
		return containerLabels
	}

	refreshed := make(map[string]string, len(containerLabels))
	for key, value := range containerLabels {
		if oldValue, inherited := oldImageLabels[key]; inherited && value == oldValue {
			continue
		}
		refreshed[key] = value
	}

	return refreshed
}

func envMap(env []string) map[string]string {
	values := make(map[string]string, len(env))
	for _, entry := range env {
		key, value, ok := splitEnv(entry)
		if ok {
			values[key] = value
		}
	}

	return values
}

func splitEnv(entry string) (string, string, bool) {
	key, value, ok := strings.Cut(entry, "=")
	if !ok || key == "" {
		return "", "", false
	}

	return key, value, true
}

// GetSelfContainerID returns the container ID of the current running container (if any)
func (c *dockerClient) GetSelfContainerID() string {
	// Try reading from cgroup
	data, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		parts := strings.Split(line, "/")
		if len(parts) > 2 {
			id := parts[len(parts)-1]
			if len(id) == 64 {
				return id
			}
		}
	}

	// Try hostname
	hostname, err := os.Hostname()
	if err != nil {
		return ""
	}
	if len(hostname) == 12 || len(hostname) == 64 {
		return hostname
	}

	return ""
}

func truncateID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

// getRegistryAuth returns the base64 encoded auth for a registry
func getRegistryAuth(imageName string) string {
	registryHost := getRegistryFromImage(imageName)

	configPaths := getDockerConfigPaths()
	for _, configPath := range configPaths {
		if auth := getAuthFromConfig(configPath, registryHost); auth != "" {
			log.Debugf("Found auth for registry %s from %s", registryHost, configPath)
			return auth
		}
	}

	log.Debugf("No auth found for registry %s", registryHost)
	return ""
}

func getDockerConfigPaths() []string {
	var paths []string

	if secretPath := os.Getenv("DOCKWARDEN_REGISTRY_SECRET"); secretPath != "" {
		paths = append(paths, secretPath)
	}
	if dockerConfig := os.Getenv("DOCKER_CONFIG"); dockerConfig != "" {
		paths = append(paths, dockerConfig+"/config.json")
	}
	if home := os.Getenv("HOME"); home != "" {
		paths = append(paths, home+"/.docker/config.json")
	}
	paths = append(paths, "/root/.docker/config.json")

	return paths
}

func getRegistryFromImage(imageName string) string {
	if idx := strings.Index(imageName, "@"); idx != -1 {
		imageName = imageName[:idx]
	}
	if idx := strings.LastIndex(imageName, ":"); idx != -1 {
		afterColon := imageName[idx+1:]
		if !strings.Contains(afterColon, "/") {
			imageName = imageName[:idx]
		}
	}

	if strings.Contains(imageName, "/") {
		parts := strings.SplitN(imageName, "/", 2)
		if strings.Contains(parts[0], ".") || strings.Contains(parts[0], ":") {
			return parts[0]
		}
	}

	return "docker.io"
}

func getAuthFromConfig(configPath string, registryHost string) string {
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

	convertAuth := func(authBase64, serverAddress string) string {
		decoded, err := base64.StdEncoding.DecodeString(authBase64)
		if err != nil {
			return ""
		}

		parts := strings.SplitN(string(decoded), ":", 2)
		if len(parts) != 2 {
			return ""
		}

		authConfig := registry.AuthConfig{
			Username:      parts[0],
			Password:      parts[1],
			ServerAddress: serverAddress,
		}

		jsonAuth, err := json.Marshal(authConfig)
		if err != nil {
			return ""
		}

		return base64.URLEncoding.EncodeToString(jsonAuth)
	}

	if auth, ok := dockerConfig.Auths[registryHost]; ok && auth.Auth != "" {
		return convertAuth(auth.Auth, registryHost)
	}
	if auth, ok := dockerConfig.Auths["https://"+registryHost]; ok && auth.Auth != "" {
		return convertAuth(auth.Auth, registryHost)
	}
	if registryHost == "docker.io" {
		dockerHubKeys := []string{
			"https://index.docker.io/v1/",
			"index.docker.io",
			"https://index.docker.io",
			"registry-1.docker.io",
		}
		for _, key := range dockerHubKeys {
			if auth, ok := dockerConfig.Auths[key]; ok && auth.Auth != "" {
				return convertAuth(auth.Auth, key)
			}
		}
	}

	return ""
}

// containerFromSummary converts new API container.Summary to our Container type
func containerFromSummary(c container.Summary) Container {
	name := ""
	if len(c.Names) > 0 {
		name = strings.TrimPrefix(c.Names[0], "/")
	}

	healthStatus := ""
	if c.Health != nil {
		healthStatus = string(c.Health.Status)
	}

	return Container{
		ID:           c.ID,
		Name:         name,
		Image:        c.Image,
		ImageID:      c.ImageID,
		State:        string(c.State),
		Status:       c.Status,
		Labels:       c.Labels,
		Created:      time.Unix(c.Created, 0),
		HealthStatus: healthStatus,
	}
}

// containerFromInspect converts inspect response to our Container type
func containerFromInspect(info container.InspectResponse) Container {
	name := strings.TrimPrefix(info.Name, "/")

	healthStatus := ""
	if info.State != nil && info.State.Health != nil {
		healthStatus = string(info.State.Health.Status)
	}

	state := ""
	if info.State != nil {
		state = string(info.State.Status)
	}

	created, _ := time.Parse(time.RFC3339Nano, info.Created)

	return Container{
		ID:           info.ID,
		Name:         name,
		Image:        info.Config.Image,
		ImageID:      info.Image,
		State:        state,
		Status:       state,
		Labels:       info.Config.Labels,
		Created:      created,
		HealthStatus: healthStatus,
	}
}
