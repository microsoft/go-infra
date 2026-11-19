# importdiff

`importdiff` compares two Git revisions and lists changed Go files that directly import a selected package or one of its subpackages on either side of the change. It includes tests and files for every build configuration.

From the `go-infra` repository:

```sh
go run ./cmd/importdiff -repo /path/to/golang/go -exclude src/crypto/internal/fips140 OLD_SHA NEW_SHA
```

By default the command searches `src` for `crypto/internal/fips140` importers and excludes nothing. Repeat `-src` to select source directories, `-import` to select package trees, and `-exclude` to skip directories and their descendants. Paths are relative to the upstream repository root. Each flag replaces its own default on first use:

```sh
go run ./cmd/importdiff -repo /path/to/go -src src/cmd -src src/crypto \
  -import crypto/internal/fips140 -import crypto/internal/boring \
  -exclude src/crypto/internal/fips140 -exclude src/crypto/internal/boring OLD_SHA NEW_SHA
```

The JSON array contains paths, old/new Git blob IDs, and matched imports on both sides. `[]` means no matches; errors exit nonzero. To check a submodule update in a GitHub pull request, use [`upstreamreview`](../upstreamreview/).
