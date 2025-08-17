package runtime

import (
	"context"
	"testing"
	"time"

	"github.com/cecil-the-coder/gha-webhook-scaler/internal/config"

	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	policyv1beta1 "k8s.io/api/policy/v1beta1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	corev1client "k8s.io/client-go/kubernetes/typed/core/v1"
	applyconfigcorev1 "k8s.io/client-go/applyconfigurations/core/v1"
	"k8s.io/client-go/rest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

func TestKubernetesRuntime_GetRuntimeType(t *testing.T) {
	runtime := &KubernetesRuntime{}
	
	assert.Equal(t, config.RuntimeTypeKubernetes, runtime.GetRuntimeType())
}

func TestKubernetesRuntime_CleanupImages(t *testing.T) {
	runtime := &KubernetesRuntime{}

	cfg := &config.Config{
		CleanupImages: true,
	}

	// Kubernetes runtime cleanup is typically a no-op
	err := runtime.CleanupImages(cfg)

	assert.NoError(t, err)
}

func TestKubernetesRuntime_CleanupImages_WithPVCs(t *testing.T) {
	mockPVC := &MockPVCInterface{}
	mockPod := &MockPodInterface{}
	mockNamespace := &MockNamespaceInterface{}
	mockCoreV1 := &MockCoreV1Client{pvcInterface: mockPVC, podInterface: mockPod, namespaceInterface: mockNamespace}
	mockClient := &MockKubernetesClient{coreV1Client: mockCoreV1}

	kr := &KubernetesRuntime{
		clientset: mockClient,
		namespace: "default",
	}

	cfg := &config.Config{
		KubernetesPVCs: true,
	}

	// When KubernetesPVCs is true, we should call cleanupUnusedPVCs
	mockPVC.On("List", mock.Anything, metav1.ListOptions{LabelSelector: "app=github-runner,type=cache"}).Return(&corev1.PersistentVolumeClaimList{}, nil)
	mockPod.On("List", mock.Anything, metav1.ListOptions{LabelSelector: "app=github-runner"}).Return(&corev1.PodList{}, nil)

	err := kr.CleanupImages(cfg)
	assert.NoError(t, err)

	mockPVC.AssertExpectations(t)
	mockPod.AssertExpectations(t)
}

// Mock implementations for testing
type MockPVCInterface struct {
	mock.Mock
}

func (m *MockPVCInterface) Apply(ctx context.Context, obj *applyconfigcorev1.PersistentVolumeClaimApplyConfiguration, opts metav1.ApplyOptions) (*corev1.PersistentVolumeClaim, error) {
	args := m.Called(ctx, obj, opts)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*corev1.PersistentVolumeClaim), args.Error(1)
}

func (m *MockPVCInterface) ApplyStatus(ctx context.Context, obj *applyconfigcorev1.PersistentVolumeClaimApplyConfiguration, opts metav1.ApplyOptions) (*corev1.PersistentVolumeClaim, error) {
	args := m.Called(ctx, obj, opts)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*corev1.PersistentVolumeClaim), args.Error(1)
}

func (m *MockPVCInterface) Get(ctx context.Context, name string, opts metav1.GetOptions) (*corev1.PersistentVolumeClaim, error) {
	args := m.Called(ctx, name, opts)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*corev1.PersistentVolumeClaim), args.Error(1)
}

func (m *MockPVCInterface) Create(ctx context.Context, pvc *corev1.PersistentVolumeClaim, opts metav1.CreateOptions) (*corev1.PersistentVolumeClaim, error) {
	args := m.Called(ctx, pvc, opts)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*corev1.PersistentVolumeClaim), args.Error(1)
}

func (m *MockPVCInterface) List(ctx context.Context, opts metav1.ListOptions) (*corev1.PersistentVolumeClaimList, error) {
	args := m.Called(ctx, opts)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*corev1.PersistentVolumeClaimList), args.Error(1)
}

func (m *MockPVCInterface) Delete(ctx context.Context, name string, opts metav1.DeleteOptions) error {
	args := m.Called(ctx, name, opts)
	return args.Error(0)
}

func (m *MockPVCInterface) Update(ctx context.Context, pvc *corev1.PersistentVolumeClaim, opts metav1.UpdateOptions) (*corev1.PersistentVolumeClaim, error) {
	args := m.Called(ctx, pvc, opts)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*corev1.PersistentVolumeClaim), args.Error(1)
}

func (m *MockPVCInterface) DeleteCollection(ctx context.Context, opts metav1.DeleteOptions, listOpts metav1.ListOptions) error {
	args := m.Called(ctx, opts, listOpts)
	return args.Error(0)
}

func (m *MockPVCInterface) Patch(ctx context.Context, name string, pt types.PatchType, data []byte, opts metav1.PatchOptions, subresources ...string) (*corev1.PersistentVolumeClaim, error) {
	args := m.Called(ctx, name, pt, data, opts, subresources)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*corev1.PersistentVolumeClaim), args.Error(1)
}

func (m *MockPVCInterface) Watch(ctx context.Context, opts metav1.ListOptions) (watch.Interface, error) {
	args := m.Called(ctx, opts)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(watch.Interface), args.Error(1)
}

func (m *MockPVCInterface) UpdateStatus(ctx context.Context, pvc *corev1.PersistentVolumeClaim, opts metav1.UpdateOptions) (*corev1.PersistentVolumeClaim, error) {
	args := m.Called(ctx, pvc, opts)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*corev1.PersistentVolumeClaim), args.Error(1)
}

type MockPodInterface struct {
	mock.Mock
}

func (m *MockPodInterface) Apply(ctx context.Context, obj *applyconfigcorev1.PodApplyConfiguration, opts metav1.ApplyOptions) (*corev1.Pod, error) {
	args := m.Called(ctx, obj, opts)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*corev1.Pod), args.Error(1)
}

func (m *MockPodInterface) ApplyStatus(ctx context.Context, obj *applyconfigcorev1.PodApplyConfiguration, opts metav1.ApplyOptions) (*corev1.Pod, error) {
	args := m.Called(ctx, obj, opts)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*corev1.Pod), args.Error(1)
}

func (m *MockPodInterface) Create(ctx context.Context, pod *corev1.Pod, opts metav1.CreateOptions) (*corev1.Pod, error) {
	args := m.Called(ctx, pod, opts)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*corev1.Pod), args.Error(1)
}

func (m *MockPodInterface) List(ctx context.Context, opts metav1.ListOptions) (*corev1.PodList, error) {
	args := m.Called(ctx, opts)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*corev1.PodList), args.Error(1)
}

