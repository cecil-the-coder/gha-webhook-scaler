package runtime

import (
	"testing"

	"github.com/cecil-the-coder/gha-webhook-scaler/internal/config"
)

func TestNewContainerRuntime(t *testing.T) {
	tests := []struct {
		name         string
		runtimeType  config.RuntimeType
		expectType   string
		expectError  bool
	}{
		{
			name:        "docker runtime",
			runtimeType: config.RuntimeTypeDocker,
			expectType:  "docker",
			expectError: false,
		},
		{
			name:        "kubernetes runtime",
			runtimeType: config.RuntimeTypeKubernetes,
			expectType:  "kubernetes",
			expectError: false, // May or may not fail depending on environment, but shouldn't panic
		},
		{
			name:        "default to docker for unknown type",
			runtimeType: config.RuntimeType("unknown"),
			expectType:  "docker",
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a test config
			testConfig := &config.Config{
				KubernetesNamespace: "default",
			}
			runtime, err := NewContainerRuntime(tt.runtimeType, testConfig)

			// For kubernetes runtime, we accept either success or failure as long as it doesn't panic
			if tt.runtimeType == config.RuntimeTypeKubernetes {
				// Just ensure no panic occurred
				return
			}

			if tt.expectError {
				if err == nil {
					t.Errorf("NewContainerRuntime() expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Errorf("NewContainerRuntime() unexpected error: %v", err)
				return
			}

			if runtime == nil {
				t.Error("NewContainerRuntime() returned nil runtime")
				return
			}

			// Check if the runtime implements the ContainerRuntime interface
			if _, ok := runtime.(ContainerRuntime); !ok {
				t.Error("NewContainerRuntime() returned object does not implement ContainerRuntime interface")
			}

			// Check the runtime type
			actualType := runtime.GetRuntimeType()
			if string(actualType) != tt.expectType {
				t.Errorf("NewContainerRuntime() type = %v, want %v", actualType, tt.expectType)
			}
		})
	}
}

func TestRuntimeInterface(t *testing.T) {
	// Test that both runtime implementations satisfy the interface
	runtimes := []struct {
		name        string
		runtimeType config.RuntimeType
	}{
		{"docker", config.RuntimeTypeDocker},
		{"kubernetes", config.RuntimeTypeKubernetes},
	}

	for _, rt := range runtimes {
		t.Run(rt.name, func(t *testing.T) {
			// Create a test config
			testConfig := &config.Config{
				KubernetesNamespace: "default",
			}
			runtime, err := NewContainerRuntime(rt.runtimeType, testConfig)
			if rt.runtimeType == config.RuntimeTypeKubernetes && err != nil {
				// Kubernetes runtime is expected to fail in test environment without kubeconfig
				t.Skipf("Skipping Kubernetes runtime test due to missing kubeconfig: %v", err)
				return
			}
			if err != nil {
				t.Fatalf("Failed to create runtime: %v", err)
			}

			// Verify all interface methods exist (this will fail to compile if they don't)
			var _ ContainerRuntime = runtime

			// Test that basic methods can be called (even if they might fail due to missing dependencies)
			// We're just testing that the interface is properly implemented
			
			runtimeType := runtime.GetRuntimeType()
			if runtimeType == "" {
				t.Error("GetRuntimeType() returned empty string")
			}

			// Test health check (might fail but should not panic)
			_ = runtime.HealthCheck()
		})
	}
}

func TestRunnerContainer_Structure(t *testing.T) {
	// Test that RunnerContainer can be created and has expected fields
	container := RunnerContainer{
		ID:           "test-id",
		Name:         "test-runner",
		Status:       "running",
		Architecture: "amd64",
	}

	if container.ID != "test-id" {
		t.Errorf("RunnerContainer.ID = %v, want %v", container.ID, "test-id")
	}

	if container.Name != "test-runner" {
		t.Errorf("RunnerContainer.Name = %v, want %v", container.Name, "test-runner")
	}

	if container.Status != "running" {
		t.Errorf("RunnerContainer.Status = %v, want %v", container.Status, "running")
	}

	if container.Architecture != "amd64" {
		t.Errorf("RunnerContainer.Architecture = %v, want %v", container.Architecture, "amd64")
	}
}