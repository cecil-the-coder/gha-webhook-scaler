package github

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestNewTokenValidator(t *testing.T) {
	client := NewClient("test-token")
	validator := NewTokenValidator(client)

	if validator.githubClient != client {
		t.Error("NewTokenValidator() did not set GitHub client correctly")
	}
}

func TestTokenValidator_ValidateToken(t *testing.T) {
	tests := []struct {
		name           string
		rateLimitResp  string
		userResp       string
		statusCode     int
		wantIsValid    bool
		wantTokenType  string
		wantErr        bool
	}{
		{
			name: "valid classic token",
			rateLimitResp: `{
				"resources": {
					"core": {
						"limit": 5000,
						"remaining": 4999,
						"reset": 1640995200,
						"used": 1
					}
				}
			}`,
			userResp: `{
				"login": "testuser",
				"id": 12345,
				"type": "User"
			}`,
			statusCode:    http.StatusOK,
			wantIsValid:   true,
			wantTokenType: "classic",
			wantErr:       false,
		},
		{
			name: "valid fine-grained token",
			rateLimitResp: `{
				"resources": {
					"core": {
						"limit": 5000,
						"remaining": 4999,
						"reset": 1640995200,
						"used": 1
					}
				}
			}`,
			userResp: `{
				"login": "testuser",
				"id": 12345,
				"type": "User"
			}`,
			statusCode:    http.StatusOK,
			wantIsValid:   true,
			wantTokenType: "fine_grained_or_app",
			wantErr:       false,
		},
		{
			name:          "invalid token",
			rateLimitResp: `{"message": "Bad credentials"}`,
			userResp:      `{"message": "Bad credentials"}`,
			statusCode:    http.StatusUnauthorized,
			wantIsValid:   false,
			wantErr:       true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create test server for rate limit endpoint
			rateLimitServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/rate_limit" {
					t.Errorf("Expected /rate_limit path, got %s", r.URL.Path)
				}

				// Set scopes header for classic token test
				if tt.name == "valid classic token" {
					w.Header().Set("X-OAuth-Scopes", "repo,workflow")
				}

				w.WriteHeader(tt.statusCode)
				w.Write([]byte(tt.rateLimitResp))
			}))
			defer rateLimitServer.Close()

			// Create test server for user endpoint
			userServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// Allow /user, /user/repos, and /notifications paths (the latter for fine-grained token validation)
				if r.URL.Path != "/user" && r.URL.Path != "/user/repos" && !strings.HasPrefix(r.URL.Path, "/user/repos?") && r.URL.Path != "/notifications" && !strings.HasPrefix(r.URL.Path, "/notifications?") {
					t.Errorf("Expected /user, /user/repos, or /notifications path, got %s", r.URL.Path)
				}

				w.WriteHeader(tt.statusCode)
				if r.URL.Path == "/user" {
					w.Write([]byte(tt.userResp))
				} else if strings.HasPrefix(r.URL.Path, "/notifications") {
					// For /notifications, return empty array
					w.Write([]byte("[]"))
				} else {
					// For /user/repos, return empty array
					w.Write([]byte("[]"))
				}
			}))
			defer userServer.Close()

			client := NewClient("test-token")
			validator := NewTokenValidator(client)

			// Mock the API URLs by replacing them in the request
			originalURL := "https://api.github.com"
			
			// Override HTTP client to redirect to test servers
			client.httpClient = &http.Client{
				Transport: &testTransport{
					rateLimitURL: rateLimitServer.URL,
					userURL:      userServer.URL,
					originalURL:  originalURL,
				},
			}

			tokenInfo, err := validator.ValidateToken()

			if tt.wantErr {
				if err == nil {
					t.Error("Expected error but got none")
				}
				return
			}

			if err != nil {
				t.Errorf("Unexpected error: %v", err)
				return
			}

			if tokenInfo.IsValid != tt.wantIsValid {
				t.Errorf("IsValid = %v, want %v", tokenInfo.IsValid, tt.wantIsValid)
			}

			if tokenInfo.TokenType != tt.wantTokenType {
				t.Errorf("TokenType = %v, want %v", tokenInfo.TokenType, tt.wantTokenType)
			}
		})
	}
}

func TestTokenValidator_parseScopes(t *testing.T) {
	tests := []struct {
		name       string
		scopesStr  string
		wantScopes []string
	}{
		{
			name:       "multiple scopes",
			scopesStr:  "repo,workflow,write:packages",
			wantScopes: []string{"repo", "workflow", "write:packages"},
		},
		{
			name:       "single scope",
			scopesStr:  "repo",
			wantScopes: []string{"repo"},
		},
		{
			name:       "empty string",
			scopesStr:  "",
			wantScopes: []string{},
		},
		{
			name:       "scopes with spaces",
			scopesStr:  "repo, workflow, write:packages",
			wantScopes: []string{"repo", "workflow", "write:packages"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scopes := parseScopes(tt.scopesStr)

			if len(scopes) != len(tt.wantScopes) {
				t.Errorf("parseScopes() length = %v, want %v", len(scopes), len(tt.wantScopes))
				return
			}

			for i, scope := range scopes {
				if scope != tt.wantScopes[i] {
					t.Errorf("parseScopes()[%d] = %v, want %v", i, scope, tt.wantScopes[i])
				}
			}
		})
	}
}

