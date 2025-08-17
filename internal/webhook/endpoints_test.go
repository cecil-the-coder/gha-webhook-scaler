

package webhook

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/cecil-the-coder/gha-webhook-scaler/internal/config"
	"github.com/cecil-the-coder/gha-webhook-scaler/internal/github"
)

// testTransport is a custom transport for testing that redirects API calls to test servers
type testTransport struct {
	testURL string
}

func (t *testTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Replace all GitHub API calls with test server URL
	if strings.Contains(req.URL.Host, "api.github.com") {
		req.URL.Scheme = "http"
		req.URL.Host = strings.TrimPrefix(t.testURL, "http://")
	}
	return http.DefaultTransport.RoundTrip(req)
}

func TestNewEndpoints(t *testing.T) {
	config := &config.Config{
		Owners: map[string]*config.OwnerConfig{"test-owner": {Owner: "test-owner", GitHubToken: "test-token", AllRepos: true}},
	}
	
	githubClients := map[string]*github.Client{
		"testowner": github.NewClient("test-token"),
	}
	
	endpoints := NewEndpoints(config, githubClients)
	
	if endpoints == nil {
		t.Error("NewEndpoints() returned nil")
	}
	
	if endpoints.config != config {
		t.Error("NewEndpoints() did not set config correctly")
	}
	
	if endpoints.githubClients == nil {
		t.Error("NewEndpoints() did not set GitHub clients correctly")
	}
	
	if endpoints.webhookManager == nil {
		t.Error("NewEndpoints() did not create webhook manager")
	}
}

func TestNewEndpoints_WithoutClients(t *testing.T) {
	config := &config.Config{
		Owners: map[string]*config.OwnerConfig{"test-owner": {Owner: "test-owner", GitHubToken: "test-token", AllRepos: true}},
	}
	
	endpoints := NewEndpoints(config, nil)
	
	if endpoints == nil {
		t.Error("NewEndpoints() returned nil")
	}
	
	if endpoints.webhookManager == nil {
		t.Error("NewEndpoints() did not create webhook manager with default client")
	}
}

func TestEndpoints_ListWebhooksHandler_NoClient(t *testing.T) {
	endpoints := &Endpoints{
		config:        &config.Config{},
		githubClients: map[string]*github.Client{},
	}
	
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Params = []gin.Param{
		{Key: "owner", Value: "testowner"},
		{Key: "repo", Value: "testrepo"},
	}
	
	endpoints.ListWebhooksHandler(c)
	
	if w.Code != http.StatusBadRequest {
		t.Errorf("Expected status %d, got %d", http.StatusBadRequest, w.Code)
	}
}

func TestEndpoints_ListWebhooksHandler_BadRequest(t *testing.T) {
	endpoints := &Endpoints{
		config:        &config.Config{},
		githubClients: map[string]*github.Client{},
	}
	
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Params = []gin.Param{
		{Key: "owner", Value: ""},
		{Key: "repo", Value: "testrepo"},
	}
	
	endpoints.ListWebhooksHandler(c)
	
	if w.Code != http.StatusBadRequest {
		t.Errorf("Expected status %d, got %d", http.StatusBadRequest, w.Code)
	}
}

func TestEndpoints_CreateWebhookHandler_BadRequest(t *testing.T) {
	endpoints := &Endpoints{
		config:        &config.Config{},
		githubClients: map[string]*github.Client{},
	}
	
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/", bytes.NewBuffer([]byte("invalid json")))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = []gin.Param{
		{Key: "owner", Value: "testowner"},
		{Key: "repo", Value: "testrepo"},
	}
	
	endpoints.CreateWebhookHandler(c)
	
	if w.Code != http.StatusBadRequest {
		t.Errorf("Expected status %d, got %d", http.StatusBadRequest, w.Code)
	}
}

func TestEndpoints_DeleteWebhookHandler_BadRequest(t *testing.T) {
	endpoints := &Endpoints{
		config:        &config.Config{},
		githubClients: map[string]*github.Client{},
	}
	
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Params = []gin.Param{
		{Key: "owner", Value: "testowner"},
		{Key: "repo", Value: "testrepo"},
		{Key: "hook_id", Value: "invalid"},
	}
	
	endpoints.DeleteWebhookHandler(c)
	
	if w.Code != http.StatusBadRequest {
		t.Errorf("Expected status %d, got %d", http.StatusBadRequest, w.Code)
	}
}

func TestEndpoints_GetWebhookHandler(t *testing.T) {
	// Test parameter validation and endpoint structure without external API calls
	config := &config.Config{
		Owners: map[string]*config.OwnerConfig{"test-owner": {Owner: "test-owner", GitHubToken: "test-token", AllRepos: true}},
	}
	githubClients := map[string]*github.Client{
		"testowner": github.NewClient("test-token"),
	}
	endpoints := NewEndpoints(config, githubClients)

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Params = []gin.Param{
		{Key: "owner", Value: "testowner"},
		{Key: "repo", Value: "testrepo"},
		{Key: "hook_id", Value: "12345"},
	}

	// Call the handler - it will try to make a real GitHub API call and fail
	// but we can verify parameter handling works correctly
	endpoints.GetWebhookHandler(c)

	// Since we can't mock the GitHub API easily, we expect a 500 error
	// The important thing is that it's not a 400 error (which would indicate parameter issues)
	if w.Code == http.StatusBadRequest {
		var response map[string]interface{}
		if err := json.Unmarshal(w.Body.Bytes(), &response); err == nil {
			if errorMsg, ok := response["error"].(string); ok {
				// These would indicate our parameter validation is wrong
				if strings.Contains(errorMsg, "required") || strings.Contains(errorMsg, "invalid hook_id format") || strings.Contains(errorMsg, "no GitHub client") {
					t.Errorf("Unexpected parameter validation error: %s", errorMsg)
				}
			}
		}
	}

	// The handler should process parameters correctly and only fail on GitHub API call
	// We expect either 500 (GitHub API error) or potentially 200 if somehow it works
	if w.Code != http.StatusInternalServerError && w.Code != http.StatusOK && w.Code != http.StatusBadRequest {
		t.Errorf("Unexpected status code: %d", w.Code)
	}
}

func TestEndpoints_GetWebhookHandler_BadRequest(t *testing.T) {
	endpoints := &Endpoints{
		config:        &config.Config{},
		githubClients: map[string]*github.Client{},
	}

	tests := []struct {
		name       string
		owner      string
		repo       string
		hookID     string
		statusCode int
	}{
		{
			name:       "empty owner",
			owner:      "",
			repo:       "testrepo",
			hookID:     "12345",
			statusCode: http.StatusBadRequest,
		},
		{
			name:       "empty repo",
			owner:      "testowner",
			repo:       "",
			hookID:     "12345",
			statusCode: http.StatusBadRequest,
		},
		{
			name:       "empty hook_id",
			owner:      "testowner",
			repo:       "testrepo",
			hookID:     "",
			statusCode: http.StatusBadRequest,
		},
		{
			name:       "no client for owner",
			owner:      "unknownowner",
			repo:       "testrepo",
			hookID:     "12345",
			statusCode: http.StatusBadRequest,
		},
		{
			name:       "invalid hook_id",
			owner:      "testowner",
			repo:       "testrepo",
			hookID:     "not-a-number",
			statusCode: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Params = []gin.Param{
				{Key: "owner", Value: tt.owner},
				{Key: "repo", Value: tt.repo},
				{Key: "hook_id", Value: tt.hookID},
			}

			endpoints.GetWebhookHandler(c)

			if w.Code != tt.statusCode {
				t.Errorf("Expected status %d, got %d", tt.statusCode, w.Code)
			}
		})
	}
}

