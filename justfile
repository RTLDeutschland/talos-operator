# Image URL to use all building/pushing image targets
IMG := env_var_or_default("IMG", "controller:latest")

# Get the currently used golang install path (in GOPATH/bin, unless GOBIN is set)
GOBIN := if env_var_or_default("GOBIN", "") == "" { `go env GOPATH` + "/bin" } else { env_var("GOBIN") }

# CONTAINER_TOOL defines the container tool to be used for building images.
CONTAINER_TOOL := env_var_or_default("CONTAINER_TOOL", "docker")

# Location to install dependencies to
LOCALBIN := justfile_directory() + "/bin"

# Tool Binaries
KUBECTL := env_var_or_default("KUBECTL", "kubectl")
KIND := env_var_or_default("KIND", "kind")
KUSTOMIZE := LOCALBIN + "/kustomize"
CONTROLLER_GEN := LOCALBIN + "/controller-gen"
ENVTEST := LOCALBIN + "/setup-envtest"
GOLANGCI_LINT := LOCALBIN + "/golangci-lint"
GOLINES := LOCALBIN + "/golines"

# Tool Versions
KUSTOMIZE_VERSION := env_var_or_default("KUSTOMIZE_VERSION", "v5.7.1")
CONTROLLER_TOOLS_VERSION := env_var_or_default("CONTROLLER_TOOLS_VERSION", "v0.19.0")
ENVTEST_VERSION := env_var_or_default("ENVTEST_VERSION", `go list -m -f "{{ .Version }}" sigs.k8s.io/controller-runtime | awk -F'[v.]' '{printf "release-%d.%d", $2, $3}'`)
ENVTEST_K8S_VERSION := env_var_or_default("ENVTEST_K8S_VERSION", `go list -m -f "{{ .Version }}" k8s.io/api | awk -F'[v.]' '{printf "1.%d", $3}'`)
GOLANGCI_LINT_VERSION := env_var_or_default("GOLANGCI_LINT_VERSION", "v2.11.4")
GOLINES_VERSION := env_var_or_default("GOLINES_VERSION", "cfabc0d10d6dbd9a4667556b62355b5c16dcacb9")

# PLATFORMS defines the target platforms for the manager image
PLATFORMS := env_var_or_default("PLATFORMS", "linux/arm64,linux/amd64,linux/s390x,linux/ppc64le")

# KIND cluster name for e2e tests
KIND_CLUSTER := env_var_or_default("KIND_CLUSTER", "talos-operator-test-e2e")

_default: build

# General recipes
[group('general')]
all: build

# Development recipes

# Generate WebhookConfiguration, ClusterRole and CustomResourceDefinition objects.
[group('development')]
manifests: controller-gen
    {{ CONTROLLER_GEN }} rbac:roleName=manager-role crd webhook paths="./..." output:crd:artifacts:config=config/crd/bases

# Generate code containing DeepCopy, DeepCopyInto, and DeepCopyObject method implementations.
[group('development')]
generate: manifests controller-gen
    #!/usr/bin/env bash
    set -euo pipefail
    {{ CONTROLLER_GEN }} object paths="./..."
    cp config/crd/bases/talos.rtl.de_clusters.yaml helm/crds/
    cp config/crd/bases/talos.rtl.de_nodes.yaml helm/crds/

# Run go fmt against code.
[group('development')]
fmt: golines
    {{ GOLINES }} -w .

# Run go vet against code.
[group('development')]
vet:
    go vet ./...

# Run tests.
[group('development')]
test: manifests generate fmt vet setup-envtest
    #!/usr/bin/env bash
    set -euo pipefail
    KUBEBUILDER_ASSETS="$({{ ENVTEST }} use {{ ENVTEST_K8S_VERSION }} --bin-dir {{ LOCALBIN }} -p path)" go test $(go list ./... | grep -v /e2e) -coverprofile cover.out

# Run the e2e tests, but only the ones that don't require talking to real Talos infrastructure.
[group('development')]
test-e2e: manifests generate fmt vet
    go test -count=1 -v ./test/e2e_k3d/

# Run the e2e tests, including a tofu to set up real Talos infrastructure to test against.
[group('development')]
test-e2e-real: manifests generate fmt vet
    go test -count=1 -v -timeout 1h ./test/e2e_k3d/ -args -real-e2e --ginkgo.v

# Run golangci-lint linter.
[group('development')]
lint: golangci-lint
    {{ GOLANGCI_LINT }} run

# Run golangci-lint linter and perform fixes.
[group('development')]
lint-fix: golangci-lint
    {{ GOLANGCI_LINT }} run --fix

# Verify golangci-lint linter configuration.
[group('development')]
lint-config: golangci-lint
    {{ GOLANGCI_LINT }} config verify

