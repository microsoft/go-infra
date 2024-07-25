---
post_title: {{.Title}}
author1: {{.Author}}
post_slug: {{.Slug}}
categories: {{.CategoriesString}}
tags: {{.TagsString}}
featured_image:
summary: A new set of Microsoft Go builds
{{- if .SecurityRelease }} including security fixes {{ else }} {{ end -}}
is now available for download.
---

A new set of Microsoft Go builds
{{- if .SecurityRelease }} including security fixes {{ else }} {{ end -}}
is now [available for download](https://github.com/microsoft/go#download-and-install).
For more information about this release and the changes included, see the table below:

| Microsoft Release | Upstream Tag |
|-------------------|--------------|
{{- range .Versions }}
| [{{.MSGoVersion}}]({{.MSGoVersionLink}}) | {{.GoVersion}} [release notes]({{.GoVersionLink}}) |
{{- end }}