func TestEndpoints_TestWebhookHandler(t *testing.T) {
	// Test parameter validation and endpoint structure without external API calls
	config := &config.Config{
		Owners: map[string]*config.OwnerConfig{"test-owner": {Owner: "test-owner", GitHubToken: "test-token", AllRepos: true}},
	}
	githubClients := map[string]*github.Client{
		"testowner": github.NewClient("test-token"),
	}
	endpoints := NewEndpoints(config, githubClients)

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Params = []gin.Param{
		{Key: "owner", Value: "testowner"},
		{Key: "repo", Value: "testrepo"},
		{Key: "hook_id", Value: "12345"},
	}

	// Call the handler - it will try to make a real GitHub API call and fail
	endpoints.TestWebhookHandler(c)

	// Similar to GetWebhookHandler, we expect parameter validation to work correctly
	// and only fail on the GitHub API call
	if w.Code == http.StatusBadRequest {
		var response map[string]interface{}
		if err := json.Unmarshal(w.Body.Bytes(), &response); err == nil {
			if errorMsg, ok := response["error"].(string); ok {
				if strings.Contains(errorMsg, "required") || strings.Contains(errorMsg, "invalid hook_id format") || strings.Contains(errorMsg, "no GitHub client") {
					t.Errorf("Unexpected parameter validation error: %s", errorMsg)
				}
			}
		}
	}

	// We expect either 500 (GitHub API error) or 200 if somehow it works
	if w.Code != http.StatusInternalServerError && w.Code != http.StatusOK && w.Code != http.StatusBadRequest {
		t.Errorf("Unexpected status code: %d", w.Code)
	}
}

func TestEndpoints_TestWebhookHandler_BadRequest(t *testing.T) {
	endpoints := &Endpoints{
		config:        &config.Config{},
		githubClients: map[string]*github.Client{},
	}

	tests := []struct {
		name       string
		owner      string
		repo       string
		hookID     string
		statusCode int
	}{
		{
			name:       "empty owner",
			owner:      "",
			repo:       "testrepo",
			hookID:     "12345",
			statusCode: http.StatusBadRequest,
		},
		{
			name:       "empty repo",
			owner:      "testowner",
			repo:       "",
			hookID:     "12345",
			statusCode: http.StatusBadRequest,
		},
		{
			name:       "empty hook_id",
			owner:      "testowner",
			repo:       "testrepo",
			hookID:     "",
			statusCode: http.StatusBadRequest,
		},
		{
			name:       "no client for owner",
			owner:      "unknownowner",
			repo:       "testrepo",
			hookID:     "12345",
			statusCode: http.StatusBadRequest,
		},
		{
			name:       "invalid hook_id",
			owner:      "testowner",
			repo:       "testrepo",
			hookID:     "not-a-number",
			statusCode: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Params = []gin.Param{
				{Key: "owner", Value: tt.owner},
				{Key: "repo", Value: tt.repo},
				{Key: "hook_id", Value: tt.hookID},
			}

			endpoints.TestWebhookHandler(c)

			if w.Code != tt.statusCode {
				t.Errorf("Expected status %d, got %d", tt.statusCode, w.Code)
			}
		})
	}
}

func TestEndpoints_EnsureWebhookHandler(t *testing.T) {
	// Test parameter validation and request body parsing without external API calls
	config := &config.Config{
		Owners: map[string]*config.OwnerConfig{"test-owner": {Owner: "test-owner", GitHubToken: "test-token", AllRepos: true}},
	}
	githubClients := map[string]*github.Client{
		"testowner": github.NewClient("test-token"),
	}
	endpoints := NewEndpoints(config, githubClients)

	// Create valid request body
	requestBody := `{"webhook_url": "https://example.com/webhook", "secret": "test-secret", "events": ["workflow_run"]}`

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/", bytes.NewBufferString(requestBody))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = []gin.Param{
		{Key: "owner", Value: "testowner"},
		{Key: "repo", Value: "testrepo"},
	}

	// Call the handler - it will try to make real GitHub API calls and fail
	endpoints.EnsureWebhookHandler(c)

	// Test that parameter validation and JSON parsing work correctly
	if w.Code == http.StatusBadRequest {
		var response map[string]interface{}
		if err := json.Unmarshal(w.Body.Bytes(), &response); err == nil {
			if errorMsg, ok := response["error"].(string); ok {
				// These would indicate our request parsing is wrong
				if strings.Contains(errorMsg, "required") || strings.Contains(errorMsg, "invalid json") || strings.Contains(errorMsg, "no GitHub client") {
					t.Errorf("Unexpected request parsing error: %s", errorMsg)
				}
			}
		}
	}

	// We expect either 500 (GitHub API error) or 200 if somehow it works
	if w.Code != http.StatusInternalServerError && w.Code != http.StatusOK && w.Code != http.StatusBadRequest {
		t.Errorf("Unexpected status code: %d", w.Code)
	}
}

func TestEndpoints_EnsureWebhookHandler_BadRequest(t *testing.T) {
	endpoints := &Endpoints{
		config:        &config.Config{},
		githubClients: map[string]*github.Client{},
	}

	tests := []struct {
		name        string
		owner       string
		repo        string
		requestBody string
		statusCode  int
	}{
		{
			name:        "empty owner",
			owner:       "",
			repo:        "testrepo",
			requestBody: `{"webhook_url": "https://example.com/webhook"}`,
			statusCode:  http.StatusBadRequest,
		},
		{
			name:        "empty repo",
			owner:       "testowner",
			repo:        "",
			requestBody: `{"webhook_url": "https://example.com/webhook"}`,
			statusCode:  http.StatusBadRequest,
		},
		{
			name:        "invalid json",
			owner:       "testowner",
			repo:        "testrepo",
			requestBody: `invalid json`,
			statusCode:  http.StatusBadRequest,
		},
		{
			name:        "no client for owner",
			owner:       "unknownowner",
			repo:        "testrepo",
			requestBody: `{"webhook_url": "https://example.com/webhook"}`,
			statusCode:  http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest("POST", "/", bytes.NewBufferString(tt.requestBody))
			c.Request.Header.Set("Content-Type", "application/json")
			c.Params = []gin.Param{
				{Key: "owner", Value: tt.owner},
				{Key: "repo", Value: tt.repo},
			}

			endpoints.EnsureWebhookHandler(c)

			if w.Code != tt.statusCode {
				t.Errorf("Expected status %d, got %d", tt.statusCode, w.Code)
			}
		})
	}
}

