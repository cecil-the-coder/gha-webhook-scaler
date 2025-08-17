package runtime

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/cecil-the-coder/gha-webhook-scaler/internal/config"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// KubernetesRuntime implements ContainerRuntime for Kubernetes
type KubernetesRuntime struct {
	clientset KubernetesClientInterface
	namespace string
}

// For testing purposes, we might want to check if we're using our mock interfaces
func (k *KubernetesRuntime) UsesMockClient() bool {
	// This is a simple way to check if we're using the real Kubernetes client or a mock
	// In production, this would return false, but in tests with our mock client, it would return true
	return k.clientset == nil || k.namespace == "test"
}

// NewKubernetesRuntimeWithConfig creates a new Kubernetes runtime client with application config
func NewKubernetesRuntimeWithConfig(appConfig *config.Config) (*KubernetesRuntime, error) {
	// Try in-cluster config first, then fallback to local kubeconfig
	config, err := rest.InClusterConfig()
	if err != nil {
		// Fallback to local kubeconfig
		config, err = clientcmd.BuildConfigFromFlags("", clientcmd.RecommendedHomeFile)
		if err != nil {
			return nil, fmt.Errorf("failed to build kubernetes config: %w", err)
		}
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create kubernetes client: %w", err)
	}

	// Use namespace from config, with fallback to current pod's namespace
	namespace := appConfig.KubernetesNamespace
	if namespace == "" {
		// Try to read the current pod's namespace
		if nsBytes, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/namespace"); err == nil {
			namespace = strings.TrimSpace(string(nsBytes))
			log.Printf("Using current pod namespace: %s", namespace)
		} else {
			namespace = "default" // final fallback only if we can't read the service account
			log.Printf("Could not determine current namespace, using default")
		}
	}

	// Verify namespace exists, but don't fall back - fail if it doesn't exist
	if _, err := clientset.CoreV1().Namespaces().Get(context.Background(), namespace, metav1.GetOptions{}); err != nil {
		return nil, fmt.Errorf("target namespace '%s' not found or not accessible: %w", namespace, err)
	}

	return &KubernetesRuntime{
		clientset: clientset,
		namespace: namespace,
	}, nil
}

// NewKubernetesRuntime creates a new Kubernetes runtime client with default config (backward compatibility)
func NewKubernetesRuntime() (*KubernetesRuntime, error) {
	// Create a minimal config with default namespace
	defaultConfig := &config.Config{
		KubernetesNamespace: "github-runners",
	}
	return NewKubernetesRuntimeWithConfig(defaultConfig)
}

// NewKubernetesRuntimeWithClient creates a new Kubernetes runtime with a custom client (for testing)
func NewKubernetesRuntimeWithClient(client KubernetesClientInterface, namespace string) *KubernetesRuntime {
	return &KubernetesRuntime{
		clientset: client,
		namespace: namespace,
	}
}

// GetRuntimeType returns the runtime type
func (k *KubernetesRuntime) GetRuntimeType() config.RuntimeType {
	return config.RuntimeTypeKubernetes
}

// HealthCheck verifies Kubernetes cluster is accessible
func (k *KubernetesRuntime) HealthCheck() error {
	ctx := context.Background()
	_, err := k.clientset.CoreV1().Namespaces().Get(ctx, k.namespace, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("Kubernetes cluster not accessible: %w", err)
	}
	return nil
}

// StartRunner starts a new runner with default labels
func (k *KubernetesRuntime) StartRunner(config *config.Config, runnerName, repoFullName string) (*RunnerContainer, error) {
	return k.StartRunnerWithLabels(config, runnerName, repoFullName, config.RunnerLabels)
}

// StartRunnerWithLabels starts a new runner with specific labels
func (k *KubernetesRuntime) StartRunnerWithLabels(config *config.Config, runnerName, repoFullName string, labels []string) (*RunnerContainer, error) {
	// Get owner configuration for this repository
	ownerConfig, err := config.GetOwnerForRepo(repoFullName)
	if err != nil {
		return nil, fmt.Errorf("failed to get owner config for repo %s: %w", repoFullName, err)
	}
	
	return k.StartRunnerWithLabelsForOwner(config, ownerConfig, runnerName, repoFullName, labels)
}

