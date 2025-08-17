package runner

import (
	"context"
	"fmt"
	"log"
	"math/rand"
	"strings"
	"sync"
	"time"

	"github.com/cecil-the-coder/gha-webhook-scaler/internal/config"
	"github.com/cecil-the-coder/gha-webhook-scaler/internal/github"
	"github.com/cecil-the-coder/gha-webhook-scaler/internal/runtime"
	"golang.org/x/time/rate"
)

type Manager struct {
	config              *config.Config
	githubClients       map[string]*github.Client // key is owner name
	runtime             runtime.ContainerRuntime

	// Rate limiting
	runnerLimiter *rate.Limiter

	// Queue memory for pending scaling requests
	queueMemory *QueueMemory

	// Metrics
	metrics *Metrics
	mu      sync.RWMutex

}

type Metrics struct {
	RunnersStarted   int64
	RunnersStopped   int64
	WebhooksReceived int64
	ScaleUpEvents    int64
	ScaleDownEvents  int64
	LastScaleEvent   time.Time
	CurrentRunners   int
}


func NewManager(config *config.Config, githubClients map[string]*github.Client, runtime runtime.ContainerRuntime) *Manager {
	return &Manager{
		config:              config,
		githubClients:       githubClients,
		runtime:             runtime,
		runnerLimiter:       rate.NewLimiter(rate.Every(1*time.Second), 1), // 1 runner per second, burst of 1
		queueMemory:         NewQueueMemory(),
		metrics: &Metrics{
			LastScaleEvent: time.Now(),
		},
	}
}

func (rm *Manager) ScaleUp(reason string, repoFullName string, repoName string) error {
	return rm.ScaleUpWithLabels(reason, repoFullName, repoName, nil)
}

