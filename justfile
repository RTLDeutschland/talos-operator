_default: build

# General recipes
[group('general')]
all: build

# Generate WebhookConfiguration, ClusterRole and CustomResourceDefinition objects.
[group('development')]
manifests:
    controller-gen rbac:roleName=manager-role crd webhook paths="./..." output:crd:artifacts:config=config/crd/bases

# Generate code containing DeepCopy, DeepCopyInto, and DeepCopyObject method implementations.
[group('development')]
generate: manifests
    #!/usr/bin/env bash
    set -euo pipefail
    controller-gen object paths="./..."
    cp config/crd/bases/talos.rtl.de_clusters.yaml helm/crds/
    cp config/crd/bases/talos.rtl.de_nodes.yaml helm/crds/

# Run go fmt against code.
[group('development')]
fmt:
    golines -w .

# Run go vet against code.
[group('development')]
vet:
    go vet ./...

# Run the tests.
[group('development')]
test: generate fmt vet
    go test -count=1 -v ./...

# Run the e2e tests, but only the ones that don't require talking to real Talos infrastructure.
[group('development')]
test-e2e: generate fmt vet
    go test -tags=e2e -count=1 -v ./test/e2e_k3d/

# Run the e2e tests, including a tofu to set up real Talos infrastructure to test against.
[group('development')]
test-e2e-real: generate fmt vet
    go test -tags=e2e -count=1 -v -timeout 1h ./test/e2e_k3d/ -args -real-e2e --ginkgo.v

# Run golangci-lint linter.
[group('development')]
lint:
    golangci-lint run

# Run golangci-lint linter and perform fixes.
[group('development')]
lint-fix:
    golangci-lint run --fix

# Verify golangci-lint linter configuration.
[group('development')]
lint-config:
    golangci-lint config verify

# Build manager binary.
[group('build')]
build: generate fmt vet
    nix build .

# Run a controller from your host.
[group('build')]
run: generate fmt vet
    go run ./cmd/manager/main.go

# Compile and serve documentation using mdbook.
[group('documentation')]
doc:
    mdbook serve docs/mdbook

# Bumps the version strings everywhere relevant to the given string.
[group('release')]
bump-version VERSION:
    echo "{{ VERSION }}" | grep --quiet --invert-match -E '^v' || { echo "usage error: version should not start with 'v'"; exit 1; }
    echo "{{ VERSION }}" > VERSION
    yq -i ".appVersion = \"v{{ VERSION }}\"" helm/Chart.yaml
    yq -i ".version = \"{{ VERSION }}\"" helm/Chart.yaml

# Build and push release artifacts.
[group('release')]
build-push-release:
    # build kubectl-talos
    mkdir -p dist
    nix build -o dist/kubectl-talos .#kubectl-talos-crosscompile
    (cd dist/kubectl-talos/bin && sha256sum * ) > dist/SHA256SUMS
    gh release upload -R github.com/RTLDeutschland/talos-operator "v$(cat VERSION)" dist/kubectl-talos/bin/* || true

    # build main image & chart
    nix run .#task-push-image-cross
    nix run .#task-push-helm

    # build alternative image & chart
    nix run .#task-push-image-cross -- git.sapphiccode.net/sapphiccode/talos-operator
    nix run .#task-push-helm -- git.sapphiccode.net/sapphiccode/talos-operator