// StartRunnerWithLabelsForOwner starts a new runner with labels for a specific owner
func (k *KubernetesRuntime) StartRunnerWithLabelsForOwner(config *config.Config, ownerConfig *config.OwnerConfig, runnerName, repoFullName string, labels []string) (*RunnerContainer, error) {
	ctx := context.Background()

	// Construct repository URL for the specific repo
	var repoURL string
	if repoFullName != "" {
		repoURL = fmt.Sprintf("https://github.com/%s", repoFullName)
	} else {
		repoURL = ownerConfig.GetRepoURL()
	}

	// Use provided labels or fall back to config labels
	var labelsString string
	if len(labels) > 0 {
		// Add "docker" label since we're running Docker sidecar for all runners
		allLabels := append(labels, "docker")
		// Remove duplicate "docker" if it already exists
		labelSet := make(map[string]bool)
		var uniqueLabels []string
		for _, label := range allLabels {
			if !labelSet[label] {
				labelSet[label] = true
				uniqueLabels = append(uniqueLabels, label)
			}
		}
		labelsString = strings.Join(uniqueLabels, ",")
	} else {
		// Add "docker" to config labels
		configLabels := config.GetRunnerLabelsString()
		if configLabels != "" {
			labelsString = configLabels + ",docker"
		} else {
			labelsString = "docker"
		}
	}

	// Detect required architecture from labels for Kubernetes scheduling
	requiredArch := k.detectArchitectureFromLabels(labels)
	if requiredArch == "" {
		requiredArch = config.Architecture // Fallback to config default
	}

	// Check if Docker is required based on workflow labels
	requiresDocker := k.requiresDocker(labels)

	// Create Pod specification
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      runnerName,
			Namespace: k.namespace,
			Labels: map[string]string{
				"app":          "github-runner",
				"architecture": requiredArch,
				"runner-name":  runnerName,
			},
			Annotations: map[string]string{
				"repo-full-name":     repoFullName,
				"created-at":         time.Now().Format(time.RFC3339),
				"required-arch":      requiredArch,
				"runner-labels":      labelsString,
			},
		},
		Spec: corev1.PodSpec{
			RestartPolicy:     corev1.RestartPolicyNever, // Ephemeral runners don't restart
			ShareProcessNamespace: &[]bool{true}[0],      // Allow containers to see each other's processes
			HostUsers:         &[]bool{true}[0],          // Disable user namespaces for DinD privileged access
			SecurityContext: &corev1.PodSecurityContext{
				FSGroup:    &[]int64{1000}[0], // Use non-root group for shared volume access
				FSGroupChangePolicy: &[]corev1.PodFSGroupChangePolicy{corev1.FSGroupChangeOnRootMismatch}[0],
			},
			InitContainers: k.buildInitContainers(requiresDocker),
			Containers: k.buildContainers(requiresDocker, runnerName, repoURL, repoFullName, labelsString, ownerConfig, config),
			Volumes: k.buildVolumes(requiresDocker),
			NodeSelector: map[string]string{
				"kubernetes.io/arch": requiredArch,
			},
			Tolerations: []corev1.Toleration{
				{
					Key:      "github-runner",
					Operator: corev1.TolerationOpEqual,
					Value:    "true",
					Effect:   corev1.TaintEffectNoSchedule,
				},
			},
		},
	}

	// Add cache volumes only if PVCs are enabled
	// (emptyDir makes no sense for ephemeral runners as they get deleted with the pod)
	if config.CacheVolumes && config.KubernetesPVCs {
		// Ensure PVCs exist for this architecture
		if err := k.ensurePersistentVolumesForArchitecture(requiredArch); err != nil {
			log.Printf("Warning: failed to ensure PVCs for architecture %s: %v", requiredArch, err)
		}
		
		// Replace the docker-lib emptyDir volume with PVC for caching
		// Find and replace the docker-lib volume
		for i, volume := range pod.Spec.Volumes {
			if volume.Name == "docker-lib" {
				pod.Spec.Volumes[i] = corev1.Volume{
					Name: "docker-lib",
					VolumeSource: corev1.VolumeSource{
						PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
							ClaimName: fmt.Sprintf("docker-cache-%s", requiredArch),
						},
					},
				}
				break
			}
		}
		
		// Add buildkit cache volume
		pod.Spec.Volumes = append(pod.Spec.Volumes, corev1.Volume{
			Name: "buildkit-cache",
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
					ClaimName: fmt.Sprintf("buildkit-cache-%s", requiredArch),
				},
			},
		})

		// Add buildkit cache volume mount
		pod.Spec.Containers[0].VolumeMounts = append(pod.Spec.Containers[0].VolumeMounts, corev1.VolumeMount{
			Name:      "buildkit-cache",
			MountPath: "/tmp/buildkit-cache",
		})
		
		log.Printf("Using PersistentVolumeClaims for caching on %s architecture", requiredArch)
	} else if config.CacheVolumes {
		// Log why we're not using cache volumes
		log.Printf("Cache volumes disabled for ephemeral runners (emptyDir would be deleted with pod)")
	}

	// Create the pod
	createdPod, err := k.clientset.CoreV1().Pods(k.namespace).Create(ctx, pod, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to create pod: %w", err)
	}

	log.Printf("Successfully created Kubernetes pod %s for repo %s on %s architecture with labels %v", createdPod.Name, repoFullName, requiredArch, labels)

	return &RunnerContainer{
		ID:           string(createdPod.UID),
		Name:         createdPod.Name,
		Status:       string(createdPod.Status.Phase),
		CreatedAt:    createdPod.CreationTimestamp.Time,
		Architecture: requiredArch,
	}, nil
}

