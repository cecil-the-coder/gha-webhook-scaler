package runtime

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/cecil-the-coder/gha-webhook-scaler/internal/config"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// MockDockerClient is a mock implementation of DockerClientInterface
type MockDockerClient struct {
	mock.Mock
}

func (m *MockDockerClient) Ping(ctx context.Context) (types.Ping, error) {
	args := m.Called(ctx)
	return args.Get(0).(types.Ping), args.Error(1)
}

func (m *MockDockerClient) ContainerCreate(ctx context.Context, config *container.Config, hostConfig *container.HostConfig, networkingConfig *network.NetworkingConfig, platform *v1.Platform, containerName string) (container.CreateResponse, error) {
	args := m.Called(ctx, config, hostConfig, networkingConfig, platform, containerName)
	return args.Get(0).(container.CreateResponse), args.Error(1)
}

func (m *MockDockerClient) ContainerStart(ctx context.Context, containerID string, options types.ContainerStartOptions) error {
	args := m.Called(ctx, containerID, options)
	return args.Error(0)
}

func (m *MockDockerClient) ContainerRemove(ctx context.Context, containerID string, options types.ContainerRemoveOptions) error {
	args := m.Called(ctx, containerID, options)
	return args.Error(0)
}

func (m *MockDockerClient) ContainerList(ctx context.Context, options types.ContainerListOptions) ([]types.Container, error) {
	args := m.Called(ctx, options)
	return args.Get(0).([]types.Container), args.Error(1)
}

func (m *MockDockerClient) ContainerStop(ctx context.Context, containerID string, options container.StopOptions) error {
	args := m.Called(ctx, containerID, options)
	return args.Error(0)
}

func (m *MockDockerClient) ContainerLogs(ctx context.Context, containerID string, options types.ContainerLogsOptions) (io.ReadCloser, error) {
	args := m.Called(ctx, containerID, options)
	return args.Get(0).(io.ReadCloser), args.Error(1)
}

func (m *MockDockerClient) ContainerInspect(ctx context.Context, containerID string) (types.ContainerJSON, error) {
	args := m.Called(ctx, containerID)
	return args.Get(0).(types.ContainerJSON), args.Error(1)
}

func (m *MockDockerClient) ImageList(ctx context.Context, options types.ImageListOptions) ([]types.ImageSummary, error) {
	args := m.Called(ctx, options)
	return args.Get(0).([]types.ImageSummary), args.Error(1)
}

func (m *MockDockerClient) ImageInspectWithRaw(ctx context.Context, imageID string) (types.ImageInspect, []byte, error) {
	args := m.Called(ctx, imageID)
	return args.Get(0).(types.ImageInspect), args.Get(1).([]byte), args.Error(2)
}

func (m *MockDockerClient) ImagePull(ctx context.Context, refStr string, options types.ImagePullOptions) (io.ReadCloser, error) {
	args := m.Called(ctx, refStr, options)
	return args.Get(0).(io.ReadCloser), args.Error(1)
}

func (m *MockDockerClient) ImageRemove(ctx context.Context, imageID string, options types.ImageRemoveOptions) ([]types.ImageDeleteResponseItem, error) {
	args := m.Called(ctx, imageID, options)
	return args.Get(0).([]types.ImageDeleteResponseItem), args.Error(1)
}

// MockReadCloser implements io.ReadCloser for testing
type MockReadCloser struct {
	*strings.Reader
}

func (m *MockReadCloser) Close() error {
	return nil
}

func NewMockReadCloser(content string) *MockReadCloser {
	return &MockReadCloser{strings.NewReader(content)}
}

