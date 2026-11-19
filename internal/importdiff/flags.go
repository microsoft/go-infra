// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package importdiff

import (
	"flag"
	"strings"
)

// BindFlags registers the rule flags. Explicit values replace that flag's defaults.
func (r *Rule) BindFlags(fs *flag.FlagSet) {
	bind := func(name, usage string, dest *[]string) {
		var values []string
		fs.Func(name, usage+" (repeatable)", func(value string) error {
			values = append(values, value)
			*dest = values
			return nil
		})
		fs.Lookup(name).DefValue = strings.Join(*dest, ", ")
	}
	bind("src", "source directory to search", &r.SourceRoots)
	bind("import", "import path to match, including subpackages", &r.ImportRoots)
	bind("exclude", "source directory to exclude", &r.ExcludeDirs)
}
