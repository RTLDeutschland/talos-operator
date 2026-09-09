// Package constants contains internal constants for the controller.
package constants

// ContextKey is a type for context keys used in the controller.
type ContextKey string

// Context keys for storing and retrieving values from context in the controller.
const (
	CtxKeyRequest ContextKey = "request"
	CtxKeyNode    ContextKey = "node"
	CtxKeyCluster ContextKey = "cluster"
)