func TestDockerRuntime_StartRunner(t *testing.T) {
	mockClient := new(MockDockerClient)
	runtime := NewDockerRuntimeWithClient(mockClient)

	cfg := &config.Config{
		RunnerImage:   "test-image:latest",
		Architecture:  "amd64",
		CacheVolumes:  true,
		RunnerLabels:  []string{"linux", "docker"},
	}

	ownerConfig := &config.OwnerConfig{
		Owner:       "testowner",
		GitHubToken: "test-token",
		RepoName:    "test-repo",
	}

	// Mock image inspection to simulate existing image
	mockClient.On("ImageInspectWithRaw", mock.Anything, "test-image:latest").Return(
		types.ImageInspect{ID: "image123"}, []byte{}, nil,
	)

	// Mock container creation
	mockClient.On("ContainerCreate", 
		mock.Anything, 
		mock.AnythingOfType("*container.Config"),
		mock.AnythingOfType("*container.HostConfig"),
		mock.Anything,
		mock.AnythingOfType("*v1.Platform"),
		"test-runner",
	).Return(container.CreateResponse{ID: "container123"}, nil)

	// Mock container start
	mockClient.On("ContainerStart", mock.Anything, "container123", mock.Anything).Return(nil)

	// Test successful runner start
	runner, err := runtime.StartRunnerWithLabelsForOwner(cfg, ownerConfig, "test-runner", "testowner/test-repo", []string{"linux", "docker"})

	assert.NoError(t, err)
	assert.NotNil(t, runner)
	assert.Equal(t, "container123", runner.ID)
	assert.Equal(t, "test-runner", runner.Name)
	assert.Equal(t, "running", runner.Status)
	assert.Equal(t, "amd64", runner.Architecture)

	mockClient.AssertExpectations(t)
}

func TestDockerRuntime_StartRunner_CreateFailure(t *testing.T) {
	mockClient := new(MockDockerClient)
	runtime := NewDockerRuntimeWithClient(mockClient)

	cfg := &config.Config{
		RunnerImage:  "test-image:latest",
		Architecture: "amd64",
	}

	ownerConfig := &config.OwnerConfig{
		Owner:       "testowner",
		GitHubToken: "test-token",
		RepoName:    "test-repo",
	}

	// Mock image inspection
	mockClient.On("ImageInspectWithRaw", mock.Anything, "test-image:latest").Return(
		types.ImageInspect{ID: "image123"}, []byte{}, nil,
	)

	// Mock container creation failure
	mockClient.On("ContainerCreate", 
		mock.Anything, 
		mock.AnythingOfType("*container.Config"),
		mock.AnythingOfType("*container.HostConfig"),
		mock.Anything,
		mock.AnythingOfType("*v1.Platform"),
		"test-runner",
	).Return(container.CreateResponse{}, errors.New("creation failed"))

	runner, err := runtime.StartRunnerWithLabelsForOwner(cfg, ownerConfig, "test-runner", "testowner/test-repo", []string{"linux"})

	assert.Error(t, err)
	assert.Nil(t, runner)
	assert.Contains(t, err.Error(), "failed to create container")

	mockClient.AssertExpectations(t)
}

func TestDockerRuntime_StartRunner_StartFailure(t *testing.T) {
	mockClient := new(MockDockerClient)
	runtime := NewDockerRuntimeWithClient(mockClient)

	cfg := &config.Config{
		RunnerImage:  "test-image:latest",
		Architecture: "amd64",
	}

	ownerConfig := &config.OwnerConfig{
		Owner:       "testowner",
		GitHubToken: "test-token",
		RepoName:    "test-repo",
	}

	// Mock image inspection
	mockClient.On("ImageInspectWithRaw", mock.Anything, "test-image:latest").Return(
		types.ImageInspect{ID: "image123"}, []byte{}, nil,
	)

	// Mock container creation
	mockClient.On("ContainerCreate", 
		mock.Anything, 
		mock.AnythingOfType("*container.Config"),
		mock.AnythingOfType("*container.HostConfig"),
		mock.Anything,
		mock.AnythingOfType("*v1.Platform"),
		"test-runner",
	).Return(container.CreateResponse{ID: "container123"}, nil)

	// Mock container start failure
	mockClient.On("ContainerStart", mock.Anything, "container123", mock.Anything).Return(errors.New("start failed"))

	// Mock container removal for cleanup
	mockClient.On("ContainerRemove", mock.Anything, "container123", mock.Anything).Return(nil)

	runner, err := runtime.StartRunnerWithLabelsForOwner(cfg, ownerConfig, "test-runner", "testowner/test-repo", []string{"linux"})

	assert.Error(t, err)
	assert.Nil(t, runner)
	assert.Contains(t, err.Error(), "failed to start container")

	mockClient.AssertExpectations(t)
}

