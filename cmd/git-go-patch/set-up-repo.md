# Setting up a fork repo with git-go-patch

The git-go-patch tool relies on a configuration file to determine where your submodule and patch files are.
For these instructions, we assume your repository is set up like this:

* `.git/`
* `.gitignore`
* `.gitmodules`
* `artifacts/` (a directory that stores temporary build artifacts, listed in `.gitignore`)
* `submodules/`
   * `forked-project/` (the submodule)
* `patches/`
   * `0001-Change-X-to-Z.patch`
   * `0002-Fix-large-number-bug.patch`
* `build.ps1` (a script that applies the patches to the submodule then builds it)

Using a terminal that is inside your repository and outside of the submodule, run:

```
git go-patch init
```

This creates a configuration file, formatted as JSON.
Fill in the fields.
For the above repo structure, they would be:

* `SubmoduleDir` is `submodules/forked-project`
* `PatchesDir` is `patches`

Make sure to check the config file into your repository.

Now, `git go-patch` subcommands that you run in any subdirectory of your repository and inside the submodule will find the configuration file and work with your submodule and your patches.

## Converting an existing fork into patch files

If you have an existing "direct" fork that you want to convert into a "patched submodule" fork, there is no single right way to do it, but these tips might help.

This section assumes you have your fork cloned locally and it has a remote named `origin` for your repo and a remote named `upstream` pointing at the upstream repo.
We also assume familiarity with Git, to be able to adjust the suggestions to fit your project's situation.

First, use this command to determine the latest common commit between your fork and upstream:

```sh
git merge-base origin/main upstream/main
```

We'll call the result `<common-commit>`.
Using this commit hash instead of `upstream/main` means you aren't trying to do an upgrade to the latest version of upstream at the same time you're trying to convert your fork into patch files.
This will save you some conflict resolution!

Then, use commands like these but with your project and upstream's names to set up a submodule:

```sh
git submodule add https://example.org/upstream/repo submodules/forked-project
# Move into the submodule. Git commands now operate on the submodule, not the outer repo.
cd submodules/forked-project
git checkout <common-commit>
cd -
# Add the submodule reference to the outer repo.
git add .
git commit
```

From this point on, make sure not to commit changes to `submodules/forked-project`.
We will be creating temporary commits that don't exist in `https://example.org/upstream/repo`, and it's important that you only commit the submodule when it references a commit that exists in the URL configured in `.gitmodules`.

Next, set up a second remote inside the submodule that points at your fork, and simplify your forked branch's history (remove merge commits and make it linear):

```sh
cd submodules/forked-project
git remote add fork https://example.org/yourfork/repo
git fetch --all
git checkout fork/main
git rebase <common-commit>
cd -
```

You may be prompted to resolve conflicts during `git rebase`.
In general, if you've had to solve merge conflicts when merging from upstream, you'll have to resolve those conflicts again during the rebase process.
If this isn't feasible, or the history of the fork is unimportant, you can `git rebase --abort`, then use these commands to wipe out history and create a single commit that contains the entire diff with a completely new commit message:

```sh
cd submodules/forked-project
git checkout fork/main
git reset --soft <common-commit>
git commit
cd -
```

> You can also use more advanced `git reset`/`git add` commands and/or graphical Git clients to stage specific chunks of the diff and split it into multiple new commits.
> Each commit will be extracted as its own patch file.
>
> Grouping related changes with explanatory commit messages can be a valuable tool in making easy-to-understand patches, because the patches show the commit message above the diff.

Then, go through the first section of the doc to set up the `git go-patch` configuration.

Finally, extract the patch files and commit them:

```sh
# In your repository (outer repository):
git go-patch extract <common-commit>
git add patches
git commit
```

Now, you can use git-go-patch workflows like "`git go-patch apply` -> modify commits -> `git go-patch extract`".
See [the main README.md](README.md) for more information.
