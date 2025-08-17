package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	
	"github.com/cecil-the-coder/gha-webhook-scaler/internal/config"
	"github.com/cecil-the-coder/gha-webhook-scaler/internal/github"
	"github.com/cecil-the-coder/gha-webhook-scaler/internal/runner"
)

type Handler struct {
	config               *config.Config
	runnerManager        *runner.Manager
	debouncer           *WebhookDebouncer
	workflowMonitor     *WorkflowMonitor
}

type WebhookDebouncer struct {
	mu            sync.Mutex
	events        map[string]*pendingEvent
	debounceTime  time.Duration
}

type pendingEvent struct {
	eventType   string
	repo        string
	count       int
	lastSeen    time.Time
	timer       *time.Timer
	handler     func()
}

type WebhookPayload struct {
	Action      string              `json:"action"`
	WorkflowRun github.WorkflowRun  `json:"workflow_run"`
	Repository  github.Repository   `json:"repository"`
}

func NewWebhookDebouncer(debounceTime time.Duration) *WebhookDebouncer {
	return &WebhookDebouncer{
		events:       make(map[string]*pendingEvent),
		debounceTime: debounceTime,
	}
}

func NewHandler(config *config.Config, runnerManager *runner.Manager) *Handler {
	return &Handler{
		config:              config,
		runnerManager:       runnerManager,
		debouncer:          NewWebhookDebouncer(2 * time.Second),
		workflowMonitor:     nil, // Will be set after GitHub clients are available
	}
}

// SetWorkflowMonitor sets the workflow monitor (called after all dependencies are ready)
func (wh *Handler) SetWorkflowMonitor(monitor *WorkflowMonitor) {
	wh.workflowMonitor = monitor
}

func (wh *Handler) HandleWebhook(c *gin.Context) {
	// Increment webhook counter
	wh.runnerManager.IncrementWebhookCount()

	// Get the event type
	eventType := c.GetHeader("X-GitHub-Event")
	if eventType == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Missing X-GitHub-Event header"})
		return
	}

	// Read the payload
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		log.Printf("Failed to read webhook body: %v", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": "Failed to read body"})
		return
	}

	// Parse payload to get repository first
	var prelimPayload struct {
		Repository github.Repository `json:"repository"`
	}
	if err := json.Unmarshal(body, &prelimPayload); err != nil {
		log.Printf("Failed to parse repository from payload: %v", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid JSON payload"})
		return
	}

	// Get owner configuration for webhook secret verification
	ownerConfig, err := wh.config.GetOwnerForRepo(prelimPayload.Repository.FullName)
	if err == nil && ownerConfig.WebhookSecret != "" {
		// Verify signature with owner-specific secret
		signature := c.GetHeader("X-Hub-Signature-256")
		if !wh.verifySignature(body, signature, ownerConfig.WebhookSecret) {
			log.Printf("Invalid webhook signature for owner %s", ownerConfig.Owner)
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid signature"})
			return
		}
	}

	log.Printf("Received webhook: %s", eventType)

	// workflow_job events should not be debounced - each job needs its own runner
	if eventType == "workflow_job" {
		// Handle immediately - no debouncing
		wh.handleWorkflowJobEvent(body)
		c.JSON(http.StatusOK, gin.H{"message": "Workflow job event processed"})
		return
	}

	// For events that can be grouped (push, workflow_run), use debouncing
	if eventType == "push" || eventType == "workflow_run" {
		key := eventType + ":" + prelimPayload.Repository.FullName
		
		wh.debouncer.debounceEvent(key, eventType, prelimPayload.Repository.FullName, func() {
			// This function will be called after debounce period
			wh.handleDebouncedEvent(eventType, body)
		})
		
		c.JSON(http.StatusOK, gin.H{"message": "Event received and queued for processing"})
		return
	}

	// For non-groupable events, handle immediately
	wh.processWebhookEvent(eventType, body, c)
}

