package runner

import (
	"log"
	"sync"
	"time"
)

// QueueMemory tracks pending job scaling requests when max runners limit is reached
type QueueMemory struct {
	mu sync.RWMutex

	// Track pending scaling requests
	pendingRequests map[string]*PendingScaleRequest // key: repoFullName
}

// PendingScaleRequest represents a scaling request that couldn't be fulfilled
type PendingScaleRequest struct {
	RepoFullName   string
	RepoName       string
	Reason         string
	Labels         []string
	RequestedAt    time.Time
	JobCount       int       // Number of jobs that need runners
	RetryCount     int       // Number of retry attempts
	LastRetryAt    time.Time
}

// NewQueueMemory creates a new queue memory system
func NewQueueMemory() *QueueMemory {
	return &QueueMemory{
		pendingRequests: make(map[string]*PendingScaleRequest),
	}
}

// AddPendingRequest adds a pending scaling request when max runners is reached
func (qm *QueueMemory) AddPendingRequest(repoFullName, repoName, reason string, labels []string, jobCount int) {
	qm.mu.Lock()
	defer qm.mu.Unlock()

	request := &PendingScaleRequest{
		RepoFullName: repoFullName,
		RepoName:     repoName,
		Reason:       reason,
		Labels:       labels,
		RequestedAt:  time.Now(),
		JobCount:     jobCount,
		RetryCount:   0,
		LastRetryAt:  time.Now(),
	}

	qm.pendingRequests[repoFullName] = request
	log.Printf("QUEUE MEMORY: Added pending scaling request for %s (%d jobs waiting)", repoFullName, jobCount)
}

// RemovePendingRequest removes a pending request when it's fulfilled or no longer needed
func (qm *QueueMemory) RemovePendingRequest(repoFullName string) {
	qm.mu.Lock()
	defer qm.mu.Unlock()

	if _, exists := qm.pendingRequests[repoFullName]; exists {
		delete(qm.pendingRequests, repoFullName)
		log.Printf("QUEUE MEMORY: Removed pending request for %s", repoFullName)
	}
}

// GetPendingRequests returns all pending scaling requests
func (qm *QueueMemory) GetPendingRequests() []*PendingScaleRequest {
	qm.mu.RLock()
	defer qm.mu.RUnlock()

	requests := make([]*PendingScaleRequest, 0, len(qm.pendingRequests))
	for _, req := range qm.pendingRequests {
		requests = append(requests, req)
	}

	return requests
}

// ShouldRetry checks if a pending request should be retried
func (qm *QueueMemory) ShouldRetry(repoFullName string) bool {
	qm.mu.RLock()
	defer qm.mu.RUnlock()

	request, exists := qm.pendingRequests[repoFullName]
	if !exists {
		return false
	}

	// Don't retry too frequently
	timeSinceLastRetry := time.Since(request.LastRetryAt)
	if timeSinceLastRetry < 30*time.Second {
		return false
	}

	// Max retry attempts (5 retries over ~2.5 minutes)
	if request.RetryCount >= 5 {
		log.Printf("QUEUE MEMORY: Max retries reached for %s, removing request", repoFullName)
		return false
	}

	return true
}

// UpdateRetryCount updates the retry count for a pending request
func (qm *QueueMemory) UpdateRetryCount(repoFullName string) {
	qm.mu.Lock()
	defer qm.mu.Unlock()

	if request, exists := qm.pendingRequests[repoFullName]; exists {
		request.RetryCount++
		request.LastRetryAt = time.Now()
		log.Printf("QUEUE MEMORY: Updated retry count for %s (attempt %d/5)", repoFullName, request.RetryCount)
	}
}

// GetStatus returns the current status of the queue memory
func (qm *QueueMemory) GetStatus() map[string]interface{} {
	qm.mu.RLock()
	defer qm.mu.RUnlock()

	pending := make([]map[string]interface{}, 0, len(qm.pendingRequests))
	for key, req := range qm.pendingRequests {
		pending = append(pending, map[string]interface{}{
			"repo_full_name": key,
			"repo_name":      req.RepoName,
			"reason":         req.Reason,
			"job_count":      req.JobCount,
			"requested_at":   req.RequestedAt,
			"retry_count":    req.RetryCount,
			"last_retry_at":  req.LastRetryAt,
			"waiting_time":   time.Since(req.RequestedAt).String(),
		})
	}

	return map[string]interface{}{
		"pending_requests_count": len(qm.pendingRequests),
		"pending_requests":     pending,
	}
}