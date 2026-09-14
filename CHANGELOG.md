# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## v0.14.0 - TBD

- ⚠️ **Breaking change**: Talos version contract heuristics **have been removed**.
  - Define these in your cluster resources: `cluster.spec.talosVersionContract: v1.14` (adjust for your own Talos versions)
- **Added** initial support for Talos v1.14.
- **Updated** various Go dependencies.

## v0.13.0 - 2026-09-09

- 🎉 **First open source release!** Thank you to everyone who made this possible.
- **Added** a Helm chart under `helm/` for easy / flexible deployment.
- **Reworked** Node Apply operation to prefer non-reboot path when available, as per Talos 1.14.0-rc's change log removing reboot mode from `talosctl apply`.
- **Fixed** a bug in cluster status collection.
- **Fixed** documentation for `kubectl talos reset`.

## v0.12.0 - 2026-08-18

- **Implemented** cobra flag parsing in cmd/manager
- **Added** feature flag container
- **Added** the ability to disable the `rtl.de/cluster-name` label globally.
- **Added** the ability to disable cross-namespace patch references, depending on cluster security posture & multi-tenancy as it relates to use of the operator.
- **Added** `Node.status.patchHierarchy` to explain on the Node-level where all the configuration patches are coming from.
- **Reimplemented** Node drains to function asynchronously, letting other Nodes reconcile during another Node's drain operation.
- **Fixed** a detail in reboot logic not waiting for the Node's Cilium agent to come back up before the node was considered Ready again.
- **Fixed** various smaller bugs, cleaned up the code.

## v0.11.0 - 2026-06-17

- **Implemented** post Talos 1.13 LifecycleClient upgrade support.
- **Added** a new node `Upgrade` operation that only runs OS upgrades. `Apply` will also still run upgrades when necessary, as it did before.
- **Added validation** for `machine.install.image` in the configuration to be a valid image before passing it to Talos, preventing a potential deadlock.
- **Updated** Talos machinery to 1.13.4.

## v0.10.0 - 2026-06-10

- **Added** `kubectl talos rolling-reboot` command to trigger a rolling reboot operation across all nodes in a cluster.
- **Added** `kubectl talos k8s-upgrade` command to trigger a Kubernetes upgrade operation across all nodes in a cluster.
- **Added** `kubectl talos --watch` (default true) to watch the progress of an operation after triggering it, showing live updates of the operation status.

## v0.9.3 - 2026-06-01

- **Fixed** `tls: expired certificate` errors by skewing client certificate generation backwards by 5 seconds.
- **Enabled** Event logging in the broadcaster.
- **Fixed** node bootstrap to check the cluster operations mutex.
- **Updated dependencies** for Talos API to v1.13.2
- **Updated** Go to v1.26.1

## v0.9.2 - 2026-05-22

- **Improved** node provisioning to refuse if the existing node.talos.rtl.de is already Provisioned = True
- **Improved** node provisioning to remove a legacy "skip" mechanism when targeting an already provisioned node
  - This was the mechanism in place before we had a formal skip mechanism, so it turns out we had two different skip mechanisms.
- **Improved** node apply to add a timeout to upgrade operations

## v0.9.1 - 2026-05-21

- **Temporarily disabled** provisioning upgrades entirely. They were the cause of many different problems and race conditions. For now, just run an Apply operation on the individual nodes to resolve the ConfigOutOfSync condition.
- **Refactored** our logging stack to base on zerolog.
- **Fixed** an erroneous node status update.
- **Improved** various log lines across the codebase.
- **Improved** reset logic to wait for node unreachability.

## v0.9.0 - 2026-05-18

- **Expanded** the end to end test suite
- **Fixed** an oversight in Cluster bootstrap, allowing clusters to be bootstrapped when nodes need initial updates
- **Improved** initial update mechanism by automatically triggering an Apply when a node needs initial updates and the cluster is bootstrapped
  - Meaning, a node deployed from OVF template will eventually have the correct `machine.install.image` installed on it, even if the cluster is completely fresh and it was not able to upgrade in the Provisioning operation.
- **Improved** Node status to run before Operations of phase Pending, previously starting an operation would immediately lock out status updates, resulting in potentially stale information being fed to the Operation logic.
- **Improved** the Provisioning operation's Apply call by adding a timeout. This should prevent the operator from getting stuck during the node reconcile.
- **Improved** Operation Reason fields to use constant values, making them part of the public API and improving consistency.
- **Improved** Event recording to also use constant Reasons and Actions, making events more easily readable.

