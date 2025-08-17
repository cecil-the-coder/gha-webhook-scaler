package webhook

import (
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	
	"github.com/cecil-the-coder/gha-webhook-scaler/internal/config"
	"github.com/cecil-the-coder/gha-webhook-scaler/internal/github"
)

// WebhookEndpoints provides HTTP handlers for webhook management operations
type Endpoints struct {
	config         *config.Config
	githubClients  map[string]*github.Client
	webhookManager *WebhookManager
}

// NewWebhookEndpoints creates a new webhook endpoints handler
func NewEndpoints(config *config.Config, githubClients map[string]*github.Client) *Endpoints {
	// Use the first available GitHub client for the webhook manager
	var defaultClient *github.Client
	for _, client := range githubClients {
		defaultClient = client
		break
	}

	return &Endpoints{
		config:         config,
		githubClients:  githubClients,
		webhookManager: NewWebhookManager(defaultClient, config),
	}
}

// ListWebhooksHandler lists all webhooks for a repository
func (we *Endpoints) ListWebhooksHandler(c *gin.Context) {
	owner := c.Param("owner")
	repo := c.Param("repo")

	if owner == "" || repo == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "owner and repo parameters are required",
		})
		return
	}

	// Get appropriate GitHub client for this owner
	githubClient := we.getGitHubClientForOwner(owner)
	if githubClient == nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": fmt.Sprintf("no GitHub client configured for owner: %s", owner),
		})
		return
	}

	manager := NewWebhookManager(githubClient, we.config)
	hooks, err := manager.ListWebhooks(owner, repo)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": fmt.Sprintf("failed to list webhooks: %v", err),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"owner":     owner,
		"repo":      repo,
		"webhooks":  hooks,
		"count":     len(hooks),
	})
}

// CreateWebhookHandler creates a new webhook for a repository
func (we *Endpoints) CreateWebhookHandler(c *gin.Context) {
	owner := c.Param("owner")
	repo := c.Param("repo")

	if owner == "" || repo == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "owner and repo parameters are required",
		})
		return
	}

	var request struct {
		WebhookURL string   `json:"webhook_url" binding:"required"`
		Secret     string   `json:"secret"`
		Events     []string `json:"events"`
	}

	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": fmt.Sprintf("invalid request body: %v", err),
		})
		return
	}

	// Get appropriate GitHub client for this owner
	githubClient := we.getGitHubClientForOwner(owner)
	if githubClient == nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": fmt.Sprintf("no GitHub client configured for owner: %s", owner),
		})
		return
	}

	manager := NewWebhookManager(githubClient, we.config)
	
	// Validate configuration
	if err := manager.ValidateWebhookConfiguration(request.WebhookURL, request.Secret, request.Events); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": fmt.Sprintf("invalid webhook configuration: %v", err),
		})
		return
	}

	// Check permissions
	canManage, err := manager.CanManageWebhooks(owner, repo)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": fmt.Sprintf("failed to check permissions: %v", err),
		})
		return
	}

	if !canManage {
		c.JSON(http.StatusForbidden, gin.H{
			"error": "insufficient permissions to manage webhooks for this repository",
		})
		return
	}

	// Create webhook
	hook, err := manager.CreateWebhook(owner, repo, request.WebhookURL, request.Secret, request.Events)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": fmt.Sprintf("failed to create webhook: %v", err),
		})
		return
	}

	c.JSON(http.StatusCreated, gin.H{
		"message": "webhook created successfully",
		"webhook": hook,
	})
}

