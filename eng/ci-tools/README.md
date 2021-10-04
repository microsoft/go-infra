# ci-tools

This module exists to store a dependency on `gotest.tools/gotestsum` without
taking a dependency on it from the root go-infra module. Using a module
dependency lets CI download/install the tool while verifying against the
checked-in go.sum file.
