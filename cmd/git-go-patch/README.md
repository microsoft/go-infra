# git-go-patch

`git-go-patch` is a tool that makes it easier to work with a "patched submodule fork" workflow.
It includes several subcommands that help with specific parts of the process.
The [Microsoft Go repository](https://github.com/microsoft/go) uses this tool, and it's currently the main reason the tool is being developed and maintained.

A "patched submodule fork" is when you don't hit GitHub's "Fork" button, but rather maintain your own Git repository that contains the upstream repo as a [submodule](https://git-scm.com/book/en/v2/Git-Tools-Submodules) along with `*.patch` files that modify the submodule when you use `git apply patches/*.patch`.
For more information about why we chose this style of fork for the Microsoft Go repository, see [/docs/fork](https://github.com/microsoft/go-infra/tree/main/docs/fork).

Related documentation:

* [set-up-repo.md](set-up-repo.md) - How to set up your repo to work with git-go-patch.
* [microsoft/go Developer Guide](https://github.com/microsoft/go/blob/microsoft/main/eng/doc/DeveloperGuide.md) - How to use this tool as part of a microsoft/go development workflow.

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
   * Commit-then-rebase workflow:
     1. Make some changes in the submodule and create commits.
     1. Use `git go-patch rebase` to start an interactive rebase.
        1. Reorder the list to put each of your commits under the patch file that it applies to.
        1. For each commit, choose `squash` if you want to edit the commit message or `fixup` if you don't. Use `pick` if you want to create a new patch file. (All of the built-in `rebase` options work, but these are the most common.)
        1. The `git` command displays information about how to continue and finish the rebase.
   * Interactive rebase `edit`:
     * Useful if you have an exact change in mind or your commits would hit rebase conflicts.
     1. Use `git go-patch rebase` to start an interactive rebase before you've made any changes.
     1. Mark commits to edit with `edit` and save/close the file to continue.
     1. When the rebase process stops at a commit, make your changes, use `git commit --amend` to edit the commit, and `git rebase --continue` to move on.
   * Other `git rebase` features like `git commit --fixup={commit}` also work as expected.
1. Use `git go-patch extract` to rewrite the patch files based on the changes in the submodule.

### Recovering from a bad rebase

It's possible to accidentally squash a commit into the wrong patch file during a rebase.
This makes the change show up in the wrong patch file.
To fix this, sometimes it's simplest to start from scratch and copy changes back in manually.
However, many general `rebase` recovery methods will work.
Here are a few strategies:

#### Go back to the pre-rebase commit in the submodule

You might be able to go back to the pre-rebase commit and try the `rebase` again.
The original commit might be in your terminal history.
Also, try `git reflog` in the submodule.

If you anticipate a challenging rebase, you can also preemptively create a temporary branch in the submodule or note down the commit hash before starting the rebase.
This way, you know for sure that you can get back to a known state if it goes wrong.

#### Reallocate your changes

Sometimes the original commits aren't worth recovering, only the sum total of all the changes you made.
Then, you can create new commits to try the rebase again.

1. In the submodule, `git checkout -B bad` to save your current state as the branch `bad`.
1. In the outer repository, check out the unchanged patch files.
1. Run `git apply -f` to apply the patch files to the submodule, changing the submodule's `HEAD`.
1. In the submodule, `git checkout bad -- .` to copy the changes from the `bad` branch into the working directory.
1. Split the staged changes into your desired commits.
1. Try the rebase again.

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
