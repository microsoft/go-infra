# git-go-patch

`git-go-patch` is a tool that makes it easier to work with the submodule and Git patch files used by the Microsoft Go repository. The subcommands help with common workflows for patch creation and maintenance.

## Installing

First, install the command:

```
go install github.com/microsoft/go-infra/cmd/git-go-patch@latest
```

> Make sure `git-go-patch` is accessible in your shell's `PATH` variable. You may need to add `$GOPATH/bin` to your `PATH`. Use `go env GOPATH` to locate it.

Then, run the command to see the help documentation:

```
git go-patch -h
```

> `git` detects binaries that start with `git-` and makes them available as `git {command}`.

## Workflow

> ⚠️`am` destroys work in progress in the submodule. See `git go-patch am -h`.

To make changes to a patch file:

1. Open a terminal anywhere within the Microsoft Go repository or its submodule.
2. Use `git go-patch am` to apply patches onto the submodule as a series of commits.
3. Edit the commits as desired.
   1. If it fits your workflow, use `git commit --fixup={commit}` to create fixup commits and `git go-patch rebase` to apply them.
4. Use `git go-patch fp` to rewrite the patch files based on the changes in the submodule.

If the patch files do not apply cleanly (for example, a patch has a conflict after a submodule update), use `git am` inside the submodule to continue the process. Consider using `git am --reject` to create `.rej` files, then use that information to redo the changes, stage the result, and use `git am --continue` to move on to the next patch. See [`git am` documentation](https://git-scm.com/docs/git-am) for more information.