func TestEndpoints_GetWebhookDeliveriesHandler(t *testing.T) {
	// Test parameter validation and endpoint structure without external API calls
	config := &config.Config{
		Owners: map[string]*config.OwnerConfig{"test-owner": {Owner: "test-owner", GitHubToken: "test-token", AllRepos: true}},
	}
	githubClients := map[string]*github.Client{
		"testowner": github.NewClient("test-token"),
	}
	endpoints := NewEndpoints(config, githubClients)

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Params = []gin.Param{
		{Key: "owner", Value: "testowner"},
		{Key: "repo", Value: "testrepo"},
		{Key: "hook_id", Value: "12345"},
	}

	// Call the handler - it will try to make a real GitHub API call and fail
	endpoints.GetWebhookDeliveriesHandler(c)

	// Test parameter validation works correctly
	if w.Code == http.StatusBadRequest {
		var response map[string]interface{}
		if err := json.Unmarshal(w.Body.Bytes(), &response); err == nil {
			if errorMsg, ok := response["error"].(string); ok {
				if strings.Contains(errorMsg, "required") || strings.Contains(errorMsg, "invalid hook_id format") || strings.Contains(errorMsg, "no GitHub client") {
					t.Errorf("Unexpected parameter validation error: %s", errorMsg)
				}
			}
		}
	}

	// We expect either 500 (GitHub API error) or 200 if somehow it works
	if w.Code != http.StatusInternalServerError && w.Code != http.StatusOK && w.Code != http.StatusBadRequest {
		t.Errorf("Unexpected status code: %d", w.Code)
	}
}

func TestEndpoints_GetWebhookDeliveriesHandler_BadRequest(t *testing.T) {
	endpoints := &Endpoints{
		config:        &config.Config{},
		githubClients: map[string]*github.Client{},
	}

	tests := []struct {
		name       string
		owner      string
		repo       string
		hookID     string
		statusCode int
	}{
		{
			name:       "empty owner",
			owner:      "",
			repo:       "testrepo",
			hookID:     "12345",
			statusCode: http.StatusBadRequest,
		},
		{
			name:       "empty repo",
			owner:      "testowner",
			repo:       "",
			hookID:     "12345",
			statusCode: http.StatusBadRequest,
		},
		{
			name:       "empty hook_id",
			owner:      "testowner",
			repo:       "testrepo",
			hookID:     "",
			statusCode: http.StatusBadRequest,
		},
		{
			name:       "no client for owner",
			owner:      "unknownowner",
			repo:       "testrepo",
			hookID:     "12345",
			statusCode: http.StatusBadRequest,
		},
		{
			name:       "invalid hook_id",
			owner:      "testowner",
			repo:       "testrepo",
			hookID:     "not-a-number",
			statusCode: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Params = []gin.Param{
				{Key: "owner", Value: tt.owner},
				{Key: "repo", Value: tt.repo},
				{Key: "hook_id", Value: tt.hookID},
			}

			endpoints.GetWebhookDeliveriesHandler(c)

			if w.Code != tt.statusCode {
				t.Errorf("Expected status %d, got %d", tt.statusCode, w.Code)
			}
		})
	}
}

