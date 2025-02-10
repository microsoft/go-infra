# Creating a new release branch

To release any given major.minor version of Go, the microsoft/go repository needs to have a branch tracking that version.
Microsoft build of Go doesn't ship betas or release candidates.

## When to create the branch

Keep an eye on https://groups.google.com/g/golang-dev for the release freeze lift announcement and create the branch then, if possible.

### Release cycle

Upstream Go follows the [*Go release cycle*](https://go.dev/s/release).

During the upstream release freeze, we keep updating `microsoft/main` with the latest changes from `master`.
Our CI sometimes catches issues with upstream beta/RC builds.
We can fix issues upstream in `master`, and they will automatically be merged or cherry-picked to the release branch by upstream.
Our CI may also catch patch conflicts that we need to resolve before the release.

When the release freeze lifts, we split our release branch from `microsoft/main`.
The timing is important, but not critical: forking later might take extra work, but it is always fixable.

## How to create the branch

If there isn't an existing branch, you need to create one.
You can check for a specific release branch here: [list of all microsoft/go branches beginning in microsoft/release-branch.go](https://github.com/microsoft/go/branches/all?query=microsoft%2Frelease-branch.go).

The steps below describe how to create the branch, using `1.x` as a stand-in for the new version of Go:

> [!NOTE]
> Some of the steps below make assumptions about how you have Git set up and how you normally use Git and GitHub.
> For the most part, the shell commands are only suggestions intended to be easy to understand.

1. Identify where to create the branch on the microsoft/go repo.
    * In most cases, simply use the latest commit of the `microsoft/main` branch in the microsoft/go repo.
    * If you are late to split the branch and *know* that a PR has been merged that works in `microsoft/main` but would make `1.x` fail, choose the commit before that PR was merged.
    * If in doubt, choose the latest `microsoft/main` commit. If something ends up failing, you can still fix it later.

1. Create a branch in microsoft/go called `microsoft/release-branch.go1.x` on the selected commit.
    * E.g. 1.19 -> `microsoft/release-branch.go1.19`
    * You can use the GitHub web UI by opening the tree at the target commit, typing the new branch name in the branch selection dropdown, and clicking "Create ...".
   * You can use `git push origin <commit>:refs/heads/microsoft/release-branch.go1.x`.
    * If you don't have permission to create branches in microsoft/go, ask a team member to add you to [golang-compiler](https://repos.opensource.microsoft.com/orgs/microsoft/teams/golang-compiler).

1. Clone/fetch the repo and create a dev branch. We need to do initial branch setup.
    ```
    # In an existing clone:
    git fetch --all
    git checkout -b dev/myname/fork1.x origin/microsoft/release-branch.go1.x
    ```

1. Update the submodule to point at the latest `1.x` commit:
    1. First, enter the submodule: `cd go`
    1. Determine the commit to use. If an upstream branch hasn't been created yet (e.g. 1.19beta1), use the latest `origin/master` commit. If one has been created, make sure you have the latest state using `git fetch --all`, then use `origin/release-branch.go1.x`.
    1. Use `git checkout`, e.g. `git checkout origin/release-branch.go1.x`.
    1. Leave the submodule: `cd ..`
    1. Stage the submodule change with `git add go`
    1. `git commit` with a message like "Update submodule for 1.x".
    1. Push the commit and submit the branch as a microsoft/go PR into the branch you created earlier.
    1. Make sure the upstream branch and commit are available in the internal [microsoft-go-mirror](https://dev.azure.com/dnceng/internal/_git/microsoft-go-mirror) repo.
        * If not, queue [microsoft-go-infra-upstream-sync](https://dev.azure.com/dnceng/internal/_build?definitionId=1061) to run with default parameters. It automatically mirrors all upstream release branches.
        * If the upstream sync pipeline ran relatively recently, this step isn't necessary.
        * Missing branches/commits can cause the internal build to fail in a later step. Public CI doesn't use the internal mirror, so it won't catch this issue.

1. Fix up PR CI failures.
    * Changing the submodule commit can cause patch conflicts, so you may need to resolve them. See [the git-go-patch README](/cmd/git-go-patch/README.md).
    * Add commits onto your dev branch. If necessary, you can add `git revert` commits for changes in `microsoft/main` that don't apply to `1.x`.

1. After approval and green PR CI, merge.

1. To ensure the change works in internal CI, go to [microsoft-go](https://dev.azure.com/dnceng/internal/_build?definitionId=958) and wait for a build to appear and start running.
    * This is not necessary if you're planning to run the release automation next. It runs an internal build as part of the release process and you will see any issues then.

Done!
