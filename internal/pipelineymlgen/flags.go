// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package pipelineymlgen

import (
	"errors"
	"flag"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

type CmdFlags struct {
	Verbose   bool
	Check     bool
	Clean     bool
	Recursive bool
}

func BindCmdFlags() *CmdFlags {
	var f CmdFlags
	flag.BoolVar(
		&f.Check, "exit-code", false,
		"Check if the file would change instead of writing it. "+
			"Exit code 2 if there's a difference, 1 if there's an error, 0 if it matches.")
	flag.BoolVar(
		&f.Clean, "clean", false,
		"Remove all generated files created by discovered <pipeline>.gen.yml files. "+
			"Use this to clean up before removing, renaming, or changing the outputs of a source file.")
	flag.BoolVar(
		&f.Recursive, "r", false,
		"Recursively search subdirectories for <pipeline>.gen.yml files, if target is a directory.")
	return &f
}

func (f *CmdFlags) TargetFiles(args []string) ([]string, error) {
	if len(args) == 0 {
		return nil, errors.New("no input files specified")
	}

	var files []string
	for _, arg := range args {
		info, err := os.Stat(arg)
		if err != nil {
			return nil, err
		}

		if !info.IsDir() {
			files = append(files, arg)
			continue
		}

		err = filepath.WalkDir(arg, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}

			if d.IsDir() {
				if d.Name() == ".git" {
					return filepath.SkipDir
				}
				if path == arg || f.Recursive {
					return nil
				}
				return filepath.SkipDir
			}

			if strings.HasSuffix(d.Name(), ".gen.yml") {
				files = append(files, path)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return files, nil
}