func (wd *WebhookDebouncer) debounceEvent(key, eventType, repo string, handler func()) {
	wd.mu.Lock()
	defer wd.mu.Unlock()

	now := time.Now()
	
	if existing, exists := wd.events[key]; exists {
		// Cancel existing timer
		existing.timer.Stop()
		// Update count and last seen time
		existing.count++
		existing.lastSeen = now
		
		log.Printf("Grouped %s event for %s (count: %d)", eventType, repo, existing.count)
	} else {
		// Create new pending event
		wd.events[key] = &pendingEvent{
			eventType: eventType,
			repo:      repo,
			count:     1,
			lastSeen:  now,
			handler:   handler,
		}
		
		log.Printf("New %s event for %s (debouncing for %v)", eventType, repo, wd.debounceTime)
	}

	// Set new timer
	wd.events[key].timer = time.AfterFunc(wd.debounceTime, func() {
		wd.mu.Lock()
		defer wd.mu.Unlock()
		
		if event, exists := wd.events[key]; exists {
			log.Printf("Processing debounced %s event for %s (total count: %d)", event.eventType, event.repo, event.count)
			delete(wd.events, key)
			event.handler()
		}
	})
}

func (wh *Handler) handleDebouncedEvent(eventType string, body []byte) {
	// Handle the event based on type
	switch eventType {
	case "workflow_run":
		wh.handleWorkflowRunDirect(body)
	case "push":
		wh.handlePushEvent(body)
	case "workflow_job":
		wh.handleWorkflowJobEvent(body)
	}
}

func (wh *Handler) processWebhookEvent(eventType string, body []byte, c *gin.Context) {
	// Handle different event types
	switch eventType {
	case "workflow_run":
		wh.handleWorkflowRun(c, body)
	case "ping":
		wh.handlePing(c)
	default:
		log.Printf("Unhandled webhook event type: %s", eventType)
		c.JSON(http.StatusOK, gin.H{"message": "Event type not handled"})
	}
}

func (wh *Handler) handlePushEvent(body []byte) {
	var payload struct {
		Repository github.Repository `json:"repository"`
		Commits    []struct {
			ID      string `json:"id"`
			Message string `json:"message"`
		} `json:"commits"`
	}
	
	if err := json.Unmarshal(body, &payload); err != nil {
		log.Printf("Failed to parse push payload: %v", err)
		return
	}
	
	log.Printf("Processed grouped push event for %s (%d commits)", payload.Repository.FullName, len(payload.Commits))
}

func (wh *Handler) handleWorkflowJobEvent(body []byte) {
	var payload struct {
		Action       string     `json:"action"`
		WorkflowJob  struct {
			ID     int64  `json:"id"`
			Status string `json:"status"`
			Name   string `json:"name"`
			Labels []string `json:"labels"`
		} `json:"workflow_job"`
		Repository github.Repository `json:"repository"`
	}
	
	if err := json.Unmarshal(body, &payload); err != nil {
		log.Printf("Failed to parse workflow_job payload: %v", err)
		return
	}
	
	log.Printf("Workflow job %s: %s (status: %s, repo: %s, job: %s)", 
		payload.Action, payload.WorkflowJob.Name, payload.WorkflowJob.Status, payload.Repository.FullName, payload.WorkflowJob.Name)

	// Check if this repository is allowed
	if !wh.config.IsRepoAllowed(payload.Repository.FullName) {
		log.Printf("Webhook for unauthorized repository: %s", payload.Repository.FullName)
		return
	}

	repoName := wh.config.GetRepoNameFromFullName(payload.Repository.FullName)

	// Handle different workflow job actions
	switch payload.Action {
	case "queued":
		log.Printf("Job queued: %s (repo: %s) with labels %v - scaling up", payload.WorkflowJob.Name, payload.Repository.FullName, payload.WorkflowJob.Labels)
		if err := wh.runnerManager.HandleJobQueued(payload.Repository.FullName, repoName, payload.WorkflowJob.Labels); err != nil {
			log.Printf("Failed to handle job queued: %v", err)
			return
		}

	case "completed":
		log.Printf("Job completed: %s (repo: %s)", payload.WorkflowJob.Name, payload.Repository.FullName)
		// Don't cleanup immediately - ephemeral runners will auto-cleanup after job completion
		// The periodic cleanup routine will handle any runners that didn't terminate properly

	case "in_progress":
		log.Printf("Job in progress: %s (repo: %s)", payload.WorkflowJob.Name, payload.Repository.FullName)

	default:
		log.Printf("Unhandled workflow_job action: %s", payload.Action)
	}
}

