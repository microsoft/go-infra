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
primitives, together with other usefull information such as the name of the
openssl function names.

Set the appropiate GOROOT when checking std packagesnot in your default Go toolchain. 

Packages are specified using the notation of "go list".
This command checks the package in the current directory:

	go run ./cmd/fips

whereas this one checks the packages whose path is provided:

	go run ./cmd/fips crypto/...
`

type fnReport struct {
	PackageID        string
	Name             string
	HasBoringEnabled bool
	BoringCalls      []string
}

func main() {
	log.SetFlags(0)
	log.SetPrefix("fips: ")

	flag.Usage = func() {
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
		for _, src := range pkg.Syntax {
			for _, decl := range src.Decls {
				if decl, ok := decl.(*ast.FuncDecl); ok {
					if !ast.IsExported(decl.Name.Name) {
						// We are only interested in exported declarations.
						continue
					}
					var report fnReport
					report.PackageID = pkg.ID
					report.Name = decl.Name.Name
					if processFuncDecl(decl, pkg.Syntax, &report) {
						log.Println(report)
					}
				}
			}
		}
	}
}

func parsePackages() ([]*packages.Package, error) {
	cfg := &packages.Config{
		Env: append(os.Environ(), "GOOS=linux"),
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

func resolveFun(expr *ast.CallExpr, files []*ast.File) *ast.FuncDecl {
	if ident, ok := expr.Fun.(*ast.Ident); ok {
		if ident.Obj != nil {
			fn, _ := ident.Obj.Decl.(*ast.FuncDecl)
			return fn
		}
		// Ideally objects are always resolved by the package loader,
		// but that's not always true so we try to resolve the function
		// looking at other files within the same package.
		for _, f := range files {
			for _, d := range f.Decls {
				if fn, ok := d.(*ast.FuncDecl); ok {
					if fn.Name.Name == ident.Name {
						return fn
					}
				}
			}
		}
	}
	return nil
}

// hasBoringEnabled traverses the node to find if it has a boring.Enabled() call.
// Function calls which are not yet resolved yet will be in files.
func hasBoringEnabled(node ast.Node, seen map[*ast.FuncDecl]struct{}, files []*ast.File) bool {
	var ret bool
	ast.Inspect(node, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.CallExpr: // enableBoring()
			fn := resolveFun(n, files)
			if fn != nil {
				if _, ok := seen[fn]; ok {
					return false
				}
				seen[fn] = struct{}{}
				if hasBoringEnabled(fn, seen, files) {
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

// searchBoringCalls traverses the node searching for calls to the boring package.
// Function calls which are not yet resolved will be searched in files.
func searchBoringCalls(node ast.Node, files []*ast.File, calls []string) []string {
	ast.Inspect(node, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.CallExpr: // enableBoring()
			fn := resolveFun(n, files)
			if fn != nil {
				calls = searchBoringCalls(fn.Body, files, calls)
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

func processFuncDecl(decl *ast.FuncDecl, files []*ast.File, report *fnReport) bool {
	if decl.Body == nil {
		return false
	}
	if decl.Recv != nil {
		// TODO: check methods.
		return false
	}
	seen := make(map[*ast.FuncDecl]struct{})
	ast.Inspect(decl.Body, func(n ast.Node) bool {
		switch stmt := n.(type) {
		// Only search for boring calls that are inside a boring.Enabled if block.
		case *ast.IfStmt:
			if hasBoringEnabled(stmt.Cond, seen, files) { // if boring.Enabled() {...}
				report.HasBoringEnabled = true
				report.BoringCalls = searchBoringCalls(stmt.Body, files, nil) // boring.Fn()
				return false
			}
		}
		return true
	})
	return true
}
