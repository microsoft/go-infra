package main

import (
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/microsoft/go-infra/internal/fork"
)

var forkRootDir = flag.String("fork", "", "Crypto fork root directory")
var backendDir = flag.String("backend", "", "Directory with Go files that implement the backend")
var outDir = flag.String(
	"out", "",
	"Output directory\n"+
		"Creates a copy of the fork in this directory and generates the backend there")

var onlyAPI = flag.Bool(
	"api", false,
	"Only generate the API (nobackend)\n"+
		"This helps generate a clean API file for use in a toolset-agnostic x/crypto patch")

var autoYes = flag.Bool("y", false, "delete old output and overwrite without prompting")

func main() {
	h := flag.Bool("h", false, "show help")
	flag.Parse()
	if *h {
		flag.Usage()
		return
	}
	rej := func(s string) {
		fmt.Fprintln(flag.CommandLine.Output(), s)
		flag.Usage()
		os.Exit(1)
	}
	if *forkRootDir == "" {
		rej("missing -fork")
	}
	if *backendDir == "" {
		rej("missing -backend")
	}
	if err := run(); err != nil {
		log.Fatalln(err)
	}
}

func run() error {
	var proxyDir string
	if *outDir == "" {
		proxyDir = filepath.Join(*forkRootDir, fork.XCryptoBackendProxyPath)
		fmt.Printf("Not specified: '-out'. Generating backend files in %q\n", proxyDir)
		if err := fork.RemoveDirContent(proxyDir, !*autoYes); err != nil {
			return err
		}
	} else {
		proxyDir = filepath.Join(*outDir, fork.XCryptoBackendProxyPath)
		fmt.Printf("Specified: '-out'. Creating copy of Git repo to generate proxy in %q\n", proxyDir)
		if err := fork.RemoveDirContent(*outDir, !*autoYes); err != nil {
			return err
		}
		if err := fork.GitCheckoutTo(*forkRootDir, *outDir); err != nil {
			return err
		}
	}
	// For now, use the nobackend as a source of truth for the API. This keeps
	// maintenance cost low while only one Go toolset implements the API.
	//
	// When sharing the API among multiple Go toolset forks, it is probably
	// better to make the API/placeholder itself be the source of truth, so it
	// receives only intentional changes.
	backends, err := fork.FindBackendFiles(*backendDir)
	if err != nil {
		return err
	}
	var backendAPI *fork.BackendFile
	for _, b := range backends {
		if b.Filename == filepath.Join(*backendDir, "nobackend.go") {
			if err := b.APITrim(); err != nil {
				return err
			}
			backendAPI = b
			break
		}
	}
	if backendAPI == nil {
		for _, b := range backends {
			log.Printf("Found backend: %v\n", b.Filename)
		}
		return errors.New("no backend found appears to be nobackend")
	}
	// Remove toolset-specific information about the API if only generating the API.
	if *onlyAPI {
		backendAPI.Constraint = ""
	}
	// Create a proxy for each backend.
	for _, b := range backends {
		if b == backendAPI {
			// This is the unimplemented placeholder API, not a proxy. It's ready to write.
			if err := writeBackend(b, filepath.Join(proxyDir, "nobackend.go")); err != nil {
				return err
			}
			continue
		} else if *onlyAPI {
			continue
		}
		proxy, err := b.ProxyAPI(backendAPI)
		if err != nil {
			return err
		}
		err = writeBackend(proxy, filepath.Join(proxyDir, filepath.Base(b.Filename)))
		if err != nil {
			return err
		}
	}
	return nil
}

func writeBackend(b fork.FormattedWriterTo, path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o777); err != nil {
		return err
	}
	apiFile, err := os.Create(path)
	if err != nil {
		return err
	}
	err = b.Format(apiFile)
	if err2 := apiFile.Close(); err == nil {
		err = err2
	}
	return err
}
