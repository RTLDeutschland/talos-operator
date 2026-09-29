# kubectl-talos native cross-compile build logic
{
  pkgs,
  lib,
  version,
  src,
  vendorHash,
  env,
  ldflags,
}: let
  # release matrix:
  goTargets = [
    {
      goos = "linux";
      goarch = "amd64";
    }
    {
      goos = "linux";
      goarch = "arm64";
    }
    {
      goos = "darwin";
      goarch = "amd64";
    }
    {
      goos = "darwin";
      goarch = "arm64";
    }
    {
      goos = "windows";
      goarch = "amd64";
    }
    {
      goos = "windows";
      goarch = "arm64";
    }
  ];

  kubectlTalosFor = {
    goos,
    goarch,
  }: let
    exe = lib.optionalString (goos == "windows") ".exe";
  in
    pkgs.buildGoModule (finalAttrs: {
      pname = "kubectl-talos";
      inherit version src;

      subPackages = ["cmd/kubectl-talos"];
      inherit env ldflags;

      vendorHash = vendorHash;
      proxyVendor = true;

      # go test cannot execute cross-compiled binaries
      doCheck = false;

      # buildGoModule clobbers env.GOOS/GOARCH from the go toolchain,
      # so the target platform must be exported as a build hook
      preBuild = ''
        export GOOS=${goos}
        export GOARCH=${goarch}
      '';

      # buildGoModule install places cross-compiled binaries in
      # $out/bin/GOOS_GOARCH, rename to release format
      postInstall = ''
        if [ -d $out/bin/${goos}_${goarch} ]; then
          mv $out/bin/${goos}_${goarch}/kubectl-talos${exe} $out/bin/kubectl-talos_${goos}_${goarch}${exe}
          rmdir $out/bin/${goos}_${goarch}
        else
          mv $out/bin/kubectl-talos${exe} $out/bin/kubectl-talos_${goos}_${goarch}${exe}
        fi
      '';

      meta = {
        description = "kubectl plugin for the Talos operator (${goos}/${goarch})";
        license = lib.licenses.mit;
      };
    });
in
  pkgs.symlinkJoin {
    name = "kubectl-talos-crosscompile";
    paths = map kubectlTalosFor goTargets;
  }
