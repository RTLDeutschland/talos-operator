package constants

// NodeAnnotationFQDNOverride is an annotation that you can set to override the node's FQDN
// in configuration and connection contexts.
//
// This overrides the default pattern of (node.metadata.name).(cluster.spec.domain).
//
// Do not specify it unless you have a good reason, because it increases the chance of human error
// (copy-pasting configs around and configuring two nodes into one another).
const NodeAnnotationFQDNOverride = "talos.rtl.de/fqdn-override"

// NodeAnnotationConnectHostOverride is an annotation that you can set to override the host used
// to connect to the node, for example if the operator runs in an environment that breaks DNS
// resolution to the node's FQDN.
//
// This only affects the operator's Talos client connection to the node.
//
// This setting takes precedence over NodeAnnotationFQDNOverride for connection purposes.
const NodeAnnotationConnectHostOverride = "talos.rtl.de/connect-host-override"

// NodeK8sAnnotationNextMaintenanceWindow is an annotation set by the operator on the Node in
// Kubernetes to indicate the start of the next maintenance window for this node.
const NodeK8sAnnotationNextMaintenanceWindow = "talos.rtl.de/next-maintenance-window"
