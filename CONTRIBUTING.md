## Contributing

### Development Environment

This project uses [nix-direnv](https://github.com/nix-community/nix-direnv) for reproducible development environments and [direnv](https://direnv.net/) for automatic environment loading.

To set up:
```sh
# (assuming Nix & direnv is installed)
cd talos-operator
direnv allow
```

This project uses Just as an alternative to Make. Run `just --list` to see the available recipes.

### macOS warning

Tools like Nix end up creating lots of files, which is quite slow on APFS. If you're going to heavily rely on `nix build`, it's recommended to do your work inside a [Linux virtual machine, such as Lima](https://lima-vm.io/).
