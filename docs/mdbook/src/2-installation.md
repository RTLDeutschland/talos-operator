# Installation

## Helm

A helm chart can be found in the following OCI registries:

- oci://ghcr.io/rtldeutschland/talos-operator/helm/talos-operator
- oci://git.sapphiccode.net/sapphiccode/talos-operator/helm/talos-operator

Alternatively, it can also be found in the Talos operator repo under `helm/`.

For example:

```sh
helm install talos-operator oci://ghcr.io/rtldeutschland/talos-operator/helm/talos-operator \
  --version 1.0.1 \
  --namespace talos-operator-system \
  --create-namespace \
  --set image.repository=ghcr.io/rtldeutschland/talos-operator
```

Helm chart versions should directly correspond to the Talos operator version they are intended to deploy. This can be overridden with `image.tag`. Image tags are `v`-prefixed.

## Useful values

- `features.enableNodeLabel` controls `--enable-node-label`
- `features.enableCrossNamespacePatchRefs` controls `--enable-cross-namespace-patch-refs`
- `features.enablePProf` controls `--enable-pprof`

Example:

```yaml
features:
  enableNodeLabel: true
  enableCrossNamespacePatchRefs: false
  enablePProf: false
```

## Kustomize

Alternatively, kustomize manifests are located under `config/`. The main entrypoint is `config/default/`. It is recommended to rsync the entire `config/` and write kustomize patches against it, or to patch `config/_deploy/`.
