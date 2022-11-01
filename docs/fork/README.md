# Microsoft Go fork maintenance

The Microsoft Go infrastructure uses submodules and patch files as the way to apply changes on top of an upstream repository.

Our Git repository is not a fork in the normal sense: our branches do not share common ancestry with an upstream repo's branches. However, it is conceptually still a fork because the patch files may be maintained for a relatively long time compared to a typical feature branch.

These documents describe the reasons for this approach:

* [**Patch files** vs. in-place modifications](patch-vs-in-place.md)
* [**Submodule** vs. fork](submodule-vs-fork.md)

This document shows the branching we use for this repository and our fork:

* [**../Branches**](../branches.md)

We also developed [a tool called `git-go-patch`](../../cmd/git-go-patch) to help maintain the Microsoft Go patches.
Other teams maintaining a submodule+patch fork may also find it useful.
Let us know!