func (m *MockPodInterface) Delete(ctx context.Context, name string, opts metav1.DeleteOptions) error {
	args := m.Called(ctx, name, opts)
	return args.Error(0)
}

func (m *MockPodInterface) Get(ctx context.Context, name string, opts metav1.GetOptions) (*corev1.Pod, error) {
	args := m.Called(ctx, name, opts)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*corev1.Pod), args.Error(1)
}

func (m *MockPodInterface) Update(ctx context.Context, pod *corev1.Pod, opts metav1.UpdateOptions) (*corev1.Pod, error) {
	args := m.Called(ctx, pod, opts)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*corev1.Pod), args.Error(1)
}

func (m *MockPodInterface) DeleteCollection(ctx context.Context, opts metav1.DeleteOptions, listOpts metav1.ListOptions) error {
	args := m.Called(ctx, opts, listOpts)
	return args.Error(0)
}

func (m *MockPodInterface) Patch(ctx context.Context, name string, pt types.PatchType, data []byte, opts metav1.PatchOptions, subresources ...string) (*corev1.Pod, error) {
	args := m.Called(ctx, name, pt, data, opts, subresources)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*corev1.Pod), args.Error(1)
}

func (m *MockPodInterface) Watch(ctx context.Context, opts metav1.ListOptions) (watch.Interface, error) {
	args := m.Called(ctx, opts)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(watch.Interface), args.Error(1)
}

func (m *MockPodInterface) UpdateStatus(ctx context.Context, pod *corev1.Pod, opts metav1.UpdateOptions) (*corev1.Pod, error) {
	args := m.Called(ctx, pod, opts)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*corev1.Pod), args.Error(1)
}

func (m *MockPodInterface) GetLogs(name string, opts *corev1.PodLogOptions) *rest.Request {
	return nil
}

func (m *MockPodInterface) Bind(ctx context.Context, binding *corev1.Binding, opts metav1.CreateOptions) error {
	return nil
}

func (m *MockPodInterface) Evict(ctx context.Context, eviction *policyv1beta1.Eviction) error {
	args := m.Called(ctx, eviction)
	return args.Error(0)
}

func (m *MockPodInterface) EvictV1(ctx context.Context, eviction *policyv1.Eviction) error {
	args := m.Called(ctx, eviction)
	return args.Error(0)
}

func (m *MockPodInterface) EvictV1beta1(ctx context.Context, eviction *policyv1beta1.Eviction) error {
	args := m.Called(ctx, eviction)
	return args.Error(0)
}

func (m *MockPodInterface) ProxyGet(scheme, name, port, path string, params map[string]string) rest.ResponseWrapper {
	args := m.Called(scheme, name, port, path, params)
	if args.Get(0) == nil {
		return nil
	}
	return args.Get(0).(rest.ResponseWrapper)
}

func (m *MockPodInterface) UpdateEphemeralContainers(ctx context.Context, podName string, pod *corev1.Pod, opts metav1.UpdateOptions) (*corev1.Pod, error) {
	args := m.Called(ctx, podName, pod, opts)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*corev1.Pod), args.Error(1)
}

type MockNamespaceInterface struct {
	mock.Mock
}

func (m *MockNamespaceInterface) Apply(ctx context.Context, obj *applyconfigcorev1.NamespaceApplyConfiguration, opts metav1.ApplyOptions) (*corev1.Namespace, error) {
	args := m.Called(ctx, obj, opts)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*corev1.Namespace), args.Error(1)
}

func (m *MockNamespaceInterface) ApplyStatus(ctx context.Context, obj *applyconfigcorev1.NamespaceApplyConfiguration, opts metav1.ApplyOptions) (*corev1.Namespace, error) {
	args := m.Called(ctx, obj, opts)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*corev1.Namespace), args.Error(1)
}

func (m *MockNamespaceInterface) Get(ctx context.Context, name string, opts metav1.GetOptions) (*corev1.Namespace, error) {
	args := m.Called(ctx, name, opts)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*corev1.Namespace), args.Error(1)
}

func (m *MockNamespaceInterface) Create(ctx context.Context, namespace *corev1.Namespace, opts metav1.CreateOptions) (*corev1.Namespace, error) {
	args := m.Called(ctx, namespace, opts)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*corev1.Namespace), args.Error(1)
}

func (m *MockNamespaceInterface) Update(ctx context.Context, namespace *corev1.Namespace, opts metav1.UpdateOptions) (*corev1.Namespace, error) {
	args := m.Called(ctx, namespace, opts)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*corev1.Namespace), args.Error(1)
}

func (m *MockNamespaceInterface) Delete(ctx context.Context, name string, opts metav1.DeleteOptions) error {
	args := m.Called(ctx, name, opts)
	return args.Error(0)
}

func (m *MockNamespaceInterface) List(ctx context.Context, opts metav1.ListOptions) (*corev1.NamespaceList, error) {
	args := m.Called(ctx, opts)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*corev1.NamespaceList), args.Error(1)
}

func (m *MockNamespaceInterface) DeleteCollection(ctx context.Context, opts metav1.DeleteOptions, listOpts metav1.ListOptions) error {
	args := m.Called(ctx, opts, listOpts)
	return args.Error(0)
}

func (m *MockNamespaceInterface) Patch(ctx context.Context, name string, pt types.PatchType, data []byte, opts metav1.PatchOptions, subresources ...string) (*corev1.Namespace, error) {
	args := m.Called(ctx, name, pt, data, opts, subresources)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*corev1.Namespace), args.Error(1)
}

func (m *MockNamespaceInterface) Finalize(ctx context.Context, namespace *corev1.Namespace, opts metav1.UpdateOptions) (*corev1.Namespace, error) {
	args := m.Called(ctx, namespace, opts)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*corev1.Namespace), args.Error(1)
}

func (m *MockNamespaceInterface) Status() corev1client.NamespaceInterface {
	return m
}

func (m *MockNamespaceInterface) Watch(ctx context.Context, opts metav1.ListOptions) (watch.Interface, error) {
	args := m.Called(ctx, opts)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(watch.Interface), args.Error(1)
}

func (m *MockNamespaceInterface) UpdateStatus(ctx context.Context, namespace *corev1.Namespace, opts metav1.UpdateOptions) (*corev1.Namespace, error) {
	args := m.Called(ctx, namespace, opts)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*corev1.Namespace), args.Error(1)
}

// MockCoreV1Client implements MockableCoreV1Interface for testing
type MockCoreV1Client struct {
	namespaceInterface *MockNamespaceInterface
	podInterface       *MockPodInterface
	pvcInterface       *MockPVCInterface
}

func (m *MockCoreV1Client) Namespaces() corev1client.NamespaceInterface {
	return m.namespaceInterface
}

