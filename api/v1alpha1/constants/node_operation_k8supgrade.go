package constants

// Phases relating to Kubernetes component upgrades
const (
	NodeOperationPhaseKubeletReboot = "KubeletReboot"
	NodeOperationPhaseVerifyReboot  = "VerifyReboot"
)

// Reasons relating to Kubernetes component upgrades
const (
	NodeOperationReasonSuccessfulReboot = "SuccessfulReboot"
)

// Kubernetes components used for KubernetesComponentUpgrade node operations:
const (
	KubernetesComponentAPIServer         = "apiserver"
	KubernetesComponentControllerManager = "controllerManager"
	KubernetesComponentScheduler         = "scheduler"
	KubernetesComponentKubelet           = "kubelet"
)
