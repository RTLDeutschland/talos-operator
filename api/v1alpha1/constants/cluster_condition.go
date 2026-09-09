package constants

// Conditions:
const (
	ClusterConditionNodesInSync             = "ClusterNodesInSync"
	ClusterConditionNodesKubernetesUpToDate = "ClusterNodesKubernetesUpToDate"
	ClusterConditionBootstrapped            = "Bootstrapped"
)

// Condition reasons:
const (
	// ClusterNodesInSync:
	ClusterConditionReasonNodesOutOfSync = "NodesOutOfSync"
	ClusterConditionReasonAllNodesInSync = "AllNodesInSync"

	// ClusterNodesKubernetesUpToDate:
	ClusterConditionReasonNodesK8sUpdateNeeded = "NodesK8sUpdateNeeded"
	ClusterConditionReasonAllNodesK8sUpToDate  = "AllNodesK8sUpToDate"

	// Bootstrapped:
	ClusterConditionReasonBootstrapped    = "ClusterBootstrapped"
	ClusterConditionReasonNotBootstrapped = "ClusterNotBootstrapped"
)
