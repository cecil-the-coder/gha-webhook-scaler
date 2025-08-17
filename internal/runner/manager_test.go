package runner

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/cecil-the-coder/gha-webhook-scaler/internal/config"
	"github.com/cecil-the-coder/gha-webhook-scaler/internal/github"
	"github.com/cecil-the-coder/gha-webhook-scaler/internal/runtime"
)

// MockContainerRuntime is a mock implementation of ContainerRuntime for testing
type MockContainerRuntime struct {
	runners          []runtime.RunnerContainer
	startRunnerError error
	healthCheckError error
	shouldFailStart  bool
}

func (m *MockContainerRuntime) GetRuntimeType() config.RuntimeType {
	return config.RuntimeTypeDocker
}

func (m *MockContainerRuntime) HealthCheck() error {
	return m.healthCheckError
}

func (m *MockContainerRuntime) StartRunner(config *config.Config, runnerName, repoFullName string) (*runtime.RunnerContainer, error) {
	return m.StartRunnerWithLabels(config, runnerName, repoFullName, config.RunnerLabels)
}

func (m *MockContainerRuntime) StartRunnerWithLabels(config *config.Config, runnerName, repoFullName string, labels []string) (*runtime.RunnerContainer, error) {
	if m.shouldFailStart || m.startRunnerError != nil {
		return nil, m.startRunnerError
	}

	container := &runtime.RunnerContainer{
		ID:   "mock-container-" + runnerName,
		Name: runnerName,
	}
	m.runners = append(m.runners, *container)
	return container, nil
}

func (m *MockContainerRuntime) StartRunnerWithLabelsForOwner(config *config.Config, ownerConfig *config.OwnerConfig, runnerName, repoFullName string, labels []string) (*runtime.RunnerContainer, error) {
	return m.StartRunnerWithLabels(config, runnerName, repoFullName, labels)
}

func (m *MockContainerRuntime) GetRunningRunners(architecture string) ([]runtime.RunnerContainer, error) {
	return m.runners, nil
}

func (m *MockContainerRuntime) StopAllRunners(architecture string) error {
	m.runners = []runtime.RunnerContainer{}
	return nil
}

func (m *MockContainerRuntime) CleanupOldRunners(architecture string, timeout time.Duration) error {
	return nil
}

func (m *MockContainerRuntime) CleanupCompletedRunners(architecture string) error {
	return nil
}

func (m *MockContainerRuntime) CleanupImages(config *config.Config) error {
	return nil
}

func (m *MockContainerRuntime) GetRunnerRepository(containerID string) (string, error) {
	return "test-owner/test-repo", nil
}

func (m *MockContainerRuntime) StopRunner(containerID string) error {
	// Remove runner from list
	for i, runner := range m.runners {
		if runner.ID == containerID {
			m.runners = append(m.runners[:i], m.runners[i+1:]...)
			break
		}
	}
	return nil
}

func (m *MockContainerRuntime) GetContainerLogs(containerID string, tail int) (string, error) {
	return "mock logs for " + containerID, nil
}

// MockGitHubClient is a mock implementation for testing
type MockGitHubClient struct {
	queuedLabels []string
	shouldError  bool
}

func (m *MockGitHubClient) GetQueuedJobLabels(owner, repo string) ([]string, error) {
	if m.shouldError {
		return nil, errors.New("mock error")
	}
	return m.queuedLabels, nil
}

func TestNewManager(t *testing.T) {
	cfg := &config.Config{
		MaxRunners: 5,
		Owners: map[string]*config.OwnerConfig{
			"test-owner": {
				Owner:       "test-owner",
				GitHubToken: "test-token",
			},
		},
	}

	githubClients := map[string]*github.Client{
		"test-owner": github.NewClient("test-token"),
	}

	mockRuntime := &MockContainerRuntime{}

	manager := NewManager(cfg, githubClients, mockRuntime)

	if manager.config != cfg {
		t.Error("NewManager() did not set config correctly")
	}

	if manager.runtime != mockRuntime {
		t.Error("NewManager() did not set runtime correctly")
	}

	if manager.metrics == nil {
		t.Error("NewManager() did not initialize metrics")
	}
}

