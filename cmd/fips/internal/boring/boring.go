// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// boring package is a stub just used for testing purposes
// so fips tests can be executed even outside the boring branch.
package boring

func Enabled() bool {
	return true
}

func A() {}
func B() {}
func C() {}