func TestTokenValidator_hasRequiredScopes(t *testing.T) {
	validator := NewTokenValidator(NewClient("test"))

	tests := []struct {
		name           string
		tokenScopes    []string
		tokenType      string
		requiredScopes []string
		want           bool
	}{
		{
			name:           "has required scope",
			tokenScopes:    []string{"repo", "workflow"},
			tokenType:      "classic",
			requiredScopes: []string{"repo"},
			want:           true,
		},
		{
			name:           "missing required scope",
			tokenScopes:    []string{"workflow"},
			tokenType:      "classic",
			requiredScopes: []string{"repo"},
			want:           false,
		},
		{
			name:           "fine-grained token always returns true",
			tokenScopes:    []string{},
			tokenType:      "fine_grained_or_app",
			requiredScopes: []string{"repo"},
			want:           true,
		},
		{
			name:           "multiple required scopes - has one",
			tokenScopes:    []string{"workflow"},
			tokenType:      "classic",
			requiredScopes: []string{"repo", "workflow"},
			want:           true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := validator.hasRequiredScopes(tt.tokenScopes, tt.tokenType, tt.requiredScopes)
			if result != tt.want {
				t.Errorf("hasRequiredScopes() = %v, want %v", result, tt.want)
			}
		})
	}
}

func TestTokenValidator_ValidateTokenForOperation(t *testing.T) {
	tests := []struct {
		name       string
		operation  string
		tokenType  string
		hasAccess  bool
		rateLimit  int
		wantErr    bool
	}{
		{
			name:      "webhook operation with classic token and access",
			operation: "webhook_management",
			tokenType: "classic",
			hasAccess: true,
			rateLimit: 100,
			wantErr:   false,
		},
		{
			name:      "webhook operation with classic token no access",
			operation: "webhook_management",
			tokenType: "classic",
			hasAccess: false,
			rateLimit: 100,
			wantErr:   true,
		},
		{
			name:      "runner operation with classic token and access",
			operation: "runner_management",
			tokenType: "classic",
			hasAccess: true,
			rateLimit: 100,
			wantErr:   false,
		},
		{
			name:      "rate limit exceeded",
			operation: "webhook_management",
			tokenType: "classic",
			hasAccess: true,
			rateLimit: 5,
			wantErr:   true,
		},
		{
			name:      "unknown operation",
			operation: "unknown_operation",
			tokenType: "classic",
			hasAccess: true,
			rateLimit: 100,
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// Mock rate limit endpoint
				if strings.Contains(r.URL.Path, "rate_limit") {
					if tt.tokenType == "classic" {
						if tt.hasAccess {
							w.Header().Set("X-OAuth-Scopes", "repo,workflow")
						} else {
							// Set scopes that don't include webhook management permissions
							w.Header().Set("X-OAuth-Scopes", "workflow")
						}
					}
					w.WriteHeader(http.StatusOK)
					w.Write([]byte(fmt.Sprintf(`{
						"resources": {
							"core": {
								"limit": 5000,
								"remaining": %d,
								"reset": %d,
								"used": 1
							}
						}
					}`, tt.rateLimit, time.Now().Add(time.Hour).Unix())))
					return
				}

				// Mock user endpoint
				w.WriteHeader(http.StatusOK)
				w.Write([]byte(`{"login": "testuser", "id": 12345, "type": "User"}`))
			}))
			defer server.Close()

			client := NewClient("test-token")
			client.httpClient = &http.Client{
				Transport: &testTransport{
					rateLimitURL: server.URL,
					userURL:      server.URL,
					originalURL:  "https://api.github.com",
				},
			}

			validator := NewTokenValidator(client)
			err := validator.ValidateTokenForOperation(tt.operation)

			if tt.wantErr {
				if err == nil {
					t.Error("Expected error but got none")
				}
			} else {
				if err != nil {
					t.Errorf("Unexpected error: %v", err)
				}
			}
		})
	}
}