# Build manager binary.
[group('build')]
build: manifests generate fmt vet
    go build -o bin/manager cmd/manager/main.go

# Run a controller from your host.
[group('build')]
run: manifests generate fmt vet
    go run ./cmd/manager/main.go

# Build docker image with the manager.
[group('build')]
docker-build:
    {{ CONTAINER_TOOL }} build -t {{ IMG }} .

# Push docker image with the manager.
[group('build')]
docker-push:
    {{ CONTAINER_TOOL }} push {{ IMG }}

# Build and push docker image for the manager for cross-platform support.
[group('build')]
docker-buildx:
    #!/usr/bin/env bash
    set -euo pipefail
    sed -e '1 s/\(^FROM\)/FROM --platform=\$$\{BUILDPLATFORM\}/; t' -e ' 1,// s//FROM --platform=\$$\{BUILDPLATFORM\}/' Dockerfile > Dockerfile.cross
    {{ CONTAINER_TOOL }} buildx create --name talos-operator-builder || true
    {{ CONTAINER_TOOL }} buildx use talos-operator-builder
    {{ CONTAINER_TOOL }} buildx build --push --platform={{ PLATFORMS }} --tag {{ IMG }} -f Dockerfile.cross . || true
    {{ CONTAINER_TOOL }} buildx rm talos-operator-builder || true
    rm Dockerfile.cross

# Install CRDs into the K8s cluster specified in ~/.kube/config.
[group('deployment')]
install: manifests kustomize
    #!/usr/bin/env bash
    set -euo pipefail
    out="$({{ KUSTOMIZE }} build config/crd 2>/dev/null || true)"
    if [ -n "$out" ]; then
        echo "$out" | {{ KUBECTL }} apply -f -
    else
        echo "No CRDs to install; skipping."
    fi

# Uninstall CRDs from the K8s cluster specified in ~/.kube/config. Call with ignore-not-found=true to ignore resource not found errors during deletion.
[group('deployment')]
uninstall ignore_not_found="false": manifests kustomize
    #!/usr/bin/env bash
    set -euo pipefail
    out="$({{ KUSTOMIZE }} build config/crd 2>/dev/null || true)"
    if [ -n "$out" ]; then
        echo "$out" | {{ KUBECTL }} delete --ignore-not-found={{ ignore_not_found }} -f -
    else
        echo "No CRDs to delete; skipping."
    fi

# Deploy controller to the K8s cluster specified in ~/.kube/config.
[group('deployment')]
deploy: manifests kustomize
    cd config/manager && {{ KUSTOMIZE }} edit set image controller={{ IMG }}
    {{ KUSTOMIZE }} build config/default | {{ KUBECTL }} apply -f -

# Undeploy controller from the K8s cluster specified in ~/.kube/config. Call with ignore-not-found=true to ignore resource not found errors during deletion.
[group('deployment')]
undeploy ignore_not_found="false": kustomize
    {{ KUSTOMIZE }} build config/default | {{ KUBECTL }} delete --ignore-not-found={{ ignore_not_found }} -f -

# Ensure local bin directory.
[group('dependencies')]
_ensure-localbin:
    mkdir -p {{ LOCALBIN }}

# Download kustomize locally if necessary.
[group('dependencies')]
kustomize: _ensure-localbin
    #!/usr/bin/env bash
    set -euo pipefail
    if [ -f "{{ KUSTOMIZE }}-{{ KUSTOMIZE_VERSION }}" ] && [ "$(readlink -- "{{ KUSTOMIZE }}" 2>/dev/null)" = "{{ KUSTOMIZE }}-{{ KUSTOMIZE_VERSION }}" ]; then
        exit 0
    fi
    package="sigs.k8s.io/kustomize/kustomize/v5@{{ KUSTOMIZE_VERSION }}"
    echo "Downloading $package"
    rm -f {{ KUSTOMIZE }}
    GOBIN={{ LOCALBIN }} go install $package
    mv {{ KUSTOMIZE }} {{ KUSTOMIZE }}-{{ KUSTOMIZE_VERSION }}
    ln -sf $(realpath {{ KUSTOMIZE }}-{{ KUSTOMIZE_VERSION }}) {{ KUSTOMIZE }}

