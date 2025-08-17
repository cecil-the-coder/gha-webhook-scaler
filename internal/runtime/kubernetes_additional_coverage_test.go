package runtime

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// Note: detectArchitectureFromLabels test already exists in kubernetes_test.go

// TestKubernetesRuntime_NewKubernetesRuntimeWithClient tests the NewKubernetesRuntimeWithClient function
func TestKubernetesRuntime_NewKubernetesRuntimeWithClient(t *testing.T) {
	mockClient := &MockKubernetesClient{}
	namespace := "test-namespace"

	kr := NewKubernetesRuntimeWithClient(mockClient, namespace)

	assert.NotNil(t, kr)
	assert.Equal(t, mockClient, kr.clientset)
	assert.Equal(t, namespace, kr.namespace)
}

// TestKubernetesRuntime_UsesMockClient tests the UsesMockClient function
func TestKubernetesRuntime_UsesMockClient(t *testing.T) {
	tests := []struct {
		name      string
		clientset KubernetesClientInterface
		namespace string
		expected  bool
	}{
		{
			name:      "nil clientset",
			clientset: nil,
			namespace: "default",
			expected:  true,
		},
		{
			name:      "test namespace",
			clientset: &MockKubernetesClient{},
			namespace: "test",
			expected:  true,
		},
		{
			name:      "production namespace",
			clientset: &MockKubernetesClient{},
			namespace: "default",
			expected:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			kr := &KubernetesRuntime{
				clientset: tt.clientset,
				namespace: tt.namespace,
			}
			result := kr.UsesMockClient()
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestKubernetesRuntime_HealthCheck tests the HealthCheck function
func TestKubernetesRuntime_HealthCheck(t *testing.T) {
	t.Run("successful health check", func(t *testing.T) {
		mockNamespace := &MockNamespaceInterface{}
		mockCoreV1 := &MockCoreV1Client{namespaceInterface: mockNamespace}
		mockClient := &MockKubernetesClient{coreV1Client: mockCoreV1}

		kr := &KubernetesRuntime{
			clientset: mockClient,
			namespace: "default",
		}

		// Mock successful namespace get
		mockNamespace.On("Get", mock.Anything, "default", mock.Anything).Return(&corev1.Namespace{}, nil)

		err := kr.HealthCheck()
		assert.NoError(t, err)
		mockNamespace.AssertExpectations(t)
	})

	t.Run("failed health check", func(t *testing.T) {
		mockNamespace := &MockNamespaceInterface{}
		mockCoreV1 := &MockCoreV1Client{namespaceInterface: mockNamespace}
		mockClient := &MockKubernetesClient{coreV1Client: mockCoreV1}

		kr := &KubernetesRuntime{
			clientset: mockClient,
			namespace: "default",
		}

		// Mock failed namespace get
		mockNamespace.On("Get", mock.Anything, "default", mock.Anything).Return(nil, assert.AnError)

		err := kr.HealthCheck()
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "Kubernetes cluster not accessible")
		mockNamespace.AssertExpectations(t)
	})
}