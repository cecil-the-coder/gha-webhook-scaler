package github

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
)

// Test the functions with 0% coverage using MockTransport pattern
func TestCoverage_ValidateTokenWithScopes(t *testing.T) {
	client := NewClient("test-token")
	mockTransport := &MockTransport{}
	client.httpClient = &http.Client{Transport: mockTransport}

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(`{"login": "testuser"}`)),
		Header:     make(http.Header),
	}
	resp.Header.Set("X-OAuth-Scopes", "repo,write:repo_hook")

	mockTransport.On("RoundTrip", mock.AnythingOfType("*http.Request")).Return(resp, nil)

	tokenInfo, err := client.ValidateTokenWithScopes()
	if err != nil {
		t.Errorf("ValidateTokenWithScopes() error = %v", err)
	}
	if tokenInfo == nil {
		t.Error("ValidateTokenWithScopes() returned nil token info")
	}
	mockTransport.AssertExpectations(t)
}

func TestCoverage_CheckTokenScopes(t *testing.T) {
	client := NewClient("test-token")
	mockTransport := &MockTransport{}
	client.httpClient = &http.Client{Transport: mockTransport}

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(`{"login": "testuser"}`)),
		Header:     make(http.Header),
	}
	resp.Header.Set("X-OAuth-Scopes", "repo,write:repo_hook")

	mockTransport.On("RoundTrip", mock.AnythingOfType("*http.Request")).Return(resp, nil)

	scopes, tokenType, err := client.CheckTokenScopes()
	if err != nil {
		t.Errorf("CheckTokenScopes() error = %v", err)
	}
	if len(scopes) == 0 {
		t.Error("CheckTokenScopes() returned empty scopes")
	}
	if tokenType == "" {
		t.Error("CheckTokenScopes() returned empty token type")
	}
	mockTransport.AssertExpectations(t)
}

func TestCoverage_HasScopeForWebhooks(t *testing.T) {
	client := NewClient("test-token")
	mockTransport := &MockTransport{}
	client.httpClient = &http.Client{Transport: mockTransport}

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(`{"login": "testuser"}`)),
		Header:     make(http.Header),
	}
	resp.Header.Set("X-OAuth-Scopes", "repo,write:repo_hook")

	mockTransport.On("RoundTrip", mock.AnythingOfType("*http.Request")).Return(resp, nil)

	hasScope, err := client.HasScopeForWebhooks()
	if err != nil {
		t.Errorf("HasScopeForWebhooks() error = %v", err)
	}
	if !hasScope {
		t.Error("HasScopeForWebhooks() should return true for repo scope")
	}
	mockTransport.AssertExpectations(t)
}

func TestCoverage_HasScopeForRunners(t *testing.T) {
	client := NewClient("test-token")
	mockTransport := &MockTransport{}
	client.httpClient = &http.Client{Transport: mockTransport}

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(`{"login": "testuser"}`)),
		Header:     make(http.Header),
	}
	resp.Header.Set("X-OAuth-Scopes", "repo")

	mockTransport.On("RoundTrip", mock.AnythingOfType("*http.Request")).Return(resp, nil)

	hasScope, err := client.HasScopeForRunners()
	if err != nil {
		t.Errorf("HasScopeForRunners() error = %v", err)
	}
	if !hasScope {
		t.Error("HasScopeForRunners() should return true for repo scope")
	}
	mockTransport.AssertExpectations(t)
}

func TestCoverage_TestRepositoryPermissions(t *testing.T) {
	client := NewClient("test-token")
	mockTransport := &MockTransport{}
	client.httpClient = &http.Client{Transport: mockTransport}

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(`{"permissions": {"admin": true, "push": true, "pull": true}}`)),
		Header:     make(http.Header),
	}

	mockTransport.On("RoundTrip", mock.AnythingOfType("*http.Request")).Return(resp, nil)

	perms, err := client.TestRepositoryPermissions("testowner", "testrepo")
	if err != nil {
		t.Errorf("TestRepositoryPermissions() error = %v", err)
	}
	if perms == nil {
		t.Error("TestRepositoryPermissions() returned nil permissions")
	}
	mockTransport.AssertExpectations(t)
}

