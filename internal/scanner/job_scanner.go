package scanner

import (
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/cecil-the-coder/gha-webhook-scaler/internal/config"
	"github.com/cecil-the-coder/gha-webhook-scaler/internal/github"
	"github.com/cecil-the-coder/gha-webhook-scaler/internal/runner"
)

// JobScanner scans repositories for queued workflow jobs that require self-hosted runners
type JobScanner struct {
	config         *config.Config
	githubClients  map[string]*github.Client
	runnerManager  *runner.Manager
	mu             sync.RWMutex
	lastScanTime   time.Time
	scanMetrics    ScanMetrics
}

// ScanMetrics tracks scanning statistics
type ScanMetrics struct {
	TotalScans         int
	LastScanDuration   time.Duration
	ReposScanned       int
	QueuedJobsFound    int
	RunnersCreated     int
	APICallsUsed       int
	LastError          string
	LastScanTime       time.Time
}

// QueuedJobInfo represents information about a queued job
type QueuedJobInfo struct {
	RepoOwner    string
	RepoName     string
	JobID        int64
	JobName      string
	WorkflowRunID int64
	Labels       []string
	RequiredArch string
}

// NewJobScanner creates a new job scanner instance
func NewJobScanner(config *config.Config, githubClients map[string]*github.Client, runnerManager *runner.Manager) *JobScanner {
	return &JobScanner{
		config:        config,
		githubClients: githubClients,
		runnerManager: runnerManager,
		scanMetrics:   ScanMetrics{},
	}
}

// StartScanning starts the periodic scanning routine
func (js *JobScanner) StartScanning() {
	if js.config.JobScanInterval <= 0 {
		log.Printf("Job scanning disabled (JOB_SCAN_INTERVAL_SECONDS=0)")
		return
	}

	log.Printf("Starting job scanner with %v interval", js.config.JobScanInterval)
	
	// Run initial scan immediately
	go js.performScan()
	
	// Set up periodic scanning
	ticker := time.NewTicker(js.config.JobScanInterval)
	go func() {
		for range ticker.C {
			js.performScan()
		}
	}()
}

// performScan conducts a single scan cycle across all configured repositories
func (js *JobScanner) performScan() {
	js.mu.Lock()
	defer js.mu.Unlock()

	startTime := time.Now()
	log.Printf("Starting job scan cycle...")
	
	js.scanMetrics.TotalScans++
	js.scanMetrics.ReposScanned = 0
	js.scanMetrics.QueuedJobsFound = 0
	js.scanMetrics.RunnersCreated = 0
	js.scanMetrics.APICallsUsed = 0
	js.scanMetrics.LastError = ""

	totalQueuedJobs := 0
	totalReposScanned := 0
	totalAPICallsUsed := 0

	// Scan repositories for each configured owner
	for ownerName, ownerConfig := range js.config.Owners {
		githubClient, exists := js.githubClients[ownerName]
		if !exists {
			log.Printf("No GitHub client found for owner %s, skipping", ownerName)
			continue
		}

		// Check rate limit before scanning this owner
		remaining, resetTime, _ := githubClient.GetRateLimitInfo()
		if remaining < 50 {
			log.Printf("Rate limit too low for owner %s (%d remaining, resets at %s), skipping", 
				ownerName, remaining, resetTime.Format("15:04:05"))
			continue
		}

		log.Printf("Scanning repositories for owner: %s", ownerName)

		if ownerConfig.AllRepos {
			// Scan all repositories for this owner
			reposScanned, queuedJobs, apiCalls, err := js.scanAllReposForOwner(ownerName, ownerConfig, githubClient)
			if err != nil {
				log.Printf("Error scanning all repos for owner %s: %v", ownerName, err)
				js.scanMetrics.LastError = fmt.Sprintf("Owner %s: %v", ownerName, err)
			}
			totalReposScanned += reposScanned
			totalQueuedJobs += queuedJobs
			totalAPICallsUsed += apiCalls
		} else if ownerConfig.RepoName != "" {
			// Scan specific repository
			queuedJobs, apiCalls, err := js.scanRepository(ownerName, ownerConfig.RepoName, githubClient)
			if err != nil {
				log.Printf("Error scanning repo %s/%s: %v", ownerName, ownerConfig.RepoName, err)
				js.scanMetrics.LastError = fmt.Sprintf("Repo %s/%s: %v", ownerName, ownerConfig.RepoName, err)
			}
			totalReposScanned++
			totalQueuedJobs += queuedJobs
			totalAPICallsUsed += apiCalls
		}

		// Add delay between owners to space out API calls
		if len(js.config.Owners) > 1 {
			time.Sleep(500 * time.Millisecond)
		}
	}

	// Update metrics
	js.scanMetrics.LastScanDuration = time.Since(startTime)
	js.scanMetrics.ReposScanned = totalReposScanned
	js.scanMetrics.QueuedJobsFound = totalQueuedJobs
	js.scanMetrics.APICallsUsed = totalAPICallsUsed
	js.scanMetrics.LastScanTime = startTime
	js.lastScanTime = startTime

	log.Printf("Job scan completed in %v: %d repos scanned, %d queued jobs found, %d API calls used", 
		js.scanMetrics.LastScanDuration.Round(time.Millisecond),
		totalReposScanned, totalQueuedJobs, totalAPICallsUsed)
}

