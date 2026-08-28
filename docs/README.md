# Microsoft build of Go Infrastructure

This repository is used by Microsoft to build Go.
See the root [README.md](../README.md) for the project overview, and the subdirectories here for more info about parts of the infra.

* [automation/](automation/) - Some details about the automated infrastructure used to maintain a Go toolset fork.
* [fork/](fork/) - A description of how the Microsoft build of Go fork is set up and why we made certain decisions.
* [release-process/](release-process/) - A description of how to release a Microsoft build of Go build, and details about the infra behind it.
* [pipeline-yml-style.md](pipeline-yml-style.md) - Style principles and quirk notes for our YAML pipelines here and in microsoft/go.
* [pipeline-css-rules.md](pipeline-css-rules.md) - custom CSS rules for Azure Pipelines to make our builds easier to read.
* [branches.md](branches.md) - 

Some documents would normally be stored in this directory, but are instead stored in the private `microsoft/go-lab` repository.
This allows us to keep some details internal without doc writers bearing the overhead of carefully considering which details specifically are safe to share publicly.

See these documents for the release process and related processes:

* [go-lab/docs/release/README.md (internal Microsoft link)](https://github.com/microsoft/go-lab/blob/main/docs/release/README.md): how to run a release.
* [go-lab/.../new-release-branch.md (internal Microsoft link)](https://github.com/microsoft/go-lab/blob/main/docs/release/toolset/new-release-branch.md): how to create a release branch for a brand new major version.
