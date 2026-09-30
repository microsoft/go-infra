# Microsoft build of Go major versions

The `msmajorversions` package describes the previous, current, and next major releases of the Microsoft build of Go.
It also constructs the expected artifact information for each release.

In this package, "major release" follows Go terminology and refers to a release such as Go 1.28.
The [`CURRENT_MAJOR`](./CURRENT_MAJOR) file contains the Go major release number, such as `28`.
The package derives the adjacent releases from that value:

- `Previous` is the stable release immediately before `Current`.
- `Current` is the latest stable release that the release process has promoted.
- `Next` is the release planned immediately after `Current`.

`Next` is a utility that allows us to plan out what shape the next release will have and lets release tooling understand when it needs to alter its behavior because it's producing `Next`.
It doesn't indicate that the release is available or supported.

See [the branch data JSON golden test file](./testdata/TestBranches/branches.golden.json) to preview the content `msmajorversions` currently produces.

## Release process

Release-specific policy changes should be prepared and reviewed while the target release is `Next`.
The release process includes promoting `Next` to `Current` (and `Current` to `Previous`) after the `Next` artifacts are available.

Promotion updates `CURRENT_MAJOR`, which advances all three package values.
For example, promoting Go 1.28 changes the package state from `Previous=1.26, Current=1.27, Next=1.28` to `Previous=1.27, Current=1.28, Next=1.29`.
Release steps that need the promoted values must run from the commit containing that update or consume data generated from that commit.

## Developer notes

### When implementing a policy/support change, use concrete versions

Platform and artifact differences should use comparisons against concrete Go versions.
They should not depend on whether a version happens to be `Previous`, `Current`, or `Next`.

For example, if Windows ARM64 support begins with Go 1.28, the policy should check for Go 1.28 or later.
When Go 1.28 moves from `Next` to `Current` and later to `Previous`, the correct policy then follows it without another source change.

### Separate publication from promotion

Publishing binaries does not immediately make a release `Current`.
The release becomes `Current` only after its version-specific artifacts are available.
This keeps the package description accurate throughout the release process.

To clarify, this broad sequence of steps should be taken during a release:

1. Publish the `1.x.y` release binaries.
2. Update version-scoped `1.x` links and metadata to point to the newly published release.
3. Update `CURRENT_MAJOR` to promote the new release.
    - Along with that, update any affected documentation and metadata in this repository, `microsoft/go`, and anywhere else.

The sequencing between publication and promotion is intentional.
If the release stalls between 2 and 3, the `1.x` binaries are available through version-specific URLs, but existing users and automation that rely on `Current` and `Previous` continue to see the previous release.
The stall then only affects promotion, not publication in general.

### Be careful about taking a dependency

When taking a dependency on `msmajorversions`, be aware that the values for `Previous`, `Current`, and `Next` will change as new releases are promoted.
Do not use this package when developing a tool that easily becomes stale.

For example, `releaseui` runs locally on the release runner's machine and would generally only be built and run once during a release cycle.
The release runner may or may not rebuild it after `Next` is promoted to `Current`.
Instead of leaving it to chance by using a build-time dependency on the `msmajorversions` package, `releaseui` should (if necessary) download an artifact at runtime that was created in a controlled environment based on `msmajorversions`.
