---
post_title: {{.Title}}
author1: {{.Author}}
post_slug: {{.Slug}}
categories: {{.CategoriesString}}
tags: {{.TagsString}}
featured_image:
summary: The Microsoft builds of the Go security patches released today, are now available for download.
---

The Microsoft builds of the Go security patches released today, are now [available for download](https://github.com/microsoft/go#binary-distribution). For more information about this release and the changes included, see the table below:

| Microsoft Release | Upstream Tag |
|-------------------|--------------|
{{- range .Versions }}
| [{{.MSGoVersion}}]({{.MSGoVersionLink}}) | {{.GoVersion}} [release notes]({{.GoVersionLink}}) |
{{- end }}
