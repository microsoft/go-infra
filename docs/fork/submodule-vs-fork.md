# Submodule vs. fork

[patch-vs-in-place.md](patch-vs-in-place.md) outlines the choice to use patch files. There are still two main choices for how we acquire the Go source code that we apply the patches to: fork the Go repository, or use a submodule.

A submodule is the right choice for the Microsoft infrastructure. The rest of this document compares these choices.

# Background

## Fork the Go repository
[Debian uses a fork of the Go repository](https://salsa.debian.org/go-team/compiler/golang/-/tree/golang-1.17) to build the Go toolset. The [Quilt](https://wiki.debian.org/UsingQuilt) tool helps developers apply the patches, rewrite them, and build with them.

> The Quilt tool may address some of the following problems with using a fork. The next sections assume we would need to create our own tooling that works across our target platforms.

The advantage of forks is that a developer can clone the fork and build as they would expect to build the upstream repo, with no adjustment to the fork's infrastructure.

## Submodule
We can use Git submodules to download the source code directly from the upstream Git repository. After the download is complete, the full Go Git history is present for developer workflows.

The disadvantage of submodules is that some developers aren't familiar with them. We can mitigate this by ensuring the default build experience sets up the submodule properly, and the patching process is well-documented and doesn't require exotic tools.

See the Git manual page for submodules for more information on how they work and common uses: https://git-scm.com/book/en/v2/Git-Tools-Submodules.

> The `.gitmodules` file points at a single URL that Git will use to fetch each submodule's sources when running `git submodule update`. This seems like it could prevent the use of a mirror of the Go source code, which is required in some situations for security reasons. However, the URL can be overridden with a `-c ...` option, like this:
>
> ```
> git -c submodule.go.url=https://example.org/go-mirror submodule update
> ```

### Similar to submodules: downloading a source tarball (tar.gz/zip)
When Fedora builds the Go toolset, it [uses a spec file](https://src.fedoraproject.org/rpms/golang/blob/rawhide/f/golang.spec) that downloads an official source tarball from `https://go.dev/dl/go*.src.tar.gz`. CBL-Mariner [does the same](https://github.com/microsoft/CBL-Mariner/blob/049d231d4586fdf1fc1c563a2adebada4986b344/SPECS/golang/golang-1.17.spec#L22) because it shares Linux distro ancestry.

Similar to how Debian has patch tooling, Fedora has tools to download source tarballs and build spec files. Using Git submodules, we leverage existing tooling to get essentially the same result.

Git submodules do address a limitation of using `https://go.dev/dl/`. For the Microsoft Go repository, a requirement is to be able to develop on `master` and the tip of release branches. `https://go.dev/dl/` only serves source code for releases, so using submodules to fetch from the upstream Git repository addresses this.

However, if necessary, a submodule's location on disk can be filled by a downloaded source tarball. For example, it might be faster to acquire a source tarball if the Git client or host lacks shallow fetch capabilities, or the source tarball is already available locally.

# Combined Git history

Using a fork means our Git history would be tied to the upstream Go history. Merging upstream commits into the fork combines them with the commits that added the patch files.

Using a repo that contains a submodule means gives us two, decoupled histories:

* Outer history: patch files, project files, and the version/commit of Go to build (the submodule).
* Inner history: the history of the upstream Go source code.

## Porting fixes between release branches
A fork's branches can't be merged with one another to port patch fixes or infrastructure changes. The fork's release branches are based on upstream's release branches, so, for example, an attempt to merge 1.16 into 1.17 will attempt to reapply 1.16 changes onto the 1.17 branch, likely causing conflicts or unintended behavior.

With a submodule, the merge technically has the same risk, but the submodule commit is a single value that's easy to keep track of and resolve conflicts for: it's unlikely to hit this problem without realizing it, and easy to fix.

## Investigation using Git history
It is difficult to navigate backward in history with a fork. Merges happen periodically, so some upstream commits don't have a close ancestor commit that includes the patch files. A bisect isn't able to narrow down an issue to a single upstream commit.

A submodule opens up investigation to more precise bisecting. Patch files *normally* apply across a range of commits, so a dev can bisect a range of upstream commits to find the source of a problem upstream, perhaps due to an unexpected interaction with a patch.

## Creating a new release branch
The combined history of a fork means that once upstream creates a new release branch, we would need to be very careful to start our own release branch from a commit from before the upstream release branch diverged. Otherwise, commits that are in our branch but not in `master` will inadvertently be included in our version of the release branch. This kind of leak could cause unexpected behavior, and may be hard to track down.

With a submodule, when a new upstream release branch diverges from upstream `master`, our repository can spawn a new branch from wherever we think our infrastructure and patches are most applicable, then change the submodule to point at the new release branch. There is no risk of unintended commits being included, because the Go source code version is all controlled by a single commit hash pointer.

# Clear code separation

## Integrity
It isn't trivial to verify the integrity of a fork. Even though the fork uses patch files, there may have also been some changes made directly to the source code. A filesystem or Git comparison is necessary to confirm no changes.

A submodule appears on Git as a clickable link to the origin repository at the submodule's checked-out commit. This makes it clear that the upstream source code is not modified, and shows the version of upstream code being used.

## Repo root ownership
File ownership within a fork can be complicated to explain, which can be confusing and create uncertainty. Some files like `.github/*` and `README.md` have major effects in GitHub (where our fork would be hosted), so these files need to be modified in-place to correctly describe the purpose of the fork and make some infrastructure work properly. At least one new directory also must be added to contain the patch files. Debian uses the `debian/` directory for this.

With a submodule, all files outside the submodule are "ours" and all files in the submodule are "theirs", so it creates no confusion to modify the files in the way GitHub expects us to.

## Easier upstreaming
A fork with patch files checked in requires some extra steps to send a patch to upstream. The patches need to be applied to a fresh copy of the repository, because the fork contains the patch files themselves, which shouldn't be sent to upstream.

A submodule is a full-fledged Git repository, so after applying the patches, they can be sent to upstream directly from the submodule.

