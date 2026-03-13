# Future functions

Below are some ideas for functions that seem like they would be useful for pipeline generation in the future, but aren't implemented yet.

### `inlinewith <pipeline>`

Inline the child element, but when evaluating the child, set `data` to the result of `pipeline`.

### `fromrepopath <path>`

Convert a path from a repository-root-relative path to a path relative to the template file being evaluated.
This uses `git rev-parse --show-toplevel` to find the repository root.

Azure Pipelines sometimes uses repository-root-relative paths, so this function could help keep a familiar style.

### `inlineswitch <pipeline>`

Structural `switch`.
The result of `pipeline` is compared against each `case` child node in order.
When a match is found, the child node is substituted in place of the `inlineswitch` node.

For some types of matching, this can be significantly easier to maintain and read than an `inlineif`/`elseif` series.

