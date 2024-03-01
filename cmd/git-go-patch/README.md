# git-go-patch

`git-go-patch` is a tool that makes it easier to work with a "patched submodule fork" workflow.
It includes several subcommands that help with specific parts of the process.
The [Microsoft Go repository](https://github.com/microsoft/go) uses this tool, and it's currently the main reason the tool is being developed and maintained.

A "patched submodule fork" is when you don't hit GitHub's "Fork" button, but rather maintain your own Git repository that contains the upstream repo as a [submodule](https://git-scm.com/book/en/v2/Git-Tools-Submodules) along with `*.patch` files that modify the submodule when you use `git apply patches/*.patch`.
For more information about why we chose this style of fork for the Microsoft Go repository, see [/docs/fork](https://github.com/microsoft/go-infra/tree/main/docs/fork).

Related documentation:

* [set-up-repo.md](set-up-repo.md) - How to set up your repo to work with git-go-patch.

## Installing

First, use Go to build and install the command:

```
go install github.com/microsoft/go-infra/cmd/git-go-patch@latest
```

> Make sure `git-go-patch` is accessible in your shell's `PATH` variable. You may need to add `$GOPATH/bin` to your `PATH`. Use `go env GOPATH` to locate it.

Then, run the command to see the help documentation:

```
git go-patch -h
```

> `git` detects that our `git-go-patch` executable starts with `git-` and makes it available as `git go-patch`. The program still works if you call it with its real name, but we think it's easier to remember and type something that looks like a `git` subcommand.

# Subcommands

## Make changes to a patch file

Sometimes you have to fix a bug in a patch file, add a new patch file, etc., and `apply`, `rebase`, and `extract` can help.

1. Open a terminal anywhere within the repository containing the patch files or the submodule.
1. Use `git go-patch apply` to apply patches onto the submodule as a series of commits.
1. Navigate into the submodule.
1. Edit the commits as desired.
   1. `git go-patch rebase` starts an interactive rebase, allowing you to do any normal Git rebase actions like reorder, squash, drop, and edit.
   1. If it fits your workflow, use `git commit --fixup={commit}` to create fixup commits and `git go-patch rebase` to apply them.
1. Use `git go-patch extract` to rewrite the patch files based on the changes in the submodule.

## Fix up patch files after a submodule update

Every so often, you need to update your submodule to the latest version of the upstream repo.
Just like a `rebase` or `merge`, this can generate conflicts when the patches no longer apply cleanly.
The error may look like this:

```
error: patch failed: src/[...].go:329
```

To fix this, follow the process to make changes to a patch file. While running `git go-patch apply`, you will see the patch failure error appear, with extra instructions about how to use `git am` to resolve it. Then:

1. Make sure your terminal is inside the submodule.
1. Resolve the conflict. There are several ways:
   1. Run `git am -3`. This performs a 3-way merge, and leaves merge conflict markers in the files for manual or tool-assisted fixing.
   1. Run `git am --reject`. This creates a `.rej` file for each file that couldn't be patched, containing the failed chunks for you to apply manually.
   1. Redo the change from scratch.
   1. See [`git am` documentation](https://git-scm.com/docs/git-am) for more information.
1. Stage your fixes.
1. Run `git am --continue` to create the fixed-up commit.
1. If there are more conflicts, go back to step 2.
1. Run `git go-patch extract` to save the fixes to your repository's patch files.

When creating a commit with the fixed patch files, make sure not to include the submodule change.
`git go-patch apply` creates temporary local commits inside the submodule with unique commit hashes.
References to these hashes won't work in other clones of the repository, causing submodule initialization errors.

If you have many patch files authored by different developers and it isn't reasonable for one person to resolve all the conflicts, you can fix a few patches and run `git go-patch extract` to save all the fixes completed so far.
Be careful when staging your WIP patch files in the outer repo, because `extract` doesn't fully understand this situation and will delete the patches that haven't been fixed up yet.
The next dev to work on resolution can then check out the WIP branch and run `git go-patch apply` to pick up where the last dev left it.

## Init submodule and apply patches with a fresh clone

`git go-patch apply` understands how to set up a repo's submodules, so you can use it to make it easy for a dev to look at your fork's modifications after a fresh clone:

```sh
git clone https://example.org/my/project proj
cd proj
git go-patch apply
# Proj and proj's submodule are now ready to examine.
```

However, in build scripts, you may want to use traditional Git commands to avoid the dependency on our tool in production environments. We suggest:

```
git submodule update --init --recursive
cd submodule
git apply ../patches/*.patch
```

If you are using Azure DevOps or similar CI mechanism, it may handle submodule initialization for you.
