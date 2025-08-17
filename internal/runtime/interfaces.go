package runtime

import (
	"context"
	"io"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/opencontainers/image-spec/specs-go/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	policyv1beta1 "k8s.io/api/policy/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	applyconfigcorev1 "k8s.io/client-go/applyconfigurations/core/v1"
	"k8s.io/client-go/rest"
	corev1client "k8s.io/client-go/kubernetes/typed/core/v1"
)

// DockerClientInterface defines the Docker client methods we need to mock
type DockerClientInterface interface {
	Ping(ctx context.Context) (types.Ping, error)
	ContainerCreate(ctx context.Context, config *container.Config, hostConfig *container.HostConfig, networkingConfig *network.NetworkingConfig, platform *v1.Platform, containerName string) (container.CreateResponse, error)
	ContainerStart(ctx context.Context, containerID string, options types.ContainerStartOptions) error
	ContainerRemove(ctx context.Context, containerID string, options types.ContainerRemoveOptions) error
	ContainerList(ctx context.Context, options types.ContainerListOptions) ([]types.Container, error)
	ContainerStop(ctx context.Context, containerID string, options container.StopOptions) error
	ContainerLogs(ctx context.Context, containerID string, options types.ContainerLogsOptions) (io.ReadCloser, error)
	ContainerInspect(ctx context.Context, containerID string) (types.ContainerJSON, error)
	ImageList(ctx context.Context, options types.ImageListOptions) ([]types.ImageSummary, error)
	ImageInspectWithRaw(ctx context.Context, imageID string) (types.ImageInspect, []byte, error)
	ImagePull(ctx context.Context, refStr string, options types.ImagePullOptions) (io.ReadCloser, error)
	ImageRemove(ctx context.Context, imageID string, options types.ImageRemoveOptions) ([]types.ImageDeleteResponseItem, error)
}

// KubernetesClientInterface defines the Kubernetes client methods we need to mock
type KubernetesClientInterface interface {
	CoreV1() corev1client.CoreV1Interface
}

// MockableNamespaceInterface defines methods for mocking namespace operations
type MockableNamespaceInterface interface {
	Get(ctx context.Context, name string, opts metav1.GetOptions) (*corev1.Namespace, error)
	Create(ctx context.Context, namespace *corev1.Namespace, opts metav1.CreateOptions) (*corev1.Namespace, error)
	Update(ctx context.Context, namespace *corev1.Namespace, opts metav1.UpdateOptions) (*corev1.Namespace, error)
	Delete(ctx context.Context, name string, opts metav1.DeleteOptions) error
	List(ctx context.Context, opts metav1.ListOptions) (*corev1.NamespaceList, error)
	Watch(ctx context.Context, opts metav1.ListOptions) (watch.Interface, error)
	Patch(ctx context.Context, name string, pt k8stypes.PatchType, data []byte, opts metav1.PatchOptions, subresources ...string) (*corev1.Namespace, error)
	Apply(ctx context.Context, namespace *applyconfigcorev1.NamespaceApplyConfiguration, opts metav1.ApplyOptions) (*corev1.Namespace, error)
	ApplyStatus(ctx context.Context, namespace *applyconfigcorev1.NamespaceApplyConfiguration, opts metav1.ApplyOptions) (*corev1.Namespace, error)
	UpdateStatus(ctx context.Context, namespace *corev1.Namespace, opts metav1.UpdateOptions) (*corev1.Namespace, error)
	Finalize(ctx context.Context, namespace *corev1.Namespace, opts metav1.UpdateOptions) (*corev1.Namespace, error)
}

// MockablePodInterface defines methods for mocking pod operations
type MockablePodInterface interface {
	Create(ctx context.Context, pod *corev1.Pod, opts metav1.CreateOptions) (*corev1.Pod, error)
	Update(ctx context.Context, pod *corev1.Pod, opts metav1.UpdateOptions) (*corev1.Pod, error)
	UpdateStatus(ctx context.Context, pod *corev1.Pod, opts metav1.UpdateOptions) (*corev1.Pod, error)
	Delete(ctx context.Context, name string, opts metav1.DeleteOptions) error
	DeleteCollection(ctx context.Context, opts metav1.DeleteOptions, listOpts metav1.ListOptions) error
	Get(ctx context.Context, name string, opts metav1.GetOptions) (*corev1.Pod, error)
	List(ctx context.Context, opts metav1.ListOptions) (*corev1.PodList, error)
	Watch(ctx context.Context, opts metav1.ListOptions) (watch.Interface, error)
	Patch(ctx context.Context, name string, pt k8stypes.PatchType, data []byte, opts metav1.PatchOptions, subresources ...string) (*corev1.Pod, error)
	Apply(ctx context.Context, pod *applyconfigcorev1.PodApplyConfiguration, opts metav1.ApplyOptions) (*corev1.Pod, error)
	ApplyStatus(ctx context.Context, pod *applyconfigcorev1.PodApplyConfiguration, opts metav1.ApplyOptions) (*corev1.Pod, error)
	GetLogs(name string, opts metav1.GetOptions) (*rest.Request, error)
	Bind(ctx context.Context, binding *corev1.Binding, opts metav1.CreateOptions) error
	Evict(ctx context.Context, eviction *policyv1beta1.Eviction) error
	EvictV1(ctx context.Context, eviction *policyv1.Eviction) error
}

// MockablePVCInterface defines methods for mocking PVC operations
type MockablePVCInterface interface {
	Get(ctx context.Context, name string, opts metav1.GetOptions) (*corev1.PersistentVolumeClaim, error)
	Create(ctx context.Context, pvc *corev1.PersistentVolumeClaim, opts metav1.CreateOptions) (*corev1.PersistentVolumeClaim, error)
	Update(ctx context.Context, pvc *corev1.PersistentVolumeClaim, opts metav1.UpdateOptions) (*corev1.PersistentVolumeClaim, error)
	UpdateStatus(ctx context.Context, pvc *corev1.PersistentVolumeClaim, opts metav1.UpdateOptions) (*corev1.PersistentVolumeClaim, error)
	Delete(ctx context.Context, name string, opts metav1.DeleteOptions) error
	DeleteCollection(ctx context.Context, opts metav1.DeleteOptions, listOpts metav1.ListOptions) error
	List(ctx context.Context, opts metav1.ListOptions) (*corev1.PersistentVolumeClaimList, error)
	Watch(ctx context.Context, opts metav1.ListOptions) (watch.Interface, error)
	Patch(ctx context.Context, name string, pt k8stypes.PatchType, data []byte, opts metav1.PatchOptions, subresources ...string) (*corev1.PersistentVolumeClaim, error)
	Apply(ctx context.Context, pvc *applyconfigcorev1.PersistentVolumeClaimApplyConfiguration, opts metav1.ApplyOptions) (*corev1.PersistentVolumeClaim, error)
	ApplyStatus(ctx context.Context, pvc *applyconfigcorev1.PersistentVolumeClaimApplyConfiguration, opts metav1.ApplyOptions) (*corev1.PersistentVolumeClaim, error)
}

// MockableCoreV1Interface defines methods for mocking CoreV1 operations
type MockableCoreV1Interface interface {
	Namespaces() MockableNamespaceInterface
	Pods(namespace string) MockablePodInterface
	PersistentVolumeClaims(namespace string) MockablePVCInterface
}