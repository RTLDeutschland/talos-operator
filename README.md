# Talos operator

A talosctl-inspired Kubernetes operator for managing Talos clusters.

Supporting versions: **Talos v1.12–v1.14**, **Kubernetes 1.34–1.37** (operator cluster)

## Motivation

talosctl is great, but there is large potential for human error. Some of this could be mitigated by using wrapper scripts and CI, but this often results in poor visibility.

The Talos operator aims to give cluster operators a single pane of glass to configure and maintain Talos clusters, declaratively, at a large scale.

The operator also strives for safety and correctness in its maintenance logic, including Kubernetes drains. The operator respects things like PodDisruptionBudgets and fails early if they can't be met.

## Features

### Configuration management

All of the config patching knobs and dials `talosctl gen config` provides, and more:

- `--config-patch`: `Cluster.spec.patches` (inline YAML), `Cluster.spec.patchRefs` (ConfigMap), `Node.spec.patches`, `Node.spec.patchRefs`
- `--config-patch-control-plane`: `Cluster.spec.controlPlanePatches`, `Cluster.spec.controlPlanePatchRefs`
- `--config-patch-worker`: `Cluster.spec.workerPatches`, `Cluster.spec.workerPatchRefs`
- `<cluster name>`: `Cluster.metadata.name`
- `<cluster endpoint>`: `https://${Cluster.metadata.name}.${Cluster.spec.domain}:6443`
  - with an escape hatch: [./api/v1alpha1/constants/node_annotation.go](./api/v1alpha1/constants/node_annotation.go)

### Automatic reconciliation / "Maintenance windows"

Automated reconciliation is enabled through maintenance windows defined by a cron expression.

```yaml
apiVersion: talos.rtl.de/v1alpha1
kind: Cluster
metadata:
  name: athena
spec:
  domain: internal # cluster external FQDN suffix -> athena.internal
  kubernetesVersion: v1.36.4
  talosVersionContract: v1.14
  options:
    maintenanceWindow:
      enabled: true
      cron: "17 4 * * 2" # every Tuesday at 04:17 AM
      #cron: "0 * * * *" # permanent automatic reconciliation
    # ...
  # patches: []
  # patchRefs: []
  # controlPlanePatches: []
  # controlPlanePatchRefs: []
  # workerPatches: []
  # workerPatchRefs: []
```

The order of operations is similar to that of Kubernetes upgrades:

- control planes (sorted alphabetically)
- workers (sorted alphabetically)

The operator knows what to do because it collects information on Node objects attached to this Cluster, such as:

```yaml
apiVersion: talos.rtl.de/v1alpha1
kind: Node
metadata:
  name: athena-c1 # node hostname + cluster-sourced FQDN suffix -> athena-c1.internal
spec:
  clusterRef: athena
  role: controlplane
  # patches: []
  # patchRefs: []
status:
  effectiveConfig: "" # the config that will be applied in the next Apply operation
  conditions:
    - lastTransitionTime: "2026-10-01T14:52:19Z" # -> node is ready (config has been successfully encoded)
      message: Node is reachable
      reason: NodeReachable
      status: "True"
      type: Ready
    - lastTransitionTime: "2026-09-02T08:14:50Z" # -> node has successfully applied the Talos config in maintenance mode
      message: Successfully applied Talos config to node in maintenance mode
      reason: SuccessfulApply
      status: "True"
      type: Provisioned
    - lastTransitionTime: "2026-10-13T10:22:47Z" # -> node does not require any configuration changes
      message: Node configuration is in sync
      reason: ConfigInSync
      status: "True"
      type: ConfigInSync
    - lastTransitionTime: "2026-09-08T11:58:53Z" # -> no kubernetes components require updates
      message: Kubernetes components are up to date
      reason: ClusterNodesKubernetesUpToDate
      status: "True"
      type: KubernetesInSync
```

Note `ConfigInSync` and `KubernetesInSync`. These conditions are summed together by the Cluster reconciler and reflected on the Cluster level,
and these conditions are what the maintenance window logic uses to determine whether or not to kick off a RollingApply or a KubernetesUpgrade.

Upgrading Talos and Kubernetes simultaneously is safe because RollingApply runs first, and upgrades the nodes first to a version that would
then support the subsequent Kubernetes upgrade.

#### Manual reconciliation as a default

As shown above, changes to Node and Cluster objects do not reconcile automatically. This allows you to stage configuration changes for opportune times.

### kubectl-talos

A `kubectl-talos` kubectl plugin to interact with operator-managed clusters almost as transparently as you would an unmanaged cluster, e.g:

- triggering a Node Apply: `kubectl talos apply athena-c1`
- triggering a Cluster RollingApply: `kubectl talos rolling-apply athena`
- triggering a Cluster KubernetesUpgrade: `kubectl talos k8s-upgrade athena`
- talosctl: `kubectl talos talosctl athena -- -n athena-c1.internal get machinestatus`
- kubectl: `kubectl talos kubectl athena -- kubectl get nodes -o wide`
- other supported tools: `kubectl talos kubectl athena -- {k9s, cilium, helm}`

## Documentation

- [Talos operator user guide](./docs/mdbook/src/)
- [Talos operator API](./api/v1alpha1/)

## Installation

The operator can be installed in two ways:

### Helm

A Helm chart can be found in `helm/`.

```sh
helm install talos-operator ./helm \
  --namespace talos-operator-system \
  --create-namespace \
  --set image.repository=<some-registry>/talos-operator \
  --set image.tag=<tag>
```

Useful toggles under `features`:

- `features.enableNodeLabel` controls `--enable-node-label`
- `features.enableCrossNamespacePatchRefs` controls `--enable-cross-namespace-patch-refs`
- `features.enableHTTP2` controls `--enable-http2`
- `features.enablePProf` controls `--enable-pprof`

### Kustomize

Alternatively, the kubebuilder-scaffolded Kustomize manifests can be found under `config/`, with a deployable overlay in `config/_deploy/`.
