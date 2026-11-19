# Upstream crypto review

[`upstreamreview`](../../cmd/upstreamreview) is a GitHub App webhook for `microsoft/go`.
It checks the `go` submodule revision on the base against the revision in the PR's **test merge**.
It does not compare the tip of a possibly stale topic branch against the latest base.
It applies to all PRs, not just bot syncs, and never needs to modify their branches.

If either revision of a changed upstream Go file under `src` imports `crypto/internal/fips140` or one of its subpackages, the App links the affected files in one PR comment and requests `@microsoft/golang-compiler` on non-draft PRs.
Files inside `src/crypto/internal/fips140` are excluded. Tests and all build configurations are included. Edits through existing imports, deletions and renames are included.

The `upstream-crypto-review` commit status is published on the verified **test merge SHA**:

| Result | Status |
| --- | --- |
| No gitlink change, or no affected importers | Success |
| Affected importers without an eligible approval | Pending |
| An active team member other than the author approved the **current PR head SHA** | Success |
| Comparison or approval lookup failed | Error; retry on reconciliation |

The App rereads effective reviews and team memberships. A bot, a PR author, a stale approval or a dismissed approval doesn't count. A subsequent `CHANGES_REQUESTED` supersedes an earlier approval from the same person. A commit status, rather than a one-shot CI job, lets a dismissal move the same commit from success back to pending. Existing branch rules still apply.

## Reproduce a comparison

With both commits available in a local `golang/go` clone:

```sh
go run ./cmd/importdiff -repo /path/to/go -exclude src/crypto/internal/fips140 OLD_SHA NEW_SHA
```

The JSON output lists paths, old/new blob IDs, and matched imports on each side. `[]` means no changes; failure to read or parse sources exits nonzero. `-src`, `-import` and `-exclude` configure the rule for other import trees. There is no baseline to regenerate.

## Deploy and enable enforcement

1. Register a dedicated GitHub App and install it on the target repository. Repository permissions: **Contents: read**, **Commit statuses: write**, **Pull requests: write** (comment and request the team). Organization permission: **Members: read**. Subscribe to `pull_request`, `pull_request_review`, and `push`. Configure an HTTPS webhook pointing to `/webhook`, JSON payloads, and a strong webhook secret.
2. Build `./cmd/upstreamreview` from reviewed `go-infra` code. On the host, `git clone --mirror https://github.com/golang/go.git go.git`. The App fetches from that mirror's operator-configured `origin` when commits are missing. It never uses the PR's `.gitmodules`, checks out PR code, or runs upstream source.
3. Supply `GITHUB_APP_CLIENT_ID`, `GITHUB_APP_INSTALLATION_ID`, `GITHUB_APP_PRIVATE_KEY` (base64-encoded PEM), and `GITHUB_WEBHOOK_SECRET` from the host's secret store. Run `upstreamreview -mirror /path/to/go.git` behind a TLS reverse proxy to `127.0.0.1:8080`. Use **one active instance** per installation/repository; it serializes decisions. `-repo` and `-team` allow a staging installation.
4. Start in advisory mode: deploy without requiring the status. On a test PR verify a clean bump, a flagged push, a team approval, and a dismissal. Check that the **same status turns pending again**. Verify an unrelated PR whose base advances stays clean.
5. Add `upstream-crypto-review` to **required status checks** on the target branch ruleset and select **this App** as the expected source. Don't use an unbound context name, a label, or the sync bot's approval as the enforcement signal. This service targets normal PR merges; do not enable it as a required check for a merge queue without also handling merge-group revisions.

Signed deliveries enqueue work; event payloads are not trusted as review state. A target-branch push reconciles PRs based on that branch. The App also sweeps open PRs at startup and every five minutes to recover from missed events, transient API failures or a test merge not yet available. No database is required. Monitor availability, webhook failures and reconciliation errors: status changes after a review dismissal are asynchronous, and GitHub cannot update an already-published status on the App's behalf during an outage.
