// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"

	"github.com/microsoft/go-infra/stringutil"
	"github.com/microsoft/go-infra/subcmd"
)

func init() {
	subcommands = append(subcommands, subcmd.Option{
		Name:    "update-json",
		Summary: "Given a MinGW source/version, update the embedded JSON data and write out the new version.",
		TakeArgsReason: "\nA list of seed info used to inform the heuristic guesses about how to find builds. Seeds for each source are:" +
			"\n - " + Nixman + ": a release page, like " + niXmanPrefix + "12.2.0-rt_v10-rev2" +
			"\n - " + Winlibs + ": a 7z artifact URL, like " + winlibsPrefix + "13.2.0posix-17.0.5-11.0.1-ucrt-r3/winlibs-x86_64-posix-seh-gcc-13.2.0-llvm-17.0.5-mingw-w64ucrt-11.0.1-r3.7z" +
			"\n - " + Sourceforge + ": a version number, like 8.1.0",
		Handle: updateJson,
	})
}

func updateJson(p subcmd.ParseFunc) error {
	out := flag.String("out", "", "Write the updated JSON to this file instead of stdout.")
	source := flag.String(
		"source", "",
		"The MinGW source to update. This defines the meaning of the list of seeds provided as args.")
	if err := p(); err != nil {
		return err
	}
	if len(flag.Args()) == 0 {
		return fmt.Errorf("must specify at least one seed")
	}
	existingBuilds, err := unmarshal()
	if err != nil {
		return err
	}
	var newBuilds []*build
	for _, seed := range flag.Args() {
		switch *source {
		case Nixman:
			version, ok := stringutil.CutPrefix(seed, niXmanPrefix+"tag/")
			if !ok {
				return fmt.Errorf("seed %#q doesn't have a tag URL prefix", seed)
			}
			nums, rev, ok := strings.Cut(version, "-")
			if !ok {
				return fmt.Errorf("version %#q doesn't have a revision part after '-'", version)
			}
			for _, arch := range []string{"i686", "x86_64"} {
				for _, ex := range []string{"seh", "dwarf"} {
					for _, th := range []string{"posix", "win32", "mcf"} {
						for _, rt := range []string{"msvcrt", "ucrt"} {
							b := build{
								Arch:      arch,
								Exception: ex,
								Threading: th,
								Runtime:   rt,
							}
							if b.Arch == "i686" && b.Exception != "dwarf" {
								continue
							}
							if b.Arch == "x86_64" && b.Exception != "seh" {
								continue
							}
							b.Source = Nixman
							b.Version = version
							b.URL = niXmanPrefix + "download/" + version +
								"/" + b.Arch + "-" + nums + "-release-" + b.Threading + "-" + b.Exception + "-" + b.Runtime +
								"-" + rev + ".7z"
							newBuilds = append(newBuilds, &b)
						}
					}
				}
			}
		case Winlibs:
			r, ok := stringutil.CutPrefix(seed, winlibsPrefix)
			if !ok {
				return fmt.Errorf("seed %#q doesn't have a tag URL prefix", seed)
			}
			tag, file, ok := strings.Cut(r, "/")
			if !ok {
				return fmt.Errorf("seed %#q doesn't have a final part with a '/'", seed)
			}
			b := build{
				Source:  Winlibs,
				URL:     seed,
				Version: tag,
			}
			if strings.Contains(file, "llvm") {
				b.LLVM = "llvm"
			} else {
				b.LLVM = "no"
			}
			for _, x := range []string{"i686", "x86_64"} {
				if strings.Contains(file, x) {
					b.Arch = x
					break
				}
			}
			if b.Arch == "" {
				return fmt.Errorf("seed %#q doesn't have an arch part", seed)
			}
			for _, x := range []string{"posix", "win32", "mcf"} {
				if strings.Contains(file, x) {
					b.Threading = x
					break
				}
			}
			if b.Threading == "" {
				return fmt.Errorf("seed %#q doesn't have a threading part", seed)
			}
			for _, x := range []string{"seh", "dwarf", "sjlj"} {
				if strings.Contains(file, x) {
					b.Exception = x
					break
				}
			}
			if b.Exception == "" {
				return fmt.Errorf("seed %#q doesn't have an exception part", seed)
			}
			for _, x := range []string{"msvcrt", "ucrt"} {
				if strings.Contains(file, x) {
					b.Runtime = x
					break
				}
			}
			if b.Runtime == "" {
				return fmt.Errorf("seed %#q doesn't have a runtime part", seed)
			}
			// Remove some build parts from the version to avoid search overlap.
			b.Version = strings.ReplaceAll(b.Version, b.Threading, "")
			b.Version = strings.ReplaceAll(b.Version, b.Runtime, "")
			b.Version = strings.ReplaceAll(b.Version, "--", "")
			newBuilds = append(newBuilds, &b)
		case Sourceforge:
			for _, arch := range []string{"i686", "x86_64"} {
				for _, ex := range []string{"seh", "sjlj", "dwarf"} {
					for _, th := range []string{"posix", "win32"} {
						b := build{
							Source:    Sourceforge,
							Version:   seed,
							Arch:      arch,
							Threading: th,
							Exception: ex,
							Runtime:   "v5",
						}
						if seed == "8.1.0" {
							b.Runtime = "v6"
						}
						targeting := "Win64"
						if arch == "i686" {
							targeting = "Win32"
						}
						b.URL = "https://sourceforge.net/projects/mingw-w64/files/Toolchains%20targetting%20" +
							targeting + "/Personal%20Builds/mingw-builds/" +
							seed + "/threads-" + th + "/" + ex + "/" + arch + "-" + seed + "-release-" + th + "-" + ex + "-rt_" + b.Runtime + "-rev0.7z/download"
						newBuilds = append(newBuilds, &b)
					}
				}
			}
		default:
			return fmt.Errorf("unknown source %#q", *source)
		}
	}
	var wg sync.WaitGroup
	for _, b := range newBuilds {
		b := b
		log.Printf("Creating checksum for %v", b.URL)
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := b.CreateFreshChecksum(); err != nil {
				if errors.Is(err, errBuildNotFound) {
					log.Printf("skipping 404 URL %#v", b)
				} else {
					log.Panicf("failed to create checksum for %#v: %v", b, err)
				}
			} else {
				log.Printf("Downloaded %v, generated checksum %v...", b.URL, b.SHA512[:16])
			}
		}()
	}
	wg.Wait()
	for _, b := range newBuilds {
		if b.SHA512 == "" {
			// Skipped, 404.
			continue
		}
		existingBuilds[b.URL] = *b
	}
	result, err := marshal(existingBuilds)
	if err != nil {
		return err
	}
	if *out != "" {
		if err := os.WriteFile(*out, result, 0o666); err != nil {
			return err
		}
		return nil
	} else {
		fmt.Println(string(result))
	}
	return nil
}
