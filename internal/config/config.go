package config

import (
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// RuntimeType represents the type of container runtime
type RuntimeType string

const (
	RuntimeTypeDocker     RuntimeType = "docker"
	RuntimeTypeKubernetes RuntimeType = "kubernetes"
)

type Config struct {
	// Multi-owner support
	MultiOwnerMode bool
	Owners         map[string]*OwnerConfig // key is owner name
	
	// Legacy single-owner configuration (backwards compatibility)
	GitHubToken   string
	RepoOwner     string
	RepoName      string
	AllRepos      bool
	WebhookSecret string

	// Runner configuration (global defaults)
	MaxRunners      int
	RunnerTimeout   time.Duration
	Architecture    string
	RunnerLabels    []string
	RunnerImage     string

	// Server configuration
	Port  int
	Debug bool

	// Runtime configuration
	Runtime       RuntimeType
	
	// Docker configuration
	DockerNetwork string
	CacheVolumes  bool
	
	// Kubernetes configuration
	KubernetesPVCs       bool
	KubernetesNamespace  string

	// Image cleanup configuration
	CleanupImages     bool
	KeepIntermediateImages bool
	ImageMaxAge       time.Duration
	MaxUnusedImages   int

	// Job scanning configuration
	JobScanInterval   time.Duration // 0 disables scanning

}

func NewConfig() *Config {
	config := &Config{
		// Check if multi-owner mode is enabled
		MultiOwnerMode: getEnvBool("MULTI_OWNER_MODE", false),
		Owners:         make(map[string]*OwnerConfig),
		
		// Legacy single-owner configuration
		GitHubToken:   getEnv("GITHUB_TOKEN", ""),
		RepoOwner:     getEnv("REPO_OWNER", ""),
		RepoName:      getEnv("REPO_NAME", ""),
		AllRepos:      getEnvBool("ALL_REPOS", false),
		WebhookSecret: getEnv("WEBHOOK_SECRET", ""),

		// Global runner defaults
		MaxRunners:    getEnvInt("MAX_RUNNERS", 5),
		RunnerTimeout: time.Duration(getEnvInt("RUNNER_TIMEOUT", 3600)) * time.Second,
		Architecture:  detectArchitecture(),
		RunnerImage:   getEnv("RUNNER_IMAGE", "myoung34/github-runner:ubuntu-noble"),

		Port:  getEnvInt("PORT", 8080),
		Debug: getEnvBool("DEBUG", false),

		Runtime:       parseRuntimeType(getEnv("RUNTIME", "docker")),
		
		DockerNetwork: getEnv("DOCKER_NETWORK", ""),
		CacheVolumes:  getEnvBool("CACHE_VOLUMES", true),
		
		KubernetesPVCs:      getEnvBool("KUBERNETES_PVCS", false),
		KubernetesNamespace: getEnv("KUBERNETES_NAMESPACE", "github-runners"),

		CleanupImages:          getEnvBool("CLEANUP_IMAGES", true),
		KeepIntermediateImages: getEnvBool("KEEP_INTERMEDIATE_IMAGES", true),
		ImageMaxAge:            time.Duration(getEnvInt("IMAGE_MAX_AGE_HOURS", 24)) * time.Hour,
		MaxUnusedImages:        getEnvInt("MAX_UNUSED_IMAGES", 10),

		JobScanInterval:        time.Duration(getEnvInt("JOB_SCAN_INTERVAL_SECONDS", 0)) * time.Second,

	}

	// Set runner labels based on architecture
	config.RunnerLabels = config.getRunnerLabels()

	// Load multi-owner configuration if enabled
	if config.MultiOwnerMode {
		config.loadMultiOwnerConfig()
	} else {
		// Create single owner config from legacy settings
		if config.GitHubToken != "" && config.RepoOwner != "" {
			ownerConfig := &OwnerConfig{
				Owner:         config.RepoOwner,
				GitHubToken:   config.GitHubToken,
				WebhookSecret: config.WebhookSecret,
				AllRepos:      config.AllRepos,
				RepoName:      config.RepoName,
			}
			config.Owners[config.RepoOwner] = ownerConfig
		}
	}

	return config
}

func (c *Config) loadMultiOwnerConfig() {
	// Load owners from OWNERS environment variable (comma-separated list)
	ownersList := getEnvSlice("OWNERS", []string{})
	
	fmt.Printf("DEBUG: Multi-owner mode: %t, OWNERS list: %v\n", c.MultiOwnerMode, ownersList)
	
	for _, owner := range ownersList {
		owner = strings.TrimSpace(owner)
		if owner == "" {
			continue
		}
		
		// Load owner-specific configuration using prefixed env vars
		prefix := fmt.Sprintf("OWNER_%s_", strings.ToUpper(strings.ReplaceAll(owner, "-", "_")))
		
		ownerConfig := &OwnerConfig{
			Owner:         owner,
			GitHubToken:   getEnv(prefix+"GITHUB_TOKEN", ""),
			WebhookSecret: getEnv(prefix+"WEBHOOK_SECRET", c.WebhookSecret), // Fall back to global
			AllRepos:      getEnvBool(prefix+"ALL_REPOS", false),
			RepoName:      getEnv(prefix+"REPO_NAME", ""),
		}
		
		// Optional per-owner runner limits
		if maxRunners := getEnvInt(prefix+"MAX_RUNNERS", -1); maxRunners > 0 {
			ownerConfig.MaxRunners = &maxRunners
		}
		
		// Optional per-owner labels
		if labels := getEnv(prefix+"LABELS", ""); labels != "" {
			ownerConfig.RunnerLabels = strings.Split(labels, ",")
			for i := range ownerConfig.RunnerLabels {
				ownerConfig.RunnerLabels[i] = strings.TrimSpace(ownerConfig.RunnerLabels[i])
			}
		}
		
		c.Owners[owner] = ownerConfig
		fmt.Printf("DEBUG: Loaded owner: %s, AllRepos: %t\n", owner, ownerConfig.AllRepos)
	}
}

func (c *Config) Validate() error {
	// Validate multi-owner or single-owner configuration
	if c.MultiOwnerMode {
		if len(c.Owners) == 0 {
			return fmt.Errorf("OWNERS list is required in multi-owner mode")
		}
		
		// Validate each owner configuration
		for owner, ownerConfig := range c.Owners {
			if err := ownerConfig.Validate(); err != nil {
				return fmt.Errorf("invalid configuration for owner %s: %w", owner, err)
			}
		}
	} else {
		// Validate legacy single-owner mode
		if len(c.Owners) == 0 {
			return fmt.Errorf("GITHUB_TOKEN and REPO_OWNER are required")
		}
		
		// Validate the single owner config
		for _, ownerConfig := range c.Owners {
			if err := ownerConfig.Validate(); err != nil {
				return err
			}
		}
	}
	
	// Validate global settings
	if c.MaxRunners <= 0 {
		return fmt.Errorf("MAX_RUNNERS must be greater than 0")
	}
	if c.Port <= 0 || c.Port > 65535 {
		return fmt.Errorf("PORT must be between 1 and 65535")
	}
	
	// Validate job scan interval (warn if too frequent)
	if c.JobScanInterval > 0 && c.JobScanInterval < 30*time.Second {
		fmt.Printf("WARNING: JOB_SCAN_INTERVAL_SECONDS is very frequent (%v), this may consume rate limit quickly\n", c.JobScanInterval)
	}
	
	return nil
}

func (c *Config) getRunnerLabels() []string {
	customLabels := getEnv("LABELS", "")
	
	// Use a map to track unique labels
	labelMap := make(map[string]bool)
	
	// Add base labels
	baseLabels := []string{"self-hosted", "linux", "docker"}
	for _, label := range baseLabels {
		labelMap[label] = true
	}

	// Add architecture-specific labels
	switch c.Architecture {
	case "amd64":
		labelMap["amd64"] = true
		labelMap["x64"] = true
	case "arm64":
		labelMap["arm64"] = true
	}

	// Add custom labels if provided
	if customLabels != "" {
		for _, label := range strings.Split(customLabels, ",") {
			label = strings.TrimSpace(label)
			if label != "" {
				labelMap[label] = true
			}
		}
	}

	// Convert map back to slice
	uniqueLabels := make([]string, 0, len(labelMap))
	for label := range labelMap {
		uniqueLabels = append(uniqueLabels, label)
	}

	return uniqueLabels
}

func (c *Config) GetRepoURL() string {
	if c.AllRepos {
		return fmt.Sprintf("https://github.com/%s", c.RepoOwner)
	}
	return fmt.Sprintf("https://github.com/%s/%s", c.RepoOwner, c.RepoName)
}

func (c *Config) GetRunnerLabelsString() string {
	return strings.Join(c.RunnerLabels, ",")
}

// IsRepoAllowed checks if a repository is allowed based on configuration
func (c *Config) IsRepoAllowed(repoFullName string) bool {
	// Check against all configured owners
	for _, ownerConfig := range c.Owners {
		if ownerConfig.IsRepoAllowed(repoFullName) {
			return true
		}
	}
	return false
}

// GetOwnerForRepo returns the owner configuration for a given repository
func (c *Config) GetOwnerForRepo(repoFullName string) (*OwnerConfig, error) {
	for _, ownerConfig := range c.Owners {
		if ownerConfig.IsRepoAllowed(repoFullName) {
			return ownerConfig, nil
		}
	}
	return nil, fmt.Errorf("no owner configuration found for repository %s", repoFullName)
}

// GetRepoNameFromFullName extracts repo name from full name (owner/repo)
func (c *Config) GetRepoNameFromFullName(repoFullName string) string {
	parts := strings.Split(repoFullName, "/")
	if len(parts) == 2 {
		return parts[1]
	}
	return repoFullName
}

func detectArchitecture() string {
	arch := runtime.GOARCH
	fmt.Printf("DEBUG: Go runtime reports GOARCH=%s\n", arch)
	
	switch arch {
	case "amd64":
		fmt.Printf("DEBUG: Using architecture: amd64\n")
		return "amd64"
	case "arm64":
		fmt.Printf("DEBUG: Using architecture: arm64\n")
		return "arm64"
	default:
		// Default to amd64 for unknown architectures
		fmt.Printf("DEBUG: Unknown architecture %s, defaulting to amd64\n", arch)
		return "amd64"
	}
}

// GetEffectiveArchitecture returns the effective architecture for the runtime
// For Docker: always use host architecture
// For Kubernetes: return "all" to enable multi-arch scheduling
func (c *Config) GetEffectiveArchitecture() string {
	switch c.Runtime {
	case RuntimeTypeKubernetes:
		return "all" // Enable multi-architecture scheduling for Kubernetes
	case RuntimeTypeDocker:
		fallthrough
	default:
		return c.Architecture // Use host architecture for Docker
	}
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func getEnvInt(key string, defaultValue int) int {
	if value := os.Getenv(key); value != "" {
		if intValue, err := strconv.Atoi(value); err == nil {
			return intValue
		}
	}
	return defaultValue
}

func getEnvBool(key string, defaultValue bool) bool {
	if value := os.Getenv(key); value != "" {
		if boolValue, err := strconv.ParseBool(value); err == nil {
			return boolValue
		}
	}
	return defaultValue
}

func getEnvSlice(key string, defaultValue []string) []string {
	if value := os.Getenv(key); value != "" {
		return strings.Split(value, ",")
	}
	return defaultValue
}


func parseRuntimeType(runtimeStr string) RuntimeType {
	switch strings.ToLower(runtimeStr) {
	case "docker":
		return RuntimeTypeDocker
	case "kubernetes", "k8s":
		return RuntimeTypeKubernetes
	default:
		return RuntimeTypeDocker // Default to Docker for backward compatibility
	}
} 
