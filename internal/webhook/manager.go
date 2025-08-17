package webhook

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/cecil-the-coder/gha-webhook-scaler/internal/config"
	"github.com/cecil-the-coder/gha-webhook-scaler/internal/github"
)

// WebhookManager handles webhook creation, verification, and management operations
type WebhookManager struct {
	githubClient *github.Client
	config       *config.Config
}

// Hook represents a GitHub webhook
type Hook struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Active   bool   `json:"active"`
	Events   []string `json:"events"`
	Config   HookConfig `json:"config"`
	TestURL  string `json:"test_url,omitempty"`
	PingURL  string `json:"ping_url,omitempty"`
	CreatedAt string `json:"created_at,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

// HookConfig contains webhook configuration
type HookConfig struct {
	URL         string `json:"url"`
	ContentType string `json:"content_type"`
	Secret      string `json:"secret,omitempty"`
	InsecureSSL string `json:"insecure_ssl,omitempty"`
}

// WebhookCreateRequest represents the request payload for creating a webhook
type WebhookCreateRequest struct {
	Name   string     `json:"name"`
	Active bool       `json:"active"`
	Events []string   `json:"events"`
	Config HookConfig `json:"config"`
}

// WebhookListResponse represents the response when listing webhooks
type WebhookListResponse []Hook

// NewWebhookManager creates a new webhook manager instance
func NewWebhookManager(githubClient *github.Client, config *config.Config) *WebhookManager {
	return &WebhookManager{
		githubClient: githubClient,
		config:       config,
	}
}

// ListWebhooks retrieves all webhooks for a repository
func (wm *WebhookManager) ListWebhooks(owner, repo string) ([]Hook, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/hooks", owner, repo)
	
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "token "+wm.githubClient.Token)
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	resp, err := wm.githubClient.MakeRequestWithBackoff(req)
	if err != nil {
		return nil, fmt.Errorf("failed to make request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("GitHub API error: %d - %s", resp.StatusCode, string(body))
	}

	var hooks []Hook
	if err := json.NewDecoder(resp.Body).Decode(&hooks); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return hooks, nil
}

// CreateWebhook creates a new webhook for a repository
func (wm *WebhookManager) CreateWebhook(owner, repo, webhookURL, secret string, events []string) (*Hook, error) {
	// Validate token permissions first
	hasAccess, err := wm.githubClient.HasScopeForWebhooks()
	if err != nil {
		log.Printf("Warning: Could not validate webhook permissions: %v", err)
		// Continue anyway for fine-grained tokens where scope detection may not work
	} else if !hasAccess {
		return nil, fmt.Errorf("token lacks required permissions for webhook management. Required: repo scope or write:repo_hook scope")
	}

	if len(events) == 0 {
		events = []string{"workflow_job"} // Default to workflow_job events for job-level scaling
	}

	webhookRequest := WebhookCreateRequest{
		Name:   "web",
		Active: true,
		Events: events,
		Config: HookConfig{
			URL:         webhookURL,
			ContentType: "json",
			Secret:      secret,
			InsecureSSL: "0", // Always require SSL
		},
	}

	payload, err := json.Marshal(webhookRequest)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/hooks", owner, repo)
	
	req, err := http.NewRequest("POST", url, bytes.NewBuffer(payload))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "token "+wm.githubClient.Token)
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	req.Header.Set("Content-Type", "application/json")

	resp, err := wm.githubClient.MakeRequestWithBackoff(req)
	if err != nil {
		return nil, fmt.Errorf("failed to make request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("GitHub API error: %d - %s", resp.StatusCode, string(body))
	}

	var hook Hook
	if err := json.NewDecoder(resp.Body).Decode(&hook); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &hook, nil
}

// GetWebhook retrieves a specific webhook by ID
func (wm *WebhookManager) GetWebhook(owner, repo string, hookID int64) (*Hook, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/hooks/%d", owner, repo, hookID)
	
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "token "+wm.githubClient.Token)
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	resp, err := wm.githubClient.MakeRequestWithBackoff(req)
	if err != nil {
		return nil, fmt.Errorf("failed to make request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("GitHub API error: %d - %s", resp.StatusCode, string(body))
	}

	var hook Hook
	if err := json.NewDecoder(resp.Body).Decode(&hook); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &hook, nil
}

// DeleteWebhook removes a webhook from a repository
func (wm *WebhookManager) DeleteWebhook(owner, repo string, hookID int64) error {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/hooks/%d", owner, repo, hookID)
	
	req, err := http.NewRequest("DELETE", url, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "token "+wm.githubClient.Token)
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	resp, err := wm.githubClient.MakeRequestWithBackoff(req)
	if err != nil {
		return fmt.Errorf("failed to make request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("GitHub API error: %d - %s", resp.StatusCode, string(body))
	}

	return nil
}

// TestWebhook sends a test ping to verify webhook connectivity
func (wm *WebhookManager) TestWebhook(owner, repo string, hookID int64) error {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/hooks/%d/tests", owner, repo, hookID)
	
	req, err := http.NewRequest("POST", url, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "token "+wm.githubClient.Token)
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	resp, err := wm.githubClient.MakeRequestWithBackoff(req)
	if err != nil {
		return fmt.Errorf("failed to make request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("GitHub API error: %d - %s", resp.StatusCode, string(body))
	}

	return nil
}

// CleanupOldWebhooks removes webhooks pointing to different URLs than the current one, and removes duplicates
func (wm *WebhookManager) CleanupOldWebhooks(owner, repo, currentURL string) error {
	hooks, err := wm.ListWebhooks(owner, repo)
	if err != nil {
		return fmt.Errorf("failed to list webhooks: %w", err)
	}

	log.Printf("DEBUG: Checking %d webhook(s) for cleanup in %s/%s (current URL: %s)", len(hooks), owner, repo, currentURL)

	deletedCount := 0
	foundCurrentURL := false

	for _, hook := range hooks {
		log.Printf("DEBUG: Found webhook ID %d with URL: %s", hook.ID, hook.Config.URL)

		// Keep only the first webhook with the current URL, delete any duplicates or different URLs
		if hook.Config.URL == currentURL {
			if !foundCurrentURL {
				// This is the first occurrence of the current URL, keep it
				log.Printf("DEBUG: Keeping webhook (ID: %d) with current URL", hook.ID)
				foundCurrentURL = true
				continue
			}
			// This is a duplicate of the current URL
			log.Printf("Cleaning up duplicate webhook (ID: %d, URL: %s) from %s/%s", hook.ID, hook.Config.URL, owner, repo)
		} else {
			// Different URL, delete it
			log.Printf("Cleaning up old webhook (ID: %d, URL: %s) from %s/%s", hook.ID, hook.Config.URL, owner, repo)
		}

		if err := wm.DeleteWebhook(owner, repo, hook.ID); err != nil {
			log.Printf("Warning: failed to delete webhook %d from %s/%s: %v", hook.ID, owner, repo, err)
			continue // Try to delete others even if one fails
		}
		deletedCount++
	}

	if deletedCount > 0 {
		log.Printf("Cleaned up %d old/duplicate webhook(s) from %s/%s", deletedCount, owner, repo)
	} else {
		log.Printf("DEBUG: No old webhooks to clean up for %s/%s", owner, repo)
	}

	return nil
}

// FindWebhookByURL searches for a webhook with the specified URL
func (wm *WebhookManager) FindWebhookByURL(owner, repo, targetURL string) (*Hook, error) {
	hooks, err := wm.ListWebhooks(owner, repo)
	if err != nil {
		return nil, fmt.Errorf("failed to list webhooks: %w", err)
	}

	for _, hook := range hooks {
		if hook.Config.URL == targetURL {
			return &hook, nil
		}
	}

	return nil, nil // Not found
}

// EnsureWebhook creates a webhook if it doesn't exist, or updates if it exists with different configuration
func (wm *WebhookManager) EnsureWebhook(owner, repo, webhookURL, secret string, events []string) (*Hook, bool, error) {
	// Validate token permissions first
	hasAccess, err := wm.githubClient.HasScopeForWebhooks()
	if err != nil {
		log.Printf("Warning: Could not validate webhook permissions: %v", err)
		// Continue anyway for fine-grained tokens where scope detection may not work
	} else if !hasAccess {
		return nil, false, fmt.Errorf("token lacks required permissions for webhook management. Required: repo scope or write:repo_hook scope")
	}

	// Cleanup old webhooks if configured
	if wm.config.CleanupOldWebhooks {
		if err := wm.CleanupOldWebhooks(owner, repo, webhookURL); err != nil {
			log.Printf("Warning: failed to cleanup old webhooks for %s/%s: %v", owner, repo, err)
			// Continue anyway - this is not critical
		}
	}

	// Check if webhook already exists
	existingHook, err := wm.FindWebhookByURL(owner, repo, webhookURL)
	if err != nil {
		return nil, false, fmt.Errorf("failed to check existing webhooks: %w", err)
	}

	if existingHook != nil {
		// Webhook exists, check if configuration matches
		eventsMatch := equalStringSlices(existingHook.Events, events)
		
		// GitHub API returns '********' for security, so we can't compare actual secrets
		// Assume secret is correct if it's masked, only fail if it's explicitly different and not masked
		secretMatch := true
		if existingHook.Config.Secret != "" && existingHook.Config.Secret != "********" {
			secretMatch = existingHook.Config.Secret == secret
		}
		
		if eventsMatch && secretMatch && existingHook.Active {
			log.Printf("Webhook already exists and is properly configured for %s/%s", owner, repo)
			return existingHook, false, nil
		}

		// Configuration differs, update it
		log.Printf("Updating existing webhook configuration for %s/%s", owner, repo)
		updatedHook, err := wm.UpdateWebhook(owner, repo, existingHook.ID, webhookURL, secret, events, true)
		if err != nil {
			return nil, false, fmt.Errorf("failed to update webhook: %w", err)
		}
		return updatedHook, true, nil
	}

	// Webhook doesn't exist, create it
	log.Printf("Creating new webhook for %s/%s", owner, repo)
	newHook, err := wm.CreateWebhook(owner, repo, webhookURL, secret, events)
	if err != nil {
		return nil, false, fmt.Errorf("failed to create webhook: %w", err)
	}
	
	return newHook, true, nil
}

// UpdateWebhook updates an existing webhook configuration
func (wm *WebhookManager) UpdateWebhook(owner, repo string, hookID int64, webhookURL, secret string, events []string, active bool) (*Hook, error) {
	if len(events) == 0 {
		events = []string{"workflow_job"} // Default to workflow_job events for job-level scaling
	}

	webhookRequest := WebhookCreateRequest{
		Name:   "web",
		Active: active,
		Events: events,
		Config: HookConfig{
			URL:         webhookURL,
			ContentType: "json",
			Secret:      secret,
			InsecureSSL: "0",
		},
	}

	payload, err := json.Marshal(webhookRequest)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/hooks/%d", owner, repo, hookID)
	
	req, err := http.NewRequest("PATCH", url, bytes.NewBuffer(payload))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "token "+wm.githubClient.Token)
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	req.Header.Set("Content-Type", "application/json")

	resp, err := wm.githubClient.MakeRequestWithBackoff(req)
	if err != nil {
		return nil, fmt.Errorf("failed to make request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("GitHub API error: %d - %s", resp.StatusCode, string(body))
	}

	var hook Hook
	if err := json.NewDecoder(resp.Body).Decode(&hook); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &hook, nil
}

// GetWebhookDeliveries retrieves recent webhook delivery attempts
func (wm *WebhookManager) GetWebhookDeliveries(owner, repo string, hookID int64, perPage int) ([]WebhookDelivery, error) {
	if perPage <= 0 || perPage > 100 {
		perPage = 30 // Default per page
	}

	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/hooks/%d/deliveries?per_page=%d", owner, repo, hookID, perPage)
	
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "token "+wm.githubClient.Token)
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	resp, err := wm.githubClient.MakeRequestWithBackoff(req)
	if err != nil {
		return nil, fmt.Errorf("failed to make request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("GitHub API error: %d - %s", resp.StatusCode, string(body))
	}

	var deliveries []WebhookDelivery
	if err := json.NewDecoder(resp.Body).Decode(&deliveries); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return deliveries, nil
}

// WebhookDelivery represents a webhook delivery attempt
type WebhookDelivery struct {
	ID           int64  `json:"id"`
	GUID         string `json:"guid"`
	DeliveredAt  string `json:"delivered_at"`
	Redelivery   bool   `json:"redelivery"`
	Duration     float64 `json:"duration"`
	Status       string `json:"status"`
	StatusCode   int    `json:"status_code"`
	Event        string `json:"event"`
	Action       string `json:"action"`
	Installation *struct {
		ID int64 `json:"id"`
	} `json:"installation,omitempty"`
}

// GetRepositoryPermissions checks what permissions the current token has for a repository
func (wm *WebhookManager) GetRepositoryPermissions(owner, repo string) (*RepositoryPermissions, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s", owner, repo)
	
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "token "+wm.githubClient.Token)
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	resp, err := wm.githubClient.MakeRequestWithBackoff(req)
	if err != nil {
		return nil, fmt.Errorf("failed to make request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("GitHub API error: %d - %s", resp.StatusCode, string(body))
	}

	var repoInfo struct {
		Permissions *RepositoryPermissions `json:"permissions"`
	}
	
	if err := json.NewDecoder(resp.Body).Decode(&repoInfo); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return repoInfo.Permissions, nil
}

// RepositoryPermissions represents user permissions for a repository
type RepositoryPermissions struct {
	Admin    bool `json:"admin"`
	Maintain bool `json:"maintain"`
	Push     bool `json:"push"`
	Triage   bool `json:"triage"`
	Pull     bool `json:"pull"`
}

// CanManageWebhooks checks if the current token has permissions to manage webhooks
func (wm *WebhookManager) CanManageWebhooks(owner, repo string) (bool, error) {
	permissions, err := wm.GetRepositoryPermissions(owner, repo)
	if err != nil {
		return false, fmt.Errorf("failed to get repository permissions: %w", err)
	}

	if permissions == nil {
		return false, fmt.Errorf("no permissions information available for repository")
	}

	// Webhook management requires admin or maintain permissions
	return permissions.Admin || permissions.Maintain, nil
}

// ValidateWebhookConfiguration checks if a webhook configuration is valid
func (wm *WebhookManager) ValidateWebhookConfiguration(webhookURL, secret string, events []string) error {
	if webhookURL == "" {
		return fmt.Errorf("webhook URL cannot be empty")
	}

	if !strings.HasPrefix(webhookURL, "https://") && !strings.HasPrefix(webhookURL, "http://") {
		return fmt.Errorf("webhook URL must be a valid HTTP/HTTPS URL")
	}

	if len(events) == 0 {
		return fmt.Errorf("at least one event must be specified")
	}

	validEvents := map[string]bool{
		"workflow_run": true,
		"workflow_job": true,
		"push": true,
		"pull_request": true,
		"issues": true,
		"issue_comment": true,
		"release": true,
		"ping": true,
	}

	for _, event := range events {
		if !validEvents[event] {
			log.Printf("Warning: event '%s' may not be valid for GitHub webhooks", event)
		}
	}

	return nil
}

// Auto-setup functionality for repositories
func (wm *WebhookManager) AutoSetupWebhookForOwner(ownerName string, baseWebhookURL string) error {
	ownerConfig, exists := wm.config.Owners[ownerName]
	if !exists {
		return fmt.Errorf("owner configuration not found for: %s", ownerName)
	}

	githubClient := github.NewClient(ownerConfig.GitHubToken)

	if ownerConfig.AllRepos {
		return wm.setupWebhooksForAllRepos(githubClient, ownerName, baseWebhookURL, ownerConfig.WebhookSecret)
	} else if ownerConfig.RepoName != "" {
		// Setup webhook for specific repository
		err := wm.setupWebhookForRepository(githubClient, ownerName, ownerConfig.RepoName, baseWebhookURL, ownerConfig.WebhookSecret)
		if err != nil {
			return fmt.Errorf("failed to setup webhook for %s/%s: %w", ownerName, ownerConfig.RepoName, err)
		}
		log.Printf("Webhook setup completed for repository %s/%s", ownerName, ownerConfig.RepoName)
	} else {
		return fmt.Errorf("no repository configuration found for owner %s", ownerName)
	}

	return nil
}

// setupWebhooksForAllRepos sets up webhooks for all repositories accessible to the user
func (wm *WebhookManager) setupWebhooksForAllRepos(githubClient *github.Client, ownerName string, baseWebhookURL string, webhookSecret string) error {
	// Perform conservative repository discovery
	ownedRepos, accessibleRepos, err := wm.ConservativeRepoDiscovery(githubClient, ownerName)
	if err != nil {
		return fmt.Errorf("failed to discover repositories for owner %s: %w", ownerName, err)
	}

	var errors []string
	successCount := 0
	skipCount := 0
	totalRepos := len(ownedRepos) + len(accessibleRepos)

	// Process owned repositories first (highest priority)
	for _, repo := range ownedRepos {
		parts := strings.Split(repo.FullName, "/")
		owner, repoName := parts[0], parts[1]
		
		err := wm.setupWebhookForRepository(githubClient, owner, repoName, baseWebhookURL, webhookSecret)
		if err != nil {
			errors = append(errors, fmt.Sprintf("%s/%s (owned): %v", owner, repoName, err))
		} else {
			successCount++
		}
	}

	// Process accessible repositories (with spaced API calls)
	if len(accessibleRepos) > 0 {
		log.Printf("Checking accessible repositories for webhook management permissions...")
		log.Printf("Processing %d accessible repositories with API call spacing to conserve rate limit", len(accessibleRepos))
		
		for i, repo := range accessibleRepos {
			// Add delay between repositories to space out API calls
			if i > 0 {
				delay := wm.calculateAPIDelay(githubClient, i)
				if delay > 0 {
					log.Printf("Spacing API calls: waiting %v before processing %s (repo %d/%d)", 
						delay, repo.FullName, i+1, len(accessibleRepos))
					time.Sleep(delay)
				}
			}
			
			// Check rate limit periodically
			if i%5 == 0 {
				remaining, resetTime, _ := githubClient.GetRateLimitInfo()
				log.Printf("Rate limit status at repo %d/%d: %d remaining (resets at %s)", 
					i+1, len(accessibleRepos), remaining, resetTime.Format("15:04:05"))
				
				if remaining < 10 {
					log.Printf("Rate limit too low (%d remaining), stopping accessible repository processing at %d/%d", 
						remaining, i, len(accessibleRepos))
					skipCount += len(accessibleRepos) - i
					break
				}
			}
			
			parts := strings.Split(repo.FullName, "/")
			owner, repoName := parts[0], parts[1]
			
			// Check if we have admin/maintain permissions on this repository
			tempManager := &WebhookManager{githubClient: githubClient, config: wm.config}
			canManage, err := tempManager.CanManageWebhooks(owner, repoName)
			if err != nil {
				errors = append(errors, fmt.Sprintf("%s/%s (accessible): failed to check permissions: %v", owner, repoName, err))
				continue
			}
			
			if !canManage {
				log.Printf("Skipping %s/%s: insufficient permissions for webhook management", owner, repoName)
				skipCount++
				continue
			}
			
			err = wm.setupWebhookForRepository(githubClient, owner, repoName, baseWebhookURL, webhookSecret)
			if err != nil {
				errors = append(errors, fmt.Sprintf("%s/%s (accessible): %v", owner, repoName, err))
			} else {
				successCount++
			}
		}
	}

	// Log comprehensive results
	log.Printf("Webhook setup completed for owner %s:", ownerName)
	log.Printf("  - %d successful", successCount)
	log.Printf("  - %d failed", len(errors))
	log.Printf("  - %d skipped (insufficient permissions)", skipCount)
	log.Printf("  - %d total repositories processed", totalRepos)
	
	if len(errors) > 0 {
		// Don't fail completely if some repos succeed
		if successCount > 0 {
			log.Printf("Partial success: %d webhooks created, but %d failed: %s", 
				successCount, len(errors), strings.Join(errors, "; "))
			return nil
		}
		return fmt.Errorf("all webhook setups failed: %s", strings.Join(errors, "; "))
	}

	return nil
}

// setupWebhookForRepository sets up a webhook for a specific repository
func (wm *WebhookManager) setupWebhookForRepository(githubClient *github.Client, owner, repo, baseWebhookURL, secret string) error {
	tempManager := &WebhookManager{
		githubClient: githubClient,
		config:       wm.config,
	}

	// Check if repository has workflows that use self-hosted runners
	hasSelfHosted, err := githubClient.HasSelfHostedRunners(owner, repo)
	if err != nil {
		log.Printf("Warning: failed to check workflows for %s/%s: %v. Skipping webhook setup.", owner, repo, err)
		return nil // Don't fail completely, just skip this repo
	}

	if !hasSelfHosted {
		log.Printf("Skipping %s/%s: no workflows use self-hosted runners", owner, repo)
		return nil
	}

	log.Printf("Repository %s/%s uses self-hosted runners, setting up webhook", owner, repo)

	// Validate permissions first
	canManage, err := tempManager.CanManageWebhooks(owner, repo)
	if err != nil {
		return fmt.Errorf("failed to check permissions: %w", err)
	}
	
	if !canManage {
		return fmt.Errorf("insufficient permissions to manage webhooks for %s/%s", owner, repo)
	}

	// Setup webhook
	events := []string{"workflow_job"}
	_, created, err := tempManager.EnsureWebhook(owner, repo, baseWebhookURL, secret, events)
	if err != nil {
		return fmt.Errorf("failed to ensure webhook: %w", err)
	}

	if created {
		log.Printf("Successfully created/updated webhook for %s/%s", owner, repo)
	} else {
		log.Printf("Webhook already properly configured for %s/%s", owner, repo)
	}

	return nil
}

// estimateAPICallsNeeded estimates the number of API calls required for webhook setup
func (wm *WebhookManager) estimateAPICallsNeeded(repos []github.Repository, ownerName string) int {
	ownedCount := 0
	accessibleCount := 0
	
	for _, repo := range repos {
		parts := strings.Split(repo.FullName, "/")
		if len(parts) == 2 && parts[0] == ownerName {
			ownedCount++
		} else {
			accessibleCount++
		}
	}
	
	// Estimate API calls:
	// - 1 call per repo to check repository permissions (for accessible repos)
	// - 1 call per repo to list existing webhooks
	// - 1 call per repo to create/update webhook (if needed)
	// - Buffer for error handling and retries
	
	ownedCalls := ownedCount * 2     // List webhooks + create/update (owned repos skip permission check)
	accessibleCalls := accessibleCount * 3  // Permission check + list webhooks + create/update
	buffer := len(repos) / 10        // 10% buffer for retries and error handling
	
	total := ownedCalls + accessibleCalls + buffer
	log.Printf("API call estimation: %d owned repos (%d calls), %d accessible repos (%d calls), %d buffer = %d total", 
		ownedCount, ownedCalls, accessibleCount, accessibleCalls, buffer, total)
	
	return total
}

// calculateAPIDelay determines how long to wait between API calls to space them out
func (wm *WebhookManager) calculateAPIDelay(githubClient *github.Client, repoIndex int) time.Duration {
	remaining, _, waitTime := githubClient.GetRateLimitInfo()
	
	// Base delay to ensure we don't make rapid-fire API calls
	baseDelay := 500 * time.Millisecond
	
	// If we have plenty of rate limit remaining, use minimal delay
	if remaining > 1000 {
		return baseDelay
	}
	
	// If rate limit is getting low, increase delays
	if remaining < 500 {
		// Calculate how much time we have until reset
		if waitTime > 0 && waitTime < 2*time.Hour {
			// Spread remaining calls over the reset period
			delayPerCall := waitTime / time.Duration(remaining)
			if delayPerCall > 10*time.Second {
				delayPerCall = 10 * time.Second // Cap at 10 seconds
			}
			if delayPerCall < baseDelay {
				delayPerCall = baseDelay // Minimum delay
			}
			return delayPerCall
		}
		
		// If reset time is far away or unknown, use conservative delay
		return 2 * time.Second
	}
	
	// Moderate rate limit - use graduated delay based on how low we are
	if remaining < 1000 {
		multiplier := (1000 - remaining) / 100 // Scale from 1-10x
		if multiplier < 1 {
			multiplier = 1
		}
		if multiplier > 10 {
			multiplier = 10
		}
		return time.Duration(multiplier) * baseDelay
	}
	
	return baseDelay
}

// SpacedRepositoryProcessing processes repositories with intelligent API call spacing
func (wm *WebhookManager) SpacedRepositoryProcessing(githubClient *github.Client, repos []github.Repository, ownerName string, 
	processFunc func(repo github.Repository) error) (int, int, []string) {
	
	var errors []string
	successCount := 0
	skipCount := 0
	
	log.Printf("Processing %d repositories with intelligent API call spacing", len(repos))
	startTime := time.Now()
	
	for i, repo := range repos {
		// Add spacing between API calls
		if i > 0 {
			delay := wm.calculateAPIDelay(githubClient, i)
			if delay > 0 {
				time.Sleep(delay)
			}
		}
		
		// Periodic rate limit checks and logging
		if i%10 == 0 || i == len(repos)-1 {
			remaining, resetTime, _ := githubClient.GetRateLimitInfo()
			elapsed := time.Since(startTime)
			avgTime := elapsed / time.Duration(i+1)
			estimatedTotal := avgTime * time.Duration(len(repos))
			
			log.Printf("Progress: %d/%d repos, %v elapsed, ~%v estimated total, %d API calls remaining (resets %s)", 
				i+1, len(repos), elapsed.Round(time.Second), estimatedTotal.Round(time.Second), 
				remaining, resetTime.Format("15:04:05"))
			
			if remaining < 5 {
				log.Printf("Rate limit critically low (%d remaining), stopping processing at %d/%d", 
					remaining, i, len(repos))
				skipCount += len(repos) - i
				break
			}
		}
		
		// Process the repository
		err := processFunc(repo)
		if err != nil {
			errors = append(errors, fmt.Sprintf("%s: %v", repo.FullName, err))
		} else {
			successCount++
		}
	}
	
	totalTime := time.Since(startTime)
	log.Printf("github.Repository processing completed in %v: %d successful, %d failed, %d skipped", 
		totalTime.Round(time.Second), successCount, len(errors), skipCount)
	
	return successCount, skipCount, errors
}

// ConservativeRepoDiscovery performs repository discovery with rate limit awareness
func (wm *WebhookManager) ConservativeRepoDiscovery(githubClient *github.Client, ownerName string) ([]github.Repository, []github.Repository, error) {
	// Check initial rate limit
	remaining, resetTime, waitTime := githubClient.GetRateLimitInfo()
	log.Printf("Starting repository discovery with %d API calls remaining (resets in %v)", remaining, waitTime)
	
	if remaining < 10 {
		return nil, nil, fmt.Errorf("insufficient rate limit for repository discovery: %d remaining (resets at %s)", 
			remaining, resetTime.Format("15:04:05"))
	}
	
	// Get repositories (this uses 1 API call)
	repos, err := githubClient.GetRepositories(ownerName)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get repositories: %w", err)
	}
	
	// Separate owned vs accessible repositories (no API calls)
	ownedRepos := []github.Repository{}
	accessibleRepos := []github.Repository{}
	
	for _, repo := range repos {
		parts := strings.Split(repo.FullName, "/")
		if len(parts) != 2 {
			continue
		}
		
		repoOwner := parts[0]
		if repoOwner == ownerName {
			ownedRepos = append(ownedRepos, repo)
		} else {
			accessibleRepos = append(accessibleRepos, repo)
		}
	}
	
	log.Printf("github.Repository discovery completed: %d owned, %d accessible, %d total", 
		len(ownedRepos), len(accessibleRepos), len(repos))
	
	// Update rate limit info after discovery
	remaining, _, _ = githubClient.GetRateLimitInfo()
	log.Printf("Rate limit after discovery: %d remaining", remaining)
	
	return ownedRepos, accessibleRepos, nil
}

// Utility function to compare string slices
func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	
	aMap := make(map[string]bool)
	for _, v := range a {
		aMap[v] = true
	}
	
	for _, v := range b {
		if !aMap[v] {
			return false
		}
	}
	
	return true
}
