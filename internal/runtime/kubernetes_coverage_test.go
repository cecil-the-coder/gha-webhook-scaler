package runtime

import (
	"testing"

	"github.com/cecil-the-coder/gha-webhook-scaler/internal/config"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// TestKubernetesRuntime_ensurePersistentVolumes tests the ensurePersistentVolumes function directly
func TestKubernetesRuntime_ensurePersistentVolumes(t *testing.T) {
	t.Run("No cache volumes configured", func(t *testing.T) {
		kr := &KubernetesRuntime{
			clientset: nil, // Will use mock behavior
			namespace: "default",
		}

		cfg := &config.Config{
			CacheVolumes: false,
		}

		err := kr.ensurePersistentVolumes(cfg)
		// When CacheVolumes is false, this should return early without error
		assert.NoError(t, err)
	})

	// Note: Other test cases for ensurePersistentVolumes would require more complex mocking
	// which is beyond the scope of this focused coverage test
}

// TestKubernetesRuntime_ensurePersistentVolumesForArchitecture tests the ensurePersistentVolumesForArchitecture function directly
func TestKubernetesRuntime_ensurePersistentVolumesForArchitecture(t *testing.T) {
	t.Run("Basic architecture PVC creation", func(t *testing.T) {
		mockPVC := &MockPVCInterface{}
		mockCoreV1 := &MockCoreV1Client{pvcInterface: mockPVC}
		mockClient := &MockKubernetesClient{coreV1Client: mockCoreV1}

		kr := &KubernetesRuntime{
			clientset: mockClient,
			namespace: "default",
		}

		// Mock the PVC Get call to return NotFound (so it will try to create)
		mockPVC.On("Get", mock.Anything, "docker-cache-amd64", mock.Anything).Return(nil, errors.NewNotFound(corev1.Resource("persistentvolumeclaims"), "docker-cache-amd64"))
		mockPVC.On("Get", mock.Anything, "buildkit-cache-amd64", mock.Anything).Return(nil, errors.NewNotFound(corev1.Resource("persistentvolumeclaims"), "buildkit-cache-amd64"))
		
		// Mock the PVC Create calls
		mockPVC.On("Create", mock.Anything, mock.AnythingOfType("*v1.PersistentVolumeClaim"), mock.Anything).Return(&corev1.PersistentVolumeClaim{}, nil)

		err := kr.ensurePersistentVolumesForArchitecture("amd64")
		assert.NoError(t, err)
		
		mockPVC.AssertExpectations(t)
	})
}

// TestKubernetesRuntime_cleanupUnusedPVCs tests the cleanupUnusedPVCs function directly
func TestKubernetesRuntime_cleanupUnusedPVCs(t *testing.T) {
	t.Run("Basic cleanup function", func(t *testing.T) {
		mockPVC := &MockPVCInterface{}
		mockPod := &MockPodInterface{}
		mockCoreV1 := &MockCoreV1Client{pvcInterface: mockPVC, podInterface: mockPod}
		mockClient := &MockKubernetesClient{coreV1Client: mockCoreV1}

		kr := &KubernetesRuntime{
			clientset: mockClient,
			namespace: "default",
		}

		// Mock the PVC and Pod list calls
		mockPVC.On("List", mock.Anything, mock.AnythingOfType("v1.ListOptions")).Return(&corev1.PersistentVolumeClaimList{}, nil)
		mockPod.On("List", mock.Anything, mock.AnythingOfType("v1.ListOptions")).Return(&corev1.PodList{}, nil)

		err := kr.cleanupUnusedPVCs()
		assert.NoError(t, err)
		
		mockPVC.AssertExpectations(t)
		mockPod.AssertExpectations(t)
	})
}