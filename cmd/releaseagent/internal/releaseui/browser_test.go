// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package releaseui

import "testing"

func TestBrowserCommand(t *testing.T) {
	for _, test := range []struct {
		goos    string
		command string
	}{
		{goos: "darwin", command: "open"},
		{goos: "linux", command: "xdg-open"},
		{goos: "windows", command: "rundll32"},
	} {
		t.Run(test.goos, func(t *testing.T) {
			command, args, err := browserCommand(test.goos, "http://127.0.0.1:1234")
			if err != nil {
				t.Fatal(err)
			}
			if command != test.command {
				t.Fatalf("command = %q, want %q", command, test.command)
			}
			if len(args) == 0 || args[len(args)-1] != "http://127.0.0.1:1234" {
				t.Fatalf("browser args do not contain target URL: %q", args)
			}
		})
	}

	if _, _, err := browserCommand("plan9", "http://127.0.0.1:1234"); err == nil {
		t.Fatal("expected unsupported OS error")
	}
}