func TestDockerRuntime_GetRunningRunners(t *testing.T) {
	mockClient := new(MockDockerClient)
	runtime := NewDockerRuntimeWithClient(mockClient)

	// Mock container list
	containers := []types.Container{
		{
			ID:    "container1",
			Names: []string{"/runner-12345-amd64"},
			State: "running",
			Labels: map[string]string{
				"gha.runner.architecture": "amd64",
				"gha.runner.repository":   "testowner/testrepo",
			},
			Created: time.Now().Unix(),
		},
		{
			ID:    "container2", 
			Names: []string{"/other-container"},
			State: "running",
		},
	}

	mockClient.On("ContainerList", mock.Anything, mock.Anything).Return(containers, nil)

	runners, err := runtime.GetRunningRunners("amd64")

	assert.NoError(t, err)
	assert.Len(t, runners, 1)
	assert.Equal(t, "container1", runners[0].ID)
	assert.Equal(t, "runner-12345-amd64", runners[0].Name)

	mockClient.AssertExpectations(t)
}

func TestDockerRuntime_GetRunningRunners_ListFailure(t *testing.T) {
	mockClient := new(MockDockerClient)
	runtime := NewDockerRuntimeWithClient(mockClient)

	mockClient.On("ContainerList", mock.Anything, mock.Anything).Return([]types.Container{}, errors.New("list failed"))

	runners, err := runtime.GetRunningRunners("amd64")

	assert.Error(t, err)
	assert.Nil(t, runners)
	assert.Contains(t, err.Error(), "failed to list containers")

	mockClient.AssertExpectations(t)
}

func TestDockerRuntime_CleanupOldRunners(t *testing.T) {
	mockClient := new(MockDockerClient)
	runtime := NewDockerRuntimeWithClient(mockClient)

	// Mock old containers
	oldTime := time.Now().Add(-2 * time.Hour).Unix()
	containers := []types.Container{
		{
			ID:    "old-container",
			Names: []string{"/runner-old-amd64"},
			State: "running",
			Labels: map[string]string{
				"gha.runner.architecture": "amd64",
			},
			Created: oldTime,
		},
	}

	mockClient.On("ContainerList", mock.Anything, mock.Anything).Return(containers, nil)
	mockClient.On("ContainerStop", mock.Anything, "old-container", mock.Anything).Return(nil)
	mockClient.On("ContainerRemove", mock.Anything, "old-container", mock.Anything).Return(nil)

	err := runtime.CleanupOldRunners("amd64", time.Hour)

	assert.NoError(t, err)
	mockClient.AssertExpectations(t)
}

func TestDockerRuntime_GetContainerLogs(t *testing.T) {
	mockClient := new(MockDockerClient)
	runtime := NewDockerRuntimeWithClient(mockClient)

	expectedLogs := "test log content"
	mockReadCloser := NewMockReadCloser(expectedLogs)

	mockClient.On("ContainerLogs", mock.Anything, "container123", mock.Anything).Return(mockReadCloser, nil)

	logs, err := runtime.GetContainerLogs("container123", 100)

	assert.NoError(t, err)
	assert.Equal(t, expectedLogs, logs)

	mockClient.AssertExpectations(t)
}

func TestDockerRuntime_GetContainerLogs_Failure(t *testing.T) {
	mockClient := new(MockDockerClient)
	runtime := NewDockerRuntimeWithClient(mockClient)

	mockClient.On("ContainerLogs", mock.Anything, "container123", mock.Anything).Return(&MockReadCloser{}, errors.New("logs failed"))

	logs, err := runtime.GetContainerLogs("container123", 100)

	assert.Error(t, err)
	assert.Empty(t, logs)
	assert.Contains(t, err.Error(), "failed to get container logs")

	mockClient.AssertExpectations(t)
}