func (rm *Manager) ScaleUpWithLabels(reason string, repoFullName string, repoName string, customLabels []string) error {
	rm.mu.Lock()
	defer rm.mu.Unlock()

	// Get owner configuration for this repository
	ownerConfig, err := rm.config.GetOwnerForRepo(repoFullName)
	if err != nil {
		return fmt.Errorf("failed to get owner config for repo %s: %w", repoFullName, err)
	}

	// Get the GitHub client for this owner
	githubClient, exists := rm.githubClients[ownerConfig.Owner]
	if !exists {
		return fmt.Errorf("no GitHub client found for owner %s", ownerConfig.Owner)
	}

	// Get current running runners (use effective architecture for multi-arch support)
	runners, err := rm.runtime.GetRunningRunners(rm.config.GetEffectiveArchitecture())
	if err != nil {
		return fmt.Errorf("failed to get running runners: %w", err)
	}

	// Count runners for this specific owner only
	ownerRunnerCount := 0
	for _, runner := range runners {
		runnerOwner := rm.getOwnerFromRunnerName(runner.Name)
		if runnerOwner == ownerConfig.Owner {
			ownerRunnerCount++
		}
	}

	// Update global metrics with total count
	rm.metrics.CurrentRunners = len(runners)

	// Check if we can scale up (use owner-specific limits if available)
	maxRunners := ownerConfig.GetEffectiveMaxRunners(rm.config.MaxRunners)

	// Calculate how many runners we can start for THIS OWNER
	availableSlots := maxRunners - ownerRunnerCount
	if availableSlots <= 0 {
		// We're at max capacity, add to queue memory
		jobCount := rm.getQueuedJobCount(githubClient, ownerConfig.Owner, repoName)
		if jobCount > 0 {
			rm.queueMemory.AddPendingRequest(repoFullName, repoName, reason, customLabels, jobCount)
		}
		log.Printf("Cannot scale up for owner %s: already at max runners (%d/%d). %d jobs waiting.", ownerConfig.Owner, ownerRunnerCount, maxRunners, jobCount)
		return fmt.Errorf("max runners reached")
	}

	// Detect required labels if not provided
	var labels []string
	if customLabels != nil && len(customLabels) > 0 {
		labels = customLabels
		log.Printf("Using provided custom labels: %v", labels)
	} else {
		// Try to detect labels from queued jobs
		detectedLabels, err := githubClient.GetQueuedJobLabels(ownerConfig.Owner, repoName)
		if err != nil {
			log.Printf("Failed to detect labels for %s, using default: %v", repoFullName, err)
			labels = rm.config.RunnerLabels
		} else if len(detectedLabels) > 0 {
			labels = detectedLabels
			log.Printf("Detected required labels for %s: %v", repoFullName, labels)
		} else {
			labels = rm.config.RunnerLabels
			log.Printf("No queued jobs found, using default labels for %s: %v", repoFullName, labels)
		}
	}

	// Validate and fix architecture labels
	labels = rm.validateArchitectureLabels(labels)

	// Add owner-specific labels if configured
	if len(ownerConfig.RunnerLabels) > 0 {
		labels = append(labels, ownerConfig.RunnerLabels...)
		log.Printf("Added owner-specific labels for %s: %v", ownerConfig.Owner, ownerConfig.RunnerLabels)
	}

	// Check if there are actually queued jobs before starting runners
	queuedJobCount := rm.getQueuedJobCount(githubClient, ownerConfig.Owner, repoName)
	if queuedJobCount == 0 {
		log.Printf("SCALE UP: Owner %s has %d/%d runners with %d available slots, but no queued jobs found. Skipping scaling.",
			ownerConfig.Owner, ownerRunnerCount, maxRunners, availableSlots)
		return nil
	}

	// Limit runners to the number of queued jobs (don't start more than needed)
	runnersToStart := availableSlots
	if queuedJobCount < availableSlots {
		runnersToStart = queuedJobCount
	}

	// Log scaling decision with owner-specific counts
	log.Printf("SCALE UP: Owner %s has %d/%d runners, can start %d more (%d jobs queued, starting %d runners)",
		ownerConfig.Owner, ownerRunnerCount, maxRunners, availableSlots, queuedJobCount, runnersToStart)

	// Start multiple runners up to the available limit
	startedCount := 0
	for i := 0; i < runnersToStart; i++ {
		runnerName := rm.generateRunnerName(ownerConfig.Owner, repoName)

		log.Printf("Scaling UP: Starting new runner %s for repo %s with labels %v (reason: %s, %d/%d)",
			runnerName, repoFullName, labels, reason, i+1, runnersToStart)

		// Rate limit runner creation - wait for permission to create a runner
		ctx := context.Background()
		if err := rm.runnerLimiter.Wait(ctx); err != nil {
			log.Printf("Rate limiter error for runner %d: %v", i+1, err)
			break
		}

		container, err := rm.runtime.StartRunnerWithLabels(rm.config, runnerName, repoFullName, labels)
		if err != nil {
			log.Printf("Failed to start runner %s: %v", runnerName, err)
			break
		}

		log.Printf("Successfully started runner %s (ID: %s) for repo %s with labels %v", container.Name, container.ID[:12], repoFullName, labels)
		startedCount++

		// Update metrics for each successful start
		rm.metrics.RunnersStarted++
		rm.metrics.ScaleUpEvents++
		rm.metrics.CurrentRunners++
	}

	if startedCount > 0 {
		rm.metrics.LastScaleEvent = time.Now()
		log.Printf("SCALE UP: Successfully started %d runners for %s", startedCount, repoFullName)
	}

	// If we started fewer runners than planned, there might be rate limiting or other issues
	// Check if there are still jobs waiting and add to queue memory for retry
	if startedCount < runnersToStart {
		remainingJobs := queuedJobCount - startedCount
		if remainingJobs > 0 {
			rm.queueMemory.AddPendingRequest(repoFullName, repoName, reason, labels, remainingJobs)
			log.Printf("SCALE UP: Started %d/%d planned runners. %d jobs still waiting (will retry).",
				startedCount, runnersToStart, remainingJobs)
		}
	}

	return nil
}

func (rm *Manager) ScaleDown(reason string) error {
	rm.mu.Lock()
	defer rm.mu.Unlock()

	// Get current running runners (use effective architecture for multi-arch support)
	runners, err := rm.runtime.GetRunningRunners(rm.config.GetEffectiveArchitecture())
	if err != nil {
		return fmt.Errorf("failed to get running runners: %w", err)
	}

	currentCount := len(runners)
	rm.metrics.CurrentRunners = currentCount

	// For webhook-driven mode, we don't enforce minimum runners in scale down
	// Ephemeral runners will exit automatically after completing their jobs

	// For ephemeral runners, we don't forcefully stop them
	// They will exit automatically after completing their jobs
	log.Printf("Scale DOWN: %d runners active (reason: %s)", currentCount, reason)
	log.Printf("Ephemeral runners will exit after completing current jobs")

	// Update metrics
	rm.metrics.ScaleDownEvents++
	rm.metrics.LastScaleEvent = time.Now()

	return nil
}

