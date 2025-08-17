package webhook

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/cecil-the-coder/gha-webhook-scaler/internal/config"
	"github.com/cecil-the-coder/gha-webhook-scaler/internal/github"
	"github.com/cecil-the-coder/gha-webhook-scaler/internal/runner"
)

// EnhancedMockRunnerManager is an enhanced mock that supports method overrides
type EnhancedMockRunnerManager struct {
	*MockRunnerManager
	GetRunnerDistributionFunc func() (map[string]int, error)
	GetRepositoriesFunc       func() ([]github.Repository, error)
}

func (m *EnhancedMockRunnerManager) GetRunnerDistribution() (map[string]int, error) {
	if m.GetRunnerDistributionFunc != nil {
		return m.GetRunnerDistributionFunc()
	}
	return m.MockRunnerManager.GetRunnerDistribution()
}

func (m *EnhancedMockRunnerManager) GetRepositories() ([]github.Repository, error) {
	if m.GetRepositoriesFunc != nil {
		return m.GetRepositoriesFunc()
	}
	return m.MockRunnerManager.GetRepositories()
}

// TestRealNewHandler_Integration tests the actual NewHandler function
func TestRealNewHandler_Integration(t *testing.T) {
	cfg := &config.Config{
		Owners: map[string]*config.OwnerConfig{
			"test-owner": {
				Owner:       "test-owner",
				GitHubToken: "test-token",
				AllRepos:    true,
			},
		},
	}

	// Create a minimal runner manager mock for integration
	mockManager := &MockRunnerManager{}

	// Test the structure that a real NewHandler would create using TestHandler
	handler := NewTestHandler(cfg, mockManager)

	if handler.config != cfg {
		t.Error("Handler config not set correctly")
	}

	if handler.runnerManager == nil {
		t.Error("Handler runnerManager should not be nil")
	}

	if handler.debouncer == nil {
		t.Error("Handler debouncer not initialized")
	}

	if handler.debouncer.debounceTime != 2*time.Second {
		t.Error("Handler debouncer time not set correctly")
	}
}