// GetRunningRunners returns all running runners for the specified architecture
// For Kubernetes, if architecture is "all", return runners of all architectures
func (k *KubernetesRuntime) GetRunningRunners(architecture string) ([]RunnerContainer, error) {
	ctx := context.Background()

	var labelSelector string
	if architecture == "all" {
		// Return runners of all architectures for Kubernetes
		labelSelector = "app=github-runner"
	} else {
		// Return runners of specific architecture
		labelSelector = fmt.Sprintf("app=github-runner,architecture=%s", architecture)
	}

	// List pods with labels matching github runners
	pods, err := k.clientset.CoreV1().Pods(k.namespace).List(ctx, metav1.ListOptions{
		LabelSelector: labelSelector,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list pods: %w", err)
	}

	var runners []RunnerContainer
	for _, pod := range pods.Items {
		// Include running, pending, and failed pods for cleanup (exclude only succeeded pods)
		if pod.Status.Phase == corev1.PodRunning || pod.Status.Phase == corev1.PodPending || pod.Status.Phase == corev1.PodFailed {
			// Get the actual architecture from pod labels
			podArch := pod.Labels["architecture"]
			if podArch == "" {
				podArch = architecture // fallback
			}
			
			runners = append(runners, RunnerContainer{
				ID:           string(pod.UID),
				Name:         pod.Name,
				Status:       string(pod.Status.Phase),
				CreatedAt:    pod.CreationTimestamp.Time,
				Architecture: podArch,
			})
		}
	}

	return runners, nil
}

// StopRunner stops a specific runner
func (k *KubernetesRuntime) StopRunner(containerID string) error {
	ctx := context.Background()

	// First try to find pod by checking if containerID is actually a pod name
	pod, err := k.clientset.CoreV1().Pods(k.namespace).Get(ctx, containerID, metav1.GetOptions{})
	if err == nil {
		// Found by name, delete it
		err = k.clientset.CoreV1().Pods(k.namespace).Delete(ctx, pod.Name, metav1.DeleteOptions{
			GracePeriodSeconds: &[]int64{30}[0], // 30 second grace period
		})
		if err != nil {
			// Check if pod was already deleted (common during cleanup race conditions)
			if errors.IsNotFound(err) {
				// Log at debug level only for "not found" errors during cleanup
				log.Printf("Pod %s already deleted (cleanup race condition)", pod.Name)
				return nil
			}
			return fmt.Errorf("failed to delete pod: %w", err)
		}
		log.Printf("Stopped Kubernetes pod %s", pod.Name)
		return nil
	}

	// If not found by name, try to find by UID using label selector
	// List all github-runner pods and find the one with matching UID
	pods, err := k.clientset.CoreV1().Pods(k.namespace).List(ctx, metav1.ListOptions{
		LabelSelector: "app=github-runner",
	})
	if err != nil {
		return fmt.Errorf("failed to list pods: %w", err)
	}

	var targetPod *corev1.Pod
	for i, pod := range pods.Items {
		if string(pod.UID) == containerID {
			targetPod = &pods.Items[i]
			break
		}
	}

	if targetPod == nil {
		// Pod not found by name or UID - likely already deleted (cleanup race condition)
		log.Printf("Pod %s already deleted or not found (cleanup race condition)", containerID)
		return nil
	}

	// Delete the pod
	err = k.clientset.CoreV1().Pods(k.namespace).Delete(ctx, targetPod.Name, metav1.DeleteOptions{
		GracePeriodSeconds: &[]int64{30}[0], // 30 second grace period
	})
	if err != nil {
		// Check if pod was already deleted
		if errors.IsNotFound(err) {
			log.Printf("Pod %s already deleted (cleanup race condition)", targetPod.Name)
			return nil
		}
		return fmt.Errorf("failed to delete pod: %w", err)
	}

	log.Printf("Stopped Kubernetes pod %s", targetPod.Name)
	return nil
}

// StopAllRunners stops all runners for the specified architecture
func (k *KubernetesRuntime) StopAllRunners(architecture string) error {
	runners, err := k.GetRunningRunners(architecture)
	if err != nil {
		return fmt.Errorf("failed to get running runners: %w", err)
	}

	for _, runner := range runners {
		if err := k.StopRunner(runner.ID); err != nil {
			// Only log actual errors, not cleanup race conditions (StopRunner now handles "not found" gracefully)
			log.Printf("Failed to stop runner %s: %v", runner.Name, err)
		} else {
			log.Printf("Stopped runner %s", runner.Name)
		}
	}

	return nil
}

// CleanupOldRunners removes runners older than maxAge that are idle (not processing jobs)
func (k *KubernetesRuntime) CleanupOldRunners(architecture string, maxAge time.Duration) error {
	runners, err := k.GetRunningRunners(architecture)
	if err != nil {
		return fmt.Errorf("failed to get running runners: %w", err)
	}

	now := time.Now()
	for _, runner := range runners {
		if now.Sub(runner.CreatedAt) > maxAge {
			// Check if runner is idle by examining logs
			isIdle, err := k.isRunnerIdle(runner.Name)
			if err != nil {
				log.Printf("Failed to check if runner %s is idle: %v (skipping cleanup)", runner.Name, err)
				continue
			}

			if isIdle {
				log.Printf("Cleaning up old idle runner %s (age: %v)", runner.Name, now.Sub(runner.CreatedAt))
				if err := k.StopRunner(runner.ID); err != nil {
					log.Printf("Failed to cleanup runner %s: %v", runner.Name, err)
				}
			} else {
				log.Printf("Skipping cleanup of old runner %s (age: %v) - still processing job", runner.Name, now.Sub(runner.CreatedAt))
			}
		}
	}

	return nil
}

// isRunnerIdle checks if a runner is idle by examining its logs
func (k *KubernetesRuntime) isRunnerIdle(podName string) (bool, error) {
	ctx := context.Background()

	// Get pod to check runner container
	pod, err := k.clientset.CoreV1().Pods(k.namespace).Get(ctx, podName, metav1.GetOptions{})
	if err != nil {
		return false, fmt.Errorf("failed to get pod: %w", err)
	}

	// Check if runner container exists and is running
	runnerContainerRunning := false
	for _, containerStatus := range pod.Status.ContainerStatuses {
		if containerStatus.Name == "runner" && containerStatus.State.Running != nil {
			runnerContainerRunning = true
			break
		}
	}

	if !runnerContainerRunning {
		// If runner container isn't running, consider it not idle (might be completed)
		return false, nil
	}

	// Get last 5 lines of runner logs
	tailLines := int64(5)
	logOptions := &corev1.PodLogOptions{
		Container: "runner",
		TailLines: &tailLines,
	}

	req := k.clientset.CoreV1().Pods(k.namespace).GetLogs(podName, logOptions)
	logs, err := req.DoRaw(ctx)
	if err != nil {
		return false, fmt.Errorf("failed to get logs: %w", err)
	}

	logStr := strings.TrimSpace(string(logs))

	// Split into lines and get the last non-empty line
	lines := strings.Split(logStr, "\n")
	var lastLine string
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.TrimSpace(lines[i]) != "" {
			lastLine = lines[i]
			break
		}
	}

	// Actively processing a job
	if strings.Contains(lastLine, "Running job:") {
		return false, nil
	}

	// Idle and waiting for jobs
	if strings.Contains(lastLine, "Listening for Jobs") {
		return true, nil
	}

	// Job just completed - consider idle (will be cleaned up by CleanupCompletedRunners soon)
	if strings.Contains(lastLine, "Job") && strings.Contains(lastLine, "completed with result:") {
		return true, nil
	}

	// Runner is still starting up, configuring, or in unknown state
	// Safe default: don't cleanup (false = not idle, skip cleanup)
	// Patterns like: "Configuring", "Connected to GitHub", "Runner successfully added"
	log.Printf("Runner %s has unknown state (last line: %s) - skipping cleanup for safety", podName, lastLine)
	return false, nil
}

