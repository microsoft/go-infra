

Based on the variables that were set by this build when it failed, here's a suggestion on how to resolve the issue:



### ⌚ Get sync PR merged commit hash

This step waits for the sync-from-upstream-tag PR to merge, by polling it. Look at the CI status of <https://github.com/microsoft/go/pull/42>:

* If the PR CI is taking a while to complete but seems to be progressing normally, simply **Retry** to resume polling.
* If the PR CI failed due to flakiness, retry the PR's CI, then **Retry** to resume polling.
* → If the flakiness can be fixed, work on the fix while the retry continues in case the failure happens again. Then, get the team to review the fix, merge it into the release branch, and try again.
* → Alternatively, you can follow the instructions for the next option to incorporate the fix with the submodule update:
* If the PR CI failed because patches couldn't be applied, [fix the patches](https://github.com/microsoft/go-infra/blob/main/cmd/git-go-patch/README.md#fix-up-patch-files-after-a-submodule-update).
* → If it's a trivial fix, push to the PR directly and **Retry** to resume polling.
* → If the fix is complex, open a new PR that applies your fix on top of the submodule update, get the team to review the fix, and merge it into the release branch. Follow **Retry** instructions, but instead of the old PR number, insert your new PR number.



---

If the build failed for a non-polling or other unusual reason, the info above may be irrelevant to the problem that occurred. Examine the build logs for more information.

For an overview of the release process, see [the release process docs](https://github.com/microsoft/go-infra/tree/main/docs/release-process).

---

# Retry Instructions


1 -  Copy this value:

**42**


2 -  Press **Run new**


3 -  Paste the number into field **1**






4 -  Press **Run**