## v0.8.1 - 2026-05-06

- **Fixed** a fallthrough bug in `hasNodeRebootedYet` that could cause incorrect reboot detection.
- **Fixed** a missed status update bug in node provisioning.
- **Fixed** error handling in `nodeBumpMaintenanceWindowAnnotation` so errors are now properly surfaced.
- **Fixed** some Kubernetes interactions to use `node.GetShortName()` instead of the `node.Name`.
- **Fixed** `otherOpsInProgress` checks on the cluster controller to be consistent and to also guard Kubernetes upgrade operations.
- **Refactored** `otherOpsInProgress` on node Kubernetes upgrades to align with usage elsewhere.

## v0.8.0 - 2026-05-05

- **Added** support for the `operation.options.unsafe` to power operations, skipping node drain and forcing reboot operations. This is useful in situations where a node might be stuck on drain operations or Kubelet shutdown.
- **Added** shutdown / reboot operations to `kubectl-talos`, with `--unsafe` flag.
- **Added** displaying the cluster's next maintenance window as an annotation of the in-cluster k8s Node objects.
- **Fixed** (removed) newline in `Node.status.bootID`

## v0.7.1 - 2026-04-28

- **Revert** the YAML library change, since it seems Talos structs are not being serialized correctly now.
- **Improved** secrets bundle validation.

## v0.7.0 - 2026-04-27

- **Removed a footgun:** `node.spec.hostname`, replaced by annotation `talos.rtl.de/fqdn-override`, to avoid accidental uses of the FQDN override feature.
  - Some Kubernetes GUIs will show all possible fields as commented out when creating a new resource, which could lead to users accidentally setting the FQDN override without realizing it, which inevitably leads to human error. Leaving this unset was a primary motivation in building this operator in the first place.
- **Added** a real Proxmox-based E2E test suite to validate real-world(-ish) operator use
- **Added** `talos.rtl.de/connect-host-override` to allow overriding the host used by the operator to connect to the given Node, which is useful in environments where DNS may not be correctly configured but direct connectivity is still possible
- **Fixed** Auto-Bootstrap to check that all controlplane nodes are Ready (reachable) before initiating bootstrap
- **Fixed** Node provisioning's verification step to add a timeout for the `talosClient.Disks()` call, which can get stuck if the node isn't there yet
- **Fixed** a parsing bug in a utility library parsing /etc/extensions.yaml
- **Fixed** a YAML serialization issue (in tests) by swapping YAML libraries from `go.yaml.in/yaml/v4` to `github.com/goccy/go-yaml`, which respects `json` tags

## v0.6.0 - 2026-04-14

- **Improved** `kubectl talos kubectl` sub-command, allowing other Kubernetes tooling to be invoked using the operator-managed kubeconfig, e.g. `kubectl talos kubectl athena -- {kubectl, helm, cilium, k9s} ...`
- **Fixed** a race condition during Node provisioning caused by unintended reads from cache.
- **Fixed** all reconciliation reads to bypass the informer cache at the entry point, likely fixing some more race conditions.
- **Fixed** the bootstrap condition on Cluster resources to always be reconciled.
- **Refactored** various code to finally silence `golangci-lint`.

## v0.5.2 - 2026-04-09

- **Fixed** that `cluster.kubernetesVersion` can now actually be left empty. The operator will default it to the latest Kubernetes release.
- **Fixed** that Node Kubernetes versions were sometimes not created during initial Node setup.
- **Fixed** that drain is now skipped during Apply when Kubernetes is not yet ready on the node, avoiding a deadlock during initial cluster bring-up.
- **Fixed** Node Provision & Apply so that nodes no longer attempt OS upgrades when the cluster has not been bootstrapped yet.
- **Improved** Node status, such that configuration errors are now surfaced as `Ready = False` on the node, instead of just emitting events.
- **Added** an end-to-end test harness using k3d with some initial tests.
- **BREAKING CHANGE**: The main kustomization deployment has moved from `config/default/` to `config/_deploy/`. Update Argo CD accordingly.

## v0.5.1 - 2026-04-01

- **Regenerated** custom resource manifests.

## v0.5.0 - 2026-03-31