func (wh *Handler) handleWorkflowRunDirect(body []byte) {
	var payload WebhookPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		log.Printf("Failed to parse workflow_run payload: %v", err)
		return
	}

	// Check if this repository is allowed
	if !wh.config.IsRepoAllowed(payload.Repository.FullName) {
		log.Printf("Webhook for unauthorized repository: %s", payload.Repository.FullName)
		return
	}

	repoName := wh.config.GetRepoNameFromFullName(payload.Repository.FullName)
	ownerConfig, _ := wh.config.GetOwnerForRepo(payload.Repository.FullName)

	log.Printf("AGGRESSIVE MONITOR: Workflow run %s: %s (status: %s, repo: %s)",
		payload.Action, payload.WorkflowRun.Name, payload.WorkflowRun.Status, payload.Repository.FullName)

	// Handle different workflow run actions
	switch payload.Action {
	case "queued":
		// Start aggressive monitoring for queued workflows
		if wh.workflowMonitor != nil {
			wh.workflowMonitor.StartMonitoring(
				payload.WorkflowRun.ID,
				ownerConfig.Owner,
				repoName,
				payload.Repository.FullName,
				payload.WorkflowRun.Status,
			)
		}

		if err := wh.runnerManager.HandleWorkflowQueued(payload.Repository.FullName, repoName); err != nil {
			log.Printf("Failed to handle workflow queued: %v", err)
			return
		}
		log.Printf("AGGRESSIVE MONITOR: Workflow queued, scaling up + starting continuous monitoring")

	case "completed":
		// Stop aggressive monitoring when workflow completes
		if wh.workflowMonitor != nil {
			wh.workflowMonitor.StopMonitoring(payload.WorkflowRun.ID, payload.Repository.FullName)
		}

		if err := wh.runnerManager.HandleWorkflowCompleted(payload.Repository.FullName, repoName); err != nil {
			log.Printf("Failed to handle workflow completed: %v", err)
		}
		log.Printf("AGGRESSIVE MONITOR: Workflow completed, stopped monitoring")

	case "requested":
		// Workflow was requested - check if it's queued and needs a runner
		if payload.WorkflowRun.Status == "queued" {
			// Start aggressive monitoring for requested workflows that are queued
			if wh.workflowMonitor != nil {
				wh.workflowMonitor.StartMonitoring(
					payload.WorkflowRun.ID,
					ownerConfig.Owner,
					repoName,
					payload.Repository.FullName,
					payload.WorkflowRun.Status,
				)
			}

			log.Printf("AGGRESSIVE MONITOR: Workflow requested and queued: %s (repo: %s) - scaling up + monitoring", payload.WorkflowRun.Name, payload.Repository.FullName)
			if err := wh.runnerManager.HandleWorkflowQueued(payload.Repository.FullName, repoName); err != nil {
				log.Printf("Failed to handle workflow requested/queued: %v", err)
				return
			}
			log.Printf("AGGRESSIVE MONITOR: Workflow requested and queued, scaling up + monitoring")
		} else {
			log.Printf("AGGRESSIVE MONITOR: Workflow requested: %s (repo: %s)", payload.WorkflowRun.Name, payload.Repository.FullName)
		}

	case "in_progress":
		// Workflow is running - ensure monitoring is active
		if wh.workflowMonitor != nil {
			wh.workflowMonitor.UpdateWorkflowStatus(
				payload.WorkflowRun.ID,
				payload.Repository.FullName,
				payload.WorkflowRun.Status,
			)
		}
		log.Printf("AGGRESSIVE MONITOR: Workflow in progress: %s (repo: %s)", payload.WorkflowRun.Name, payload.Repository.FullName)

	default:
		log.Printf("AGGRESSIVE MONITOR: Unhandled workflow_run action: %s", payload.Action)
	}
}

