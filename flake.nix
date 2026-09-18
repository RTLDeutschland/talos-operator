{
  description = ''
    CI/CD and development environment for the Talos operator
  '';

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    nix2container.url = "github:nlewo/nix2container";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs = {
    self,
    nixpkgs,
    flake-utils,
    nix2container,
    ...
  }:
    flake-utils.lib.eachDefaultSystem (system: let
      pkgs = import nixpkgs {inherit system;};
      lib = pkgs.lib;

      n2cPkgs = nix2container.packages.${system};

      vendorHash = "sha256-FygExKqGMeN219zHOWyigKfOjNPQB7hB1xJp6QsrkSA=";
      version = lib.strings.trim (builtins.readFile ./VERSION);

      # filter the app sources to avoid spurious rebuilds
      src = lib.fileset.toSource {
        root = ./.;
        fileset = lib.fileset.unions [
          ./go.mod
          ./go.sum
          (lib.fileset.fileFilter (f: f.hasExt "go") ./.)
        ];
      };
      ldflags = [
        "-X main.Version=${version}"
      ];
      env.CGO_ENABLED = "0";

      # packages available in the dev shell
      devPackages = with pkgs; [
        # misc dev tools
        just
        buildah
        talosctl

        # fix semantic-release on arm64 by using an older Python
        (semantic-release.overrideAttrs (final: prev: {
          python = pkgs.python311;
          meta.badPlatforms = [];
        }))

        # build tools
        go
        golines
        kubernetes-controller-tools
        kubebuilder

        # test tools
        k3d
        opentofu

        # docs
        mdbook
      ];
    in {
      formatter = pkgs.alejandra;
      devShells.default = pkgs.mkShell {packages = devPackages;};
      packages.default = self.packages.${system}.kubectl-talos;

      packages = {
        # kubectl-talos plugin
        kubectl-talos = pkgs.buildGoModule (finalAttrs: {
          pname = "kubectl-talos";
          inherit version;
          inherit src;

          subPackages = ["cmd/kubectl-talos"];
          inherit env;
          inherit ldflags;

          vendorHash = vendorHash;
          proxyVendor = true;

          meta = {
            description = "kubectl plugin for the Talos operator";
            license = lib.licenses.mit;
          };
        });

        # operator manager binary
        operator-manager = pkgs.buildGoModule (finalAttrs: {
          pname = "talos-operator-manager";
          inherit version;
          inherit src;

          subPackages = ["cmd/manager"];
          inherit env;
          inherit ldflags;

          vendorHash = vendorHash;
          proxyVendor = true;

          meta = {
            description = "manager (main binary) for the Talos operator";
            license = lib.licenses.mit;
          };
        });

        # container containing operator-manager
        operator-container = n2cPkgs.nix2container.buildImage {
          name = "ghcr.io/rtldeutschland/talos-operator";
          tag = lib.strings.concatStrings ["v" version];
          copyToRoot =
            pkgs.runCommandWith {
              name = "talos-operator-container-root";
            }
            ''
              mkdir -p $out/etc
              ln -s ${pkgs.cacert.out}/etc/ssl $out/etc/ssl
            '';
          config = {
            Entrypoint = ["${self.packages.${system}.operator-manager}/bin/manager"];
            User = "65532";
            Labels = {
              "org.opencontainers.image.source" = "https://github.com/RTLDeutschland/talos-operator";
              "org.opencontainers.image.licenses" = "MIT";
              "org.opencontainers.image.version" = version;
            };
          };
        };

        operator-container-multiarch =
          pkgs.runCommandWith {
            name = "operator-container-multiarch";
            derivationArgs.nativeBuildInputs = with pkgs; [regclient];
          } ''
            mkdir -p tmp
            mkdir -p $out
            tag="${self.packages.${system}.operator-container.imageTag}"

            # build both architectures (needs a cross-platform Nix)
            ${lib.getExe self.packages.x86_64-linux.operator-container.copyTo} "oci:tmp:amd64"
            ${lib.getExe self.packages.aarch64-linux.operator-container.copyTo} "oci:tmp:arm64"

            # create a multi-architecture index for the built images
            regctl index create "ocidir://''${out}:''${tag}" \
              --ref "ocidir://tmp:amd64" \
              --ref "ocidir://tmp:arm64" \
              --annotation "org.opencontainers.image.source=https://github.com/RTLDeutschland/talos-operator" \
              --annotation "org.opencontainers.image.licenses=MIT" \
              --annotation "org.opencontainers.image.version=${version}"
          '';

        # CI scripts to build & push images, **without** Docker daemon
        task-push-image = pkgs.writeShellApplication {
          name = "task-push-image";
          text = ''
            # $1: repository URL
            ${lib.getExe self.packages.${system}.operator-container.copyTo} "docker://''${1}:${self.packages.${system}.operator-container.imageTag}"
          '';
        };
        task-push-image-cross = pkgs.writeShellApplication {
          name = "task-push-image-cross";
          runtimeInputs = with pkgs; [regctl skopeo nushell];
          text = ''
            # $1: repository URL
            out="${self.packages.${system}.operator-container-multiarch}"
            tag="${self.packages.${system}.operator-container.imageTag}"

            # push the bundle to repo
            skopeo copy --all "oci://''${out}:''${tag}" "docker://''${1}:''${tag}"

            # output the final digest
            nu -c "open $out/index.json | get manifests | last | get digest | print"
          '';
        };

        # other CI scripts
        task-build-docs = pkgs.writeShellApplication {
          name = "task-build-docs";
          runtimeInputs = with pkgs; [mdbook];
          text = ''
            mdbook build docs/mdbook/
          '';
        };
      };
    });
}
