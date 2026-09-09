package constants

// Node operation phases
const (
	NodeOperationPhasePrefixApply = "Apply"

	NodeOperationPhaseApplyDrain     = NodeOperationPhasePrefixApply + "Drain"
	NodeOperationPhaseApplyVerifying = NodeOperationPhasePrefixApply + "Verifying"
)
