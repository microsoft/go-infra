# Benchmark workflow

[`benchmark.yml`](../.github/workflows/benchmark.yml) is a reusable workflow that runs Go benchmarks on a PR (or `workflow_dispatch` run), compares them to a base ref using [`benchstat`][benchstat], and posts a single aggregated report back to the PR as a comment.
It is consumed by per-backend `benchmark.yml` files in `microsoft/go-crypto-winnative` and `microsoft/go-crypto-darwin`.

[benchstat]: https://pkg.go.dev/golang.org/x/perf/cmd/benchstat

## What it does

Three jobs run in sequence:

1. **`setup`** — resolves `head-ref`, `base-ref`, and `pr-number` from the triggering event (`pull_request` payload, or `dispatch-base-ref` / `default-base-ref` for `workflow_dispatch`).
2. **`bench`** — one matrix cell per entry in `inputs.matrix`.
   For each cell: checks out HEAD and BASE, runs `go test -bench=. -count=10 -benchmem` against each, and uploads the per-cell results as a `benchstat-<label>` artifact.
3. **`conclude`** — downloads all per-cell artifacts, builds a single markdown report with [`cmd/benchcheck`](../cmd/benchcheck), posts (or updates) the PR comment, and fails the job if any cell reported a regression or test failure.

## Caller skeleton

```yaml
name: Benchmark

on:
  pull_request:
  workflow_dispatch:
    inputs:
      base-ref:
        description: 'Base ref to compare against'
        required: true
        default: 'main'

permissions:
  actions: read
  contents: read
  pull-requests: write

concurrency:
  group: ${{ github.workflow }}-${{ github.event.pull_request.number || github.ref }}
  cancel-in-progress: true

jobs:
  benchmark:
    uses: microsoft/go-infra/.github/workflows/benchmark.yml@<sha>
    permissions:
      actions: read
      contents: read
      pull-requests: write
    with:
      default-base-ref: main
      dispatch-base-ref: ${{ inputs.base-ref }}
      matrix: |
        {
          "runs-on": ["ubuntu-latest", "macos-15"],
          "go-version": ["1.25", "1.26"],
          "include": [
            { "runs-on": "macos-15", "cgo-enabled": "1" }
          ]
        }
```

Pin the workflow to a SHA.
The `benchcheck` binary is built from `microsoft/go-infra` at that same SHA (read from `job.workflow_sha`), so there is no second ref to keep in sync.

If `microsoft/go-infra` ships release tags, add a `# vX.Y.Z` comment after the SHA so Dependabot can keep the pin up to date.

## `matrix` cell fields

`inputs.matrix` is parsed with `fromJSON` and used directly as the bench job's `strategy.matrix`, so any [standard matrix shape](https://docs.github.com/actions/how-tos/write-workflows/choose-what-workflows-do/run-job-variations) works — top-level arrays for cross-product, plus optional `include` / `exclude` to add or trim cells.
Each cell entry supports:

| field | required | meaning |
|---|---|---|
| `runs-on` | yes | GitHub Actions runner label. |
| `go-version` | yes | `actions/setup-go` version spec. |
| `go-download-base-url` | no | Override `setup-go`'s `go-download-base-url`. Defaults to `https://aka.ms/golang/release/latest` (Microsoft build of Go). Pass a non-empty URL to override; e.g. `https://go.dev/dl/` for upstream Go releases. |
| `fips-mode` | no | Non-empty enables FIPS mode for the bench steps; on Windows runners this also writes the `FipsAlgorithmPolicy` registry key to the supplied value. |
| `cgo-enabled` | no | Exported as `CGO_ENABLED` to the bench steps. |
| `xcode-version` | no | macOS only; runs `sudo xcode-select -s /Applications/Xcode_<v>.app` before benchmarking. |

Each cell's display name and uploaded artifact name are derived as `<runs-on>-go<go-version>[-fips<n>][-cgo<n>][-xcode<v>]`.
The combination must be unique across the matrix.

## Other inputs

| input | default | meaning |
|---|---|---|
| `default-base-ref` | `main` | Base ref used for `workflow_dispatch` runs when `dispatch-base-ref` is empty. |
| `dispatch-base-ref` | `''` | Caller should pass its `workflow_dispatch` `base-ref` input here. Ignored on `pull_request`. |
| `go-version` | `1.25.x` | Go toolchain used to build `benchcheck` in the `conclude` job. |