// scanAllReposForOwner scans all repositories for a specific owner
func (js *JobScanner) scanAllReposForOwner(ownerName string, ownerConfig *config.OwnerConfig, githubClient *github.Client) (int, int, int, error) {
	// Get all repositories for this owner
	repos, err := githubClient.GetRepositories(ownerName)
	if err != nil {
		return 0, 0, 1, fmt.Errorf("failed to get repositories: %w", err)
	}

	reposScanned := 0
	totalQueuedJobs := 0
	apiCallsUsed := 1 // GetRepositories call

	// Filter to repositories owned by this owner
	ownedRepos := []github.Repository{}
	for _, repo := range repos {
		parts := strings.Split(repo.FullName, "/")
		if len(parts) == 2 && parts[0] == ownerName {
			ownedRepos = append(ownedRepos, repo)
		}
	}

	log.Printf("Found %d repositories for owner %s", len(ownedRepos), ownerName)

	// Scan each repository with rate limit awareness
	for i, repo := range ownedRepos {
		// Check rate limit periodically
		if i > 0 && i%5 == 0 {
			remaining, resetTime, _ := githubClient.GetRateLimitInfo()
			if remaining < 20 {
				log.Printf("Rate limit getting low (%d remaining, resets at %s), stopping repo scan for owner %s at %d/%d", 
					remaining, resetTime.Format("15:04:05"), ownerName, i, len(ownedRepos))
				break
			}
		}

		parts := strings.Split(repo.FullName, "/")
		if len(parts) != 2 {
			continue
		}
		
		repoOwner, repoName := parts[0], parts[1]
		queuedJobs, apiCalls, err := js.scanRepository(repoOwner, repoName, githubClient)
		if err != nil {
			log.Printf("Error scanning repository %s: %v", repo.FullName, err)
			continue
		}
		
		reposScanned++
		totalQueuedJobs += queuedJobs
		apiCallsUsed += apiCalls

		// Add small delay between repositories
		if i < len(ownedRepos)-1 {
			time.Sleep(200 * time.Millisecond)
		}
	}

	return reposScanned, totalQueuedJobs, apiCallsUsed, nil
}

// scanRepository scans a single repository for queued self-hosted jobs
func (js *JobScanner) scanRepository(repoOwner, repoName string, githubClient *github.Client) (int, int, error) {
	// Get queued workflow runs (this method already filters for self-hosted jobs)
	queuedRuns, err := githubClient.GetQueuedWorkflowRuns(repoOwner, repoName)
	if err != nil {
		return 0, 2, fmt.Errorf("failed to get queued workflow runs: %w", err)
	}

	apiCallsUsed := 1 + len(queuedRuns.WorkflowRuns) // GetQueuedWorkflowRuns + GetWorkflowJobs calls
	queuedJobCount := 0

	if len(queuedRuns.WorkflowRuns) == 0 {
		return 0, apiCallsUsed, nil
	}

	// Analyze queued jobs and determine if we need to create runners
	queuedJobs := []QueuedJobInfo{}
	
	for _, run := range queuedRuns.WorkflowRuns {
		jobs, err := githubClient.GetWorkflowJobs(repoOwner, repoName, run.ID)
		if err != nil {
			log.Printf("Failed to get jobs for workflow run %d in %s/%s: %v", run.ID, repoOwner, repoName, err)
			continue
		}
		
		for _, job := range jobs.Jobs {
			if job.Status == "queued" && js.isJobCompatible(job.Labels) {
				queuedJobInfo := QueuedJobInfo{
					RepoOwner:     repoOwner,
					RepoName:      repoName,
					JobID:         job.ID,
					JobName:       job.Name,
					WorkflowRunID: run.ID,
					Labels:        job.Labels,
					RequiredArch:  js.extractArchitecture(job.Labels),
				}
				queuedJobs = append(queuedJobs, queuedJobInfo)
				queuedJobCount++
			}
		}
	}

	if len(queuedJobs) > 0 {
		log.Printf("Found %d compatible queued jobs in %s/%s", len(queuedJobs), repoOwner, repoName)
		
		// Check if we should create runners for these jobs
		js.considerCreatingRunners(queuedJobs)
	}

	return queuedJobCount, apiCallsUsed, nil
}

