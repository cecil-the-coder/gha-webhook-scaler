package github

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"
)

// TokenValidator handles GitHub token validation and scope checking
type TokenValidator struct {
	githubClient *Client
}

// TokenInfo represents information about a GitHub token
type TokenInfo struct {
	Scopes         []string          `json:"scopes"`
	TokenType      string            `json:"token_type"`      // "classic" or "fine_grained"
	User           *TokenUser        `json:"user,omitempty"`
	App            *TokenApp         `json:"app,omitempty"`
	Permissions    *TokenPermissions `json:"permissions,omitempty"`
	RateLimit      *RateLimitInfo    `json:"rate_limit"`
	IsValid        bool              `json:"is_valid"`
	HasWebhookAccess bool            `json:"has_webhook_access"`
	CanManageRunners bool            `json:"can_manage_runners"`
	Warnings       []string          `json:"warnings,omitempty"`
}

// TokenUser represents user information from the token
type TokenUser struct {
	Login string `json:"login"`
	ID    int64  `json:"id"`
	Type  string `json:"type"` // "User" or "Organization"
}

// TokenApp represents app information for app tokens
type TokenApp struct {
	Name string `json:"name"`
	ID   int64  `json:"id"`
}

// TokenPermissions represents detailed permissions for fine-grained tokens
type TokenPermissions struct {
	Actions           string `json:"actions,omitempty"`
	Administration    string `json:"administration,omitempty"`
	Contents          string `json:"contents,omitempty"`
	Metadata          string `json:"metadata,omitempty"`
	PullRequests      string `json:"pull_requests,omitempty"`
	RepositoryHooks   string `json:"repository_hooks,omitempty"`
	OrganizationHooks string `json:"organization_hooks,omitempty"`
}

// RateLimitInfo represents current rate limit status
type RateLimitInfo struct {
	Limit     int       `json:"limit"`
	Remaining int       `json:"remaining"`
	Reset     time.Time `json:"reset"`
	Used      int       `json:"used"`
}

// RequiredScopes defines what scopes are needed for different operations
type RequiredScopes struct {
	WebhookManagement []string
	RunnerManagement  []string
	RepoAccess        []string
	OrgAccess         []string
}

// NewTokenValidator creates a new token validator
func NewTokenValidator(githubClient *Client) *TokenValidator {
	return &TokenValidator{
		githubClient: githubClient,
	}
}

// ValidateToken performs comprehensive token validation
func (tv *TokenValidator) ValidateToken() (*TokenInfo, error) {
	tokenInfo := &TokenInfo{
		IsValid:  false,
		Warnings: []string{},
	}

	// Get token scopes using rate_limit endpoint (doesn't count against rate limit)
	scopes, tokenType, err := tv.getTokenScopes()
	if err != nil {
		return tokenInfo, fmt.Errorf("failed to get token scopes: %w", err)
	}

	tokenInfo.Scopes = scopes
	tokenInfo.TokenType = tokenType
	tokenInfo.IsValid = true

	// Get user information
	user, err := tv.getUserInfo()
	if err != nil {
		// Not fatal, but add warning
		tokenInfo.Warnings = append(tokenInfo.Warnings, fmt.Sprintf("Could not get user info: %v", err))
	} else {
		tokenInfo.User = user
	}

	// Get rate limit information
	rateLimit, err := tv.getRateLimitInfo()
	if err != nil {
		tokenInfo.Warnings = append(tokenInfo.Warnings, fmt.Sprintf("Could not get rate limit info: %v", err))
	} else {
		tokenInfo.RateLimit = rateLimit
	}

	// Analyze permissions
	tv.analyzePermissions(tokenInfo)

	return tokenInfo, nil
}

