package constants

// Node roles:
const (
	NodeRoleControlplane = "controlplane"
	NodeRoleWorker       = "worker"
)

// Node reset modes:
const (
	NodeResetModeAll        = "all"
	NodeResetModeSystemDisk = "system-disk"
	NodeResetModeUserDisks  = "user-disks"
)

// Node finalizers:
const (
	// Deprecated: use [constants.Finalizer] instead
	// go:fix inline
	NodeFinalizer = Finalizer
)
