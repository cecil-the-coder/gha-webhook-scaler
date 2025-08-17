package runtime

import (
	"context"
	"fmt"
	"io"
	"log"
	"strings"
	"time"

	"github.com/cecil-the-coder/gha-webhook-scaler/internal/config"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/client"
	"github.com/opencontainers/image-spec/specs-go/v1"
)

// DockerRuntime implements ContainerRuntime for Docker
type DockerRuntime struct {
	client DockerClientInterface
}

// NewDockerRuntime creates a new Docker runtime client
func NewDockerRuntime() (*DockerRuntime, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("failed to create Docker client: %w", err)
	}

	return &DockerRuntime{
		client: cli,
	}, nil
}

// NewDockerRuntimeWithClient creates a new Docker runtime with a custom client (for testing)
func NewDockerRuntimeWithClient(client DockerClientInterface) *DockerRuntime {
	return &DockerRuntime{
		client: client,
	}
}

// GetRuntimeType returns the runtime type
func (d *DockerRuntime) GetRuntimeType() config.RuntimeType {
	return config.RuntimeTypeDocker
}

// HealthCheck verifies Docker is available and accessible
func (d *DockerRuntime) HealthCheck() error {
	ctx := context.Background()
	_, err := d.client.Ping(ctx)
	if err != nil {
		return fmt.Errorf("Docker daemon not accessible: %w", err)
	}
	return nil
}

// StartRunner starts a new runner with default labels
func (d *DockerRuntime) StartRunner(config *config.Config, runnerName, repoFullName string) (*RunnerContainer, error) {
	return d.StartRunnerWithLabels(config, runnerName, repoFullName, config.RunnerLabels)
}

// StartRunnerWithLabels starts a new runner with specific labels
func (d *DockerRuntime) StartRunnerWithLabels(config *config.Config, runnerName, repoFullName string, labels []string) (*RunnerContainer, error) {
	// Get owner configuration for this repository
	ownerConfig, err := config.GetOwnerForRepo(repoFullName)
	if err != nil {
		return nil, fmt.Errorf("failed to get owner config for repo %s: %w", repoFullName, err)
	}
	
	return d.StartRunnerWithLabelsForOwner(config, ownerConfig, runnerName, repoFullName, labels)
}

// StartRunnerWithLabelsForOwner starts a new runner with labels for a specific owner
func (d *DockerRuntime) StartRunnerWithLabelsForOwner(config *config.Config, ownerConfig *config.OwnerConfig, runnerName, repoFullName string, labels []string) (*RunnerContainer, error) {
	ctx := context.Background()

	// Pull image if not exists
	if err := d.ensureImage(ctx, config.RunnerImage); err != nil {
		return nil, fmt.Errorf("failed to ensure image: %w", err)
	}

	// Construct repository URL for the specific repo
	var repoURL string
	if repoFullName != "" {
		repoURL = fmt.Sprintf("https://github.com/%s", repoFullName)
	} else {
		repoURL = ownerConfig.GetRepoURL()
	}

	// Use provided labels or fall back to config labels
	var labelsString string
	if len(labels) > 0 {
		labelsString = strings.Join(labels, ",")
	} else {
		labelsString = config.GetRunnerLabelsString()
	}

	// Prepare environment variables
	env := []string{
		fmt.Sprintf("RUNNER_NAME=%s", runnerName),
		fmt.Sprintf("ACCESS_TOKEN=%s", ownerConfig.GitHubToken),
		fmt.Sprintf("REPO_URL=%s", repoURL),
		"RUNNER_SCOPE=repo",
		fmt.Sprintf("LABELS=%s", labelsString),
		"EPHEMERAL=true",
		"DISABLE_AUTO_UPDATE=true",
	}

	// Add repository info to labels for tracking
	if repoFullName != "" {
		env = append(env, fmt.Sprintf("REPO_FULL_NAME=%s", repoFullName))
	}

	// Prepare mounts
	mounts := []mount.Mount{
		{
			Type:   mount.TypeBind,
			Source: "/var/run/docker.sock",
			Target: "/var/run/docker.sock",
		},
	}

	// Add cache volumes if enabled
	if config.CacheVolumes {
		mounts = append(mounts,
			mount.Mount{
				Type:   mount.TypeVolume,
				Source: fmt.Sprintf("docker-cache-%s", config.Architecture),
				Target: "/var/lib/docker",
			},
			mount.Mount{
				Type:   mount.TypeVolume,
				Source: fmt.Sprintf("buildkit-cache-%s", config.Architecture),
				Target: "/tmp/buildkit-cache",
			},
			mount.Mount{
				Type:   mount.TypeVolume,
				Source: "registry-cache",
				Target: "/tmp/registry-cache",
			},
		)
	}

	// Container configuration
	containerConfig := &container.Config{
		Image: config.RunnerImage,
		Env:   env,
	}

	// Host configuration
	hostConfig := &container.HostConfig{
		Mounts: mounts,
		Resources: container.Resources{
			Memory:   6 * 1024 * 1024 * 1024, // 6GB
			NanoCPUs: 4 * 1000000000,         // 4 CPUs
		},
		RestartPolicy: container.RestartPolicy{
			Name: "no",
		},
		AutoRemove: true,
	}

	// Platform configuration to ensure correct architecture
	platform := &v1.Platform{
		Architecture: config.Architecture,
		OS:          "linux",
	}

	// Create container with explicit platform
	resp, err := d.client.ContainerCreate(ctx, containerConfig, hostConfig, nil, platform, runnerName)
	if err != nil {
		return nil, fmt.Errorf("failed to create container: %w", err)
	}

	// Start container
	if err := d.client.ContainerStart(ctx, resp.ID, types.ContainerStartOptions{}); err != nil {
		// Clean up container if start fails
		d.client.ContainerRemove(ctx, resp.ID, types.ContainerRemoveOptions{Force: true})
		return nil, fmt.Errorf("failed to start container: %w", err)
	}

	return &RunnerContainer{
		ID:           resp.ID,
		Name:         runnerName,
		Status:       "running",
		CreatedAt:    time.Now(),
		Architecture: config.Architecture,
	}, nil
}