// GetContainerLogs retrieves logs from a specific container
func (k *KubernetesRuntime) GetContainerLogs(containerID string, tail int) (string, error) {
	ctx := context.Background()

	// First try to find pod by checking if containerID is actually a pod name
	podPtr, err := k.clientset.CoreV1().Pods(k.namespace).Get(ctx, containerID, metav1.GetOptions{})
	if err != nil {
		// If not found by name, try to find by UID using label selector
		// List all github-runner pods and find the one with matching UID
		pods, err := k.clientset.CoreV1().Pods(k.namespace).List(ctx, metav1.ListOptions{
			LabelSelector: "app=github-runner",
		})
		if err != nil {
			return "", fmt.Errorf("failed to list pods: %w", err)
		}

		var foundPod *corev1.Pod
		for i, p := range pods.Items {
			if string(p.UID) == containerID {
				foundPod = &pods.Items[i]
				break
			}
		}

		if foundPod == nil {
			return "", fmt.Errorf("pod with UID %s not found", containerID)
		}
		podPtr = foundPod
	}
	
	pod := *podPtr

	// Get pod logs
	logOptions := &corev1.PodLogOptions{
		Container: "runner",
		TailLines: &[]int64{int64(tail)}[0],
	}

	req := k.clientset.CoreV1().Pods(k.namespace).GetLogs(pod.Name, logOptions)
	logs, err := req.Stream(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to get pod logs: %w", err)
	}
	defer logs.Close()

	// Read all logs
	logData := make([]byte, 4096)
	var allLogs strings.Builder
	for {
		n, err := logs.Read(logData)
		if n > 0 {
			allLogs.Write(logData[:n])
		}
		if err != nil {
			break
		}
	}

	return allLogs.String(), nil
}

