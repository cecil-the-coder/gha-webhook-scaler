package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/joho/godotenv"
	
	"github.com/cecil-the-coder/gha-webhook-scaler/internal/config"
	"github.com/cecil-the-coder/gha-webhook-scaler/internal/github"
	"github.com/cecil-the-coder/gha-webhook-scaler/internal/runtime"
	"github.com/cecil-the-coder/gha-webhook-scaler/internal/runner"
	"github.com/cecil-the-coder/gha-webhook-scaler/internal/scanner"
	"github.com/cecil-the-coder/gha-webhook-scaler/internal/webhook"
)

// Build-time variables set by ldflags
var (
	Version   = "dev"
	Revision  = "unknown"
	BuildTime = "unknown"
)

// Application holds the application state and dependencies
type Application struct {
	Config           *config.Config
	GitHubClients    map[string]*github.Client
	Runtime          runtime.ContainerRuntime
	RunnerManager    *runner.Manager
	WebhookHandler   *webhook.Handler
	WebhookEndpoints *webhook.Endpoints
	JobScanner       *scanner.JobScanner
	WorkflowMonitor  *webhook.WorkflowMonitor
	Server           *http.Server
}

// NewApplication creates a new application instance with all dependencies initialized
func NewApplication() (*Application, error) {
	// Load environment variables
	if err := godotenv.Load(); err != nil {
		log.Println("No .env file found, using system environment variables")
	}

	// Initialize configuration
	cfg := config.NewConfig()
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("configuration error: %w", err)
	}

	// Initialize services
	githubClients := make(map[string]*github.Client)
	for owner, ownerConfig := range cfg.Owners {
		githubClients[owner] = github.NewClient(ownerConfig.GitHubToken)
	}
	
	// Initialize the container runtime based on configuration
	rt, err := runtime.NewContainerRuntime(cfg.Runtime, cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize %s runtime: %w", cfg.Runtime, err)
	}
	
	// Verify runtime is accessible
	if err := rt.HealthCheck(); err != nil {
		return nil, fmt.Errorf("runtime health check failed for %s: %w", cfg.Runtime, err)
	}
	
	runnerManager := runner.NewManager(cfg, githubClients, rt)
	webhookHandler := webhook.NewHandler(cfg, runnerManager)
	webhookEndpoints := webhook.NewEndpoints(cfg, githubClients)
	jobScanner := scanner.NewJobScanner(cfg, githubClients, runnerManager)
	workflowMonitor := webhook.NewWorkflowMonitor(cfg, githubClients, runnerManager)

	// Set the workflow monitor in the webhook handler after all dependencies are ready
	webhookHandler.SetWorkflowMonitor(workflowMonitor)

	app := &Application{
		Config:           cfg,
		GitHubClients:    githubClients,
		Runtime:          rt,
		RunnerManager:    runnerManager,
		WebhookHandler:   webhookHandler,
		WebhookEndpoints: webhookEndpoints,
		JobScanner:       jobScanner,
		WorkflowMonitor:  workflowMonitor,
	}

	return app, nil
}

// SetupRouter configures and returns the HTTP router
func (app *Application) SetupRouter() *gin.Engine {
	// Setup HTTP server
	if !app.Config.Debug {
		gin.SetMode(gin.ReleaseMode)
	}
	
	router := gin.Default()
	
	// Health check endpoint
	router.GET("/health", app.healthHandler)

	// Webhook endpoint
	router.POST("/webhook", app.WebhookHandler.HandleWebhook)

	// Metrics endpoint
	router.GET("/metrics", app.metricsHandler)

	return router
}

// healthHandler handles the /health endpoint
func (app *Application) healthHandler(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"status": "healthy",
		"time":   time.Now().UTC(),
	})
}

// versionHandler handles the /version endpoint
func (app *Application) versionHandler(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"version":    Version,
		"revision":   Revision,
		"build_time": BuildTime,
		"runtime":    app.Config.Runtime,
	})
}

// metricsHandler handles the /metrics endpoint with both webhook and scanner metrics
func (app *Application) metricsHandler(c *gin.Context) {
	// Get runner metrics (same as webhook handler does)
	runnerMetrics := app.RunnerManager.GetMetrics()
	
	// Get scanner metrics
	scannerMetrics := app.JobScanner.GetMetrics()
	
	// Combine all metrics (same structure as webhook handler)
	response := gin.H{
		"metrics": gin.H{
			"runner": runnerMetrics,
			"scanner": gin.H{
				"enabled":               app.Config.JobScanInterval > 0,
				"scan_interval_seconds": int(app.Config.JobScanInterval.Seconds()),
				"total_scans":           scannerMetrics.TotalScans,
				"last_scan_duration_ms": int(scannerMetrics.LastScanDuration.Milliseconds()),
				"repos_scanned":         scannerMetrics.ReposScanned,
				"queued_jobs_found":     scannerMetrics.QueuedJobsFound,
				"runners_created":       scannerMetrics.RunnersCreated,
				"api_calls_used":        scannerMetrics.APICallsUsed,
				"last_error":            scannerMetrics.LastError,
				"last_scan_time":        scannerMetrics.LastScanTime,
			},
		},
	}
	
	c.JSON(http.StatusOK, response)
}

