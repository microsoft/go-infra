# Azure pipeline YAML style

Our pipeline implementation is shaped by a few AzDO pipeline and YAML quirks. This doc contains notes on these quirks and how they influence how we've written the pipeline YAML.

These notes apply to this repository, microsoft/go-infra, and also the [microsoft/go](https://github.com/microsoft/go), [microsoft/go-images](https://github.com/microsoft/go-images), and [microsoft/go-infra-images](https://github.com/microsoft/go-infra-images) pipelines.

Useful AzDO Pipeline YAML docs:
* [YAML schema reference for Azure Pipelines](https://docs.microsoft.com/en-us/azure/devops/pipelines/yaml-schema)
  * [Shortcut steps: `pwsh`, `script`, `publish`, etc.](https://docs.microsoft.com/en-us/azure/devops/pipelines/yaml-schema/steps?view=azure-pipelines)
* [Template types & usage](https://docs.microsoft.com/en-us/azure/devops/pipelines/process/templates?view=azure-devops)
* [Build and release tasks](https://docs.microsoft.com/en-us/azure/devops/pipelines/tasks/?view=azure-devops)

General YAML resources:
* [Advanced multi-line value handling (`>`, `|`, ...)](https://yaml-multiline.info/)

## Faster retry: one stage per job

A typical way to arrange an AzDO pipeline is to add a job for each platform into a single stage:

> ![](images/many-jobs-one-stage.png)

The "Rerun failed jobs" button is useful to rerun flaky jobs while leaving the successful status intact on the other jobs.

However, this button only works when all jobs in the stage have completed (succeeded or failed). This is a problem: if a job fails quickly and other jobs are still running, you have to wait for all jobs to finish before you can retry the failed job. This wastes time in a few ways:

* You need to keep an eye on the pipeline to trigger the retry as soon as the other jobs are complete.
* Assuming the jobs take roughly the same time to complete, the total time to finish CI is now at least double the usual time because the retry doesn't run in parallel with the other jobs.

Instead of waiting, you could cancel the whole build to make all the jobs fail quickly and then retry all jobs. This wastes the time already spent making progress on the in-progress jobs, which is particularly bad if the canceled jobs are even longer than the one that failed.

The workaround we use in microsoft/go CI to avoid wasting time is to use one stage per job, so any stage/job can be retried as soon as it fails:

> ![](images/one-stage-per-job.png)

> Related feedback item: [Allow failed Jobs to be retried as soon as they have finished running - Visual Studio Feedback](https://developercommunity.visualstudio.com/t/Allow-failed-Jobs-to-be-retried-as-soon/10130213)

> Also requested internally (2018): https://dev.azure.com/dnceng/internal/_queries/edit/110

## Dynamic vs. typed template parameter declarations

Some of our templates use dynamic template parameters. These look like:

```yml
parameters:
  apple: 2
  lemon: "acidic"
  orange: true
```

In this mode, additional parameters can be passed when using a `- template: ...` statement. All parameters other than objects and lists seem to be typecast to `string` when passed in. This means `${{ if parameters.orange }}` will pass if `orange` is passed in as `false`, because the implicitly converted `"False"` is truthy.

A workaround is to instead use `${{ if eq(parameters.orange, true) }}` to (it seems) perform the implicit cast on both sides.

In general, typed parameters are preferable:

```yml
parameters:
  - name: orange
    type: boolean
    default: true
```

Because `orange` is passed as a `boolean`, `${{ if parameters.orange }}` evaluates as expected.
This is more predictable and easier to understand.

In typed parameter mode, any parameters passed by a `- template: ...` statement that aren't specified in the template's `parameters` block are rejected as a template evaluation error.
If we need to pass a template some arbitrary set of parameters without a fixed schema, we use an extra `object` param, such as:

```yml
parameters:
  - name: ctx
    type: object
```

## Runtime parameters

[Runtime parameters](https://docs.microsoft.com/en-us/azure/devops/pipelines/process/runtime-parameters) are a way to specify parameters for a pipeline run that show up at the top level of the "Run" dialogue. These are nicer to use for commonly adjusted user inputs than variables: you don't need to dig deep in the UI to change them, and they have friendly labels defined in the pipeline YML file. The parameters are also usable in AzDO templates without the limitations of variables.

We also use runtime parameters to transfer release data between pipelines in the automated release process.

In AzDO YML, a runtime parameter either has a default value, or a value must be assigned by the user before the "Run" button is enabled.

### 'nil' string default

A problem is that AzDO doesn't treat the empty string `''` as a valid default value: AzDO requires the user to specify some non-empty string before hitting "Run". We sometimes need to define an optional string parameter without any meaningful default value. If `''` worked, it would be the easy choice for this scenario, but it doesn't work.

As a workaround, when we need an optional string parameter, we use the string `nil` as the default value and `ne(parameter.example, 'nil')` to check if the parameter was set. For example:

```yml
parameters:
  - name: example
    displayName: 'Description of an example pseudo-optional string parameter'
    type: string
    default: nil
```

### Ensuring attentive booleans

AzDO runtime boolean parameters are easy to use and work well for many scenarios.
However, there's a risk that a user might not pay close attention to it and accept the default value either by mistake in a rush or not even realizing that it requires attention in the first place.

Ideally, we would add a boolean parameter that accepts `true` or `false` as a radio selection and indicate to AzDO that it shouldn't select a default.
However, it doesn't appear that this is possible as of writing.
If we use `values:`, for example, AzDO always picks a default even if we don't specify one with the `default:` field.

So, when this risk is a concern, we use a string parameter with three values:

```yml
  - name: isSecurityRelease
    displayName: >
      This release includes security fixes:
    type: string
    default: Cancel
    values:
      - True
      - False
      - Cancel
```

To help a user that left it at `Cancel` realize their mistake as soon as possible, we add a condition to the template that introduces a template error:

```yml
    stages:
      - stage: Release
        jobs:
          # Validation for complex inputs.
          - ${{ if not(in(parameters.isSecurityRelease, 'True', 'False')) }}:
            - 'Cancelled run. Please pick an option to indicate whether or not this is a security release.': error
```

This way, the user can fix the issue quickly without even waiting for the pipeline to start.
The error message that is shown to the user is not ideal, but it includes the message and is clear enough for us.

## Templates for data reuse

AzDO supports [variable templates](https://docs.microsoft.com/en-us/azure/devops/pipelines/process/templates?view=azure-devops#variable-templates-with-parameter), but it's hard to determine where variables defined this way are usable.

For example, when defining a job's pool name:

* A macro expansion `$(example)` will evaluate to define a job's pool name, but won't evaluate to define the demands on that pool.
* A runtime expression `$[example]` will not evaluate in either case.
* A template expression `${{ example }}` will always work.

As well as only being evaluted in certain contexts, each of these types of expression strings also each have their own rules on what expressions they can evaluate and what data they have access to.

Template expressions have the broadest applicability, so we prefer templates and `parameters` (rather than `matrix` and `variables`) to share logic and values between stages.

Another major benefit of templates and template expressions is early evaluation: sometimes AzDO can catch errors when we hit the "Run" button in the "queue build" dialog (rather than at build execution time), saving significantly on dev iteration time.

We also use the [`pipelineymlgen`](/cmd/pipelineymlgen/README.md) tool, which adds another layer of templating evaluated at dev-time:

* A `pipelineymlgen` expression `${ example }` will always work. It has access to even less data than a template expression, but executes locally, reducing iteration time even further and improving observability and debuggability.

Despite the benefits, we don't always use `pipelineymlgen` for templating:

* It's a custom tool that new contributors won't be familiar with.
* It may make it more confusing to compare our yml against 1ES PT documentation.
  * Mitigation: the evaluated files are checked into the repo, and these can be compared against 1ES PT docs.
* When possible, we strongly prefer to move complex logic from AzDO yml into Go code rather than improve the way the templates work.

### Compile-time variable usage

In a given YAML file, you can use `${{ variables['example'] }}` to access a variable defined in that file, or in a variable template used by the file.
You can even use a previously defined variable to determine the value of a new variable within the sames `variables` block.
This can be useful to calculate some values that would otherwise require deep template nesting.

However, these variables don't generally pass through to jobs/stages template files.
The behavior makes it appear that jobs and stages templates have their own `variables` scope that shadows the caller's `variables`.

We tried to pass in the pipeline `variables` to job/stage `variables` using `parameters` and re-expansion.
This partially works.
However, because it requires piping variables through as `parameters` and a decent amount of duplicated logic to re-insert as `variables`, so we might as well just use the `parameters`.
We can still use `variables` to perform chained reuse, but values that come from the parent might need to be accessed from `parameters` rather than `variables`.

Templates are (generally) evaluated in text order.
This determines whether `variables` is available as `${{ variables[...] }}` or not.

The [`pipelineymlgen`](/cmd/pipelineymlgen/README.md) tool has more traditional capabilities for variable reuse and might be a good alternative when it seems that these details about `variables` matter.

## Pipeline templates to reduce duplication with official/unofficial split

In some cases, we can use these blocks to maintain both the official and unofficial pipeline in the same yml file:

```yml
extends:
  ${{ if variables['Go1ESOfficial'] }}:
    template: v1/1ES.Official.PipelineTemplate.yml@1ESPipelineTemplates
  ${{ else }}:
    template: v1/1ES.Unofficial.PipelineTemplate.yml@1ESPipelineTemplates
```

```yml
extends:
  ${{ if variables['Go1ESOfficial'] }}:
    template: azure-pipelines/MicroBuild.1ES.Official.yml@MicroBuildTemplate
  ${{ else }}:
    template: azure-pipelines/MicroBuild.1ES.Unofficial.yml@MicroBuildTemplate
```

(With [`pipeline.yml`](https://github.com/microsoft/go/blob/9ab06144d6e90e1686f7916bb6acc46134f0bd72/eng/pipeline/variables/pipeline.yml) variables template.)

However, in other cases, we need to make separate pipeline entrypoint files.
For example, the CI trigger can disabled in the AzDO UI, but a scheduled trigger can't be disabled.
A scheduled trigger can only be overridden to a different schedule, and that schedule must have some triggers in it, not zero.

> [!NOTE]
> To mitigate the maintenance impact of having multiple pipeline files, we considered using the AzDO `extends:` feature to share some logic.
> However, it doesn't share enough:
>
> * ✅ The template can define `resources`.
> * ❌ The template can't define `variables`, so they must be duplicated in each entrypoint yml.
>   * Mitigation: we can use templates to define variables. But this is harder for devs: more files to keep in mind, and a decision to be made of whether making a template file is actually worthwhile.
>   * ❌ The variables defined in the entrypoint yml can't be used at compile-time in the template yml. This is a major limitation, because types of reuse that need to be done with template expressions won't work.
> * ❌ The template can't define runtime `parameters`, so these must also be duplicated.
>
> We moved on to developing and using `pipelineymlgen`.

For consistency and a reasonable migration path for existing pipelines, we use this set of files when necessary:

* `foo.gen.yml` - The [`pipelineymlgen`](/cmd/pipelineymlgen/README.md) template file. This isn't a pipeline entrypoint on its own, but it generates both of the following files.
* `foo-pipeline.yml` - The original file for the `foo` pipeline.
* `foo-pipeline-unofficial.yml` - A new file for the unofficial `foo` pipeline to point to, with modified triggers.

The `*.gen.yml` file generates both files and differentiates the content using expressions like `${ inlineif .official }`, for example [rolling-internal-validation.gen.yml](/eng/pipelines/rolling-internal-validation.gen.yml):

```yml
pipelineymlgen:
  output:
    - file: rolling-internal-validation-pipeline-unofficial.yml
      data:
        official: false
    - file: rolling-internal-validation-pipeline.yml
      data:
        official: true
---
# ...
schedules:
  - ${ inlineif .official }:
    - cron: '45 11 * * 2'
      displayName: Periodic Validation
      branches:
        include:
          - main
# ...
```

## Editing tools

The last time we checked, the [Azure Pipelines extension for VS Code](https://marketplace.visualstudio.com/items?itemName=ms-azure-devops.azure-pipelines) fails to parse our templates properly.
It didn't generally understand template expressions, doesn't understand dynamic template parameters, and the language server crashes when it sees `insert` as the first element in a list, like this:

```yml
- ${{ each value in parameters.thing }}:
  - ${{ insert }}: ${{ value }}
    answer: 42
```

We haven't tested its behavior with `pipelineymlgen` templates.

We expect to edit the YAML files with generic YAML tools.

## Indentation style

We indent when starting a new list in the YAML files in this repository. This isn't typical for pipeline YAML: in AzDO docs, a list's `-` is on the same column as the element containing the list. Both styles are commonly found in general YAML files. For example, we use:

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