func TestDockerRuntime_GetRunnerRepository(t *testing.T) {
	mockClient := new(MockDockerClient)
	runtime := NewDockerRuntimeWithClient(mockClient)

	containerJSON := types.ContainerJSON{
		Config: &container.Config{
			Env: []string{
				"REPO_FULL_NAME=testowner/testrepo",
				"ACCESS_TOKEN=token123",
			},
		},
	}

	mockClient.On("ContainerInspect", mock.Anything, "container123").Return(containerJSON, nil)

	repo, err := runtime.GetRunnerRepository("container123")

	assert.NoError(t, err)
	assert.Equal(t, "testowner/testrepo", repo)

	mockClient.AssertExpectations(t)
}

func TestDockerRuntime_CleanupImages(t *testing.T) {
	mockClient := new(MockDockerClient)
	runtime := NewDockerRuntimeWithClient(mockClient)

	cfg := &config.Config{
		CleanupImages: true,
		RunnerImage:   "test-image:latest",
	}

	// Mock image list
	images := []types.ImageSummary{
		{
			ID:       "sha256:abcdef123456789012345678901234567890abcdef123456789012345678901234",
			RepoTags: []string{"old-runner:v1"},
			Created:  time.Now().Add(-48 * time.Hour).Unix(),
		},
		{
			ID:       "sha256:fedcba098765432109876543210987654321fedcba098765432109876543210987", 
			RepoTags: []string{"test-image:latest"},
			Created:  time.Now().Unix(),
		},
	}

	containers := []types.Container{
		{
			ID:      "container1",
			ImageID: "sha256:fedcba098765432109876543210987654321fedcba098765432109876543210987",
		},
	}

	mockClient.On("ImageList", mock.Anything, mock.Anything).Return(images, nil)
	mockClient.On("ContainerList", mock.Anything, mock.Anything).Return(containers, nil)
	mockClient.On("ImageRemove", mock.Anything, "sha256:abcdef123456789012345678901234567890abcdef123456789012345678901234", mock.Anything).Return([]types.ImageDeleteResponseItem{}, nil)

	err := runtime.CleanupImages(cfg)

	assert.NoError(t, err)
	mockClient.AssertExpectations(t)
}

func TestDockerRuntime_StopAllRunners(t *testing.T) {
	mockClient := new(MockDockerClient)
	runtime := NewDockerRuntimeWithClient(mockClient)

	containers := []types.Container{
		{
			ID:    "runner1",
			Names: []string{"/runner-1-amd64"},
			Labels: map[string]string{
				"gha.runner.architecture": "amd64",
			},
		},
		{
			ID:    "runner2",
			Names: []string{"/runner-2-amd64"},
			Labels: map[string]string{
				"gha.runner.architecture": "amd64",
			},
		},
	}

	mockClient.On("ContainerList", mock.Anything, mock.Anything).Return(containers, nil)
	mockClient.On("ContainerStop", mock.Anything, "runner1", mock.Anything).Return(nil)
	mockClient.On("ContainerStop", mock.Anything, "runner2", mock.Anything).Return(nil)
	mockClient.On("ContainerRemove", mock.Anything, "runner1", mock.Anything).Return(nil)
	mockClient.On("ContainerRemove", mock.Anything, "runner2", mock.Anything).Return(nil)

	err := runtime.StopAllRunners("amd64")

	assert.NoError(t, err)

	mockClient.AssertExpectations(t)
}

func TestDockerRuntime_HealthCheck(t *testing.T) {
	mockClient := new(MockDockerClient)
	runtime := NewDockerRuntimeWithClient(mockClient)

	mockClient.On("Ping", mock.Anything).Return(types.Ping{}, nil)

	err := runtime.HealthCheck()

	assert.NoError(t, err)
	mockClient.AssertExpectations(t)
}

func TestDockerRuntime_HealthCheck_Failure(t *testing.T) {
	mockClient := new(MockDockerClient)
	runtime := NewDockerRuntimeWithClient(mockClient)

	mockClient.On("Ping", mock.Anything).Return(types.Ping{}, errors.New("ping failed"))

	err := runtime.HealthCheck()

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "Docker daemon not accessible")

	mockClient.AssertExpectations(t)
}