func TestTokenValidator_getRateLimitInfo(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rate_limit" {
			t.Errorf("Expected /rate_limit path, got %s", r.URL.Path)
		}

		rateLimitResp := `{
			"resources": {
				"core": {
					"limit": 5000,
					"remaining": 4999,
					"reset": 1640995200,
					"used": 1
				}
			}
		}`

		w.WriteHeader(http.StatusOK)
		w.Write([]byte(rateLimitResp))
	}))
	defer server.Close()

	client := NewClient("test-token")
	client.httpClient = &http.Client{
		Transport: &testTransport{
			rateLimitURL: server.URL,
			userURL:      server.URL,
			originalURL:  "https://api.github.com",
		},
	}

	validator := NewTokenValidator(client)
	rateLimitInfo, err := validator.getRateLimitInfo()

	if err != nil {
		t.Errorf("getRateLimitInfo() error = %v, want no error", err)
		return
	}

	if rateLimitInfo.Limit != 5000 {
		t.Errorf("getRateLimitInfo() limit = %d, want %d", rateLimitInfo.Limit, 5000)
	}

	if rateLimitInfo.Remaining != 4999 {
		t.Errorf("getRateLimitInfo() remaining = %d, want %d", rateLimitInfo.Remaining, 4999)
	}

	expectedReset := time.Unix(1640995200, 0)
	if !rateLimitInfo.Reset.Equal(expectedReset) {
		t.Errorf("getRateLimitInfo() reset = %v, want %v", rateLimitInfo.Reset, expectedReset)
	}

	if rateLimitInfo.Used != 1 {
		t.Errorf("getRateLimitInfo() used = %d, want %d", rateLimitInfo.Used, 1)
	}
}

func TestTokenValidator_getUserInfo(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/user" {
			t.Errorf("Expected /user path, got %s", r.URL.Path)
		}

		userResp := `{
			"login": "testuser",
			"id": 12345,
			"type": "User"
		}`

		w.WriteHeader(http.StatusOK)
		w.Write([]byte(userResp))
	}))
	defer server.Close()

	client := NewClient("test-token")
	client.httpClient = &http.Client{
		Transport: &testTransport{
			rateLimitURL: server.URL,
			userURL:      server.URL,
			originalURL:  "https://api.github.com",
		},
	}

	validator := NewTokenValidator(client)
	userInfo, err := validator.getUserInfo()

	if err != nil {
		t.Errorf("getUserInfo() error = %v, want no error", err)
		return
	}

	if userInfo.Login != "testuser" {
		t.Errorf("getUserInfo() login = %s, want %s", userInfo.Login, "testuser")
	}

	if userInfo.ID != 12345 {
		t.Errorf("getUserInfo() id = %d, want %d", userInfo.ID, 12345)
	}

	if userInfo.Type != "User" {
		t.Errorf("getUserInfo() type = %s, want %s", userInfo.Type, "User")
	}
}

func TestTokenValidator_analyzePermissions(t *testing.T) {
	validator := NewTokenValidator(NewClient("test"))
	
	// Test with webhook management scopes
	tokenInfo := &TokenInfo{
		Scopes: []string{"repo", "write:repo_hook"},
		TokenType: "classic",
	}
	
	validator.analyzePermissions(tokenInfo)
	
	if !tokenInfo.HasWebhookAccess {
		t.Error("analyzePermissions() should set HasWebhookAccess to true with repo scope")
	}
	
	if !tokenInfo.CanManageRunners {
		t.Error("analyzePermissions() should set CanManageRunners to true with repo scope")
	}
	
	// Test with missing scopes
	tokenInfo2 := &TokenInfo{
		Scopes: []string{"read:user"},
		TokenType: "classic",
	}
	
	validator.analyzePermissions(tokenInfo2)
	
	if tokenInfo2.HasWebhookAccess {
		t.Error("analyzePermissions() should set HasWebhookAccess to false without webhook scopes")
	}
	
	if tokenInfo2.CanManageRunners {
		t.Error("analyzePermissions() should set CanManageRunners to false without runner scopes")
	}
	
	// Test with fine-grained token type (should always return true)
	tokenInfo3 := &TokenInfo{
		Scopes: []string{},
		TokenType: "fine_grained_or_app",
	}
	
	validator.analyzePermissions(tokenInfo3)
	
	if !tokenInfo3.HasWebhookAccess {
		t.Error("analyzePermissions() should set HasWebhookAccess to true for fine-grained tokens")
	}
	
	if !tokenInfo3.CanManageRunners {
		t.Error("analyzePermissions() should set CanManageRunners to true for fine-grained tokens")
	}
}

func TestTokenValidator_getRequiredScopes(t *testing.T) {
	validator := NewTokenValidator(NewClient("test"))
	requiredScopes := validator.getRequiredScopes()
	
	if len(requiredScopes.WebhookManagement) == 0 {
		t.Error("getRequiredScopes() should return WebhookManagement scopes")
	}
	
	if len(requiredScopes.RunnerManagement) == 0 {
		t.Error("getRequiredScopes() should return RunnerManagement scopes")
	}
	
	if len(requiredScopes.RepoAccess) == 0 {
		t.Error("getRequiredScopes() should return RepoAccess scopes")
	}
	
	if len(requiredScopes.OrgAccess) == 0 {
		t.Error("getRequiredScopes() should return OrgAccess scopes")
	}
}