// GetRunnerRepository determines which repository a runner is registered to
func (k *KubernetesRuntime) GetRunnerRepository(containerID string) (string, error) {
	ctx := context.Background()

	// First try to find pod by checking if containerID is actually a pod name
	podPtr, err := k.clientset.CoreV1().Pods(k.namespace).Get(ctx, containerID, metav1.GetOptions{})
	if err != nil {
		// If not found by name, try to find by UID using label selector
		// List all github-runner pods and find the one with matching UID
		pods, err := k.clientset.CoreV1().Pods(k.namespace).List(ctx, metav1.ListOptions{
			LabelSelector: "app=github-runner",
		})
		if err != nil {
			return "", fmt.Errorf("failed to list pods: %w", err)
		}

		var foundPod *corev1.Pod
		for i, p := range pods.Items {
			if string(p.UID) == containerID {
				foundPod = &pods.Items[i]
				break
			}
		}

		if foundPod == nil {
			return "", fmt.Errorf("pod with UID %s not found", containerID)
		}
		podPtr = foundPod
	}
	
	pod := *podPtr

	// Check annotations first
	if repoFullName, exists := pod.Annotations["repo-full-name"]; exists {
		return repoFullName, nil
	}

	// Fall back to environment variables
	for _, container := range pod.Spec.Containers {
		if container.Name == "runner" {
			for _, env := range container.Env {
				if env.Name == "REPO_FULL_NAME" {
					return env.Value, nil
				}
				if env.Name == "REPO_URL" {
					// Extract repository full name from URL
					if strings.HasPrefix(env.Value, "https://github.com/") {
						return strings.TrimPrefix(env.Value, "https://github.com/"), nil
					}
				}
			}
		}
	}

	return "", fmt.Errorf("repository information not found in pod")
}

// CleanupCompletedRunners removes all completed/succeeded runners
func (k *KubernetesRuntime) CleanupCompletedRunners(architecture string) error {
	ctx := context.Background()

	// List all pods with labels matching github runners and completed status
	// If architecture is "all", don't filter by architecture
	labelSelector := "app=github-runner"
	if architecture != "" && architecture != "all" {
		labelSelector = fmt.Sprintf("app=github-runner,architecture=%s", architecture)
	}
	
	pods, err := k.clientset.CoreV1().Pods(k.namespace).List(ctx, metav1.ListOptions{
		LabelSelector: labelSelector,
	})
	if err != nil {
		return fmt.Errorf("failed to list pods: %w", err)
	}
	
	cleanedCount := 0
	for _, pod := range pods.Items {
		shouldCleanup := false
		reason := ""

		// Check if pod has completed (succeeded or failed)
		if pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed {
			shouldCleanup = true
			reason = string(pod.Status.Phase)
		} else if pod.Status.Phase == corev1.PodRunning {
			// Check if the runner container has terminated (Docker sidecar may still be running)
			for _, containerStatus := range pod.Status.ContainerStatuses {
				if containerStatus.Name == "runner" && containerStatus.State.Terminated != nil {
					shouldCleanup = true
					reason = fmt.Sprintf("runner container terminated with exit code %d", containerStatus.State.Terminated.ExitCode)
					break
				}
			}
		}

		if shouldCleanup {
			log.Printf("Cleaning up completed runner pod: %s (reason: %s)", pod.Name, reason)

			// Delete the pod
			err := k.clientset.CoreV1().Pods(k.namespace).Delete(ctx, pod.Name, metav1.DeleteOptions{})
			if err != nil {
				// Check if pod was already deleted (common during cleanup race conditions)
				if errors.IsNotFound(err) {
					log.Printf("Pod %s already deleted (cleanup race condition)", pod.Name)
					cleanedCount++ // Still count as cleaned since it's in the desired state
				} else {
					log.Printf("Failed to delete completed pod %s: %v", pod.Name, err)
				}
			} else {
				cleanedCount++
			}
		}
	}
	
	if cleanedCount > 0 {
		log.Printf("Successfully cleaned up %d completed runner pods", cleanedCount)
	}
	
	return nil
}

// CleanupImages handles cleanup for Kubernetes
func (k *KubernetesRuntime) CleanupImages(config *config.Config) error {
	// For Kubernetes, image cleanup is typically handled by:
	// 1. kubelet's image garbage collection
	// 2. DaemonSets running image cleanup tools
	// 3. Node-level cleanup processes
	
	log.Printf("Image cleanup for Kubernetes runtime is handled by the cluster")
	
	// If PVCs are enabled, we can optionally clean up unused PVCs
	if config.KubernetesPVCs {
		if err := k.cleanupUnusedPVCs(); err != nil {
			log.Printf("Warning: failed to cleanup unused PVCs: %v", err)
		}
	}
	
	return nil
}

