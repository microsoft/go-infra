# Azure Linux spec file update instructions

This document describes how to update the Azure Linux 3.0 spec file to use a new version of Go.

## Prerequisites

Join [azurelinuxde-frja](https://coreidentity.microsoft.com/manage/Entitlement/entitlement/azurelinuxde-frja) with `Azl-Collaborator` role.

Example justification: `Maintaining Microsoft build of Go Azure Linux package`

## Manual workflow

For each version released:

1. Find and open the [microsoft-go](https://dev.azure.com/dnceng/internal/_build?definitionId=958) build that produced the new Microsoft build of Go release.
1. From the URL, copy the build ID.
    * For example, in `[...]results?buildId=2535309&view=results`, it is `2535309`.
1. Go to [microsoft-go-infra-update-azure-linux](https://dev.azure.com/dnceng/internal/_build?definitionId=1405).
1. Click "Run pipeline", and:
    1. For `The ID of the microsoft-go build pipeline`, paste the build ID.
    1. Fill other fields as instructed in the dialog.
    1. Click "Run".
1. Wait for the pipeline to complete.
1. Go to the generated PR: either look in the pipeline's logs for the link or check your GitHub notifications.
    1. **If** this is a new major release, follow instructions in [Adding a new major version](#adding-a-new-major-version) before proceeding.
1. Follow the instructions in the PR to finalize it.
1. Done!

## Semi-automated workflow

This workflow uses tools to avoid time consuming copy paste and manual CI retry after tarball mirroring.

1. Use https://github.com/microsoft/go-lab/tree/main/goaztool to mirror the source tarballs to Azure Linux storage.
    * For example, `go run github.com/microsoft/go-lab/goaztool/cmd/azlmirror -versions 1.24.5-1,1.25.3-1`
    * Make sure to do this first, so you don't have to manually re-run the GitHub CI pipelines later.

Follow manual workflow to create PRs:

1. Find and open each microsoft-go build...
1. ... See above manual workflow ...
1. Wait for each microsoft-go-infra-update-azure-linux pipeline to complete.
    1. **If** this is a new major release, follow instructions in [Adding a new major version](#adding-a-new-major-version) before proceeding.

Ignore instructions in each PR, and instead:

1. Use https://github.com/microsoft/go-lab/tree/main/goaztool to create a buddy build for each PR:
    1. For example, `go run github.com/microsoft/go-lab/goaztool/cmd/azlbuddy -prs 1234`
        * Replace `1234` with the PR URL or number.
    1. Paste a link to the buddy build from the command output into the PR conversation.
    1. Mark the PR ready to review.
1. Done!

## Automated workflow

Tracked by [microsoft/go-lab#79](https://github.com/microsoft/go-lab/issues/79).

## Adding a new major version

The tool doesn't totally handle the creation of a new major version of Go.
The expected end state is that we only have `golang.spec` (for `N`) and `golang-<N-1>.spec` files.
No old `golang-<X>.spec` files should be left around.

This process should have a good result for a new major version `N`, supported `N-1`, and removing `N-2`:

1. Open the auto-PR locally.
1. Go to `SPECS/golang/golang.spec`.
1. Reset to the original `3.0-dev` branch state for:  
    `golang.spec`  
    `golang.signatures.json`
    * E.g. `git restore --source=origin/3.0-dev --staged --worktree -- golang.spec golang.signatures.json`
1. Copy:  
    `golang.spec` to `golang-<N-1>.spec`  
    `golang.signatures.json` to `golang-<N-1>.signatures.json`
1. Restore the auto-generated updates to `golang.spec` and `golang.signatures.json`.
    * E.g. `git restore --source=HEAD --staged --worktree -- golang.spec golang.signatures.json`
1. Delete:  
    `golang-<N-2>.spec`  
    `golang-<N-2>.signatures.json`
1. If the new major version of Go requires an update to the bootstrap version of Go, update `golang.spec` accordingly.
    1. Add a new `SourceX` entry.
    1. Add a new `%prep` entry.
    1. Add a new `%build` call to `go_bootstrap X`.
1. In the repo root `cgmanifest.json`, remove the entry with name `golang` and `version` `<N-2>.*`.
1. Update `LICENSES-AND-NOTICES/SPECS/LICENSES-MAP.md` and `LICENSES-AND-NOTICES/SPECS/data/licenses.json`.
    * E.g. (Linux/WSL with podman (or Docker))
        ```
        podman pull mcr.microsoft.com/azurelinux/base/core:3.0
        podman run -v "$(pwd):/work:z" -w /work -it --rm mcr.microsoft.com/azurelinux/base/core:3.0
        tdnf install -y python3 python3-pip
        pip install -r ./toolkit/scripts/requirements.txt
        ./toolkit/scripts/license_map.py --no_check --update --remove_missing         LICENSES-AND-NOTICES/SPECS/data/licenses.json         LICENSES-AND-NOTICES/SPECS/LICENSES-MAP.md         SPECS SPECS-EXTENDED SPECS-SIGNED
        exit
        ```
    * If this doesn't turn out quite right, don't worry: AzL GitHub Actions CI will catch it and fail.
1. Commit the changes and push back to the automatically opened PR.

## Resources

The Azure Linux PR generation implementation is in [/cmd/releasego/update-azure-linux.go](../../cmd/releasego/update-azure-linux.go).

These are the Azure Linux pipelines involved in finishing up the PR:

* [Source Tarball Publishing](https://dev.azure.com/mariner-org/mariner/_build?definitionId=2284)
* [Buddy Build](https://dev.azure.com/mariner-org/mariner/_build?definitionId=2190)

## Improvements

We intend to incorporate this process into release automation.
See https://github.com/microsoft/go-lab/issues/79.