func (rm *Manager) HandleWorkflowQueued(repoFullName string, repoName string) error {
	return rm.ScaleUp("workflow_queued", repoFullName, repoName)
}

func (rm *Manager) HandleJobQueued(repoFullName string, repoName string, jobLabels []string) error {
	return rm.ScaleUpWithLabels("job_queued", repoFullName, repoName, jobLabels)
}

func (rm *Manager) HandleWorkflowCompleted(repoFullName string, repoName string) error {
	log.Printf("Workflow completed for repo %s - triggering runner cleanup", repoFullName)
	
	// Trigger immediate cleanup of runners for this repository
	go rm.cleanupRunnersForRepository(repoFullName)
	
	return nil
}

func (rm *Manager) GetStatus() map[string]interface{} {
	rm.mu.RLock()
	defer rm.mu.RUnlock()

	runners, err := rm.runtime.GetRunningRunners(rm.config.Architecture)
	if err != nil {
		log.Printf("Failed to get runners for status: %v", err)
		runners = []runtime.RunnerContainer{}
	}

	rm.metrics.CurrentRunners = len(runners)

	// Create owner information
	ownerInfo := make([]map[string]interface{}, 0, len(rm.config.Owners))
	for owner, ownerConfig := range rm.config.Owners {
		info := map[string]interface{}{
			"owner":     owner,
			"all_repos": ownerConfig.AllRepos,
		}
		if !ownerConfig.AllRepos {
			info["repo_name"] = ownerConfig.RepoName
		}
		if ownerConfig.MaxRunners != nil {
			info["max_runners"] = *ownerConfig.MaxRunners
		}
		if len(ownerConfig.RunnerLabels) > 0 {
			info["custom_labels"] = ownerConfig.RunnerLabels
		}
		ownerInfo = append(ownerInfo, info)
	}
	
	var repositoryInfo interface{}
	if len(rm.config.Owners) > 1 {
		repositoryInfo = map[string]interface{}{
			"mode":   "multiple-owners",
			"owners": ownerInfo,
		}
	} else if len(rm.config.Owners) == 1 {
		// Single owner mode
		for owner, ownerConfig := range rm.config.Owners {
			if ownerConfig.AllRepos {
				repositoryInfo = map[string]interface{}{
					"scope": "all",
					"owner": owner,
				}
			} else {
				repositoryInfo = fmt.Sprintf("%s/%s", owner, ownerConfig.RepoName)
			}
			break
		}
	}

	return map[string]interface{}{
		"status": "healthy",
		"config": map[string]interface{}{
			"repository":         repositoryInfo,
			"owner_count":        len(rm.config.Owners),
			"architecture":       rm.config.Architecture,
			"max_runners":        rm.config.MaxRunners,
			"runner_image":       rm.config.RunnerImage,
			"cache_volumes":      rm.config.CacheVolumes,
			"cleanup_images":     rm.config.CleanupImages,
			"keep_intermediate_images": rm.config.KeepIntermediateImages,
			"image_max_age":      rm.config.ImageMaxAge.String(),
			"max_unused_images":  rm.config.MaxUnusedImages,
		},
		"runners": map[string]interface{}{
			"current": len(runners),
			"max":     rm.config.MaxRunners,
			"list":    runners,
		},
		"queue_memory": rm.queueMemory.GetStatus(),
		"metrics": rm.metrics,
	}
}

func (rm *Manager) GetMetrics() *Metrics {
	rm.mu.RLock()
	defer rm.mu.RUnlock()

	// Update current runners count
	runners, err := rm.runtime.GetRunningRunners(rm.config.GetEffectiveArchitecture())
	if err == nil {
		rm.metrics.CurrentRunners = len(runners)
	}

	return rm.metrics
}