// Start starts the application server and background services
func (app *Application) Start() error {
	router := app.SetupRouter()

	app.Server = &http.Server{
		Addr:    fmt.Sprintf(":%d", app.Config.Port),
		Handler: router,
	}

	// Start background cleanup routine for all runtimes
	go app.RunnerManager.StartCleanupRoutine()

	// Start job scanning routine
	go app.JobScanner.StartScanning()

	// Start aggressive workflow monitoring
	go app.WorkflowMonitor.Start()

	// Log startup information
	app.logStartupInfo()

	// Start server in a goroutine
	go func() {
		if err := app.Server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Failed to start server: %v", err)
		}
	}()

	// Setup webhooks for configured owners (async after server starts)
	go func() {
		// Small delay to ensure server is ready
		time.Sleep(2 * time.Second)
		if err := app.setupWebhooks(); err != nil {
			log.Printf("Warning: Failed to setup webhooks: %v", err)
		}
	}()

	return nil
}

// setupWebhooks automatically sets up webhooks for all configured owners
func (app *Application) setupWebhooks() error {
	// Check if webhook URL is configured
	webhookURL := os.Getenv("WEBHOOK_URL")
	if webhookURL == "" {
		log.Printf("No WEBHOOK_URL configured, skipping automatic webhook setup")
		return nil
	}

	log.Printf("Setting up webhooks with URL: %s", webhookURL)

	// Process each configured owner
	for ownerName, ownerConfig := range app.Config.Owners {
		log.Printf("Setting up webhooks for owner: %s", ownerName)
		
		// Create webhook manager with the owner's GitHub client
		githubClient := github.NewClient(ownerConfig.GitHubToken)
		webhookManager := webhook.NewWebhookManager(githubClient, app.Config)
		
		// Setup webhooks using the auto setup function
		if err := webhookManager.AutoSetupWebhookForOwner(ownerName, webhookURL); err != nil {
			log.Printf("Failed to setup webhooks for owner %s: %v", ownerName, err)
			continue
		}
		
		log.Printf("Successfully setup webhooks for owner: %s", ownerName)
	}

	return nil
}

// logStartupInfo logs application startup information
func (app *Application) logStartupInfo() {
	log.Printf("Starting GitHub Actions Auto-Scaler v%s (revision: %s, built: %s)", Version, Revision, BuildTime)
	log.Printf("Server starting on port %d", app.Config.Port)
	log.Printf("Container runtime: %s", app.Config.Runtime)

	// Log owner configuration
	log.Printf("Configured owners: %d", len(app.Config.Owners))
	for ownerName, ownerConfig := range app.Config.Owners {
		if ownerConfig.AllRepos {
			log.Printf("  - %s: All repositories", ownerName)
		} else {
			log.Printf("  - %s: %s/%s", ownerName, ownerName, ownerConfig.RepoName)
		}
		if ownerConfig.MaxRunners != nil {
			log.Printf("    Max runners: %d", *ownerConfig.MaxRunners)
		}
	}

	log.Printf("Host architecture: %s", app.Config.Architecture)
	log.Printf("Effective architecture: %s", app.Config.GetEffectiveArchitecture())
	if app.Config.Runtime == config.RuntimeTypeKubernetes {
		log.Printf("Kubernetes mode: Multi-architecture scheduling enabled")
		if app.Config.CacheVolumes && app.Config.KubernetesPVCs {
			log.Printf("Kubernetes caching: PersistentVolumeClaims enabled")
		} else if app.Config.CacheVolumes {
			log.Printf("Kubernetes caching: Disabled (PVCs required for ephemeral runners)")
		} else {
			log.Printf("Kubernetes caching: Disabled")
		}
	}
	log.Printf("Max runners: %d (webhook-driven only)", app.Config.MaxRunners)

	if app.Config.JobScanInterval > 0 {
		log.Printf("Job scanning: enabled (%v interval)", app.Config.JobScanInterval)
	} else {
		log.Printf("Job scanning: disabled")
	}
}

// Shutdown gracefully shuts down the application
func (app *Application) Shutdown(ctx context.Context) error {
	log.Println("Shutting down auto-scaler...")

	// Stop workflow monitoring
	app.WorkflowMonitor.Stop()

	// Stop all runners
	app.RunnerManager.StopAllRunners()

	// Shutdown server
	if err := app.Server.Shutdown(ctx); err != nil {
		return fmt.Errorf("server forced to shutdown: %w", err)
	}

	return nil
}

func main() {
	// Create application instance
	app, err := NewApplication()
	if err != nil {
		log.Fatalf("Failed to initialize application: %v", err)
	}

	// Start the application
	if err := app.Start(); err != nil {
		log.Fatalf("Failed to start application: %v", err)
	}

	// Wait for interrupt signal to gracefully shutdown
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	// Graceful shutdown
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := app.Shutdown(ctx); err != nil {
		log.Printf("Application shutdown error: %v", err)
	}

	log.Println("Auto-scaler stopped")
} 
