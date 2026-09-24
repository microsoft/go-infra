# Microsoft OSS repository JIT elevation

`elevate` requests temporary administrator access to a GitHub repository through
the Microsoft Open Source Management Portal.

By default, the command inspects the current Git repository. It prefers a
`github.com` remote named `upstream`, then `origin`, then any other unambiguous
GitHub remote. Pass `OWNER/REPO` to target a repository explicitly. The
`-repo OWNER/REPO` flag is also supported.

## Install

Install the latest version with Go:

```console
go install github.com/microsoft/go-infra/cmd/elevate@latest
```

Go installs the `elevate` executable in `GOBIN`. When `GOBIN` is unset, the
default location is `$(go env GOPATH)/bin`. Ensure that directory is on `PATH`.
For example, on macOS or Linux:

```console
export PATH="$(go env GOPATH)/bin:$PATH"
```

Confirm the installation:

```console
elevate -h
```

## Usage

```console
elevate
Brief description for the JIT elevation: Investigate a release pipeline failure

Repository:  microsoft/go-infra
Portal URL:  https://repos.opensource.microsoft.com/orgs/microsoft/repos/go-infra/jit/grant
Description: "Investigate a release pipeline failure"

Request JIT administrator elevation? [y/N]: y
```

The JIT operation is available only through an authenticated portal browser
session. The command therefore opens a visible Chrome, Edge, or Chromium window,
navigates directly to the repository's `/jit/grant` form, fills it in, and
verifies the JIT network request before reporting success. Complete Microsoft
sign-in in that window if prompted.

The browser session is kept in a dedicated directory under the OS user cache so
later runs can reuse the portal login. Use `-profile-dir` to choose a different
location. The command creates that directory with user-only permissions. Use
`-browser` to select a browser executable if one is not discovered automatically.

The command never submits an elevation until both a non-empty description and an
explicit `y` or `yes` confirmation have been provided.

To elevate a repository other than the one in the current directory:

```console
elevate microsoft/go-infra
```