func (m *MockCoreV1Client) Pods(namespace string) corev1client.PodInterface {
	return m.podInterface
}

func (m *MockCoreV1Client) PersistentVolumeClaims(namespace string) corev1client.PersistentVolumeClaimInterface {
	return m.pvcInterface
}

// Implement all other required CoreV1Interface methods as no-op
func (m *MockCoreV1Client) ComponentStatuses() corev1client.ComponentStatusInterface { return nil }
func (m *MockCoreV1Client) ConfigMaps(namespace string) corev1client.ConfigMapInterface { return nil }
func (m *MockCoreV1Client) Endpoints(namespace string) corev1client.EndpointsInterface { return nil }
func (m *MockCoreV1Client) Events(namespace string) corev1client.EventInterface { return nil }
func (m *MockCoreV1Client) LimitRanges(namespace string) corev1client.LimitRangeInterface { return nil }
func (m *MockCoreV1Client) Nodes() corev1client.NodeInterface { return nil }
func (m *MockCoreV1Client) PersistentVolumes() corev1client.PersistentVolumeInterface { return nil }
func (m *MockCoreV1Client) PodTemplates(namespace string) corev1client.PodTemplateInterface { return nil }
func (m *MockCoreV1Client) ReplicationControllers(namespace string) corev1client.ReplicationControllerInterface { return nil }
func (m *MockCoreV1Client) ResourceQuotas(namespace string) corev1client.ResourceQuotaInterface { return nil }
func (m *MockCoreV1Client) Secrets(namespace string) corev1client.SecretInterface { return nil }
func (m *MockCoreV1Client) Services(namespace string) corev1client.ServiceInterface { return nil }
func (m *MockCoreV1Client) ServiceAccounts(namespace string) corev1client.ServiceAccountInterface { return nil }
func (m *MockCoreV1Client) RESTClient() rest.Interface { return nil }

// MockKubernetesClient implements KubernetesClientInterface for testing
type MockKubernetesClient struct {
	coreV1Client corev1client.CoreV1Interface
}

func (m *MockKubernetesClient) CoreV1() corev1client.CoreV1Interface {
	return m.coreV1Client
}

func TestKubernetesRuntime_detectArchitectureFromLabels(t *testing.T) {
	kr := &KubernetesRuntime{}

	// Test case 1: x64 labels
	labels := []string{"ubuntu-latest", "x64", "self-hosted"}
	arch := kr.detectArchitectureFromLabels(labels)
	assert.Equal(t, "amd64", arch)

	// Test case 2: amd64 labels
	labels = []string{"ubuntu-latest", "amd64", "self-hosted"}
	arch = kr.detectArchitectureFromLabels(labels)
	assert.Equal(t, "amd64", arch)

	// Test case 3: x86_64 labels
	labels = []string{"ubuntu-latest", "x86_64", "self-hosted"}
	arch = kr.detectArchitectureFromLabels(labels)
	assert.Equal(t, "amd64", arch)

	// Test case 4: arm64 labels
	labels = []string{"ubuntu-latest", "arm64", "self-hosted"}
	arch = kr.detectArchitectureFromLabels(labels)
	assert.Equal(t, "arm64", arch)

	// Test case 5: aarch64 labels
	labels = []string{"ubuntu-latest", "aarch64", "self-hosted"}
	arch = kr.detectArchitectureFromLabels(labels)
	assert.Equal(t, "arm64", arch)

	// Test case 6: no architecture labels
	labels = []string{"ubuntu-latest", "self-hosted", "large"}
	arch = kr.detectArchitectureFromLabels(labels)
	assert.Equal(t, "", arch)

	// Test case 7: case insensitive
	labels = []string{"ubuntu-latest", "X64", "self-hosted"}
	arch = kr.detectArchitectureFromLabels(labels)
	assert.Equal(t, "amd64", arch)

	// Test case 8: empty labels
	labels = []string{}
	arch = kr.detectArchitectureFromLabels(labels)
	assert.Equal(t, "", arch)
}

func TestKubernetesRuntime_detectArchitectureFromLabels_NegativeCases(t *testing.T) {
	kr := &KubernetesRuntime{}

	// Test case 1: unrecognized architecture labels
	labels := []string{"ubuntu-latest", "ppc64le", "self-hosted"}
	arch := kr.detectArchitectureFromLabels(labels)
	assert.Equal(t, "", arch)

	// Test case 2: mixed case unrecognized labels
	labels = []string{"ubuntu-latest", "PPC64LE", "self-hosted"}
	arch = kr.detectArchitectureFromLabels(labels)
	assert.Equal(t, "", arch)
}

func TestKubernetesRuntime_ensurePersistentVolumes_ExistingPVCs(t *testing.T) {
	mockPVC := &MockPVCInterface{}
	mockPod := &MockPodInterface{}
	mockNamespace := &MockNamespaceInterface{}
	mockCoreV1 := &MockCoreV1Client{pvcInterface: mockPVC, podInterface: mockPod, namespaceInterface: mockNamespace}
	mockClient := &MockKubernetesClient{coreV1Client: mockCoreV1}

	kr := &KubernetesRuntime{
		clientset: mockClient,
		namespace: "default",
	}

	cfg := &config.Config{
		CacheVolumes: true,
		Architecture: "amd64",
	}

	// Test case: PVCs already exist
	mockPVC.On("Get", mock.Anything, "docker-cache-amd64", mock.Anything).Return(&corev1.PersistentVolumeClaim{}, nil)
	mockPVC.On("Get", mock.Anything, "buildkit-cache-amd64", mock.Anything).Return(&corev1.PersistentVolumeClaim{}, nil)

	err := kr.ensurePersistentVolumes(cfg)
	assert.NoError(t, err)

	mockPVC.AssertExpectations(t)
}

