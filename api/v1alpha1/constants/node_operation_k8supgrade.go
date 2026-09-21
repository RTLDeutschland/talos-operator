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