// ensurePersistentVolumes creates PVCs for caching if they don't exist
func (k *KubernetesRuntime) ensurePersistentVolumes(config *config.Config) error {
	if !config.CacheVolumes {
		return nil
	}

	ctx := context.Background()
	
	pvcNames := []string{
		fmt.Sprintf("docker-cache-%s", config.Architecture),
		fmt.Sprintf("buildkit-cache-%s", config.Architecture),
	}

	for _, pvcName := range pvcNames {
		// Check if PVC already exists
		_, err := k.clientset.CoreV1().PersistentVolumeClaims(k.namespace).Get(ctx, pvcName, metav1.GetOptions{})
		if err == nil {
			continue // PVC already exists
		}
		
		if !errors.IsNotFound(err) {
			return fmt.Errorf("failed to check PVC %s: %w", pvcName, err)
		}

		// Create PVC
		pvc := &corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{
				Name:      pvcName,
				Namespace: k.namespace,
			},
			Spec: corev1.PersistentVolumeClaimSpec{
				AccessModes: []corev1.PersistentVolumeAccessMode{
					corev1.ReadWriteOnce,
				},
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceStorage: resource.MustParse("20Gi"),
					},
				},
			},
		}

		_, err = k.clientset.CoreV1().PersistentVolumeClaims(k.namespace).Create(ctx, pvc, metav1.CreateOptions{})
		if err != nil {
			return fmt.Errorf("failed to create PVC %s: %w", pvcName, err)
		}

		log.Printf("Created PersistentVolumeClaim %s", pvcName)
	}

	return nil
}

// buildContainers creates the container specifications based on whether Docker is required
func (k *KubernetesRuntime) buildContainers(requiresDocker bool, runnerName, repoURL, repoFullName, labelsString string, ownerConfig *config.OwnerConfig, config *config.Config) []corev1.Container {
	containers := []corev1.Container{}

	// Add DinD sidecar only if Docker is required
	if requiresDocker {
		log.Printf("Including Docker-in-Docker sidecar for runner %s (enabled for all jobs)", runnerName)
		dindContainer := corev1.Container{
			Name:  "docker",
			Image: "docker:27-dind",
			SecurityContext: &corev1.SecurityContext{
				Privileged: &[]bool{true}[0],
			},
			Env: []corev1.EnvVar{
				{Name: "DOCKER_TLS_CERTDIR", Value: ""},
			},
			Command: []string{"sh", "-c"},
			Args: []string{
				"set -e; " +
					"if [ -f /sys/fs/cgroup/cgroup.controllers ]; then " +
					"  mkdir -p /sys/fs/cgroup/init; " +
					"  xargs -rn1 < /sys/fs/cgroup/cgroup.procs > /sys/fs/cgroup/init/cgroup.procs || :; " +
					"  sed -e 's/ / +/g' -e 's/^/+/' < /sys/fs/cgroup/cgroup.controllers > /sys/fs/cgroup/cgroup.subtree_control || :; " +
					"  mkdir -p /sys/fs/cgroup/docker; " +
					"  echo '+cpuset +cpu +io +memory +pids' > /sys/fs/cgroup/docker/cgroup.subtree_control || :; " +
					"fi; " +
					"dockerd --host=unix:///var/run/docker.sock --storage-driver=overlay2 --default-cgroupns-mode=host --log-level=error & " +
					"DOCKERD_PID=$!; " +
					"while [ ! -S /var/run/docker.sock ]; do sleep 0.1; done; " +
					"chmod 666 /var/run/docker.sock; " +
					// Wait for runner process to start (timeout after 60 seconds)
					"echo 'Waiting for runner process to start...'; " +
					"RUNNER_STARTED=false; " +
					"for i in $(seq 1 120); do " +
					"  if pgrep -u 1001 -f 'Runner.Listener|runsvc.js' > /dev/null 2>&1; then " +
					"    echo 'Runner process detected, monitoring for completion...'; " +
					"    RUNNER_STARTED=true; " +
					"    break; " +
					"  fi; " +
					"  sleep 0.5; " +
					"done; " +
					// Monitor for runner process exit only if it started
					"if [ \"$RUNNER_STARTED\" = \"true\" ]; then " +
					"  while kill -0 $DOCKERD_PID 2>/dev/null; do " +
					"    if ! pgrep -u 1001 -f 'Runner.Listener|runsvc.js' > /dev/null 2>&1; then " +
					"      echo 'Runner process exited, stopping dockerd...'; " +
					"      kill -TERM $DOCKERD_PID; " +
					"      sleep 2; " +
					"      kill -0 $DOCKERD_PID 2>/dev/null && kill -KILL $DOCKERD_PID || :; " +
					"      exit 0; " +
					"    fi; " +
					"    sleep 1; " +
					"  done; " +
					"fi; " +
					"wait $DOCKERD_PID",
			},
			VolumeMounts: []corev1.VolumeMount{
				{
					Name:      "docker-sock",
					MountPath: "/var/run",
				},
				{
					Name:      "docker-lib",
					MountPath: "/var/lib/docker",
				},
				{
					Name:      "cgroup",
					MountPath: "/sys/fs/cgroup",
				},
				{
					Name:      "docker-config",
					MountPath: "/etc/docker/daemon.json",
					SubPath:   "daemon.json",
					ReadOnly:  true,
				},
			},
			Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("100m"),
					corev1.ResourceMemory: resource.MustParse("512Mi"),
				},
				Limits: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("2"),
					corev1.ResourceMemory: resource.MustParse("4Gi"),
				},
			},
		}
		containers = append(containers, dindContainer)
	}

	// Build runner container environment variables
	runnerEnv := []corev1.EnvVar{
		{Name: "RUNNER_NAME", Value: runnerName},
		{Name: "ACCESS_TOKEN", Value: ownerConfig.GitHubToken},
		{Name: "REPO_URL", Value: repoURL},
		{Name: "RUNNER_SCOPE", Value: "repo"},
		{Name: "LABELS", Value: labelsString},
		{Name: "EPHEMERAL", Value: "true"},
		{Name: "DISABLE_AUTO_UPDATE", Value: "true"},
		{Name: "REPO_FULL_NAME", Value: repoFullName},
		{Name: "RUN_AS_ROOT", Value: "false"}, // Explicitly disable root mode
	}

	// Add DOCKER_HOST only if Docker is required
	if requiresDocker {
		runnerEnv = append(runnerEnv, corev1.EnvVar{
			Name:  "DOCKER_HOST",
			Value: "unix:///var/run/docker.sock",
		})
	}

	// Build runner container volume mounts
	runnerVolumeMounts := []corev1.VolumeMount{}
	if requiresDocker {
		runnerVolumeMounts = append(runnerVolumeMounts, corev1.VolumeMount{
			Name:      "docker-sock",
			MountPath: "/var/run",
			ReadOnly:  false,
		})
	}

	// Build runner container
	runnerContainer := corev1.Container{
		Name:  "runner",
		Image: config.RunnerImage,
		Env:   runnerEnv,
		SecurityContext: &corev1.SecurityContext{
			RunAsNonRoot:             &[]bool{true}[0],
			RunAsUser:                &[]int64{1001}[0], // Use different UID than Podman container
			RunAsGroup:               &[]int64{1000}[0], // Same group as Podman for socket access
			AllowPrivilegeEscalation: &[]bool{true}[0],  // Allow sudo to work in runner container
		},
		Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("1"),
				corev1.ResourceMemory: resource.MustParse("2Gi"),
			},
			Limits: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("4"),
				corev1.ResourceMemory: resource.MustParse("6Gi"),
			},
		},
		VolumeMounts: runnerVolumeMounts,
	}

	// Add readiness probe only if Docker is required (to wait for Docker socket)
	if requiresDocker {
		runnerContainer.ReadinessProbe = &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				Exec: &corev1.ExecAction{
					Command: []string{"sh", "-c", "test -S /var/run/docker.sock && timeout 5 docker version > /dev/null 2>&1"},
				},
			},
			InitialDelaySeconds: 10,
			PeriodSeconds:       2,
			TimeoutSeconds:      5,
			FailureThreshold:    30,
		}
	}

	containers = append(containers, runnerContainer)
	return containers
}

