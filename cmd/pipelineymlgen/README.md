# pipelineymlgen

The `pipelineymlgen` command helps generate Azure Pipelines YAML by adding `${ ... }` commands.
This tool aims to provide a few benefits over built-in templates:

* More flexible.
  * These templates work anywhere in the YAML tree.
  * No dependence on specific types of Azure Pipelines template construct. No unclear limitations on which elements can be included in which constructs.
* More maintainable.
  * Easy to reuse values and logic without caveats.
  * Templates evaluated in front of your eyes, with debugging possible and comments preserved (when feasible).
  * Reviewers can check the output YAML.
* More readable.
  * Easier to avoid unintuitive nesting and inversion-of-control techniques commonly found in advanced pipeline yml.
  * Keep the "expressions and procedural code embedded in YAML" style that Azure Pipelines uses, and expand to a natural conclusion.
  * Use Go's text/template engine to support advanced logic. Its syntax is already familiar to many Go devs.

However, `pipelineymlgen` isn't a replacement for Azure Pipelines templates.
`pipelineymlgen` only generates files, so it has no access to a pipeline run's parameters and variables.
Executing conditional logic based on these is still something only Azure Pipelines template expressions can do.
A mixture of `pipelineymlgen` and built-in template logic is expected be the best solution.

## Set up `pipelineymlgen`

1. `go get -tool github.com/microsoft/go-infra/cmd/pipelineymlgen@latest`
1. Add `//go:generate go run github.com/microsoft/go-infra/cmd/pipelineymlgen .` to a `.go` file in your repository.
    * Optional: use `-r` to recursively find `.gen.yml` in all subdirectories (except `.git`).
1. Add a test that runs `go run github.com/microsoft/go-infra/cmd/pipelineymlgen -check .` in CI to ensure the generated YAML is up to date.

Then, run `go generate ./...` to search for `*.gen.yml` files and generate the corresponding `*.yml` files.

For an example, see [the `go generate` for go-infra](gen.go) and [the test](gen_test.go).

## Generation syntax

When `pipelineymlgen` encounters `${ `, everything up to the next ` }` is treated as an expression.
A `${ ` ` }` pair will be found within any key or value.

The spaces in `${ ` and ` }` are required.
This helps easily distinguish them from `${{`.

Expressions are handled by generating a [`text/template`](https://pkg.go.dev/text/template) template that wraps the content of `${ ... }` with `{{ ... }}` and evaluates it.
This means text/template actions and functions are supported.
However, the text/template-based output is treated as a YAML string, not YAML elements or other YAML types.
`pipelineymlgen` adds more functions described in the next sections that are used to interact with the YAML structure.

`pipelineymlgen` keeps track of `data`, a `map[string]any` that is passed through evaluation.
When evaluting a `text/template` template, `data` is passed as dot, and its keys can be read as `.keyname`.

The value of dot when finishing evaluating an expression is discarded.
To change `data`, use the `pipelineymlgen` functions.

When entering a document, `data` includes:

* `.filename`: The filename (with extension) of the template file being processed.
* `.output`: The path of the output file being generated, relative to the template file being processed.

See more specific documentation below for more information.

> [!NOTE]
> Due to this implementation, `text/template` variables (`$x := 5`) that are defined in one `${ ... }` block don't exist in future or nested `${ ... }` blocks.
> Use `data` instead.

All of the `pipelineymlgen`-specific YAML interaction functions only work when they encompass an entire YAML node and return the final result.
These functions could be considered *actions*, but `text/template` doesn't allow adding custom actions.
The `pipelineymlgen` tool attempts to detect improper use and report an error.

See [the testdata files](/internal/pipelineymlgen/testdata/) for more demonstrations of this syntax.
Add more tests and run the tests with `-update` flag to try out the behavior and find bugs.

### `inlineif <pipeline>`

Structural `if`.
If the condition is true, substitutes the child object/array/value in place of the `inlineif` node.
If not, ignores the node and its children.

The value is only evaluated if the condition is true.
(Shortcircuiting.)

> [!IMPORTANT]
> The "array-ness" of the child must match the "array-ness" of the `inlineif` node.
> This prevents syntax conflicts with surrounding yml nodes.
> This rule applies to all inlining commands.

No specific next sibling or `end` command is required.
This command is aware of yml syntax.

> [!NOTE]
> The `inlineelseif` and `elseinline` commands associated with this `inlineif` may be anywhere after the `inlineif` node (but not after another `inline*` function) according to an in-order traversal of the YAML node tree.
> This is done to simplify the implementation: use traversal order rather than deep structural analysis.

### `inlineelseif <pipeline>`

Structural `else if`.
Must be an immediate next sibling of an `inlineif` node or another `inlineelseif` node.

The condition is only evaluated if the previous conditions were all unsatisfied.
The value is only evaluated if the condition is true.
(Shortcircuiting.)

> [!NOTE]
> The pipeline is always evaluated, even if the `inlineif` condition is true.

### `elseinline`

Structural `else`.
Must be an immediate next sibling of an `inlineif` node or `inlineelseif` node.

The value is only evaluated if the condition is true.
(Shortcircuiting.)

### `yml <pipeline>`

Insert the result of `pipeline` as YAML-encoded data.

### `inlinetemplate <path>`

Inline the `.yml` file at `path`.
If the file is `gen.yml`, evaluate it and inline the result.

If this node has an object child, it is used as the initial value of `data` before evaluating the template.

More technically: if the node is a the key node of a mapping pair, the value node is decoded into `map[string]any` and merged into `data` before evaluating the template.

### Sprig functions

Most Sprig functions functions are included.
Specifically, `HermeticTxtFuncMap`.

See [Sprig documentation](https://masterminds.github.io/sprig/) for more details.

## `pipelineymlgen` configuration document

A `gen.yml` file may contain multiple YAML documents.
If the first document matches this structure, it instructs `pipelineymlgen` to assign to `data` then evaluate the next document.
The result of evaluating the second document is used as the final result of the `.gen.yml` file.

```yml
pipelineymlgen:
  data:
    greeting: "Hi" # Can be referred to as `${ .greeting }` in the document.
```

If the `pipelineymlgen` command is evaluating the file (not `inlinetemplate`), `output` is also considered.
Instead of discarding the result, it's written to the specified file:

```yml
pipelineymlgen:
  # Write the output of the evaluation to a file with the same name as the
  # source file, but with the .yml extension instead of .gen.yml.
  output: self
```

Multiple files may be specified, and each may have its own `data` overrides that are applied after the base `data` override (if present).

```yml
pipelineymlgen:
  data:
    official: unknown
  output:
    - file: rolling-internal-validation-pipeline-unofficial.yml
      data:
        official: false
    - file: rolling-internal-validation-pipeline.yml
      data:
        official: true
```

> [!TIP]
> `inlinetemplate` (and other functions) may be used inside a `pipelineymlgen` document.
> You can use this to share data.

> [!NOTE]
> Multiple-file output is designed to reduce the number of files necessary, making it easier to manage tightly related pipelines.
