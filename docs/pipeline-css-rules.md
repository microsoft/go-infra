# Azure Pipeline CSS rules for readability

Some of the Azure Pipelines in use for Microsoft build of Go have a very large number of steps, jobs, and stages.
The Azure Pipelines UI uses a large amount of whitespace and a fixed-size left column, which can make it difficult to understand.

Here are some CSS rules that can be applied by a browser extension like [Stylus](https://en.wikipedia.org/wiki/Stylus_(browser_extension)) to make the build results easier to read without hovering over and clicking various items to see full names.

> [!NOTE]
> These rules may break the Azure Pipelines UI at any time.
> Try disabling these first if something seems wrong.

```css
/*
Make the job log sidebar wide enough to avoid cutting off detailed job names,
build step names, and stage names.
*/
.bolt-master-panel {
    width: 440px;
}

/*
Trim a significant amount of padding around each build step in job log sidebar.
This shows many more steps at once by saving vertical space.
*/
.run-logs-tree .run-tree-cell {
    height: 8px;
}

.bolt-tree-cell .bolt-table-cell-content {
    padding-top: 0px;
    padding-bottom: 0px;
}

/*
Trim whitespace from Stage cells (labels).
*/
.bolt-table-header-cell-content {
    margin: 1px 0;
    padding: .1rem .1rem;
}

/*
Trim whitespace from the build/run name at the top-left of the job log sidebar.
*/
.bolt-master-panel-header {
    padding-top: 2px;
    padding-bottom: 8px;
}
```