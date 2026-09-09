# Cluster Resource

Cluster resources look something like this:

```yaml
{{#include z-cluster.yaml}}
```

## Explanation

### `spec.domain`

> [!IMPORTANT]
> Required field.

The FQDN suffix to use for nodes in this cluster. The operator uses Node/Cluster `metadata.name` + `.` + Cluster `spec.domain` to reach nodes and generate SANs.

### `spec.secretsRef`

A reference to a Secret containing a single key, `secrets.yaml`, generated with `talosctl gen secrets`. If the target secret is empty, the operator will generate a new cluster secrets bundle and store it in the target secret.

### `spec.kubernetesVersion`

The target Kubernetes version for this cluster. If left empty, the operator will set a reasonable default.

Newly-provisoned nodes will install this version of Kubernetes.

Existing nodes can be upgraded using a `KubernetesUpgrade` operation.

### `spec.patchRefs`, `spec.workerPatchRefs` and `spec.controlPlanePatchRefs`

A list of references to ConfigMaps containing configuration patches to apply to the relevant node roles, in order.

References must contain the `key` of the ConfigMap they are referencing, as well as the `name` of the ConfigMap. Optionally, `namespace` can be specified if the ConfigMap is in a different namespace than the Cluster resource.

Role-specific patches take priority over generic patches.

### `spec.patches`, `spec.workerPatches` and `spec.controlPlanePatches`

A list of inline configuration patches to apply to the relevant node roles, in order. These take priority over `patchRefs` if both are specified.

Role-specific patches take priority over generic patches.

### `spec.options`

Options for customizing the behavior of the Talos operator for this Cluster.

#### `spec.options.maintenanceWindow`

See [Automated Upgrades](./4.5-automated_upgrades.md).

#### `spec.options.argoCDSecret`

```yaml
spec:
  options:
    argoCDSecret:
      enabled: true
      namespace: "argocd"
      #clusterNameOverride: "foo-cluster
```

The operator may be configured to automatically create and refresh an [Argo CD cluster secret](https://argo-cd.readthedocs.io/en/stable/operator-manual/declarative-setup/#clusters) containing admin kubeconfig credentials for this cluster. In here, point the `namespace` field to the location of your Argo installation in the same cluster.

Optionally, `clusterNameOverride` can be set to change the name presented to Argo CD away from the default of `cluster.Metadata.Name`.

#### `spec.options.phaseTimeout`

> [!NOTE]
> Default: `10m`

The maximum length that an operation phase may take before being considered failed. This applies to all phases except Draining, most notably the Verifying phase.

#### `spec.options.drainTimeout`

> [!NOTE]
> Default: `20m`

Timeout for drain operations performed by the operator.

> [!WARNING]
> If you are using [Longhorn](https://longhorn.io) or any other storage solution that uses volumes with significant eviction times, you should increase this timeout. Longhorn volumes can take several minutes to migrate, and the default 20-minute timeout may not be sufficient for clusters with many volumes or large amounts of data.
