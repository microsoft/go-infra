package pipelineymlgen

import "flag"

type CmdFlags struct {
	Verbose   bool
	Check     bool
	Clean     bool
	Target    string
	Recursive bool
}

func BindCmdFlags() *CmdFlags {
	var f CmdFlags
	flag.BoolVar(
		&f.Verbose, "v", false,
		"Enable verbose output.")
	flag.BoolVar(
		&f.Check, "check", false,
		"Check if the file would change instead of writing it. "+
			"Exit code 2 if there's a difference, 1 if there's an error, 0 if it matches.")
	flag.BoolVar(
		&f.Clean, "clean", false,
		"Remove all generated files created by discovered <pipeline>.src.yml files. "+
			"Use this to clean up before removing, renaming, or changing the outputs of a source file.")
	flag.StringVar(
		&f.Target, "t", ".",
		"Path to the target <pipeline>.src.yml file, or a directory that may contain many files.")
	flag.BoolVar(
		&f.Recursive, "r", false,
		"Recursively search subdirectories for <pipeline>.src.yml files, if target is a directory.")
	return &f
}

func Run(f *CmdFlags) error {

	return nil
}