# Download controller-gen locally if necessary.
[group('dependencies')]
controller-gen: _ensure-localbin
    #!/usr/bin/env bash
    set -euo pipefail
    if [ -f "{{ CONTROLLER_GEN }}-{{ CONTROLLER_TOOLS_VERSION }}" ] && [ "$(readlink -- "{{ CONTROLLER_GEN }}" 2>/dev/null)" = "{{ CONTROLLER_GEN }}-{{ CONTROLLER_TOOLS_VERSION }}" ]; then
        exit 0
    fi
    package="sigs.k8s.io/controller-tools/cmd/controller-gen@{{ CONTROLLER_TOOLS_VERSION }}"
    echo "Downloading $package"
    rm -f {{ CONTROLLER_GEN }}
    GOBIN={{ LOCALBIN }} go install $package
    mv {{ CONTROLLER_GEN }} {{ CONTROLLER_GEN }}-{{ CONTROLLER_TOOLS_VERSION }}
    ln -sf $(realpath {{ CONTROLLER_GEN }}-{{ CONTROLLER_TOOLS_VERSION }}) {{ CONTROLLER_GEN }}

# Download the binaries required for ENVTEST in the local bin directory.
[group('dependencies')]
setup-envtest: envtest
    #!/usr/bin/env bash
    set -euo pipefail
    echo "Setting up envtest binaries for Kubernetes version {{ ENVTEST_K8S_VERSION }}..."
    {{ ENVTEST }} use {{ ENVTEST_K8S_VERSION }} --bin-dir {{ LOCALBIN }} -p path || {
        echo "Error: Failed to set up envtest binaries for version {{ ENVTEST_K8S_VERSION }}."
        exit 1
    }

# Download setup-envtest locally if necessary.
[group('dependencies')]
envtest: _ensure-localbin
    #!/usr/bin/env bash
    set -euo pipefail
    if [ -f "{{ ENVTEST }}-{{ ENVTEST_VERSION }}" ] && [ "$(readlink -- "{{ ENVTEST }}" 2>/dev/null)" = "{{ ENVTEST }}-{{ ENVTEST_VERSION }}" ]; then
        exit 0
    fi
    package="sigs.k8s.io/controller-runtime/tools/setup-envtest@{{ ENVTEST_VERSION }}"
    echo "Downloading $package"
    rm -f {{ ENVTEST }}
    GOBIN={{ LOCALBIN }} go install $package
    mv {{ ENVTEST }} {{ ENVTEST }}-{{ ENVTEST_VERSION }}
    ln -sf $(realpath {{ ENVTEST }}-{{ ENVTEST_VERSION }}) {{ ENVTEST }}

# Download golangci-lint locally if necessary.
[group('dependencies')]
golangci-lint: _ensure-localbin
    #!/usr/bin/env bash
    set -euo pipefail
    if [ -f "{{ GOLANGCI_LINT }}-{{ GOLANGCI_LINT_VERSION }}" ] && [ "$(readlink -- "{{ GOLANGCI_LINT }}" 2>/dev/null)" = "{{ GOLANGCI_LINT }}-{{ GOLANGCI_LINT_VERSION }}" ]; then
        exit 0
    fi
    package="github.com/golangci/golangci-lint/v2/cmd/golangci-lint@{{ GOLANGCI_LINT_VERSION }}"
    echo "Downloading $package"
    rm -f {{ GOLANGCI_LINT }}
    GOBIN={{ LOCALBIN }} go install $package
    mv {{ GOLANGCI_LINT }} {{ GOLANGCI_LINT }}-{{ GOLANGCI_LINT_VERSION }}
    ln -sf $(realpath {{ GOLANGCI_LINT }}-{{ GOLANGCI_LINT_VERSION }}) {{ GOLANGCI_LINT }}

# Download golines locally if necessary.
[group('dependencies')]
golines: _ensure-localbin
    #!/usr/bin/env bash
    set -euo pipefail
    if [ -f "{{ GOLINES }}-{{ GOLINES_VERSION }}" ] && [ "$(readlink -- "{{ GOLINES }}" 2>/dev/null)" = "{{ GOLINES }}-{{ GOLINES_VERSION }}" ]; then
        exit 0
    fi
    package="github.com/golangci/golines@{{ GOLINES_VERSION }}"
    echo "Downloading $package"
    rm -f {{ GOLINES }}
    GOBIN={{ LOCALBIN }} go install $package
    mv {{ GOLINES }} {{ GOLINES }}-{{ GOLINES_VERSION }}
    ln -sf $(realpath {{ GOLINES }}-{{ GOLINES_VERSION }}) {{ GOLINES }}

# Compile and serve documentation using mdbook.
[group('documentation')]
doc:
    mdbook serve docs/mdbook

bump-version VERSION:
    echo "{{ VERSION }}" | grep --quiet --invert-match -E '^v' || { echo "usage error: version should not start with 'v'"; exit 1; }
    echo "{{ VERSION }}" > VERSION
    yq -i ".appVersion = \"v{{ VERSION }}\"" helm/Chart.yaml
    yq -i ".version = \"{{ VERSION }}\"" helm/Chart.yaml