func TestKubernetesRuntime_ensurePersistentVolumes_CreatePVCs(t *testing.T) {
	mockPVC := &MockPVCInterface{}
	mockPod := &MockPodInterface{}
	mockNamespace := &MockNamespaceInterface{}
	mockCoreV1 := &MockCoreV1Client{pvcInterface: mockPVC, podInterface: mockPod, namespaceInterface: mockNamespace}
	mockClient := &MockKubernetesClient{coreV1Client: mockCoreV1}

	kr := &KubernetesRuntime{
		clientset: mockClient,
		namespace: "default",
	}

	cfg := &config.Config{
		CacheVolumes: true,
		Architecture: "arm64",
	}

	// Test case: PVCs don't exist (return not found errors)
	mockPVC.On("Get", mock.Anything, "docker-cache-arm64", mock.Anything).Return((*corev1.PersistentVolumeClaim)(nil), errors.NewNotFound(corev1.Resource("persistentvolumeclaims"), "docker-cache-arm64"))
	mockPVC.On("Get", mock.Anything, "buildkit-cache-arm64", mock.Anything).Return((*corev1.PersistentVolumeClaim)(nil), errors.NewNotFound(corev1.Resource("persistentvolumeclaims"), "buildkit-cache-arm64"))
	mockPVC.On("Create", mock.Anything, mock.Anything, mock.Anything).Return(&corev1.PersistentVolumeClaim{}, nil)

	err := kr.ensurePersistentVolumes(cfg)
	assert.NoError(t, err)

	mockPVC.AssertExpectations(t)
	// Should call Create twice, once for each PVC
	mockPVC.AssertNumberOfCalls(t, "Create", 2)
}

func TestKubernetesRuntime_ensurePersistentVolumes_ErrorHandling(t *testing.T) {
	mockPVC := &MockPVCInterface{}
	mockPod := &MockPodInterface{}
	mockNamespace := &MockNamespaceInterface{}
	mockCoreV1 := &MockCoreV1Client{pvcInterface: mockPVC, podInterface: mockPod, namespaceInterface: mockNamespace}
	mockClient := &MockKubernetesClient{coreV1Client: mockCoreV1}

	kr := &KubernetesRuntime{
		clientset: mockClient,
		namespace: "default",
	}

	cfg := &config.Config{
		CacheVolumes: false, // Should return early
		Architecture: "amd64",
	}

	// Should return early without calling PVC methods
	err := kr.ensurePersistentVolumes(cfg)
	assert.NoError(t, err)
}

func TestKubernetesRuntime_ensurePersistentVolumes_GetError(t *testing.T) {
	mockPVC := &MockPVCInterface{}
	mockPod := &MockPodInterface{}
	mockNamespace := &MockNamespaceInterface{}
	mockCoreV1 := &MockCoreV1Client{pvcInterface: mockPVC, podInterface: mockPod, namespaceInterface: mockNamespace}
	mockClient := &MockKubernetesClient{coreV1Client: mockCoreV1}

	kr := &KubernetesRuntime{
		clientset: mockClient,
		namespace: "default",
	}

	cfg := &config.Config{
		CacheVolumes: true,
		Architecture: "amd64",
	}

	// Test case: PVC Get returns a generic error
	mockPVC.On("Get", mock.Anything, "docker-cache-amd64", mock.Anything).Return((*corev1.PersistentVolumeClaim)(nil), assert.AnError)

	err := kr.ensurePersistentVolumes(cfg)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to check PVC")

	mockPVC.AssertExpectations(t)
}

func TestKubernetesRuntime_ensurePersistentVolumes_CreateError(t *testing.T) {
	mockPVC := &MockPVCInterface{}
	mockPod := &MockPodInterface{}
	mockNamespace := &MockNamespaceInterface{}
	mockCoreV1 := &MockCoreV1Client{pvcInterface: mockPVC, podInterface: mockPod, namespaceInterface: mockNamespace}
	mockClient := &MockKubernetesClient{coreV1Client: mockCoreV1}

	kr := &KubernetesRuntime{
		clientset: mockClient,
		namespace: "default",
	}

	cfg := &config.Config{
		CacheVolumes: true,
		Architecture: "amd64",
	}

	// Test case: PVC doesn't exist and Create fails
	mockPVC.On("Get", mock.Anything, "docker-cache-amd64", mock.Anything).Return((*corev1.PersistentVolumeClaim)(nil), errors.NewNotFound(corev1.Resource("persistentvolumeclaims"), "docker-cache-amd64"))
	mockPVC.On("Create", mock.Anything, mock.Anything, mock.Anything).Return((*corev1.PersistentVolumeClaim)(nil), assert.AnError)

	err := kr.ensurePersistentVolumes(cfg)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to create PVC")

	mockPVC.AssertExpectations(t)
}

func TestKubernetesRuntime_ensurePersistentVolumesForArchitecture_ExistingPVCs(t *testing.T) {
	mockPVC := &MockPVCInterface{}
	mockPod := &MockPodInterface{}
	mockNamespace := &MockNamespaceInterface{}
	mockCoreV1 := &MockCoreV1Client{pvcInterface: mockPVC, podInterface: mockPod, namespaceInterface: mockNamespace}
	mockClient := &MockKubernetesClient{coreV1Client: mockCoreV1}

	kr := &KubernetesRuntime{
		clientset: mockClient,
		namespace: "default",
	}

	// Test case: PVCs already exist
	mockPVC.On("Get", mock.Anything, "docker-cache-arm64", mock.Anything).Return(&corev1.PersistentVolumeClaim{}, nil)
	mockPVC.On("Get", mock.Anything, "buildkit-cache-arm64", mock.Anything).Return(&corev1.PersistentVolumeClaim{}, nil)

	err := kr.ensurePersistentVolumesForArchitecture("arm64")
	assert.NoError(t, err)

	mockPVC.AssertExpectations(t)
}

func TestKubernetesRuntime_ensurePersistentVolumesForArchitecture_CreatePVCs(t *testing.T) {
	mockPVC := &MockPVCInterface{}
	mockPod := &MockPodInterface{}
	mockNamespace := &MockNamespaceInterface{}
	mockCoreV1 := &MockCoreV1Client{pvcInterface: mockPVC, podInterface: mockPod, namespaceInterface: mockNamespace}
	mockClient := &MockKubernetesClient{coreV1Client: mockCoreV1}

	kr := &KubernetesRuntime{
		clientset: mockClient,
		namespace: "default",
	}

	// Test case: PVCs don't exist (return not found errors)
	mockPVC.On("Get", mock.Anything, "docker-cache-arm64", mock.Anything).Return((*corev1.PersistentVolumeClaim)(nil), errors.NewNotFound(corev1.Resource("persistentvolumeclaims"), "docker-cache-arm64"))
	mockPVC.On("Get", mock.Anything, "buildkit-cache-arm64", mock.Anything).Return((*corev1.PersistentVolumeClaim)(nil), errors.NewNotFound(corev1.Resource("persistentvolumeclaims"), "buildkit-cache-arm64"))
	mockPVC.On("Create", mock.Anything, mock.Anything, mock.Anything).Return(&corev1.PersistentVolumeClaim{}, nil)

	err := kr.ensurePersistentVolumesForArchitecture("arm64")
	assert.NoError(t, err)

	mockPVC.AssertExpectations(t)
	// Should call Create twice, once for each PVC
	mockPVC.AssertNumberOfCalls(t, "Create", 2)
}

