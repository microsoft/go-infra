// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

// A target defines a fuzz target.
// The name must be a fuzz target, it can be prefixed with a relative directory path.
// The weight will be used to calculate the fuzz time for the target
// by scaling --fuzztime by the weight relative to the sum of all the target weights.
type target struct {
	name   string
	weight float64
}

var alltargets = []target{
	{"std/FuzzSha1", 1},
	{"std/FuzzSha224", 1},
	{"std/FuzzSha256", 1},
	{"std/FuzzSha384", 1},
	{"std/FuzzSha512", 1},
	{"std/FuzzHMACSha1", 1},
	{"std/FuzzHMACSha224", 1},
	{"std/FuzzHMACSha256", 1},
	{"std/FuzzHMACSha384", 1},
	{"std/FuzzHMACSha512", 1},
	{"std/FuzzRSAOAEP", 2},
	{"std/FuzzRSAPKCS1", 2},
	{"std/FuzzRSASignPSS", 2},
	{"std/FuzzRSASignPKCS1v15", 2},
	{"go-cose/FuzzSign1Message_UnmarshalCBOR", 2},
	{"go-cose/FuzzSign1", 2},
}
