# Azure pipeline YAML style

Our pipeline implementation is shaped by a few AzDO pipeline and YAML quirks. This doc contains notes on these quirks and how they influence how we've written the pipeline YAML.

These notes also apply to the [microsoft/go](https://github.com/microsoft/go) pipelines.

Useful AzDO Pipeline YAML docs:
* [YAML schema reference for Azure Pipelines](https://docs.microsoft.com/en-us/azure/devops/pipelines/yaml-schema)
  * [Shortcut steps: `pwsh`, `script`, `publish`, etc.](https://docs.microsoft.com/en-us/azure/devops/pipelines/yaml-schema/steps?view=azure-pipelines)
* [Template types & usage](https://docs.microsoft.com/en-us/azure/devops/pipelines/process/templates?view=azure-devops)
* [Build and release tasks](https://docs.microsoft.com/en-us/azure/devops/pipelines/tasks/?view=azure-devops)

## One stage per job

If a job fails in a running AzDO build with only one stage, you must wait for every single job to fail before you can retry the failed job. This is a problem: if a long job fails early but other long jobs are still running, we waste time waiting for the whole stage to finish.

Alternatively, you can cancel the whole build and start it again, but this wastes the time already spent making progress on the other jobs, which is particularly bad if those jobs are even longer than the one that failed.

The workaround is to use one stage per job, so any stage/job can be retried as soon as it fails.

## Dynamic vs. typed template parameter declarations

Most of our templates use dynamic template parameters. These look like:

```yml
parameters:
  apple: 2
  lemon: "acidic"
  orange: true
```

In this mode, additional parameters can be passed when using a `- template: ...` statement. All parameters other than objects and lists seem to be typecast to `string` when passed in. This means `${{ if parameters.orange }}` will pass if `orange` is passed in as `false`, because the implicitly converted `"False"` is truthy.

A workaround is to instead use `${{ if eq(parameters.orange, true) }}` to (it seems) perform the implicit cast on both sides.

Some templates instead use typed parameters:

```yml
parameters:
  - name: orange
    type: boolean
    default: true
```

In this mode, any additional parameters passed by a `- template: ...` statement are rejected as a template evaluation error. If we need some arbitrary parameters, we use an extra `object` param.

Because `orange` is passed as a `boolean`, `${{ if parameters.orange }}` evaluates as expected.

## Templates for data reuse

AzDO supports [variable templates](https://docs.microsoft.com/en-us/azure/devops/pipelines/process/templates?view=azure-devops#variable-templates-with-parameter), but it's hard to determine where variables defined this way are usable. For example, a macro expansion `$(example)` will evaluate to define a job's pool name, but not the demands on that pool. A runtime expression `$[example]` will not evaluate in either case. A template expression `${{ example }}` will always work.

For universal applicability, we use templates to share logic and values between stages. Another benefit is early evaluation: we can sometimes catch errors in the queue dialog (rather than at build-time), saving dev time.

## Editing tools

The [Azure Pipelines extension for VS Code](https://marketplace.visualstudio.com/items?itemName=ms-azure-devops.azure-pipelines) fails to parse our templates properly. It doesn't generally understand template expressions, doesn't understand dynamic template parameters, and the language server crashes when it sees `insert` as the first element in a list, like this:

```yml
- ${{ each value in parameters.thing }}:
  - ${{ insert }}: ${{ value }}
    answer: 42
```

We edit the YAML files with generic YAML tools.

## Indentation style

The YAML files in this repository indent when starting a list, which isn't typical for pipeline YAML. This isn't specific to AzDO, but 

```yml
stages:
  - template: shorthand-builders-to-builders.yml
    ...
    shorthandBuilders:
      - ${{ if eq(parameters.innerloop, true) }}:
        - { os: linux, arch: amd64, config: buildandpack }
        - { os: linux, arch: amd64, config: devscript }
      - ${{ if eq(parameters.outerloop, true) }}:
        - { os: linux, arch: amd64, config: longtest }
```

vs.

```yml
stages:
- template: shorthand-builders-to-builders.yml
  ...
  shorthandBuilders:
  - ${{ if eq(parameters.innerloop, true) }}:
    - { os: linux, arch: amd64, config: buildandpack }
    - { os: linux, arch: amd64, config: devscript }
  - ${{ if eq(parameters.outerloop, true) }}:
    - { os: linux, arch: amd64, config: longtest }
```

This style makes indent guides work in tools like VS Code. The line below `stages` runs to the left of each stage element. The line below `shorthandBuilders` shows a line to the left of every condition used to include builders. Without the indent before `-`, there is no indent guide for these lists.
