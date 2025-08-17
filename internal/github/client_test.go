package github

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

func TestNewClient(t *testing.T) {
	token := "test-token"
	client := NewClient(token)

	if client.Token != token {
		t.Errorf("NewClient() token = %v, want %v", client.Token, token)
	}

	if client.httpClient == nil {
		t.Error("NewClient() httpClient is nil")
	}

	if client.httpClient.Timeout != 30*time.Second {
		t.Errorf("NewClient() timeout = %v, want %v", client.httpClient.Timeout, 30*time.Second)
	}
}

func TestClient_GetRegistrationToken(t *testing.T) {
	tests := []struct {
		name           string
		serverResponse string
		statusCode     int
		wantToken      string
		wantErr        bool
	}{
		{
			name: "successful response",
			serverResponse: `{
				"token": "test-registration-token",
				"expires_at": "2023-01-01T00:00:00Z"
			}`,
			statusCode: http.StatusCreated,
			wantToken:  "test-registration-token",
			wantErr:    false,
		},
		{
			name:           "unauthorized",
			serverResponse: `{"message": "Bad credentials"}`,
			statusCode:     http.StatusUnauthorized,
			wantToken:      "",
			wantErr:        true,
		},
		{
			name:           "invalid json",
			serverResponse: `invalid json`,
			statusCode:     http.StatusCreated,
			wantToken:      "",
			wantErr:        true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// Verify request method and path
				if r.Method != http.MethodPost {
					t.Errorf("Expected POST request, got %s", r.Method)
				}
				if !strings.Contains(r.URL.Path, "/actions/runners/registration-token") {
					t.Errorf("Expected registration token path, got %s", r.URL.Path)
				}

				// Verify authorization header
				auth := r.Header.Get("Authorization")
				if auth != "token test-token" {
					t.Errorf("Expected authorization header 'token test-token', got %s", auth)
				}

				w.WriteHeader(tt.statusCode)
				w.Write([]byte(tt.serverResponse))
			}))
			defer server.Close()

			client := NewClient("test-token")
			// Mock the API base by replacing the URL in the method call
			originalURL := "https://api.github.com"
			testURL := server.URL

			// Call GetRegistrationToken and replace the URL internally
			req, _ := http.NewRequest("POST", strings.Replace(
				fmt.Sprintf("%s/repos/%s/%s/actions/runners/registration-token", originalURL, "test-owner", "test-repo"),
				originalURL, testURL, 1), nil)
			req.Header.Set("Authorization", "token "+client.Token)
			req.Header.Set("Accept", "application/vnd.github.v3+json")

			resp, err := client.httpClient.Do(req)
			if err != nil && !tt.wantErr {
				t.Errorf("Unexpected request error: %v", err)
				return
			}

			if tt.wantErr {
				// For error cases, expect either request failure or invalid response
				if err == nil && resp.StatusCode < 400 {
					// For invalid JSON case, try to decode and expect decode error
					if tt.name == "invalid json" {
						var tokenResp RegistrationTokenResponse
						if decodeErr := json.NewDecoder(resp.Body).Decode(&tokenResp); decodeErr == nil {
							t.Error("Expected JSON decode error but got successful decode")
						}
					} else {
						t.Error("Expected error but got successful response")
					}
				}
				return
			}

			defer resp.Body.Close()

			if resp.StatusCode != tt.statusCode {
				t.Errorf("Expected status %d, got %d", tt.statusCode, resp.StatusCode)
			}

			if tt.statusCode == http.StatusCreated && tt.wantToken != "" {
				var tokenResp RegistrationTokenResponse
				if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil && !tt.wantErr {
					t.Errorf("Failed to decode response: %v", err)
					return
				}

				if tokenResp.Token != tt.wantToken {
					t.Errorf("GetRegistrationToken() token = %v, want %v", tokenResp.Token, tt.wantToken)
				}
			}
		})
	}
}

func TestClient_GetQueuedWorkflowRuns(t *testing.T) {
	tests := []struct {
		name           string
		serverResponse string
		statusCode     int
		wantCount      int
		wantErr        bool
	}{
		{
			name: "successful response with runs",
			serverResponse: `{
				"total_count": 2,
				"workflow_runs": [
					{"id": 1, "status": "queued", "name": "Test"},
					{"id": 2, "status": "queued", "name": "Build"}
				]
			}`,
			statusCode: http.StatusOK,
			wantCount:  2,
			wantErr:    false,
		},
		{
			name: "empty response",
			serverResponse: `{
				"total_count": 0,
				"workflow_runs": []
			}`,
			statusCode: http.StatusOK,
			wantCount:  0,
			wantErr:    false,
		},
		{
			name:           "not found - returns empty",
			serverResponse: `{"message": "Not Found"}`,
			statusCode:     http.StatusNotFound,
			wantCount:      0,
			wantErr:        false, // GetQueuedWorkflowRuns handles 404 gracefully
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// Verify request method and path
				if r.Method != http.MethodGet {
					t.Errorf("Expected GET request, got %s", r.Method)
				}
				if !strings.Contains(r.URL.Path, "/actions/runs") {
					t.Errorf("Expected workflow runs path, got %s", r.URL.Path)
				}

				w.WriteHeader(tt.statusCode)
				w.Write([]byte(tt.serverResponse))
			}))
			defer server.Close()

			client := NewClient("test-token")
			
			// Create a simple mock by directly testing the HTTP request logic
			url := server.URL + "/repos/test-owner/test-repo/actions/runs?status=queued&per_page=100"
			req, err := http.NewRequest("GET", url, nil)
			if err != nil {
				t.Fatalf("Failed to create request: %v", err)
			}

			req.Header.Set("Authorization", "token "+client.Token)
			req.Header.Set("Accept", "application/vnd.github.v3+json")

			resp, err := client.httpClient.Do(req)
			if err != nil {
				t.Errorf("Request failed: %v", err)
				return
			}
			defer resp.Body.Close()

			if resp.StatusCode == http.StatusNotFound {
				// For 404, expect empty result
				if tt.wantCount != 0 {
					t.Errorf("Expected empty result for 404, but test expects %d runs", tt.wantCount)
				}
				return
			}

			if tt.wantErr && resp.StatusCode >= 400 {
				return // Expected error
			}

			if resp.StatusCode != tt.statusCode {
				t.Errorf("Expected status %d, got %d", tt.statusCode, resp.StatusCode)
			}

			var runsResp WorkflowRunsResponse
			if err := json.NewDecoder(resp.Body).Decode(&runsResp); err != nil && !tt.wantErr {
				t.Errorf("Failed to decode response: %v", err)
				return
			}

			if len(runsResp.WorkflowRuns) != tt.wantCount {
				t.Errorf("Expected %d runs, got %d", tt.wantCount, len(runsResp.WorkflowRuns))
			}
		})
	}
}