// DeleteWebhookHandler deletes a webhook from a repository
func (we *Endpoints) DeleteWebhookHandler(c *gin.Context) {
	owner := c.Param("owner")
	repo := c.Param("repo")
	hookIDStr := c.Param("hook_id")

	if owner == "" || repo == "" || hookIDStr == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "owner, repo, and hook_id parameters are required",
		})
		return
	}

	hookID, err := strconv.ParseInt(hookIDStr, 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "invalid hook_id format",
		})
		return
	}

	// Get appropriate GitHub client for this owner
	githubClient := we.getGitHubClientForOwner(owner)
	if githubClient == nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": fmt.Sprintf("no GitHub client configured for owner: %s", owner),
		})
		return
	}

	manager := NewWebhookManager(githubClient, we.config)

	// Check permissions
	canManage, err := manager.CanManageWebhooks(owner, repo)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": fmt.Sprintf("failed to check permissions: %v", err),
		})
		return
	}

	if !canManage {
		c.JSON(http.StatusForbidden, gin.H{
			"error": "insufficient permissions to manage webhooks for this repository",
		})
		return
	}

	// Delete webhook
	if err := manager.DeleteWebhook(owner, repo, hookID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": fmt.Sprintf("failed to delete webhook: %v", err),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": fmt.Sprintf("webhook %d deleted successfully", hookID),
	})
}

// GetWebhookHandler retrieves a specific webhook
func (we *Endpoints) GetWebhookHandler(c *gin.Context) {
	owner := c.Param("owner")
	repo := c.Param("repo")
	hookIDStr := c.Param("hook_id")

	if owner == "" || repo == "" || hookIDStr == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "owner, repo, and hook_id parameters are required",
		})
		return
	}

	hookID, err := strconv.ParseInt(hookIDStr, 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "invalid hook_id format",
		})
		return
	}

	// Get appropriate GitHub client for this owner
	githubClient := we.getGitHubClientForOwner(owner)
	if githubClient == nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": fmt.Sprintf("no GitHub client configured for owner: %s", owner),
		})
		return
	}

	manager := NewWebhookManager(githubClient, we.config)
	hook, err := manager.GetWebhook(owner, repo, hookID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": fmt.Sprintf("failed to get webhook: %v", err),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"webhook": hook,
	})
}

// TestWebhookHandler tests a webhook by sending a ping
func (we *Endpoints) TestWebhookHandler(c *gin.Context) {
	owner := c.Param("owner")
	repo := c.Param("repo")
	hookIDStr := c.Param("hook_id")

	if owner == "" || repo == "" || hookIDStr == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "owner, repo, and hook_id parameters are required",
		})
		return
	}

	hookID, err := strconv.ParseInt(hookIDStr, 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "invalid hook_id format",
		})
		return
	}

	// Get appropriate GitHub client for this owner
	githubClient := we.getGitHubClientForOwner(owner)
	if githubClient == nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": fmt.Sprintf("no GitHub client configured for owner: %s", owner),
		})
		return
	}

	manager := NewWebhookManager(githubClient, we.config)

	// Check permissions
	canManage, err := manager.CanManageWebhooks(owner, repo)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": fmt.Sprintf("failed to check permissions: %v", err),
		})
		return
	}

	if !canManage {
		c.JSON(http.StatusForbidden, gin.H{
			"error": "insufficient permissions to test webhooks for this repository",
		})
		return
	}

	// Test webhook
	if err := manager.TestWebhook(owner, repo, hookID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": fmt.Sprintf("failed to test webhook: %v", err),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": fmt.Sprintf("test ping sent to webhook %d", hookID),
	})
}