- **Added** `extraLabels` support on the Argo CD cluster secret, allowing custom labels to be propagated for use with Argo CD ApplicationSets and other label-based selectors (closes #16).
- **Added** automatic cluster secret generation when `spec.secretsRef` is not set, removing the need to manually `talosctl gen secrets` before creating a Cluster resource (closes #15).
- **Improved** `kubectl get` output with additional columns: Kubernetes version, Talos version, and next maintenance window.
- **Fixed** default Kubernetes version bumped to v1.35.3.
- **Fixed** a logic bug in node status causing conditions to sometimes not be updated correctly.
- **Refactored** event recording from the old recorder API to the new events framework.

## v0.4.1 - 2026-03-26

- **Fixed** install image parsing errors now surface in node status instead of silently failing reconciliation.
- **Fixed** missing `return` after upgrade failure in the Apply operation, which could cause unintended subsequent phases to execute.
- **Fixed** OS upgrades being missed during the Provision operation in non-ISO scenarios (OVA templates, cloud-reset nodes) (closes #17).
- **Refactored** version comparison logic to use the semver/v4 library.
- **Refactored** eligible node listing in the cluster controller into a helper.

## v0.4.0 - 2026-03-24

- **Added** `--cloud-reset` flag to `kubectl talos reset`, which wipes only the STATE and EPHEMERAL partitions, useful for cloud environments where reinstalling the OS is not desired. Mutually exclusive with `--system-labels-to-wipe`.
- **Improved** the Apply operation with a dedicated upgrade phase, giving the node state machine a clearer distinction between applying config and upgrading the Talos install image.
  - Potentially also **fixes** a harmless second upgrade reboot race condition.
- **Fixed** config sync incorrectly reporting in-sync when the node is not running the desired Talos install version.
- **Fixed** a race condition where `ConfigInSync` could be incorrectly reported True when a config change and an Apply operation are started simultaneously.
- **Refactored** all node and cluster operation state machines to use switch/case, improving readability and making it easier to add phases in the future.
- **Refactored** operation-specific phases out of public APIs. The public phases are `Pending`, `Done` and `Failed`. All other phases are internal implementation details and should be treated as in-progress.
- **Refactored** operation status updates across all operations to use a consistent `updateOpStatus` helper with built-in retry logic.
- **Formatted** all code with `golines`, wrapping long lines to improve readability.

## v0.3.0 - 2026-03-05

- **Improved** Kubernetes Kubelet upgrade logic to drain nodes if Longhorn is detected to be installed
- **Changed** `Cluster.Spec.MaintenanceWindow` → `Cluster.Spec.Options.MaintenanceWindow` (breaking change)
- **Added** configurable timeout options to `Cluster.Spec.Options`
  - `phaseTimeout` (default: 10m) - Maximum length for operation phases
  - `drainTimeout` (default: 20m) - Timeout for drain operations
  - All previously hardcoded drain and phase timeouts now respect these settings
- **Added** automatic cluster bootstrap, also configurable in `Cluster.Spec.Options`
- **Added** `Cluster.Spec.Options.MaintenanceWindow.Enabled` to allow disabling maintenance windows without removing the entire configuration
- **Added** Argo CD cluster secret generation. This allows clusters managed by the operator to be automatically connected to Argo CD on the same cluster.
- **Fixed** intermittent node reachability issues by retrying TCP pings
- **Fixed** context usage bugs: Talos API calls now correctly use `tCtx` instead of `ctx`, and standardized variable naming to `talosClient`/`tCtx` throughout
- **Fixed** context shadowing in `getTalosClient()`
- **Fixed** redundant `Close()` call, removed dead code, and fixed misleading error message in cluster controller

## v0.2.1 - 2026-02-13

- **Fixed** config generation during upgrades / downgrades
- **Fixed** removal of demo secrets in `Node.status.effectiveConfig`
  - These secrets were already invalid, but now they won't be emitted at all.

## v0.2.0 - 2026-02-10

- **Removed** `cmd/debug-*` commands
- **Added** `cmd/kubectl-talos` plugin for interacting with the Talos operator from the kubectl command-line
  - `go install github.com/RTLDeutschland/talos-operator/cmd/kubectl-talos@latest` to install
- **Updated** Talos API dependencies to 1.12.3
- Largely **replaced** our own base configuration patch with Talos' configuration generator, which now adheres to VersionContracts based on `machine.install.image`.
  - This is equivalent to `talosctl gen config --talos-version $x` as a base patch.
  - `rtl.de/cluster-name` is still here.
  - As a side effect of this, secrets are now emitted in Node's `.status.effectiveConfig`. These are dummy secrets with fake data, not the actual secrets deployed.