// GetRunningRunners returns all running runners for the specified architecture
func (d *DockerRuntime) GetRunningRunners(architecture string) ([]RunnerContainer, error) {
	ctx := context.Background()

	containers, err := d.client.ContainerList(ctx, types.ContainerListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list containers: %w", err)
	}

	runners := make([]RunnerContainer, 0)
	for _, container := range containers {
		// Check if this is a runner container for our architecture
		if d.isRunnerContainer(container, architecture) {
			runners = append(runners, RunnerContainer{
				ID:           container.ID,
				Name:         container.Names[0][1:], // Remove leading slash
				Status:       container.Status,
				CreatedAt:    time.Unix(container.Created, 0),
				Architecture: architecture,
			})
		}
	}

	return runners, nil
}

// StopRunner stops a specific runner
func (d *DockerRuntime) StopRunner(containerID string) error {
	ctx := context.Background()

	// Stop container gracefully
	timeoutSeconds := 30
	stopOptions := container.StopOptions{
		Timeout: &timeoutSeconds,
	}
	if err := d.client.ContainerStop(ctx, containerID, stopOptions); err != nil {
		log.Printf("Failed to stop container %s gracefully: %v", containerID, err)
	}

	// Remove container
	if err := d.client.ContainerRemove(ctx, containerID, types.ContainerRemoveOptions{Force: true}); err != nil {
		return fmt.Errorf("failed to remove container: %w", err)
	}

	return nil
}

// StopAllRunners stops all runners for the specified architecture
func (d *DockerRuntime) StopAllRunners(architecture string) error {
	runners, err := d.GetRunningRunners(architecture)
	if err != nil {
		return fmt.Errorf("failed to get running runners: %w", err)
	}

	for _, runner := range runners {
		if err := d.StopRunner(runner.ID); err != nil {
			log.Printf("Failed to stop runner %s: %v", runner.Name, err)
		} else {
			log.Printf("Stopped runner %s", runner.Name)
		}
	}

	return nil
}

// CleanupOldRunners removes runners older than maxAge
func (d *DockerRuntime) CleanupOldRunners(architecture string, maxAge time.Duration) error {
	runners, err := d.GetRunningRunners(architecture)
	if err != nil {
		return fmt.Errorf("failed to get running runners: %w", err)
	}

	now := time.Now()
	for _, runner := range runners {
		if now.Sub(runner.CreatedAt) > maxAge {
			log.Printf("Cleaning up old runner %s (age: %v)", runner.Name, now.Sub(runner.CreatedAt))
			if err := d.StopRunner(runner.ID); err != nil {
				log.Printf("Failed to cleanup runner %s: %v", runner.Name, err)
			}
		}
	}

	return nil
}