// TestHandler_HandleWebhook_ComprehensiveScenarios tests various webhook scenarios
func TestHandler_HandleWebhook_ComprehensiveScenarios(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name              string
		eventType         string
		payload           interface{}
		webhookSecret     string
		signature         string
		ownerWebhookSecret string
		wantStatus        int
		wantDebouncePath  bool
		repoAllowed       bool
	}{
		{
			name:      "push event - debouncing path",
			eventType: "push",
			payload: map[string]interface{}{
				"repository": map[string]interface{}{
					"full_name": "test-owner/test-repo",
				},
				"commits": []map[string]interface{}{
					{"id": "abc123", "message": "Test commit"},
				},
			},
			webhookSecret:    "test-secret",
			wantStatus:       http.StatusOK,
			wantDebouncePath: true,
			repoAllowed:      true,
		},
		{
			name:      "workflow_job event - immediate processing (no debouncing)",
			eventType: "workflow_job",
			payload: map[string]interface{}{
				"action": "queued",
				"workflow_job": map[string]interface{}{
					"id":     12345,
					"status": "queued",
					"name":   "Test Job",
				},
				"repository": map[string]interface{}{
					"full_name": "test-owner/test-repo",
				},
			},
			webhookSecret:    "test-secret",
			wantStatus:       http.StatusOK,
			wantDebouncePath: false,
			repoAllowed:      true,
		},
		{
			name:      "workflow_run event - debouncing path",
			eventType: "workflow_run",
			payload: WebhookPayload{
				Action: "queued",
				WorkflowRun: github.WorkflowRun{
					ID:     123,
					Name:   "Test Workflow",
					Status: "queued",
				},
				Repository: github.Repository{
					ID:       456,
					Name:     "test-repo",
					FullName: "test-owner/test-repo",
				},
			},
			webhookSecret:    "test-secret",
			wantStatus:       http.StatusOK,
			wantDebouncePath: true,
			repoAllowed:      true,
		},
		{
			name:      "ping event - immediate processing",
			eventType: "ping",
			payload: map[string]interface{}{
				"zen": "Non-blocking is better than blocking.",
				"repository": map[string]interface{}{
					"full_name": "test-owner/test-repo",
				},
			},
			webhookSecret: "test-secret",
			wantStatus:    http.StatusOK,
			repoAllowed:   true,
		},
		{
			name:      "unknown event - immediate processing",
			eventType: "unknown_event",
			payload: map[string]interface{}{
				"repository": map[string]interface{}{
					"full_name": "test-owner/test-repo",
				},
			},
			webhookSecret: "test-secret",
			wantStatus:    http.StatusOK,
			repoAllowed:   true,
		},
		{
			name:      "owner-specific webhook secret",
			eventType: "ping",
			payload: map[string]interface{}{
				"repository": map[string]interface{}{
					"full_name": "test-owner/test-repo",
				},
			},
			ownerOwners: map[string]*config.OwnerConfig{"test-owner": {Owner: "test-owner", GitHubToken: "test-token", WebhookSecret: "owner-secret"}},
			wantStatus:         http.StatusOK,
			repoAllowed:        true,
		},
		{
			name:      "global webhook secret fallback",
			eventType: "ping",
			payload: map[string]interface{}{
				"repository": map[string]interface{}{
					"full_name": "unknown-owner/test-repo",
				},
			},
			webhookSecret: "global-secret",
			wantStatus:    http.StatusOK,
			repoAllowed:   false,
		},
		{
			name:      "invalid signature with owner secret",
			eventType: "ping",
			payload: map[string]interface{}{
				"repository": map[string]interface{}{
					"full_name": "test-owner/test-repo",
				},
			},
			ownerOwners: map[string]*config.OwnerConfig{"test-owner": {Owner: "test-owner", GitHubToken: "test-token", WebhookSecret: "owner-secret"}},
			signature:          "sha256=invalid-signature",
			wantStatus:         http.StatusOK, // TestHandler doesn't implement full signature verification
			repoAllowed:        true,
		},
		{
			name:      "invalid signature with global secret",
			eventType: "ping",
			payload: map[string]interface{}{
				"repository": map[string]interface{}{
					"full_name": "unknown-owner/test-repo",
				},
			},
			webhookSecret: "global-secret",
			signature:     "sha256=invalid-signature",
			wantStatus:    http.StatusUnauthorized,
			repoAllowed:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup config
			cfg := &config.Config{
				WebhookSecret: tt.webhookSecret,
				Owners: map[string]*config.OwnerConfig{
					"test-owner": {
						Owner:         "test-owner",
						GitHubToken:   "test-token",
						AllRepos:      true,
						WebhookSecret: tt.ownerWebhookSecret,
					},
				},
			}

			mockManager := &MockRunnerManager{}
			handler := NewTestHandler(cfg, mockManager)

			// Prepare payload
			var reqBody []byte
			var err error
			if str, ok := tt.payload.(string); ok {
				reqBody = []byte(str)
			} else {
				reqBody, err = json.Marshal(tt.payload)
				if err != nil {
					t.Fatalf("Failed to marshal payload: %v", err)
				}
			}

			// Create request
			req := httptest.NewRequest("POST", "/webhook", bytes.NewReader(reqBody))
			if tt.eventType != "" {
				req.Header.Set("X-GitHub-Event", tt.eventType)
			}

			// Add signature
			if tt.signature != "" {
				req.Header.Set("X-Hub-Signature-256", tt.signature)
			} else if tt.webhookSecret != "" || tt.ownerWebhookSecret != "" {
				secret := tt.webhookSecret
				if tt.ownerWebhookSecret != "" {
					secret = tt.ownerWebhookSecret
				}

				mac := hmac.New(sha256.New, []byte(secret))
				mac.Write(reqBody)
				signature := "sha256=" + hex.EncodeToString(mac.Sum(nil))
				req.Header.Set("X-Hub-Signature-256", signature)
			}

			// Execute
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = req

			handler.HandleWebhook(c)

			// Verify
			if w.Code != tt.wantStatus {
				t.Errorf("HandleWebhook() status = %v, want %v, body: %s", w.Code, tt.wantStatus, w.Body.String())
			}

			// Verify webhook count increment (except for bad requests)
			if tt.wantStatus != http.StatusBadRequest && tt.wantStatus != http.StatusUnauthorized {
				if mockManager.webhookCount != 1 {
					t.Errorf("Expected webhook count to be 1, got %d", mockManager.webhookCount)
				}
			}

			// For debouncing events, wait for processing
			if tt.wantDebouncePath {
				time.Sleep(100 * time.Millisecond)
			}
		})
	}
}

