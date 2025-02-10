

Based on the variables that were set by this build when it failed, here's a suggestion on how to resolve the issue:



     

### ⌚ Wait for go-images dependency flow

This step might time out if the auto-update PR from the Microsoft build of Go build didn't merge successfully. It could also time out if the version numbers are incorrect.

If you see that the update PR is in progress, simply follow the **Retry** instructions below.

If you expect this to be complete, look carefully at the build logs to find typos or other errors occurring. If necessary, **Retry** the build, but change parameters to fix them.

    



---

If the build failed for a non-polling or other unusual reason, the info above may be irrelevant to the problem that occurred. Examine the build logs for more information.

For an overview of the release process, see [the release process docs](https://github.com/microsoft/go-infra/tree/main/docs/release-process).

---

# Retry Instructions



1 -  Press **Run new**






2 -  If not already enabled, check the 'Approve right now' checkbox


3 -  Press **Run**