// CleanupCompletedRunners removes all completed/stopped runners
func (d *DockerRuntime) CleanupCompletedRunners(architecture string) error {
	ctx := context.Background()
	
	// List all containers with the GitHub runner label
	filters := filters.NewArgs()
	filters.Add("label", "gha.runner.architecture="+architecture)
	
	containers, err := d.client.ContainerList(ctx, types.ContainerListOptions{
		All:     true, // Include stopped containers
		Filters: filters,
	})
	if err != nil {
		return fmt.Errorf("failed to list containers: %w", err)
	}
	
	cleanedCount := 0
	for _, container := range containers {
		// Only clean up exited/stopped containers
		if container.State == "exited" || container.State == "dead" {
			log.Printf("Cleaning up completed runner container: %s (status: %s)", 
				strings.TrimPrefix(container.Names[0], "/"), container.State)
			
			// Remove the container
			err := d.client.ContainerRemove(ctx, container.ID, types.ContainerRemoveOptions{
				Force: true,
			})
			if err != nil {
				log.Printf("Failed to remove completed container %s: %v", container.ID[:12], err)
			} else {
				cleanedCount++
			}
		}
	}
	
	if cleanedCount > 0 {
		log.Printf("Successfully cleaned up %d completed runner containers", cleanedCount)
	}
	
	return nil
}

// GetContainerLogs retrieves logs from a specific container
func (d *DockerRuntime) GetContainerLogs(containerID string, tail int) (string, error) {
	ctx := context.Background()

	options := types.ContainerLogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Tail:       fmt.Sprintf("%d", tail),
	}

	reader, err := d.client.ContainerLogs(ctx, containerID, options)
	if err != nil {
		return "", fmt.Errorf("failed to get container logs: %w", err)
	}
	defer reader.Close()

	logs, err := io.ReadAll(reader)
	if err != nil {
		return "", fmt.Errorf("failed to read logs: %w", err)
	}

	return string(logs), nil
}

// GetRunnerRepository determines which repository a runner is registered to
func (d *DockerRuntime) GetRunnerRepository(containerID string) (string, error) {
	ctx := context.Background()
	
	// Inspect the container to get its environment variables
	containerJSON, err := d.client.ContainerInspect(ctx, containerID)
	if err != nil {
		return "", fmt.Errorf("failed to inspect container: %w", err)
	}
	
	// Look for REPO_URL or REPO_FULL_NAME environment variables
	for _, env := range containerJSON.Config.Env {
		if strings.HasPrefix(env, "REPO_FULL_NAME=") {
			return strings.TrimPrefix(env, "REPO_FULL_NAME="), nil
		}
		if strings.HasPrefix(env, "REPO_URL=") {
			repoURL := strings.TrimPrefix(env, "REPO_URL=")
			// Extract repository full name from URL (e.g., "https://github.com/owner/repo" -> "owner/repo")
			if strings.HasPrefix(repoURL, "https://github.com/") {
				return strings.TrimPrefix(repoURL, "https://github.com/"), nil
			}
		}
	}
	
	return "", fmt.Errorf("repository information not found in container environment")
}

