package config

import (
	"os"
	"testing"
	"time"
)

func TestNewConfig(t *testing.T) {
	// Save original environment
	originalEnv := make(map[string]string)
	envVars := []string{
		"GITHUB_TOKEN", "REPO_OWNER", "REPO_NAME", "WEBHOOK_SECRET",
		"MAX_RUNNERS", "PORT", "DEBUG", "RUNNER_TIMEOUT", "ARCHITECTURE",
		"RUNNER_LABELS", "RUNNER_IMAGE", "RUNTIME", "DOCKER_NETWORK",
		"CACHE_VOLUMES", "ALL_REPOS", "OWNERS",
	}
	
	for _, envVar := range envVars {
		if val, exists := os.LookupEnv(envVar); exists {
			originalEnv[envVar] = val
		}
		os.Unsetenv(envVar)
	}
	
	// Restore environment after test
	defer func() {
		for _, envVar := range envVars {
			os.Unsetenv(envVar)
		}
		for envVar, val := range originalEnv {
			os.Setenv(envVar, val)
		}
	}()

	tests := []struct {
		name     string
		envVars  map[string]string
		expected *Config
		wantErr  bool
	}{
		{
			name: "minimal valid config - legacy mode",
			envVars: map[string]string{
				"GITHUB_TOKEN": "test-token",
				"REPO_OWNER":   "test-owner",
				"REPO_NAME":    "test-repo",
			},
			expected: &Config{
				MaxRunners:      5,
				RunnerTimeout:   3600 * time.Second,
				Port:            8080,
				Debug:           false,
				Runtime:         RuntimeTypeDocker,
				CacheVolumes:    true,
			},
			wantErr: false,
		},
		{
			name: "full config with custom values - legacy mode",
			envVars: map[string]string{
				"GITHUB_TOKEN":    "custom-token",
				"REPO_OWNER":      "custom-owner",
				"REPO_NAME":       "custom-repo",
				"WEBHOOK_SECRET":  "secret123",
				"MAX_RUNNERS":     "10",
				"PORT":            "9090",
				"DEBUG":           "true",
				"RUNNER_TIMEOUT":  "120",
				"RUNNER_IMAGE":    "custom/runner:latest",
				"RUNTIME":         "kubernetes",
				"DOCKER_NETWORK":  "custom-network",
				"CACHE_VOLUMES":   "false",
				"ALL_REPOS":       "true",
			},
			expected: &Config{
				MaxRunners:      10,
				RunnerTimeout:   120 * time.Second,
				Port:            9090,
				Debug:           true,
				RunnerImage:     "custom/runner:latest",
				Runtime:         RuntimeTypeKubernetes,
				CacheVolumes:    false,
			},
			wantErr: false,
		},
		{
			name: "owner-based config",
			envVars: map[string]string{
				"OWNERS":                   "testorg",
				"OWNER_TESTORG_GITHUB_TOKEN": "test-token",
				"OWNER_TESTORG_ALL_REPOS":    "true",
			},
			expected: &Config{
				MaxRunners:      5,
				Port:            8080,
				Runtime:         RuntimeTypeDocker,
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Set environment variables
			for key, value := range tt.envVars {
				os.Setenv(key, value)
			}

			config := NewConfig()

			// Compare global config fields
			if config.MaxRunners != tt.expected.MaxRunners {
				t.Errorf("MaxRunners = %v, want %v", config.MaxRunners, tt.expected.MaxRunners)
			}
			if config.Port != tt.expected.Port {
				t.Errorf("Port = %v, want %v", config.Port, tt.expected.Port)
			}
			if config.Debug != tt.expected.Debug {
				t.Errorf("Debug = %v, want %v", config.Debug, tt.expected.Debug)
			}
			if config.Runtime != tt.expected.Runtime {
				t.Errorf("Runtime = %v, want %v", config.Runtime, tt.expected.Runtime)
			}

			// Verify at least one owner was loaded
			if len(config.Owners) == 0 {
				t.Error("Expected at least one owner to be configured")
			}

			// Clean up environment for next test
			for key := range tt.envVars {
				os.Unsetenv(key)
			}
		})
	}
}

