# TL;DR / Quick Start

## Operations

The Talos Operator is intended to replace most things you would do with `talosctl`:

| Talos Command | Equivalent Node Operation | Equivalent Cluster Operation |
| --- | --- | --- |
| `talosctl apply-config --insecure` | `Node.Provision` | N/A |
| `talosctl apply-config` | `Node.Apply` | `Cluster.RollingApply` |
| `talosctl reboot` | `Node.Reboot` | `Cluster.RollingReboot` |
| `talosctl shutdown` | `Node.Shutdown` | N/A |
| `talosctl reset` | `Node.Reset` | N/A |
| `talosctl k8s-upgrade` | `Node.KubernetesComponentUpgrade` | `Cluster.KubernetesUpgrade` |

It takes ownership of the lifecycle of the resulting resources. Once a Node is provisioned, the Talos Operator will continuously monitor it and update its status in the cluster.

## Patching

A usual Talos cluster looks something like this:

- general patches
- cluster-specific patches
  - e.g. networking config
- cluster-specific Control Plane patches
  - e.g. [Virtual IPs](https://docs.siderolabs.com/talos/v1.11/networking/vip)
- cluster-specific Worker patches
  - e.g. node labels or annotations
- node-specific patches
  - e.g. static IP addressing, routes, bonds

Our resources facilitate this:

```yaml
apiVersion: talos.rtl.de/v1alpha1
kind: Cluster
metadata:
  name: #string
  namespace: #string
#  annotations:  key: string
#  labels:  key: string
spec:
#  controlPlanePatchRefs:
#    - key: string
#      name: string
#      namespace: string
#  controlPlanePatches:
#    - string
#  domain: string
#  kubernetesVersion: string
#  patchRefs:
#    - key: string
#      name: string
#      namespace: string
#  patches:
#    - string
#  secretsRef: string
#  workerPatchRefs:
#    - key: string
#      name: string
#      namespace: string
#  workerPatches:
#    - string
```

```yaml
apiVersion: talos.rtl.de/v1alpha1
kind: Node
metadata:
  name: #string
  namespace: #string
#  annotations:  key: string
#  labels:  key: string
spec:
#  clusterRef: string
#  hostname: string
#  patchRefs:
#    - key: string
#      name: string
#      namespace: string
#  patches:
#    - string
#  role: string
```

Meaning our new patch hierarchy looks like this:

- Auto-generated base cluster patch containing Cluster FQDN SANs and sane defaults
- Auto-generated Kubernetes version patch
- Auto-generated secrets patch (omitted for `Node.Status.EffectiveConfig`)
- Cluster-level ConfigMap references
- Cluster-level inline patches
- Cluster-role-level (control plane or worker) ConfigMaps
- Cluster-role-level inline patches
- Node-level ConfigMap references
- Node-level inline patches
