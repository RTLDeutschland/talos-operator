package v1alpha1

import (
	"strings"

	. "github.com/RTLDeutschland/talos-operator/api/v1alpha1/constants"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Cluster",type=string,JSONPath=`.spec.clusterRef`,description="Cluster this Node belongs to"
// +kubebuilder:printcolumn:name="Role",type=string,JSONPath=`.spec.role`,description="Role of the Node in the cluster"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
// +kubebuilder:printcolumn:name="Talos",type=string,JSONPath=`.status.talosVersion`
// +kubebuilder:printcolumn:name="Kubernetes",type=string,JSONPath=`.status.kubernetesVersions.kubelet`
// +kubebuilder:printcolumn:name="Provisioned",type=string,JSONPath=`.status.conditions[?(@.type=="Provisioned")].status`,description="Indicates if the Node has been provisioned"
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`,description="Indicates if the Node is ready"
// +kubebuilder:printcolumn:name="ConfigInSync",type=string,JSONPath=`.status.conditions[?(@.type=="ConfigInSync")].status`,description="Indicates if the Node's configuration is in sync"
// +kubebuilder:printcolumn:name="K8sInSync",type=string,JSONPath=`.status.conditions[?(@.type=="KubernetesInSync")].status`,description="Indicates if the Node's configuration is in sync"
// +kubebuilder:printcolumn:name="Operation",type=string,JSONPath=`.status.operationStatus.type`,description="Current operation on the Node"
// +kubebuilder:printcolumn:name="Operation Phase",type=string,JSONPath=`.status.operationStatus.phase`,description="Phase of the current operation on the Node"
// +kubebuilder:printcolumn:name="Operation Age",type=date,JSONPath=`.status.operationStatus.lastTransitionTime`,description="Last transition time of the current operation"

// Node is the Schema for the nodes API
type Node struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty,omitzero"`

	// spec defines the desired state of Node
	// +required
	Spec NodeSpec `json:"spec"`

	// operation defines a requested operation on the Node
	// +optional
	Operation *NodeOperation `json:"operation,omitempty,omitzero"`

	// status defines the observed state of Node
	// +optional
	Status NodeStatus `json:"status,omitempty,omitzero"`
}

// getFQDNOverride checks if the Node has the FQDN override annotation and returns its value if set.
func (n *Node) getFQDNOverride() (string, bool) {
	if len(n.Annotations) > 0 {
		if fqdn, ok := n.Annotations[NodeAnnotationFQDNOverride]; ok && fqdn != "" {
			return fqdn, true
		}
	}
	return "", false
}

// GetFQDN returns the FQDN for this node.
func (n *Node) GetFQDN(c *Cluster) string {
	if fqdn, ok := n.getFQDNOverride(); ok {
		return fqdn
	}
	return n.Name + "." + c.Spec.Domain
}

// GetConnectHost returns the host to connect to for this node, taking into account the connection host override annotation.
func (n *Node) GetConnectHost(c *Cluster) string {
	if len(n.Annotations) > 0 {
		if host, ok := n.Annotations[NodeAnnotationConnectHostOverride]; ok && host != "" {
			return host
		}
	}
	return n.GetFQDN(c)
}

// GetShortName returns the first segment of the FQDN override if set,
// otherwise the metadata.name, to be used in Kubernetes contexts.
func (n *Node) GetShortName() string {
	if fqdn, ok := n.getFQDNOverride(); ok {
		base, _, _ := strings.Cut(fqdn, ".")
		return base
	}
	return n.Name
}

// NodeSpec defines the desired state of Node
type NodeSpec struct {
	// ClusterRef references the Cluster this Node belongs to
	// +required
	ClusterRef string `json:"clusterRef"`

	// Role defines the role of the Node in the cluster (e.g., controlplane, worker).
	// +kubebuilder:validation:Enum=controlplane;worker
	// +required
	Role string `json:"role"`

	// Patches is a list of inline YAML / JSON patches to be applied to this specific Node's Talos config.
	// +optional
	Patches []string `json:"patches,omitempty"`

	// PatchRefs is a list of references to ConfigMap resources containing patches to be applied to this specific Node's Talos config.
	// +optional
	PatchRefs []PatchRef `json:"patchRefs,omitempty"`
}

// NodeOperation defines a requested operation on the Node.
type NodeOperation struct {
	// Type of operation to perform on the Node.
	// +kubebuilder:validation:Enum=Provision;Bootstrap;Apply;Upgrade;Reboot;Shutdown;Reset;KubernetesComponentUpgrade
	// +required
	Type string `json:"type"`

	// Options contains additional options for the operation.
	// +optional
	Options NodeOperationOptions `json:"options,omitzero"`

	// Provision contains arguments for the Provision operation.
	// +optional
	Provision ProvisionOperationOptions `json:"provision,omitzero"`

	// Reset contains arguments for the Reset operation.
	// +optional
	Reset ResetOperationOptions `json:"reset,omitzero"`

	// KubernetesComponentUpgrade contains arguments for the KubernetesComponentUpgrade operation.
	// +optional
	KubernetesComponentUpgrade KubernetesComponentUpgradeOptions `json:"kubernetesComponentUpgrade,omitzero"`
}

// NodeOperationOptions contains additional options for Node operations.
type NodeOperationOptions struct {
	// Unsafe indicates whether to perform the operation in unsafe mode, bypassing certain safety checks.
	// Commonly this means bypassing Kubernetes drain and uncordon operations.
	// +optional
	Unsafe bool `json:"unsafe,omitempty"`
}

