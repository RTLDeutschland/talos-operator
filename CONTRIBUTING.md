# Contributing

## Development Environment

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

## Error handling

Various parts of the operator handle errors differently depending on error context, but the most predominant forms are the following two:

```go
// assuming the context of a function that returns (*ctrl.Result, error)

err := somethingThatMightFail()
if err != nil {
    return nil, fmt.Errorf("something failed: %w", err)
}

err := somethingThatMightFail()
if err != nil {
    r.Recorder.Eventf(someObject, corev1.EventTypeWarning, ValidationError, "something failed: %v", err)
    return nil, nil
}
```

The reason for this distinction is that we use the first type of handling when an error is likely ephemeral and retryable, think temporary network failures or Kubernetes update errors.

The second type of error is how we communicate user errors to the user, such as duration parsing failures or other invalid input scenarios. Kubernetes does a good job of aggregating events, so we just keep sending them on the default requeue interval until the user intervenes. The rest of the reconcile loop proceeds as normal because we returned `nil, nil`.
