// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package powershell_test

import (
	"encoding/json"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/microsoft/go-infra/goinstallscript/powershell"
)

var download = flag.Bool(
	"download", false,
	"Run tests that include downloading Microsoft Go from the internet. "+
		"These may be very slow and not totally reproducible.")

const endOfFunctionsMarker = "# [END OF FUNCTIONS]"

// makeTestFile creates a file in a temporary directory where some content has been inserted after the "end of functions" marker.
func makeTestFile(t *testing.T, postFuncContent string) string {
	t.Helper()
	before, after, ok := strings.Cut(powershell.Content, endOfFunctionsMarker)
	if !ok {
		t.Fatal("missing # [END OF FUNCTIONS] in powershellscript.Content")
	}
	content := before + "\n" + endOfFunctionsMarker + "\n" + postFuncContent + "\n" + after
	p := filepath.Join(t.TempDir(), powershell.Name)
	if err := os.WriteFile(p, []byte(content), 0o777); err != nil {
		t.Fatal(err)
	}
	return p
}

func runTestFile(t *testing.T, interpreter, path string, args ...string) string {
	t.Helper()
	cmd := exec.Command(interpreter, append([]string{"-NoProfile", path}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("error running %v: %v; output:\n---\n%s\n---", cmd, err, out)
	}
	return strings.TrimSpace(string(out))
}

func currentOSInterpreters(t *testing.T) []string {
	t.Helper()
	if runtime.GOOS == "windows" {
		// Windows has both pwsh (PowerShell Core) and powershell (Windows PowerShell).
		return []string{"pwsh", "powershell"}
	}
	// Other platforms only have pwsh (PowerShell Core).
	return []string{"pwsh"}
}

func TestDetectOS(t *testing.T) {
	for _, interpreter := range currentOSInterpreters(t) {
		t.Run(interpreter, func(t *testing.T) {
			t.Parallel()

			out := runTestFile(t, interpreter, makeTestFile(t, `
			@{
				Arch = Get-CLIArchitecture-From-Architecture $Architecture
				OS = Get-CLIOS-From-OS $OS
			} | ConvertTo-Json | Write-Output
			exit
			`))

			var result struct {
				Arch string
				OS   string
			}
			if err := json.Unmarshal([]byte(out), &result); err != nil {
				t.Fatalf("error unmarshalling JSON: %v; output:\n---\n%s\n---", err, out)
			}

			if result.Arch != runtime.GOARCH {
				t.Errorf("expected architecture %q, got %q", runtime.GOARCH, result.Arch)
			}
			if result.OS != runtime.GOOS {
				t.Errorf("expected OS %q, got %q", runtime.GOOS, result.OS)
			}
		})
	}
}

func TestGenerateArchivePath(t *testing.T) {
	for _, interpreter := range currentOSInterpreters(t) {
		t.Run(interpreter, func(t *testing.T) {
			for _, os := range []string{"windows", "linux", "darwin"} {
				t.Run(os, func(t *testing.T) {
					t.Parallel()

					out := runTestFile(t, interpreter, makeTestFile(t, `
					Write-Output (Get-GeneratedArchivePath -CLIOS "`+os+`")
					exit
					`))

					t.Logf("Generated path:\n%s\n", out)

					expectExt := ".tar.gz"
					if os == "windows" {
						expectExt = ".zip"
					}
					if !strings.HasSuffix(out, expectExt) {
						t.Errorf("expected path to end with %q, got %q", expectExt, out)
					}
				})
			}
		})
	}
}

func TestInstallPath(t *testing.T) {
	// Figure out some prefix that we expect the install path to start with.
	// Do the best we can without getting complicated.
	installDirPrefix, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	if localAppData := os.Getenv("LOCALAPPDATA"); localAppData != "" {
		installDirPrefix = localAppData
	}
	if xdgDataHome := os.Getenv("XDG_DATA_HOME"); xdgDataHome != "" {
		installDirPrefix = xdgDataHome
	}

	for _, interpreter := range currentOSInterpreters(t) {
		t.Run(interpreter, func(t *testing.T) {
			t.Parallel()

			out := runTestFile(t, interpreter, makeTestFile(t, `
			Write-Output (Resolve-Installation-Path "<auto>")
			exit
			`))

			if !strings.HasPrefix(out, installDirPrefix) {
				t.Errorf("expected path to start with %q, got %q", installDirPrefix, out)
			}
			t.Logf("Install path:\n%s\n", out)
		})
	}
}

func TestInstall(t *testing.T) {
	if !*download {
		t.Skip("skipping test that downloads Microsoft Go from the internet; use -download to run it")
	}
	for _, interpreter := range currentOSInterpreters(t) {
		t.Run(interpreter, func(t *testing.T) {
			// Test a few version strings that have interesting handling. Note that this test isn't
			// necessarily reproducible because some of these versions are floating versions and
			// will change over time.
			for _, version := range []string{
				"latest",
				"previous",
				"go1.21.2-1",
			} {
				t.Run(version, func(t *testing.T) {
					t.Parallel()

					installDir := t.TempDir()

					cmd := exec.Command(
						interpreter, "-NoProfile",
						makeTestFile(t, ``),
						"-InstallDir", installDir,
						"-Version", version,
					)
					out, err := cmd.CombinedOutput()
					if err != nil {
						t.Fatalf("error running %v: %v; output:\n---\n%s\n---", cmd, err, out)
					}
					t.Logf("Output:\n---\n%s\n---\n", out)

					// Check that there is an installed go or go.exe binary.
					goBlob := filepath.Join(installDir, "go*", "bin", "go")
					if runtime.GOOS == "windows" {
						goBlob += ".exe"
					}
					results, err := filepath.Glob(goBlob)
					if err != nil {
						t.Fatalf("error globbing %q: %v", goBlob, err)
					}
					if len(results) != 1 {
						t.Fatalf("expected exactly one result for %q, got %v", goBlob, results)
					}
				})
			}
		})
	}
}