func TestCoverage_GetActiveWorkflowJobs(t *testing.T) {
	client := NewClient("test-token")
	mockTransport := &MockTransport{}
	client.httpClient = &http.Client{Transport: mockTransport}

	// First call for workflow runs
	resp1 := &http.Response{
		StatusCode: http.StatusOK,
		Body: io.NopCloser(strings.NewReader(`{
			"workflow_runs": [
				{
					"id": 123,
					"status": "in_progress"
				}
			]
		}`)),
		Header: make(http.Header),
	}

	// Second call for jobs
	resp2 := &http.Response{
		StatusCode: http.StatusOK,
		Body: io.NopCloser(strings.NewReader(`{
			"jobs": [
				{
					"id": 456,
					"name": "test-job",
					"status": "in_progress",
					"labels": ["self-hosted"]
				}
			]
		}`)),
		Header: make(http.Header),
	}

	mockTransport.On("RoundTrip", mock.AnythingOfType("*http.Request")).Return(resp1, nil).Once()
	mockTransport.On("RoundTrip", mock.AnythingOfType("*http.Request")).Return(resp2, nil).Once()

	jobs, err := client.GetActiveWorkflowJobs("testowner", "testrepo")
	if err != nil {
		t.Errorf("GetActiveWorkflowJobs() error = %v", err)
	}
	if len(jobs) == 0 {
		t.Error("GetActiveWorkflowJobs() returned no jobs")
	}
	mockTransport.AssertExpectations(t)
}

func TestCoverage_GetQueuedJobLabels(t *testing.T) {
	client := NewClient("test-token")
	mockTransport := &MockTransport{}
	client.httpClient = &http.Client{Transport: mockTransport}

	// Mock response for empty workflow runs to test the function flow
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body: io.NopCloser(strings.NewReader(`{
			"workflow_runs": []
		}`)),
		Header: make(http.Header),
	}

	mockTransport.On("RoundTrip", mock.AnythingOfType("*http.Request")).Return(resp, nil)

	labels, err := client.GetQueuedJobLabels("testowner", "testrepo")
	if err != nil {
		t.Errorf("GetQueuedJobLabels() error = %v", err)
	}
	// Should return empty slice when no workflow runs
	if labels == nil {
		t.Error("GetQueuedJobLabels() returned nil")
	}
	mockTransport.AssertExpectations(t)
}

