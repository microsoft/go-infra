# Azure DevOps helpers

This package uses `github.com/microsoft/azure-devops-go-api/azuredevops/v7` at v7.1.0. `ClientFlags.NewConnection` returns a v7 connection, and `GetBuildWebURL` accepts a v7 `build.Build`. Callers that use these types must use the `/v7` SDK import paths as well.

The existing `-org`, `-proj`, and `-azdopat` flags and environment defaults are unchanged. Generated SDK clients handle resource discovery, API version negotiation, and authentication. When a custom HTTP client is needed, v7 provides `azuredevops.NewClientWithOptions` and `azuredevops.WithHTTPClient`.

The release command now uses `build.Client.QueueBuild` and `build.Build.TemplateParameters` rather than constructing a version-specific HTTP request. Build variables still use the Build API's legacy JSON-encoded `Parameters` field. Optional template parameters are retried using validation details in `azuredevops.WrappedError.CustomProperties`; authentication errors and unrelated validation errors are not retried. Request payloads, which can contain sensitive variable values, are not logged.

Other existing build, Git, and pipeline calls use their v7 equivalents. `retain-build` deliberately retains its permanent, idempotent `keepForever=true` behavior: switching to expiring retention leases would change its contract. The upgrade does not add new pipeline actions or invoke live Azure writes during testing.