// buildVolumes creates the volume specifications based on whether Docker is required
func (k *KubernetesRuntime) buildVolumes(requiresDocker bool) []corev1.Volume {
	volumes := []corev1.Volume{}

	// Add Docker-related volumes only if Docker is required
	if requiresDocker {
		volumes = append(volumes,
			corev1.Volume{
				Name: "docker-sock",
				VolumeSource: corev1.VolumeSource{
					EmptyDir: &corev1.EmptyDirVolumeSource{}, // Shared volume for Docker socket
				},
			},
			corev1.Volume{
				Name: "docker-lib",
				VolumeSource: corev1.VolumeSource{
					EmptyDir: &corev1.EmptyDirVolumeSource{}, // Docker container storage
				},
			},
			corev1.Volume{
				Name: "cgroup",
				VolumeSource: corev1.VolumeSource{
					HostPath: &corev1.HostPathVolumeSource{
						Path: "/sys/fs/cgroup",
						Type: &[]corev1.HostPathType{corev1.HostPathDirectory}[0],
					},
				},
			},
			corev1.Volume{
				Name: "docker-config",
				VolumeSource: corev1.VolumeSource{
					ConfigMap: &corev1.ConfigMapVolumeSource{
						LocalObjectReference: corev1.LocalObjectReference{
							Name: "dind-daemon-config",
						},
						Items: []corev1.KeyToPath{
							{
								Key:  "daemon.json",
								Path: "daemon.json",
							},
						},
					},
				},
			},
		)
	}

	return volumes
}