func TestCoverage_SimpleAPIFunctions(t *testing.T) {
	client := NewClient("test-token")
	mockTransport := &MockTransport{}
	client.httpClient = &http.Client{Transport: mockTransport}

	// Test GetIssues
	issuesResp := &http.Response{
		StatusCode: http.StatusOK,
		Body: io.NopCloser(strings.NewReader(`[
			{
				"id": 1,
				"number": 1,
				"title": "Test Issue",
				"state": "open"
			}
		]`)),
		Header: make(http.Header),
	}

	mockTransport.On("RoundTrip", mock.AnythingOfType("*http.Request")).Return(issuesResp, nil).Once()

	issues, err := client.GetIssues("testowner", "testrepo", "open")
	if err != nil {
		t.Errorf("GetIssues() error = %v", err)
	}
	if len(issues) == 0 {
		t.Error("GetIssues() returned no issues")
	}

	// Test GetPullRequests
	prsResp := &http.Response{
		StatusCode: http.StatusOK,
		Body: io.NopCloser(strings.NewReader(`[
			{
				"id": 1,
				"number": 1,
				"title": "Test PR",
				"state": "open"
			}
		]`)),
		Header: make(http.Header),
	}

	mockTransport.On("RoundTrip", mock.AnythingOfType("*http.Request")).Return(prsResp, nil).Once()

	prs, err := client.GetPullRequests("testowner", "testrepo", "open")
	if err != nil {
		t.Errorf("GetPullRequests() error = %v", err)
	}
	if len(prs) == 0 {
		t.Error("GetPullRequests() returned no pull requests")
	}

	// Test GetReleases
	releasesResp := &http.Response{
		StatusCode: http.StatusOK,
		Body: io.NopCloser(strings.NewReader(`[
			{
				"id": 1,
				"tag_name": "v1.0.0",
				"name": "Release 1.0.0",
				"draft": false
			}
		]`)),
		Header: make(http.Header),
	}

	mockTransport.On("RoundTrip", mock.AnythingOfType("*http.Request")).Return(releasesResp, nil).Once()

	releases, err := client.GetReleases("testowner", "testrepo")
	if err != nil {
		t.Errorf("GetReleases() error = %v", err)
	}
	if len(releases) == 0 {
		t.Error("GetReleases() returned no releases")
	}

	// Test GetWebhooks
	webhooksResp := &http.Response{
		StatusCode: http.StatusOK,
		Body: io.NopCloser(strings.NewReader(`[
			{
				"id": 1,
				"name": "web",
				"active": true,
				"events": ["push", "pull_request"]
			}
		]`)),
		Header: make(http.Header),
	}

	mockTransport.On("RoundTrip", mock.AnythingOfType("*http.Request")).Return(webhooksResp, nil).Once()

	webhooks, err := client.GetWebhooks("testowner", "testrepo")
	if err != nil {
		t.Errorf("GetWebhooks() error = %v", err)
	}
	if len(webhooks) == 0 {
		t.Error("GetWebhooks() returned no webhooks")
	}

	// Test CreateWebhook
	createResp := &http.Response{
		StatusCode: http.StatusCreated,
		Body: io.NopCloser(strings.NewReader(`{
			"id": 1,
			"name": "web",
			"active": true,
			"events": ["push"]
		}`)),
		Header: make(http.Header),
	}

	mockTransport.On("RoundTrip", mock.AnythingOfType("*http.Request")).Return(createResp, nil).Once()

	webhook, err := client.CreateWebhook("testowner", "testrepo", "http://example.com/webhook", []string{"push"}, "secret")
	if err != nil {
		t.Errorf("CreateWebhook() error = %v", err)
	}
	if webhook == nil {
		t.Error("CreateWebhook() returned nil webhook")
	}

	// Test DeleteWebhook
	deleteResp := &http.Response{
		StatusCode: http.StatusNoContent,
		Body:       io.NopCloser(strings.NewReader("")),
		Header:     make(http.Header),
	}

	mockTransport.On("RoundTrip", mock.AnythingOfType("*http.Request")).Return(deleteResp, nil).Once()

	err = client.DeleteWebhook("testowner", "testrepo", 1)
	if err != nil {
		t.Errorf("DeleteWebhook() error = %v", err)
	}

	// Test GetBranches
	branchesResp := &http.Response{
		StatusCode: http.StatusOK,
		Body: io.NopCloser(strings.NewReader(`[
			{
				"name": "main",
				"commit": {
					"sha": "abc123"
				}
			}
		]`)),
		Header: make(http.Header),
	}

	mockTransport.On("RoundTrip", mock.AnythingOfType("*http.Request")).Return(branchesResp, nil).Once()

	branches, err := client.GetBranches("testowner", "testrepo")
	if err != nil {
		t.Errorf("GetBranches() error = %v", err)
	}
	if len(branches) == 0 {
		t.Error("GetBranches() returned no branches")
	}

	// Test GetCommits
	commitsResp := &http.Response{
		StatusCode: http.StatusOK,
		Body: io.NopCloser(strings.NewReader(`[
			{
				"sha": "abc123",
				"commit": {
					"message": "Test commit",
					"author": {
						"date": "2023-01-01T00:00:00Z"
					}
				}
			}
		]`)),
		Header: make(http.Header),
	}

	mockTransport.On("RoundTrip", mock.AnythingOfType("*http.Request")).Return(commitsResp, nil).Once()

	since := time.Now().Add(-24 * time.Hour)
	commits, err := client.GetCommits("testowner", "testrepo", &since)
	if err != nil {
		t.Errorf("GetCommits() error = %v", err)
	}
	if len(commits) == 0 {
		t.Error("GetCommits() returned no commits")
	}

	mockTransport.AssertExpectations(t)
}