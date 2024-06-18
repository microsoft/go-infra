---
post_title: {{.Title}}
author1: {{.Author}}
post_slug: {{.Slug}}
categories: {{.CategoriesString}}
tags: {{.TagsString}}
featured_image:
summary: The Microsoft builds of the Go security patches released on {{.ReleaseDate}} are now available for download.
---

The Microsoft builds of the Go security patches released today, {{.ReleaseDate}}, are now [available for download](https://github.com/microsoft/go#binary-distribution). The new versions are:

| Microsoft Release | Upstream Tag |
|-------------------|--------------|
{{- range .Versions }}
| [{{.MSGoVersion}}]({{.MSGoVersionLink}}) | {{.GoVersion}} [release notes]({{.GoVersionLink}}) |
{{- end }}