func TestKubernetesRuntime_ensurePersistentVolumesForArchitecture_GetError(t *testing.T) {
	mockPVC := &MockPVCInterface{}
	mockPod := &MockPodInterface{}
	mockNamespace := &MockNamespaceInterface{}
	mockCoreV1 := &MockCoreV1Client{pvcInterface: mockPVC, podInterface: mockPod, namespaceInterface: mockNamespace}
	mockClient := &MockKubernetesClient{coreV1Client: mockCoreV1}

	kr := &KubernetesRuntime{
		clientset: mockClient,
		namespace: "default",
	}

	// Test case: PVC Get returns a generic error (not a not-found error)
	mockPVC.On("Get", mock.Anything, "docker-cache-arm64", mock.Anything).Return((*corev1.PersistentVolumeClaim)(nil), assert.AnError)
	mockPVC.On("Get", mock.Anything, "buildkit-cache-arm64", mock.Anything).Return((*corev1.PersistentVolumeClaim)(nil), assert.AnError)

	err := kr.ensurePersistentVolumesForArchitecture("arm64")
	// should continue despite the error and not return it (just log it)
	assert.NoError(t, err)

	mockPVC.AssertExpectations(t)
}

func TestKubernetesRuntime_ensurePersistentVolumesForArchitecture_CreateError(t *testing.T) {
	mockPVC := &MockPVCInterface{}
	mockPod := &MockPodInterface{}
	mockNamespace := &MockNamespaceInterface{}
	mockCoreV1 := &MockCoreV1Client{pvcInterface: mockPVC, podInterface: mockPod, namespaceInterface: mockNamespace}
	mockClient := &MockKubernetesClient{coreV1Client: mockCoreV1}

	kr := &KubernetesRuntime{
		clientset: mockClient,
		namespace: "default",
	}

	// Test case: PVC doesn't exist and Create fails
	mockPVC.On("Get", mock.Anything, "docker-cache-arm64", mock.Anything).Return((*corev1.PersistentVolumeClaim)(nil), errors.NewNotFound(corev1.Resource("persistentvolumeclaims"), "docker-cache-arm64"))
	mockPVC.On("Get", mock.Anything, "buildkit-cache-arm64", mock.Anything).Return((*corev1.PersistentVolumeClaim)(nil), errors.NewNotFound(corev1.Resource("persistentvolumeclaims"), "buildkit-cache-arm64"))
	mockPVC.On("Create", mock.Anything, mock.Anything, mock.Anything).Return((*corev1.PersistentVolumeClaim)(nil), assert.AnError).Times(2)

	err := kr.ensurePersistentVolumesForArchitecture("arm64")
	// should continue despite the error and not return it (just log it)
	assert.NoError(t, err)

	mockPVC.AssertExpectations(t)
}

func TestKubernetesRuntime_cleanupUnusedPVCs_NoCleanupNeeded(t *testing.T) {
	mockPVC := &MockPVCInterface{}
	mockPod := &MockPodInterface{}
	mockNamespace := &MockNamespaceInterface{}
	mockCoreV1 := &MockCoreV1Client{pvcInterface: mockPVC, podInterface: mockPod, namespaceInterface: mockNamespace}
	mockClient := &MockKubernetesClient{coreV1Client: mockCoreV1}

	kr := &KubernetesRuntime{
		clientset: mockClient,
		namespace: "default",
	}

	// Test case: No PVCs to clean up (all in use)
	mockPVC.On("List", mock.Anything, metav1.ListOptions{LabelSelector: "app=github-runner,type=cache"}).Return(&corev1.PersistentVolumeClaimList{
		Items: []corev1.PersistentVolumeClaim{
			{
				ObjectMeta: metav1.ObjectMeta{Name: "docker-cache-amd64"},
			},
			{
				ObjectMeta: metav1.ObjectMeta{Name: "buildkit-cache-amd64"},
			},
		},
	}, nil)

	mockPod.On("List", mock.Anything, metav1.ListOptions{LabelSelector: "app=github-runner"}).Return(&corev1.PodList{
		Items: []corev1.Pod{
			{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pod",
				},
				Spec: corev1.PodSpec{
					Volumes: []corev1.Volume{
						{
							VolumeSource: corev1.VolumeSource{
								PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "docker-cache-amd64"},
							},
						},
						{
							VolumeSource: corev1.VolumeSource{
								PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "buildkit-cache-amd64"},
							},
						},
					},
				},
			},
		},
	}, nil)

	err := kr.cleanupUnusedPVCs()
	assert.NoError(t, err)

	mockPVC.AssertExpectations(t)
	mockPod.AssertExpectations(t)
}

func TestKubernetesRuntime_cleanupUnusedPVCs_CleanupPerformed(t *testing.T) {
	mockPVC := &MockPVCInterface{}
	mockPod := &MockPodInterface{}
	mockNamespace := &MockNamespaceInterface{}
	mockCoreV1 := &MockCoreV1Client{pvcInterface: mockPVC, podInterface: mockPod, namespaceInterface: mockNamespace}
	mockClient := &MockKubernetesClient{coreV1Client: mockCoreV1}

	kr := &KubernetesRuntime{
		clientset: mockClient,
		namespace: "default",
	}

	// Test case: Some PVCs are unused
	mockPVC.On("List", mock.Anything, metav1.ListOptions{LabelSelector: "app=github-runner,type=cache"}).Return(&corev1.PersistentVolumeClaimList{
		Items: []corev1.PersistentVolumeClaim{
			{
				ObjectMeta: metav1.ObjectMeta{
					Name:              "docker-cache-amd64",
					CreationTimestamp: metav1.Now(),
				},
			},
			{
				ObjectMeta: metav1.ObjectMeta{
					Name:              "buildkit-cache-amd64",
					CreationTimestamp: metav1.Now(),
				},
			},
			{
				ObjectMeta: metav1.ObjectMeta{
					Name:              "docker-cache-arm64",
					CreationTimestamp: metav1.NewTime(time.Now().Add(-2 * time.Hour)), // Older than 1 hour
				},
			},
		},
	}, nil)

	mockPod.On("List", mock.Anything, metav1.ListOptions{LabelSelector: "app=github-runner"}).Return(&corev1.PodList{
		Items: []corev1.Pod{
			{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pod",
				},
				Spec: corev1.PodSpec{
					Volumes: []corev1.Volume{
						{
							VolumeSource: corev1.VolumeSource{
								PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "docker-cache-amd64"},
							},
						},
						{
							VolumeSource: corev1.VolumeSource{
								PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "buildkit-cache-amd64"},
							},
						},
					},
				},
			},
		},
	}, nil)

	mockPVC.On("Delete", mock.Anything, "docker-cache-arm64", mock.Anything).Return(nil)

	err := kr.cleanupUnusedPVCs()
	assert.NoError(t, err)

	mockPVC.AssertExpectations(t)
	mockPod.AssertExpectations(t)
}

