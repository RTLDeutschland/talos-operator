package constants

// Cluster operation phases:
const (
	ClusterOperationPhasePending     = "Pending"
	ClusterOperationPhasePreflight   = "Preflight"
	ClusterOperationPhaseInProgress  = "InProgress"
	ClusterOperationPhasePostflight  = "Postflight"
	ClusterOperationPhaseDone        = "Done"
	ClusterOperationPhaseFailed      = "Failed"
	ClusterSubOperationPhaseApplying = "Applying"
)

// Cluster operation types:
const (
	ClusterOperationTypeRollingReboot     = "RollingReboot"
	ClusterOperationTypeRollingApply      = "RollingApply"
	ClusterOperationTypeKubernetesUpgrade = "KubernetesUpgrade"
	ClusterOperationTypeBootstrap         = "Bootstrap"
)

// Cluster operation reasons:
const (
	ClusterOperationReasonNodeNotReady        = "NodeNotReady"
	ClusterOperationReasonNodeConfigDrift     = "NodeConfigDrift"
	ClusterOperationReasonNodeConfigOutOfSync = "NodeConfigOutOfSync"
	ClusterOperationReasonNoControlPlanes     = "NoControlPlanes"
	ClusterOperationReasonNodeGetFailed       = "NodeGetFailed" // indicates that a node was deleted during a cluster operation
	ClusterOperationReasonSubOperationFailed  = "SubOperationFailed"
)
