package constants

// Event reasons (e.g. "Created", "Updated", "Deleted"), very broadly *what* happened:
//
// But, unlike condition & status reasons, these are not intended to be consumed programmatically,
// so we are not hyper-specific down to every individual failure condition.
// These are just meant to be understood by humans reading events.
const (
	EventReasonStarted   = OperationReasonStarted
	EventReasonCompleted = OperationReasonCompleted
	EventReasonFailed    = "Failed"
	EventReasonSkipped   = "Skipped"

	EventReasonCreated = "Created"
	EventReasonUpdated = "Updated"
	EventReasonDeleted = "Deleted"

	EventReasonValidationError = "ValidationError"
)

// Event actions, what we were doing when this event happened:
const (
	EventActionSecretReconcile  = "SecretReconcile"
	EventActionStatusReconcile  = "StatusReconcile"
	EventActionRunningOperation = "RunningOperation"

	EventActionBootstrap                  = "Bootstrap"
	EventActionApply                      = "Apply"
	EventActionOSUpgrade                  = "OSUpgrade"
	EventActionRollingApply               = "RollingApply"
	EventActionApplyDrain                 = "ApplyDrain"
	EventActionDrain                      = "Drain"
	EventActionPowerOperation             = "PowerOperation"
	EventActionProvision                  = "Provision"
	EventActionReset                      = "Reset"
	EventActionKubernetesComponentUpgrade = "KubernetesComponentUpgrade"
	EventActionKubernetesUpgrade          = "KubernetesUpgrade"
	EventActionKubernetesApply            = "KubernetesApply"
)