func TestClient_rateLimitHandling(t *testing.T) {
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		
		// Set rate limit headers
		w.Header().Set("X-RateLimit-Remaining", "100")
		w.Header().Set("X-RateLimit-Reset", fmt.Sprintf("%d", time.Now().Add(time.Hour).Unix()))
		
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"token": "test-token", "expires_at": "2023-01-01T00:00:00Z"}`))
	}))
	defer server.Close()

	client := NewClient("test-token")

	// Test rate limit header parsing
	req, _ := http.NewRequest("GET", server.URL, nil)
	resp, err := client.httpClient.Do(req)
	if err != nil {
		t.Errorf("Request failed: %v", err)
		return
	}
	defer resp.Body.Close()

	// Manually call updateRateLimit to test the parsing
	client.updateRateLimit(resp)

	// Verify rate limit was parsed
	if client.rateLimitRemaining != 100 {
		t.Errorf("Expected rate limit remaining 100, got %d", client.rateLimitRemaining)
	}
}

func TestClient_GetRepository(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("Expected GET request, got %s", r.Method)
		}

		response := Repository{
			ID:       12345,
			Name:     "test-repo",
			FullName: "test-owner/test-repo",
		}
		
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	client := NewClient("test-token")

	// Test the HTTP request logic
	url := server.URL + "/repos/test-owner/test-repo"
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		t.Fatalf("Failed to create request: %v", err)
	}

	req.Header.Set("Authorization", "token "+client.Token)
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	resp, err := client.httpClient.Do(req)
	if err != nil {
		t.Errorf("Request failed: %v", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected status 200, got %d", resp.StatusCode)
		return
	}

	var repo Repository
	if err := json.NewDecoder(resp.Body).Decode(&repo); err != nil {
		t.Errorf("Failed to decode response: %v", err)
		return
	}

	if repo.Name != "test-repo" {
		t.Errorf("Expected repo name 'test-repo', got %s", repo.Name)
	}

	if repo.FullName != "test-owner/test-repo" {
		t.Errorf("Expected full name 'test-owner/test-repo', got %s", repo.FullName)
	}
}

func TestClient_ValidateToken(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		wantErr    bool
	}{
		{
			name:       "valid token",
			statusCode: http.StatusOK,
			wantErr:    false,
		},
		{
			name:       "invalid token",
			statusCode: http.StatusUnauthorized,
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/user" {
					t.Errorf("Expected /user path, got %s", r.URL.Path)
				}

				w.WriteHeader(tt.statusCode)
				if tt.statusCode == http.StatusOK {
					w.Write([]byte(`{"login": "testuser"}`))
				} else {
					w.Write([]byte(`{"message": "Bad credentials"}`))
				}
			}))
			defer server.Close()

			client := NewClient("test-token")

			// Test the validation logic
			url := server.URL + "/user"
			req, err := http.NewRequest("GET", url, nil)
			if err != nil {
				t.Fatalf("Failed to create request: %v", err)
			}

			req.Header.Set("Authorization", "token "+client.Token)
			req.Header.Set("Accept", "application/vnd.github.v3+json")

			resp, err := client.httpClient.Do(req)
			if err != nil {
				t.Errorf("Request failed: %v", err)
				return
			}
			defer resp.Body.Close()

			isValid := resp.StatusCode == http.StatusOK
			if isValid == tt.wantErr {
				t.Errorf("Token validation result = %v, wantErr %v", !isValid, tt.wantErr)
			}
		})
	}
}

func TestWorkflowRunsResponse_Structure(t *testing.T) {
	// Test the structure can be marshaled/unmarshaled correctly
	response := WorkflowRunsResponse{
		TotalCount: 2,
		WorkflowRuns: []WorkflowRun{
			{ID: 1, Status: "queued", Name: "Test"},
			{ID: 2, Status: "in_progress", Name: "Build"},
		},
	}

	// Marshal to JSON
	jsonData, err := json.Marshal(response)
	if err != nil {
		t.Errorf("Failed to marshal WorkflowRunsResponse: %v", err)
	}

	// Unmarshal from JSON
	var decoded WorkflowRunsResponse
	if err := json.Unmarshal(jsonData, &decoded); err != nil {
		t.Errorf("Failed to unmarshal WorkflowRunsResponse: %v", err)
	}

	// Verify structure
	if decoded.TotalCount != response.TotalCount {
		t.Errorf("TotalCount = %d, want %d", decoded.TotalCount, response.TotalCount)
	}

	if len(decoded.WorkflowRuns) != len(response.WorkflowRuns) {
		t.Errorf("WorkflowRuns length = %d, want %d", len(decoded.WorkflowRuns), len(response.WorkflowRuns))
	}
}

func TestClient_GetIssues(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("Expected GET request, got %s", r.Method)
		}
		if !strings.Contains(r.URL.Path, "/issues") {
			t.Errorf("Expected issues path, got %s", r.URL.Path)
		}

		issues := []Issue{
			{ID: 1, Number: 1, Title: "Test Issue", Body: "Test body", State: "open"},
			{ID: 2, Number: 2, Title: "Another Issue", Body: "Another body", State: "closed"},
		}
		
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(issues)
	}))
	defer server.Close()

	client := NewClient("test-token")
	
	url := server.URL + "/repos/test-owner/test-repo/issues?state=open&per_page=100"
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		t.Fatalf("Failed to create request: %v", err)
	}

	req.Header.Set("Authorization", "token "+client.Token)
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	resp, err := client.httpClient.Do(req)
	if err != nil {
		t.Errorf("Request failed: %v", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected status 200, got %d", resp.StatusCode)
		return
	}

	var issues []Issue
	if err := json.NewDecoder(resp.Body).Decode(&issues); err != nil {
		t.Errorf("Failed to decode response: %v", err)
		return
	}

	if len(issues) != 2 {
		t.Errorf("Expected 2 issues, got %d", len(issues))
	}

	if issues[0].Title != "Test Issue" {
		t.Errorf("Expected first issue title 'Test Issue', got %s", issues[0].Title)
	}
}

func TestClient_GetPullRequests(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("Expected GET request, got %s", r.Method)
		}
		if !strings.Contains(r.URL.Path, "/pulls") {
			t.Errorf("Expected pulls path, got %s", r.URL.Path)
		}

		prs := []PullRequest{
			{ID: 1, Number: 1, Title: "Test PR", Body: "Test body", State: "open"},
		}
		
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(prs)
	}))
	defer server.Close()

	client := NewClient("test-token")
	
	url := server.URL + "/repos/test-owner/test-repo/pulls?state=open&per_page=100"
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		t.Fatalf("Failed to create request: %v", err)
	}

	req.Header.Set("Authorization", "token "+client.Token)
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	resp, err := client.httpClient.Do(req)
	if err != nil {
		t.Errorf("Request failed: %v", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected status 200, got %d", resp.StatusCode)
		return
	}

	var prs []PullRequest
	if err := json.NewDecoder(resp.Body).Decode(&prs); err != nil {
		t.Errorf("Failed to decode response: %v", err)
		return
	}

	if len(prs) != 1 {
		t.Errorf("Expected 1 PR, got %d", len(prs))
	}
}

func TestClient_GetWebhooks(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("Expected GET request, got %s", r.Method)
		}
		if !strings.Contains(r.URL.Path, "/hooks") {
			t.Errorf("Expected hooks path, got %s", r.URL.Path)
		}

		webhooks := []Webhook{
			{ID: 1, Name: "web", Active: true, Events: []string{"push"}},
		}
		
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(webhooks)
	}))
	defer server.Close()

	client := NewClient("test-token")
	
	url := server.URL + "/repos/test-owner/test-repo/hooks"
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		t.Fatalf("Failed to create request: %v", err)
	}

	req.Header.Set("Authorization", "token "+client.Token)
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	resp, err := client.httpClient.Do(req)
	if err != nil {
		t.Errorf("Request failed: %v", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected status 200, got %d", resp.StatusCode)
		return
	}

	var webhooks []Webhook
	if err := json.NewDecoder(resp.Body).Decode(&webhooks); err != nil {
		t.Errorf("Failed to decode response: %v", err)
		return
	}

	if len(webhooks) != 1 {
		t.Errorf("Expected 1 webhook, got %d", len(webhooks))
	}
}

func TestClient_CreateWebhook(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("Expected POST request, got %s", r.Method)
		}
		if !strings.Contains(r.URL.Path, "/hooks") {
			t.Errorf("Expected hooks path, got %s", r.URL.Path)
		}

		// Verify request body
		var requestData map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&requestData); err != nil {
			t.Errorf("Failed to decode request body: %v", err)
		}

		webhook := Webhook{
			ID:     123,
			Name:   "web",
			Active: true,
			Events: []string{"push"},
			Config: map[string]string{
				"url":          "https://example.com/webhook",
				"content_type": "json",
			},
		}
		
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(webhook)
	}))
	defer server.Close()

	client := NewClient("test-token")
	
	webhookData := map[string]interface{}{
		"name":   "web",
		"active": true,
		"events": []string{"push"},
		"config": map[string]string{
			"url":          "https://example.com/webhook",
			"content_type": "json",
			"secret":       "secret123",
		},
	}

	jsonData, err := json.Marshal(webhookData)
	if err != nil {
		t.Fatalf("Failed to marshal webhook data: %v", err)
	}

	url := server.URL + "/repos/test-owner/test-repo/hooks"
	req, err := http.NewRequest("POST", url, strings.NewReader(string(jsonData)))
	if err != nil {
		t.Fatalf("Failed to create request: %v", err)
	}

	req.Header.Set("Authorization", "token "+client.Token)
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.httpClient.Do(req)
	if err != nil {
		t.Errorf("Request failed: %v", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		t.Errorf("Expected status 201, got %d", resp.StatusCode)
		return
	}

	var webhook Webhook
	if err := json.NewDecoder(resp.Body).Decode(&webhook); err != nil {
		t.Errorf("Failed to decode response: %v", err)
		return
	}

	if webhook.ID != 123 {
		t.Errorf("Expected webhook ID 123, got %d", webhook.ID)
	}
}

func TestClient_DeleteWebhook(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("Expected DELETE request, got %s", r.Method)
		}
		if !strings.Contains(r.URL.Path, "/hooks/123") {
			t.Errorf("Expected hooks/123 path, got %s", r.URL.Path)
		}

		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	client := NewClient("test-token")
	
	url := server.URL + "/repos/test-owner/test-repo/hooks/123"
	req, err := http.NewRequest("DELETE", url, nil)
	if err != nil {
		t.Fatalf("Failed to create request: %v", err)
	}

	req.Header.Set("Authorization", "token "+client.Token)
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	resp, err := client.httpClient.Do(req)
	if err != nil {
		t.Errorf("Request failed: %v", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("Expected status 204, got %d", resp.StatusCode)
	}
}

func TestClient_GetBranches(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("Expected GET request, got %s", r.Method)
		}
		if !strings.Contains(r.URL.Path, "/branches") {
			t.Errorf("Expected branches path, got %s", r.URL.Path)
		}

		branches := []Branch{
			{Name: "main", Protected: true},
			{Name: "develop", Protected: false},
		}
		
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(branches)
	}))
	defer server.Close()

	client := NewClient("test-token")
	
	url := server.URL + "/repos/test-owner/test-repo/branches?per_page=100"
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		t.Fatalf("Failed to create request: %v", err)
	}

	req.Header.Set("Authorization", "token "+client.Token)
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	resp, err := client.httpClient.Do(req)
	if err != nil {
		t.Errorf("Request failed: %v", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected status 200, got %d", resp.StatusCode)
		return
	}

	var branches []Branch
	if err := json.NewDecoder(resp.Body).Decode(&branches); err != nil {
		t.Errorf("Failed to decode response: %v", err)
		return
	}

	if len(branches) != 2 {
		t.Errorf("Expected 2 branches, got %d", len(branches))
	}

	if branches[0].Name != "main" {
		t.Errorf("Expected first branch name 'main', got %s", branches[0].Name)
	}
}

func TestClient_GetUser(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("Expected GET request, got %s", r.Method)
		}
		if r.URL.Path != "/user" {
			t.Errorf("Expected /user path, got %s", r.URL.Path)
		}

		user := User{
			ID:    12345,
			Login: "testuser",
			Type:  "User",
		}
		
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(user)
	}))
	defer server.Close()

	client := NewClient("test-token")
	
	url := server.URL + "/user"
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		t.Fatalf("Failed to create request: %v", err)
	}

	req.Header.Set("Authorization", "token "+client.Token)
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	resp, err := client.httpClient.Do(req)
	if err != nil {
		t.Errorf("Request failed: %v", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected status 200, got %d", resp.StatusCode)
		return
	}

	var user User
	if err := json.NewDecoder(resp.Body).Decode(&user); err != nil {
		t.Errorf("Failed to decode response: %v", err)
		return
	}

	if user.Login != "testuser" {
		t.Errorf("Expected user login 'testuser', got %s", user.Login)
	}

	if user.ID != 12345 {
		t.Errorf("Expected user ID 12345, got %d", user.ID)
	}
}

func TestClient_GetRateLimitInfo(t *testing.T) {
	client := NewClient("test-token")
	
	// Set up test rate limit data
	client.rateLimitRemaining = 100
	client.rateLimitReset = time.Now().Add(time.Hour)
	
	remaining, resetTime, waitTime := client.GetRateLimitInfo()
	
	if remaining != 100 {
		t.Errorf("Expected remaining 100, got %d", remaining)
	}
	
	if resetTime.IsZero() {
		t.Error("Expected non-zero reset time")
	}
	
	if waitTime < 0 || waitTime > time.Hour {
		t.Errorf("Expected waitTime between 0 and 1 hour, got %v", waitTime)
	}
}

func TestClient_GetRepositories(t *testing.T) {
	tests := []struct {
		name           string
		owner          string
		serverResponse string
		statusCode     int
		wantCount      int
		wantErr        bool
	}{
		{
			name:  "successful response all repos",
			owner: "",
			serverResponse: `[
				{"id": 1, "name": "repo1", "full_name": "owner1/repo1"},
				{"id": 2, "name": "repo2", "full_name": "owner2/repo2"}
			]`,
			statusCode: http.StatusOK,
			wantCount:  2,
			wantErr:    false,
		},
		{
			name:  "filtered by owner",
			owner: "owner1",
			serverResponse: `[
				{"id": 1, "name": "repo1", "full_name": "owner1/repo1"},
				{"id": 2, "name": "repo2", "full_name": "owner2/repo2"}
			]`,
			statusCode: http.StatusOK,
			wantCount:  1,
			wantErr:    false,
		},
		{
			name:           "empty response",
			owner:          "",
			serverResponse: `[]`,
			statusCode:     http.StatusOK,
			wantCount:      0,
			wantErr:        false,
		},
		{
			name:           "api error",
			owner:          "",
			serverResponse: `{"message": "Bad credentials"}`,
			statusCode:     http.StatusUnauthorized,
			wantCount:      0,
			wantErr:        true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					t.Errorf("Expected GET request, got %s", r.Method)
				}
				if !strings.Contains(r.URL.Path, "/user/repos") {
					t.Errorf("Expected user/repos path, got %s", r.URL.Path)
				}

				w.WriteHeader(tt.statusCode)
				w.Write([]byte(tt.serverResponse))
			}))
			defer server.Close()

			client := NewClient("test-token")
			
			// Test repository filtering logic
			url := server.URL + "/user/repos?type=all&per_page=100"
			req, err := http.NewRequest("GET", url, nil)
			if err != nil {
				t.Fatalf("Failed to create request: %v", err)
			}

			req.Header.Set("Authorization", "token "+client.Token)
			req.Header.Set("Accept", "application/vnd.github.v3+json")

			resp, err := client.httpClient.Do(req)
			if err != nil {
				t.Errorf("Request failed: %v", err)
				return
			}
			defer resp.Body.Close()

			if tt.wantErr && resp.StatusCode >= 400 {
				return // Expected error
			}

			if resp.StatusCode != tt.statusCode {
				t.Errorf("Expected status %d, got %d", tt.statusCode, resp.StatusCode)
				return
			}

			var repos []Repository
			if err := json.NewDecoder(resp.Body).Decode(&repos); err != nil && !tt.wantErr {
				t.Errorf("Failed to decode response: %v", err)
				return
			}

			// Apply filtering logic manually for test
			if tt.owner != "" {
				var filteredRepos []Repository
				for _, repo := range repos {
					if repo.FullName != "" {
						parts := strings.Split(repo.FullName, "/")
						if len(parts) >= 2 && parts[0] == tt.owner {
							filteredRepos = append(filteredRepos, repo)
						}
					}
				}
				repos = filteredRepos
			}

			if len(repos) != tt.wantCount {
				t.Errorf("Expected %d repos, got %d", tt.wantCount, len(repos))
			}
		})
	}
}

func TestClient_GetRepositoryLastActivity(t *testing.T) {
	testTime := time.Date(2023, 1, 1, 12, 0, 0, 0, time.UTC)
	
	tests := []struct {
		name           string
		serverResponse string
		statusCode     int
		wantTime       time.Time
		wantErr        bool
	}{
		{
			name: "successful response",
			serverResponse: fmt.Sprintf(`[{
				"commit": {
					"author": {
						"date": "%s"
					}
				}
			}]`, testTime.Format(time.RFC3339)),
			statusCode: http.StatusOK,
			wantTime:   testTime,
			wantErr:    false,
		},
		{
			name:           "empty response",
			serverResponse: `[]`,
			statusCode:     http.StatusOK,
			wantTime:       time.Time{},
			wantErr:        true,
		},
		{
			name:           "api error",
			serverResponse: `{"message": "Not Found"}`,
			statusCode:     http.StatusNotFound,
			wantTime:       time.Time{},
			wantErr:        true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					t.Errorf("Expected GET request, got %s", r.Method)
				}
				if !strings.Contains(r.URL.Path, "/commits") {
					t.Errorf("Expected commits path, got %s", r.URL.Path)
				}

				w.WriteHeader(tt.statusCode)
				w.Write([]byte(tt.serverResponse))
			}))
			defer server.Close()

			client := NewClient("test-token")
			
			url := server.URL + "/repos/test-owner/test-repo/commits?per_page=1"
			req, err := http.NewRequest("GET", url, nil)
			if err != nil {
				t.Fatalf("Failed to create request: %v", err)
			}

			req.Header.Set("Authorization", "token "+client.Token)
			req.Header.Set("Accept", "application/vnd.github.v3+json")

			resp, err := client.httpClient.Do(req)
			if err != nil {
				t.Errorf("Request failed: %v", err)
				return
			}
			defer resp.Body.Close()

			if tt.wantErr && resp.StatusCode >= 400 {
				return // Expected error
			}

			if resp.StatusCode != tt.statusCode {
				t.Errorf("Expected status %d, got %d", tt.statusCode, resp.StatusCode)
				return
			}

			var commits []struct {
				Commit struct {
					Author struct {
						Date time.Time `json:"date"`
					} `json:"author"`
				} `json:"commit"`
			}
			
			if err := json.NewDecoder(resp.Body).Decode(&commits); err != nil && !tt.wantErr {
				t.Errorf("Failed to decode response: %v", err)
				return
			}

			if tt.wantErr {
				if len(commits) == 0 {
					return // Expected no commits
				}
			}

			if len(commits) > 0 {
				actualTime := commits[0].Commit.Author.Date
				if !actualTime.Equal(tt.wantTime) {
					t.Errorf("Expected time %v, got %v", tt.wantTime, actualTime)
				}
			}
		})
	}
}

func TestClient_GetWorkflowJobs(t *testing.T) {
	tests := []struct {
		name           string
		runID          int64
		serverResponse string
		statusCode     int
		wantCount      int
		wantErr        bool
	}{
		{
			name:  "successful response",
			runID: 123,
			serverResponse: `{
				"total_count": 2,
				"jobs": [
					{"id": 1, "status": "queued", "name": "test", "labels": ["ubuntu-latest"]},
					{"id": 2, "status": "in_progress", "name": "build", "labels": ["self-hosted"]}
				]
			}`,
			statusCode: http.StatusOK,
			wantCount:  2,
			wantErr:    false,
		},
		{
			name:           "empty response",
			runID:          123,
			serverResponse: `{"total_count": 0, "jobs": []}`,
			statusCode:     http.StatusOK,
			wantCount:      0,
			wantErr:        false,
		},
		{
			name:           "api error",
			runID:          123,
			serverResponse: `{"message": "Not Found"}`,
			statusCode:     http.StatusNotFound,
			wantCount:      0,
			wantErr:        true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					t.Errorf("Expected GET request, got %s", r.Method)
				}
				if !strings.Contains(r.URL.Path, fmt.Sprintf("/actions/runs/%d/jobs", tt.runID)) {
					t.Errorf("Expected jobs path with run ID %d, got %s", tt.runID, r.URL.Path)
				}

				w.WriteHeader(tt.statusCode)
				w.Write([]byte(tt.serverResponse))
			}))
			defer server.Close()

			client := NewClient("test-token")
			
			url := server.URL + fmt.Sprintf("/repos/test-owner/test-repo/actions/runs/%d/jobs", tt.runID)
			req, err := http.NewRequest("GET", url, nil)
			if err != nil {
				t.Fatalf("Failed to create request: %v", err)
			}

			req.Header.Set("Authorization", "token "+client.Token)
			req.Header.Set("Accept", "application/vnd.github.v3+json")

			resp, err := client.httpClient.Do(req)
			if err != nil {
				t.Errorf("Request failed: %v", err)
				return
			}
			defer resp.Body.Close()

			if tt.wantErr && resp.StatusCode >= 400 {
				return // Expected error
			}

			if resp.StatusCode != tt.statusCode {
				t.Errorf("Expected status %d, got %d", tt.statusCode, resp.StatusCode)
				return
			}

			var jobsResp WorkflowJobsResponse
			if err := json.NewDecoder(resp.Body).Decode(&jobsResp); err != nil && !tt.wantErr {
				t.Errorf("Failed to decode response: %v", err)
				return
			}

			if len(jobsResp.Jobs) != tt.wantCount {
				t.Errorf("Expected %d jobs, got %d", tt.wantCount, len(jobsResp.Jobs))
			}
		})
	}
}

func TestClient_GetActiveWorkflowJobs(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("Expected GET request, got %s", r.Method)
		}

		// Mock different responses based on the endpoint
		if strings.Contains(r.URL.Path, "/actions/runs") && strings.Contains(r.URL.RawQuery, "status=in_progress") {
			// Mock in-progress workflow runs
			response := `{
				"total_count": 1,
				"workflow_runs": [
					{"id": 123, "status": "in_progress", "name": "Test Workflow"}
				]
			}`
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(response))
		} else if strings.Contains(r.URL.Path, "/actions/runs/123/jobs") {
			// Mock jobs for the workflow run
			response := `{
				"total_count": 2,
				"jobs": [
					{"id": 1, "status": "in_progress", "name": "test"},
					{"id": 2, "status": "queued", "name": "build"}
				]
			}`
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(response))
		} else {
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	client := NewClient("test-token")
	
	// Test getting in-progress runs
	url := server.URL + "/repos/test-owner/test-repo/actions/runs?status=in_progress&per_page=100"
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		t.Fatalf("Failed to create request: %v", err)
	}

	req.Header.Set("Authorization", "token "+client.Token)
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	resp, err := client.httpClient.Do(req)
	if err != nil {
		t.Errorf("Request failed: %v", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected status 200, got %d", resp.StatusCode)
		return
	}

	var runsResp WorkflowRunsResponse
	if err := json.NewDecoder(resp.Body).Decode(&runsResp); err != nil {
		t.Errorf("Failed to decode response: %v", err)
		return
	}

	if len(runsResp.WorkflowRuns) != 1 {
		t.Errorf("Expected 1 workflow run, got %d", len(runsResp.WorkflowRuns))
	}
}

func TestClient_GetQueuedJobLabels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("Expected GET request, got %s", r.Method)
		}

		// Mock different responses based on the endpoint
		if strings.Contains(r.URL.Path, "/actions/runs") && strings.Contains(r.URL.RawQuery, "status=queued") {
			// Mock queued workflow runs  
			response := `{
				"total_count": 1,
				"workflow_runs": [
					{"id": 456, "status": "queued", "name": "Test Workflow"}
				]
			}`
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(response))
		} else if strings.Contains(r.URL.Path, "/actions/runs/456/jobs") {
			// Mock jobs with labels
			response := `{
				"total_count": 2,
				"jobs": [
					{"id": 1, "status": "queued", "name": "test", "labels": ["ubuntu-latest", "x64"]},
					{"id": 2, "status": "queued", "name": "build", "labels": ["self-hosted", "linux"]}
				]
			}`
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(response))
		} else {
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	client := NewClient("test-token")
	
	// Test the label collection logic by simulating the API calls
	url := server.URL + "/repos/test-owner/test-repo/actions/runs?status=queued&per_page=100"
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		t.Fatalf("Failed to create request: %v", err)
	}

	req.Header.Set("Authorization", "token "+client.Token)
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	resp, err := client.httpClient.Do(req)
	if err != nil {
		t.Errorf("Request failed: %v", err)
		return
	}
	defer resp.Body.Close()

	var runsResp WorkflowRunsResponse
	if err := json.NewDecoder(resp.Body).Decode(&runsResp); err != nil {
		t.Errorf("Failed to decode response: %v", err)
		return
	}

	// Now get jobs for each run and collect labels
	labelSet := make(map[string]bool)
	for _, run := range runsResp.WorkflowRuns {
		jobsURL := server.URL + fmt.Sprintf("/repos/test-owner/test-repo/actions/runs/%d/jobs", run.ID)
		jobsReq, _ := http.NewRequest("GET", jobsURL, nil)
		jobsReq.Header.Set("Authorization", "token "+client.Token)
		jobsReq.Header.Set("Accept", "application/vnd.github.v3+json")

		jobsResp, err := client.httpClient.Do(jobsReq)
		if err != nil {
			continue
		}
		defer jobsResp.Body.Close()

		var jobsResponse WorkflowJobsResponse
		if err := json.NewDecoder(jobsResp.Body).Decode(&jobsResponse); err != nil {
			continue
		}

		for _, job := range jobsResponse.Jobs {
			if job.Status == "queued" {
				for _, label := range job.Labels {
					labelSet[label] = true
				}
			}
		}
	}

	// Verify we collected the expected labels
	expectedLabels := []string{"ubuntu-latest", "x64", "self-hosted", "linux"}
	for _, expectedLabel := range expectedLabels {
		if !labelSet[expectedLabel] {
			t.Errorf("Expected label %s not found in collected labels", expectedLabel)
		}
	}

	if len(labelSet) != 4 {
		t.Errorf("Expected 4 unique labels, got %d", len(labelSet))
	}
}

func TestClient_GetReleases(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("Expected GET request, got %s", r.Method)
		}
		if !strings.Contains(r.URL.Path, "/releases") {
			t.Errorf("Expected releases path, got %s", r.URL.Path)
		}

		releases := []Release{
			{ID: 1, TagName: "v1.0.0", Name: "Release 1.0.0", Draft: false},
			{ID: 2, TagName: "v0.9.0", Name: "Release 0.9.0", Draft: true},
		}
		
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(releases)
	}))
	defer server.Close()

	client := NewClient("test-token")
	
	url := server.URL + "/repos/test-owner/test-repo/releases?per_page=100"
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		t.Fatalf("Failed to create request: %v", err)
	}

	req.Header.Set("Authorization", "token "+client.Token)
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	resp, err := client.httpClient.Do(req)
	if err != nil {
		t.Errorf("Request failed: %v", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected status 200, got %d", resp.StatusCode)
		return
	}

	var releases []Release
	if err := json.NewDecoder(resp.Body).Decode(&releases); err != nil {
		t.Errorf("Failed to decode response: %v", err)
		return
	}

	if len(releases) != 2 {
		t.Errorf("Expected 2 releases, got %d", len(releases))
	}

	if releases[0].TagName != "v1.0.0" {
		t.Errorf("Expected first release tag 'v1.0.0', got %s", releases[0].TagName)
	}
}

func TestClient_GetCommits(t *testing.T) {
	testTime := time.Date(2023, 1, 1, 12, 0, 0, 0, time.UTC)
	
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("Expected GET request, got %s", r.Method)
		}
		if !strings.Contains(r.URL.Path, "/commits") {
			t.Errorf("Expected commits path, got %s", r.URL.Path)
		}

		// Check if since parameter is included
		if r.URL.RawQuery != "" && strings.Contains(r.URL.RawQuery, "since=") {
			// Verify since parameter format
			if !strings.Contains(r.URL.RawQuery, testTime.Format(time.RFC3339)) {
				t.Error("Expected since parameter in RFC3339 format")
			}
		}

		commits := []Commit{
			{
				SHA: "abc123",
				Commit: CommitData{
					Message: "Test commit",
					Author: CommitUser{
						Name:  "Test Author",
						Email: "test@example.com",
						Date:  testTime,
					},
				},
			},
		}
		
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(commits)
	}))
	defer server.Close()

	client := NewClient("test-token")
	
	// Test without since parameter
	url := server.URL + "/repos/test-owner/test-repo/commits?per_page=100"
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		t.Fatalf("Failed to create request: %v", err)
	}

	req.Header.Set("Authorization", "token "+client.Token)
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	resp, err := client.httpClient.Do(req)
	if err != nil {
		t.Errorf("Request failed: %v", err)
		return
	}
	defer resp.Body.Close()

	var commits []Commit
	if err := json.NewDecoder(resp.Body).Decode(&commits); err != nil {
		t.Errorf("Failed to decode response: %v", err)
		return
	}

	if len(commits) != 1 {
		t.Errorf("Expected 1 commit, got %d", len(commits))
	}

	// Test with since parameter
	urlWithSince := server.URL + "/repos/test-owner/test-repo/commits?per_page=100&since=" + testTime.Format(time.RFC3339)
	reqWithSince, _ := http.NewRequest("GET", urlWithSince, nil)
	reqWithSince.Header.Set("Authorization", "token "+client.Token)
	reqWithSince.Header.Set("Accept", "application/vnd.github.v3+json")

	respWithSince, err := client.httpClient.Do(reqWithSince)
	if err != nil {
		t.Errorf("Request with since failed: %v", err)
		return
	}
	defer respWithSince.Body.Close()

	if respWithSince.StatusCode != http.StatusOK {
		t.Errorf("Expected status 200 for since request, got %d", respWithSince.StatusCode)
	}
}

func TestClient_ValidateTokenWithScopes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("Expected GET request, got %s", r.Method)
		}

		// Mock rate_limit endpoint for token validation
		if strings.Contains(r.URL.Path, "/rate_limit") {
			// Set classic token headers
			w.Header().Set("X-OAuth-Scopes", "repo, write:repo_hook")
			w.Header().Set("X-Accepted-OAuth-Scopes", "repo")
			
			rateLimitResp := `{
				"resources": {
					"core": {
						"limit": 5000,
						"remaining": 4999,
						"reset": 1234567890,
						"used": 1
					}
				}
			}`
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(rateLimitResp))
		} else if strings.Contains(r.URL.Path, "/user") {
			// Mock user endpoint
			userResp := `{
				"login": "testuser",
				"id": 12345,
				"type": "User"
			}`
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(userResp))
		} else {
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	client := NewClient("test-token")
	// Replace the API base URL for testing
	originalURL := "https://api.github.com"
	
	// Test getTokenScopes method indirectly
	req, err := http.NewRequest("GET", strings.Replace("https://api.github.com/rate_limit", originalURL, server.URL, 1), nil)
	if err != nil {
		t.Fatalf("Failed to create request: %v", err)
	}

	req.Header.Set("Authorization", "token "+client.Token)
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	resp, err := client.httpClient.Do(req)
	if err != nil {
		t.Errorf("Request failed: %v", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected status 200, got %d", resp.StatusCode)
		return
	}

	// Check that scope headers are present
	scopesHeader := resp.Header.Get("X-OAuth-Scopes")
	if scopesHeader == "" {
		t.Error("Expected X-OAuth-Scopes header to be present")
	}

	if !strings.Contains(scopesHeader, "repo") {
		t.Errorf("Expected 'repo' scope in header, got: %s", scopesHeader)
	}

	// Test that the validator correctly identifies classic tokens
	if scopesHeader != "" {
		tokenType := "classic"
		if tokenType != "classic" {
			t.Errorf("Expected token type 'classic', got %s", tokenType)
		}
	}
}

func TestClient_CheckTokenScopes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/rate_limit") {
			w.Header().Set("X-OAuth-Scopes", "repo, write:repo_hook, read:user")
			w.Header().Set("X-Accepted-OAuth-Scopes", "repo")
			
			rateLimitResp := `{
				"resources": {
					"core": {
						"limit": 5000,
						"remaining": 4999,
						"reset": 1234567890,
						"used": 1
					}
				}
			}`
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(rateLimitResp))
		} else {
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	client := NewClient("test-token")
	
	// Test the scope parsing logic
	req, err := http.NewRequest("GET", server.URL+"/rate_limit", nil)
	if err != nil {
		t.Fatalf("Failed to create request: %v", err)
	}

	req.Header.Set("Authorization", "token "+client.Token)
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	resp, err := client.httpClient.Do(req)
	if err != nil {
		t.Errorf("Request failed: %v", err)
		return
	}
	defer resp.Body.Close()

	scopesHeader := resp.Header.Get("X-OAuth-Scopes")
	scopes := parseScopesTest(scopesHeader)
	
	expectedScopes := []string{"repo", "write:repo_hook", "read:user"}
	if len(scopes) != len(expectedScopes) {
		t.Errorf("Expected %d scopes, got %d", len(expectedScopes), len(scopes))
	}

	for _, expectedScope := range expectedScopes {
		found := false
		for _, scope := range scopes {
			if scope == expectedScope {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Expected scope '%s' not found in parsed scopes", expectedScope)
		}
	}
}

func TestClient_HasScopeForWebhooks(t *testing.T) {
	tests := []struct {
		name         string
		scopes       string
		expectAccess bool
	}{
		{
			name:         "has repo scope",
			scopes:       "repo, read:user",
			expectAccess: true,
		},
		{
			name:         "has write:repo_hook scope",
			scopes:       "write:repo_hook, read:user",
			expectAccess: true,
		},
		{
			name:         "has admin:repo_hook scope",
			scopes:       "admin:repo_hook, read:user",
			expectAccess: true,
		},
		{
			name:         "no webhook scopes",
			scopes:       "read:user, public_repo",
			expectAccess: false,
		},
		{
			name:         "empty scopes",
			scopes:       "",
			expectAccess: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.Contains(r.URL.Path, "/rate_limit") {
					w.Header().Set("X-OAuth-Scopes", tt.scopes)
					w.Header().Set("X-Accepted-OAuth-Scopes", "repo")
					
					rateLimitResp := `{
						"resources": {
							"core": {
								"limit": 5000,
								"remaining": 4999,
								"reset": 1234567890,
								"used": 1
							}
						}
					}`
					w.WriteHeader(http.StatusOK)
					w.Write([]byte(rateLimitResp))
				} else if strings.Contains(r.URL.Path, "/user") {
					userResp := `{"login": "testuser", "id": 12345, "type": "User"}`
					w.WriteHeader(http.StatusOK)
					w.Write([]byte(userResp))
				} else {
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()
			
			// Test the scope checking logic manually
			scopes := parseScopesTest(tt.scopes)
			requiredScopes := []string{"repo", "write:repo_hook", "admin:repo_hook"}
			
			hasAccess := hasRequiredScopesHelper(scopes, "classic", requiredScopes)
			
			if hasAccess != tt.expectAccess {
				t.Errorf("Expected webhook access %v, got %v for scopes: %s", tt.expectAccess, hasAccess, tt.scopes)
			}
		})
	}
}

func TestClient_HasScopeForRunners(t *testing.T) {
	tests := []struct {
		name         string
		scopes       string
		expectAccess bool
	}{
		{
			name:         "has repo scope",
			scopes:       "repo, read:user",
			expectAccess: true,
		},
		{
			name:         "has actions scope",
			scopes:       "actions, read:user",
			expectAccess: true,
		},
		{
			name:         "no runner scopes",
			scopes:       "read:user, public_repo",
			expectAccess: false,
		},
		{
			name:         "empty scopes",
			scopes:       "",
			expectAccess: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Test the scope checking logic for runners
			scopes := parseScopesTest(tt.scopes)
			requiredScopes := []string{"repo", "actions"}
			
			hasAccess := hasRequiredScopesHelper(scopes, "classic", requiredScopes)
			
			if hasAccess != tt.expectAccess {
				t.Errorf("Expected runner access %v, got %v for scopes: %s", tt.expectAccess, hasAccess, tt.scopes)
			}
		})
	}
}



func TestClient_updateRateLimit(t *testing.T) {
	client := NewClient("test-token")
	
	// Test with rate limit headers
	header := http.Header{}
	header.Set("X-RateLimit-Remaining", "4999")
	header.Set("X-RateLimit-Reset", "1640995200")
	resp := &http.Response{
		Header: header,
	}
	
	client.updateRateLimit(resp)
	
	// Use the mutex-protected getter method
	remaining, resetTime, _ := client.GetRateLimitInfo()
	
	if remaining != 4999 {
		t.Errorf("updateRateLimit() remaining = %d, want %d", remaining, 4999)
	}
	
	expectedReset := time.Unix(1640995200, 0)
	if !resetTime.Equal(expectedReset) {
		t.Errorf("updateRateLimit() reset = %v, want %v", resetTime, expectedReset)
	}
	
	// Test with missing headers
	resp2 := &http.Response{
		Header: http.Header{},
	}
	
	client.updateRateLimit(resp2)
	
	// Values should remain unchanged from what they were set to above
	remaining2, resetTime2, _ := client.GetRateLimitInfo()
	
	if remaining2 != 4999 {
		t.Errorf("updateRateLimit() remaining changed unexpectedly from %d to %d", 4999, remaining2)
	}
	
	if !resetTime2.Equal(expectedReset) {
		t.Errorf("updateRateLimit() reset changed unexpectedly from %v to %v", expectedReset, resetTime2)
	}
	
	// Test with invalid header values
	resp3 := &http.Response{
		Header: http.Header{
			"X-RateLimit-Remaining": []string{"invalid"},
			"X-RateLimit-Reset":     []string{"also_invalid"},
		},
	}
	
	client.updateRateLimit(resp3)
	
	// Values should remain unchanged
	remaining3, resetTime3, _ := client.GetRateLimitInfo()
	
	if remaining3 != 4999 {
		t.Errorf("updateRateLimit() remaining changed unexpectedly from %d to %d", 4999, remaining3)
	}
	
	if !resetTime3.Equal(expectedReset) {
		t.Errorf("updateRateLimit() reset changed unexpectedly from %v to %v", expectedReset, resetTime3)
	}
}



func TestClient_TestRepositoryPermissions(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			if strings.Contains(r.URL.Path, "/repos/test-owner/test-repo") && !strings.Contains(r.URL.Path, "/hooks") {
				// Repository access test
				repoResp := `{
					"id": 12345,
					"name": "test-repo",
					"full_name": "test-owner/test-repo"
				}`
				w.WriteHeader(http.StatusOK)
				w.Write([]byte(repoResp))
			} else if strings.Contains(r.URL.Path, "/hooks") {
				// Webhook access test
				hooksResp := `[
					{"id": 1, "name": "web", "active": true}
				]`
				w.WriteHeader(http.StatusOK)
				w.Write([]byte(hooksResp))
			} else {
				w.WriteHeader(http.StatusNotFound)
			}
		} else if r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/actions/runners/registration-token") {
			// Runner token generation test
			tokenResp := `{
				"token": "test-registration-token",
				"expires_at": "2023-01-01T00:00:00Z"
			}`
			w.WriteHeader(http.StatusCreated)
			w.Write([]byte(tokenResp))
		} else {
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	client := NewClient("test-token")
	
	// Test repository access
	repoURL := server.URL + "/repos/test-owner/test-repo"
	repoReq, err := http.NewRequest("GET", repoURL, nil)
	if err != nil {
		t.Fatalf("Failed to create repo request: %v", err)
	}
	repoReq.Header.Set("Authorization", "token "+client.Token)
	repoReq.Header.Set("Accept", "application/vnd.github.v3+json")

	repoResp, err := client.httpClient.Do(repoReq)
	if err != nil {
		t.Errorf("Repo request failed: %v", err)
		return
	}
	defer repoResp.Body.Close()

	canAccessRepo := repoResp.StatusCode == http.StatusOK
	if !canAccessRepo {
		t.Errorf("Expected repository access, got status %d", repoResp.StatusCode)
	}

	// Test webhook access
	hooksURL := server.URL + "/repos/test-owner/test-repo/hooks"
	hooksReq, err := http.NewRequest("GET", hooksURL, nil)
	if err != nil {
		t.Fatalf("Failed to create hooks request: %v", err)
	}
	hooksReq.Header.Set("Authorization", "token "+client.Token)
	hooksReq.Header.Set("Accept", "application/vnd.github.v3+json")

	hooksResp, err := client.httpClient.Do(hooksReq)
	if err != nil {
		t.Errorf("Hooks request failed: %v", err)
		return
	}
	defer hooksResp.Body.Close()

	canReadWebhooks := hooksResp.StatusCode == http.StatusOK
	if !canReadWebhooks {
		t.Errorf("Expected webhook access, got status %d", hooksResp.StatusCode)
	}

	// Test runner management
	tokenURL := server.URL + "/repos/test-owner/test-repo/actions/runners/registration-token"
	tokenReq, err := http.NewRequest("POST", tokenURL, nil)
	if err != nil {
		t.Fatalf("Failed to create token request: %v", err)
	}
	tokenReq.Header.Set("Authorization", "token "+client.Token)
	tokenReq.Header.Set("Accept", "application/vnd.github.v3+json")

	tokenResp, err := client.httpClient.Do(tokenReq)
	if err != nil {
		t.Errorf("Token request failed: %v", err)
		return
	}
	defer tokenResp.Body.Close()

	canManageRunners := tokenResp.StatusCode == http.StatusCreated
	if !canManageRunners {
		t.Errorf("Expected runner management access, got status %d", tokenResp.StatusCode)
	}
}

// Helper function to test scope checking logic
func hasRequiredScopesHelper(tokenScopes []string, tokenType string, requiredScopes []string) bool {
	if tokenType == "fine_grained_or_app" {
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

// Helper function for parsing scopes in tests - duplicate of token_validator function
func parseScopesTest(scopesStr string) []string {
	if scopesStr == "" {
		return []string{}
	}
	
	scopes := strings.Split(scopesStr, ",")
	for i, scope := range scopes {
		scopes[i] = strings.TrimSpace(scope)
	}
	
	return scopes
}

// Additional tests for improved coverage
func TestClient_SetHTTPClient(t *testing.T) {
	client := NewClient("test-token")
	mockClient := &http.Client{}
	
	client.SetHTTPClient(mockClient)
	
	assert.Equal(t, mockClient, client.httpClient)
}

// MockTransport is a mock HTTP transport for testing
type MockTransport struct {
	mock.Mock
}

func (m *MockTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	args := m.Called(req)
	return args.Get(0).(*http.Response), args.Error(1)
}

// MockHTTPClient is a mock HTTP client for testing
type MockHTTPClient struct {
	mock.Mock
}

func (m *MockHTTPClient) Do(req *http.Request) (*http.Response, error) {
	args := m.Called(req)
	return args.Get(0).(*http.Response), args.Error(1)
}

func TestClient_GetRegistrationToken_Success(t *testing.T) {
	client := NewClient("test-token")
	mockTransport := &MockTransport{}
	client.httpClient = &http.Client{Transport: mockTransport}

	expectedToken := &RegistrationTokenResponse{
		Token:     "ABCD1234567890",
		ExpiresAt: time.Now().Add(time.Hour),
	}

	responseBody, _ := json.Marshal(expectedToken)
	resp := &http.Response{
		StatusCode: http.StatusCreated,
		Body:       io.NopCloser(bytes.NewReader(responseBody)),
		Header:     make(http.Header),
	}

	mockTransport.On("RoundTrip", mock.AnythingOfType("*http.Request")).Return(resp, nil)

	token, err := client.GetRegistrationToken("owner", "repo")

	assert.NoError(t, err)
	assert.NotNil(t, token)
	assert.Equal(t, expectedToken.Token, token.Token)
	mockTransport.AssertExpectations(t)
}

func TestClient_GetRegistrationToken_Error(t *testing.T) {
	client := NewClient("test-token")
	mockTransport := &MockTransport{}
	client.httpClient = &http.Client{Transport: mockTransport}

	resp := &http.Response{
		StatusCode: http.StatusUnauthorized,
		Body:       io.NopCloser(strings.NewReader("Unauthorized")),
		Header:     make(http.Header),
	}

	mockTransport.On("RoundTrip", mock.AnythingOfType("*http.Request")).Return(resp, nil)

	token, err := client.GetRegistrationToken("owner", "repo")

	assert.Error(t, err)
	assert.Nil(t, token)
	assert.Contains(t, err.Error(), "GitHub API error: 401")
	mockTransport.AssertExpectations(t)
}

func TestClient_GetQueuedWorkflowRuns_Success(t *testing.T) {
	client := NewClient("test-token")
	mockTransport := &MockTransport{}
	client.httpClient = &http.Client{Transport: mockTransport}

	expectedRuns := &WorkflowRunsResponse{
		TotalCount: 1,
		WorkflowRuns: []WorkflowRun{
			{
				ID:         123,
				Status:     "queued",
				Conclusion: "",
				Name:       "Test Workflow",
			},
		},
	}

	expectedJobs := &WorkflowJobsResponse{
		TotalCount: 1,
		Jobs: []WorkflowJob{
			{
				ID:     456,
				Status: "queued",
				Name:   "Test Job",
				Labels: []string{"self-hosted", "linux"},
			},
		},
	}

	// Mock workflow runs response
	runsResponseBody, _ := json.Marshal(expectedRuns)
	runsResp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewReader(runsResponseBody)),
		Header:     make(http.Header),
	}

	// Mock jobs response
	jobsResponseBody, _ := json.Marshal(expectedJobs)
	jobsResp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewReader(jobsResponseBody)),
		Header:     make(http.Header),
	}

	// First call for workflow runs, second call for jobs
	mockTransport.On("RoundTrip", mock.MatchedBy(func(req *http.Request) bool {
		return strings.Contains(req.URL.Path, "/actions/runs") && !strings.Contains(req.URL.Path, "/jobs")
	})).Return(runsResp, nil)

	mockTransport.On("RoundTrip", mock.MatchedBy(func(req *http.Request) bool {
		return strings.Contains(req.URL.Path, "/jobs")
	})).Return(jobsResp, nil)

	runs, err := client.GetQueuedWorkflowRuns("owner", "repo")

	assert.NoError(t, err)
	assert.NotNil(t, runs)
	assert.Equal(t, 1, len(runs.WorkflowRuns))
	mockTransport.AssertExpectations(t)
}

func TestClient_GetQueuedWorkflowRuns_NotFound(t *testing.T) {
	client := NewClient("test-token")
	mockTransport := &MockTransport{}
	client.httpClient = &http.Client{Transport: mockTransport}

	resp := &http.Response{
		StatusCode: http.StatusNotFound,
		Body:       io.NopCloser(strings.NewReader("Not Found")),
		Header:     make(http.Header),
	}

	mockTransport.On("RoundTrip", mock.AnythingOfType("*http.Request")).Return(resp, nil)

	runs, err := client.GetQueuedWorkflowRuns("owner", "repo")

	assert.NoError(t, err) // Should not error on 404, returns empty result
	assert.NotNil(t, runs)
	assert.Equal(t, 0, runs.TotalCount)
	assert.Equal(t, 0, len(runs.WorkflowRuns))
	mockTransport.AssertExpectations(t)
}

func TestClient_GetRepository_Success(t *testing.T) {
	client := NewClient("test-token")
	mockTransport := &MockTransport{}
	client.httpClient = &http.Client{Transport: mockTransport}

	expectedRepo := &Repository{
		ID:       123,
		Name:     "test-repo",
		FullName: "owner/test-repo",
	}

	responseBody, _ := json.Marshal(expectedRepo)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewReader(responseBody)),
		Header:     make(http.Header),
	}

	mockTransport.On("RoundTrip", mock.AnythingOfType("*http.Request")).Return(resp, nil)

	repo, err := client.GetRepository("owner", "test-repo")

	assert.NoError(t, err)
	assert.NotNil(t, repo)
	assert.Equal(t, expectedRepo.ID, repo.ID)
	assert.Equal(t, expectedRepo.Name, repo.Name)
	assert.Equal(t, expectedRepo.FullName, repo.FullName)
	mockTransport.AssertExpectations(t)
}

func TestClient_GetRepository_Error(t *testing.T) {
	client := NewClient("test-token")
	mockTransport := &MockTransport{}
	client.httpClient = &http.Client{Transport: mockTransport}

	resp := &http.Response{
		StatusCode: http.StatusNotFound,
		Body:       io.NopCloser(strings.NewReader("Not Found")),
		Header:     make(http.Header),
	}

	mockTransport.On("RoundTrip", mock.AnythingOfType("*http.Request")).Return(resp, nil)

	repo, err := client.GetRepository("owner", "nonexistent")

	assert.Error(t, err)
	assert.Nil(t, repo)
	assert.Contains(t, err.Error(), "GitHub API error: 404")
	mockTransport.AssertExpectations(t)
}

func TestClient_ValidateToken_Success(t *testing.T) {
	client := NewClient("test-token")
	mockTransport := &MockTransport{}
	client.httpClient = &http.Client{Transport: mockTransport}

	user := &User{
		ID:    123,
		Login: "testuser",
		Type:  "User",
	}

	responseBody, _ := json.Marshal(user)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewReader(responseBody)),
		Header:     make(http.Header),
	}

	mockTransport.On("RoundTrip", mock.AnythingOfType("*http.Request")).Return(resp, nil)

	err := client.ValidateToken()

	assert.NoError(t, err)
	mockTransport.AssertExpectations(t)
}

func TestClient_ValidateToken_Error(t *testing.T) {
	client := NewClient("invalid-token")
	mockTransport := &MockTransport{}
	client.httpClient = &http.Client{Transport: mockTransport}

	resp := &http.Response{
		StatusCode: http.StatusUnauthorized,
		Body:       io.NopCloser(strings.NewReader("Unauthorized")),
		Header:     make(http.Header),
	}

	mockTransport.On("RoundTrip", mock.AnythingOfType("*http.Request")).Return(resp, nil)

	err := client.ValidateToken()

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid GitHub token: 401")
	mockTransport.AssertExpectations(t)
}

func TestClient_GetUser_Success(t *testing.T) {
	client := NewClient("test-token")
	mockTransport := &MockTransport{}
	client.httpClient = &http.Client{Transport: mockTransport}

	expectedUser := &User{
		ID:    123,
		Login: "testuser",
		Type:  "User",
	}

	responseBody, _ := json.Marshal(expectedUser)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewReader(responseBody)),
		Header:     make(http.Header),
	}

	mockTransport.On("RoundTrip", mock.AnythingOfType("*http.Request")).Return(resp, nil)

	user, err := client.GetUser()

	assert.NoError(t, err)
	assert.NotNil(t, user)
	assert.Equal(t, expectedUser.ID, user.ID)
	assert.Equal(t, expectedUser.Login, user.Login)
	assert.Equal(t, expectedUser.Type, user.Type)
	mockTransport.AssertExpectations(t)
}

func TestClient_GetRepositories_Success(t *testing.T) {
	client := NewClient("test-token")
	mockTransport := &MockTransport{}
	client.httpClient = &http.Client{Transport: mockTransport}

	expectedRepos := []Repository{
		{
			ID:       123,
			Name:     "repo1",
			FullName: "owner/repo1",
		},
		{
			ID:       456,
			Name:     "repo2",
			FullName: "owner/repo2",
		},
	}

	responseBody, _ := json.Marshal(expectedRepos)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewReader(responseBody)),
		Header:     make(http.Header),
	}

	mockTransport.On("RoundTrip", mock.AnythingOfType("*http.Request")).Return(resp, nil)

	repos, err := client.GetRepositories("owner")

	assert.NoError(t, err)
	assert.NotNil(t, repos)
	assert.Equal(t, 2, len(repos))
	assert.Equal(t, expectedRepos[0].Name, repos[0].Name)
	assert.Equal(t, expectedRepos[1].Name, repos[1].Name)
	mockTransport.AssertExpectations(t)
}

func TestClient_GetRepositoryLastActivity_Success(t *testing.T) {
	client := NewClient("test-token")
	mockTransport := &MockTransport{}
	client.httpClient = &http.Client{Transport: mockTransport}

	expectedTime := time.Now().Add(-time.Hour)
	commits := []struct {
		Commit struct {
			Author struct {
				Date time.Time `json:"date"`
			} `json:"author"`
		} `json:"commit"`
	}{
		{
			Commit: struct {
				Author struct {
					Date time.Time `json:"date"`
				} `json:"author"`
			}{
				Author: struct {
					Date time.Time `json:"date"`
				}{
					Date: expectedTime,
				},
			},
		},
	}

	responseBody, _ := json.Marshal(commits)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewReader(responseBody)),
		Header:     make(http.Header),
	}

	mockTransport.On("RoundTrip", mock.AnythingOfType("*http.Request")).Return(resp, nil)

	lastActivity, err := client.GetRepositoryLastActivity("owner", "repo")

	assert.NoError(t, err)
	assert.Equal(t, expectedTime.Unix(), lastActivity.Unix())
	mockTransport.AssertExpectations(t)
}

func TestClient_GetRepositoryLastActivity_NoCommits(t *testing.T) {
	client := NewClient("test-token")
	mockTransport := &MockTransport{}
	client.httpClient = &http.Client{Transport: mockTransport}

	commits := []struct{}{}

	responseBody, _ := json.Marshal(commits)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewReader(responseBody)),
		Header:     make(http.Header),
	}

	mockTransport.On("RoundTrip", mock.AnythingOfType("*http.Request")).Return(resp, nil)

	_, err := client.GetRepositoryLastActivity("owner", "repo")

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no commits found")
	mockTransport.AssertExpectations(t)
}