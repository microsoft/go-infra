// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package counter

import (
	"flag"
	"path"
	"runtime/debug"

	"github.com/microsoft/go-infra/telemetry/internal/appinsights"
	"github.com/microsoft/go-infra/telemetry/internal/telemetry"
)

// A Counter is a single named event counter.
type Counter = appinsights.Event

// Inc increments the counter with the given name.
func Inc(name string) {
	New(name).Inc()
}

// Add adds n to the counter with the given name.
func Add(name string, n int64) {
	New(name).Add(n)
}

// New returns a counter with the given name.
func New(name string) *Counter {
	return telemetry.Client.NewEvent(name)
}

// CountFlags creates a counter for every flag that is set
// and increments the counter. The name of the counter is
// the concatenation of prefix and the flag name.
//
//	For instance, CountFlags("gopls/flag:", *flag.CommandLine)
func CountFlags(prefix string, fs flag.FlagSet) {
	fs.Visit(func(f *flag.Flag) {
		New(prefix + f.Name).Inc()
	})
}

// CountCommandLineFlags creates a counter for every flag
// that is set in the default flag.CommandLine FlagSet using
// the counter name binaryName+"/flag:"+flagName where
// binaryName is the base name of the Path embedded in the
// binary's build info. If the binary does not have embedded build
// info, the "flag:"+flagName counter will be incremented.
//
// CountCommandLineFlags must be called after flags are parsed
// with flag.Parse.
//
// For instance, if the -S flag is passed to cmd/compile and
// CountCommandLineFlags is called after flags are parsed,
// the "compile/flag:S" counter will be incremented.
func CountCommandLineFlags() {
	prefix := "flag:"
	if buildInfo, ok := debug.ReadBuildInfo(); ok && buildInfo.Path != "" {
		prefix = path.Base(buildInfo.Path) + "/" + prefix
	}
	CountFlags(prefix, *flag.CommandLine)
}
