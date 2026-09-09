package constants

// Node operation types:
const (
	NodeOperationTypeProvision                  = "Provision"
	NodeOperationTypeBootstrap                  = "Bootstrap"
	NodeOperationTypeApply                      = "Apply"
	NodeOperationTypeKubernetesComponentUpgrade = "KubernetesComponentUpgrade"
	NodeOperationTypeShutdown                   = "Shutdown"
	NodeOperationTypeReboot                     = "Reboot"
	NodeOperationTypeReset                      = "Reset"
)

// Node operation phases:
const (
	NodeOperationPhasePending   = "Pending"
	NodeOperationPhaseDraining  = "Draining"
	NodeOperationPhaseApplying  = "Applying"
	NodeOperationPhaseVerifying = "Verifying"
	NodeOperationPhaseDone      = "Done"
	NodeOperationPhaseFailed    = "Failed"
)

// Node reasons:
const (
	NodeReasonConfigGenerationFailed    = "ConfigGenerationFailed"
	NodeReasonConfigSerializationFailed = "ConfigSerializationFailed"
	NodeReasonNodeReachable             = "NodeReachable"
	NodeReasonNodeUnreachable           = "NodeUnreachable"
)

// Node operation reasons:
const (
	NodeOperationReasonAlreadyProvisionedNode         = "AlreadyProvisionedNode"
	NodeOperationReasonAlreadyProvisionedTarget       = "AlreadyProvisionedTarget"
	NodeOperationReasonConfigOutOfSync                = "ConfigOutOfSync"
	NodeOperationReasonFailedApply                    = "FailedApply"
	NodeOperationReasonFailedBootstrap                = "FailedBootstrap"
	NodeOperationReasonFailedDrain                    = "FailedDrain"
	NodeOperationReasonFailedImageValidation          = "FailedImageValidation"
	NodeOperationReasonFailedPowerOperation           = "FailedPowerOperation"
	NodeOperationReasonFailedReboot                   = "FailedReboot"
	NodeOperationReasonFailedReset                    = "FailedReset"
	NodeOperationReasonFailedUncordon                 = "FailedUncordon"
	NodeOperationReasonFailedUpgrade                  = "FailedUpgrade"
	NodeOperationReasonInvalidArguments               = "InvalidArguments"
	NodeOperationReasonInvalidComponentForRole        = "InvalidComponentForRole"
	NodeOperationReasonNodeDeletionCleanup            = "NodeDeletionCleanup"
	NodeOperationReasonRebootRequested                = "RebootRequested"
	NodeOperationReasonRebootRequired                 = "RebootRequired"
	NodeOperationReasonSkippedConfigInSync            = "SkippedConfigInSync"
	NodeOperationReasonSkippedDisabledComponent       = "SkippedDisabledComponent"
	NodeOperationReasonSkippedDrain                   = "SkippedDrain"
	NodeOperationReasonSkippedProvisioning            = "SkippedProvisioning"
	NodeOperationReasonSkippedUpgrade                 = "SkippedUpgrade"
	NodeOperationReasonSuccessfulApply                = "SuccessfulApply"
	NodeOperationReasonSuccessfulDrain                = "SuccessfulDrain"
	NodeOperationReasonSuccessfulUpgrade              = "SuccessfulUpgrade"
	NodeOperationReasonTimedOutVerifyingApply         = OperationReasonTimedOut + "VerifyingApply"
	NodeOperationReasonTimedOutVerifyingProvision     = OperationReasonTimedOut + "VerifyingProvision"
	NodeOperationReasonTimedOutVerifyingReboot        = OperationReasonTimedOut + "VerifyingReboot"
	NodeOperationReasonTimedOutVerifyingUpgrade       = OperationReasonTimedOut + "VerifyingUpgrade"
	NodeOperationReasonTimedOutStartingUpgrade        = OperationReasonTimedOut + "StartingUpgrade"
	NodeOperationReasonUnknownComponent               = "UnknownComponent"
	NodeOperationReasonUpgradeRequired                = "UpgradeRequired"
	NodeOperationReasonUpgradeNotRequired             = "UpgradeNotRequired"
	NodeOperationReasonProgramError                   = "ProgramError"
	NodeOperationReasonKubernetesReady                = "KubernetesReady"
	NodeOperationReasonKubernetesNotReadySkippedDrain = "KubernetesNotReadySkippedDrain"
)
