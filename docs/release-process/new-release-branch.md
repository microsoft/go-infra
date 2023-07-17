# Creating a new release branch

To release any given major.minor version of Go, the microsoft/go repository needs to have a branch tracking that version.
Early minor/patch/prerelease versions are likely to not have a branch yet, e.g. 1.19, 1.19beta1, and 1.19rc1.

You can check for a specific release branch here: [list of all microsoft/go branches beginning in microsoft/release-branch.go](https://github.com/microsoft/go/branches/all?query=microsoft%2Frelease-branch.go).

> In Go versions prior to 1.19, you may notice an additional `microsoft/dev.boringcrypto.go*` branch.
> This is no longer necessary in Go 1.19+.

If there isn't an existing branch, you need to create one.
The steps below use `1.x` as a stand-in for the target version of Go:

1. Identify where to create the branch on the microsoft/go repo.
    * In most cases, simply use the latest commit of the `microsoft/main` branch in the microsoft/go repo.
        * This branch tracks the upstream `master` branch.
        * In general, upstream ships "beta" builds from `master`, and only splits a release branch before the first "rc" release.
        * In microsoft/go, we always split a release branch, even for a "beta" release. This simplifies our release infra by letting it assume that we always use a release branch for releases.
    * If you *know* that a PR has been merged that works in `microsoft/main` but would make `1.x` fail, choose the commit before that PR was merged.
    * If in doubt, choose the latest `microsoft/main` commit. If something ends up failing, you can still fix it later.

1. Create a branch in microsoft/go called `microsoft/release-branch.go1.x` on the selected commit.
    * E.g. 1.19rc1 -> `microsoft/release-branch.go1.19`
    * You can use `git push origin <commit>:refs/heads/microsoft/release-branch.go1.x`.
    * You can use the GitHub web UI by opening the tree at the target commit, typing the new branch name in the branch selection dropdown, and clicking "Create ...".
    * If you don't have permission to create branches in microsoft/go, ask a team member to add you to [golang-compiler](https://repos.opensource.microsoft.com/orgs/microsoft/teams/golang-compiler).

1. Clone/fetch the repo and create a dev branch.  
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

1. Request review from the team in a Teams chat.

1. After approval and green PR CI, merge.

1. To ensure the change works in internal CI, go to [microsoft-go](https://dev.azure.com/dnceng/internal/_build?definitionId=958) and wait for a build to appear and start running.
    * This is not necessary if you're planning to run the release automation next. It runs an internal build as part of the release process and you will see any issues then.

Done!

> Some of the steps above make assumptions about how you have Git set up and how you normally use Git and GitHub.
> For the most part, the shell commands are only suggestions intended to be easy to understand.
