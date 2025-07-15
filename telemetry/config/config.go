// The config package holds the config.json file defining the Go telemetry
// upload configuration.
//
// An upload configuration specifies the set of values that are permitted in
// telemetry uploads: GOOS, GOARCH, and per-program counters.
package config

import _ "embed" // for config.json

//go:embed config.json
var Config []byte
