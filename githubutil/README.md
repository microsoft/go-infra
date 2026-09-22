# GitHub helpers

This package uses `github.com/google/go-github/v92` v92.0.0, which requires Go 1.26. The helper functions return v92 SDK types; consumers must use the `/v92/github` import path too.

`NewClient` and `GitHubAuthFlags` retain the existing PAT and GitHub App interfaces. PATs and app JWTs now use the SDK's `WithAuthToken` option. Installation authentication still uses an OAuth2 token source to cache and refresh short-lived tokens. Refresh requests use the supplied context and its HTTP client, including any custom transport. Credentials are not added to logs.

The SDK constructor now takes functional options and returns an error. Client configuration is immutable after construction: use `Client.Clone` with options such as `WithUserAgent` or `WithURLs` instead of assigning SDK fields. The existing helper honors HTTP clients supplied through `oauth2.HTTPClient` in the context.

The migration uses the SDK's distinct create/update request types for Git refs, issues, comments, labels, pull requests, and releases. Required strings and value-based arguments are supplied explicitly. Partial updates preserve omitted fields: publishing a release changes only `draft`, and updating a report changes only the issue body. Release uploads, cleanup on failure, pagination, and repository/file error sentinels retain their previous behavior.

`gitpr.PostGitHub` now uses `PullRequests.Create`, retaining its existing request/response types and `ErrPRAlreadyExists` contract. Its authentication adapter does not attach credentials to redirected requests for another host or an insecure scheme. The GraphQL operations remain separate because go-github is a REST client.

The SDK still defaults to REST API version `2022-11-28`; this dependency upgrade does not opt callers into a newer REST API version or change permissions. Tests use local HTTP servers for authentication/token refresh, pagination, errors, Git objects, issue/PR requests, and release creation/upload/publication/cleanup. No live GitHub mutations are needed to validate the upgrade.
