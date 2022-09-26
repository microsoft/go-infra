

Based on the variables that were set by this build when it failed, here's a suggestion on how to resolve the issue:



### ⌚ Wait for internal mirror

Once the sync-from-upstream PR merges, mirroring the merge commit from GitHub to the AzDO internal mirror should only take a few minutes. If it times out, there is likely an outage in some deeper part of the infrastructure.

Look for a mirroring outage already reported in the [First Responder Teams channel](https://teams.microsoft.com/l/channel/19%3aafba3d1545dd45d7b79f34c1821f6055%40thread.skype/First%2520Responders?groupId=4d73664c-9f2f-450d-82a5-c2f02756606d&tenantId=72f988bf-86f1-41af-91ab-2d7cd011db47). If you don't see one reported within the last few hours, post about the mirroring issue there to alert the infra team and get help. Once the issue is resolved, **Retry** to resume polling.



---

If the build failed for a non-polling or other unusual reason, the info above may be irrelevant to the problem that occurred. Examine the build logs for more information.

For an overview of the release process, see [the release process docs](https://github.com/microsoft/go-infra/tree/main/docs/release-process).

---

# Retry Instructions


1 -  Copy this value:

**2004985**


2 -  Press **Run new**


3 -  Paste the number into field **2**






4 -  Press **Run**
