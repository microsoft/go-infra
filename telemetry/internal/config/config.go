// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package config

import (
	"encoding/json"
	"os"
	"strings"
)

// An UploadConfig controls what data is uploaded.
type UploadConfig struct {
	GOOS      []string
	GOARCH    []string
	GoVersion []string
	Programs  []*ProgramConfig
}

// A ProgramConfig contains the configuration for a single program.
type ProgramConfig struct {
	// the counter names may have to be
	// repeated for each program. (e.g., if the counters are in a package
	// that is used in more than one program.)
	Name     string
	Counters []CounterConfig `json:",omitempty"`
}

// A CounterConfig contains the configuration for a single counter.
type CounterConfig struct {
	Name string // The "collapsed" counter: <chart>:{<bucket1>,<bucket2>,...}
}

func ReadConfig(file string) (*UploadConfig, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	return UnmarshalConfig(data)
}

func UnmarshalConfig(data []byte) (*UploadConfig, error) {
	var cfg UploadConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// Expand takes a counter defined with buckets and expands it into distinct
// strings for each bucket.
func Expand(counter string) []string {
	prefix, rest, hasBuckets := strings.Cut(counter, "{")
	var counters []string
	if hasBuckets {
		buckets := strings.SplitSeq(strings.TrimSuffix(rest, "}"), ",")
		for b := range buckets {
			counters = append(counters, prefix+b)
		}
	} else {
		counters = append(counters, prefix)
	}
	return counters
}