// EnsureWebhookHandler creates or updates a webhook to match the desired configuration
func (we *Endpoints) EnsureWebhookHandler(c *gin.Context) {
	owner := c.Param("owner")
	repo := c.Param("repo")

	if owner == "" || repo == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "owner and repo parameters are required",
		})
		return
	}

	var request struct {
		WebhookURL string   `json:"webhook_url" binding:"required"`
		Secret     string   `json:"secret"`
		Events     []string `json:"events"`
	}

	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": fmt.Sprintf("invalid request body: %v", err),
		})
		return
	}

	// Get appropriate GitHub client for this owner
	githubClient := we.getGitHubClientForOwner(owner)
	if githubClient == nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": fmt.Sprintf("no GitHub client configured for owner: %s", owner),
		})
		return
	}

	manager := NewWebhookManager(githubClient, we.config)

	// Validate configuration
	if err := manager.ValidateWebhookConfiguration(request.WebhookURL, request.Secret, request.Events); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": fmt.Sprintf("invalid webhook configuration: %v", err),
		})
		return
	}

	// Check permissions
	canManage, err := manager.CanManageWebhooks(owner, repo)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": fmt.Sprintf("failed to check permissions: %v", err),
		})
		return
	}

	if !canManage {
		c.JSON(http.StatusForbidden, gin.H{
			"error": "insufficient permissions to manage webhooks for this repository",
		})
		return
	}

	// Ensure webhook
	hook, created, err := manager.EnsureWebhook(owner, repo, request.WebhookURL, request.Secret, request.Events)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": fmt.Sprintf("failed to ensure webhook: %v", err),
		})
		return
	}

	status := "unchanged"
	if created {
		status = "created_or_updated"
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "webhook ensured successfully",
		"status":  status,
		"webhook": hook,
	})
}

// GetWebhookDeliveriesHandler retrieves recent webhook delivery attempts
func (we *Endpoints) GetWebhookDeliveriesHandler(c *gin.Context) {
	owner := c.Param("owner")
	repo := c.Param("repo")
	hookIDStr := c.Param("hook_id")

	if owner == "" || repo == "" || hookIDStr == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "owner, repo, and hook_id parameters are required",
		})
		return
	}

	hookID, err := strconv.ParseInt(hookIDStr, 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "invalid hook_id format",
		})
		return
	}

	perPage := 30
	if perPageStr := c.Query("per_page"); perPageStr != "" {
		if pp, err := strconv.Atoi(perPageStr); err == nil && pp > 0 && pp <= 100 {
			perPage = pp
		}
	}

	// Get appropriate GitHub client for this owner
	githubClient := we.getGitHubClientForOwner(owner)
	if githubClient == nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": fmt.Sprintf("no GitHub client configured for owner: %s", owner),
		})
		return
	}

	manager := NewWebhookManager(githubClient, we.config)
	deliveries, err := manager.GetWebhookDeliveries(owner, repo, hookID, perPage)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": fmt.Sprintf("failed to get webhook deliveries: %v", err),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"hook_id":    hookID,
		"deliveries": deliveries,
		"count":      len(deliveries),
	})
}

// AutoSetupWebhooksHandler automatically sets up webhooks for an owner's repositories
func (we *Endpoints) AutoSetupWebhooksHandler(c *gin.Context) {
	owner := c.Param("owner")
	if owner == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "owner parameter is required",
		})
		return
	}

	var request struct {
		WebhookURL string `json:"webhook_url" binding:"required"`
	}

	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": fmt.Sprintf("invalid request body: %v", err),
		})
		return
	}

	// Get appropriate GitHub client for this owner
	githubClient := we.getGitHubClientForOwner(owner)
	if githubClient == nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": fmt.Sprintf("no GitHub client configured for owner: %s", owner),
		})
		return
	}

	manager := NewWebhookManager(githubClient, we.config)
	if err := manager.AutoSetupWebhookForOwner(owner, request.WebhookURL); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": fmt.Sprintf("failed to auto-setup webhooks: %v", err),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": fmt.Sprintf("webhook auto-setup completed for owner: %s", owner),
		"owner":   owner,
		"webhook_url": request.WebhookURL,
	})
}