// buildInitContainers creates init containers to set up shared volumes
func (k *KubernetesRuntime) buildInitContainers(requiresDocker bool) []corev1.Container {
	if !requiresDocker {
		return nil
	}

	return []corev1.Container{
		{
			Name:  "volume-setup",
			Image: "busybox:1.35",
			Command: []string{
				"sh", "-c",
				"mkdir -p /var/run && chmod 777 /var/run",
			},
			SecurityContext: &corev1.SecurityContext{
				RunAsUser:  &[]int64{0}[0], // Run as root to set ownership
				RunAsGroup: &[]int64{0}[0],
			},
			VolumeMounts: []corev1.VolumeMount{
				{
					Name:      "docker-sock",
					MountPath: "/var/run",
				},
			},
		},
	}
}

// detectArchitectureFromLabels analyzes runner labels to determine required architecture
func (k *KubernetesRuntime) detectArchitectureFromLabels(labels []string) string {
	for _, label := range labels {
		switch strings.ToLower(label) {
		case "x64", "amd64", "x86_64":
			return "amd64"
		case "arm64", "aarch64":
			return "arm64"
		}
	}
	return "" // No specific architecture detected
}

// requiresDocker always returns true to ensure Docker-in-Docker sidecar runs for all jobs
func (k *KubernetesRuntime) requiresDocker(labels []string) bool {
	// Always enable Docker sidecar for all jobs
	return true
}

// ensurePersistentVolumesForArchitecture creates PVCs for a specific architecture if they don't exist
func (k *KubernetesRuntime) ensurePersistentVolumesForArchitecture(architecture string) error {
	ctx := context.Background()
	
	pvcNames := []string{
		fmt.Sprintf("docker-cache-%s", architecture),
		fmt.Sprintf("buildkit-cache-%s", architecture),
	}

	for _, pvcName := range pvcNames {
		// Check if PVC already exists
		_, err := k.clientset.CoreV1().PersistentVolumeClaims(k.namespace).Get(ctx, pvcName, metav1.GetOptions{})
		if err == nil {
			continue // PVC already exists
		}
		
		if !errors.IsNotFound(err) {
			log.Printf("Warning: failed to check PVC %s: %v", pvcName, err)
			continue
		}

		// Create PVC
		pvc := &corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{
				Name:      pvcName,
				Namespace: k.namespace,
				Labels: map[string]string{
					"app":          "github-runner",
					"architecture": architecture,
					"type":         "cache",
				},
			},
			Spec: corev1.PersistentVolumeClaimSpec{
				AccessModes: []corev1.PersistentVolumeAccessMode{
					corev1.ReadWriteOnce,
				},
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceStorage: resource.MustParse("20Gi"),
					},
				},
			},
		}

		_, err = k.clientset.CoreV1().PersistentVolumeClaims(k.namespace).Create(ctx, pvc, metav1.CreateOptions{})
		if err != nil {
			log.Printf("Warning: failed to create PVC %s: %v", pvcName, err)
		} else {
			log.Printf("Created PersistentVolumeClaim %s for %s architecture", pvcName, architecture)
		}
	}

	return nil
}
// cleanupUnusedPVCs removes PVCs that are not currently being used by any pods
func (k *KubernetesRuntime) cleanupUnusedPVCs() error {
	ctx := context.Background()
	
	// Get all PVCs with our labels
	pvcs, err := k.clientset.CoreV1().PersistentVolumeClaims(k.namespace).List(ctx, metav1.ListOptions{
		LabelSelector: "app=github-runner,type=cache",
	})
	if err != nil {
		return fmt.Errorf("failed to list PVCs: %w", err)
	}
	
	// Get all pods to check which PVCs are in use
	pods, err := k.clientset.CoreV1().Pods(k.namespace).List(ctx, metav1.ListOptions{
		LabelSelector: "app=github-runner",
	})
	if err != nil {
		return fmt.Errorf("failed to list pods: %w", err)
	}
	
	// Create set of PVCs currently in use
	usedPVCs := make(map[string]bool)
	for _, pod := range pods.Items {
		for _, volume := range pod.Spec.Volumes {
			if volume.PersistentVolumeClaim != nil {
				usedPVCs[volume.PersistentVolumeClaim.ClaimName] = true
			}
		}
	}
	
	// Clean up unused PVCs (be conservative - only remove if really unused)
	for _, pvc := range pvcs.Items {
		if !usedPVCs[pvc.Name] {
			// Double-check: wait a bit and check again to avoid race conditions
			// Only remove PVCs that have been unused for a while
			if time.Since(pvc.CreationTimestamp.Time) > 1*time.Hour {
				log.Printf("Cleaning up unused PVC: %s", pvc.Name)
				err := k.clientset.CoreV1().PersistentVolumeClaims(k.namespace).Delete(ctx, pvc.Name, metav1.DeleteOptions{})
				if err != nil {
					log.Printf("Warning: failed to delete unused PVC %s: %v", pvc.Name, err)
				}
			}
		}
	}
	
	return nil
}