func (rm *Manager) StopAllRunners() {
	effectiveArch := rm.config.GetEffectiveArchitecture()
	log.Printf("Stopping all runners for architecture: %s", effectiveArch)
	
	if err := rm.runtime.StopAllRunners(effectiveArch); err != nil {
		log.Printf("Failed to stop all runners: %v", err)
	}
}

func (rm *Manager) StartCleanupRoutine() {
	log.Printf("Starting cleanup routine (runs every 5 minutes)")

	// Skip immediate cleanup on startup to avoid killing active runners
	// when the autoscaler restarts. The first cleanup will run after 5 minutes.
	log.Printf("Skipping immediate cleanup on startup (first cleanup in 5 minutes)")

	ticker := time.NewTicker(5 * time.Minute) // Run cleanup every 5 minutes
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			rm.cleanupOldRunners()
		}
	}
}

func (rm *Manager) cleanupOldRunners() {
	log.Printf("Running cleanup routine for old runners, completed runners, and images")

	// Get runner count before cleanup
	runnersBefore, err := rm.runtime.GetRunningRunners(rm.config.GetEffectiveArchitecture())
	countBefore := 0
	if err == nil {
		countBefore = len(runnersBefore)
	}

	// Clean up completed runners first (immediate cleanup)
	if err := rm.runtime.CleanupCompletedRunners(rm.config.GetEffectiveArchitecture()); err != nil {
		log.Printf("Failed to cleanup completed runners: %v", err)
	}

	// Clean up old runner containers based on timeout
	if err := rm.runtime.CleanupOldRunners(rm.config.GetEffectiveArchitecture(), rm.config.RunnerTimeout); err != nil {
		log.Printf("Failed to cleanup old runners: %v", err)
	}

	// Clean up unused images (Docker-specific, may be no-op for other runtimes)
	if err := rm.runtime.CleanupImages(rm.config); err != nil {
		log.Printf("Failed to cleanup images: %v", err)
	}

	// Check if any runners were cleaned up, creating capacity for pending requests
	runnersAfter, err := rm.runtime.GetRunningRunners(rm.config.GetEffectiveArchitecture())
	countAfter := 0
	if err == nil {
		countAfter = len(runnersAfter)
	}

	// If runners were cleaned up, process pending scaling requests
	if countAfter < countBefore {
		log.Printf("CLEANUP: %d runners cleaned up (%d -> %d), checking for pending scaling requests",
			countBefore-countAfter, countBefore, countAfter)

		// Process pending requests that might now have capacity
		go rm.ProcessPendingRequests()
	}
}

// cleanupRunnersForRepository immediately cleans up runners for a specific repository
func (rm *Manager) cleanupRunnersForRepository(repoFullName string) {
	log.Printf("Cleaning up runners for repository: %s", repoFullName)

	runners, err := rm.runtime.GetRunningRunners(rm.config.GetEffectiveArchitecture())
	if err != nil {
		log.Printf("Failed to get running runners for cleanup: %v", err)
		return
	}

	cleanedCount := 0
	for _, runner := range runners {
		// Check if this runner belongs to the completed workflow's repository
		runnerRepo, err := rm.runtime.GetRunnerRepository(runner.ID)
		if err != nil {
			log.Printf("Failed to get repository for runner %s: %v", runner.Name, err)
			continue
		}

		if runnerRepo == repoFullName {
			log.Printf("Cleaning up runner %s for completed workflow in %s", runner.Name, repoFullName)
			if err := rm.runtime.StopRunner(runner.ID); err != nil {
				log.Printf("Failed to stop runner %s: %v", runner.Name, err)
			} else {
				cleanedCount++
			}
		}
	}

	if cleanedCount > 0 {
		log.Printf("Successfully cleaned up %d runners for repository %s", cleanedCount, repoFullName)

		// If runners were cleaned up, process pending scaling requests that might now have capacity
		log.Printf("WORKFLOW CLEANUP: %d runners cleaned up for %s, checking for pending scaling requests",
			cleanedCount, repoFullName)

		// Process pending requests asynchronously to avoid blocking the webhook processing
		go rm.ProcessPendingRequests()
	} else {
		log.Printf("No runners found to clean up for repository %s", repoFullName)
	}
}