func TestKubernetesRuntime_cleanupUnusedPVCs_PVCListError(t *testing.T) {
	mockPVC := &MockPVCInterface{}
	mockPod := &MockPodInterface{}
	mockNamespace := &MockNamespaceInterface{}
	mockCoreV1 := &MockCoreV1Client{pvcInterface: mockPVC, podInterface: mockPod, namespaceInterface: mockNamespace}
	mockClient := &MockKubernetesClient{coreV1Client: mockCoreV1}

	kr := &KubernetesRuntime{
		clientset: mockClient,
		namespace: "default",
	}

	// Test case: PVC List returns an error
	mockPVC.On("List", mock.Anything, metav1.ListOptions{LabelSelector: "app=github-runner,type=cache"}).Return((*corev1.PersistentVolumeClaimList)(nil), assert.AnError)

	err := kr.cleanupUnusedPVCs()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to list PVCs")

	mockPVC.AssertExpectations(t)
}

func TestKubernetesRuntime_cleanupUnusedPVCs_PodListError(t *testing.T) {
	mockPVC := &MockPVCInterface{}
	mockPod := &MockPodInterface{}
	mockNamespace := &MockNamespaceInterface{}
	mockCoreV1 := &MockCoreV1Client{pvcInterface: mockPVC, podInterface: mockPod, namespaceInterface: mockNamespace}
	mockClient := &MockKubernetesClient{coreV1Client: mockCoreV1}

	kr := &KubernetesRuntime{
		clientset: mockClient,
		namespace: "default",
	}

	// Test case: PVCs exist, but pod List returns an error
	mockPVC.On("List", mock.Anything, metav1.ListOptions{LabelSelector: "app=github-runner,type=cache"}).Return(&corev1.PersistentVolumeClaimList{}, nil)
	mockPod.On("List", mock.Anything, metav1.ListOptions{LabelSelector: "app=github-runner"}).Return((*corev1.PodList)(nil), assert.AnError)

	err := kr.cleanupUnusedPVCs()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to list pods")

	mockPVC.AssertExpectations(t)
	mockPod.AssertExpectations(t)
}

// TestKubernetesRuntime_StopRunner tests the StopRunner method
func TestKubernetesRuntime_StopRunner(t *testing.T) {
	mockPod := &MockPodInterface{}
	mockCoreV1 := &MockCoreV1Client{podInterface: mockPod}
	mockClient := &MockKubernetesClient{coreV1Client: mockCoreV1}

	kr := &KubernetesRuntime{
		clientset: mockClient,
		namespace: "default",
	}

	// Mock the Pod List call to find the pod by UID
	mockPod.On("List", mock.Anything, metav1.ListOptions{
		FieldSelector: fields.OneTermEqualSelector("metadata.uid", "test-uid").String(),
	}).Return(&corev1.PodList{
		Items: []corev1.Pod{
			{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-runner",
					UID:  "test-uid",
				},
			},
		},
	}, nil)

	// Mock the Pod Delete call
	mockPod.On("Delete", mock.Anything, "test-runner", mock.Anything).Return(nil)

	err := kr.StopRunner("test-uid")

	assert.NoError(t, err)
	mockPod.AssertExpectations(t)
}

// TestKubernetesRuntime_StopRunner_NotFound tests the StopRunner method when pod is not found
func TestKubernetesRuntime_StopRunner_NotFound(t *testing.T) {
	mockPod := &MockPodInterface{}
	mockCoreV1 := &MockCoreV1Client{podInterface: mockPod}
	mockClient := &MockKubernetesClient{coreV1Client: mockCoreV1}

	kr := &KubernetesRuntime{
		clientset: mockClient,
		namespace: "default",
	}

	// Mock the Pod List call to return empty list (pod not found)
	mockPod.On("List", mock.Anything, metav1.ListOptions{
		FieldSelector: fields.OneTermEqualSelector("metadata.uid", "nonexistent-uid").String(),
	}).Return(&corev1.PodList{Items: []corev1.Pod{}}, nil)

	err := kr.StopRunner("nonexistent-uid")

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "pod with UID nonexistent-uid not found")
	mockPod.AssertExpectations(t)
}

// TestKubernetesRuntime_StopRunner_DeleteError tests the StopRunner method when delete fails
func TestKubernetesRuntime_StopRunner_DeleteError(t *testing.T) {
	mockPod := &MockPodInterface{}
	mockCoreV1 := &MockCoreV1Client{podInterface: mockPod}
	mockClient := &MockKubernetesClient{coreV1Client: mockCoreV1}

	kr := &KubernetesRuntime{
		clientset: mockClient,
		namespace: "default",
	}

	// Mock the Pod List call to find the pod by UID
	mockPod.On("List", mock.Anything, metav1.ListOptions{
		FieldSelector: fields.OneTermEqualSelector("metadata.uid", "test-uid").String(),
	}).Return(&corev1.PodList{
		Items: []corev1.Pod{
			{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-runner",
					UID:  "test-uid",
				},
			},
		},
	}, nil)

	// Mock the Pod Delete call to return an error
	mockPod.On("Delete", mock.Anything, "test-runner", mock.Anything).Return(assert.AnError)

	err := kr.StopRunner("test-uid")

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to delete pod")
	mockPod.AssertExpectations(t)
}

