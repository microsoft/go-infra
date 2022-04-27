// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// Package subcmd implements simple command line interface subcommands.
package subcmd

import (
	"flag"
	"fmt"
	"os"
)

// ParseFunc parses all flags that have been set up. Include extra handling for "-h" help flag
// detailed output, invalid args, and error conditions.
type ParseFunc func() error

// Option implements one subcommand option that the user may pick.
type Option interface {
	// Name of the option. This must match what the user types for this option to be selected.
	Name() string
	// Summary is a brief description of the option. Short: needs to fit in a list of all
	// subcommands in the help text that summarizes all subcommand options.
	Summary() string
	// Description is a description of the option that will be printed directly appended to Summary
	// to optionally add more detail to the option-specific help message.
	Description() string
	// Handle is called when this option is the one picked by the user. Handle must set up any
	// additional flags on its own, run flag parsing by invoking p, then carry out the cmd. Handle
	// is a single function rather than split into individual "Flags" and "Run" funcs so the flags
	// can be declared succinctly as local variables.
	Handle(p ParseFunc) error
}

// OptionArgTaker is an Option that takes args. The Run function checks for this interface to
// provide more detailed help and to only allows non-flag args if the type matches.
type OptionArgTaker interface {
	Option
	// ArgsSummary returns a string to use in help text to briefly describe the purpose of the extra
	// args that can be given to the command.
	ArgsSummary() string
}

// Run runs a subcommand specified by args, or a help request.
func Run(cmdBaseDoc, description string, options []Option) error {
	printMainUsage := func() {
		fmt.Fprintf(flag.CommandLine.Output(), "Usage:\n")
		for _, c := range options {
			fmt.Fprintf(flag.CommandLine.Output(), "  %v %v [-h] [...]\n", cmdBaseDoc, c.Name())
			fmt.Fprintf(flag.CommandLine.Output(), "    %v\n", c.Summary())
		}
		fmt.Fprintf(flag.CommandLine.Output(), "%v", description)
	}

	if len(os.Args) < 2 || os.Args[1] == "-h" {
		printMainUsage()
		return nil
	}

	for _, subCmd := range options {
		if subCmd.Name() == os.Args[1] {
			flag.Usage = func() {
				fmt.Fprintf(flag.CommandLine.Output(), "Usage:\n")
				flag.PrintDefaults()
				if t, ok := subCmd.(OptionArgTaker); ok {
					fmt.Fprintf(flag.CommandLine.Output(), "  [args] ...string\n    \t%v\n", t.ArgsSummary())
				}
				fmt.Fprintf(flag.CommandLine.Output(), "\n%s", subCmd.Summary()+subCmd.Description())
			}

			p := func() error {
				// Ignore arg 1: option name.
				if err := flag.CommandLine.Parse(os.Args[2:]); err != nil {
					return err
				}

				if _, ok := subCmd.(OptionArgTaker); !ok {
					if len(flag.Args()) > 0 {
						flag.Usage()
						return fmt.Errorf("non-flag argument(s) provided but not accepted: %v", flag.Args())
					}
				}
				return nil
			}

			if err := subCmd.Handle(p); err != nil {
				fmt.Printf("\n%v\n", err)
				os.Exit(1)
			}

			fmt.Println("\nSuccess.")
			return nil
		}
	}
	printMainUsage()
	return fmt.Errorf("error: not a valid option: %v", os.Args[1])
}