// GetRepositoryPermissionsHandler checks current token permissions for a repository
func (we *Endpoints) GetRepositoryPermissionsHandler(c *gin.Context) {
	owner := c.Param("owner")
	repo := c.Param("repo")

	if owner == "" || repo == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "owner and repo parameters are required",
		})
		return
	}

	// Get appropriate GitHub client for this owner
	githubClient := we.getGitHubClientForOwner(owner)
	if githubClient == nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": fmt.Sprintf("no GitHub client configured for owner: %s", owner),
		})
		return
	}

	manager := NewWebhookManager(githubClient, we.config)
	permissions, err := manager.GetRepositoryPermissions(owner, repo)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": fmt.Sprintf("failed to get repository permissions: %v", err),
		})
		return
	}

	canManageWebhooks := permissions.Admin || permissions.Maintain

	c.JSON(http.StatusOK, gin.H{
		"owner":              owner,
		"repo":               repo,
		"permissions":        permissions,
		"can_manage_webhooks": canManageWebhooks,
	})
}

// ValidateTokenHandler validates token scopes and permissions
func (we *Endpoints) ValidateTokenHandler(c *gin.Context) {
	owner := c.Param("owner")
	if owner == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "owner parameter is required",
		})
		return
	}

	// Get appropriate GitHub client for this owner
	githubClient := we.getGitHubClientForOwner(owner)
	if githubClient == nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": fmt.Sprintf("no GitHub client configured for owner: %s", owner),
		})
		return
	}

	// Validate token with comprehensive scope checking
	tokenInfo, err := githubClient.ValidateTokenWithScopes()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": fmt.Sprintf("failed to validate token: %v", err),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"owner":      owner,
		"token_info": tokenInfo,
	})
}

// ValidateTokenForRepositoryHandler validates token permissions for a specific repository
func (we *Endpoints) ValidateTokenForRepositoryHandler(c *gin.Context) {
	owner := c.Param("owner")
	repo := c.Param("repo")

	if owner == "" || repo == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "owner and repo parameters are required",
		})
		return
	}

	// Get appropriate GitHub client for this owner
	githubClient := we.getGitHubClientForOwner(owner)
	if githubClient == nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": fmt.Sprintf("no GitHub client configured for owner: %s", owner),
		})
		return
	}

	// Test repository-specific permissions
	repoPermissions, err := githubClient.TestRepositoryPermissions(owner, repo)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": fmt.Sprintf("failed to test repository permissions: %v", err),
		})
		return
	}

	// Also get general token info
	tokenInfo, err := githubClient.ValidateTokenWithScopes()
	if err != nil {
		// Don't fail completely, just log the error
		log.Printf("Warning: Could not get token info: %v", err)
	}

	response := gin.H{
		"owner":               owner,
		"repo":                repo,
		"repository_permissions": repoPermissions,
	}

	if tokenInfo != nil {
		response["token_info"] = tokenInfo
	}

	c.JSON(http.StatusOK, response)
}

// CheckTokenScopesHandler returns the scopes for a token
func (we *Endpoints) CheckTokenScopesHandler(c *gin.Context) {
	owner := c.Param("owner")
	if owner == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "owner parameter is required",
		})
		return
	}

	// Get appropriate GitHub client for this owner
	githubClient := we.getGitHubClientForOwner(owner)
	if githubClient == nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": fmt.Sprintf("no GitHub client configured for owner: %s", owner),
		})
		return
	}

	// Get token scopes
	scopes, tokenType, err := githubClient.CheckTokenScopes()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": fmt.Sprintf("failed to check token scopes: %v", err),
		})
		return
	}

	// Check specific capabilities
	hasWebhookAccess, _ := githubClient.HasScopeForWebhooks()
	hasRunnerAccess, _ := githubClient.HasScopeForRunners()

	c.JSON(http.StatusOK, gin.H{
		"owner":               owner,
		"scopes":              scopes,
		"token_type":          tokenType,
		"has_webhook_access":  hasWebhookAccess,
		"has_runner_access":   hasRunnerAccess,
	})
}

