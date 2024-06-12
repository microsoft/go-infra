<p>The Microsoft builds of the Go security patches released today, {{ .Date }}, are now available for download. The new versions are:</p>
    <ul>
        {{- range .Versions }}
        <li>{{ . }}</li>
        {{- end }}
    </ul>
<p>For more information about the changes included in this release, take a look at the upstream Go announcement: <a href="{{ .Details }}">{{ .Label }}</a> are released.</p>