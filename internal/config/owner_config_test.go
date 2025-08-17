package config

import (
	"testing"
)

func TestOwnerConfig_Validate(t *testing.T) {
	tests := []struct {
		name    string
		config  *OwnerConfig
		wantErr bool
	}{
		{
			name: "valid config with specific repo",
			config: &OwnerConfig{
				Owner:         "test-owner",
				GitHubToken:   "token123",
				RepoName:      "test-repo",
				WebhookSecret: "secret123",
				AllRepos:      false,
			},
			wantErr: false,
		},
		{
			name: "valid config with all repos",
			config: &OwnerConfig{
				Owner:         "test-owner",
				GitHubToken:   "token123",
				AllRepos:      true,
				WebhookSecret: "secret123",
			},
			wantErr: false,
		},
		{
			name: "missing owner",
			config: &OwnerConfig{
				GitHubToken: "token123",
				RepoName:    "test-repo",
			},
			wantErr: true,
		},
		{
			name: "missing token",
			config: &OwnerConfig{
				Owner:    "test-owner",
				RepoName: "test-repo",
			},
			wantErr: true,
		},
		{
			name: "missing repo name when not all repos",
			config: &OwnerConfig{
				Owner:       "test-owner",
				GitHubToken: "token123",
				AllRepos:    false,
			},
			wantErr: true,
		},
		{
			name: "invalid max runners - zero",
			config: &OwnerConfig{
				Owner:       "test-owner",
				GitHubToken: "token123",
				RepoName:    "test-repo",
				MaxRunners:  &[]int{0}[0],
			},
			wantErr: true,
		},
		{
			name: "invalid max runners - negative",
			config: &OwnerConfig{
				Owner:       "test-owner",
				GitHubToken: "token123",
				RepoName:    "test-repo",
				MaxRunners:  &[]int{-1}[0],
			},
			wantErr: true,
		},
		{
			name: "valid max runners",
			config: &OwnerConfig{
				Owner:       "test-owner",
				GitHubToken: "token123",
				RepoName:    "test-repo",
				MaxRunners:  &[]int{5}[0],
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("OwnerConfig.Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestOwnerConfig_IsRepoAllowed(t *testing.T) {
	tests := []struct {
		name         string
		config       *OwnerConfig
		repoFullName string
		expected     bool
	}{
		{
			name: "specific repo - allowed",
			config: &OwnerConfig{
				Owner:    "test-owner",
				RepoName: "test-repo",
				AllRepos: false,
			},
			repoFullName: "test-owner/test-repo",
			expected:     true,
		},
		{
			name: "specific repo - not allowed",
			config: &OwnerConfig{
				Owner:    "test-owner",
				RepoName: "test-repo",
				AllRepos: false,
			},
			repoFullName: "test-owner/other-repo",
			expected:     false,
		},
		{
			name: "all repos - allowed",
			config: &OwnerConfig{
				Owner:    "test-owner",
				AllRepos: true,
			},
			repoFullName: "test-owner/any-repo",
			expected:     true,
		},
		{
			name: "all repos - wrong owner",
			config: &OwnerConfig{
				Owner:    "test-owner",
				AllRepos: true,
			},
			repoFullName: "other-owner/repo",
			expected:     false,
		},
		{
			name: "invalid repo format",
			config: &OwnerConfig{
				Owner:    "test-owner",
				AllRepos: true,
			},
			repoFullName: "invalid-format",
			expected:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.config.IsRepoAllowed(tt.repoFullName)
			if result != tt.expected {
				t.Errorf("OwnerConfig.IsRepoAllowed(%q) = %v, want %v", tt.repoFullName, result, tt.expected)
			}
		})
	}
}

func TestOwnerConfig_GetRepoURL(t *testing.T) {
	tests := []struct {
		name     string
		config   *OwnerConfig
		expected string
	}{
		{
			name: "specific repo",
			config: &OwnerConfig{
				Owner:    "test-owner",
				RepoName: "test-repo",
				AllRepos: false,
			},
			expected: "https://github.com/test-owner/test-repo",
		},
		{
			name: "all repos",
			config: &OwnerConfig{
				Owner:    "test-owner",
				AllRepos: true,
			},
			expected: "https://github.com/test-owner",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.config.GetRepoURL()
			if result != tt.expected {
				t.Errorf("OwnerConfig.GetRepoURL() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestOwnerConfig_GetRepoNameFromFullName(t *testing.T) {
	config := &OwnerConfig{}

	tests := []struct {
		name         string
		repoFullName string
		expected     string
	}{
		{
			name:         "normal repo",
			repoFullName: "owner/repo",
			expected:     "repo",
		},
		{
			name:         "repo with special characters",
			repoFullName: "owner/repo-name.test",
			expected:     "repo-name.test",
		},
		{
			name:         "invalid format",
			repoFullName: "invalid",
			expected:     "invalid",
		},
		{
			name:         "empty string",
			repoFullName: "",
			expected:     "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := config.GetRepoNameFromFullName(tt.repoFullName)
			if result != tt.expected {
				t.Errorf("OwnerConfig.GetRepoNameFromFullName(%q) = %v, want %v", tt.repoFullName, result, tt.expected)
			}
		})
	}
}

func TestOwnerConfig_GetEffectiveMaxRunners(t *testing.T) {
	tests := []struct {
		name          string
		config        *OwnerConfig
		globalDefault int
		expected      int
	}{
		{
			name: "owner-specific limit",
			config: &OwnerConfig{
				MaxRunners: &[]int{10}[0],
			},
			globalDefault: 5,
			expected:      10,
		},
		{
			name: "use global default",
			config: &OwnerConfig{
				MaxRunners: nil,
			},
			globalDefault: 5,
			expected:      5,
		},
		{
			name: "zero global default",
			config: &OwnerConfig{
				MaxRunners: nil,
			},
			globalDefault: 0,
			expected:      0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.config.GetEffectiveMaxRunners(tt.globalDefault)
			if result != tt.expected {
				t.Errorf("OwnerConfig.GetEffectiveMaxRunners(%d) = %v, want %v", tt.globalDefault, result, tt.expected)
			}
		})
	}
}