{{/*
This template contains the documentation that appears on the "Extensions" tab when a release automation build fails to describe how to retry the build or fix issues that may have occurred.

This file only contains a subset of Markdown syntax, because Azure Pipelines' "Extensions" tab supports a subset. Once filled out, the template is uploaded to the build via [`##vso[task.uploadsummary]`](https://learn.microsoft.com/en-us/azure/devops/pipelines/scripts/logging-commands?view=azure-devops&tabs=bash#uploadsummary-add-some-markdown-content-to-the-build-summary).

Known limitations of the Extensions tab's Markdown renderer:

* Numbered lists are rendered as bulleted lists instead. To make a numbered list, use characters that aren't recognized in Markdown as a list, like `1 - Description of step one`.
* Indentation to make a multi-level bulleted list doesn't work. Use `→` inside a `*` list, instead.
* Indentation to include a new line in a numbered list doesn't work, and leaves the block unindented.
* Leading whitespace makes the renderer treat formatting symbols like `**` and backticks as literal characters.
*/}}

Based on the variables that were set by this build when it failed, here's a suggestion on how to resolve the issue:

{{ if not .LastNonNilEnv }}

    {{ if not .Preapproval }} {{/* microsoft/go build */}}

### ⌚ Get upstream commit for release

This step might time out if the upstream Go tag is not yet available and remains unavailable for a handful of hours since you triggered the build.

If it's reasonable that the tag isn't up yet, simply follow the **Retry** instructions below.

If you expect the tag to be available, look carefully at the tag name being polled in the build and see if there's a typo in the version number. If so, retry the build, but fix the version number in the parameters. Also, cancel and re-run the release-go-images build with the corrected version number.

    {{ else }} {{/* microsoft/go-images build */}}

### ⌚ Wait for go-images dependency flow

This step might time out if the auto-update PR from the Microsoft Go build didn't merge successfully. It could also time out if the version numbers are incorrect.

If you see that the update PR is in progress, simply follow the **Retry** instructions below.

If you expect this to be complete, look carefully at the build logs to find typos or other errors occurring. If necessary, **Retry** the build, but change parameters to fix them.

    {{ end }}

{{ else if ieq .LastNonNilEnv.Name "MicrosoftGoPRNumber" }}

### ⌚ Get sync PR merged commit hash

This step waits for the sync-from-upstream-tag PR to merge, by polling it. Look at the CI status of <https://github.com/microsoft/go/pull/{{ .LastNonNilEnv.Value }}>:

* If the PR CI is taking a while to complete but seems to be progressing normally, simply **Retry** to resume polling.
* If the PR CI failed due to flakiness, retry the PR's CI, then **Retry** to resume polling.
* → If the flakiness can be fixed, work on the fix while the retry continues in case the failure happens again. Then, get the team to review the fix, merge it into the release branch, and try again.
* → Alternatively, you can follow the instructions for the next option to incorporate the fix with the submodule update:
* If the PR CI failed because patches couldn't be applied, [fix the patches](https://github.com/microsoft/go-infra/blob/main/cmd/git-go-patch/README.md#fix-up-patch-files-after-a-submodule-update).
* → If it's a trivial fix, push to the PR directly and **Retry** to resume polling.
* → If the fix is complex, open a new PR that applies your fix on top of the submodule update, get the team to review the fix, and merge it into the release branch. Follow **Retry** instructions, but instead of the old PR number, insert your new PR number.

{{ else if ieq .LastNonNilEnv.Name "MicrosoftGoCommitHash" "MicrosoftGoImagesCommitHash" }}

### ⌚ Wait for internal mirror

Once the sync-from-upstream PR merges, mirroring the merge commit from GitHub to the AzDO internal mirror should only take a few minutes. If it times out, there is likely an outage in some deeper part of the infrastructure.

Look for a mirroring outage already reported in the [First Responder Teams channel](https://teams.microsoft.com/l/channel/19%3aafba3d1545dd45d7b79f34c1821f6055%40thread.skype/First%2520Responders?groupId=4d73664c-9f2f-450d-82a5-c2f02756606d&tenantId=72f988bf-86f1-41af-91ab-2d7cd011db47). If you don't see one reported within the last few hours, post about the mirroring issue there to alert the infra team and get help. Once the issue is resolved, **Retry** to resume polling.

{{ else if ieq .LastNonNilEnv.Name "MicrosoftGoBuildID" "MicrosoftGoImagesBuildID" }}

### ⌚ Wait for internal build

This step waits for an internal build to complete. Failure is not expected here, but the internal build may have flakiness issues, or may suffer from infra outages.

First, go to the build: <https://dev.azure.com/dnceng/internal/_build/results?buildId={{ .LastNonNilEnv.Value }}>. Press "rerun failed jobs" to try again, just in case the failure is due to flakiness. Then follow the **Retry** instructions. If the problem was flakiness, this may get the build back on track.

While the internal build is continuing, investigate the issue the build hit in its first attempt to try to determine if more fixes are needed.

If the problem is not flakiness, make a fix using a PR to the release branch.

    {{ if ieq .LastNonNilEnv.Name "MicrosoftGoBuildID" }}

After submitting the PR, follow the **Retry** instructions below, but replace all fields with the string `nil` then put your PR number into the first field, "microsoft/go PR number to poll for merge". The retry build will then wait for your PR to merge and run an official build using the merged commit. Get the team to review your PR and merge it.

    {{ else }}

After submitting the PR, get the team to review it and merge it. Find the merged commit hash generated by your PR. Follow the **Retry** instructions below, but replace all fields with the string `nil` and put your commit hash into the first field, "microsoft/go-images commit hash to poll for build". The retry will then wait for your commit to be mirrored and run a new official build using the merged commit. Get the team to review your PR and merge it.

    {{ end }}

{{ else if ieq .LastNonNilEnv.Name "MicrosoftGoImagesPRNumber" }}

### ⌚ Wait for go-images update PR merge

This step waits until the PR completes to push the new Microsoft Go build into the microsoft/go-images repository. Look at the CI status of <https://github.com/microsoft/go-images/pull/{{ .LastNonNilEnv.Value }}>:

* If the PR CI is taking a while to complete but seems to be progressing normally, simply **Retry** to resume polling.
* If the PR CI failed due to flakiness, retry the PR's CI, then **Retry** to resume polling.
    * If the flakiness can be fixed, work on the fix while the retry continues in case the failure happens again. Then, get the team to review the fix, merge it into the release branch, and try again.

{{ else }}

No potential causes identified, sorry!

{{ end }}

---

If the build failed for a non-polling or other unusual reason, the info above may be irrelevant to the problem that occurred. Examine the build logs for more information.

For an overview of the release process, see [the release process docs](https://github.com/microsoft/go-infra/tree/main/docs/release-process).

---

# Retry Instructions

{{ if .LastNonNilEnv }}
{{ nextListEntry }} Copy this value:

**{{ .LastNonNilEnv.Value }}**
{{ end }}

{{ nextListEntry }} Press **Run new**

{{ if .LastNonNilEnv }}
{{ nextListEntry }} Paste the number into field **{{ .LastNonNilEnv.Index }}**
{{ end }}

{{ if .Checkboxes }}
{{ nextListEntry }} If the build has successfully run some release steps (Git Tag, etc.), uncheck the corresponding boxes
{{ end }}

{{ if .Preapproval }}
{{ nextListEntry }} If not already enabled, check the 'Approve right now' checkbox
{{ end }}

{{ nextListEntry }} Press **Run**
