# TL;DR / Quick Start

## Operations

The Talos operator is intended to replace most things you would do with `talosctl`:

| Talos Command | Equivalent Node Operation | Equivalent Cluster Operation |
| --- | --- | --- |
| `talosctl apply-config --insecure` | `Node.Provision` | N/A |
| `talosctl apply-config` | `Node.Apply` | `Cluster.RollingApply` |
| `talosctl reboot` | `Node.Reboot` | `Cluster.RollingReboot` |
| `talosctl shutdown` | `Node.Shutdown` | N/A |
| `talosctl reset` | `Node.Reset` | N/A |
| `talosctl k8s-upgrade` | `Node.KubernetesComponentUpgrade` | `Cluster.KubernetesUpgrade` |

It takes ownership of the lifecycle of the resulting resources. Once a Node is provisioned, the Talos operator will continuously monitor it and update its status in the cluster.

### Starting operations

Because the Talos operator is architected in a way that does not immediately apply changes to the manifest to the cluster and its nodes, there exists an `operation` key to tell the it to start processing.[^1]

[^1]: Except in cases of maintenanceWindow, see [Unattended configuration and Kubernetes upgrades](./4.5-automated_upgrades.md).

Both Node and Cluster resources support an `operation` key. This key is normally empty and omitted from any YAML output, but it can be set in a few different ways:

- supplying it with the resource manifest (not recommended)
- using raw `kubectl`: `kubectl patch nodes.talos.rtl.de NODE_NAME --type=merge -p '{"operation":{"type":"Apply"}}'`
- or using the [kubectl-talos plugin](./5.1-kubectl_plugin.md): `kubectl talos apply NODE`

When setting this key, the operator detects this and:

1. copies its contents into `status.operationStatus`
2. sets `phase` to `Pending` and updates the `lastTransitionTime` to the current time
2. sets `operation` back to empty / null, removing it from the manifest

It will then internally begin processing the operation according to its type, updating the phase as the operation progresses.
This means the operator can be restarted at any point without losing track of what it was doing.

The `phase` lifecycle depends on the operation type, but generally follows a pattern of `Pending` to various in-progress states to `Done` or `Failed`.

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