// ProvisionOperationOptions contains arguments for the Provision operation.
type ProvisionOperationOptions struct {
	// Address is the IP / DNS address to find the Talos node in maintenance mode.
	Address string `json:"address,omitempty"`

	// Skip indicates whether to skip the provisioning operation.
	// +optional
	Skip bool `json:"skip,omitempty"`
}

// ResetOperationOptions contains arguments for the Reset operation.
type ResetOperationOptions struct {
	// Mode defines the mode of erasure for the reset operation.
	// (default "all" if no system labels or user disks are specified)
	// +optional
	// +kubebuilder:validation:Enum=all;system-disk;user-disks
	Mode string `json:"mode,omitempty"`

	// Reboot indicates whether to reboot the Node after the reset operation.
	// +optional
	// +default=false
	Reboot bool `json:"reboot,omitempty"`

	// Graceful indicates whether to perform a graceful reset, allowing services to shut down properly and etcd members to leave cleanly.
	// +optional
	// +default=true
	Graceful *bool `json:"graceful,omitempty"`

	// SystemLabelsToWipe is a list of specific system labels to wipe during the reset operation.
	// If empty, all system labels will be wiped, assuming the mode includes system-disk.
	// +optional
	SystemLabelsToWipe []string `json:"systemLabelsToWipe,omitempty"`

	// UserDisksToWipe is a list of specific user disks to wipe during the reset operation.
	// If empty, all user disks will be wiped, assuming the mode includes user-disks.
	// +optional
	UserDisksToWipe []string `json:"userLabelsToWipe,omitempty"`
}

// KubernetesComponentUpgradeOptions contains arguments for the KubernetesComponentUpgrade operation.
type KubernetesComponentUpgradeOptions struct {
	// ComponentName is the name of the Kubernetes component to upgrade
	// +required
	// +kubebuilder:validation:Enum=apiserver;controllerManager;scheduler;kubelet
	ComponentName string `json:"componentName,omitempty"`
}

// NodeStatus defines the observed state of Node.
type NodeStatus struct {
	// Important: Run "make" to regenerate code after modifying this file

	// For Kubernetes API conventions, see:
	// https://github.com/kubernetes/community/blob/master/contributors/devel/sig-architecture/api-conventions.md#typical-status-properties

	// conditions represent the current state of the Node resource.
	// Each condition has a unique type and reflects the status of a specific aspect of the resource.
	//
	// Standard condition types include:
	// - "Available": the resource is fully functional
	// - "Progressing": the resource is being created or updated
	// - "Degraded": the resource failed to reach or maintain its desired state
	//
	// The status of each condition is one of True, False, or Unknown.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// Operation provides details about the current operation being performed on the Node.
	// +optional
	Operation *NodeOperationStatus `json:"operationStatus,omitempty"`

	// BootID is the unique boot identifier of the Node, used to detect reboots.
	// +optional
	BootID string `json:"bootID,omitempty"`

	// AppliedConfigHash is the hash of the last successfully applied configuration on the Node.
	// +optional
	AppliedConfigHash string `json:"appliedConfigHash,omitempty"`

	// TalosVersion is the version of Talos currently running on the Node.
	// +optional
	TalosVersion string `json:"talosVersion,omitempty"`

	// TalosSchematicID is the schematic ID of the Talos version currently running on the Node.
	// +optional
	TalosSchematicID string `json:"talosSchematicID,omitempty"`

	// KubernetesVersions represents the versions of various Kubernetes components running on the Node.
	// +optional
	KubernetesVersions *KubernetesVersions `json:"kubernetesVersions,omitempty"`

	// EffectiveConfig is the configuration we expect to be running on this node after applying all patches, without secrets.
	// +optional
	EffectiveConfig string `json:"effectiveConfig,omitempty"`

	// PatchHierarchy explains how the node config came to be.
	// +optional
	PatchHierarchy PatchHierarchy `json:"patchHierarchy,omitempty"`
}

// KubernetesVersions represents the versions of various Kubernetes components running on the Node.
type KubernetesVersions struct {
	APIServer         string `json:"apiServer,omitempty"`
	ControllerManager string `json:"controllerManager,omitempty"`
	Scheduler         string `json:"scheduler,omitempty"`
	Kubelet           string `json:"kubelet,omitempty"`
}

func (kv *KubernetesVersions) GetComponentVersion(component string) string {
	switch component {
	case "apiserver":
		return kv.APIServer
	case "controllerManager":
		return kv.ControllerManager
	case "scheduler":
		return kv.Scheduler
	case "kubelet":
		return kv.Kubelet
	default:
		return ""
	}
}

func (kv *KubernetesVersions) SetComponentVersion(component, version string) {
	switch component {
	case "apiserver":
		kv.APIServer = version
	case "controllerManager":
		kv.ControllerManager = version
	case "scheduler":
		kv.Scheduler = version
	case "kubelet":
		kv.Kubelet = version
	}
}

// NodeOperationStatus provides details about the current operation being performed on the Node.
type NodeOperationStatus struct {
	NodeOperation `json:",inline"`

	// Phase indicates the current phase of the operation.
	// +optional
	Phase string `json:"phase,omitempty"`

	// Reason provides a code or short description of why the operation is in its current phase.
	// +optional
	Reason string `json:"reason,omitempty"`

	// Message provides a human-readable message about the operation.
	// +optional
	Message string `json:"message,omitempty"`

	// LastTransitionTime is the last time the operation changed phases.
	// +optional
	LastTransitionTime metav1.Time `json:"lastTransitionTime,omitzero"`
}

// +kubebuilder:object:root=true

// NodeList contains a list of Node
type NodeList struct {
	metav1.TypeMeta `       json:",inline"`
	metav1.ListMeta `       json:"metadata,omitempty"`
	Items           []Node `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Node{}, &NodeList{})
}
