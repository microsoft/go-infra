

Based on the variables that were set by this build when it failed, here's a suggestion on how to resolve the issue:



### ⌚ Wait for go-images update PR merge

This step waits until the PR completes to push the new Microsoft build of Go build into the microsoft/go-images repository. Look at the CI status of <https://github.com/microsoft/go-images/pull/8>:

* If the PR CI is taking a while to complete but seems to be progressing normally, simply **Retry** to resume polling.
* If the PR CI failed due to flakiness, retry the PR's CI, then **Retry** to resume polling.
    * If the flakiness can be fixed, work on the fix while the retry continues in case the failure happens again. Then, get the team to review the fix, merge it into the release branch, and try again.



---

If the build failed for a non-polling or other unusual reason, the info above may be irrelevant to the problem that occurred. Examine the build logs for more information.

For an overview of the release process, see [the release process docs](https://github.com/microsoft/go-infra/tree/main/docs/release-process).

---

# Retry Instructions


1 -  Copy this value:

**8**


2 -  Press **Run new**


3 -  Paste the number into field **4**






4 -  Press **Run**