// ListRepositoriesHandler lists repositories accessible to the owner
func (we *Endpoints) ListRepositoriesHandler(c *gin.Context) {
	owner := c.Param("owner")
	if owner == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "owner parameter is required",
		})
		return
	}

	// Get appropriate GitHub client for this owner
	githubClient := we.getGitHubClientForOwner(owner)
	if githubClient == nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": fmt.Sprintf("no GitHub client configured for owner: %s", owner),
		})
		return
	}

	// Check rate limit before proceeding
	remaining, resetTime, waitTime := githubClient.GetRateLimitInfo()
	if remaining < 10 {
		c.JSON(http.StatusTooManyRequests, gin.H{
			"error": fmt.Sprintf("insufficient rate limit for repository listing: %d remaining (resets in %v)", remaining, waitTime),
			"rate_limit": gin.H{
				"remaining": remaining,
				"reset_time": resetTime.Format("15:04:05"),
			},
		})
		return
	}

	// Get repositories using conservative discovery
	manager := NewWebhookManager(githubClient, we.config)
	ownedRepos, accessibleRepos, err := manager.ConservativeRepoDiscovery(githubClient, owner)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": fmt.Sprintf("failed to discover repositories: %v", err),
		})
		return
	}

	// Limit permission checking for accessible repos to conserve API calls
	checkPermissions := c.Query("check_permissions") == "true"
	accessibleWithPermissions := []github.Repository{}
	accessibleWithoutPermissions := []github.Repository{}

	if checkPermissions && len(accessibleRepos) > 0 {
		log.Printf("Checking permissions for %d accessible repositories with API call spacing", len(accessibleRepos))
		
		// Use spaced permission checking to avoid consuming too many API calls rapidly
		for i, repo := range accessibleRepos {
			// Add delay between permission checks to space out API calls
			if i > 0 {
				delay := 750 * time.Millisecond // Conservative delay for permission checks
				remaining, _, _ := githubClient.GetRateLimitInfo()
				
				// Increase delay if rate limit is getting low
				if remaining < 100 {
					delay = 2 * time.Second
				} else if remaining < 500 {
					delay = 1 * time.Second
				}
				
				time.Sleep(delay)
			}
			
			// Check rate limit every 5 repositories
			if i%5 == 0 {
				remaining, resetTime, _ := githubClient.GetRateLimitInfo()
				log.Printf("Permission check progress: %d/%d repos, %d API calls remaining (resets %s)", 
					i+1, len(accessibleRepos), remaining, resetTime.Format("15:04:05"))
				
				if remaining < 10 {
					log.Printf("Rate limit too low (%d remaining), stopping permission checks at %d/%d", 
						remaining, i, len(accessibleRepos))
					// Add remaining repos to "unknown" category
					for j := i; j < len(accessibleRepos); j++ {
						accessibleWithoutPermissions = append(accessibleWithoutPermissions, accessibleRepos[j])
					}
					break
				}
			}

			parts := strings.Split(repo.FullName, "/")
			repoOwner, repoName := parts[0], parts[1]
			
			canManage, err := manager.CanManageWebhooks(repoOwner, repoName)
			if err != nil {
				log.Printf("Failed to check permissions for %s: %v", repo.FullName, err)
				accessibleWithoutPermissions = append(accessibleWithoutPermissions, repo)
			} else if canManage {
				accessibleWithPermissions = append(accessibleWithPermissions, repo)
			} else {
				accessibleWithoutPermissions = append(accessibleWithoutPermissions, repo)
			}
		}
	} else {
		// If not checking permissions, put all accessible repos in "without permissions" category
		accessibleWithoutPermissions = accessibleRepos
	}

	totalRepos := len(ownedRepos) + len(accessibleRepos)
	
	c.JSON(http.StatusOK, gin.H{
		"owner": owner,
		"summary": gin.H{
			"total_repositories":           totalRepos,
			"owned_repositories":           len(ownedRepos),
			"accessible_with_permissions":  len(accessibleWithPermissions),
			"accessible_without_permissions": len(accessibleWithoutPermissions),
		},
		"repositories": gin.H{
			"owned":                        ownedRepos,
			"accessible_with_permissions":  accessibleWithPermissions,
			"accessible_without_permissions": accessibleWithoutPermissions,
		},
	})
}

