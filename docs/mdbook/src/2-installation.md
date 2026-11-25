# Installation

## Helm

A helm chart can be found in the following OCI registries:

- oci://ghcr.io/rtldeutschland/talos-operator/helm/talos-operator
- oci://git.sapphiccode.net/sapphiccode/talos-operator/helm/talos-operator

Alternatively, it can also be found in the Talos operator repo under `helm/`.

For example:

```sh
helm install talos-operator oci://ghcr.io/rtldeutschland/talos-operator/helm/talos-operator \
  --version 1.1.0 \
  --namespace talos-operator-system \
  --create-namespace # \
  # --set image.repository=git.sapphiccode.net/sapphiccode/talos-operator
```

Helm chart versions should directly correspond to the Talos operator version they are intended to deploy. This can be overridden with `image.tag`. Image tags are `v`-prefixed.

## Useful values

- `features.enableNodeLabel`: (default true), Enables automatic `rtl.de/cluster-name` node label for all managed Kubernetes nodes.
- `features.enableCrossNamespacePatchRefs`: (default true), Allows operator users to reference patches in other namespaces from Node and Cluster resources via `patchRefs`.
- `features.enableCrossNamespaceBackupCredentialsRef` (default true), Allows operator users to reference etcd backup credentials (S3 credentials) in other namespaces than the current Cluster resource's namespace.
- `features.enablePProf` (default false), for memory profiling and debugging.

Example:

```yaml
features:
  enableNodeLabel: true
  enableCrossNamespacePatchRefs: false
  enablePProf: false
```

## Kustomize

Alternatively, kustomize manifests are located under `config/`. The main entrypoint is `config/default/`. It is recommended to rsync the entire `config/` and write kustomize patches against it, or to patch `config/_deploy/`.
