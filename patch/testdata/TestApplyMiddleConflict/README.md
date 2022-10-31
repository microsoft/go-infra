This test is analogous to a typical Go patch fixup.
The `before` patches are created on v1.0.0 of moremath, and the 0002 patch conflicts with later changes.
The `after` patches are created on v1.0.2, where the hypothetical dev has fixed the 0002 conflicts.

`git-go-patch` has a feature that detects 0001 and 0003 have not changed and avoids updating those two patch files.
If the patching tool were run in `-verbatim` mode, it would make some spurious changes that the dev would have to manually filter out before submitting the fixed patch files.
