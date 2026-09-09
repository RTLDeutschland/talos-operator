package constants

// Kubernetes components used for KubernetesComponentUpgrade node operations:
const (
	KubernetesComponentAPIServer         = "apiserver"
	KubernetesComponentControllerManager = "controllerManager"
	KubernetesComponentScheduler         = "scheduler"
	KubernetesComponentKubelet           = "kubelet"
)

// Node roles:
const (
	NodeRoleControlplane = "controlplane"
	NodeRoleWorker       = "worker"
)

// Node reset modes:
const (
	NodeResetModeAll        = "all"
	NodeResetModeSystemDisk = "system-disk"
	NodeResetModeUserDisks  = "user-disks"
)

// Node finalizers:
const (
	NodeFinalizer = "talos.rtl.de/finalizer"
)
