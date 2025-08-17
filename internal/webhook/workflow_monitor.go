package webhook

import (
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/cecil-the-coder/gha-webhook-scaler/internal/config"
	"github.com/cecil-the-coder/gha-webhook-scaler/internal/github"
	"github.com/cecil-the-coder/gha-webhook-scaler/internal/runner"
)

// WorkflowMonitor tracks active workflows and continuously polls for job changes
type WorkflowMonitor struct {
	config               *config.Config
	githubClients        map[string]*github.Client
	runnerManager        *runner.Manager

	// Workflow tracking
	activeWorkflows      map[string]*MonitoredWorkflow // key: "owner/repo:run_id"
	mu                   sync.RWMutex

	// Polling configuration
	pollInterval         time.Duration
	maxPollDuration      time.Duration
	stopChan             chan bool
}

// MonitoredWorkflow represents a workflow being actively monitored
type MonitoredWorkflow struct {
	RunID       int64
	RepoOwner   string
	RepoName    string
	FullName    string // "owner/repo"
	Status      string
	StartedAt   time.Time
	LastChecked time.Time

	// Polling state
	pollCount   int
	maxPolls    int

	// Callbacks
	onJobFound  func(job github.WorkflowJob, repoFullName string)
	onComplete  func(runID int64)
}

// NewWorkflowMonitor creates a new workflow monitor
func NewWorkflowMonitor(config *config.Config, githubClients map[string]*github.Client, runnerManager *runner.Manager) *WorkflowMonitor {
	return &WorkflowMonitor{
		config:           config,
		githubClients:    githubClients,
		runnerManager:    runnerManager,
		activeWorkflows:  make(map[string]*MonitoredWorkflow),
		pollInterval:     30 * time.Second,  // Check every 30 seconds
		maxPollDuration:  30 * time.Minute,  // Maximum monitoring time
		stopChan:         make(chan bool),
	}
}

// Start begins the workflow monitoring background service
func (wm *WorkflowMonitor) Start() {
	log.Printf("Starting workflow monitor with %v poll interval", wm.pollInterval)

	go func() {
		ticker := time.NewTicker(wm.pollInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				wm.pollActiveWorkflows()
			case <-wm.stopChan:
				log.Println("Workflow monitor stopped")
				return
			}
		}
	}()
}

// Stop stops the workflow monitoring service
func (wm *WorkflowMonitor) Stop() {
	close(wm.stopChan)
}

// StartMonitoring begins monitoring a new workflow
func (wm *WorkflowMonitor) StartMonitoring(runID int64, repoOwner, repoName, fullName, status string) {
	key := fmt.Sprintf("%s:%d", fullName, runID)

	wm.mu.Lock()
	defer wm.mu.Unlock()

	// Check if already being monitored
	if _, exists := wm.activeWorkflows[key]; exists {
		log.Printf("Workflow %s is already being monitored", key)
		return
	}

	// Create monitored workflow
	maxPolls := int(wm.maxPollDuration / wm.pollInterval)
	workflow := &MonitoredWorkflow{
		RunID:       runID,
		RepoOwner:   repoOwner,
		RepoName:    repoName,
		FullName:    fullName,
		Status:      status,
		StartedAt:   time.Now(),
		LastChecked: time.Now(),
		pollCount:   0,
		maxPolls:    maxPolls,
		onJobFound:  wm.handleJobFound,
		onComplete:  wm.handleWorkflowComplete,
	}

	wm.activeWorkflows[key] = workflow
	log.Printf("Started monitoring workflow %s (run_id: %d, status: %s, max_polls: %d)",
		fullName, runID, status, maxPolls)
}

// StopMonitoring stops monitoring a workflow
func (wm *WorkflowMonitor) StopMonitoring(runID int64, fullName string) {
	key := fmt.Sprintf("%s:%d", fullName, runID)

	wm.mu.Lock()
	defer wm.mu.Unlock()

	if workflow, exists := wm.activeWorkflows[key]; exists {
		delete(wm.activeWorkflows, key)
		log.Printf("Stopped monitoring workflow %s after %d polls", key, workflow.pollCount)
	}
}