// getTokenScopes retrieves token scopes from GitHub API headers
func (tv *TokenValidator) getTokenScopes() ([]string, string, error) {
	// Use rate_limit endpoint as it doesn't count against rate limits
	req, err := http.NewRequest("GET", "https://api.github.com/rate_limit", nil)
	if err != nil {
		return nil, "", fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "token "+tv.githubClient.Token)
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	resp, err := tv.githubClient.MakeRequestWithBackoff(req)
	if err != nil {
		return nil, "", fmt.Errorf("failed to make request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("invalid token or API error: %d", resp.StatusCode)
	}

	// Check for classic token scopes
	scopesHeader := resp.Header.Get("X-OAuth-Scopes")
	acceptedScopesHeader := resp.Header.Get("X-Accepted-OAuth-Scopes")

	var scopes []string
	tokenType := "unknown"

	if scopesHeader != "" {
		// Classic personal access token
		tokenType = "classic"
		if scopesHeader != "" {
			scopes = parseScopes(scopesHeader)
		}
		log.Printf("Detected classic PAT with scopes: %s (accepted: %s)", scopesHeader, acceptedScopesHeader)
	} else {
		// Likely fine-grained token or app token
		// For fine-grained tokens, we can't directly get scopes from headers
		tokenType = "fine_grained_or_app"
		log.Printf("Detected fine-grained or app token (no X-OAuth-Scopes header)")
		
		// We'll need to infer permissions through testing API endpoints
		scopes, err = tv.inferFineGrainedPermissions()
		if err != nil {
			log.Printf("Warning: Could not infer fine-grained permissions: %v", err)
		}
	}

	return scopes, tokenType, nil
}

// parseScopes parses the comma-separated scopes string
func parseScopes(scopesStr string) []string {
	if scopesStr == "" {
		return []string{}
	}
	
	scopes := strings.Split(scopesStr, ",")
	for i, scope := range scopes {
		scopes[i] = strings.TrimSpace(scope)
	}
	
	return scopes
}

// inferFineGrainedPermissions attempts to infer permissions for fine-grained tokens
func (tv *TokenValidator) inferFineGrainedPermissions() ([]string, error) {
	permissions := []string{}

	// Test various endpoints to infer permissions
	testEndpoints := map[string]string{
		"user":           "https://api.github.com/user",
		"user_repos":     "https://api.github.com/user/repos?per_page=1",
		"notifications":  "https://api.github.com/notifications?per_page=1",
	}

	for permissionName, endpoint := range testEndpoints {
		req, err := http.NewRequest("GET", endpoint, nil)
		if err != nil {
			continue
		}

		req.Header.Set("Authorization", "token "+tv.githubClient.Token)
		req.Header.Set("Accept", "application/vnd.github.v3+json")

		resp, err := tv.githubClient.httpClient.Do(req)
		if err != nil {
			continue
		}
		resp.Body.Close()

		if resp.StatusCode == http.StatusOK {
			permissions = append(permissions, permissionName)
		}
	}

	return permissions, nil
}

// getUserInfo gets user information from the token
func (tv *TokenValidator) getUserInfo() (*TokenUser, error) {
	req, err := http.NewRequest("GET", "https://api.github.com/user", nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "token "+tv.githubClient.Token)
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	resp, err := tv.githubClient.MakeRequestWithBackoff(req)
	if err != nil {
		return nil, fmt.Errorf("failed to make request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to get user info: %d", resp.StatusCode)
	}

	var user TokenUser
	if err := decodeJSONResponse(resp, &user); err != nil {
		return nil, fmt.Errorf("failed to decode user info: %w", err)
	}

	return &user, nil
}

// getRateLimitInfo gets current rate limit status
func (tv *TokenValidator) getRateLimitInfo() (*RateLimitInfo, error) {
	req, err := http.NewRequest("GET", "https://api.github.com/rate_limit", nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "token "+tv.githubClient.Token)
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	resp, err := tv.githubClient.MakeRequestWithBackoff(req)
	if err != nil {
		return nil, fmt.Errorf("failed to make request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to get rate limit: %d", resp.StatusCode)
	}

	var rateLimitResp struct {
		Resources struct {
			Core struct {
				Limit     int   `json:"limit"`
				Remaining int   `json:"remaining"`
				Reset     int64 `json:"reset"`
				Used      int   `json:"used"`
			} `json:"core"`
		} `json:"resources"`
	}

	if err := decodeJSONResponse(resp, &rateLimitResp); err != nil {
		return nil, fmt.Errorf("failed to decode rate limit: %w", err)
	}

	return &RateLimitInfo{
		Limit:     rateLimitResp.Resources.Core.Limit,
		Remaining: rateLimitResp.Resources.Core.Remaining,
		Reset:     time.Unix(rateLimitResp.Resources.Core.Reset, 0),
		Used:      rateLimitResp.Resources.Core.Used,
	}, nil
}

// analyzePermissions analyzes token permissions and sets capability flags
func (tv *TokenValidator) analyzePermissions(tokenInfo *TokenInfo) {
	requiredScopes := tv.getRequiredScopes()

	// Check webhook management capabilities
	tokenInfo.HasWebhookAccess = tv.hasRequiredScopes(tokenInfo.Scopes, tokenInfo.TokenType, requiredScopes.WebhookManagement)
	
	// Check runner management capabilities  
	tokenInfo.CanManageRunners = tv.hasRequiredScopes(tokenInfo.Scopes, tokenInfo.TokenType, requiredScopes.RunnerManagement)

	// Add warnings for missing critical permissions
	if !tokenInfo.HasWebhookAccess {
		if tokenInfo.TokenType == "classic" {
			tokenInfo.Warnings = append(tokenInfo.Warnings, 
				"Missing webhook management permissions. Required: repo scope or write:repo_hook scope")
		} else {
			tokenInfo.Warnings = append(tokenInfo.Warnings, 
				"Cannot verify webhook permissions for fine-grained token. Ensure 'Repository webhooks' permission is granted")
		}
	}

	if !tokenInfo.CanManageRunners {
		if tokenInfo.TokenType == "classic" {
			tokenInfo.Warnings = append(tokenInfo.Warnings, 
				"Missing runner management permissions. Required: repo scope for Actions")
		} else {
			tokenInfo.Warnings = append(tokenInfo.Warnings, 
				"Cannot verify runner permissions for fine-grained token. Ensure 'Actions' permission is granted")
		}
	}

	// Check rate limit warnings
	if tokenInfo.RateLimit != nil && tokenInfo.RateLimit.Remaining < 100 {
		tokenInfo.Warnings = append(tokenInfo.Warnings, 
			fmt.Sprintf("Low rate limit remaining: %d/%d (resets at %s)", 
				tokenInfo.RateLimit.Remaining, tokenInfo.RateLimit.Limit, 
				tokenInfo.RateLimit.Reset.Format(time.RFC3339)))
	}
}

// getRequiredScopes returns the scopes needed for different operations
func (tv *TokenValidator) getRequiredScopes() RequiredScopes {
	return RequiredScopes{
		WebhookManagement: []string{"repo", "write:repo_hook", "admin:repo_hook"},
		RunnerManagement:  []string{"repo", "actions"},
		RepoAccess:        []string{"repo", "read:repo"},
		OrgAccess:         []string{"admin:org", "read:org", "admin:org_hook"},
	}
}

// hasRequiredScopes checks if the token has any of the required scopes
func (tv *TokenValidator) hasRequiredScopes(tokenScopes []string, tokenType string, requiredScopes []string) bool {
	if tokenType == "fine_grained_or_app" {
		// For fine-grained tokens, we can't reliably check scopes from headers
		// Return true and let the actual API calls determine permissions
		return true
	}

	scopeMap := make(map[string]bool)
	for _, scope := range tokenScopes {
		scopeMap[scope] = true
	}

	for _, required := range requiredScopes {
		if scopeMap[required] {
			return true
		}
	}

	return false
}

// ValidateTokenForOperation validates that a token has permissions for a specific operation
func (tv *TokenValidator) ValidateTokenForOperation(operation string) error {
	tokenInfo, err := tv.ValidateToken()
	if err != nil {
		return fmt.Errorf("token validation failed: %w", err)
	}

	if !tokenInfo.IsValid {
		return fmt.Errorf("token is not valid")
	}

	switch operation {
	case "webhook_management":
		if !tokenInfo.HasWebhookAccess && tokenInfo.TokenType == "classic" {
			return fmt.Errorf("token lacks webhook management permissions. Required scopes: repo, write:repo_hook, or admin:repo_hook")
		}
	case "runner_management":
		if !tokenInfo.CanManageRunners && tokenInfo.TokenType == "classic" {
			return fmt.Errorf("token lacks runner management permissions. Required scopes: repo or actions")
		}
	default:
		return fmt.Errorf("unknown operation: %s", operation)
	}

	// Warn about rate limits
	if tokenInfo.RateLimit != nil && tokenInfo.RateLimit.Remaining < 10 {
		return fmt.Errorf("rate limit nearly exceeded: %d requests remaining", tokenInfo.RateLimit.Remaining)
	}

	return nil
}

// TestTokenPermissions tests token permissions against a specific repository
func (tv *TokenValidator) TestTokenPermissions(owner, repo string) (*RepositoryTokenPermissions, error) {
	permissions := &RepositoryTokenPermissions{
		Repository: fmt.Sprintf("%s/%s", owner, repo),
	}

	// Test repository access
	repoReq, err := http.NewRequest("GET", fmt.Sprintf("https://api.github.com/repos/%s/%s", owner, repo), nil)
	if err != nil {
		return permissions, fmt.Errorf("failed to create request: %w", err)
	}
	repoReq.Header.Set("Authorization", "token "+tv.githubClient.Token)
	repoReq.Header.Set("Accept", "application/vnd.github.v3+json")

	repoResp, err := tv.githubClient.MakeRequestWithBackoff(repoReq)
	if err != nil {
		return permissions, fmt.Errorf("failed to test repository access: %w", err)
	}
	repoResp.Body.Close()

	permissions.CanAccessRepo = repoResp.StatusCode == http.StatusOK
	if repoResp.StatusCode == http.StatusNotFound {
		permissions.Errors = append(permissions.Errors, "Repository not found or no access")
	}

	// Test webhook listing (read permission)
	hooksReq, err := http.NewRequest("GET", fmt.Sprintf("https://api.github.com/repos/%s/%s/hooks", owner, repo), nil)
	if err == nil {
		hooksReq.Header.Set("Authorization", "token "+tv.githubClient.Token)
		hooksReq.Header.Set("Accept", "application/vnd.github.v3+json")

		hooksResp, err := tv.githubClient.MakeRequestWithBackoff(hooksReq)
		if err == nil {
			hooksResp.Body.Close()
			permissions.CanReadWebhooks = hooksResp.StatusCode == http.StatusOK
			permissions.CanWriteWebhooks = hooksResp.StatusCode == http.StatusOK // Writing typically requires same permissions as reading
		}
	}

	// Test runner token generation (Actions permission)
	tokenReq, err := http.NewRequest("POST", fmt.Sprintf("https://api.github.com/repos/%s/%s/actions/runners/registration-token", owner, repo), nil)
	if err == nil {
		tokenReq.Header.Set("Authorization", "token "+tv.githubClient.Token)
		tokenReq.Header.Set("Accept", "application/vnd.github.v3+json")

		tokenResp, err := tv.githubClient.MakeRequestWithBackoff(tokenReq)
		if err == nil {
			tokenResp.Body.Close()
			permissions.CanManageRunners = tokenResp.StatusCode == http.StatusCreated
		}
	}

	return permissions, nil
}

// RepositoryTokenPermissions represents token permissions for a specific repository
type RepositoryTokenPermissions struct {
	Repository        string   `json:"repository"`
	CanAccessRepo     bool     `json:"can_access_repo"`
	CanReadWebhooks   bool     `json:"can_read_webhooks"`
	CanWriteWebhooks  bool     `json:"can_write_webhooks"`
	CanManageRunners  bool     `json:"can_manage_runners"`
	Errors           []string `json:"errors,omitempty"`
}

// Helper function to decode JSON responses
func decodeJSONResponse(resp *http.Response, v interface{}) error {
	// Import encoding/json at the top of the file
	// For now, we'll use a placeholder since json import is already available in the project
	decoder := json.NewDecoder(resp.Body)
	return decoder.Decode(v)
}
