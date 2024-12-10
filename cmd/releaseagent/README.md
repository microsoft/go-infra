# Release Agent

The release agent runs in the context of an Azure Pipeline and executes the steps of a microsoft/go release.

Subcommands:

* `releaseagent run [...]` - Run the release agent. (Not yet implemented.)
* `releaseagent write-mermaid-diagram` - Writes a mermaid diagram showing the steps and dependencies of the release process.

See [ADR-0005 Use a release agent to coordinate releases](https://github.com/microsoft/go-lab/blob/main/docs/adr/0005-use-a-release-agent-to-coordinate-releases.md) for more information.
