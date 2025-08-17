package runtime

import (
	"time"

	"github.com/cecil-the-coder/gha-webhook-scaler/internal/config"
)

// RunnerContainer represents a running GitHub Actions runner container
type RunnerContainer struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Status       string    `json:"status"`
	CreatedAt    time.Time `json:"created_at"`
	Architecture string    `json:"architecture"`
}

// ContainerRuntime defines the interface for container runtime implementations
type ContainerRuntime interface {
	// StartRunner starts a new runner with default labels
	StartRunner(config *config.Config, runnerName, repoFullName string) (*RunnerContainer, error)
	
	// StartRunnerWithLabels starts a new runner with specific labels
	StartRunnerWithLabels(config *config.Config, runnerName, repoFullName string, labels []string) (*RunnerContainer, error)
	
	// StartRunnerWithLabelsForOwner starts a new runner with labels for a specific owner
	StartRunnerWithLabelsForOwner(config *config.Config, ownerConfig *config.OwnerConfig, runnerName, repoFullName string, labels []string) (*RunnerContainer, error)
	
	// GetRunningRunners returns all running runners for the specified architecture
	GetRunningRunners(architecture string) ([]RunnerContainer, error)
	
	// StopRunner stops a specific runner
	StopRunner(containerID string) error
	
	// StopAllRunners stops all runners for the specified architecture
	StopAllRunners(architecture string) error
	
	// CleanupOldRunners removes runners older than maxAge
	CleanupOldRunners(architecture string, maxAge time.Duration) error
	
	// CleanupCompletedRunners removes all completed/succeeded runners
	CleanupCompletedRunners(architecture string) error
	
	// GetContainerLogs retrieves logs from a specific container
	GetContainerLogs(containerID string, tail int) (string, error)
	
	// GetRunnerRepository determines which repository a runner is registered to
	GetRunnerRepository(containerID string) (string, error)
	
	// CleanupImages removes unused images (Docker-specific, may be no-op for other runtimes)
	CleanupImages(config *config.Config) error
	
	// GetRuntimeType returns the type of this runtime
	GetRuntimeType() config.RuntimeType
	
	// HealthCheck verifies the runtime is available and accessible
	HealthCheck() error
}

// NewContainerRuntime creates a new container runtime based on the specified type
func NewContainerRuntime(runtimeType config.RuntimeType, cfg *config.Config) (ContainerRuntime, error) {
	switch runtimeType {
	case config.RuntimeTypeDocker:
		return NewDockerRuntime()
	case config.RuntimeTypeKubernetes:
		return NewKubernetesRuntimeWithConfig(cfg)
	default:
		return NewDockerRuntime() // Default to Docker for backward compatibility
	}
}