// CleanupImages removes unused Docker images while preserving intermediate images if configured
func (d *DockerRuntime) CleanupImages(config *config.Config) error {
	ctx := context.Background()

	if !config.CleanupImages {
		return nil // Image cleanup disabled
	}

	log.Printf("Starting Docker image cleanup (keep intermediate: %t, max age: %v, max unused: %d)", 
		config.KeepIntermediateImages, config.ImageMaxAge, config.MaxUnusedImages)

	// Get all images
	images, err := d.client.ImageList(ctx, types.ImageListOptions{All: true})
	if err != nil {
		return fmt.Errorf("failed to list images: %w", err)
	}

	// Get currently used images by containers
	usedImages := make(map[string]bool)
	containers, err := d.client.ContainerList(ctx, types.ContainerListOptions{All: true})
	if err != nil {
		log.Printf("Warning: failed to list containers for image cleanup: %v", err)
	} else {
		for _, container := range containers {
			usedImages[container.ImageID] = true
			usedImages[container.Image] = true
		}
	}

	// Separate images into categories
	var unusedImages []types.ImageSummary
	var finalImages []types.ImageSummary
	var intermediateImages []types.ImageSummary

	for _, image := range images {
		imageID := image.ID
		
		// Skip if image is currently in use
		if usedImages[imageID] {
			continue
		}
		
		// Check if any tag/repo name is in use
		inUse := false
		for _, repoTag := range image.RepoTags {
			if usedImages[repoTag] {
				inUse = true
				break
			}
		}
		if inUse {
			continue
		}

		// Categorize the image
		if len(image.RepoTags) == 0 || (len(image.RepoTags) == 1 && image.RepoTags[0] == "<none>:<none>") {
			// This is likely an intermediate image (no tags or <none>:<none>)
			intermediateImages = append(intermediateImages, image)
		} else {
			// This is a final/tagged image
			finalImages = append(finalImages, image)
		}
		
		unusedImages = append(unusedImages, image)
	}

	log.Printf("Found %d unused images (%d intermediate, %d final)", 
		len(unusedImages), len(intermediateImages), len(finalImages))

	// Clean up final images first (these are typically larger and more redundant)
	removedCount := 0
	
	// Remove old final images based on age
	for _, image := range finalImages {
		if removedCount >= config.MaxUnusedImages {
			break
		}
		
		imageAge := time.Since(time.Unix(image.Created, 0))
		if imageAge > config.ImageMaxAge {
			if err := d.removeImage(ctx, image.ID, false); err != nil {
				log.Printf("Failed to remove old final image %s: %v", image.ID[:12], err)
			} else {
				log.Printf("Removed old final image %s (age: %v)", image.ID[:12], imageAge)
				removedCount++
			}
		}
	}

	// If not keeping intermediate images, clean them up too
	if !config.KeepIntermediateImages {
		for _, image := range intermediateImages {
			if removedCount >= config.MaxUnusedImages {
				break
			}
			
			imageAge := time.Since(time.Unix(image.Created, 0))
			if imageAge > config.ImageMaxAge {
				if err := d.removeImage(ctx, image.ID, false); err != nil {
					log.Printf("Failed to remove old intermediate image %s: %v", image.ID[:12], err)
				} else {
					log.Printf("Removed old intermediate image %s (age: %v)", image.ID[:12], imageAge)
					removedCount++
				}
			}
		}
	}

	// If we still have too many unused images, remove the oldest final images
	if len(unusedImages) > config.MaxUnusedImages {
		// Sort final images by creation time (oldest first)
		// We'll remove the oldest final images beyond the limit
		excessFinalImages := len(finalImages) - (config.MaxUnusedImages - len(intermediateImages))
		if excessFinalImages > 0 && !config.KeepIntermediateImages {
			excessFinalImages = len(finalImages) - config.MaxUnusedImages
		}
		
		if excessFinalImages > 0 {
			// Sort by creation time (oldest first)
			for i := 0; i < len(finalImages)-1; i++ {
				for j := i + 1; j < len(finalImages); j++ {
					if finalImages[i].Created > finalImages[j].Created {
						finalImages[i], finalImages[j] = finalImages[j], finalImages[i]
					}
				}
			}
			
			// Remove excess final images
			for i := 0; i < excessFinalImages && i < len(finalImages); i++ {
				image := finalImages[i]
				if err := d.removeImage(ctx, image.ID, false); err != nil {
					log.Printf("Failed to remove excess final image %s: %v", image.ID[:12], err)
				} else {
					log.Printf("Removed excess final image %s", image.ID[:12])
					removedCount++
				}
			}
		}
	}

	log.Printf("Image cleanup completed: removed %d images", removedCount)
	return nil
}

// ensureImage pulls the Docker image if it doesn't exist locally
func (d *DockerRuntime) ensureImage(ctx context.Context, imageName string) error {
	// Check if image exists locally
	_, _, err := d.client.ImageInspectWithRaw(ctx, imageName)
	if err == nil {
		return nil // Image exists
	}

	log.Printf("Pulling Docker image: %s", imageName)
	
	// Pull image
	reader, err := d.client.ImagePull(ctx, imageName, types.ImagePullOptions{})
	if err != nil {
		return fmt.Errorf("failed to pull image: %w", err)
	}
	defer reader.Close()

	// Wait for pull to complete (simplified)
	// In production, you might want to parse the JSON stream for progress
	_, err = io.ReadAll(reader)
	if err != nil {
		return fmt.Errorf("failed to read pull response: %w", err)
	}

	log.Printf("Successfully pulled image: %s", imageName)
	return nil
}

// isRunnerContainer checks if a container is a runner container for the specified architecture
func (d *DockerRuntime) isRunnerContainer(container types.Container, architecture string) bool {
	// Check if container name matches runner pattern
	for _, name := range container.Names {
		name = strings.TrimPrefix(name, "/")
		if strings.HasPrefix(name, "runner-") && strings.HasSuffix(name, "-"+architecture) {
			return true
		}
	}
	return false
}

// removeImage safely removes a Docker image
func (d *DockerRuntime) removeImage(ctx context.Context, imageID string, force bool) error {
	_, err := d.client.ImageRemove(ctx, imageID, types.ImageRemoveOptions{
		Force:         force,
		PruneChildren: true,
	})
	return err
}
