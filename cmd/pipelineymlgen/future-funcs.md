# Future functions

Below are some ideas for functions that seem like they would be useful for pipeline generation in the future, but aren't implemented yet.

### `inlinewith <pipeline>`

Inline the child element, but when evaluating the child, set `data` to the result of `pipeline`.

### `inlinerange <args...>`

A YAML-inlining version of `range`:

> The value of the pipeline must be an array, slice, map, iter.Seq,
> iter.Seq2, integer or channel.
>
> If the value of the pipeline has length zero, nothing is output;
> otherwise, dot is set to the successive elements of the array,
> slice, or map and T1 is executed. If the value is a map and the
> keys are of basic type with a defined order, the elements will be
> visited in sorted key order.

Passing results from Sprig `until` and `seq` for retries could be a common use case.
Iterating through platforms could be another.

Possible args are:

* `inlinerange <pipeline>`

    `data` is set to each element of the list in turn and the child element value is evaluated.
    Note that this means there is no way to access the outer value of `data` while evaluating child elements.

* `inlinerange "<valuename>" <pipeline>`

    Merges `data` with `map[string]int{valuename: <value>}` for each iteration.

* `inlinerange "<keyname>" "<valuename>" <pipeline>`

    Merges `data` with `map[string]int{keyname: <key>, valuename: <value>}` for each iteration.

### `fromrepopath <path>`

Convert a path from a repository-root-relative path to a path relative to the template file being evaluated.
This uses `git rev-parse --show-toplevel` to find the repository root.

Azure Pipelines sometimes uses repository-root-relative paths, so this function could help keep a familiar style.

### `inlineswitch <pipeline>`

Structural `switch`.
The result of `pipeline` is compared against each `case` child node in order.
When a match is found, the child node is substituted in place of the `inlineswitch` node.

For some types of matching, this can be significantly easier to maintain and read than an `inlineif`/`elseif` series.