func TestParseRuntimeType(t *testing.T) {
	tests := []struct {
		input    string
		expected RuntimeType
	}{
		{"docker", RuntimeTypeDocker},
		{"Docker", RuntimeTypeDocker},
		{"DOCKER", RuntimeTypeDocker},
		{"kubernetes", RuntimeTypeKubernetes},
		{"k8s", RuntimeTypeKubernetes},
		{"Kubernetes", RuntimeTypeKubernetes},
		{"K8S", RuntimeTypeKubernetes},
		{"invalid", RuntimeTypeDocker}, // defaults to docker
		{"", RuntimeTypeDocker},        // defaults to docker
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := parseRuntimeType(tt.input)
			if result != tt.expected {
				t.Errorf("parseRuntimeType(%q) = %v, want %v", tt.input, result, tt.expected)
			}
		})
	}
}

func TestConfig_Validate(t *testing.T) {
	tests := []struct {
		name    string
		config  *Config
		wantErr bool
	}{
		{
			name: "valid owner config",
			config: &Config{
				MaxRunners:     5,
				Port:           8080,
				Owners: map[string]*OwnerConfig{
					"owner": {
						Owner:       "owner",
						GitHubToken: "token",
						RepoName:    "repo",
					},
				},
			},
			wantErr: false,
		},
		{
			name: "empty owners",
			config: &Config{
				MaxRunners:     5,
				Port:           8080,
				Owners:         make(map[string]*OwnerConfig),
			},
			wantErr: true,
		},
		{
			name: "invalid port - too low",
			config: &Config{
				Port:           0,
				MaxRunners:     5,
				Owners: map[string]*OwnerConfig{
					"owner": {
						Owner:       "owner",
						GitHubToken: "token",
						RepoName:    "repo",
					},
				},
			},
			wantErr: true,
		},
		{
			name: "invalid max runners - negative",
			config: &Config{
				Port:           8080,
				MaxRunners:     -1,
				Owners: map[string]*OwnerConfig{
					"owner": {
						Owner:       "owner",
						GitHubToken: "token",
						RepoName:    "repo",
					},
				},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Config.Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestConfig_GetRepoURL(t *testing.T) {
	tests := []struct {
		name     string
		config   *Config
		expected string
	}{
		{
			name: "single repo",
			config: &Config{
				Owners: map[string]*OwnerConfig{
					"owner": {
						Owner:    "owner",
						RepoName: "repo",
						AllRepos: false,
					},
				},
			},
			expected: "https://github.com/owner/repo",
		},
		{
			name: "all repos",
			config: &Config{
				Owners: map[string]*OwnerConfig{
					"owner": {
						Owner:    "owner",
						AllRepos: true,
					},
				},
			},
			expected: "https://github.com/owner",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.config.GetRepoURL()
			if result != tt.expected {
				t.Errorf("Config.GetRepoURL() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestConfig_GetRunnerLabelsString(t *testing.T) {
	config := &Config{
		RunnerLabels: []string{"linux", "x64", "docker"},
	}

	result := config.GetRunnerLabelsString()
	expected := "linux,x64,docker"

	if result != expected {
		t.Errorf("Config.GetRunnerLabelsString() = %v, want %v", result, expected)
	}
}

func TestConfig_IsRepoAllowed(t *testing.T) {
	config := &Config{
		Owners: map[string]*OwnerConfig{
			"owner1": {
				Owner:    "owner1",
				RepoName: "repo1",
				AllRepos: false,
			},
			"owner2": {
				Owner:    "owner2",
				AllRepos: true,
			},
		},
	}

	tests := []struct {
		name         string
		repoFullName string
		expected     bool
	}{
		{
			name:         "allowed single repo",
			repoFullName: "owner1/repo1",
			expected:     true,
		},
		{
			name:         "not allowed single repo",
			repoFullName: "owner1/other-repo",
			expected:     false,
		},
		{
			name:         "allowed all repos owner",
			repoFullName: "owner2/any-repo",
			expected:     true,
		},
		{
			name:         "unknown owner",
			repoFullName: "unknown/repo",
			expected:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := config.IsRepoAllowed(tt.repoFullName)
			if result != tt.expected {
				t.Errorf("Config.IsRepoAllowed(%q) = %v, want %v", tt.repoFullName, result, tt.expected)
			}
		})
	}
}

func TestDetectArchitecture(t *testing.T) {
	// This test just verifies the function doesn't panic and returns a valid value
	arch := detectArchitecture()
	
	if arch != "amd64" && arch != "arm64" {
		t.Errorf("detectArchitecture() = %v, expected amd64 or arm64", arch)
	}
}

func TestConfig_GetEffectiveArchitecture(t *testing.T) {
	tests := []struct {
		name     string
		config   *Config
		expected string
	}{
		{
			name: "docker runtime",
			config: &Config{
				Runtime:      RuntimeTypeDocker,
				Architecture: "amd64",
			},
			expected: "amd64",
		},
		{
			name: "kubernetes runtime",
			config: &Config{
				Runtime:      RuntimeTypeKubernetes,
				Architecture: "arm64",
			},
			expected: "all",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.config.GetEffectiveArchitecture()
			if result != tt.expected {
				t.Errorf("Config.GetEffectiveArchitecture() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestConfig_loadOwnerConfig(t *testing.T) {
	// Save original environment
	originalEnv := make(map[string]string)
	envVars := []string{
		"OWNERS", "OWNER_ORG1_GITHUB_TOKEN", "OWNER_ORG1_WEBHOOK_SECRET",
		"OWNER_ORG1_ALL_REPOS", "OWNER_ORG1_REPO_NAME", "OWNER_ORG1_MAX_RUNNERS",
		"OWNER_ORG1_LABELS", "OWNER_ORG_2_GITHUB_TOKEN", "OWNER_ORG_2_REPO_NAME",
		"GITHUB_TOKEN", "REPO_OWNER", "REPO_NAME", "WEBHOOK_SECRET", "ALL_REPOS",
	}

	for _, envVar := range envVars {
		if val, exists := os.LookupEnv(envVar); exists {
			originalEnv[envVar] = val
		}
		os.Unsetenv(envVar)
	}

	// Restore environment after test
	defer func() {
		for _, envVar := range envVars {
			os.Unsetenv(envVar)
		}
		for envVar, val := range originalEnv {
			os.Setenv(envVar, val)
		}
	}()

	tests := []struct {
		name    string
		envVars map[string]string
		setup   func(*Config)
		verify  func(*testing.T, *Config)
	}{
		{
			name: "load single owner config",
			envVars: map[string]string{
				"OWNERS":                  "org1",
				"OWNER_ORG1_GITHUB_TOKEN": "token1",
				"OWNER_ORG1_ALL_REPOS":    "true",
				"OWNER_ORG1_MAX_RUNNERS":  "10",
				"OWNER_ORG1_LABELS":       "linux, x64",
				"WEBHOOK_SECRET":          "global-secret",
			},
			setup: func(c *Config) {
				c.Owners = make(map[string]*OwnerConfig)
			},
			verify: func(t *testing.T, c *Config) {
				if len(c.Owners) != 1 {
					t.Errorf("Expected 1 owner, got %d", len(c.Owners))
				}

				owner, exists := c.Owners["org1"]
				if !exists {
					t.Error("Expected org1 owner to exist")
					return
				}

				if owner.Owner != "org1" {
					t.Errorf("Expected owner 'org1', got %s", owner.Owner)
				}
				if owner.GitHubToken != "token1" {
					t.Errorf("Expected token 'token1', got %s", owner.GitHubToken)
				}
				if !owner.AllRepos {
					t.Error("Expected AllRepos to be true")
				}
				if owner.MaxRunners == nil || *owner.MaxRunners != 10 {
					t.Errorf("Expected MaxRunners 10, got %v", owner.MaxRunners)
				}
				if len(owner.RunnerLabels) != 2 || owner.RunnerLabels[0] != "linux" || owner.RunnerLabels[1] != "x64" {
					t.Errorf("Expected labels [linux, x64], got %v", owner.RunnerLabels)
				}
				if owner.WebhookSecret != "global-secret" {
					t.Errorf("Expected webhook secret fallback to global, got %s", owner.WebhookSecret)
				}
			},
		},
		{
			name: "load multiple owners config",
			envVars: map[string]string{
				"OWNERS":                    "org1,org-2",
				"OWNER_ORG1_GITHUB_TOKEN":  "token1",
				"OWNER_ORG1_REPO_NAME":     "repo1",
				"OWNER_ORG_2_GITHUB_TOKEN": "token2",
				"OWNER_ORG_2_ALL_REPOS":    "true",
			},
			setup: func(c *Config) {
				c.Owners = make(map[string]*OwnerConfig)
			},
			verify: func(t *testing.T, c *Config) {
				if len(c.Owners) != 2 {
					t.Errorf("Expected 2 owners, got %d", len(c.Owners))
				}

				// Verify org1
				owner1, exists := c.Owners["org1"]
				if !exists {
					t.Error("Expected org1 owner to exist")
				} else {
					if owner1.RepoName != "repo1" {
						t.Errorf("Expected repo name 'repo1', got %s", owner1.RepoName)
					}
					if owner1.AllRepos {
						t.Error("Expected AllRepos to be false for org1")
					}
				}

				// Verify org-2 (with hyphen in name)
				owner2, exists := c.Owners["org-2"]
				if !exists {
					t.Error("Expected org-2 owner to exist")
				} else {
					if !owner2.AllRepos {
						t.Error("Expected AllRepos to be true for org-2")
					}
				}
			},
		},
		{
			name: "legacy configuration - backward compatibility",
			envVars: map[string]string{
				"GITHUB_TOKEN":   "legacy-token",
				"REPO_OWNER":     "legacy-owner",
				"REPO_NAME":      "legacy-repo",
				"WEBHOOK_SECRET": "legacy-secret",
				"ALL_REPOS":      "false",
			},
			setup: func(c *Config) {
				c.Owners = make(map[string]*OwnerConfig)
			},
			verify: func(t *testing.T, c *Config) {
				if len(c.Owners) != 1 {
					t.Errorf("Expected 1 owner (legacy mode), got %d", len(c.Owners))
				}

				owner, exists := c.Owners["legacy-owner"]
				if !exists {
					t.Error("Expected legacy-owner to exist")
					return
				}

				if owner.GitHubToken != "legacy-token" {
					t.Errorf("Expected token 'legacy-token', got %s", owner.GitHubToken)
				}
				if owner.RepoName != "legacy-repo" {
					t.Errorf("Expected repo name 'legacy-repo', got %s", owner.RepoName)
				}
				if owner.WebhookSecret != "legacy-secret" {
					t.Errorf("Expected webhook secret 'legacy-secret', got %s", owner.WebhookSecret)
				}
			},
		},
		{
			name: "owners with whitespace",
			envVars: map[string]string{
				"OWNERS":                   " org1 , , org2 ",
				"OWNER_ORG1_GITHUB_TOKEN": "token1",
				"OWNER_ORG2_GITHUB_TOKEN": "token2",
			},
			setup: func(c *Config) {
				c.Owners = make(map[string]*OwnerConfig)
			},
			verify: func(t *testing.T, c *Config) {
				if len(c.Owners) != 2 {
					t.Errorf("Expected 2 owners, got %d", len(c.Owners))
				}

				if _, exists := c.Owners["org1"]; !exists {
					t.Error("Expected org1 owner to exist")
				}
				if _, exists := c.Owners["org2"]; !exists {
					t.Error("Expected org2 owner to exist")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Set environment variables
			for key, value := range tt.envVars {
				os.Setenv(key, value)
			}

			config := &Config{}
			tt.setup(config)
			config.loadOwnerConfig()
			tt.verify(t, config)

			// Clean up environment for next test
			for key := range tt.envVars {
				os.Unsetenv(key)
			}
		})
	}
}

func TestConfig_GetOwnerForRepo(t *testing.T) {
	config := &Config{
		Owners: map[string]*OwnerConfig{
			"org1": {
				Owner:    "org1",
				RepoName: "repo1",
				AllRepos: false,
			},
			"org2": {
				Owner:    "org2",
				AllRepos: true,
			},
			"org3": {
				Owner:    "org3",
				RepoName: "specific-repo",
				AllRepos: false,
			},
		},
	}

	tests := []struct {
		name         string
		repoFullName string
		wantOwner    string
		wantErr      bool
	}{
		{
			name:         "exact repo match",
			repoFullName: "org1/repo1",
			wantOwner:    "org1",
			wantErr:      false,
		},
		{
			name:         "all repos match",
			repoFullName: "org2/any-repo",
			wantOwner:    "org2",
			wantErr:      false,
		},
		{
			name:         "specific repo match",
			repoFullName: "org3/specific-repo",
			wantOwner:    "org3",
			wantErr:      false,
		},
		{
			name:         "no match - wrong repo",
			repoFullName: "org1/wrong-repo",
			wantOwner:    "",
			wantErr:      true,
		},
		{
			name:         "no match - unknown owner",
			repoFullName: "unknown/repo",
			wantOwner:    "",
			wantErr:      true,
		},
		{
			name:         "invalid repo format",
			repoFullName: "invalid-format",
			wantOwner:    "",
			wantErr:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ownerConfig, err := config.GetOwnerForRepo(tt.repoFullName)
			
			if tt.wantErr {
				if err == nil {
					t.Errorf("GetOwnerForRepo() expected error, got nil")
				}
				return
			}
			
			if err != nil {
				t.Errorf("GetOwnerForRepo() unexpected error: %v", err)
				return
			}
			
			if ownerConfig == nil {
				t.Error("GetOwnerForRepo() returned nil owner config")
				return
			}
			
			if ownerConfig.Owner != tt.wantOwner {
				t.Errorf("GetOwnerForRepo() returned owner %s, want %s", ownerConfig.Owner, tt.wantOwner)
			}
		})
	}
}

func TestConfig_GetRepoNameFromFullName(t *testing.T) {
	config := &Config{}

	tests := []struct {
		name         string
		repoFullName string
		expected     string
	}{
		{
			name:         "standard repo format",
			repoFullName: "owner/repo",
			expected:     "repo",
		},
		{
			name:         "repo with hyphens",
			repoFullName: "org-name/repo-name",
			expected:     "repo-name",
		},
		{
			name:         "repo with underscores",
			repoFullName: "org_name/repo_name",
			expected:     "repo_name",
		},
		{
			name:         "invalid format - no slash",
			repoFullName: "just-repo-name",
			expected:     "just-repo-name",
		},
		{
			name:         "invalid format - multiple slashes",
			repoFullName: "owner/group/repo",
			expected:     "owner/group/repo",
		},
		{
			name:         "empty string",
			repoFullName: "",
			expected:     "",
		},
		{
			name:         "only slash",
			repoFullName: "/",
			expected:     "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := config.GetRepoNameFromFullName(tt.repoFullName)
			if result != tt.expected {
				t.Errorf("GetRepoNameFromFullName(%q) = %v, want %v", tt.repoFullName, result, tt.expected)
			}
		})
	}
}

func TestMultipleOwners_Integration(t *testing.T) {
	// Save original environment
	originalEnv := make(map[string]string)
	envVars := []string{
		"OWNERS", "OWNER_TESTORG_GITHUB_TOKEN",
		"OWNER_TESTORG_ALL_REPOS", "OWNER_COMPANY_GITHUB_TOKEN",
		"OWNER_COMPANY_REPO_NAME",
	}

	for _, envVar := range envVars {
		if val, exists := os.LookupEnv(envVar); exists {
			originalEnv[envVar] = val
		}
		os.Unsetenv(envVar)
	}

	// Restore environment after test
	defer func() {
		for _, envVar := range envVars {
			os.Unsetenv(envVar)
		}
		for envVar, val := range originalEnv {
			os.Setenv(envVar, val)
		}
	}()

	// Set up multiple owners
	os.Setenv("OWNERS", "testorg,company")
	os.Setenv("OWNER_TESTORG_GITHUB_TOKEN", "token1")
	os.Setenv("OWNER_TESTORG_ALL_REPOS", "true")
	os.Setenv("OWNER_COMPANY_GITHUB_TOKEN", "token2")
	os.Setenv("OWNER_COMPANY_REPO_NAME", "private-repo")

	config := NewConfig()

	// Test owner loading
	if len(config.Owners) != 2 {
		t.Errorf("Expected 2 owners, got %d", len(config.Owners))
	}

	// Test GetOwnerForRepo works correctly
	owner1, err := config.GetOwnerForRepo("testorg/any-repo")
	if err != nil {
		t.Errorf("Unexpected error for testorg repo: %v", err)
	}
	if owner1 == nil || owner1.Owner != "testorg" {
		t.Error("Expected testorg owner for testorg/any-repo")
	}

	owner2, err := config.GetOwnerForRepo("company/private-repo")
	if err != nil {
		t.Errorf("Unexpected error for company repo: %v", err)
	}
	if owner2 == nil || owner2.Owner != "company" {
		t.Error("Expected company owner for company/private-repo")
	}

	// Test that wrong repo is rejected
	_, err = config.GetOwnerForRepo("company/wrong-repo")
	if err == nil {
		t.Error("Expected error for wrong repo, got nil")
	}

	// Test IsRepoAllowed
	if !config.IsRepoAllowed("testorg/any-repo") {
		t.Error("Expected testorg/any-repo to be allowed")
	}
	if !config.IsRepoAllowed("company/private-repo") {
		t.Error("Expected company/private-repo to be allowed")
	}
	if config.IsRepoAllowed("unknown/repo") {
		t.Error("Expected unknown/repo to be rejected")
	}

	// Test GetRepoNameFromFullName
	repoName := config.GetRepoNameFromFullName("testorg/my-awesome-repo")
	if repoName != "my-awesome-repo" {
		t.Errorf("Expected repo name 'my-awesome-repo', got %s", repoName)
	}
}