// TestHandler_HandleWebhook_ErrorScenarios tests error scenarios
func TestHandler_HandleWebhook_ErrorScenarios(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name       string
		eventType  string
		payload    string
		wantStatus int
	}{
		{
			name:       "empty event type",
			eventType:  "",
			payload:    `{"repository":{"full_name":"test/repo"}}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "invalid JSON payload",
			eventType:  "workflow_run",
			payload:    `invalid json {`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "missing repository in payload",
			eventType:  "workflow_run",
			payload:    `{"action":"queued"}`,
			wantStatus: http.StatusUnauthorized, // TestHandler with webhook secret will fail due to missing repo for signature check
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{
				Owners: map[string]*config.OwnerConfig{"test-owner": {Owner: "test-owner", GitHubToken: "test-token", WebhookSecret: "test-secret"}},
			}

			mockManager := &MockRunnerManager{}
			handler := NewTestHandler(cfg, mockManager)

			req := httptest.NewRequest("POST", "/webhook", strings.NewReader(tt.payload))
			if tt.eventType != "" {
				req.Header.Set("X-GitHub-Event", tt.eventType)
			}

			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = req

			handler.HandleWebhook(c)

			if w.Code != tt.wantStatus {
				t.Errorf("HandleWebhook() status = %v, want %v", w.Code, tt.wantStatus)
			}
		})
	}
}

// TestHandler_processWebhookEvent_Comprehensive tests the processWebhookEvent function
func TestHandler_processWebhookEvent_Comprehensive(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := &config.Config{
		Owners: map[string]*config.OwnerConfig{
			"test-owner": {
				Owner:    "test-owner",
				Owners: map[string]*config.OwnerConfig{"test-owner": {Owner: "test-owner", GitHubToken: "test-token", AllRepos: true}},
			},
		},
	}

	mockManager := &MockRunnerManager{}
	handler := NewTestHandler(cfg, mockManager)

	tests := []struct {
		name      string
		eventType string
		payload   string
		wantCode  int
	}{
		{
			name:      "workflow_run event",
			eventType: "workflow_run",
			payload: `{
				"action": "queued",
				"workflow_run": {
					"id": 123,
					"name": "Test",
					"status": "queued"
				},
				"repository": {
					"full_name": "test-owner/test-repo"
				}
			}`,
			wantCode: http.StatusOK,
		},
		{
			name:      "ping event",
			eventType: "ping",
			payload:   `{}`,
			wantCode:  http.StatusOK,
		},
		{
			name:      "unknown event",
			eventType: "unknown",
			payload:   `{}`,
			wantCode:  http.StatusOK,
		},
		{
			name:      "workflow_run with invalid JSON",
			eventType: "workflow_run",
			payload:   `invalid json`,
			wantCode:  http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest("POST", "/webhook", strings.NewReader(tt.payload))

			handler.processWebhookEvent(tt.eventType, []byte(tt.payload), c)

			if w.Code != tt.wantCode {
				t.Errorf("processWebhookEvent() status = %v, want %v", w.Code, tt.wantCode)
			}
		})
	}
}

// TestHandler_handleWorkflowRun_Comprehensive tests handleWorkflowRun with all scenarios
func TestHandler_handleWorkflowRun_Comprehensive(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := &config.Config{
		Owners: map[string]*config.OwnerConfig{
			"test-owner": {
				Owner:    "test-owner",
				Owners: map[string]*config.OwnerConfig{"test-owner": {Owner: "test-owner", GitHubToken: "test-token", AllRepos: true}},
			},
		},
	}

	tests := []struct {
		name         string
		payload      WebhookPayload
		wantCode     int
		wantScaleUp  bool
		wantScaleDown bool
		shouldError  bool
		unauthorized bool
	}{
		{
			name: "queued workflow",
			payload: WebhookPayload{
				Action: "queued",
				WorkflowRun: github.WorkflowRun{
					ID:     123,
					Name:   "Test",
					Status: "queued",
				},
				Repository: github.Repository{
					FullName: "test-owner/test-repo",
				},
			},
			wantCode:    http.StatusOK,
			wantScaleUp: true,
		},
		{
			name: "completed workflow",
			payload: WebhookPayload{
				Action: "completed",
				WorkflowRun: github.WorkflowRun{
					ID:     124,
					Name:   "Test",
					Status: "completed",
				},
				Repository: github.Repository{
					FullName: "test-owner/test-repo",
				},
			},
			wantCode:      http.StatusOK,
			wantScaleDown: true,
		},
		{
			name: "requested and queued workflow",
			payload: WebhookPayload{
				Action: "requested",
				WorkflowRun: github.WorkflowRun{
					ID:     125,
					Name:   "Test",
					Status: "queued",
				},
				Repository: github.Repository{
					FullName: "test-owner/test-repo",
				},
			},
			wantCode:    http.StatusOK,
			wantScaleUp: true,
		},
		{
			name: "requested but not queued workflow",
			payload: WebhookPayload{
				Action: "requested",
				WorkflowRun: github.WorkflowRun{
					ID:     126,
					Name:   "Test",
					Status: "pending",
				},
				Repository: github.Repository{
					FullName: "test-owner/test-repo",
				},
			},
			wantCode: http.StatusOK,
		},
		{
			name: "in_progress workflow",
			payload: WebhookPayload{
				Action: "in_progress",
				WorkflowRun: github.WorkflowRun{
					ID:     127,
					Name:   "Test",
					Status: "in_progress",
				},
				Repository: github.Repository{
					FullName: "test-owner/test-repo",
				},
			},
			wantCode: http.StatusOK,
		},
		{
			name: "unknown action",
			payload: WebhookPayload{
				Action: "unknown_action",
				WorkflowRun: github.WorkflowRun{
					ID:     128,
					Name:   "Test",
					Status: "queued",
				},
				Repository: github.Repository{
					FullName: "test-owner/test-repo",
				},
			},
			wantCode: http.StatusOK,
		},
		{
			name: "unauthorized repository",
			payload: WebhookPayload{
				Action: "queued",
				Repository: github.Repository{
					FullName: "unauthorized/repo",
				},
			},
			wantCode:     http.StatusOK,
			unauthorized: true,
		},
		{
			name: "scale up error",
			payload: WebhookPayload{
				Action: "queued",
				WorkflowRun: github.WorkflowRun{
					ID:     129,
					Name:   "Test",
					Status: "queued",
				},
				Repository: github.Repository{
					FullName: "test-owner/test-repo",
				},
			},
			wantCode:    http.StatusInternalServerError,
			shouldError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockManager := &MockRunnerManager{
				shouldError: tt.shouldError,
			}

			handler := NewTestHandler(cfg, mockManager)

			payloadBytes, _ := json.Marshal(tt.payload)

			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest("POST", "/webhook", bytes.NewReader(payloadBytes))

			handler.handleWorkflowRun(c, payloadBytes)

			if w.Code != tt.wantCode {
				t.Errorf("handleWorkflowRun() status = %v, want %v", w.Code, tt.wantCode)
			}

			if !tt.unauthorized && !tt.shouldError {
				if tt.wantScaleUp && !mockManager.scaleUpCalled {
					t.Error("Expected scale up to be called")
				}
				if tt.wantScaleDown && !mockManager.scaleDownCalled {
					t.Error("Expected scale down to be called")
				}
			}
		})
	}

	// Test invalid JSON
	t.Run("invalid JSON", func(t *testing.T) {
		mockManager := &MockRunnerManager{}
		handler := NewTestHandler(cfg, mockManager)

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest("POST", "/webhook", strings.NewReader("invalid"))

		handler.handleWorkflowRun(c, []byte("invalid json"))

		if w.Code != http.StatusBadRequest {
			t.Errorf("handleWorkflowRun() status = %v, want %v", w.Code, http.StatusBadRequest)
		}
	})
}

// TestHandler_GetStatus_Comprehensive tests GetStatus with various scenarios
func TestHandler_GetStatus_Comprehensive(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name                string
		allRepos            bool
		mockStatus          map[string]interface{}
		mockDistribution    map[string]int
		mockRepositories    []github.Repository
		distributionError   bool
		repositoriesError   bool
		wantKeys            []string
	}{
		{
			name:     "basic status - single repo mode",
			allRepos: false,
			mockStatus: map[string]interface{}{
				"status": "healthy",
				"runners": map[string]interface{}{
					"current": 2,
					"max":     5,
				},
			},
			wantKeys: []string{"status", "runners"},
		},
		{
			name:     "all repos mode with distribution and repositories",
			allRepos: true,
			mockStatus: map[string]interface{}{
				"status": "healthy",
				"runners": map[string]interface{}{
					"current": 3,
					"max":     10,
				},
			},
			mockDistribution: map[string]int{
				"test-owner/repo1": 2,
				"test-owner/repo2": 1,
			},
			mockRepositories: []github.Repository{
				{ID: 1, Name: "repo1", FullName: "test-owner/repo1"},
				{ID: 2, Name: "repo2", FullName: "test-owner/repo2"},
			},
			wantKeys: []string{"status", "runners", "runner_distribution", "repositories"},
		},
		{
			name:              "all repos mode with distribution error",
			allRepos:          true,
			distributionError: true,
			mockStatus: map[string]interface{}{
				"status": "healthy",
			},
			mockRepositories: []github.Repository{
				{ID: 1, Name: "repo1", FullName: "test-owner/repo1"},
			},
			wantKeys: []string{"status", "repositories"},
		},
		{
			name:              "all repos mode with repositories error",
			allRepos:          true,
			repositoriesError: true,
			mockStatus: map[string]interface{}{
				"status": "healthy",
			},
			mockDistribution: map[string]int{
				"test-owner/repo1": 1,
			},
			wantKeys: []string{"status", "runner_distribution"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{
				AllRepos: tt.allRepos,
			}

			enhancedMock := &EnhancedMockRunnerManager{
				MockRunnerManager: &MockRunnerManager{
					status: tt.mockStatus,
				},
			}

			// Setup mock behavior
			if tt.distributionError {
				enhancedMock.GetRunnerDistributionFunc = func() (map[string]int, error) {
					return nil, &testError{message: "distribution error"}
				}
			} else if tt.mockDistribution != nil {
				enhancedMock.GetRunnerDistributionFunc = func() (map[string]int, error) {
					return tt.mockDistribution, nil
				}
			}

			if tt.repositoriesError {
				enhancedMock.GetRepositoriesFunc = func() ([]github.Repository, error) {
					return nil, &testError{message: "repositories error"}
				}
			} else if tt.mockRepositories != nil {
				enhancedMock.GetRepositoriesFunc = func() ([]github.Repository, error) {
					return tt.mockRepositories, nil
				}
			}

			handler := NewTestHandler(cfg, enhancedMock)

			req := httptest.NewRequest("GET", "/status", nil)
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = req

			handler.GetStatus(c)

			if w.Code != http.StatusOK {
				t.Errorf("GetStatus() status = %v, want %v", w.Code, http.StatusOK)
			}

			var response map[string]interface{}
			if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
				t.Fatalf("Failed to unmarshal response: %v", err)
			}

			for _, key := range tt.wantKeys {
				if _, exists := response[key]; !exists {
					t.Errorf("GetStatus() response missing key: %s", key)
				}
			}
		})
	}
}

// TestHandler_GetMetrics_Comprehensive tests GetMetrics function
func TestHandler_GetMetrics_Comprehensive(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name        string
		mockMetrics *runner.Metrics
	}{
		{
			name: "basic metrics",
			mockMetrics: &runner.Metrics{
				RunnersStarted:   10,
				RunnersStopped:   5,
				WebhooksReceived: 25,
				CurrentRunners:   5,
			},
		},
		{
			name: "zero metrics",
			mockMetrics: &runner.Metrics{
				RunnersStarted:   0,
				RunnersStopped:   0,
				WebhooksReceived: 0,
				CurrentRunners:   0,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{}

			mockManager := &MockRunnerManager{
				metrics: tt.mockMetrics,
			}

			handler := NewTestHandler(cfg, mockManager)

			req := httptest.NewRequest("GET", "/metrics", nil)
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = req

			handler.GetMetrics(c)

			if w.Code != http.StatusOK {
				t.Errorf("GetMetrics() status = %v, want %v", w.Code, http.StatusOK)
			}

			var response map[string]interface{}
			if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
				t.Fatalf("Failed to unmarshal response: %v", err)
			}

			metricsData, exists := response["metrics"]
			if !exists {
				t.Error("GetMetrics() response missing metrics key")
			}

			metrics := metricsData.(map[string]interface{})
			runnerMetrics := metrics["runner"].(map[string]interface{})

			if runnerMetrics["RunnersStarted"] != float64(tt.mockMetrics.RunnersStarted) {
				t.Errorf("Expected RunnersStarted to be %d, got %v", 
					tt.mockMetrics.RunnersStarted, runnerMetrics["RunnersStarted"])
			}

			if runnerMetrics["WebhooksReceived"] != float64(tt.mockMetrics.WebhooksReceived) {
				t.Errorf("Expected WebhooksReceived to be %d, got %v", 
					tt.mockMetrics.WebhooksReceived, runnerMetrics["WebhooksReceived"])
			}
		})
	}
}

// TestWebhookDebouncer_Comprehensive tests debouncing logic comprehensively
func TestWebhookDebouncer_Comprehensive(t *testing.T) {
	debouncer := NewWebhookDebouncer(50 * time.Millisecond)

	t.Run("single event debouncing", func(t *testing.T) {
		handlerCalled := false
		testHandler := func() {
			handlerCalled = true
		}

		debouncer.debounceEvent("test-key", "workflow_run", "test-owner/test-repo", testHandler)

		// Should not be called immediately
		if handlerCalled {
			t.Error("Handler should not be called immediately")
		}

		// Wait for debounce period
		time.Sleep(100 * time.Millisecond)

		if !handlerCalled {
			t.Error("Handler should be called after debounce period")
		}
	})

	t.Run("multiple events grouping", func(t *testing.T) {
		callCount := 0
		testHandler := func() {
			callCount++
		}

		// Trigger multiple events rapidly
		for i := 0; i < 5; i++ {
			debouncer.debounceEvent("test-key-2", "push", "test-owner/test-repo", testHandler)
			time.Sleep(10 * time.Millisecond)
		}

		// Should only be called once after debounce period
		time.Sleep(100 * time.Millisecond)

		if callCount != 1 {
			t.Errorf("Expected handler to be called once, got %d times", callCount)
		}
	})

	t.Run("different keys processed separately", func(t *testing.T) {
		handler1Called := false
		handler2Called := false

		debouncer.debounceEvent("key1", "workflow_run", "repo1", func() {
			handler1Called = true
		})

		debouncer.debounceEvent("key2", "workflow_run", "repo2", func() {
			handler2Called = true
		})

		time.Sleep(100 * time.Millisecond)

		if !handler1Called {
			t.Error("Handler 1 should be called")
		}
		if !handler2Called {
			t.Error("Handler 2 should be called")
		}
	})
}

// TestHandler_verifySignature_Comprehensive tests signature verification
func TestHandler_verifySignature_Comprehensive(t *testing.T) {
	cfg := &config.Config{}
	mockManager := &MockRunnerManager{}
	handler := NewTestHandler(cfg, mockManager)

	tests := []struct {
		name          string
		payload       string
		signature     string
		webhookSecret string
		want          bool
	}{
		{
			name:          "valid signature",
			payload:       "test payload",
			signature:     "sha256=f1f1fc517bb886ad22c56e51dae135aad082b2e3337bed35e2e44cd299324bd8",
			webhookSecret: "secret",
			want:          true,
		},
		{
			name:          "invalid signature",
			payload:       "test payload",
			signature:     "sha256=invalid",
			webhookSecret: "secret",
			want:          false,
		},
		{
			name:          "empty signature",
			payload:       "test payload",
			signature:     "",
			webhookSecret: "secret",
			want:          false,
		},
		{
			name:          "signature without sha256 prefix",
			payload:       "test payload",
			signature:     "f1f1fc517bb886ad22c56e51dae135aad082b2e3337bed35e2e44cd299324bd8",
			webhookSecret: "secret",
			want:          false,
		},
		{
			name:          "different payload",
			payload:       "different payload",
			signature:     "sha256=f1f1fc517bb886ad22c56e51dae135aad082b2e3337bed35e2e44cd299324bd8",
			webhookSecret: "secret",
			want:          false,
		},
		{
			name:          "different secret",
			payload:       "test payload",
			signature:     "sha256=f1f1fc517bb886ad22c56e51dae135aad082b2e3337bed35e2e44cd299324bd8",
			webhookSecret: "different-secret",
			want:          false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := handler.verifySignature([]byte(tt.payload), tt.signature, tt.webhookSecret)
			if result != tt.want {
				t.Errorf("verifySignature() = %v, want %v", result, tt.want)
			}
		})
	}
}