// isJobCompatible checks if a job's labels are compatible with our runner capabilities
func (js *JobScanner) isJobCompatible(jobLabels []string) bool {
	// Must include self-hosted
	hasSelfHosted := false
	for _, label := range jobLabels {
		if label == "self-hosted" {
			hasSelfHosted = true
			break
		}
	}
	
	if !hasSelfHosted {
		return false
	}

	// Check if we support the required architecture
	requiredArch := js.extractArchitecture(jobLabels)
	if requiredArch != "" {
		// If job specifies architecture, check if we support it
		supportedArch := js.config.GetEffectiveArchitecture()
		if supportedArch != "all" && supportedArch != requiredArch {
			return false
		}
	}

	// Additional compatibility checks can be added here
	// e.g., checking for specific labels we support
	
	return true
}

// extractArchitecture extracts the architecture requirement from job labels
func (js *JobScanner) extractArchitecture(labels []string) string {
	for _, label := range labels {
		switch label {
		case "amd64", "x64":
			return "amd64"
		case "arm64":
			return "arm64"
		}
	}
	return "" // No specific architecture requirement
}

// considerCreatingRunners evaluates whether to create new runners for queued jobs
func (js *JobScanner) considerCreatingRunners(queuedJobs []QueuedJobInfo) {
	if len(queuedJobs) == 0 {
		return
	}

	// Group jobs by repository
	jobsByRepo := make(map[string][]QueuedJobInfo)
	for _, job := range queuedJobs {
		repoKey := fmt.Sprintf("%s/%s", job.RepoOwner, job.RepoName)
		jobsByRepo[repoKey] = append(jobsByRepo[repoKey], job)
	}

	for repoKey, jobs := range jobsByRepo {
		parts := strings.Split(repoKey, "/")
		if len(parts) != 2 {
			continue
		}
		repoOwner, repoName := parts[0], parts[1]
		repoFullName := fmt.Sprintf("%s/%s", repoOwner, repoName)

		log.Printf("Found %d queued compatible jobs in %s", len(jobs), repoFullName)

		// Create runners for each queued job
		// The ScaleUpWithLabels method handles all capacity checks and per-owner limits
		for _, job := range jobs {
			// Create a runner for this job using ScaleUpWithLabels
			// This method handles per-owner runner counting and max runner limits
			reason := fmt.Sprintf("Job scanner detected queued job %d (%s)", job.JobID, job.JobName)
			err := js.runnerManager.ScaleUpWithLabels(reason, repoFullName, repoName, job.Labels)
			if err != nil {
				// If we hit max runners, log and stop trying for this repo
				if err.Error() == "max runners reached" {
					log.Printf("Cannot create more runners for %s (at max capacity)", repoFullName)
					break
				}
				log.Printf("Failed to create runner for %s/%s (job %d): %v", repoOwner, repoName, job.JobID, err)
				continue
			}

			js.scanMetrics.RunnersCreated++
			log.Printf("Created runner for queued job %d (%s) in %s/%s", job.JobID, job.JobName, repoOwner, repoName)
		}
	}
}

// GetMetrics returns current scanning metrics
func (js *JobScanner) GetMetrics() ScanMetrics {
	js.mu.RLock()
	defer js.mu.RUnlock()
	return js.scanMetrics
}

// GetLastScanTime returns the time of the last scan
func (js *JobScanner) GetLastScanTime() time.Time {
	js.mu.RLock()
	defer js.mu.RUnlock()
	return js.lastScanTime
}