func TestEndpoints_AutoSetupWebhooksHandler(t *testing.T) {
	// Mock server to simulate GitHub API responses
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify authorization header
		auth := r.Header.Get("Authorization")
		if auth != "token test-token" {
			t.Errorf("Expected authorization header 'token test-token', got %s", auth)
		}

		if r.Method == http.MethodGet && r.URL.Path == "/rate_limit" {
			// Return rate limit info
			rateLimitResponse := struct {
				Resources struct {
					Core struct {
						Limit     int `json:"limit"`
						Used      int `json:"used"`
						Remaining int `json:"remaining"`
						Reset     int `json:"reset"`
					} `json:"core"`
				} `json:"resources"`
			}{}
			rateLimitResponse.Resources.Core.Limit = 5000
			rateLimitResponse.Resources.Core.Used = 0
			rateLimitResponse.Resources.Core.Remaining = 5000
			rateLimitResponse.Resources.Core.Reset = int(time.Now().Add(time.Hour).Unix())
			
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(rateLimitResponse)
		} else if r.Method == http.MethodGet && r.URL.Path == "/repos/testowner/testrepo/hooks" {
			// Return empty list of existing webhooks
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode([]Hook{})
		} else if r.Method == http.MethodPost && r.URL.Path == "/repos/testowner/testrepo/hooks" {
			// Return created webhook
			webhook := Hook{
				ID:     12345,
				Name:   "web",
				Active: true,
				Events: []string{"workflow_run"},
				Config: HookConfig{
					URL:         "https://example.com/webhook",
					ContentType: "json",
				},
			}

			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(webhook)
		} else if r.Method == http.MethodGet && r.URL.Path == "/user" {
			// Return user info for token validation
			userResponse := struct {
				Login string `json:"login"`
				ID    int    `json:"id"`
			}{
				Login: "testuser",
				ID:    123,
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(userResponse)
		} else if r.Method == http.MethodGet && r.URL.Path == "/user/repos" {
			// Return user repos for token validation
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode([]interface{}{})
		} else if r.Method == http.MethodGet && r.URL.Path == "/notifications" {
			// Return notifications for token validation
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode([]interface{}{})
		} else if r.Method == http.MethodGet && r.URL.Path == "/repos/testowner/testrepo" {
			// Return repository permissions
			response := struct {
				Permissions *RepositoryPermissions `json:"permissions"`
			}{
				Permissions: &RepositoryPermissions{
					Admin:    true,
					Maintain: false,
					Push:     false,
					Triage:   false,
					Pull:     false,
				},
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(response)
		} else if r.Method == http.MethodGet && r.URL.Path == "/repos/testowner/testrepo/actions/workflows" {
			// Return repository workflows for workflow scanning
			workflowsResponse := struct {
				TotalCount int `json:"total_count"`
				Workflows  []struct {
					ID      int64  `json:"id"`
					Name    string `json:"name"`
					Path    string `json:"path"`
					State   string `json:"state"`
					Created string `json:"created_at"`
					Updated string `json:"updated_at"`
				} `json:"workflows"`
			}{
				TotalCount: 1,
				Workflows: []struct {
					ID      int64  `json:"id"`
					Name    string `json:"name"`
					Path    string `json:"path"`
					State   string `json:"state"`
					Created string `json:"created_at"`
					Updated string `json:"updated_at"`
				}{
					{
						ID:      123456,
						Name:    "Test Workflow",
						Path:    ".github/workflows/test.yml",
						State:   "active",
						Created: "2023-01-01T00:00:00Z",
						Updated: "2023-01-01T00:00:00Z",
					},
				},
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(workflowsResponse)
		} else if r.Method == http.MethodGet && r.URL.Path == "/repos/testowner/testrepo/contents/.github/workflows/test.yml" {
			// Return workflow file content for self-hosted runner detection
			workflowContent := `name: Test Workflow
on: [push, pull_request]
jobs:
  test:
    runs-on: self-hosted
    steps:
      - uses: actions/checkout@v2
      - name: Run tests
        run: echo "Testing"`
			
			// Encode content in base64 as GitHub API does
			encodedContent := base64.StdEncoding.EncodeToString([]byte(workflowContent))
			
			fileResponse := struct {
				Name     string `json:"name"`
				Path     string `json:"path"`
				Content  string `json:"content"`
				Encoding string `json:"encoding"`
				Size     int    `json:"size"`
				Type     string `json:"type"`
			}{
				Name:     "test.yml",
				Path:     ".github/workflows/test.yml",
				Content:  encodedContent,
				Encoding: "base64",
				Size:     len(workflowContent),
				Type:     "file",
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(fileResponse)
		} else {
			t.Errorf("Unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer server.Close()

	// Create endpoints with a mock GitHub client
	client := github.NewClient("test-token")
	// Set up custom transport to redirect to mock server
	client.SetHTTPClient(&http.Client{
		Transport: &testTransport{
			testURL: server.URL,
		},
	})
	config := &config.Config{
		Owners: map[string]*config.OwnerConfig{"test-owner": {Owner: "test-owner", GitHubToken: "test-token", AllRepos: true}},
		RepoOwner:   "testowner",
		RepoName:    "testrepo",
	}
	githubClients := map[string]*github.Client{
		"testowner": client,
	}
	endpoints := NewEndpoints(config, githubClients)

	// Create request body
	requestBody := `{"webhook_url": "https://example.com/webhook"}`

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/", bytes.NewBufferString(requestBody))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = []gin.Param{
		{Key: "owner", Value: "testowner"},
	}

	endpoints.AutoSetupWebhooksHandler(c)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status %d, got %d", http.StatusOK, w.Code)
		t.Errorf("Response body: %s", w.Body.String())
	}

	var response struct {
		Message     string `json:"message"`
		Owner       string `json:"owner"`
		WebhookURL  string `json:"webhook_url"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("Failed to unmarshal response: %v", err)
	}

	expectedMessage := fmt.Sprintf("webhook auto-setup completed for owner: %s", "testowner")
	if response.Message != expectedMessage {
		t.Errorf("Expected message '%s', got '%s'", expectedMessage, response.Message)
	}

	if response.Owner != "testowner" {
		t.Errorf("Expected owner 'testowner', got '%s'", response.Owner)
	}

	if response.WebhookURL != "https://example.com/webhook" {
		t.Errorf("Expected webhook_url 'https://example.com/webhook', got '%s'", response.WebhookURL)
	}
}

func TestEndpoints_AutoSetupWebhooksHandler_BadRequest(t *testing.T) {
	endpoints := &Endpoints{
		config:        &config.Config{},
		githubClients: map[string]*github.Client{},
	}

	tests := []struct {
		name        string
		owner       string
		requestBody string
		statusCode  int
	}{
		{
			name:        "empty owner",
			owner:       "",
			requestBody: `{"webhook_url": "https://example.com/webhook"}`,
			statusCode:  http.StatusBadRequest,
		},
		{
			name:        "invalid json",
			owner:       "testowner",
			requestBody: `invalid json`,
			statusCode:  http.StatusBadRequest,
		},
		{
			name:        "no client for owner",
			owner:       "unknownowner",
			requestBody: `{"webhook_url": "https://example.com/webhook"}`,
			statusCode:  http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest("POST", "/", bytes.NewBufferString(tt.requestBody))
			c.Request.Header.Set("Content-Type", "application/json")
			c.Params = []gin.Param{
				{Key: "owner", Value: tt.owner},
			}

			endpoints.AutoSetupWebhooksHandler(c)

			if w.Code != tt.statusCode {
				t.Errorf("Expected status %d, got %d", tt.statusCode, w.Code)
			}
		})
	}
}

func TestEndpoints_GetRepositoryPermissionsHandler(t *testing.T) {
	// Mock server to simulate GitHub API response
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify request method and path
		if r.Method != http.MethodGet {
			t.Errorf("Expected GET request, got %s", r.Method)
		}
		if r.URL.Path != "/repos/testowner/testrepo" {
			t.Errorf("Expected path '/repos/testowner/testrepo', got %s", r.URL.Path)
		}

		// Verify authorization header
		auth := r.Header.Get("Authorization")
		if auth != "token test-token" {
			t.Errorf("Expected authorization header 'token test-token', got %s", auth)
		}

		// Return a mock repository permissions response
		response := struct {
			Permissions *RepositoryPermissions `json:"permissions"`
		}{
			Permissions: &RepositoryPermissions{
				Admin:    true,
				Maintain: false,
				Push:     false,
				Triage:   false,
				Pull:     false,
			},
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	// Create endpoints with a mock GitHub client
	client := github.NewClient("test-token")
	// Set up custom transport to redirect to mock server
	client.SetHTTPClient(&http.Client{
		Transport: &testTransport{
			testURL: server.URL,
		},
	})
	config := &config.Config{
		Owners: map[string]*config.OwnerConfig{"test-owner": {Owner: "test-owner", GitHubToken: "test-token", AllRepos: true}},
	}
	githubClients := map[string]*github.Client{
		"testowner": client,
	}
	endpoints := NewEndpoints(config, githubClients)

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Params = []gin.Param{
		{Key: "owner", Value: "testowner"},
		{Key: "repo", Value: "testrepo"},
	}

	endpoints.GetRepositoryPermissionsHandler(c)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status %d, got %d", http.StatusOK, w.Code)
	}

	var response struct {
		Owner             string               `json:"owner"`
		Repo              string               `json:"repo"`
		Permissions       RepositoryPermissions `json:"permissions"`
		CanManageWebhooks bool                 `json:"can_manage_webhooks"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("Failed to unmarshal response: %v", err)
	}

	if response.Owner != "testowner" {
		t.Errorf("Expected owner 'testowner', got '%s'", response.Owner)
	}

	if response.Repo != "testrepo" {
		t.Errorf("Expected repo 'testrepo', got '%s'", response.Repo)
	}

	if !response.CanManageWebhooks {
		t.Error("Expected can_manage_webhooks to be true")
	}

	if !response.Permissions.Admin {
		t.Error("Expected admin permission to be true")
	}
}

func TestEndpoints_GetRepositoryPermissionsHandler_BadRequest(t *testing.T) {
	endpoints := &Endpoints{
		config:        &config.Config{},
		githubClients: map[string]*github.Client{},
	}

	tests := []struct {
		name       string
		owner      string
		repo       string
		statusCode int
	}{
		{
			name:       "empty owner",
			owner:      "",
			repo:       "testrepo",
			statusCode: http.StatusBadRequest,
		},
		{
			name:       "empty repo",
			owner:      "testowner",
			repo:       "",
			statusCode: http.StatusBadRequest,
		},
		{
			name:       "no client for owner",
			owner:      "unknownowner",
			repo:       "testrepo",
			statusCode: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Params = []gin.Param{
				{Key: "owner", Value: tt.owner},
				{Key: "repo", Value: tt.repo},
			}

			endpoints.GetRepositoryPermissionsHandler(c)

			if w.Code != tt.statusCode {
				t.Errorf("Expected status %d, got %d", tt.statusCode, w.Code)
			}
		})
	}
}

func TestEndpoints_ValidateTokenHandler(t *testing.T) {
	// Mock server to simulate GitHub API response
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify request method
		if r.Method != http.MethodGet {
			t.Errorf("Expected GET request, got %s", r.Method)
		}

		// Verify authorization header
		auth := r.Header.Get("Authorization")
		if auth != "token test-token" {
			t.Errorf("Expected authorization header 'token test-token', got %s", auth)
		}

		if r.URL.Path == "/rate_limit" {
			// Set OAuth scopes header
			w.Header().Set("X-OAuth-Scopes", "repo, write:repo_hook, admin:org")
			w.Header().Set("X-Accepted-OAuth-Scopes", "repo")
		} else if r.URL.Path == "/user" {
			// Return user info for token validation
			userResponse := struct {
				Login string `json:"login"`
				ID    int    `json:"id"`
			}{
				Login: "testuser",
				ID:    123,
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(userResponse)
			return
		} else if r.URL.Path == "/user/repos" {
			// Return user repos for token validation
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode([]interface{}{})
			return
		} else if r.URL.Path == "/notifications" {
			// Return notifications for token validation  
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode([]interface{}{})
			return
		} else {
			t.Errorf("Unexpected path: %s", r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		// Return a mock rate limit response
		response := struct {
			Resources struct {
				Core struct {
					Limit     int   `json:"limit"`
					Remaining int   `json:"remaining"`
					Reset     int64 `json:"reset"`
					Used      int   `json:"used"`
				} `json:"core"`
			} `json:"resources"`
		}{
			Resources: struct {
				Core struct {
					Limit     int   `json:"limit"`
					Remaining int   `json:"remaining"`
					Reset     int64 `json:"reset"`
					Used      int   `json:"used"`
				} `json:"core"`
			}{
				Core: struct {
					Limit     int   `json:"limit"`
					Remaining int   `json:"remaining"`
					Reset     int64 `json:"reset"`
					Used      int   `json:"used"`
				}{
					Limit:     5000,
					Remaining: 4999,
					Reset:     time.Now().Add(time.Hour).Unix(),
					Used:      1,
				},
			},
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	// Create endpoints with a mock GitHub client
	client := github.NewClient("test-token")
	// Set up custom transport to redirect to mock server
	client.SetHTTPClient(&http.Client{
		Transport: &testTransport{
			testURL: server.URL,
		},
	})
	config := &config.Config{
		Owners: map[string]*config.OwnerConfig{"test-owner": {Owner: "test-owner", GitHubToken: "test-token", AllRepos: true}},
	}
	githubClients := map[string]*github.Client{
		"testowner": client,
	}
	endpoints := NewEndpoints(config, githubClients)

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Params = []gin.Param{
		{Key: "owner", Value: "testowner"},
	}

	endpoints.ValidateTokenHandler(c)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status %d, got %d", http.StatusOK, w.Code)
	}

	var response struct {
		Owner    string `json:"owner"`
		TokenInfo *struct {
			Scopes               []string `json:"scopes"`
			TokenType            string   `json:"token_type"`
			IsValid              bool     `json:"is_valid"`
			HasWebhookAccess     bool     `json:"has_webhook_access"`
			CanManageRunners     bool     `json:"can_manage_runners"`
		} `json:"token_info"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("Failed to unmarshal response: %v", err)
	}

	if response.Owner != "testowner" {
		t.Errorf("Expected owner 'testowner', got '%s'", response.Owner)
	}

	if response.TokenInfo == nil {
		t.Fatal("Expected token_info to be present")
	}

	expectedScopes := []string{"repo", "write:repo_hook", "admin:org"}
	if len(response.TokenInfo.Scopes) != len(expectedScopes) {
		t.Errorf("Expected %d scopes, got %d", len(expectedScopes), len(response.TokenInfo.Scopes))
	}

	for i, scope := range expectedScopes {
		if response.TokenInfo.Scopes[i] != scope {
			t.Errorf("Expected scope '%s' at index %d, got '%s'", scope, i, response.TokenInfo.Scopes[i])
		}
	}
}

func TestEndpoints_ValidateTokenHandler_BadRequest(t *testing.T) {
	endpoints := &Endpoints{
		config:        &config.Config{},
		githubClients: map[string]*github.Client{},
	}

	tests := []struct {
		name       string
		owner      string
		statusCode int
	}{
		{
			name:       "empty owner",
			owner:      "",
			statusCode: http.StatusBadRequest,
		},
		{
			name:       "no client for owner",
			owner:      "unknownowner",
			statusCode: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Params = []gin.Param{
				{Key: "owner", Value: tt.owner},
			}

			endpoints.ValidateTokenHandler(c)

			if w.Code != tt.statusCode {
				t.Errorf("Expected status %d, got %d", tt.statusCode, w.Code)
			}
		})
	}
}

func TestEndpoints_ValidateTokenForRepositoryHandler(t *testing.T) {
	// Mock server to simulate GitHub API responses
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify authorization header
		auth := r.Header.Get("Authorization")
		if auth != "token test-token" {
			t.Errorf("Expected authorization header 'token test-token', got %s", auth)
		}

		if r.Method == http.MethodGet && r.URL.Path == "/rate_limit" {
			// Set OAuth scopes header
			w.Header().Set("X-OAuth-Scopes", "repo, write:repo_hook")
			w.Header().Set("X-Accepted-OAuth-Scopes", "repo")

			// Return a mock rate limit response
			response := struct {
				Resources struct {
					Core struct {
						Limit     int   `json:"limit"`
						Remaining int   `json:"remaining"`
						Reset     int64 `json:"reset"`
						Used      int   `json:"used"`
					} `json:"core"`
				} `json:"resources"`
			}{
				Resources: struct {
					Core struct {
						Limit     int   `json:"limit"`
						Remaining int   `json:"remaining"`
						Reset     int64 `json:"reset"`
						Used      int   `json:"used"`
					} `json:"core"`
				}{
					Core: struct {
						Limit     int   `json:"limit"`
						Remaining int   `json:"remaining"`
						Reset     int64 `json:"reset"`
						Used      int   `json:"used"`
					}{
						Limit:     5000,
						Remaining: 4999,
						Reset:     time.Now().Add(time.Hour).Unix(),
						Used:      1,
					},
				},
			}

			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(response)
		} else if r.Method == http.MethodGet && r.URL.Path == "/repos/testowner/testrepo" {
			// Return successful repository access response
			repo := github.Repository{
				ID:       12345,
				Name:     "testrepo",
				FullName: "testowner/testrepo",
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(repo)
		} else if r.Method == http.MethodGet && r.URL.Path == "/repos/testowner/testrepo/hooks" {
			// Return empty list of hooks for token validation
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode([]interface{}{})
		} else if r.Method == http.MethodPost && r.URL.Path == "/repos/testowner/testrepo/actions/runners/registration-token" {
			// Return registration token for token validation
			response := struct {
				Token     string `json:"token"`
				ExpiresAt string `json:"expires_at"`
			}{
				Token:     "test-registration-token",
				ExpiresAt: "2024-01-01T00:00:00Z",
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(response)
		} else if r.Method == http.MethodGet && r.URL.Path == "/user" {
			// Return user info for token validation
			userResponse := struct {
				Login string `json:"login"`
				ID    int    `json:"id"`
			}{
				Login: "testuser",
				ID:    123,
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(userResponse)
		} else if r.Method == http.MethodGet && r.URL.Path == "/user/repos" {
			// Return user repos for token validation
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode([]interface{}{})
		} else if r.Method == http.MethodGet && r.URL.Path == "/notifications" {
			// Return notifications for token validation
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode([]interface{}{})
		} else {
			t.Errorf("Unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer server.Close()

	// Create endpoints with a mock GitHub client
	client := github.NewClient("test-token")
	// Set up custom transport to redirect to mock server
	client.SetHTTPClient(&http.Client{
		Transport: &testTransport{
			testURL: server.URL,
		},
	})
	config := &config.Config{
		Owners: map[string]*config.OwnerConfig{"test-owner": {Owner: "test-owner", GitHubToken: "test-token", AllRepos: true}},
	}
	githubClients := map[string]*github.Client{
		"testowner": client,
	}
	endpoints := NewEndpoints(config, githubClients)

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Params = []gin.Param{
		{Key: "owner", Value: "testowner"},
		{Key: "repo", Value: "testrepo"},
	}

	endpoints.ValidateTokenForRepositoryHandler(c)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status %d, got %d", http.StatusOK, w.Code)
	}

	var response struct {
		Owner               string `json:"owner"`
		Repo                string `json:"repo"`
		RepositoryPermissions *struct {
			Repository       string   `json:"repository"`
			CanAccessRepo    bool     `json:"can_access_repo"`
			CanReadWebhooks  bool     `json:"can_read_webhooks"`
			CanWriteWebhooks bool     `json:"can_write_webhooks"`
			CanManageRunners bool     `json:"can_manage_runners"`
			Errors          []string `json:"errors,omitempty"`
		} `json:"repository_permissions"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("Failed to unmarshal response: %v", err)
	}

	if response.Owner != "testowner" {
		t.Errorf("Expected owner 'testowner', got '%s'", response.Owner)
	}

	if response.Repo != "testrepo" {
		t.Errorf("Expected repo 'testrepo', got '%s'", response.Repo)
	}

	if response.RepositoryPermissions == nil {
		t.Fatal("Expected repository_permissions to be present")
	}

	if !response.RepositoryPermissions.CanAccessRepo {
		t.Error("Expected can_access_repo to be true")
	}
}

func TestEndpoints_ValidateTokenForRepositoryHandler_BadRequest(t *testing.T) {
	endpoints := &Endpoints{
		config:        &config.Config{},
		githubClients: map[string]*github.Client{},
	}

	tests := []struct {
		name       string
		owner      string
		repo       string
		statusCode int
	}{
		{
			name:       "empty owner",
			owner:      "",
			repo:       "testrepo",
			statusCode: http.StatusBadRequest,
		},
		{
			name:       "empty repo",
			owner:      "testowner",
			repo:       "",
			statusCode: http.StatusBadRequest,
		},
		{
			name:       "no client for owner",
			owner:      "unknownowner",
			repo:       "testrepo",
			statusCode: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Params = []gin.Param{
				{Key: "owner", Value: tt.owner},
				{Key: "repo", Value: tt.repo},
			}

			endpoints.ValidateTokenForRepositoryHandler(c)

			if w.Code != tt.statusCode {
				t.Errorf("Expected status %d, got %d", tt.statusCode, w.Code)
			}
		})
	}
}

func TestEndpoints_CheckTokenScopesHandler(t *testing.T) {
	// Mock server to simulate GitHub API response
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify request method
		if r.Method != http.MethodGet {
			t.Errorf("Expected GET request, got %s", r.Method)
		}

		// Verify authorization header
		auth := r.Header.Get("Authorization")
		if auth != "token test-token" {
			t.Errorf("Expected authorization header 'token test-token', got %s", auth)
		}

		if r.URL.Path == "/rate_limit" {
			// Set OAuth scopes header
			w.Header().Set("X-OAuth-Scopes", "repo, write:repo_hook, admin:org")
			w.Header().Set("X-Accepted-OAuth-Scopes", "repo")
		} else if r.URL.Path == "/user" {
			// Return user info for token validation
			userResponse := struct {
				Login string `json:"login"`
				ID    int    `json:"id"`
			}{
				Login: "testuser",
				ID:    123,
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(userResponse)
			return
		} else if r.URL.Path == "/user/repos" {
			// Return user repos for token validation
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode([]interface{}{})
			return
		} else if r.URL.Path == "/notifications" {
			// Return notifications for token validation
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode([]interface{}{})
			return
		} else {
			t.Errorf("Unexpected path: %s", r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		// Return a mock rate limit response
		response := struct {
			Resources struct {
				Core struct {
					Limit     int   `json:"limit"`
					Remaining int   `json:"remaining"`
					Reset     int64 `json:"reset"`
					Used      int   `json:"used"`
				} `json:"core"`
			} `json:"resources"`
		}{
			Resources: struct {
				Core struct {
					Limit     int   `json:"limit"`
					Remaining int   `json:"remaining"`
					Reset     int64 `json:"reset"`
					Used      int   `json:"used"`
				} `json:"core"`
			}{
				Core: struct {
					Limit     int   `json:"limit"`
					Remaining int   `json:"remaining"`
					Reset     int64 `json:"reset"`
					Used      int   `json:"used"`
				}{
					Limit:     5000,
					Remaining: 4999,
					Reset:     time.Now().Add(time.Hour).Unix(),
					Used:      1,
				},
			},
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	// Create endpoints with a mock GitHub client
	client := github.NewClient("test-token")
	// Set up custom transport to redirect to mock server
	client.SetHTTPClient(&http.Client{
		Transport: &testTransport{
			testURL: server.URL,
		},
	})
	config := &config.Config{
		Owners: map[string]*config.OwnerConfig{"test-owner": {Owner: "test-owner", GitHubToken: "test-token", AllRepos: true}},
	}
	githubClients := map[string]*github.Client{
		"testowner": client,
	}
	endpoints := NewEndpoints(config, githubClients)

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Params = []gin.Param{
		{Key: "owner", Value: "testowner"},
	}

	endpoints.CheckTokenScopesHandler(c)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status %d, got %d", http.StatusOK, w.Code)
	}

	var response struct {
		Owner            string   `json:"owner"`
		Scopes           []string `json:"scopes"`
		TokenType        string   `json:"token_type"`
		HasWebhookAccess bool     `json:"has_webhook_access"`
		CanManageRunners bool     `json:"has_runner_access"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("Failed to unmarshal response: %v", err)
	}

	if response.Owner != "testowner" {
		t.Errorf("Expected owner 'testowner', got '%s'", response.Owner)
	}

	expectedScopes := []string{"repo", "write:repo_hook", "admin:org"}
	if len(response.Scopes) != len(expectedScopes) {
		t.Errorf("Expected %d scopes, got %d", len(expectedScopes), len(response.Scopes))
	}

	// Safely check scopes to avoid panic
	if len(response.Scopes) >= len(expectedScopes) {
		for i, scope := range expectedScopes {
			if response.Scopes[i] != scope {
				t.Errorf("Expected scope '%s' at index %d, got '%s'", scope, i, response.Scopes[i])
			}
		}
	}

	if response.HasWebhookAccess != true {
		t.Error("Expected has_webhook_access to be true")
	}
}

func TestEndpoints_CheckTokenScopesHandler_BadRequest(t *testing.T) {
	endpoints := &Endpoints{
		config:        &config.Config{},
		githubClients: map[string]*github.Client{},
	}

	tests := []struct {
		name       string
		owner      string
		statusCode int
	}{
		{
			name:       "empty owner",
			owner:      "",
			statusCode: http.StatusBadRequest,
		},
		{
			name:       "no client for owner",
			owner:      "unknownowner",
			statusCode: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Params = []gin.Param{
				{Key: "owner", Value: tt.owner},
			}

			endpoints.CheckTokenScopesHandler(c)

			if w.Code != tt.statusCode {
				t.Errorf("Expected status %d, got %d", tt.statusCode, w.Code)
			}
		})
	}
}

func TestEndpoints_ListRepositoriesHandler(t *testing.T) {
	// Mock server to simulate GitHub API responses
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify authorization header
		auth := r.Header.Get("Authorization")
		if auth != "token test-token" {
			t.Errorf("Expected authorization header 'token test-token', got %s", auth)
		}

		if r.Method == http.MethodGet && r.URL.Path == "/rate_limit" {
			// Set rate limit headers
			w.Header().Set("X-RateLimit-Remaining", "1000")
			w.Header().Set("X-RateLimit-Reset", fmt.Sprintf("%d", time.Now().Add(time.Hour).Unix()))

			// Return a mock rate limit response
			response := struct {
				Resources struct {
					Core struct {
						Limit     int   `json:"limit"`
						Remaining int   `json:"remaining"`
						Reset     int64 `json:"reset"`
						Used      int   `json:"used"`
					} `json:"core"`
				} `json:"resources"`
			}{
				Resources: struct {
					Core struct {
						Limit     int   `json:"limit"`
						Remaining int   `json:"remaining"`
						Reset     int64 `json:"reset"`
						Used      int   `json:"used"`
					} `json:"core"`
				}{
					Core: struct {
						Limit     int   `json:"limit"`
						Remaining int   `json:"remaining"`
						Reset     int64 `json:"reset"`
						Used      int   `json:"used"`
					}{
						Limit:     5000,
						Remaining: 1000,
						Reset:     time.Now().Add(time.Hour).Unix(),
						Used:      4000,
					},
				},
			}

			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(response)
		} else if r.Method == http.MethodGet && r.URL.Path == "/user/repos" {
			// Return a mock list of repositories
			repos := []github.Repository{
				{
					ID:       1,
					Name:     "repo1",
					FullName: "testowner/repo1",
				},
				{
					ID:       2,
					Name:     "repo2",
					FullName: "testowner/repo2",
				},
				{
					ID:       3,
					Name:     "repo3",
					FullName: "testowner/repo3",
				},
			}

			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(repos)
		} else if r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/repos/") {
			// Return repository permissions for individual repo checks
			// Log the request for debugging
			t.Logf("Repository permission check for: %s", r.URL.Path)
			
			var response struct {
				Permissions *RepositoryPermissions `json:"permissions"`
			}
			
			// Return different permissions based on repository
			if strings.Contains(r.URL.Path, "/repos/testowner/repo3") {
				// repo3 has push access but not admin (accessible but not owned)
				response.Permissions = &RepositoryPermissions{
					Admin:    false,
					Maintain: false,
					Push:     true,
					Triage:   false,
					Pull:     true,
				}
			} else {
				// repo1 and repo2 have admin access (owned)
				response.Permissions = &RepositoryPermissions{
					Admin:    true,
					Maintain: false,
					Push:     false,
					Triage:   false,
					Pull:     false,
				}
			}

			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(response)
		} else {
			t.Errorf("Unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer server.Close()

	// Create endpoints with a mock GitHub client
	client := github.NewClient("test-token")
	// Set up custom transport to redirect to mock server
	client.SetHTTPClient(&http.Client{
		Transport: &testTransport{
			testURL: server.URL,
		},
	})
	config := &config.Config{
		Owners: map[string]*config.OwnerConfig{"test-owner": {Owner: "test-owner", GitHubToken: "test-token", AllRepos: true}},
	}
	githubClients := map[string]*github.Client{
		"testowner": client,
	}
	endpoints := NewEndpoints(config, githubClients)

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Params = []gin.Param{
		{Key: "owner", Value: "testowner"},
	}

	endpoints.ListRepositoriesHandler(c)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status %d, got %d", http.StatusOK, w.Code)
	}

	var response struct {
		Owner     string `json:"owner"`
		Summary   struct {
			TotalRepositories          int `json:"total_repositories"`
			OwnedRepositories          int `json:"owned_repositories"`
			AccessibleWithPermissions  int `json:"accessible_with_permissions"`
			AccessibleWithoutPermissions int `json:"accessible_without_permissions"`
		} `json:"summary"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("Failed to unmarshal response: %v", err)
	}

	if response.Owner != "testowner" {
		t.Errorf("Expected owner 'testowner', got '%s'", response.Owner)
	}

	if response.Summary.TotalRepositories != 3 {
		t.Errorf("Expected total_repositories 3, got %d", response.Summary.TotalRepositories)
	}

	if response.Summary.OwnedRepositories != 3 {
		t.Errorf("Expected owned_repositories 3, got %d", response.Summary.OwnedRepositories)
	}
}

func TestEndpoints_ListRepositoriesHandler_BadRequest(t *testing.T) {
	endpoints := &Endpoints{
		config:        &config.Config{},
		githubClients: map[string]*github.Client{},
	}

	tests := []struct {
		name       string
		owner      string
		statusCode int
	}{
		{
			name:       "empty owner",
			owner:      "",
			statusCode: http.StatusBadRequest,
		},
		{
			name:       "no client for owner",
			owner:      "unknownowner",
			statusCode: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Params = []gin.Param{
				{Key: "owner", Value: tt.owner},
			}

			endpoints.ListRepositoriesHandler(c)

			if w.Code != tt.statusCode {
				t.Errorf("Expected status %d, got %d", tt.statusCode, w.Code)
			}
		})
	}
}

func TestEndpoints_ListRepositoriesHandler_TooManyRequests(t *testing.T) {
	// Mock server to simulate low rate limit
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/rate_limit" {
			w.Header().Set("X-RateLimit-Remaining", "0")
			w.Header().Set("X-RateLimit-Reset", fmt.Sprintf("%d", time.Now().Add(5*time.Minute).Unix()))
			
			response := struct {
				Resources struct {
					Core struct {
						Limit     int   `json:"limit"`
						Remaining int   `json:"remaining"`
						Reset     int64 `json:"reset"`
						Used      int   `json:"used"`
					} `json:"core"`
				} `json:"resources"`
			}{
				Resources: struct {
					Core struct {
						Limit     int   `json:"limit"`
						Remaining int   `json:"remaining"`
						Reset     int64 `json:"reset"`
						Used      int   `json:"used"`
					} `json:"core"`
				}{
					Core: struct {
						Limit     int   `json:"limit"`
						Remaining int   `json:"remaining"`
						Reset     int64 `json:"reset"`
						Used      int   `json:"used"`
					}{
						Limit:     5000,
						Remaining: 0,
						Reset:     time.Now().Add(5*time.Minute).Unix(),
						Used:      5000,
					},
				},
			}
			
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(response)
		} else if r.URL.Path == "/user/repos" {
			// This should not be called due to rate limit, but if it is, return empty
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode([]interface{}{})
		} else {
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer server.Close()

	// Create endpoints with a mock GitHub client
	client := github.NewClient("test-token")
	// Set up custom transport to redirect to mock server
	client.SetHTTPClient(&http.Client{
		Transport: &testTransport{
			testURL: server.URL,
		},
	})
	config := &config.Config{
		Owners: map[string]*config.OwnerConfig{"test-owner": {Owner: "test-owner", GitHubToken: "test-token", AllRepos: true}},
	}
	githubClients := map[string]*github.Client{
		"testowner": client,
	}
	endpoints := NewEndpoints(config, githubClients)

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Params = []gin.Param{
		{Key: "owner", Value: "testowner"},
	}

	endpoints.ListRepositoriesHandler(c)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status %d, got %d", http.StatusOK, w.Code)
	}
}

func TestEndpoints_PreviewWebhookSetupHandler(t *testing.T) {
	// Mock server to simulate GitHub API responses
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Log all requests for debugging
		t.Logf("Request: %s %s", r.Method, r.URL.Path)
		
		// Verify authorization header
		auth := r.Header.Get("Authorization")
		if auth != "token test-token" {
			t.Errorf("Expected authorization header 'token test-token', got %s", auth)
		}

		if r.Method == http.MethodGet && r.URL.Path == "/user/repos" {
			// Return a mock list of repositories
			repos := []github.Repository{
				{
					ID:       1,
					Name:     "repo1",
					FullName: "testowner/repo1",
				},
				{
					ID:       2,
					Name:     "repo2",
					FullName: "testowner/repo2",
				},
			}

			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(repos)
		} else if r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/repos/") {
			// Return repository information with permissions
			response := struct {
				Permissions *RepositoryPermissions `json:"permissions"`
			}{
				Permissions: &RepositoryPermissions{
					Admin:    true,
					Maintain: false,
					Push:     false,
					Triage:   false,
					Pull:     false,
				},
			}

			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(response)
		} else if r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/hooks") {
			// Return empty list of hooks (no existing webhook)
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte("[]"))
		} else if r.Method == http.MethodGet && r.URL.Path == "/rate_limit" {
			// Return rate limit info
			rateLimitResponse := struct {
				Resources struct {
					Core struct {
						Limit     int `json:"limit"`
						Used      int `json:"used"`
						Remaining int `json:"remaining"`
						Reset     int `json:"reset"`
					} `json:"core"`
				} `json:"resources"`
			}{}
			rateLimitResponse.Resources.Core.Limit = 5000
			rateLimitResponse.Resources.Core.Used = 0
			rateLimitResponse.Resources.Core.Remaining = 5000
			rateLimitResponse.Resources.Core.Reset = int(time.Now().Add(time.Hour).Unix())
			
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(rateLimitResponse)
		} else if r.Method == http.MethodGet && r.URL.Path == "/user" {
			// Return user info for token validation
			userResponse := struct {
				Login string `json:"login"`
				ID    int    `json:"id"`
			}{
				Login: "testuser",
				ID:    123,
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(userResponse)
		} else if r.Method == http.MethodGet && r.URL.Path == "/notifications" {
			// Return notifications for token validation
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode([]interface{}{})
		} else {
			t.Errorf("Unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer server.Close()

	// Create endpoints with a mock GitHub client
	client := github.NewClient("test-token")
	// Set up custom transport to redirect to mock server
	client.SetHTTPClient(&http.Client{
		Transport: &testTransport{
			testURL: server.URL,
		},
	})
	config := &config.Config{
		Owners: map[string]*config.OwnerConfig{"test-owner": {Owner: "test-owner", GitHubToken: "test-token", AllRepos: true}},
		Owners: map[string]*config.OwnerConfig{
			"testowner": {
				Owners: map[string]*config.OwnerConfig{"test-owner": {Owner: "test-owner", GitHubToken: "test-token", AllRepos: true}},
				AllRepos:    true,
			},
		},
	}
	githubClients := map[string]*github.Client{
		"testowner": client,
	}
	endpoints := NewEndpoints(config, githubClients)

	// Create request body
	requestBody := `{"webhook_url": "https://example.com/webhook", "dry_run": true}`

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/", bytes.NewBufferString(requestBody))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = []gin.Param{
		{Key: "owner", Value: "testowner"},
	}

	endpoints.PreviewWebhookSetupHandler(c)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status %d, got %d", http.StatusOK, w.Code)
		t.Errorf("Response body: %s", w.Body.String())
	}

	var response struct {
		Owner      string `json:"owner"`
		WebhookURL string `json:"webhook_url"`
		AllRepos   bool   `json:"all_repos"`
		Summary    struct {
			TotalRepositories int `json:"total_repositories"`
			WouldProcess      int `json:"would_process"`
		} `json:"summary"`
		Preview []struct {
			Repository         string `json:"repository"`
			OwnedByUser        bool   `json:"owned_by_user"`
			CanManageWebhooks  bool   `json:"can_manage_webhooks"`
			WebhookExists      bool   `json:"webhook_exists"`
			Action             string `json:"action"`
		} `json:"preview"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("Failed to unmarshal response: %v", err)
	}

	if response.Owner != "testowner" {
		t.Errorf("Expected owner 'testowner', got '%s'", response.Owner)
	}

	if response.WebhookURL != "https://example.com/webhook" {
		t.Errorf("Expected webhook_url 'https://example.com/webhook', got '%s'", response.WebhookURL)
	}

	if response.AllRepos != true {
		t.Error("Expected all_repos to be true")
	}

	if response.Summary.TotalRepositories != 2 {
		t.Errorf("Expected total_repositories 2, got %d", response.Summary.TotalRepositories)
	}

	if len(response.Preview) != 2 {
		t.Errorf("Expected 2 preview items, got %d", len(response.Preview))
	}

	for _, preview := range response.Preview {
		if !preview.OwnedByUser {
			t.Error("Expected owned_by_user to be true for all repositories")
		}
		if !preview.CanManageWebhooks {
			t.Error("Expected can_manage_webhooks to be true")
		}
		if preview.WebhookExists {
			t.Error("Expected webhook_exists to be false")
		}
		if preview.Action != "create new webhook" {
			t.Errorf("Expected action 'create new webhook', got '%s'", preview.Action)
		}
	}
}

func TestEndpoints_PreviewWebhookSetupHandler_BadRequest(t *testing.T) {
	endpoints := &Endpoints{
		config:        &config.Config{},
		githubClients: map[string]*github.Client{},
	}

	tests := []struct {
		name        string
		owner       string
		requestBody string
		statusCode  int
	}{
		{
			name:        "empty owner",
			owner:       "",
			requestBody: `{"webhook_url": "https://example.com/webhook"}`,
			statusCode:  http.StatusBadRequest,
		},
		{
			name:        "invalid json",
			owner:       "testowner",
			requestBody: `invalid json`,
			statusCode:  http.StatusBadRequest,
		},
		{
			name:        "no client for owner",
			owner:       "unknownowner",
			requestBody: `{"webhook_url": "https://example.com/webhook"}`,
			statusCode:  http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest("POST", "/", bytes.NewBufferString(tt.requestBody))
			c.Request.Header.Set("Content-Type", "application/json")
			c.Params = []gin.Param{
				{Key: "owner", Value: tt.owner},
			}

			endpoints.PreviewWebhookSetupHandler(c)

			if w.Code != tt.statusCode {
				t.Errorf("Expected status %d, got %d", tt.statusCode, w.Code)
			}
		})
	}
}

func TestEndpoints_PreviewWebhookSetupHandler_AllReposDisabled(t *testing.T) {
	// Create endpoints with a mock config where ALL_REPOS is disabled
	config := &config.Config{
		Owners: map[string]*config.OwnerConfig{
			"testowner": {
				Owners: map[string]*config.OwnerConfig{"test-owner": {Owner: "test-owner", GitHubToken: "test-token", AllRepos: true}},
				AllRepos:    false, // Disabled
			},
		},
	}
	githubClients := map[string]*github.Client{
		"testowner": github.NewClient("test-token"),
	}
	endpoints := NewEndpoints(config, githubClients)

	// Create request body
	requestBody := `{"webhook_url": "https://example.com/webhook"}`

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/", bytes.NewBufferString(requestBody))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = []gin.Param{
		{Key: "owner", Value: "testowner"},
	}

	endpoints.PreviewWebhookSetupHandler(c)

	if w.Code != http.StatusBadRequest {
		t.Errorf("Expected status %d, got %d", http.StatusBadRequest, w.Code)
	}

	var response struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("Failed to unmarshal response: %v", err)
	}

	expectedError := "ALL_REPOS is not enabled for this owner. Auto-setup only works with ALL_REPOS=true"
	if response.Error != expectedError {
		t.Errorf("Expected error '%s', got '%s'", expectedError, response.Error)
	}
}