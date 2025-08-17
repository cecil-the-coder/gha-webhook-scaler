package github

import (
	"testing"
	
	"gopkg.in/yaml.v3"
)

func TestContainsSelfHosted(t *testing.T) {
	tests := []struct {
		name     string
		runsOn   interface{}
		expected bool
	}{
		{
			name:     "string with self-hosted",
			runsOn:   "self-hosted",
			expected: true,
		},
		{
			name:     "string with ubuntu",
			runsOn:   "ubuntu-latest",
			expected: false,
		},
		{
			name:     "array with self-hosted",
			runsOn:   []interface{}{"self-hosted", "linux", "x64"},
			expected: true,
		},
		{
			name:     "array with ubuntu only",
			runsOn:   []interface{}{"ubuntu-latest"},
			expected: false,
		},
		{
			name:     "string array with self-hosted",
			runsOn:   []string{"self-hosted", "linux", "docker"},
			expected: true,
		},
		{
			name:     "string array without self-hosted",
			runsOn:   []string{"ubuntu-latest", "windows-latest"},
			expected: false,
		},
		{
			name:     "case insensitive",
			runsOn:   "SELF-HOSTED",
			expected: true,
		},
		{
			name:     "mixed case in array",
			runsOn:   []string{"Self-Hosted", "Linux"},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := containsSelfHosted(tt.runsOn)
			if result != tt.expected {
				t.Errorf("containsSelfHosted(%v) = %v, want %v", tt.runsOn, result, tt.expected)
			}
		})
	}
}

func TestWorkflowConfig_Parse(t *testing.T) {
	// Test parsing a sample workflow YAML
	yamlContent := `
name: CI
on: [push, pull_request]
jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v2
  build:
    runs-on: [self-hosted, linux, x64]
    steps:
      - run: echo "Building on self-hosted runner"
`
	
	var config WorkflowConfig
	err := yaml.Unmarshal([]byte(yamlContent), &config)
	if err != nil {
		t.Fatalf("Failed to parse YAML: %v", err)
	}

	// Check if build job has self-hosted runner
	buildJob, exists := config.Jobs["build"]
	if !exists {
		t.Fatal("Build job not found")
	}

	if !containsSelfHosted(buildJob.RunsOn) {
		t.Error("Build job should use self-hosted runner")
	}

	// Check if test job doesn't have self-hosted runner
	testJob, exists := config.Jobs["test"]
	if !exists {
		t.Fatal("Test job not found")
	}

	if containsSelfHosted(testJob.RunsOn) {
		t.Error("Test job should not use self-hosted runner")
	}
}