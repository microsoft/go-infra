# importdiff

`importdiff` compares two Git revisions and lists changed Go files that directly import a selected package or one of its subpackages on either side of the change. It includes tests and files for every build configuration.

From the `go-infra` repository:

```sh
go run ./cmd/importdiff -repo /path/to/golang/go -exclude src/crypto/internal/fips140 OLD_SHA NEW_SHA
```

By default the command searches `src` for `crypto/internal/fips140` importers. Repeat `-src` to select multiple source directories and `-import` to select multiple package trees. Each flag replaces its own default on first use:

```sh
go run ./cmd/importdiff -repo /path/to/go -src src/cmd -src src/crypto -import crypto/internal/fips140 -import crypto/internal/boring OLD_SHA NEW_SHA
```

The JSON array contains paths, old/new Git blob IDs, and matched imports on both sides. `[]` means no matches; errors exit nonzero. The [`importdiff` package](../../importdiff) provides the same comparison to Go callers.
