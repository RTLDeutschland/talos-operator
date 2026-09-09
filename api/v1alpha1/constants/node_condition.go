package constants

// Conditions:
const (
	NodeConditionReady                   = "Ready"
	NodeConditionProvisioned             = "Provisioned"
	NodeConditionConfigInSync            = "ConfigInSync"
	NodeConditionConfigDriftDetected     = "ConfigDriftDetected"
	NodeConditionKubernetesInSync        = "KubernetesInSync"
	NodeConditionKubernetesReady         = "KubernetesReady"
	NodeConditionKubernetesUnschedulable = "KubernetesUnschedulable"
	NodeConditionNeedsInitialUpdate      = "NeedsInitialUpdate"
)

// Reasons:
const (
	// Provisioned:
	NodeConditionReasonInvalidArguments    = NodeOperationReasonInvalidArguments
	NodeConditionReasonSkippedProvisioning = NodeOperationReasonSkippedProvisioning
	NodeConditionReasonFailedApply         = NodeOperationReasonFailedApply
	NodeConditionReasonSuccessfulApply     = NodeOperationReasonSuccessfulApply

	// Ready & Provisioned:
	NodeConditionReasonNodeNotProvisioned = "NodeNotProvisioned"

	// Ready:
	NodeConditionReasonNodeReachable             = NodeReasonNodeReachable
	NodeConditionReasonNodeUnreachable           = NodeReasonNodeUnreachable
	NodeConditionReasonWaitingForNode            = "WaitingForNode"
	NodeConditionReasonReset                     = "NodeReset"
	NodeConditionReasonFailedImageValidation     = NodeOperationReasonFailedImageValidation
	NodeConditionReasonConfigGenerationFailed    = NodeReasonConfigGenerationFailed
	NodeConditionReasonConfigSerializationFailed = NodeReasonConfigSerializationFailed
	NodeConditionReasonNodeStatusFailed          = "NodeStatusFailed"
	NodeConditionReasonClusterNotFound           = "ClusterNotFound"

	// KubernetesReady:
	NodeConditionReasonKubernetesNodeNotFound = "KubernetesNodeNotFound"
	NodeConditionReasonKubernetesNodeReady    = "KubernetesNodeReady"
	NodeConditionReasonKubernetesNodeNotReady = "KubernetesNodeNotReady"

	// KubernetesUnschedulable:
	NodeConditionReasonKubernetesNodeUnschedulable = "KubernetesNodeUnschedulable"
	NodeConditionReasonKubernetesNodeSchedulable   = "KubernetesNodeSchedulable"

	// NeedsInitialUpdate:
	NodeConditionReasonNodeNeedsInitialUpdate = "NodeNeedsInitialUpdate"

	// ConfigDriftDetected:
	NodeConditionReasonConfigHasDrifted = "ConfigHasDrifted"

	// ConfigInSync:
	NodeConditionReasonConfigInSync    = "ConfigInSync"
	NodeConditionReasonConfigOutOfSync = "ConfigOutOfSync"

	// KubernetesInSync:
	NodeConditionReasonKubernetesUpgradeNeeded = "KubernetesUpgradeNeeded"
	NodeConditionReasonKubernetesUpToDate      = "KubernetesUpToDate"
)
