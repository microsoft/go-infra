# pipelineymlgen

The `pipelineymlgen` command helps generate Azure Pipelines YML by adding `${ ... }` commands.
This tool aims to provide a few benefits over built-in templates:

* More flexible.
  * These templates work in more places, without hard-to-spot limitations.
* More maintainable.
  * Easy to reuse values and logic without caveats.
  * Templates evaluated before your eyes, with debugging possible.
  * A reviewer can check the output YML.
* More readable.
  * Easier to avoid unintuitive nesting and inversion-of-control techniques commonly found in advanced pipeline yml.
  * Keep the "expressions embedded in YML" style that Azure Pipelines uses, but expand upon it in an intuitive way.
  * Use some Go text/template syntax to support advanced logic.

However, `pipelineymlgen` isn't a replacement for built-in Azure Pipelines templates.
This generator runs before the pipeline run's parameters and variables are known, so it can't execute conditional logic based on these, while Azure Pipelines can (in some cases).
A mixture of `pipelineymlgen` and built-in template logic is expected be the best solution.

For simplicity, this tool always finds the Git repository the tool is invoked from and uses it when evaluating some paths.
Like Azure Pipelines, `pipelineymlgen` uses repository-relative paths in some cases.
A repository-local path begins with `/`.
Other paths are local to the template currently being evaluated.

## Set up `pipelineymlgen`

1. `go get -tool github.com/microsoft/go-infra/cmd/pipelineymlgen@latest`
1. Add `//go:generate go run github.com/microsoft/go-infra/cmd/pipelineymlgen` to a `.go` file in your repository.
1. Add a test that runs `go run github.com/microsoft/go-infra/cmd/pipelineymlgen -check` in CI to ensure the generated YML is up to date.

Then, run `go generate ./...` to search for `*.gen.yml` files and generate the corresponding `*.yml` files.

## Function syntax

When `pipelineymlgen` encounters `${ `, everything up to the next ` }` is treated as a function.
A `${ ` ` }` pair will be found within any key or value.

The spaces in `${ ` and ` }` are required to easily distinguish in searches from `${{`.

Functions are handled by generating a [`text/template`](https://pkg.go.dev/text/template) template that wraps the content of `${ ... }` with `{{ ... }}` and evaluates it.
This means text/template actions and functions are supported.
However, the built-in output will be always be treated as a YML string, not YML elements.
`pipelineymlgen` adds more commands that must be used to interact with the yml structure.

> [!NOTE]
> Due to this implementation, `text/template` variables that are defined in one `${ ... }` block don't exist in future or nested `${ ... }` blocks.
> Use context data instead if necessary.

All of the `pipelineymlgen`-specific yml interaction functions only work properly when they provide the final returned value.
They could be considered *actions*, but `text/template` doesn't allow adding custom actions.
The `pipelineymlgen` tool attempts to detect improper use and report an error.

### `pipelineymlgen` configuration

The object inside this key contains a configuration object to use for this document.
If this command is used, it must be used before any other commands in the document.

This example demonstrates all available options:

```yml
${ pipelineymlgen }:
  data:
    greeting: "Hi" # Can be referred to as `${ .greeting }` in the document.
    # Some data is provided automatically:
    # source: The path of the template file being processed, relative to the repository root.
    # output: The path of the output file being generated, relative to the repository root.
  pre: |
    # NOT IMPLEMENTED.
    # This would be prepended to every text/template string that is generated
    # while processing this document, to define common templates, data, and more
    # using ordinary text/template syntax without involving another file.
    {{$hi := 5}}
  output: # If not specified, the input file path with `.gen` removed is used.
    - file: rolling-internal-validation-pipeline-unofficial.yml
      data:
        official: false
    - file: rolling-internal-validation-pipeline.yml
      data:
        official: true
```

> [!TIP]
> `inlinetemplate` (and other functions) may be used inside a `pipelineymlgen` object.
> Use this to share data.

### `inlineif <pipeline>`

Structural `if`.
If the condition is true, substitute the child object/array/value in place of the `inlineif` node.

> [!NOTE]
> The "array-ness" of the child must match the "array-ness" of the `inlineif` node.
> This prevents syntax conflicts with surrounding yml nodes.
> This rule applies to all inlining commands.

No specific next sibling or `end` command is required.
This command is aware of yml syntax.

### `elseinlineif <pipeline>`

Structural `else if`.
Must be an immediate next sibling of an `inlineif` node or another `elseinlineif` node.

### `elseinline`

Structural `else`.
Must be an immediate next sibling of an `inlineif` node or `elseinlineif` node.

### `inlinewith <pipeline>`

Inline the child object/array/value.
Any commands evaluated in the child will see the result of `pipeline` as dot (`.`)

### `inlinerange <pipeline>`

A yml-inlining version of `range`:

> The value of the pipeline must be an array, slice, map, iter.Seq,
> iter.Seq2, integer or channel.
>
> If the value of the pipeline has length zero, nothing is output;
> otherwise, dot is set to the successive elements of the array,
> slice, or map and T1 is executed. If the value is a map and the
> keys are of basic type with a defined order, the elements will be
> visited in sorted key order.

Dot (`.`) is set to each element of the list in turn and the child element value is evaluated.
Note that this means there is no way to access the existing value of dot while evaluating child elements.

### `inlinerange "<valuename>" <pipeline>`

`inlinerange <pipeline>`, but merges dot with `map[string]int{valuename: <value>}` for each iteration.

### `inlinerange "<keyname>" "<valuename>" <pipeline>`

`inlinerange <pipeline>`, but merges dot with `map[string]int{keyname: <key>, valuename: <value>}` for each iteration.

### `inlinetemplate <path> [pipeline]`

Evaluate the `.gen.yml` template at `path` and inline the result.
If `pipeline` is specified, passes its result to the template as dot instead of the current dot.

### Sprig functions

Most Sprig functions functions are included.
Specifically, `pipelineymlgen` includes `HermeticTxtFuncMap`.
Functions that seems particularly important for pipeline yml generation and using `pipelineymlgen` features are:

* `dict` - Generate a map to pass to `inlinetemplate` for parameters.
* `until x` - Generate a sequence from 0 until x. Useful for repeating steps, like retries.

See [Sprig documentation](https://masterminds.github.io/sprig/) for more details.
