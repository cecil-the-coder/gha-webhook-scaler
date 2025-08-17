package config

import (
	"fmt"
	"strings"
)

// OwnerConfig represents configuration for a single GitHub owner/organization
type OwnerConfig struct {
	// GitHub configuration
	Owner         string
	GitHubToken   string
	WebhookSecret string
	AllRepos      bool   // Support all repos under owner
	RepoName      string // Specific repo (if AllRepos is false)
	
	// Owner-specific runner settings (optional overrides)
	MaxRunners    *int    // nil means use global default
	RunnerLabels  []string // additional labels for this owner
}

// Validate validates the owner configuration
func (oc *OwnerConfig) Validate() error {
	if oc.Owner == "" {
		return fmt.Errorf("owner is required")
	}
	if oc.GitHubToken == "" {
		return fmt.Errorf("GitHub token is required for owner %s", oc.Owner)
	}
	// RepoName is only required if not using AllRepos mode
	if !oc.AllRepos && oc.RepoName == "" {
		return fmt.Errorf("repo name is required for owner %s (or set AllRepos=true)", oc.Owner)
	}
	if oc.MaxRunners != nil && *oc.MaxRunners <= 0 {
		return fmt.Errorf("max runners must be greater than 0 for owner %s", oc.Owner)
	}
	return nil
}

// GetRepoURL returns the repository URL for this owner
func (oc *OwnerConfig) GetRepoURL() string {
	if oc.AllRepos {
		return fmt.Sprintf("https://github.com/%s", oc.Owner)
	}
	return fmt.Sprintf("https://github.com/%s/%s", oc.Owner, oc.RepoName)
}

// IsRepoAllowed checks if a repository is allowed for this owner
func (oc *OwnerConfig) IsRepoAllowed(repoFullName string) bool {
	if oc.AllRepos {
		// Check if repo belongs to this owner
		parts := strings.Split(repoFullName, "/")
		if len(parts) != 2 {
			return false
		}
		return parts[0] == oc.Owner
	}
	
	// Single repo mode - exact match required
	expectedFullName := fmt.Sprintf("%s/%s", oc.Owner, oc.RepoName)
	return repoFullName == expectedFullName
}

// GetRepoNameFromFullName extracts repo name from full name (owner/repo)
func (oc *OwnerConfig) GetRepoNameFromFullName(repoFullName string) string {
	parts := strings.Split(repoFullName, "/")
	if len(parts) == 2 {
		return parts[1]
	}
	return repoFullName
}

// GetEffectiveMaxRunners returns the effective max runners for this owner
func (oc *OwnerConfig) GetEffectiveMaxRunners(globalDefault int) int {
	if oc.MaxRunners != nil {
		return *oc.MaxRunners
	}
	return globalDefault
}

