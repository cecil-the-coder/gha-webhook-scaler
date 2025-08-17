package webhook

import (
	"testing"

	"github.com/cecil-the-coder/gha-webhook-scaler/internal/config"
	"github.com/cecil-the-coder/gha-webhook-scaler/internal/github"
)

func TestNewWebhookManager(t *testing.T) {
	client := github.NewClient("test-token")
	config := &config.Config{}
	
	manager := NewWebhookManager(client, config)
	
	if manager == nil {
		t.Error("NewWebhookManager() returned nil")
	}
	
	if manager.githubClient != client {
		t.Error("NewWebhookManager() did not set GitHub client correctly")
	}
	
	if manager.config != config {
		t.Error("NewWebhookManager() did not set config correctly")
	}
}

func TestWebhookManager_ListWebhooks_InvalidParameters(t *testing.T) {
	// Test error handling with invalid parameters
	manager := NewWebhookManager(github.NewClient("test-token"), &config.Config{})
	
	// Test with empty owner
	hooks, err := manager.ListWebhooks("", "testrepo")
	if err == nil {
		t.Error("Expected error when owner is empty")
	}
	
	if hooks != nil {
		t.Error("Expected nil hooks when error occurs")
	}
	
	// Test with empty repo
	hooks, err = manager.ListWebhooks("testowner", "")
	if err == nil {
		t.Error("Expected error when repo is empty")
	}
	
	if hooks != nil {
		t.Error("Expected nil hooks when error occurs")
	}
}

func TestWebhookManager_CreateWebhook_InvalidParameters(t *testing.T) {
	// Test error handling with invalid parameters
	manager := NewWebhookManager(github.NewClient("test-token"), &config.Config{})
	
	// Test with empty owner
	hook, err := manager.CreateWebhook("", "testrepo", "https://example.com/webhook", "secret", []string{"workflow_run"})
	if err == nil {
		t.Error("Expected error when owner is empty")
	}
	
	if hook != nil {
		t.Error("Expected nil hook when error occurs")
	}
}

func TestWebhookManager_DeleteWebhook_InvalidParameters(t *testing.T) {
	// Test error handling with invalid parameters
	manager := NewWebhookManager(github.NewClient("test-token"), &config.Config{})
	
	// Test with empty owner
	err := manager.DeleteWebhook("", "testrepo", 12345)
	if err == nil {
		t.Error("Expected error when owner is empty")
	}
}

func TestWebhookManager_ValidateWebhookConfiguration(t *testing.T) {
	manager := NewWebhookManager(github.NewClient("test-token"), &config.Config{})
	
	tests := []struct {
		name        string
		webhookURL  string
		secret      string
		events      []string
		expectError bool
	}{
		{
			name:        "valid configuration",
			webhookURL:  "https://example.com/webhook",
			secret:      "secret",
			events:      []string{"workflow_run"},
			expectError: false,
		},
		{
			name:        "invalid URL",
			webhookURL:  "not-a-url",
			secret:      "secret",
			events:      []string{"workflow_run"},
			expectError: true,
		},
		{
			name:        "empty URL",
			webhookURL:  "",
			secret:      "secret",
			events:      []string{"workflow_run"},
			expectError: true,
		},
		{
			name:        "no events",
			webhookURL:  "https://example.com/webhook",
			secret:      "secret",
			events:      []string{},
			expectError: true,
		},
	}
	
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := manager.ValidateWebhookConfiguration(tt.webhookURL, tt.secret, tt.events)
			
			if tt.expectError && err == nil {
				t.Error("Expected error but got none")
			}
			
			if !tt.expectError && err != nil {
				t.Errorf("Unexpected error: %v", err)
			}
		})
	}
}