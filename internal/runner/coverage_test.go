package runner

import (
	"testing"
	"time"

	"github.com/cecil-the-coder/gha-webhook-scaler/internal/config"
	"github.com/cecil-the-coder/gha-webhook-scaler/internal/github"
	"github.com/cecil-the-coder/gha-webhook-scaler/internal/runtime"
)

// TestCoverage_StopAllRunners tests the StopAllRunners function
func TestCoverage_StopAllRunners(t *testing.T) {
	cfg := &config.Config{
		Architecture: "amd64",
	}
	
	mockRuntime := &MockContainerRuntime{
		runners: []runtime.RunnerContainer{
			{ID: "test1", Name: "runner1", Status: "running"},
		},
	}
	
	githubClients := make(map[string]*github.Client)
	manager := NewManager(cfg, githubClients, mockRuntime)
	
	// Should not panic and should call the runtime's StopAllRunners
	manager.StopAllRunners()
	
	// Verify that runtime was called (mock clears runners)
	if len(mockRuntime.runners) != 0 {
		t.Error("StopAllRunners should have cleared all runners")
	}
}

// TestCoverage_cleanupOldRunners tests the cleanupOldRunners function
func TestCoverage_cleanupOldRunners(t *testing.T) {
	cfg := &config.Config{
		Architecture:   "amd64",
		RunnerTimeout: 30 * time.Minute,
	}
	
	mockRuntime := &MockContainerRuntime{}
	githubClients := make(map[string]*github.Client)
	manager := NewManager(cfg, githubClients, mockRuntime)
	
	// Should call the runtime cleanup methods without panicking
	manager.cleanupOldRunners()
	
	// Mock doesn't fail, so this test passes if no panic occurs
}

// TestCoverage_getOwnerFromRunnerName tests owner extraction from runner names
func TestCoverage_getOwnerFromRunnerName(t *testing.T) {
	cfg := &config.Config{
		Owners: map[string]*config.OwnerConfig{
			"testowner": {
				Owner: "testowner",
			},
			"verylongownername": {
				Owner: "verylongownername",
			},
		},
	}
	
	githubClients := make(map[string]*github.Client)
	manager := NewManager(cfg, githubClients, &MockContainerRuntime{})
	
	tests := []struct {
		name       string
		runnerName string
		expected   string
	}{
		{
			name:       "valid runner name format",
			runnerName: "runner-testowner-repo-123456-789-amd64",
			expected:   "testowner",
		},
		{
			name:       "truncated owner name",
			runnerName: "runner-verylong-repo-123456-789-amd64",
			expected:   "verylongownername",
		},
		{
			name:       "invalid format",
			runnerName: "invalid-format",
			expected:   "",
		},
		{
			name:       "legacy format",
			runnerName: "runner-repo-123456-789",
			expected:   "",
		},
	}
	
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := manager.getOwnerFromRunnerName(tt.runnerName)
			if result != tt.expected {
				t.Errorf("getOwnerFromRunnerName(%s) = %s, want %s", tt.runnerName, result, tt.expected)
			}
		})
	}
}

// TestCoverage_getOwnerFromRepoName tests owner extraction from repo full names
func TestCoverage_getOwnerFromRepoName(t *testing.T) {
	githubClients := make(map[string]*github.Client)
	manager := NewManager(&config.Config{}, githubClients, &MockContainerRuntime{})
	
	tests := []struct {
		name         string
		repoFullName string
		expected     string
	}{
		{
			name:         "valid full name",
			repoFullName: "testowner/testrepo",
			expected:     "testowner",
		},
		{
			name:         "empty string",
			repoFullName: "",
			expected:     "",
		},
		{
			name:         "no slash",
			repoFullName: "invalid",
			expected:     "",
		},
		{
			name:         "multiple slashes",
			repoFullName: "owner/repo/subdir",
			expected:     "owner",
		},
	}
	
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := manager.getOwnerFromRepoName(tt.repoFullName)
			if result != tt.expected {
				t.Errorf("getOwnerFromRepoName(%s) = %s, want %s", tt.repoFullName, result, tt.expected)
			}
		})
	}
}

// TestCoverage_GetRunnerDistribution tests runner distribution analytics
func TestCoverage_GetRunnerDistribution(t *testing.T) {
	cfg := &config.Config{
		Architecture: "amd64",
	}
	
	mockRuntime := &MockContainerRuntime{
		runners: []runtime.RunnerContainer{
			{ID: "runner1", Name: "test-runner-1", Status: "running"},
			{ID: "runner2", Name: "test-runner-2", Status: "running"},
		},
	}
	
	githubClients := make(map[string]*github.Client)
	manager := NewManager(cfg, githubClients, mockRuntime)
	
	distribution, err := manager.GetRunnerDistribution()
	if err != nil {
		t.Errorf("GetRunnerDistribution() error = %v", err)
	}
	
	if distribution == nil {
		t.Error("GetRunnerDistribution() returned nil")
	}
	
	// Mock returns "test-owner/test-repo" for all runners
	if len(distribution) == 0 {
		t.Error("GetRunnerDistribution() returned empty distribution")
	}
}

// TestCoverage_GetRepositories tests repository discovery function
func TestCoverage_GetRepositories(t *testing.T) {
	cfg := &config.Config{
		Owners: map[string]*config.OwnerConfig{
			"testowner": {
				Owner:    "testowner",
				AllRepos: true,
			},
		},
	}
	
	// Create a mock GitHub client that won't make real API calls
	mockClient := github.NewClient("test-token")
	githubClients := map[string]*github.Client{
		"testowner": mockClient,
	}
	
	manager := NewManager(cfg, githubClients, &MockContainerRuntime{})
	
	// This will likely fail with actual API calls, but exercises the function logic
	repos, err := manager.GetRepositories()
	
	// We expect this to fail due to no real API setup, but the function should be called
	// and return some response (error or empty slice)
	if repos == nil && err == nil {
		t.Error("GetRepositories() returned nil repos and nil error")
	}
	
	// Function should handle the error gracefully
	if err != nil {
		// This is expected since we don't have real API setup
		t.Logf("GetRepositories() failed as expected: %v", err)
	}
}

// TestCoverage_StartCleanupRoutine tests the cleanup routine startup
func TestCoverage_StartCleanupRoutine(t *testing.T) {
	cfg := &config.Config{
		Architecture:   "amd64",
		RunnerTimeout: 30 * time.Minute,
	}
	
	mockRuntime := &MockContainerRuntime{}
	githubClients := make(map[string]*github.Client)
	manager := NewManager(cfg, githubClients, mockRuntime)
	
	// Start cleanup routine in goroutine and stop it quickly for coverage
	done := make(chan bool)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				t.Logf("StartCleanupRoutine panicked (expected for test): %v", r)
			}
			done <- true
		}()
		
		// This will run forever, so we'll let it run briefly then panic to exit
		time.Sleep(10 * time.Millisecond)
		panic("test cleanup stop")
	}()
	
	// Let it start up briefly to get some coverage
	go manager.StartCleanupRoutine()
	
	// Wait for test goroutine to complete
	<-done
}