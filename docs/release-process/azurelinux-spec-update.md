# Azure Linux spec file update instructions

This document describes how to update the Azure Linux 3.0 spec file to use a new version of Go.

In the following steps, Replace the stand-in text `[version]` with the new version of Go to update to, `1.X.Y-Z`.

1. Go to [the microsoft/go GitHub releases](https://github.com/microsoft/go/releases) and open the release for the new version of Go.

1. Download and open `assets.json` and the file ending in `.src.tar.gz.sha256`.

1. Find `goSrcURL` in `assets.json` and copy the URL.
    * Keep this file open--you will use it again later.

1. Go to [Azure Linux - Source Tarball Publishing](https://dev.azure.com/mariner-org/mariner/_build?definitionId=2284&_a=summary).

1. Click "Run pipeline", and:

    1. For `URL from where to download [...]`, paste `goSrcURL`.

    1. Select the filename and extension from the end of the URL and copy it.

    1. For `Full Name of the source tarball [...]`, paste the filename and extension.

    1. Click "Run".
        * There is no need to wait for this to complete before continuing.
        * If an error occurs and wasn't due to a copy paste mistake, discuss internally with Go team.
        The likely action will be to contact the Azure Linux team.

1. In a Linux-like environment, clone https://github.com/microsoft/azurelinux and create a branch based on `3.0-dev`.
    * Linux, because we will run a bash script provided by Azure Linux.

1. Modify `SPECS/golang/golang.spec`:

    1. Run `./toolkit/scripts/update_spec.sh 'Bump version to [version]' SPECS/golang/golang.spec`

    1. Observe the new entry in the `%changelog` section of `SPECS/golang/golang.spec` and correct any issues.

    1. Near the top of `SPECS/golang/golang.spec`, update the `Version:` field to `[version]` but without the `-1` part (or `-2`, etc.).
        * If the `[version]` release doesn't correspond with a new upstream Go release, there may be no change to `Version:`.

    1. Update the number in `Release:`. This does *not* correspond to `[version]`. Instead, set it to the next higher number, or reset it to `1` if `Version` changed.
        * The `Release:` number may be higher than the the `Z` number in the Microsoft Go release if the Go package has been updated in Azure Linux without a corresponding Microsoft Go release.

    1. Update `ms_go_filename` to the filename and extension at the end of `goSrcURL`.

    1. Update `ms_go_revision` to the revision (`Z`) in `[version]`, if it differs.

1. Modify `SPECS/golang/golang.signatures.json`:

    1. Find the property with its key matching the previous value of `ms_go_filename`
    
    1. Replace the key with the new `ms_go_filename`.

    1. Replace the value with the checksum in the file ending in `.src.tar.gz.sha256`.

1. Modify `cgmanifest.json`

    1. Find the entry with name `golang` and update the `version` field to `[version]` without `-Z`.

    1. Go to the Microsoft Go release page again.

    1. Right click -> "copy link" of the file ending in `.src.tar.gz`.

    1. Paste the link into the `downloadUrl` value.

1. If the major version has changed, a new bootstrap layer may be needed. Follow the patterns in the spec file to add a new layer.

1. Commit and push your changes.
    * Suggested commit message: `Bump golang version to [version]`

1. In https://github.com/microsoft/azurelinux/pulls, create a new pull request, and:

    1. Switch the base branch to `3.0-dev`.

    1. Check all boxes other than "Packages depending on static components modified in this PR (Golang, `*-static` subpackages, etc.) have had their `Release` tag incremented."

    1. Summary: `Bump version to [version]`

    1. Change Log: `- Bump version to [version]`

    1. Does this affect the toolchain? `No`

    1. Delete optional sections.

    1. Leave Test Methodology as is for now.

1. Go to [Azure Linux - [OneBranch]-Unified-Buddy-Build](https://dev.azure.com/mariner-org/mariner/_build?definitionId=2190).

1. Click "Run pipeline", and:

    1. For `Mariner GitHub topic branch to pull or PR with PR-XXXX [...]`, enter `PR-XXXX` where `XXXX` is the draft PR number.

    1. For `Space-separated list of core specs to build [...]`, enter `golang`.

    1. Leave remaining fields at default values.

    1. Press Run.

1. Copy the build's URL.

1. Edit the URL into the draft PR, under "Test Methodology".

1. Mark the PR as ready for review.

1. Wait for comments or approvals.

1. If/when you get an approval that allows it, merge the PR.

1. Done!

## Improvements

We intend to incorporate this process into release automation.
See https://github.com/microsoft/go-lab/issues/79.
