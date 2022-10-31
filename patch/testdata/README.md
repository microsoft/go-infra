# patch testdata

This dir contains the testdata used to test git-go-patch functionality in the patch package.

The most realistic scenario for microsoft/go patch conflict fixups is [TestApplyMiddleConflict](TestApplyMiddleConflict/).

Each scenario directory has `before` and `after` patch dirs that are used to test `git go-patch extract`:

* `before` represents the state of the patches in the outer repo, before `extract` starts.
* `after` represents the changes made by the dev inside the submodule, and how the patches should look after `extract`.
    * If a patch file ends in `_matching.patch`, it means that the tests should verify `extract` recognizes the `before` and `after` versions of that patch are the same.

As of writing, the tests only verify `_matching.patch` and that no errors occur.

# moremath.pack

This is a Git archive created using [`git bundle`](https://git-scm.com/docs/git-bundle) containing a pointless testbed Go module "moremath".
It contains just enough code and Git data to use it as a fake upstream repo for patching tool tests.

This directory contains some cross-platform PowerShell utility scripts to help modify the pack file and patches.
Use these commands in this directory to modify the `.pack`:

```sh
pwsh init.ps1
# ... Make edits in moremath
pwsh repack.ps1
```

To develop a patch, make use of the `.git-go-patch` and utility scripts in this directory:

```sh
pwsh init.ps1
# ... Move patches to edit to "patch-dev" directory
git go-patch apply
# ... Make edits, commits in moremath
git go-patch extract
# ... Move patches out of the "patch-dev" directory
pwsh clean.ps1
```

These commands set up the moremath repo inside the go-infra repo, but not as a submodule, so some operations may behave differently than normal and some `git go-patch` options may not work completely.
Sticking with the utilities and process above helps avoid some issues.
