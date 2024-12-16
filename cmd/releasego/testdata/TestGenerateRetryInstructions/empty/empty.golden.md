

Based on the variables that were set by this build when it failed, here's a suggestion on how to resolve the issue:



     

### ⌚ Get upstream commit for release

This step might time out if the upstream Go tag is not yet available and remains unavailable for a handful of hours since you triggered the build.

If it's reasonable that the tag isn't up yet, simply follow the **Retry** instructions below.

If you expect the tag to be available, look carefully at the tag name being polled in the build and see if there's a typo in the version number. If so, retry the build, but fix the version number in the parameters. Also, cancel and re-run the release-go-images build with the corrected version number.

    



---

If the build failed for a non-polling or other unusual reason, the info above may be irrelevant to the problem that occurred. Examine the build logs for more information.

For an overview of the release process, see [the release process docs](https://github.com/microsoft/go-infra/tree/main/docs/release-process).

---

# Retry Instructions



1 -  Press **Run new**







2 -  Press **Run**