func (rm *Manager) generateRunnerName(owner, repoName string) string {
	// Use shorter timestamp (last 8 digits) + random number for uniqueness
	timestamp := time.Now().Unix()
	shortTimestamp := timestamp % 100000000  // Last 8 digits for uniqueness
	randomSuffix := rand.Intn(999)  // 0-999 random number (3 digits max)
	
	if owner != "" && repoName != "" {
		// Calculate available space for owner and repo
		// Format: runner-{owner}-{repo}-{timestamp}-{rand}-{arch}
		// Fixed parts: "runner-" (7) + 2 separators (2) + timestamp (8) + "-" (1) + rand (3) + "-" (1) + arch
		
		archLen := len(rm.config.Architecture)
		fixedLen := 7 + 2 + 8 + 1 + 3 + 1 + archLen  // Total fixed length
		availableLen := 63 - fixedLen - 1  // -1 for separator between owner and repo, use 63 not 64
		
		// More aggressive truncation - favor repo name for identification
		maxOwnerLen := 6  // Fixed short owner length
		maxRepoLen := availableLen - maxOwnerLen
		
		if maxRepoLen < 8 {  // Ensure minimum repo identification
			maxRepoLen = 8
			maxOwnerLen = availableLen - maxRepoLen
			if maxOwnerLen < 3 {
				maxOwnerLen = 3
			}
		}
		
		// Truncate owner and repo names
		truncatedOwner := owner
		if len(owner) > maxOwnerLen {
			truncatedOwner = owner[:maxOwnerLen]
		}
		
		truncatedRepoName := repoName
		if len(repoName) > maxRepoLen {
			// For very long repo names, take first part + last part for better identification
			if maxRepoLen > 6 {
				halfLen := maxRepoLen / 2
				truncatedRepoName = repoName[:halfLen] + repoName[len(repoName)-(maxRepoLen-halfLen):]
			} else {
				truncatedRepoName = repoName[:maxRepoLen]
			}
		}
		
		// Convert to lowercase for Kubernetes compatibility
		truncatedOwner = strings.ToLower(truncatedOwner)
		truncatedRepoName = strings.ToLower(truncatedRepoName)
		arch := strings.ToLower(rm.config.Architecture)
		
		result := fmt.Sprintf("runner-%s-%s-%d-%03d-%s", truncatedOwner, truncatedRepoName, shortTimestamp, randomSuffix, arch)
		
		// Final safety check - if still too long, use fallback
		if len(result) > 63 {
			return fmt.Sprintf("runner-%d-%03d-%s", shortTimestamp, randomSuffix, arch)
		}
		
		return result
	} else if repoName != "" {
		arch := strings.ToLower(rm.config.Architecture)
		maxRepoLen := 63 - 7 - 1 - 8 - 1 - 3 - 1 - len(arch) // Calculate max repo length
		
		if len(repoName) > maxRepoLen {
			repoName = repoName[:maxRepoLen]
		}
		
		repoName = strings.ToLower(repoName)
		return fmt.Sprintf("runner-%s-%d-%03d-%s", repoName, shortTimestamp, randomSuffix, arch)
	}
	
	arch := strings.ToLower(rm.config.Architecture)
	return fmt.Sprintf("runner-%d-%03d-%s", shortTimestamp, randomSuffix, arch)
}

func (rm *Manager) IncrementWebhookCount() {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	rm.metrics.WebhooksReceived++
}

// ScaleUpForJob scales up runners for a specific job using its labels
func (rm *Manager) ScaleUpForJob(job github.WorkflowJob, repoFullName string) error {
	// Extract repository name from full name
	parts := strings.Split(repoFullName, "/")
	var repoName string
	if len(parts) == 2 {
		repoName = parts[1]
	} else {
		repoName = repoFullName
	}

	// Log job details for debugging
	log.Printf("AGGRESSIVE MONITOR: Scaling up runner for job %d (%s) with labels: %v",
		job.ID, job.Name, job.Labels)

	// Scale up using the job's labels
	reason := fmt.Sprintf("aggressive-monitor-job-%d", job.ID)
	return rm.ScaleUpWithLabels(reason, repoFullName, repoName, job.Labels)
}



