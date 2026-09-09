# Installation

## Helm

The Helm chart lives in `helm/` and includes the CRDs under `helm/crds/`.

```sh
helm install talos-operator ./helm \
  --namespace talos-operator-system \
  --create-namespace \
  --set image.repository=<some-registry>/talos-operator \
  --set image.tag=<tag>
```

Useful toggles live under `features`:

- `features.enableNodeLabel` controls `--enable-node-label`
- `features.enableCrossNamespacePatchRefs` controls `--enable-cross-namespace-patch-refs`
- `features.enableHTTP2` controls `--enable-http2`
- `features.enablePProf` controls `--enable-pprof`

Example:

```yaml
features:
  enableNodeLabel: true
  enableCrossNamespacePatchRefs: false
  enableHTTP2: false
  enablePProf: false
```

## Kustomize

Alternatively, kustomize manifests are located under `config/`. The main entrypoint is `config/default/`. It is recommended to rsync the entire `config/` and write kustomize patches against it, or to patch `config/_deploy/`.