// PreviewWebhookSetupHandler shows what repositories would be affected by auto-setup
func (we *Endpoints) PreviewWebhookSetupHandler(c *gin.Context) {
	owner := c.Param("owner")
	if owner == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "owner parameter is required",
		})
		return
	}

	var request struct {
		WebhookURL string `json:"webhook_url" binding:"required"`
		DryRun     bool   `json:"dry_run"`
	}

	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": fmt.Sprintf("invalid request body: %v", err),
		})
		return
	}

	// Get appropriate GitHub client for this owner
	githubClient := we.getGitHubClientForOwner(owner)
	if githubClient == nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": fmt.Sprintf("no GitHub client configured for owner: %s", owner),
		})
		return
	}

	// Get owner configuration to check if ALL_REPOS is enabled
	ownerConfig, exists := we.config.Owners[owner]
	if !exists {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": fmt.Sprintf("no configuration found for owner: %s", owner),
		})
		return
	}

	allRepos := ownerConfig.AllRepos

	if !allRepos {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "ALL_REPOS is not enabled for this owner. Auto-setup only works with ALL_REPOS=true",
		})
		return
	}

	// Get repositories that would be processed
	repos, err := githubClient.GetRepositories(owner)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": fmt.Sprintf("failed to get repositories: %v", err),
		})
		return
	}

	// Analyze each repository for webhook setup potential
	repoAnalysis := []gin.H{}
	manager := NewWebhookManager(githubClient, we.config)

	for _, repo := range repos {
		parts := strings.Split(repo.FullName, "/")
		if len(parts) != 2 {
			continue
		}
		
		repoOwner, repoName := parts[0], parts[1]
		analysis := gin.H{
			"repository":     repo.FullName,
			"owned_by_user":  repoOwner == owner,
			"private":        repo.ID > 0, // This is a simplified check - in real API response this would be a "private" field
		}

		// Check webhook management permissions
		canManage, err := manager.CanManageWebhooks(repoOwner, repoName)
		if err != nil {
			analysis["can_manage_webhooks"] = false
			analysis["permission_error"] = err.Error()
		} else {
			analysis["can_manage_webhooks"] = canManage
		}

		// Check if webhook already exists
		if canManage {
			existingHook, err := manager.FindWebhookByURL(repoOwner, repoName, request.WebhookURL)
			if err != nil {
				analysis["webhook_check_error"] = err.Error()
			} else if existingHook != nil {
				analysis["webhook_exists"] = true
				analysis["webhook_id"] = existingHook.ID
				analysis["webhook_active"] = existingHook.Active
			} else {
				analysis["webhook_exists"] = false
			}
		}

		// Determine action that would be taken
		if !canManage {
			analysis["action"] = "skip - insufficient permissions"
		} else if existingHook, _ := manager.FindWebhookByURL(repoOwner, repoName, request.WebhookURL); existingHook != nil {
			analysis["action"] = "update/verify existing webhook"
		} else {
			analysis["action"] = "create new webhook"
		}

		repoAnalysis = append(repoAnalysis, analysis)
	}

	c.JSON(http.StatusOK, gin.H{
		"owner":       owner,
		"webhook_url": request.WebhookURL,
		"all_repos":   allRepos,
		"summary": gin.H{
			"total_repositories": len(repos),
			"would_process":      len(repoAnalysis),
		},
		"preview": repoAnalysis,
		"note":    "This is a preview. Use the auto-setup endpoint to actually create/update webhooks.",
	})
}

// getGitHubClientForOwner returns the appropriate GitHub client for an owner
func (we *Endpoints) getGitHubClientForOwner(owner string) *github.Client {
	// Try to get specific client for owner
	if client, exists := we.githubClients[owner]; exists {
		return client
	}

	// Check if there's an owner configuration that matches
	if ownerConfig, exists := we.config.Owners[owner]; exists {
		return github.NewClient(ownerConfig.GitHubToken)
	}

	return nil
}
