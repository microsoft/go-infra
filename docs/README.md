# Microsoft Go Infrastructure

This repository is used by Microsoft to build Go. See the root
[README.md](../README.md) for the project overview, and the subdirectories here
for more info about parts of the infra.

* [pipeline-yml-style.md](pipeline-yml-style.md) - Style principles and quirk
  notes for our YAML pipelines here and in microsoft/go.

## Branches

This repository has only one maintained branch, `main`. This branch contains the
infrastructure used by Microsoft to build any Go release branch.

This is similar to how all release branches of
[`go`](https://go.googlesource.com/go/) are built and released using the one
branch of [`x/build`](https://go.googlesource.com/build/).

Using a single branch has significant tradeoffs. On the plus side:

* :+1: Parts of the infra that *do not* take part in the Go build are intuitive
  to maintain. We would only use the `main` version of this code anyway, so it
  makes sense to only have a single branch where the code is maintained.
  * If this code were in a repository with release branches, every release
    branch would have **dead code**: a copy of the infra at the point in time
    that the release branch forked from `main`.
  * For example: auto-updating the Docker image repo is not part of the Go
    build. It runs an Azure Pipelines `yml` file in the main branch.
* :+1: Parts of the infra that *do* take part in the Go build can be shared more
  easily.
  * Instead of cherry-picking every `main` commit into every applicable release
    branch all the time, the `go-infra` module dependency can simply be updated
    to the latest version to get a batch of fixes.

There are downsides:

* :confused: When making a change to `go-infra`, it can be (very!) hard to tell
  if the change will break a release branch.
  * This can be mitigated by setting up PR validation jobs that that simulate an
    update across each active release branch and run a Go build in each one.
* :confused: It is more difficult to design changes or new infra features when
  they must be compatible with every release branch at the same time.
  * It may be impossible, in some cases. This forces the code to be maintained
    in the repository rather than `go-infra`.

It's important to consider the balance. This is why not all of the code
Microsoft uses to build Go is stored in this repo, even if it could be migrated
(even partially). A significant amount of important Go build and packaging logic
is stored in the repo where it's used.