// getOwnerFromRunnerName extracts owner name from runner name
func (rm *Manager) getOwnerFromRunnerName(runnerName string) string {
	// Runner names are in format: "runner-owner-reponame-timestamp-rand-arch"
	if parts := strings.Split(runnerName, "-"); len(parts) >= 6 {
		truncatedOwner := parts[1] // Owner is the second part (possibly truncated)
		
		// Find matching full owner name from config
		for _, ownerConfig := range rm.config.Owners {
			if strings.HasPrefix(ownerConfig.Owner, truncatedOwner) {
				return ownerConfig.Owner // Return full owner name
			}
		}
		
		// If no prefix match, return the truncated name (better than nothing)
		return truncatedOwner
	}
	// Fallback for old format: "runner-reponame-timestamp-id-arch" 
	if parts := strings.Split(runnerName, "-"); len(parts) >= 4 {
		repoName := parts[1]
		log.Printf("Runner %s uses old naming format, attempting repository lookup for repo %s", runnerName, repoName)
		
		// Get all repositories and find which owner this repo belongs to
		repos, err := rm.GetRepositories()
		if err == nil {
			for _, repo := range repos {
				// Extract repo name from full name (owner/repo)
				if repoParts := strings.Split(repo.FullName, "/"); len(repoParts) == 2 {
					if repoParts[1] == repoName || strings.HasPrefix(repoParts[1], repoName) {
						return repoParts[0] // Return the owner
					}
				}
			}
		}
		
		log.Printf("Could not determine owner for runner %s with repo %s", runnerName, repoName)
	}
	return ""
}

// getOwnerFromRepoName extracts owner from repository full name
func (rm *Manager) getOwnerFromRepoName(repoFullName string) string {
	if parts := strings.Split(repoFullName, "/"); len(parts) >= 2 {
		return parts[0]
	}
	return ""
}








// GetRunnerDistribution returns information about how runners are distributed across repositories
func (rm *Manager) GetRunnerDistribution() (map[string]int, error) {
	runners, err := rm.runtime.GetRunningRunners(rm.config.GetEffectiveArchitecture())
	if err != nil {
		return nil, fmt.Errorf("failed to get running runners: %w", err)
	}

	distribution := make(map[string]int)
	
	for _, runner := range runners {
		repoFullName, err := rm.runtime.GetRunnerRepository(runner.ID)
		if err != nil {
			log.Printf("Failed to get repository for runner %s: %v", runner.Name, err)
			distribution["unknown"]++
			continue
		}
		distribution[repoFullName]++
	}

	return distribution, nil
}






// GetRepositories returns the list of repositories for the configured owner
func (rm *Manager) GetRepositories() ([]github.Repository, error) {
	allRepos := []github.Repository{}
	
	log.Printf("DEBUG: GetRepositories() started for %d owners", len(rm.config.Owners))
	
	// Get repositories for all configured owners
	for owner, ownerConfig := range rm.config.Owners {
		log.Printf("DEBUG: Getting repositories for owner: %s", owner)
		githubClient, exists := rm.githubClients[owner]
		if !exists {
			log.Printf("No GitHub client found for owner %s", owner)
			continue
		}
		
		log.Printf("DEBUG: About to call githubClient.GetRepositories() for owner: %s", owner)
		repos, err := githubClient.GetRepositories(owner)
		log.Printf("DEBUG: Finished githubClient.GetRepositories() for owner %s, got %d repos, error: %v", owner, len(repos), err)
		if err != nil {
			log.Printf("Failed to get repositories for owner %s: %v", owner, err)
			continue
		}
		
		// Filter repositories based on owner configuration
		for _, repo := range repos {
			if ownerConfig.IsRepoAllowed(repo.FullName) {
				allRepos = append(allRepos, repo)
			}
		}
	}
	
	return allRepos, nil
}



