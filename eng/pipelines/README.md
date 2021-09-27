# Pipelines

This directory contains Azure DevOps (AzDO) YAML pipelines for CI and utilities.

Pipeline definitions currently using each YAML file are:

* [`go-update.yml`](go-update.yml) - Update dependencies after a Go build. Runs
  go-docker updates.
  * [`microsoft-go-infra-update-docker`](https://dev.azure.com/dnceng/internal/_build?definitionId=1040&_a=summary)
    * To manually queue an update to a specific build, use the "Resources"
      options in the "Run pipeline" dialog.
    * To see where an update came from, click the "X consumed" button:
      ![](img/consumed-artifacts.png)