func TestManager_ScaleUp(t *testing.T) {
	tests := []struct {
		name           string
		repoFullName   string
		repoName       string
		maxRunners     int
		currentRunners int
		wantError      bool
		shouldFailStart bool
	}{
		{
			name:           "successful scale up",
			repoFullName:   "test-owner/test-repo",
			repoName:       "test-repo",
			maxRunners:     5,
			currentRunners: 2,
			wantError:      false,
			shouldFailStart: false,
		},
		{
			name:           "at max runners",
			repoFullName:   "test-owner/test-repo",
			repoName:       "test-repo",
			maxRunners:     2,
			currentRunners: 2,
			wantError:      false, // Not an error, just won't scale
			shouldFailStart: false,
		},
		{
			name:           "runtime start fails",
			repoFullName:   "test-owner/test-repo",
			repoName:       "test-repo",
			maxRunners:     5,
			currentRunners: 1,
			wantError:      true,
			shouldFailStart: true,
		},
		{
			name:         "unauthorized repository",
			repoFullName: "unauthorized/repo",
			repoName:     "repo",
			maxRunners:   5,
			wantError:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{
				MaxRunners:   tt.maxRunners,
				Architecture: "amd64",
				RunnerLabels: []string{"self-hosted", "amd64"},
				Owners: map[string]*config.OwnerConfig{
					"test-owner": {
						Owner:       "test-owner",
						GitHubToken: "test-token",
						AllRepos:    true,
					},
				},
			}

			githubClients := map[string]*github.Client{
				"test-owner": github.NewClient("test-token"),
			}

			mockRuntime := &MockContainerRuntime{
				shouldFailStart: tt.shouldFailStart,
			}
			if tt.shouldFailStart {
				mockRuntime.startRunnerError = errors.New("mock start error")
			}

			// Pre-populate with current runners
			for i := 0; i < tt.currentRunners; i++ {
				mockRuntime.runners = append(mockRuntime.runners, runtime.RunnerContainer{
					ID:   "existing-" + string(rune(i)),
					Name: "existing-runner-" + string(rune(i)),
				})
			}

			manager := NewManager(cfg, githubClients, mockRuntime)

			err := manager.ScaleUp("test", tt.repoFullName, tt.repoName)

			if tt.wantError {
				if err == nil {
					t.Error("Expected error but got none")
				}
			} else {
				if err != nil {
					t.Errorf("Unexpected error: %v", err)
				}
			}
		})
	}
}

func TestManager_ScaleUpWithLabels(t *testing.T) {
	cfg := &config.Config{
		MaxRunners:   5,
		Architecture: "amd64",
		RunnerLabels: []string{"self-hosted", "amd64"},
		Owners: map[string]*config.OwnerConfig{
			"test-owner": {
				Owner:       "test-owner",
				GitHubToken: "test-token",
				AllRepos:    true,
			},
		},
	}

	githubClients := map[string]*github.Client{
		"test-owner": github.NewClient("test-token"),
	}

	mockRuntime := &MockContainerRuntime{}
	manager := NewManager(cfg, githubClients, mockRuntime)

	customLabels := []string{"custom-label", "amd64"}
	err := manager.ScaleUpWithLabels("test", "test-owner/test-repo", "test-repo", customLabels)

	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}

	if len(mockRuntime.runners) != 1 {
		t.Errorf("Expected 1 runner, got %d", len(mockRuntime.runners))
	}

	// Check that metrics were updated
	if manager.metrics.RunnersStarted != 1 {
		t.Errorf("Expected RunnersStarted to be 1, got %d", manager.metrics.RunnersStarted)
	}

	if manager.metrics.ScaleUpEvents != 1 {
		t.Errorf("Expected ScaleUpEvents to be 1, got %d", manager.metrics.ScaleUpEvents)
	}
}

func TestManager_ScaleDown(t *testing.T) {
	cfg := &config.Config{
		MaxRunners:   5,
		Architecture: "amd64",
		Owners: map[string]*config.OwnerConfig{
			"test-owner": {
				Owner:       "test-owner",
				GitHubToken: "test-token",
			},
		},
	}

	githubClients := map[string]*github.Client{
		"test-owner": github.NewClient("test-token"),
	}

	mockRuntime := &MockContainerRuntime{
		runners: []runtime.RunnerContainer{
			{ID: "runner1", Name: "test-runner-1"},
			{ID: "runner2", Name: "test-runner-2"},
		},
	}

	manager := NewManager(cfg, githubClients, mockRuntime)

	err := manager.ScaleDown("test")

	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}

	// For ephemeral runners, scale down doesn't forcefully stop runners
	// Check that metrics were updated
	if manager.metrics.ScaleDownEvents != 1 {
		t.Errorf("Expected ScaleDownEvents to be 1, got %d", manager.metrics.ScaleDownEvents)
	}
}

