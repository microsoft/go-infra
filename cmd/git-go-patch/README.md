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

## `am` Workflow

> ⚠️ `am` destroys work in progress in the submodule. See `git go-patch am -h`.

### Make changes to a patch file

1. Open a terminal anywhere within the Microsoft Go repository or its submodule.
2. Use `git go-patch am` to apply patches onto the submodule as a series of commits.
3. Edit the commits as desired.
   1. If it fits your workflow, use `git commit --fixup={commit}` to create fixup commits and `git go-patch rebase` to apply them.
4. Use `git go-patch fp` to rewrite the patch files based on the changes in the submodule.

### Fix up patch files after a submodule update

After a submodule update, patches may fail to apply and cause an error like this:

```
error: patch failed: src/[...].go:329
```

To fix this, follow the process to make changes to a patch file. After running `git go-patch am`, you will see the patch failure error appear, with extra instructions about how to use `git am` to resolve it. Then:

1. Make sure your terminal is inside the submodule.
2. Resolve the conflict. There are several ways:
   1. Run `git am -3`. This performs a 3-way merge, and leaves merge conflict markers in the files for manual or tool-assisted fixing.
   2. Run `git am --reject`. This creates a `.rej` file for each file that couldn't be patched, containing the failed chunks for you to apply manually.
   3. Redo the change from scratch.
3. Run `git am --continue` after staging fixes.

See [`git am` documentation](https://git-scm.com/docs/git-am) for more information.
