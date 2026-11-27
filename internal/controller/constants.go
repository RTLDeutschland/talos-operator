package controller

import (
	"time"

	. "github.com/RTLDeutschland/talos-operator/api/v1alpha1/constants"
	tconstants "github.com/siderolabs/talos/pkg/machinery/constants"
)

// Requeue intervals.
const (
	Requeue1s  = 1 * time.Second
	Requeue5s  = 5 * time.Second
	Requeue10s = 10 * time.Second
)

// Node role shorthands.
const (
	Controlplane = NodeRoleControlplane
	Worker       = NodeRoleWorker
)

// Default Kubernetes version used if not specified in the cluster spec.
const DefaultKubernetesVersion = "v" + tconstants.DefaultKubernetesVersion
