# ci-tools

This module exists to store tool dependencies that can't be depended on by the root go-infra module.

The root go-infra module should be usable by the N-1 version of Go, so that it can be used to build both supported versions of the Microsoft build of Go without requiring the builder to already have a newer Go version.
However, we may want to use some tools that require a newer version of Go.
This module allows such dependencies.

It is typically more dev-friendly to depend on modules from the root go-infra go.mod file.
So, when possible, dependencies should be in the root module, and dependencies that exist here should be migrated to the root module.

As of writing, this ci-tools module has no dependencies on any modules, because none require a newer version of Go.

> [!NOTE]
> Using a module dependency lets CI download/install the tool while verifying against the checked-in go.sum file.
> Commands like `go run github.com/foo/example` automatically use the version of the tool matching the go.mod and go.sum file, and fail safe when there is no match.
>
> It's tempting to use `go run ...@latest` to get a tool in CI extremely simply, but this is not a good idea.
> `@latest` may lead to security implications, and more generally, `@latest` means the dependency is not tracked under source control.