func TestManager_HandleWorkflowQueued(t *testing.T) {
	cfg := &config.Config{
		MaxRunners:   5,
		Architecture: "amd64",
		RunnerLabels: []string{"self-hosted", "amd64"},
		Owners: map[string]*config.OwnerConfig{
			"test-owner": {
				Owner:       "test-owner",
				GitHubToken: "test-token",
				AllRepos:    true,
			},
		},
	}

	githubClients := map[string]*github.Client{
		"test-owner": github.NewClient("test-token"),
	}

	mockRuntime := &MockContainerRuntime{}
	manager := NewManager(cfg, githubClients, mockRuntime)

	err := manager.HandleWorkflowQueued("test-owner/test-repo", "test-repo")

	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}

	if len(mockRuntime.runners) != 1 {
		t.Errorf("Expected 1 runner after workflow queued, got %d", len(mockRuntime.runners))
	}
}

func TestManager_HandleWorkflowCompleted(t *testing.T) {
	cfg := &config.Config{
		MaxRunners: 5,
		Owners: map[string]*config.OwnerConfig{
			"test-owner": {
				Owner:       "test-owner",
				GitHubToken: "test-token",
			},
		},
	}

	githubClients := map[string]*github.Client{
		"test-owner": github.NewClient("test-token"),
	}

	mockRuntime := &MockContainerRuntime{}
	manager := NewManager(cfg, githubClients, mockRuntime)

	err := manager.HandleWorkflowCompleted("test-owner/test-repo", "test-repo")

	// Should not return error - ephemeral runners handle cleanup automatically
	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}
}

func TestManager_GetStatus(t *testing.T) {
	cfg := &config.Config{
		MaxRunners:   5,
		Architecture: "amd64",
		RunnerImage:  "test-image",
		CacheVolumes: true,
		Owners: map[string]*config.OwnerConfig{
			"test-owner": {
				Owner:       "test-owner",
				GitHubToken: "test-token",
				AllRepos:    true,
			},
		},
	}

	githubClients := map[string]*github.Client{
		"test-owner": github.NewClient("test-token"),
	}

	mockRuntime := &MockContainerRuntime{
		runners: []runtime.RunnerContainer{
			{ID: "runner1", Name: "test-runner-1"},
			{ID: "runner2", Name: "test-runner-2"},
		},
	}

	manager := NewManager(cfg, githubClients, mockRuntime)

	status := manager.GetStatus()

	if status["status"] != "healthy" {
		t.Errorf("Expected status to be 'healthy', got %v", status["status"])
	}

	runners := status["runners"].(map[string]interface{})
	if runners["current"] != 2 {
		t.Errorf("Expected current runners to be 2, got %v", runners["current"])
	}

	if runners["max"] != 5 {
		t.Errorf("Expected max runners to be 5, got %v", runners["max"])
	}
}

func TestManager_GetMetrics(t *testing.T) {
	cfg := &config.Config{
		MaxRunners:   5,
		Architecture: "amd64",
		Owners: map[string]*config.OwnerConfig{
			"test-owner": {
				Owner:       "test-owner",
				GitHubToken: "test-token",
			},
		},
	}

	githubClients := map[string]*github.Client{
		"test-owner": github.NewClient("test-token"),
	}

	mockRuntime := &MockContainerRuntime{
		runners: []runtime.RunnerContainer{
			{ID: "runner1", Name: "test-runner-1"},
		},
	}

	manager := NewManager(cfg, githubClients, mockRuntime)

	// Set some metrics
	manager.metrics.RunnersStarted = 5
	manager.metrics.RunnersStopped = 2
	manager.metrics.WebhooksReceived = 10

	metrics := manager.GetMetrics()

	if metrics.RunnersStarted != 5 {
		t.Errorf("Expected RunnersStarted to be 5, got %d", metrics.RunnersStarted)
	}

	if metrics.RunnersStopped != 2 {
		t.Errorf("Expected RunnersStopped to be 2, got %d", metrics.RunnersStopped)
	}

	if metrics.WebhooksReceived != 10 {
		t.Errorf("Expected WebhooksReceived to be 10, got %d", metrics.WebhooksReceived)
	}

	if metrics.CurrentRunners != 1 {
		t.Errorf("Expected CurrentRunners to be 1, got %d", metrics.CurrentRunners)
	}
}

