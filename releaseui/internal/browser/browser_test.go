// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package browser

import "testing"

func TestCommand(t *testing.T) {
	for _, test := range []struct {
		goos    string
		isWSL   bool
		command string
	}{
		{goos: "darwin", command: "open"},
		{goos: "linux", command: "xdg-open"},
		{goos: "linux", isWSL: true, command: "rundll32.exe"},
		{goos: "windows", command: "rundll32"},
	} {
		t.Run(test.command, func(t *testing.T) {
			actual, args, err := command(test.goos, test.isWSL, "http://127.0.0.1:1234")
			if err != nil {
				t.Fatal(err)
			}
			if actual != test.command {
				t.Fatalf("command = %q, want %q", actual, test.command)
			}
			if len(args) == 0 || args[len(args)-1] != "http://127.0.0.1:1234" {
				t.Fatalf("browser args do not contain target URL: %q", args)
			}
		})
	}

	if _, _, err := command("plan9", false, "http://127.0.0.1:1234"); err == nil {
		t.Fatal("expected unsupported OS error")
	}
}