// validateArchitectureLabels ensures that architecture labels match the host architecture
func (rm *Manager) validateArchitectureLabels(labels []string) []string {
	// For Kubernetes runtime, support multi-architecture scheduling
	// Don't filter architecture labels since pods can be scheduled on different node architectures
	if rm.config.Runtime == config.RuntimeTypeKubernetes {
		log.Printf("Kubernetes runtime: preserving all architecture labels for multi-arch scheduling")
		return labels
	}
	
	// For Docker runtime, validate against host architecture
	hostArch := rm.config.Architecture
	var validatedLabels []string
	hasValidArch := false
	
	// Common architecture label mappings
	archLabels := map[string]bool{
		"x64":   true,
		"amd64": true,
		"x86_64": true,
		"arm64": true,
		"aarch64": true,
	}
	
	for _, label := range labels {
		// Handle architecture labels
		if archLabels[label] {
			// Check if this architecture label is compatible with host
			if hostArch == "amd64" && (label == "amd64" || label == "x64" || label == "x86_64") {
				// For amd64 hosts, keep both amd64 and x64 labels
				validatedLabels = append(validatedLabels, label)
				hasValidArch = true
			} else if hostArch == "arm64" && (label == "arm64" || label == "aarch64") {
				// For arm64 hosts, keep arm64 labels
				validatedLabels = append(validatedLabels, label)
				hasValidArch = true
			} else {
				log.Printf("WARNING: Skipping mismatched architecture label '%s' (host is %s)", label, hostArch)
			}
		} else {
			// Keep non-architecture labels
			validatedLabels = append(validatedLabels, label)
		}
	}
	
	// Ensure we have the correct architecture labels (for Docker runtime only)
	if !hasValidArch {
		if hostArch == "amd64" {
			// For amd64, add both amd64 and x64 labels
			validatedLabels = append(validatedLabels, "amd64", "x64")
			log.Printf("Added host architecture labels: amd64, x64")
		} else {
			validatedLabels = append(validatedLabels, hostArch)
			log.Printf("Added host architecture label: %s", hostArch)
		}
	}
	
	return validatedLabels
}

// getQueuedJobCount estimates the number of queued jobs for a repository
func (rm *Manager) getQueuedJobCount(githubClient *github.Client, owner, repoName string) int {
	// Try to get queued workflow runs
	runs, err := githubClient.GetQueuedWorkflowRuns(owner, repoName)
	if err != nil {
		log.Printf("Failed to get queued workflow runs for %s/%s: %v", owner, repoName, err)
		return 0
	}

	jobCount := 0
	for _, run := range runs.WorkflowRuns {
		// Get jobs for this workflow run
		jobs, err := githubClient.GetWorkflowJobs(owner, repoName, run.ID)
		if err != nil {
			log.Printf("Failed to get jobs for workflow run %d: %v", run.ID, err)
			continue
		}

		// Count jobs that need self-hosted runners
		for _, job := range jobs.Jobs {
			if job.Status == "queued" || job.Status == "waiting" || job.Status == "in_progress" {
				requiresSelfHosted := false
				for _, label := range job.Labels {
					if label == "self-hosted" {
						requiresSelfHosted = true
						break
					}
				}
				if requiresSelfHosted {
					jobCount++
				}
			}
		}
	}

	return jobCount
}

// ProcessPendingRequests checks for pending scaling requests when capacity becomes available
func (rm *Manager) ProcessPendingRequests() {
	pendingRequests := rm.queueMemory.GetPendingRequests()
	if len(pendingRequests) == 0 {
		return
	}

	log.Printf("QUEUE MEMORY: Processing %d pending scaling requests", len(pendingRequests))

	for _, request := range pendingRequests {
		// Check if this request should be retried
		if !rm.queueMemory.ShouldRetry(request.RepoFullName) {
			continue
		}

		// Try to scale up again
		log.Printf("QUEUE MEMORY: Retrying pending request for %s (attempt %d)", request.RepoFullName, request.RetryCount+1)
		rm.queueMemory.UpdateRetryCount(request.RepoFullName)

		// Remove the request temporarily to avoid duplicate processing
		rm.queueMemory.RemovePendingRequest(request.RepoFullName)

		// Attempt to scale up
		err := rm.ScaleUpWithLabels(
			fmt.Sprintf("queue-retry-%d", request.RetryCount),
			request.RepoFullName,
			request.RepoName,
			request.Labels,
		)

		if err != nil {
			// If scaling still fails (likely still at max capacity), add back to queue
			if err.Error() == "max runners reached" {
				rm.queueMemory.AddPendingRequest(request.RepoFullName, request.RepoName, request.Reason, request.Labels, request.JobCount)
			} else {
				log.Printf("QUEUE MEMORY: Failed to retry scaling for %s: %v", request.RepoFullName, err)
			}
		}
	}
} 