func TestManager_generateRunnerName(t *testing.T) {
	tests := []struct {
		name         string
		owner        string
		repoName     string
		architecture string
		wantContains []string
	}{
		{
			name:         "with owner and repo",
			owner:        "testowner",
			repoName:     "testrepo",
			architecture: "amd64",
			wantContains: []string{"runner-", "testow", "testrepo", "amd64"},
		},
		{
			name:         "with long owner and repo names",
			owner:        "verylongownernamethatexceedslimit",
			repoName:     "verylongreponamethatexceedslimit",
			architecture: "amd64",
			wantContains: []string{"runner-", "amd64"},
		},
		{
			name:         "with repo only",
			owner:        "",
			repoName:     "testrepo",
			architecture: "arm64",
			wantContains: []string{"runner-", "testrepo", "arm64"},
		},
		{
			name:         "with neither owner nor repo",
			owner:        "",
			repoName:     "",
			architecture: "amd64",
			wantContains: []string{"runner-", "amd64"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{
				Architecture: tt.architecture,
				Owners: map[string]*config.OwnerConfig{
					"test-owner": {
						Owner: "test-owner",
					},
				},
			}

			manager := NewManager(cfg, map[string]*github.Client{}, &MockContainerRuntime{})

			runnerName := manager.generateRunnerName(tt.owner, tt.repoName)

			// Check that the runner name contains expected parts
			for _, want := range tt.wantContains {
				if !strings.Contains(runnerName, want) {
					t.Errorf("generateRunnerName() = %v, want to contain %v", runnerName, want)
				}
			}

			// Check name length doesn't exceed Kubernetes limits (63 chars)
			if len(runnerName) > 63 {
				t.Errorf("generateRunnerName() length = %d, want <= 63", len(runnerName))
			}
		})
	}
}

func TestManager_IncrementWebhookCount(t *testing.T) {
	cfg := &config.Config{
		Owners: map[string]*config.OwnerConfig{
			"test-owner": {
				Owner: "test-owner",
			},
		},
	}

	manager := NewManager(cfg, map[string]*github.Client{}, &MockContainerRuntime{})

	initialCount := manager.metrics.WebhooksReceived

	manager.IncrementWebhookCount()

	if manager.metrics.WebhooksReceived != initialCount+1 {
		t.Errorf("IncrementWebhookCount() did not increment count correctly. Got %d, want %d", 
			manager.metrics.WebhooksReceived, initialCount+1)
	}
}

func TestManager_validateArchitectureLabels(t *testing.T) {
	tests := []struct {
		name         string
		hostArch     string
		inputLabels  []string
		wantLabels   []string
	}{
		{
			name:        "amd64 host with x64 label",
			hostArch:    "amd64",
			inputLabels: []string{"self-hosted", "x64"},
			wantLabels:  []string{"self-hosted", "x64"},
		},
		{
			name:        "amd64 host with arm64 label should replace",
			hostArch:    "amd64",
			inputLabels: []string{"self-hosted", "arm64"},
			wantLabels:  []string{"self-hosted", "amd64", "x64"},
		},
		{
			name:        "arm64 host with arm64 label",
			hostArch:    "arm64",
			inputLabels: []string{"self-hosted", "arm64"},
			wantLabels:  []string{"self-hosted", "arm64"},
		},
		{
			name:        "no architecture labels",
			hostArch:    "amd64",
			inputLabels: []string{"self-hosted", "linux"},
			wantLabels:  []string{"self-hosted", "linux", "amd64", "x64"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{
				Architecture: tt.hostArch,
				Owners: map[string]*config.OwnerConfig{
					"test-owner": {
						Owner: "test-owner",
					},
				},
			}

			manager := NewManager(cfg, map[string]*github.Client{}, &MockContainerRuntime{})

			result := manager.validateArchitectureLabels(tt.inputLabels)

			// Check that all expected labels are present
			for _, wantLabel := range tt.wantLabels {
				found := false
				for _, resultLabel := range result {
					if resultLabel == wantLabel {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("validateArchitectureLabels() = %v, want to contain %v", result, wantLabel)
				}
			}
		})
	}
}