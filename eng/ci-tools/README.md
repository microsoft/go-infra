# ci-tools

This module exists to store tool dependencies without taking a dependency from the root go-infra module.
Using a module dependency lets CI download/install the tool while verifying against the checked-in go.sum file.
