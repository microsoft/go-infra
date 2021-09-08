# Microsoft Go Infrastructure

The `github.com/microsoft/go-infra` module is a library used by Microsoft to
build Go from source. The repository also contains other utilities that help
Microsoft to build and use Go.

## Branches

This repository has only one maintained branch, `main`, rather than maintaining
one branch per release.

Using a single branch makes this repository easy to maintain. It also means we
can use the infra module to share code between the Go release branches without
as many cherry-picks, only module dependency updates.

The cost of a single branch is that any change to the branch needs to be
compatible with *all* release branches. This is why some infra must still be
maintained in the release branches themselves: differences between the branches
create different infra requirements.

## Contributing

This project welcomes contributions and suggestions. Most contributions require you to agree to a
Contributor License Agreement (CLA) declaring that you have the right to, and actually do, grant us
the rights to use your contribution. For details, visit https://cla.opensource.microsoft.com.

When you submit a pull request, a CLA bot will automatically determine whether you need to provide
a CLA and decorate the PR appropriately (e.g., status check, comment). Simply follow the instructions
provided by the bot. You will only need to do this once across all repos using our CLA.

This project has adopted the [Microsoft Open Source Code of Conduct](https://opensource.microsoft.com/codeofconduct/).
For more information see the [Code of Conduct FAQ](https://opensource.microsoft.com/codeofconduct/faq/) or
contact [opencode@microsoft.com](mailto:opencode@microsoft.com) with any additional questions or comments.

## Trademarks

This project may contain trademarks or logos for projects, products, or services. Authorized use of Microsoft
trademarks or logos is subject to and must follow
[Microsoft's Trademark & Brand Guidelines](https://www.microsoft.com/en-us/legal/intellectualproperty/trademarks/usage/general).
Use of Microsoft trademarks or logos in modified versions of this project must not cause confusion or imply Microsoft sponsorship.
Any use of third-party trademarks or logos are subject to those third-party's policies.