func TestTokenValidator_inferFineGrainedPermissions(t *testing.T) {
	tests := []struct {
		name           string
		endpointStatus map[string]int
		wantLength     int
	}{
		{
			name: "all endpoints accessible",
			endpointStatus: map[string]int{
				"user":          http.StatusOK,
				"user_repos":    http.StatusOK,
				"notifications": http.StatusOK,
			},
			wantLength: 3,
		},
		{
			name: "some endpoints accessible",
			endpointStatus: map[string]int{
				"user":          http.StatusOK,
				"user_repos":    http.StatusForbidden,
				"notifications": http.StatusOK,
			},
			wantLength: 2,
		},
		{
			name: "no endpoints accessible",
			endpointStatus: map[string]int{
				"user":          http.StatusForbidden,
				"user_repos":    http.StatusForbidden,
				"notifications": http.StatusForbidden,
			},
			wantLength: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// Determine which endpoint is being called
				var status int
				switch {
				case strings.Contains(r.URL.Path, "/user/repos"):
					status = tt.endpointStatus["user_repos"]
				case strings.Contains(r.URL.Path, "/notifications"):
					status = tt.endpointStatus["notifications"]
					// Special handling for notifications endpoint - it returns 404 for invalid token
					if status == http.StatusForbidden {
						status = http.StatusNotFound
					}
				case strings.Contains(r.URL.Path, "/user"):
					status = tt.endpointStatus["user"]
				default:
					status = http.StatusNotFound
				}
				
				w.WriteHeader(status)
				if status == http.StatusOK {
					w.Write([]byte(`{"login": "testuser"}`))
				}
			}))
			defer server.Close()

			client := NewClient("test-token")
			client.httpClient = &http.Client{
				Transport: &testTransport{
					rateLimitURL: server.URL,
					userURL:      server.URL,
					originalURL:  "https://api.github.com",
				},
			}

			validator := NewTokenValidator(client)
			permissions, err := validator.inferFineGrainedPermissions()

			if err != nil {
				t.Errorf("inferFineGrainedPermissions() error = %v, want no error", err)
				return
			}

			if len(permissions) != tt.wantLength {
				t.Errorf("inferFineGrainedPermissions() length = %d, want %d", len(permissions), tt.wantLength)
			}
		})
	}
}

func TestRepositoryTokenPermissions(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/registration-token"):
			// Runner token test - check this first since it's more specific
			w.WriteHeader(http.StatusCreated)
			w.Write([]byte(`{"token": "test-token", "expires_at": "2023-01-01T00:00:00Z"}`))
		case strings.Contains(r.URL.Path, "/hooks"):
			// Webhook access test
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`[]`))
		case strings.Contains(r.URL.Path, "/repos/"):
			// Repository access test
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"id": 12345, "name": "test-repo", "full_name": "owner/test-repo"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	client := NewClient("test-token")
	client.httpClient = &http.Client{
		Transport: &testTransport{
			rateLimitURL: server.URL,
			userURL:      server.URL,
			originalURL:  "https://api.github.com",
		},
	}

	validator := NewTokenValidator(client)
	permissions, err := validator.TestTokenPermissions("owner", "repo")

	if err != nil {
		t.Errorf("Unexpected error: %v", err)
		return
	}

	if !permissions.CanAccessRepo {
		t.Error("Expected CanAccessRepo to be true")
	}

	if !permissions.CanReadWebhooks {
		t.Error("Expected CanReadWebhooks to be true")
	}

	if !permissions.CanManageRunners {
		t.Error("Expected CanManageRunners to be true")
	}
}

// testTransport is a custom transport for testing that redirects API calls to test servers
type testTransport struct {
	rateLimitURL string
	userURL      string
	originalURL  string
}

func (t *testTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Replace the API URL with test server URLs based on the path
	switch {
	case strings.Contains(req.URL.Path, "rate_limit"):
		req.URL.Scheme = "http"
		req.URL.Host = strings.TrimPrefix(t.rateLimitURL, "http://")
	case strings.Contains(req.URL.Path, "user"):
		req.URL.Scheme = "http"
		req.URL.Host = strings.TrimPrefix(t.userURL, "http://")
	case strings.Contains(req.URL.Path, "repos"):
		req.URL.Scheme = "http"
		req.URL.Host = strings.TrimPrefix(t.userURL, "http://")
	case strings.Contains(req.URL.Path, "notifications"):
		req.URL.Scheme = "http"
		req.URL.Host = strings.TrimPrefix(t.userURL, "http://")
	}

	return http.DefaultTransport.RoundTrip(req)
}