package github

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
	
	"gopkg.in/yaml.v3"
)

type Client struct {
	Token           string
	httpClient      *http.Client
	rateLimitMu     sync.Mutex
	rateLimitRemaining int
	rateLimitReset     time.Time
	lastAPICall        time.Time
}

type RegistrationTokenResponse struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

type WorkflowRun struct {
	ID         int64  `json:"id"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	Name       string `json:"name"`
}

type WorkflowRunsResponse struct {
	TotalCount   int           `json:"total_count"`
	WorkflowRuns []WorkflowRun `json:"workflow_runs"`
}

type Repository struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	FullName string `json:"full_name"`
}

type RepositoriesResponse struct {
	Repositories []Repository `json:"repositories"`
}

type WorkflowJob struct {
	ID       int64    `json:"id"`
	Status   string   `json:"status"`
	Name     string   `json:"name"`
	Labels   []string `json:"labels"`
	RunnerID *int64   `json:"runner_id"`
}

type WorkflowJobsResponse struct {
	TotalCount int           `json:"total_count"`
	Jobs       []WorkflowJob `json:"jobs"`
}

type Workflow struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Path     string `json:"path"`
	State    string `json:"state"`
	Created  string `json:"created_at"`
	Updated  string `json:"updated_at"`
}

type WorkflowsResponse struct {
	TotalCount int        `json:"total_count"`
	Workflows  []Workflow `json:"workflows"`
}

type FileContent struct {
	Name        string `json:"name"`
	Path        string `json:"path"`
	Content     string `json:"content"`
	Encoding    string `json:"encoding"`
	Size        int    `json:"size"`
	Type        string `json:"type"`
}

type WorkflowConfig struct {
	Name string `yaml:"name"`
	On   interface{} `yaml:"on"`
	Jobs map[string]struct {
		RunsOn interface{} `yaml:"runs-on"`
		Steps  []interface{} `yaml:"steps"`
	} `yaml:"jobs"`
}

func NewClient(token string) *Client {
	return &Client{
		Token: token,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		rateLimitRemaining: 5000, // Default assumption
		rateLimitReset:     time.Now().Add(time.Hour),
	}
}

// SetHTTPClient sets a custom HTTP client for testing
func (g *Client) SetHTTPClient(client *http.Client) {
	g.httpClient = client
}

// checkRateLimit checks and waits if rate limit is exceeded
func (g *Client) checkRateLimit() error {
	g.rateLimitMu.Lock()
	defer g.rateLimitMu.Unlock()

	// Check if we're close to rate limit
	if g.rateLimitRemaining < 10 && time.Now().Before(g.rateLimitReset) {
		waitTime := time.Until(g.rateLimitReset)
		if waitTime > 0 && waitTime < 60*time.Minute {
			// Only wait a maximum of 2 minutes, not the full reset time
			maxWait := 2 * time.Minute
			if waitTime > maxWait {
				log.Printf("Rate limit nearly exceeded (%d remaining), would wait %v but limiting to %v", 
					g.rateLimitRemaining, waitTime.Round(time.Second), maxWait)
				return fmt.Errorf("rate limit exceeded, try again in %v", waitTime.Round(time.Second))
			} else {
				log.Printf("Rate limit nearly exceeded (%d remaining), waiting %v until reset", 
					g.rateLimitRemaining, waitTime.Round(time.Second))
				time.Sleep(waitTime)
			}
		}
	}

	// Implement minimum delay between API calls (100ms)
	timeSinceLastCall := time.Since(g.lastAPICall)
	if timeSinceLastCall < 100*time.Millisecond {
		time.Sleep(100*time.Millisecond - timeSinceLastCall)
	}

	g.lastAPICall = time.Now()
	return nil
}

// GetRateLimitInfo returns current rate limit status
func (g *Client) GetRateLimitInfo() (remaining int, resetTime time.Time, waitTime time.Duration) {
	g.rateLimitMu.Lock()
	defer g.rateLimitMu.Unlock()
	
	remaining = g.rateLimitRemaining
	resetTime = g.rateLimitReset
	waitTime = time.Until(g.rateLimitReset)
	if waitTime < 0 {
		waitTime = 0
	}
	
	return remaining, resetTime, waitTime
}

// updateRateLimit updates rate limit information from response headers
func (g *Client) updateRateLimit(resp *http.Response) {
	g.rateLimitMu.Lock()
	defer g.rateLimitMu.Unlock()

	if remaining := resp.Header.Get("X-RateLimit-Remaining"); remaining != "" {
		if val, err := strconv.Atoi(remaining); err == nil {
			g.rateLimitRemaining = val
		}
	}

	if reset := resp.Header.Get("X-RateLimit-Reset"); reset != "" {
		if val, err := strconv.ParseInt(reset, 10, 64); err == nil {
			g.rateLimitReset = time.Unix(val, 0)
		}
	}
}

// MakeRequestWithBackoff makes an HTTP request with exponential backoff on rate limit errors
func (g *Client) MakeRequestWithBackoff(req *http.Request) (*http.Response, error) {
	maxRetries := 3
	baseDelay := 1 * time.Second

	for attempt := 0; attempt < maxRetries; attempt++ {
		// Check rate limit before making request
		if err := g.checkRateLimit(); err != nil {
			return nil, err
		}

		resp, err := g.httpClient.Do(req)
		if err != nil {
			return nil, err
		}

		// Update rate limit info
		g.updateRateLimit(resp)

		// If not rate limited, return the response
		if resp.StatusCode != 403 && resp.StatusCode != 429 {
			return resp, nil
		}

		// Check if it's a rate limit error
		if resp.StatusCode == 403 || resp.StatusCode == 429 {
			resp.Body.Close()
			
			if attempt < maxRetries-1 {
				delay := baseDelay * time.Duration(1<<attempt) // Exponential backoff
				log.Printf("Rate limited (status %d), retrying in %v (attempt %d/%d)", 
					resp.StatusCode, delay, attempt+1, maxRetries)
				time.Sleep(delay)
				continue
			}
		}

		return resp, nil
	}

	return nil, fmt.Errorf("max retries exceeded due to rate limiting")
}

func (g *Client) GetRegistrationToken(repoOwner, repoName string) (*RegistrationTokenResponse, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/actions/runners/registration-token", repoOwner, repoName)
	
	req, err := http.NewRequest("POST", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "token "+g.Token)
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	resp, err := g.MakeRequestWithBackoff(req)
	if err != nil {
		return nil, fmt.Errorf("failed to make request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("GitHub API error: %d - %s", resp.StatusCode, string(body))
	}

	var tokenResp RegistrationTokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &tokenResp, nil
}

func (g *Client) GetQueuedWorkflowRuns(repoOwner, repoName string) (*WorkflowRunsResponse, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/actions/runs?status=queued&per_page=100", repoOwner, repoName)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "token "+g.Token)
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	resp, err := g.MakeRequestWithBackoff(req)
	if err != nil {
		return nil, fmt.Errorf("failed to make request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		// For 404 errors on workflow runs, return empty result instead of error
		// This handles cases where repos don't exist or don't have Actions enabled
		if resp.StatusCode == http.StatusNotFound {
			log.Printf("Repository %s/%s not found or Actions not enabled - treating as no queued runs", repoOwner, repoName)
			return &WorkflowRunsResponse{
				TotalCount:   0,
				WorkflowRuns: []WorkflowRun{},
			}, nil
		}
		return nil, fmt.Errorf("GitHub API error: %d - %s", resp.StatusCode, string(body))
	}

	var runsResp WorkflowRunsResponse
	if err := json.NewDecoder(resp.Body).Decode(&runsResp); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	// Debug logging for all queued workflow runs
	if len(runsResp.WorkflowRuns) > 0 {
		fmt.Printf("DEBUG: Found %d total queued workflow runs for %s/%s:\n", len(runsResp.WorkflowRuns), repoOwner, repoName)
		for i, run := range runsResp.WorkflowRuns {
			fmt.Printf("  %d. ID: %d, Name: %s, Status: %s, Conclusion: %s\n",
				i+1, run.ID, run.Name, run.Status, run.Conclusion)
		}
	}

	// Filter runs to only include those with jobs that need self-hosted runners
	filteredRuns := []WorkflowRun{}
	selfHostedJobCount := 0

	for _, run := range runsResp.WorkflowRuns {
		jobs, err := g.GetWorkflowJobs(repoOwner, repoName, run.ID)
		if err != nil {
			fmt.Printf("DEBUG: Failed to get jobs for workflow run %d: %v\n", run.ID, err)
			continue
		}

		// FIX: Check for jobs in multiple states that still need self-hosted runners
		hasActiveSelfHostedJobs := false
		hasWaitingJobs := false // Track if there are jobs in "waiting" state

		for _, job := range jobs.Jobs {
			// Check jobs that still need runners: queued, waiting, in_progress
			if job.Status == "queued" || job.Status == "waiting" || job.Status == "in_progress" {
				// Check if this job requires self-hosted runners
				requiresSelfHosted := false
				for _, label := range job.Labels {
					if label == "self-hosted" {
						requiresSelfHosted = true
						break
					}
				}

				if requiresSelfHosted {
					hasActiveSelfHostedJobs = true
					selfHostedJobCount++

					if job.Status == "waiting" {
						hasWaitingJobs = true
					}

					fmt.Printf("DEBUG: Job %d (%s) [status: %s] requires self-hosted runner with labels: %v\n",
						job.ID, job.Name, job.Status, job.Labels)
				} else {
					fmt.Printf("DEBUG: Job %d (%s) [status: %s] uses GitHub-hosted runner with labels: %v\n",
						job.ID, job.Name, job.Status, job.Labels)
				}
			} else {
				// Log completed jobs for debugging
				fmt.Printf("DEBUG: Job %d (%s) [status: %s] - completed, labels: %v\n",
					job.ID, job.Name, job.Status, job.Labels)
			}
		}

		// RACE CONDITION FIX: Include workflow run if:
		// 1. It has active self-hosted jobs, OR
		// 2. The workflow run is still "queued" and has waiting jobs (indicates recent state changes)
		if hasActiveSelfHostedJobs || (run.Status == "queued" && hasWaitingJobs) {
			filteredRuns = append(filteredRuns, run)
			if !hasActiveSelfHostedJobs && hasWaitingJobs {
				fmt.Printf("DEBUG: Including workflow run %d due to race condition protection (run queued, has waiting jobs)\n", run.ID)
			}
		}
	}

	if len(filteredRuns) != len(runsResp.WorkflowRuns) {
		fmt.Printf("DEBUG: Filtered from %d total queued runs to %d runs needing self-hosted runners (%d jobs)\n",
			len(runsResp.WorkflowRuns), len(filteredRuns), selfHostedJobCount)
	}

	// Return filtered results
	runsResp.WorkflowRuns = filteredRuns
	runsResp.TotalCount = len(filteredRuns)

	return &runsResp, nil
}

func (g *Client) GetRepository(repoOwner, repoName string) (*Repository, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s", repoOwner, repoName)
	
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "token "+g.Token)
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	resp, err := g.MakeRequestWithBackoff(req)
	if err != nil {
		return nil, fmt.Errorf("failed to make request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("GitHub API error: %d - %s", resp.StatusCode, string(body))
	}

	var repo Repository
	if err := json.NewDecoder(resp.Body).Decode(&repo); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &repo, nil
}

func (g *Client) ValidateToken() error {
	req, err := http.NewRequest("GET", "https://api.github.com/user", nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "token "+g.Token)
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	resp, err := g.MakeRequestWithBackoff(req)
	if err != nil {
		return fmt.Errorf("failed to make request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("invalid GitHub token: %d", resp.StatusCode)
	}

	return nil
}

// ValidateTokenWithScopes performs comprehensive token validation including scope checking
func (g *Client) ValidateTokenWithScopes() (*TokenInfo, error) {
	validator := NewTokenValidator(g)
	return validator.ValidateToken()
}

// CheckTokenScopes returns the scopes available to the current token
func (g *Client) CheckTokenScopes() ([]string, string, error) {
	validator := NewTokenValidator(g)
	return validator.getTokenScopes()
}

// HasScopeForWebhooks checks if the token has sufficient permissions for webhook operations
func (g *Client) HasScopeForWebhooks() (bool, error) {
	validator := NewTokenValidator(g)
	err := validator.ValidateTokenForOperation("webhook_management")
	return err == nil, err
}

// HasScopeForRunners checks if the token has sufficient permissions for runner operations  
func (g *Client) HasScopeForRunners() (bool, error) {
	validator := NewTokenValidator(g)
	err := validator.ValidateTokenForOperation("runner_management")
	return err == nil, err
}

// TestRepositoryPermissions tests specific permissions against a repository
func (g *Client) TestRepositoryPermissions(owner, repo string) (*RepositoryTokenPermissions, error) {
	validator := NewTokenValidator(g)
	return validator.TestTokenPermissions(owner, repo)
}

func (g *Client) GetRepositories(owner string) ([]Repository, error) {
	// For authenticated user's repositories (including private ones)
	// Use /user/repos instead of /users/{owner}/repos
	url := "https://api.github.com/user/repos?type=all&per_page=100"
	
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "token "+g.Token)
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	resp, err := g.MakeRequestWithBackoff(req)
	if err != nil {
		return nil, fmt.Errorf("failed to make request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("GitHub API error: %d - %s", resp.StatusCode, string(body))
	}

	var repos []Repository
	if err := json.NewDecoder(resp.Body).Decode(&repos); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	// Filter repositories by owner if specified
	if owner != "" {
		var filteredRepos []Repository
		for _, repo := range repos {
			if repo.FullName != "" {
				// Extract owner from full_name (e.g., "owner/repo")
				parts := strings.Split(repo.FullName, "/")
				if len(parts) >= 2 && parts[0] == owner {
					filteredRepos = append(filteredRepos, repo)
				}
			}
		}
		return filteredRepos, nil
	}

	return repos, nil
}

// GetRepositoryLastActivity gets the last commit time for a repository
func (g *Client) GetRepositoryLastActivity(repoOwner, repoName string) (time.Time, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/commits?per_page=1", repoOwner, repoName)
	
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return time.Time{}, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "token "+g.Token)
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	resp, err := g.MakeRequestWithBackoff(req)
	if err != nil {
		return time.Time{}, fmt.Errorf("failed to make request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return time.Time{}, fmt.Errorf("GitHub API error: %d - %s", resp.StatusCode, string(body))
	}

	var commits []struct {
		Commit struct {
			Author struct {
				Date time.Time `json:"date"`
			} `json:"author"`
		} `json:"commit"`
	}
	
	if err := json.NewDecoder(resp.Body).Decode(&commits); err != nil {
		return time.Time{}, fmt.Errorf("failed to decode response: %w", err)
	}

	if len(commits) == 0 {
		return time.Time{}, fmt.Errorf("no commits found")
	}

	return commits[0].Commit.Author.Date, nil
}

// GetWorkflowJobs fetches jobs for a specific workflow run with pagination support
func (g *Client) GetWorkflowJobs(repoOwner, repoName string, runID int64) (*WorkflowJobsResponse, error) {
	allJobs := []WorkflowJob{}
	totalCount := 0
	page := 1
	perPage := 100

	for {
		url := fmt.Sprintf("https://api.github.com/repos/%s/%s/actions/runs/%d/jobs?per_page=%d&page=%d",
			repoOwner, repoName, runID, perPage, page)

		req, err := http.NewRequest("GET", url, nil)
		if err != nil {
			return nil, fmt.Errorf("failed to create request: %w", err)
		}

		req.Header.Set("Authorization", "token "+g.Token)
		req.Header.Set("Accept", "application/vnd.github.v3+json")

		resp, err := g.MakeRequestWithBackoff(req)
		if err != nil {
			return nil, fmt.Errorf("failed to make request: %w", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			return nil, fmt.Errorf("GitHub API error: %d - %s", resp.StatusCode, string(body))
		}

		var jobsResp WorkflowJobsResponse
		if err := json.NewDecoder(resp.Body).Decode(&jobsResp); err != nil {
			return nil, fmt.Errorf("failed to decode response: %w", err)
		}

		// Accumulate jobs from this page
		allJobs = append(allJobs, jobsResp.Jobs...)
		if totalCount == 0 {
			totalCount = jobsResp.TotalCount
		}

		// If we've received all jobs, break
		if len(allJobs) >= jobsResp.TotalCount || len(jobsResp.Jobs) < perPage {
			break
		}

		page++
	}

	return &WorkflowJobsResponse{
		TotalCount: totalCount,
		Jobs:       allJobs,
	}, nil
}

// GetActiveWorkflowJobs returns all active (in_progress) workflow jobs for a repository
func (g *Client) GetActiveWorkflowJobs(repoOwner, repoName string) ([]WorkflowJob, error) {
	// Get in-progress workflow runs
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/actions/runs?status=in_progress&per_page=100", repoOwner, repoName)
	
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "token "+g.Token)
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	resp, err := g.MakeRequestWithBackoff(req)
	if err != nil {
		return nil, fmt.Errorf("failed to make request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("GitHub API error: %d - %s", resp.StatusCode, string(body))
	}

	var runsResp WorkflowRunsResponse
	if err := json.NewDecoder(resp.Body).Decode(&runsResp); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	var activeJobs []WorkflowJob
	for _, run := range runsResp.WorkflowRuns {
		jobs, err := g.GetWorkflowJobs(repoOwner, repoName, run.ID)
		if err != nil {
			log.Printf("Failed to get jobs for in-progress workflow run %d: %v", run.ID, err)
			continue
		}
		
		for _, job := range jobs.Jobs {
			if job.Status == "in_progress" {
				activeJobs = append(activeJobs, job)
			}
		}
	}

	return activeJobs, nil
}

// GetQueuedJobLabels analyzes queued workflow runs and returns the required labels
func (g *Client) GetQueuedJobLabels(repoOwner, repoName string) ([]string, error) {
	// Get queued workflow runs
	runs, err := g.GetQueuedWorkflowRuns(repoOwner, repoName)
	if err != nil {
		return nil, fmt.Errorf("failed to get queued runs: %w", err)
	}

	if len(runs.WorkflowRuns) == 0 {
		return []string{}, nil
	}

	// Collect all unique labels from queued jobs
	labelSet := make(map[string]bool)
	
	for _, run := range runs.WorkflowRuns {
		jobs, err := g.GetWorkflowJobs(repoOwner, repoName, run.ID)
		if err != nil {
			// Log error but continue with other runs
			continue
		}
		
		for _, job := range jobs.Jobs {
			if job.Status == "queued" {
				for _, label := range job.Labels {
					labelSet[label] = true
				}
			}
		}
	}

	// Convert set to slice
	labels := make([]string, 0, len(labelSet))
	for label := range labelSet {
		labels = append(labels, label)
	}

	return labels, nil
}

// Additional structs for new API functionality
type Issue struct {
	ID     int64  `json:"id"`
	Number int    `json:"number"`
	Title  string `json:"title"`
	Body   string `json:"body"`
	State  string `json:"state"`
	User   *User  `json:"user"`
}

type PullRequest struct {
	ID     int64  `json:"id"`
	Number int    `json:"number"`
	Title  string `json:"title"`
	Body   string `json:"body"`
	State  string `json:"state"`
	User   *User  `json:"user"`
	Head   *Ref   `json:"head"`
	Base   *Ref   `json:"base"`
}

type User struct {
	ID    int64  `json:"id"`
	Login string `json:"login"`
	Type  string `json:"type"`
}

type Ref struct {
	Ref  string      `json:"ref"`
	SHA  string      `json:"sha"`
	Repo *Repository `json:"repo"`
}

type Release struct {
	ID      int64  `json:"id"`
	TagName string `json:"tag_name"`
	Name    string `json:"name"`
	Body    string `json:"body"`
	Draft   bool   `json:"draft"`
}

type Webhook struct {
	ID     int64             `json:"id"`
	Name   string            `json:"name"`
	Active bool              `json:"active"`
	Events []string          `json:"events"`
	Config map[string]string `json:"config"`
}

type Branch struct {
	Name      string `json:"name"`
	Commit    Commit `json:"commit"`
	Protected bool   `json:"protected"`
}

type Commit struct {
	SHA    string      `json:"sha"`
	Commit CommitData  `json:"commit"`
	Author *CommitUser `json:"author"`
}

type CommitData struct {
	Author    CommitUser `json:"author"`
	Committer CommitUser `json:"committer"`
	Message   string     `json:"message"`
}

type CommitUser struct {
	Name  string    `json:"name"`
	Email string    `json:"email"`
	Date  time.Time `json:"date"`
}

// GetIssues retrieves issues for a repository
func (g *Client) GetIssues(repoOwner, repoName string, state string) ([]Issue, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/issues?state=%s&per_page=100", repoOwner, repoName, state)
	
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "token "+g.Token)
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	resp, err := g.MakeRequestWithBackoff(req)
	if err != nil {
		return nil, fmt.Errorf("failed to make request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("GitHub API error: %d - %s", resp.StatusCode, string(body))
	}

	var issues []Issue
	if err := json.NewDecoder(resp.Body).Decode(&issues); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return issues, nil
}

// GetPullRequests retrieves pull requests for a repository
func (g *Client) GetPullRequests(repoOwner, repoName string, state string) ([]PullRequest, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/pulls?state=%s&per_page=100", repoOwner, repoName, state)
	
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "token "+g.Token)
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	resp, err := g.MakeRequestWithBackoff(req)
	if err != nil {
		return nil, fmt.Errorf("failed to make request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("GitHub API error: %d - %s", resp.StatusCode, string(body))
	}

	var pullRequests []PullRequest
	if err := json.NewDecoder(resp.Body).Decode(&pullRequests); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return pullRequests, nil
}

// GetReleases retrieves releases for a repository
func (g *Client) GetReleases(repoOwner, repoName string) ([]Release, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases?per_page=100", repoOwner, repoName)
	
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "token "+g.Token)
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	resp, err := g.MakeRequestWithBackoff(req)
	if err != nil {
		return nil, fmt.Errorf("failed to make request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("GitHub API error: %d - %s", resp.StatusCode, string(body))
	}

	var releases []Release
	if err := json.NewDecoder(resp.Body).Decode(&releases); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return releases, nil
}

// GetWebhooks retrieves webhooks for a repository
func (g *Client) GetWebhooks(repoOwner, repoName string) ([]Webhook, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/hooks", repoOwner, repoName)
	
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "token "+g.Token)
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	resp, err := g.MakeRequestWithBackoff(req)
	if err != nil {
		return nil, fmt.Errorf("failed to make request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("GitHub API error: %d - %s", resp.StatusCode, string(body))
	}

	var webhooks []Webhook
	if err := json.NewDecoder(resp.Body).Decode(&webhooks); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return webhooks, nil
}

// CreateWebhook creates a new webhook for a repository
func (g *Client) CreateWebhook(repoOwner, repoName, url string, events []string, secret string) (*Webhook, error) {
	webhookData := map[string]interface{}{
		"name":   "web",
		"active": true,
		"events": events,
		"config": map[string]string{
			"url":          url,
			"content_type": "json",
			"secret":       secret,
		},
	}

	jsonData, err := json.Marshal(webhookData)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal webhook data: %w", err)
	}

	reqURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/hooks", repoOwner, repoName)
	req, err := http.NewRequest("POST", reqURL, strings.NewReader(string(jsonData)))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "token "+g.Token)
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	req.Header.Set("Content-Type", "application/json")

	resp, err := g.MakeRequestWithBackoff(req)
	if err != nil {
		return nil, fmt.Errorf("failed to make request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("GitHub API error: %d - %s", resp.StatusCode, string(body))
	}

	var webhook Webhook
	if err := json.NewDecoder(resp.Body).Decode(&webhook); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &webhook, nil
}

// DeleteWebhook deletes a webhook from a repository
func (g *Client) DeleteWebhook(repoOwner, repoName string, webhookID int64) error {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/hooks/%d", repoOwner, repoName, webhookID)
	
	req, err := http.NewRequest("DELETE", url, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "token "+g.Token)
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	resp, err := g.MakeRequestWithBackoff(req)
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

// GetBranches retrieves branches for a repository
func (g *Client) GetBranches(repoOwner, repoName string) ([]Branch, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/branches?per_page=100", repoOwner, repoName)
	
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "token "+g.Token)
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	resp, err := g.MakeRequestWithBackoff(req)
	if err != nil {
		return nil, fmt.Errorf("failed to make request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("GitHub API error: %d - %s", resp.StatusCode, string(body))
	}

	var branches []Branch
	if err := json.NewDecoder(resp.Body).Decode(&branches); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return branches, nil
}

// GetCommits retrieves commits for a repository
func (g *Client) GetCommits(repoOwner, repoName string, since *time.Time) ([]Commit, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/commits?per_page=100", repoOwner, repoName)
	
	if since != nil {
		url += "&since=" + since.Format(time.RFC3339)
	}
	
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "token "+g.Token)
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	resp, err := g.MakeRequestWithBackoff(req)
	if err != nil {
		return nil, fmt.Errorf("failed to make request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("GitHub API error: %d - %s", resp.StatusCode, string(body))
	}

	var commits []Commit
	if err := json.NewDecoder(resp.Body).Decode(&commits); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return commits, nil
}

// GetUser retrieves information about the authenticated user
func (g *Client) GetUser() (*User, error) {
	req, err := http.NewRequest("GET", "https://api.github.com/user", nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "token "+g.Token)
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	resp, err := g.MakeRequestWithBackoff(req)
	if err != nil {
		return nil, fmt.Errorf("failed to make request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("GitHub API error: %d - %s", resp.StatusCode, string(body))
	}

	var user User
	if err := json.NewDecoder(resp.Body).Decode(&user); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &user, nil
}

// GetWorkflows retrieves all workflows for a repository
func (g *Client) GetWorkflows(owner, repo string) (*WorkflowsResponse, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/actions/workflows", owner, repo)
	
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "token "+g.Token)
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	resp, err := g.MakeRequestWithBackoff(req)
	if err != nil {
		return nil, fmt.Errorf("failed to make request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("GitHub API error: %d - %s", resp.StatusCode, string(body))
	}

	var workflows WorkflowsResponse
	if err := json.NewDecoder(resp.Body).Decode(&workflows); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &workflows, nil
}

// GetFileContent retrieves the content of a file from a repository
func (g *Client) GetFileContent(owner, repo, path string) (*FileContent, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/contents/%s", owner, repo, path)
	
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "token "+g.Token)
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	resp, err := g.MakeRequestWithBackoff(req)
	if err != nil {
		return nil, fmt.Errorf("failed to make request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("GitHub API error: %d - %s", resp.StatusCode, string(body))
	}

	var content FileContent
	if err := json.NewDecoder(resp.Body).Decode(&content); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &content, nil
}

// HasSelfHostedRunners checks if a repository has any workflows that use self-hosted runners
func (g *Client) HasSelfHostedRunners(owner, repo string) (bool, error) {
	workflows, err := g.GetWorkflows(owner, repo)
	if err != nil {
		return false, fmt.Errorf("failed to get workflows: %w", err)
	}

	for _, workflow := range workflows.Workflows {
		// Skip disabled workflows
		if workflow.State != "active" {
			continue
		}

		hasSelfHosted, err := g.checkWorkflowForSelfHosted(owner, repo, workflow.Path)
		if err != nil {
			log.Printf("Warning: failed to check workflow %s for self-hosted runners: %v", workflow.Path, err)
			continue
		}

		if hasSelfHosted {
			return true, nil
		}
	}

	return false, nil
}

// checkWorkflowForSelfHosted checks a specific workflow file for self-hosted runner usage
func (g *Client) checkWorkflowForSelfHosted(owner, repo, workflowPath string) (bool, error) {
	content, err := g.GetFileContent(owner, repo, workflowPath)
	if err != nil {
		return false, fmt.Errorf("failed to get workflow content: %w", err)
	}

	// Decode base64 content
	var yamlContent []byte
	if content.Encoding == "base64" {
		yamlContent, err = base64.StdEncoding.DecodeString(content.Content)
		if err != nil {
			return false, fmt.Errorf("failed to decode base64 content: %w", err)
		}
	} else {
		yamlContent = []byte(content.Content)
	}

	// Parse YAML
	var config WorkflowConfig
	if err := yaml.Unmarshal(yamlContent, &config); err != nil {
		return false, fmt.Errorf("failed to parse YAML: %w", err)
	}

	// Check each job for self-hosted runners
	for jobName, job := range config.Jobs {
		if containsSelfHosted(job.RunsOn) {
			log.Printf("Found self-hosted runner usage in workflow %s, job %s", workflowPath, jobName)
			return true, nil
		}
	}

	return false, nil
}

// containsSelfHosted checks if runs-on configuration contains self-hosted runners
func containsSelfHosted(runsOn interface{}) bool {
	switch v := runsOn.(type) {
	case string:
		return strings.Contains(strings.ToLower(v), "self-hosted")
	case []interface{}:
		for _, item := range v {
			if str, ok := item.(string); ok {
				if strings.Contains(strings.ToLower(str), "self-hosted") {
					return true
				}
			}
		}
	case []string:
		for _, str := range v {
			if strings.Contains(strings.ToLower(str), "self-hosted") {
				return true
			}
		}
	}
	return false
} 
