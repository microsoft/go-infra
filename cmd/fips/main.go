// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"flag"
	"fmt"
	"go/ast"
	"log"
	"os"
	"strings"

	"golang.org/x/tools/go/packages"
)

const description = `
Fips examines Go source code and reports which public APIs are backed by openssl
primitives, together with other useful information such as the
openssl function names.

Set the appropriate GOROOT when checking std packages not in your default Go toolchain. 

Packages are specified using the notation of "go list".
This command checks the package in the current directory:

	go run ./cmd/fips

whereas this one checks the std crypto package in GOROOT, as specified:

	go run ./cmd/fips crypto/...
`

type fnReport struct {
	PackageID        string
	Name             string
	HasBoringEnabled bool
	BoringCalls      []string
}

var goos = flag.String("goos", "", "The operating system for which to compile the examinded packaged. Defaults to GOOS")

func main() {
	log.SetFlags(0)
	log.SetPrefix("fips: ")

	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "\nUsage:\n")
		flag.PrintDefaults()
		fmt.Fprintf(flag.CommandLine.Output(), "%s\n\n", description)
	}

	flag.Parse()

	pkgs, err := parsePackages()
	if err != nil {
		log.Fatalf("load: %v\n", err)
		os.Exit(1)
	}
	if packages.PrintErrors(pkgs) > 0 {
		os.Exit(1)
	}

	for _, pkg := range pkgs {
		// We are only interested in public packages.
		if strings.Contains(pkg.ID, "/internal/") {
			continue
		}
		tryResolveMissingObjects(pkg.Syntax)
		for _, src := range pkg.Syntax {
			for _, decl := range src.Decls {
				if decl, ok := decl.(*ast.FuncDecl); ok {
					if decl.Body == nil {
						continue
					}
					if decl.Recv != nil {
						// We are only interested in top level functions.
						// Methods may contain boring calls but these are always
						// called from top level functions.
						continue
					}
					if !ast.IsExported(decl.Name.Name) {
						// We are only interested in exported declarations.
						continue
					}
					report := fnReport{
						PackageID: pkg.ID,
						Name:      decl.Name.Name,
					}
					processFuncDecl(decl, &report)
					log.Println(report)
				}
			}
		}
	}
}

func parsePackages() ([]*packages.Package, error) {
	env := os.Environ()
	if *goos != "" {
		env = append(env, "GOOS="+*goos)
	}
	cfg := &packages.Config{
		Env: env,
		Mode: packages.NeedImports | packages.NeedDeps | packages.NeedSyntax |
			packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles |
			packages.NeedTypes | packages.NeedTypesInfo | packages.NeedTypesSizes,
	}
	pkgs, err := packages.Load(cfg, flag.Args()...)
	if err != nil {
		return nil, err
	}
	return pkgs, nil

}

// Ideally objects are always resolved by the package loader but that's not always true
// so we try to resolve objects looking at other files within the same package.
func tryResolveMissingObjects(files []*ast.File) {
	funcDecls := make(map[string]*ast.Object)
	for _, f := range files {
		for _, d := range f.Decls {
			if fn, ok := d.(*ast.FuncDecl); ok {
				if fn.Recv == nil && fn.Name.Obj != nil {
					funcDecls[fn.Name.Name] = fn.Name.Obj
				}
			}
		}
	}
	for _, f := range files {
		ast.Inspect(f, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.CallExpr:
				if ident, ok := n.Fun.(*ast.Ident); ok && ident.Obj == nil {
					if ident.Obj, ok = funcDecls[ident.Name]; ok {
						return false
					}
				}
			}
			return true
		})
	}
}

func resolveFun(expr *ast.CallExpr) *ast.FuncDecl {
	if ident, ok := expr.Fun.(*ast.Ident); ok {
		if ident.Obj != nil {
			fn, ok := ident.Obj.Decl.(*ast.FuncDecl)
			if !ok {
				log.Fatalf("An Ident.Obj referenced from a CallExpr.Fun should always be a FuncDecl but is a %T\n", ident.Obj.Decl)
			}
			return fn
		}
	}
	return nil
}

// findBoringCalls traverses the node searching for calls to the boring package.
func findBoringCalls(node ast.Node) []string {
	var calls []string
	ast.Inspect(node, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.CallExpr: // enableBoring()
			fn := resolveFun(n)
			if fn != nil {
				if fn.Recv != nil {
					// TODO: This may require dynamic dispatch analysis.
					// See https://github.com/microsoft/go/issues/278
					return false
				}
				calls = append(calls, findBoringCalls(fn.Body)...)
				return false
			}
		case *ast.SelectorExpr: // boring.Fn(...)
			if ident, ok := n.X.(*ast.Ident); ok {
				if ident.Name == "boring" && n.Sel.Name != "Enabled" {
					calls = append(calls, n.Sel.Name)
					return false
				}
			}
		}
		return true
	})
	return calls
}

// hasBoringEnabled traverses the node to find if it has a boring.Enabled() call.
func hasBoringEnabled(node ast.Node, seen map[*ast.FuncDecl]struct{}) bool {
	var ret bool
	ast.Inspect(node, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.CallExpr: // enableBoring()
			fn := resolveFun(n)
			if fn != nil {
				if _, ok := seen[fn]; ok {
					// Avoid infinite loop.
					return false
				}
				seen[fn] = struct{}{}
				if hasBoringEnabled(fn, seen) {
					ret = true
					return false
				}
			}
		case *ast.SelectorExpr: // foo.Fn(...)
			if ident, ok := n.X.(*ast.Ident); ok {
				if ident.Name == "boring" {
					if n.Sel.Name == "Enabled" {
						ret = true
						return false
					}
				}
			}
		}
		return true
	})
	return ret
}

func processFuncDecl(decl *ast.FuncDecl, report *fnReport) {
	seen := make(map[*ast.FuncDecl]struct{})
	ast.Inspect(decl.Body, func(n ast.Node) bool {
		switch stmt := n.(type) {
		// Only search for boring calls that are inside a boring.Enabled if block.
		case *ast.IfStmt:
			if hasBoringEnabled(stmt.Cond, seen) { // if boring.Enabled() {...}
				report.HasBoringEnabled = true
				report.BoringCalls = findBoringCalls(stmt.Body) // boring.Fn()
				return false
			}
		}
		return true
	})
}
