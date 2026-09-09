package constants

const NodeOperationTypeUpgrade = "Upgrade"

// Node upgrade operation phases:
const (
	NodeOperationPhasePrefixUpgrade = "Upgrade"

	NodeOperationPhaseUpgradeDrain     = NodeOperationPhasePrefixUpgrade + "Drain"
	NodeOperationPhaseUpgradePreflight = NodeOperationPhasePrefixUpgrade + "Preflight"
	NodeOperationPhaseUpgradeVerifying = NodeOperationPhasePrefixUpgrade + "Verifying"
	NodeOperationPhaseUpgradeUncordon  = NodeOperationPhasePrefixUpgrade + "Uncordon"

	// 1.13+ LifecycleService Upgrades
	NodeOperationPhaseUpgrade113ImagePull = NodeOperationPhasePrefixUpgrade + "113ImagePull"
	NodeOperationPhaseUpgrade113Upgrade   = NodeOperationPhasePrefixUpgrade + "113Upgrade"

	// legacy MachineService Upgrade
	NodeOperationPhaseUpgradeLegacyUpgrade = NodeOperationPhasePrefixUpgrade + "LegacyUpgrade"
)

const failedGRPCCall = "FailedGRPCCall"

// Node upgrade operation reasons:
const (
	NodeOperationReasonUpgradePathPost113                   = "UpgradePathPost113"
	NodeOperationReasonUpgradePathPre113                    = "UpgradePathPre113"
	NodeOperationReasonFailedGRPCCallImagePull              = failedGRPCCall + "ImagePull"
	NodeOperationReasonFailedGRPCCallImagePullBadResult     = failedGRPCCall + "ImagePullBadResult"
	NodeOperationReasonFailedGRPCCallLifecycleClientUpgrade = failedGRPCCall + "LifecycleClientUpgrade"
	NodeOperationReasonSuccessfulImagePull                  = "SuccessfulImagePull"
	NodeOperationReasonSuccessfulLegacyUpgradeRequest       = "SuccessfulUpgradeRequest"
	NodeOperationReasonSuccessfulLifecycleClientUpgrade     = "SuccessfulLifecycleClientUpgrade"
)

// Example transitions:
// - Pending -> UpgradeDrain -> UpgradePreflight -> Upgrade113ImagePull -> Upgrade113Upgrade -> UpgradeUncordon -> Done
// - Pending -> UpgradeDrain -> UpgradePreflight -> UpgradeLegacyUpgrade -> UpgradeUncordon -> Done
// - Pending -> UpgradeDrain -> UpgradePreflight -> Upgrade113ImagePull -> Failed
// - Pending -> UpgradeDrain -> UpgradePreflight -> Upgrade113ImagePull -> Upgrade113Upgrade -> Failed
// - Pending -> UpgradeDrain -> UpgradePreflight -> UpgradeLegacyUpgrade -> Failed