// TestKubernetesRuntime_StopAllRunners tests the StopAllRunners method
func TestKubernetesRuntime_StopAllRunners(t *testing.T) {
	mockPod := &MockPodInterface{}
	mockCoreV1 := &MockCoreV1Client{podInterface: mockPod}
	mockClient := &MockKubernetesClient{coreV1Client: mockCoreV1}

	kr := &KubernetesRuntime{
		clientset: mockClient,
		namespace: "default",
	}

	// Mock the Pod List call to return running pods
	mockPod.On("List", mock.Anything, metav1.ListOptions{LabelSelector: "app=github-runner,architecture=amd64"}).Return(&corev1.PodList{
		Items: []corev1.Pod{
			{
				ObjectMeta: metav1.ObjectMeta{
					Name: "runner-1",
					UID:  "uid-1",
				},
				Status: corev1.PodStatus{
					Phase: corev1.PodRunning,
				},
			},
			{
				ObjectMeta: metav1.ObjectMeta{
					Name: "runner-2",
					UID:  "uid-2",
				},
				Status: corev1.PodStatus{
					Phase: corev1.PodRunning,
				},
			},
		},
	}, nil)

	// Mock the Pod List calls that happen in StopRunner for each pod
	mockPod.On("List", mock.Anything, metav1.ListOptions{FieldSelector: fields.OneTermEqualSelector("metadata.uid", "uid-1").String()}).Return(&corev1.PodList{
		Items: []corev1.Pod{
			{
				ObjectMeta: metav1.ObjectMeta{
					Name: "runner-1",
					UID:  "uid-1",
				},
			},
		},
	}, nil)
	mockPod.On("List", mock.Anything, metav1.ListOptions{FieldSelector: fields.OneTermEqualSelector("metadata.uid", "uid-2").String()}).Return(&corev1.PodList{
		Items: []corev1.Pod{
			{
				ObjectMeta: metav1.ObjectMeta{
					Name: "runner-2",
					UID:  "uid-2",
				},
			},
		},
	}, nil)

	// Mock the Pod Delete calls for each pod
	mockPod.On("Delete", mock.Anything, "runner-1", mock.Anything).Return(nil)
	mockPod.On("Delete", mock.Anything, "runner-2", mock.Anything).Return(nil)

	err := kr.StopAllRunners("amd64")

	assert.NoError(t, err)
	mockPod.AssertExpectations(t)
}
// TestKubernetesRuntime_CleanupOldRunners tests the CleanupOldRunners method
func TestKubernetesRuntime_CleanupOldRunners(t *testing.T) {
	mockPod := &MockPodInterface{}
	mockCoreV1 := &MockCoreV1Client{podInterface: mockPod}
	mockClient := &MockKubernetesClient{coreV1Client: mockCoreV1}

	kr := &KubernetesRuntime{
		clientset: mockClient,
		namespace: "default",
	}

	// Mock the Pod List call to return a mix of old and new pods
	// For this test, we want to have at least one pod that will be cleaned up
	mockPod.On("List", mock.Anything, metav1.ListOptions{LabelSelector: "app=github-runner,architecture=amd64"}).Return(&corev1.PodList{
		Items: []corev1.Pod{
			{
				ObjectMeta: metav1.ObjectMeta{
					Name: "old-runner",
					UID:  "old-uid",
				},
				Status: corev1.PodStatus{
					Phase: corev1.PodRunning,
				},
			},
			{
				ObjectMeta: metav1.ObjectMeta{
					Name: "new-runner",
					UID:  "new-uid",
				},
				Status: corev1.PodStatus{
					Phase: corev1.PodRunning,
				},
			},
		},
	}, nil)

	// Mock the Pod List calls that happen in StopRunner for both pods
	mockPod.On("List", mock.Anything, metav1.ListOptions{FieldSelector: fields.OneTermEqualSelector("metadata.uid", "old-uid").String()}).Return(&corev1.PodList{
		Items: []corev1.Pod{
			{
				ObjectMeta: metav1.ObjectMeta{
					Name: "old-runner",
					UID:  "old-uid",
				},
			},
		},
	}, nil)
	mockPod.On("List", mock.Anything, metav1.ListOptions{FieldSelector: fields.OneTermEqualSelector("metadata.uid", "new-uid").String()}).Return(&corev1.PodList{
		Items: []corev1.Pod{
			{
				ObjectMeta: metav1.ObjectMeta{
					Name: "new-runner",
					UID:  "new-uid",
				},
			},
		},
	}, nil)

	// Mock the Pod Delete calls for both pods (since both are older than 0 duration)
	mockPod.On("Delete", mock.Anything, "old-runner", mock.Anything).Return(nil)
	mockPod.On("Delete", mock.Anything, "new-runner", mock.Anything).Return(nil)

	// Create a duration that makes the old pod eligible for cleanup
	// In the actual implementation, we're comparing time.Now() - pod creation time > maxAge
	// But in our test, we don't have real creation timestamps, so let's pass a very small duration
	// to make sure any pod would be cleaned up
	err := kr.CleanupOldRunners("amd64", 0*time.Minute)

	assert.NoError(t, err)
	mockPod.AssertExpectations(t)
}

// TestKubernetesRuntime_GetRunningRunnersStatuses tests the GetRunningRunners method with different pod statuses
func TestKubernetesRuntime_GetRunningRunnersStatuses(t *testing.T) {
	mockPod := &MockPodInterface{}
	mockCoreV1 := &MockCoreV1Client{podInterface: mockPod}
	mockClient := &MockKubernetesClient{coreV1Client: mockCoreV1}

	kr := &KubernetesRuntime{
		clientset: mockClient,
		namespace: "default",
	}

	// Mock the Pod List call to return a mix of running, pending, and failed pods
	mockPod.On("List", mock.Anything, metav1.ListOptions{LabelSelector: "app=github-runner,architecture=amd64"}).Return(&corev1.PodList{
		Items: []corev1.Pod{
			{
				ObjectMeta: metav1.ObjectMeta{
					Name: "running-runner",
					UID:  "running-uid",
				},
				Status: corev1.PodStatus{
					Phase: corev1.PodRunning,
				},
			},
			{
				ObjectMeta: metav1.ObjectMeta{
					Name: "pending-runner",
					UID:  "pending-uid",
				},
				Status: corev1.PodStatus{
					Phase: corev1.PodPending,
				},
			},
			{
				ObjectMeta: metav1.ObjectMeta{
					Name: "failed-runner",
					UID:  "failed-uid",
				},
				Status: corev1.PodStatus{
					Phase: corev1.PodFailed,
				},
			},
		},
	}, nil)

	// Call GetRunningRunners with specific architecture
	runners, err := kr.GetRunningRunners("amd64")
	
	// Should return running, pending, and failed runners (excludes only succeeded)
	assert.NoError(t, err)
	assert.Len(t, runners, 3)
	
	mockPod.AssertExpectations(t)
}

// TestKubernetesRuntime_GetRunningRunnersArchitectures tests the GetRunningRunners method with different architectures
func TestKubernetesRuntime_GetRunningRunnersArchitectures(t *testing.T) {
	mockPod := &MockPodInterface{}
	mockCoreV1 := &MockCoreV1Client{podInterface: mockPod}
	mockClient := &MockKubernetesClient{coreV1Client: mockCoreV1}

	kr := &KubernetesRuntime{
		clientset: mockClient,
		namespace: "default",
	}

	// Mock the Pod List call to return runners of all architectures
	mockPod.On("List", mock.Anything, metav1.ListOptions{LabelSelector: "app=github-runner"}).Return(&corev1.PodList{
		Items: []corev1.Pod{
			{
				ObjectMeta: metav1.ObjectMeta{
					Name: "amd64-runner",
					UID:  "amd64-uid",
				},
				Status: corev1.PodStatus{
					Phase: corev1.PodRunning,
				},
			},
			{
				ObjectMeta: metav1.ObjectMeta{
					Name: "arm64-runner",
					UID:  "arm64-uid",
				},
				Status: corev1.PodStatus{
					Phase: corev1.PodRunning,
				},
			},
		},
	}, nil)

	// Call GetRunningRunners with "all" architecture
	runners, err := kr.GetRunningRunners("all")
	
	assert.NoError(t, err)
	assert.Len(t, runners, 2)
	
	mockPod.AssertExpectations(t)
}

