# Talos operator

A talosctl-inspired Kubernetes operator for managing Talos clusters.

Supporting versions up to, and including: **Talos v1.14**, **Kubernetes 1.37**

## Motivation

talosctl is great, but there is large potential for human error. Some of this could be mitigated by using wrapper scripts and CI, but this often results in poor visibility and inflexibility.

The Talos operator aims to give cluster operators a single pane of glass to configure and maintain Talos clusters, declaratively, at a large scale.

The operator also strives for safety and correctness in day-to-day operations such as Kubernetes drains. To that effect, the operator respects things like PodDisruptionBudgets and fails early if they can't be met.

## Features

### Configuration management

All of the config patching knobs and dials `talosctl gen config` provides, and more:

- `--config-patch`: `Cluster.spec.patches` (inline YAML), `Cluster.spec.patchRefs` (ConfigMap), `Node.spec.patches`, `Node.spec.patchRefs`
- `--config-patch-control-plane`: `Cluster.spec.controlPlanePatches`, `Cluster.spec.controlPlanePatchRefs`
- `--config-patch-worker`: `Cluster.spec.workerPatches`, `Cluster.spec.workerPatchRefs`
- `<cluster name>`: `Cluster.metadata.name`
- `<cluster endpoint>`: `https://${Cluster.metadata.name}.${Cluster.spec.domain}:6443`
  - with an escape hatch: [./api/v1alpha1/constants/node_annotation.go](./api/v1alpha1/constants/node_annotation.go)

### Maintenance windows

Automated cluster node reconciliation inside a maintenance window defined by a cron expression: config application, Kubernetes upgrades, OS upgrades.

### kubectl-talos

A `kubectl-talos` kubectl plugin to interact with operator-managed clusters almost as transparently as you would an unmanaged cluster, e.g:

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

## Contributing

### Development Environment

This project uses [nix-direnv](https://github.com/nix-community/nix-direnv) for reproducible development environments and [direnv](https://direnv.net/) for automatic environment loading.

To set up:
```sh
# (assuming devbox and direnv are installed)
cd talos-operator
direnv allow
```

This project uses Just as an alternative to Make. Run `just --list` to see the available recipes.
