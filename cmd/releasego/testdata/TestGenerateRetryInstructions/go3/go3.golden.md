

Based on the variables that were set by this build when it failed, here's a suggestion on how to resolve the issue:



### ⌚ Wait for internal build

This step waits for an internal build to complete. Failure is not expected here, but the internal build may have flakiness issues, or may suffer from infra outages.

First, go to the build: <https://dev.azure.com/dnceng/internal/_build/results?buildId=2004985>. Press "rerun failed jobs" to try again, just in case the failure is due to flakiness. Then follow the **Retry** instructions. If the problem was flakiness, this may get the build back on track.

While the internal build is continuing, investigate the issue the build hit in its first attempt to try to determine if more fixes are needed.

If the problem is not flakiness, make a fix using a PR to the release branch.

    

After submitting the PR, follow the **Retry** instructions below, but replace all fields with the string `nil` then put your PR number into the first field, "microsoft/go PR number to poll for merge". The retry build will then wait for your PR to merge and run an official build using the merged commit. Get the team to review your PR and merge it.

    



---

If the build failed for a non-polling or other unusual reason, the info above may be irrelevant to the problem that occurred. Examine the build logs for more information.

For an overview of the release process, see [the release process docs](https://github.com/microsoft/go-infra/tree/main/docs/release-process).

---

# Retry Instructions


1 -  Copy this value:

**2004985**


2 -  Press **Run new**


3 -  Paste the number into field **3**






4 -  Press **Run**