// UpdateWorkflowStatus updates the status of a monitored workflow
func (wm *WorkflowMonitor) UpdateWorkflowStatus(runID int64, fullName, status string) {
	key := fmt.Sprintf("%s:%d", fullName, runID)

	wm.mu.Lock()
	defer wm.mu.Unlock()

	if workflow, exists := wm.activeWorkflows[key]; exists {
		workflow.Status = status
		log.Printf("Updated workflow %s status to: %s", key, status)

		// If workflow is completed, stop monitoring
		if status == "completed" || status == "failed" || status == "cancelled" {
			delete(wm.activeWorkflows, key)
			log.Printf("Workflow %s completed (%s), stopped monitoring", key, status)
		}
	}
}

// GetMonitoredWorkflows returns the list of currently monitored workflows
func (wm *WorkflowMonitor) GetMonitoredWorkflows() []MonitoredWorkflow {
	wm.mu.RLock()
	defer wm.mu.RUnlock()

	workflows := make([]MonitoredWorkflow, 0, len(wm.activeWorkflows))
	for _, workflow := range wm.activeWorkflows {
		workflows = append(workflows, *workflow)
	}

	return workflows
}

// pollActiveWorkflows checks all active workflows for job changes
func (wm *WorkflowMonitor) pollActiveWorkflows() {
	wm.mu.RLock()
	workflows := make([]*MonitoredWorkflow, 0, len(wm.activeWorkflows))
	for _, workflow := range wm.activeWorkflows {
		workflows = append(workflows, workflow)
	}
	wm.mu.RUnlock()

	for _, workflow := range workflows {
		wm.pollWorkflow(workflow)
	}
}

// pollWorkflow checks a specific workflow for job changes
func (wm *WorkflowMonitor) pollWorkflow(workflow *MonitoredWorkflow) {
	// Check if we've exceeded max polls
	if workflow.pollCount >= workflow.maxPolls {
		log.Printf("Workflow %s exceeded max polls (%d), stopping monitoring",
			workflow.FullName, workflow.maxPolls)
		wm.StopMonitoring(workflow.RunID, workflow.FullName)
		return
	}

	workflow.pollCount++
	workflow.LastChecked = time.Now()

	// Get GitHub client for this repository
	githubClient, exists := wm.githubClients[workflow.RepoOwner]
	if !exists {
		log.Printf("No GitHub client found for owner %s", workflow.RepoOwner)
		return
	}

	// Get workflow jobs
	jobs, err := githubClient.GetWorkflowJobs(workflow.RepoOwner, workflow.RepoName, workflow.RunID)
	if err != nil {
		log.Printf("Failed to get jobs for workflow %s run %d: %v",
			workflow.FullName, workflow.RunID, err)
		return
	}

	// Look for jobs that need self-hosted runners
	hasActiveJobs := false
	for _, job := range jobs.Jobs {
		// Check jobs in active states that need self-hosted runners
		if job.Status == "queued" || job.Status == "waiting" || job.Status == "in_progress" {
			requiresSelfHosted := false
			for _, label := range job.Labels {
				if label == "self-hosted" {
					requiresSelfHosted = true
					break
				}
			}

			if requiresSelfHosted {
				hasActiveJobs = true
				if workflow.onJobFound != nil {
					workflow.onJobFound(job, workflow.FullName)
				}
			}
		}
	}

	// If no active jobs found, we continue monitoring
	// The workflow will be stopped when we receive a completion webhook
	// or when it reaches max polling duration
	if !hasActiveJobs {
		log.Printf("AGGRESSIVE MONITOR: No active self-hosted jobs found for workflow %s, continuing to monitor",
			workflow.FullName)
	}

	if workflow.pollCount%5 == 0 { // Log every 5th poll to reduce noise
		log.Printf("Workflow %s: poll %d/%d, still monitoring",
			workflow.FullName, workflow.pollCount, workflow.maxPolls)
	}
}

// handleJobFound is called when a job needing self-hosted runners is found
func (wm *WorkflowMonitor) handleJobFound(job github.WorkflowJob, repoFullName string) {
	log.Printf("AGGRESSIVE MONITOR: Found active job %d (%s) [status: %s] requiring self-hosted runner with labels: %v",
		job.ID, job.Name, job.Status, job.Labels)

	// Trigger runner scaling
	err := wm.runnerManager.ScaleUpForJob(job, repoFullName)
	if err != nil {
		log.Printf("Failed to scale up for job %d: %v", job.ID, err)
	}
}

// handleWorkflowComplete is called when a workflow completes
func (wm *WorkflowMonitor) handleWorkflowComplete(runID int64) {
	log.Printf("AGGRESSIVE MONITOR: Workflow run %d completed", runID)
}