// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// check-imports verifies that a set of Go packages only import from an
// allowlist of standard-library packages. This is intended for libraries
// that are vendored into the Go standard library, where new imports require
// a corresponding update to the Go dependency rules (deps_test.go).
//
// Usage:
//
//	check-imports -pkg ./osslsetup -allow errors,strconv,strings,sync,syscall,unsafe
//	check-imports -pkg ./internal/ossl -allow errors,unsafe -pkg . -allow crypto,errors,hash,io,...
//
// Each -pkg flag starts a new package check. The -allow flag that follows it
// specifies the comma-separated list of allowed standard-library imports for
// that package. Internal module imports (matching the module path) and "C"
// are always permitted.
//
// Exit code 0 means all imports are within the allowlists. Exit code 1 means
// one or more disallowed imports were found.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

type pkgCheck struct {
	pkg     string
	allowed map[string]bool
}

// defaultAllowed is the allowlist used when -allow is not specified for a -pkg.
// These correspond to the imports permitted by deps_test.go in the Go
// standard library for the crypto backends.
var defaultAllowed = []string{"crypto", "crypto/cipher", "crypto/subtle", "errors", "hash", "io", "math/bits", "runtime", "slices", "strconv", "sync", "unsafe"}

func main() {
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, `Usage: check-imports [flags]

Flags:
  -pkg <import-path>       Go package to check (may be repeated)
  -allow <pkg1,pkg2,...>   Comma-separated allowlist for the preceding -pkg (optional: overrides default)
  -module <module-path>    Module path prefix to treat as internal (auto-detected if omitted)

If -allow is omitted after a -pkg, the built-in default allowlist is used:
  crypto,crypto/cipher,crypto/subtle,errors,hash,io,math/bits,runtime,slices,strconv,sync,unsafe

Imports of "C" and packages under the module path are always permitted.

Example:
  check-imports -pkg ./internal/ossl -pkg ./osslsetup -pkg .
  check-imports -pkg ./internal/ossl -allow errors,unsafe -pkg . -allow crypto,errors
`)
	}

	args := os.Args[1:]
	var checks []pkgCheck
	var modulePath string

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-h", "-help", "--help":
			flag.Usage()
			os.Exit(0)
		case "-module":
			i++
			if i >= len(args) {
				fmt.Fprintln(os.Stderr, "error: -module requires a value")
				os.Exit(2)
			}
			modulePath = args[i]
		case "-pkg":
			i++
			if i >= len(args) {
				fmt.Fprintln(os.Stderr, "error: -pkg requires a value")
				os.Exit(2)
			}
			pkg := args[i]
			// Check if next arg is -allow (optional).
			var allowed map[string]bool
			if i+1 < len(args) && args[i+1] == "-allow" {
				i++ // consume -allow
				i++
				if i >= len(args) {
					fmt.Fprintf(os.Stderr, "error: -allow requires a value after -pkg %s\n", pkg)
					os.Exit(2)
				}
				allowed = make(map[string]bool)
				for _, a := range strings.Split(args[i], ",") {
					a = strings.TrimSpace(a)
					if a != "" {
						allowed[a] = true
					}
				}
			} else {
				allowed = make(map[string]bool, len(defaultAllowed))
				for _, a := range defaultAllowed {
					allowed[a] = true
				}
			}
			checks = append(checks, pkgCheck{pkg: pkg, allowed: allowed})
		default:
			fmt.Fprintf(os.Stderr, "error: unknown argument: %s\n", args[i])
			flag.Usage()
			os.Exit(2)
		}
	}

	if len(checks) == 0 {
		fmt.Fprintln(os.Stderr, "error: no -pkg flags specified")
		flag.Usage()
		os.Exit(2)
	}

	// Auto-detect module path if not provided.
	if modulePath == "" {
		var err error
		modulePath, err = detectModule()
		if err != nil {
			fmt.Fprintf(os.Stderr, "error detecting module path: %v\n", err)
			os.Exit(2)
		}
	}

	failed := false
	for _, c := range checks {
		imports, err := listImports(c.pkg)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error listing imports for %s: %v\n", c.pkg, err)
			os.Exit(2)
		}
		for _, imp := range imports {
			if imp == "C" {
				continue
			}
			if strings.HasPrefix(imp, modulePath+"/") || imp == modulePath {
				continue
			}
			if !c.allowed[imp] {
				fmt.Printf("FAIL: %s imports disallowed package: %s\n", c.pkg, imp)
				failed = true
			}
		}
	}

	if failed {
		fmt.Println("\nOne or more packages import disallowed dependencies.")
		fmt.Println("If the new import is intentional, update the allowlist in the CI workflow")
		fmt.Println("and the corresponding deps_test.go patch in microsoft-go.")
		os.Exit(1)
	}

	fmt.Println("OK: all package imports are within the allowlists.")
}

// goListOutput is the subset of `go list -json` we need.
type goListOutput struct {
	Imports []string `json:"Imports"`
}

func listImports(pkg string) ([]string, error) {
	cmd := exec.Command("go", "list", "-json", pkg)
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	var result goListOutput
	if err := json.Unmarshal(out, &result); err != nil {
		return nil, fmt.Errorf("parsing go list output: %w", err)
	}
	return result.Imports, nil
}

// goModOutput is the subset of `go mod edit -json` we need.
type goModOutput struct {
	Module struct {
		Path string `json:"Path"`
	} `json:"Module"`
}

func detectModule() (string, error) {
	cmd := exec.Command("go", "mod", "edit", "-json")
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	var result goModOutput
	if err := json.Unmarshal(out, &result); err != nil {
		return "", fmt.Errorf("parsing go mod output: %w", err)
	}
	if result.Module.Path == "" {
		return "", fmt.Errorf("no module path found in go.mod")
	}
	return result.Module.Path, nil
}