// TestKubernetesRuntime_GetContainerLogs_NotFound tests the GetContainerLogs method when pod is not found
func TestKubernetesRuntime_GetContainerLogs_NotFound(t *testing.T) {
	mockPod := &MockPodInterface{}
	mockCoreV1 := &MockCoreV1Client{podInterface: mockPod}
	mockClient := &MockKubernetesClient{coreV1Client: mockCoreV1}

	kr := &KubernetesRuntime{
		clientset: mockClient,
		namespace: "default",
	}

	// Mock the Pod List call to return empty list (pod not found)
	mockPod.On("List", mock.Anything, metav1.ListOptions{FieldSelector: fields.OneTermEqualSelector("metadata.uid", "nonexistent-uid").String()}).Return(&corev1.PodList{Items: []corev1.Pod{}}, nil)

	// Call GetContainerLogs
	logs, err := kr.GetContainerLogs("nonexistent-uid", 100)
	
	assert.Error(t, err)
	assert.Equal(t, "", logs)
	assert.Contains(t, err.Error(), "pod with UID nonexistent-uid not found")
	
	mockPod.AssertExpectations(t)
}

// TestKubernetesRuntime_GetRunnerRepository tests the GetRunnerRepository method
func TestKubernetesRuntime_GetRunnerRepository(t *testing.T) {
	mockPod := &MockPodInterface{}
	mockCoreV1 := &MockCoreV1Client{podInterface: mockPod}
	mockClient := &MockKubernetesClient{coreV1Client: mockCoreV1}

	kr := &KubernetesRuntime{
		clientset: mockClient,
		namespace: "default",
	}

	// Mock the Pod List call to find the pod by UID with repo-full-name annotation
	mockPod.On("List", mock.Anything, metav1.ListOptions{
		FieldSelector: fields.OneTermEqualSelector("metadata.uid", "test-uid").String(),
	}).Return(&corev1.PodList{
		Items: []corev1.Pod{
			{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-runner",
					UID:  "test-uid",
					Annotations: map[string]string{
						"repo-full-name": "test/owner",
					},
				},
			},
		},
	}, nil)

	repo, err := kr.GetRunnerRepository("test-uid")

	assert.NoError(t, err)
	assert.Equal(t, "test/owner", repo)
	mockPod.AssertExpectations(t)
}

// TestKubernetesRuntime_GetRunnerRepositoryFromEnv tests the GetRunnerRepository method extracting from env vars
func TestKubernetesRuntime_GetRunnerRepositoryFromEnv(t *testing.T) {
	mockPod := &MockPodInterface{}
	mockCoreV1 := &MockCoreV1Client{podInterface: mockPod}
	mockClient := &MockKubernetesClient{coreV1Client: mockCoreV1}

	kr := &KubernetesRuntime{
		clientset: mockClient,
		namespace: "default",
	}

	// Mock the Pod List call to find the pod by UID without annotations but with env vars
	mockPod.On("List", mock.Anything, metav1.ListOptions{
		FieldSelector: fields.OneTermEqualSelector("metadata.uid", "test-uid").String(),
	}).Return(&corev1.PodList{
		Items: []corev1.Pod{
			{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-runner",
					UID:  "test-uid",
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name: "runner",
							Env: []corev1.EnvVar{
								{Name: "REPO_FULL_NAME", Value: "test/owner"},
							},
						},
					},
				},
			},
		},
	}, nil)

	repo, err := kr.GetRunnerRepository("test-uid")

	assert.NoError(t, err)
	assert.Equal(t, "test/owner", repo)
	mockPod.AssertExpectations(t)
}

// TestKubernetesRuntime_GetRunnerRepositoryFromURL tests the GetRunnerRepository method extracting from URL env var
func TestKubernetesRuntime_GetRunnerRepositoryFromURL(t *testing.T) {
	mockPod := &MockPodInterface{}
	mockCoreV1 := &MockCoreV1Client{podInterface: mockPod}
	mockClient := &MockKubernetesClient{coreV1Client: mockCoreV1}

	kr := &KubernetesRuntime{
		clientset: mockClient,
		namespace: "default",
	}

	// Mock the Pod List call to find the pod by UID without annotations but with REPO_URL env var
	mockPod.On("List", mock.Anything, metav1.ListOptions{
		FieldSelector: fields.OneTermEqualSelector("metadata.uid", "test-uid").String(),
	}).Return(&corev1.PodList{
		Items: []corev1.Pod{
			{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-runner",
					UID:  "test-uid",
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name: "runner",
							Env: []corev1.EnvVar{
								{Name: "REPO_URL", Value: "https://github.com/test/owner"},
							},
						},
					},
				},
			},
		},
	}, nil)

	repo, err := kr.GetRunnerRepository("test-uid")

	assert.NoError(t, err)
	assert.Equal(t, "test/owner", repo)
	mockPod.AssertExpectations(t)
}

// TestKubernetesRuntime_GetRunnerRepository_NotFound tests the GetRunnerRepository method when pod is not found
func TestKubernetesRuntime_GetRunnerRepository_NotFound(t *testing.T) {
	mockPod := &MockPodInterface{}
	mockCoreV1 := &MockCoreV1Client{podInterface: mockPod}
	mockClient := &MockKubernetesClient{coreV1Client: mockCoreV1}

	kr := &KubernetesRuntime{
		clientset: mockClient,
		namespace: "default",
	}

	// Mock the Pod List call to return empty list (pod not found)
	mockPod.On("List", mock.Anything, metav1.ListOptions{
		FieldSelector: fields.OneTermEqualSelector("metadata.uid", "nonexistent-uid").String(),
	}).Return(&corev1.PodList{Items: []corev1.Pod{}}, nil)

	repo, err := kr.GetRunnerRepository("nonexistent-uid")

	assert.Error(t, err)
	assert.Equal(t, "", repo)
	assert.Contains(t, err.Error(), "pod with UID nonexistent-uid not found")
	mockPod.AssertExpectations(t)
}