func (wh *Handler) handleWorkflowRun(c *gin.Context, body []byte) {
	var payload WebhookPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		log.Printf("Failed to parse workflow_run payload: %v", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid JSON payload"})
		return
	}

	// Check if this repository is allowed
	if !wh.config.IsRepoAllowed(payload.Repository.FullName) {
		log.Printf("Webhook for unauthorized repository: %s", payload.Repository.FullName)
		c.JSON(http.StatusOK, gin.H{"message": "Repository not configured"})
		return
	}

	repoName := wh.config.GetRepoNameFromFullName(payload.Repository.FullName)
	log.Printf("Workflow run %s: %s (status: %s, repo: %s)", 
		payload.Action, payload.WorkflowRun.Name, payload.WorkflowRun.Status, payload.Repository.FullName)

	// Handle different workflow run actions
	switch payload.Action {
	case "queued":
		
		if err := wh.runnerManager.HandleWorkflowQueued(payload.Repository.FullName, repoName); err != nil {
			log.Printf("Failed to handle workflow queued: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to scale up"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"message": "Workflow queued, scaling up"})

	case "completed":
		if err := wh.runnerManager.HandleWorkflowCompleted(payload.Repository.FullName, repoName); err != nil {
			log.Printf("Failed to handle workflow completed: %v", err)
		}
		c.JSON(http.StatusOK, gin.H{"message": "Workflow completed"})

	case "requested":
		// Workflow was requested - check if it's queued and needs a runner
		if payload.WorkflowRun.Status == "queued" {
			
			log.Printf("Workflow requested and queued: %s (repo: %s) - scaling up", payload.WorkflowRun.Name, payload.Repository.FullName)
			if err := wh.runnerManager.HandleWorkflowQueued(payload.Repository.FullName, repoName); err != nil {
				log.Printf("Failed to handle workflow requested/queued: %v", err)
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to scale up"})
				return
			}
			c.JSON(http.StatusOK, gin.H{"message": "Workflow requested and queued, scaling up"})
		} else {
			log.Printf("Workflow requested: %s (repo: %s)", payload.WorkflowRun.Name, payload.Repository.FullName)
			c.JSON(http.StatusOK, gin.H{"message": "Workflow requested"})
		}

	case "in_progress":
		// Workflow is running
		log.Printf("Workflow in progress: %s (repo: %s)", payload.WorkflowRun.Name, payload.Repository.FullName)
		c.JSON(http.StatusOK, gin.H{"message": "Workflow in progress"})

	default:
		log.Printf("Unhandled workflow_run action: %s", payload.Action)
		c.JSON(http.StatusOK, gin.H{"message": "Action not handled"})
	}
}

func (wh *Handler) handlePing(c *gin.Context) {
	log.Printf("Received ping webhook")
	c.JSON(http.StatusOK, gin.H{
		"message": "pong",
		"status":  "healthy",
	})
}

func (wh *Handler) GetStatus(c *gin.Context) {
	status := wh.runnerManager.GetStatus()

	// Add runner distribution and repository information
	// (shown when any owner has all-repos mode enabled)
	distribution, err := wh.runnerManager.GetRunnerDistribution()
	if err != nil {
		log.Printf("Failed to get runner distribution: %v", err)
	} else if len(distribution) > 0 {
		status["runner_distribution"] = distribution
	}

	// Add repository information
	repos, err := wh.runnerManager.GetRepositories()
	if err != nil {
		log.Printf("Failed to get repositories: %v", err)
	} else if len(repos) > 0 {
		// Format basic repository information for the API response
		repoInfo := make([]map[string]interface{}, 0, len(repos))
		for _, repo := range repos {
			repoInfo = append(repoInfo, map[string]interface{}{
				"repository": repo.FullName,
				"name":       repo.Name,
			})
		}
		status["repositories"] = repoInfo
	}

	c.JSON(http.StatusOK, status)
}

func (wh *Handler) GetMetrics(c *gin.Context) {
	metrics := wh.runnerManager.GetMetrics()
	
	c.JSON(http.StatusOK, gin.H{
		"metrics": gin.H{
			"runner": metrics,
		},
	})
}


func (wh *Handler) verifySignature(payload []byte, signature string, webhookSecret string) bool {
	if signature == "" {
		return false
	}

	// Remove "sha256=" prefix
	if !strings.HasPrefix(signature, "sha256=") {
		return false
	}
	signature = signature[7:]

	// Calculate expected signature
	mac := hmac.New(sha256.New, []byte(webhookSecret))
	mac.Write(payload)
	expectedSignature := hex.EncodeToString(mac.Sum(nil))

	// Compare signatures
	return hmac.Equal([]byte(signature), []byte(expectedSignature))
}

 
