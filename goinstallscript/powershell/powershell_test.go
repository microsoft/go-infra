// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package powershell_test

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
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
	"Run tests that include downloading Microsoft build of Go from the internet. "+
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

func TestAzurePipelinePath(t *testing.T) {
	for _, interpreter := range currentOSInterpreters(t) {
		t.Run(interpreter, func(t *testing.T) {
			t.Parallel()

			installDir := t.TempDir()
			out := runTestFile(t, interpreter, makeTestFile(t, `
			Prepend-ToolsetPathEnv -InstallRoot $InstallDir -SpecificVersion "1.21.2-1"
			exit
			`), "-InstallDir", installDir, "-AzurePipelinePath")

			want := "go-install: ##vso[task.prependpath]" + filepath.Join(installDir, "go1.21.2-1", "bin")
			for _, line := range strings.Split(out, "\n") {
				if strings.TrimSuffix(line, "\r") == want {
					return
				}
			}
			t.Errorf("expected logging command %q, got:\n%s", want, out)
		})
	}
}

func TestGitHubActionsPath(t *testing.T) {
	for _, interpreter := range currentOSInterpreters(t) {
		t.Run(interpreter, func(t *testing.T) {
			t.Parallel()

			installDir := t.TempDir()
			pathFile := filepath.Join(installDir, "github-path")
			if err := os.WriteFile(pathFile, []byte("existing-path\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			runTestFile(t, interpreter, makeTestFile(t, `
			$env:GITHUB_PATH = Join-Path $InstallDir "github-path"
			Prepend-ToolsetPathEnv -InstallRoot $InstallDir -SpecificVersion "1.21.2-1"
			exit
			`), "-InstallDir", installDir, "-GitHubActionsPath")

			content, err := os.ReadFile(pathFile)
			if err != nil {
				t.Fatal(err)
			}
			want := "existing-path\n" + filepath.Join(installDir, "go1.21.2-1", "bin") + "\n"
			if got := strings.ReplaceAll(string(content), "\r\n", "\n"); got != want {
				t.Errorf("expected GITHUB_PATH content %q, got %q", want, got)
			}
		})
	}
}

func TestDownloadFileRetriesStreamFailure(t *testing.T) {
	for _, interpreter := range currentOSInterpreters(t) {
		t.Run(interpreter, func(t *testing.T) {
			t.Parallel()

			outPath := filepath.Join(t.TempDir(), "download")
			outPathPS := strings.ReplaceAll(outPath, "'", "''")
			out := runTestFile(t, interpreter, makeTestFile(t, `
			Load-Assembly -Assembly System.Net.Http
			$script:downloadAttempts = 0
			function GetHTTPResponse([Uri] $Uri, [bool]$HeaderOnly, [bool]$DisableRedirect, [int]$MaxAttempts = 3) {
				$script:downloadAttempts++
				$response = [System.Net.Http.HttpResponseMessage]::new([System.Net.HttpStatusCode]::OK)
				if ($script:downloadAttempts -eq 1) {
					$stream = [System.IO.MemoryStream]::new([byte[]]@(65, 66, 67))
					$response.Content = [System.Net.Http.StreamContent]::new($stream)
					$stream.Dispose()
				}
				else {
					$response.Content = [System.Net.Http.StringContent]::new("downloaded")
				}
				return $response
			}
			DownloadFile -Source "https://example.invalid/file" -OutPath '`+outPathPS+`'
			Write-Output "attempts=$script:downloadAttempts"
			exit
			`))

			if !strings.Contains(out, "attempts=2") {
				t.Fatalf("expected the download to be attempted twice, got:\n%s", out)
			}
			downloaded, err := os.ReadFile(outPath)
			if err != nil {
				t.Fatal(err)
			}
			if string(downloaded) != "downloaded" {
				t.Errorf("expected successful retry content %q, got %q", "downloaded", downloaded)
			}
		})
	}
}

func TestDownloadFileDoesNotRetryNotFound(t *testing.T) {
	for _, interpreter := range currentOSInterpreters(t) {
		t.Run(interpreter, func(t *testing.T) {
			t.Parallel()

			out := runTestFile(t, interpreter, makeTestFile(t, `
			$script:downloadAttempts = 0
			function GetHTTPResponse([Uri] $Uri, [bool]$HeaderOnly, [bool]$DisableRedirect, [int]$MaxAttempts = 3) {
				$script:downloadAttempts++
				$exception = [System.Exception]::new("Not found")
				$exception.Data["NonRetryable"] = $true
				throw $exception
			}
			try {
				DownloadFile -Source "https://example.invalid/missing" -OutPath "missing-download"
			}
			catch {
			}
			Write-Output "attempts=$script:downloadAttempts"
			exit
			`))

			if !strings.Contains(out, "attempts=1") {
				t.Fatalf("expected the not-found download to be attempted once, got:\n%s", out)
			}
		})
	}
}

func TestInstallArchiveIntegrity(t *testing.T) {
	const toolchainContent = "test toolchain"
	var archive bytes.Buffer
	w := zip.NewWriter(&archive)
	f, err := w.Create("go/bin/go")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte(toolchainContent)); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(archive.Bytes()))
	wrongDigest := strings.Repeat("0", 64)

	for _, interpreter := range currentOSInterpreters(t) {
		t.Run(interpreter, func(t *testing.T) {
			for _, tt := range []struct {
				name         string
				digest       any
				keepArchive  bool
				hashFailure  bool
				dryRun       bool
				installed    bool
				wantDownload bool
				wantError    string
			}{
				{name: "lowercase", digest: digest, wantDownload: true},
				{name: "uppercase", digest: strings.ToUpper(digest), wantDownload: true},
				{name: "mixed-case-kept", digest: strings.ToUpper(digest[:32]) + digest[32:], keepArchive: true, wantDownload: true},
				{name: "mismatch", digest: wrongDigest, wantDownload: true, wantError: "SHA-256 mismatch"},
				{name: "mismatch-kept", digest: wrongDigest, keepArchive: true, wantDownload: true, wantError: "SHA-256 mismatch"},
				{name: "missing", wantError: "valid SHA-256 digest"},
				{name: "null", digest: json.RawMessage("null"), wantError: "valid SHA-256 digest"},
				{name: "empty", digest: "", wantError: "valid SHA-256 digest"},
				{name: "short", digest: digest[:63], wantError: "valid SHA-256 digest"},
				{name: "long", digest: digest + "0", wantError: "valid SHA-256 digest"},
				{name: "non-hex", digest: "g" + digest[1:], wantError: "valid SHA-256 digest"},
				{name: "leading-space", digest: " " + digest, wantError: "valid SHA-256 digest"},
				{name: "trailing-space", digest: digest + " ", wantError: "valid SHA-256 digest"},
				{name: "trailing-newline", digest: digest + "\n", wantError: "valid SHA-256 digest"},
				{name: "trailing-crlf", digest: digest + "\r\n", wantError: "valid SHA-256 digest"},
				{name: "embedded-newline", digest: digest[:32] + "\n" + digest[32:], wantError: "valid SHA-256 digest"},
				{name: "boolean", digest: true, wantError: "valid SHA-256 digest"},
				{name: "number", digest: 1234, wantError: "valid SHA-256 digest"},
				{name: "object", digest: map[string]string{"value": digest}, wantError: "valid SHA-256 digest"},
				{name: "empty-array", digest: []string{}, wantError: "valid SHA-256 digest"},
				{name: "single-element-array", digest: []string{digest}, wantError: "valid SHA-256 digest"},
				{name: "multiple-element-array", digest: []string{digest, digest}, wantError: "valid SHA-256 digest"},
				{name: "hash-error", digest: digest, hashFailure: true, wantDownload: true, wantError: "simulated hash failure"},
				{name: "hash-error-kept", digest: digest, hashFailure: true, keepArchive: true, wantDownload: true, wantError: "simulated hash failure"},
				{name: "dry-run", dryRun: true},
				{name: "already-installed", installed: true},
			} {
				t.Run(tt.name, func(t *testing.T) {
					t.Parallel()

					injected := `
					trap {
						Write-Output $_.Exception.Message
						exit 1
					}
					function DownloadJson([string]$Source) {
						return Get-Content -LiteralPath (Join-Path $PSScriptRoot "assets.json") -Raw | ConvertFrom-Json
					}
					`
					if tt.hashFailure {
						injected += `
						Import-Module Microsoft.PowerShell.Utility
						function Get-FileHash([string]$LiteralPath, [string]$Algorithm) {
							throw "simulated hash failure"
						}
						`
					}
					script := makeTestFile(t, injected)
					dir := filepath.Dir(script)
					source := filepath.Join(dir, "source.zip")
					if err := os.WriteFile(source, archive.Bytes(), 0o600); err != nil {
						t.Fatal(err)
					}
					asset := map[string]any{
						"env": map[string]string{"GOOS": "windows", "GOARCH": "amd64"},
						"url": source,
					}
					if tt.digest != nil {
						asset["sha256"] = tt.digest
					}
					manifest, err := json.Marshal(map[string]any{
						"version": "1.21.2-1",
						"arches": []any{
							map[string]any{
								"env":    map[string]string{"GOOS": "linux", "GOARCH": "amd64"},
								"url":    "https://example.invalid/unselected",
								"sha256": "invalid",
							},
							asset,
						},
					})
					if err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(dir, "assets.json"), manifest, 0o600); err != nil {
						t.Fatal(err)
					}

					installDir := filepath.Join(dir, "install")
					goPath := filepath.Join(installDir, "go1.21.2-1", "bin", "go")
					if tt.installed {
						if err := os.MkdirAll(filepath.Dir(goPath), 0o755); err != nil {
							t.Fatal(err)
						}
						if err := os.WriteFile(goPath, []byte("existing toolchain"), 0o600); err != nil {
							t.Fatal(err)
						}
					}
					archivePath := filepath.Join(dir, "download archive.zip")
					args := []string{
						"-NoProfile", "-File", script,
						"-Version", "go1.21.2-1",
						"-OS", "windows", "-Architecture", "amd64",
						"-InstallDir", installDir,
						"-ArchivePath", archivePath,
						"-NoPath",
					}
					if tt.keepArchive {
						args = append(args, "-KeepArchive")
					}
					if tt.dryRun {
						args = append(args, "-DryRun")
					}
					cmd := exec.Command(interpreter, args...)
					// Let each PowerShell edition find its own modules rather than inherit
					// a different edition's PSModulePath from the shell running go test.
					cmd.Env = append(os.Environ(), "PSModulePath=")
					out, err := cmd.CombinedOutput()
					output := string(out)
					if tt.wantError == "" {
						if err != nil {
							t.Fatalf("error running %v: %v; output:\n%s", cmd, err, output)
						}
					} else if err == nil || !strings.Contains(output, tt.wantError) {
						t.Fatalf("expected error containing %q, got %v; output:\n%s", tt.wantError, err, output)
					}
					if tt.wantError == "SHA-256 mismatch" {
						for _, want := range []string{"Expected: " + wrongDigest, "Actual: " + strings.ToUpper(digest)} {
							if !strings.Contains(output, want) {
								t.Errorf("missing %q in mismatch error:\n%s", want, output)
							}
						}
					}
					if got := strings.Contains(output, "Copying file from "); got != tt.wantDownload {
						t.Errorf("download attempted = %v, want %v; output:\n%s", got, tt.wantDownload, output)
					}
					wantExtract := tt.wantDownload && tt.wantError == ""
					if got := strings.Contains(output, "Extracting the archive."); got != wantExtract {
						t.Errorf("extraction attempted = %v, want %v; output:\n%s", got, wantExtract, output)
					}

					installed, err := os.ReadFile(goPath)
					if wantExtract || tt.installed {
						wantContent := toolchainContent
						if tt.installed {
							wantContent = "existing toolchain"
						}
						if err != nil || string(installed) != wantContent {
							t.Errorf("installed toolchain = %q, %v; want %q", installed, err, wantContent)
						}
					} else if !os.IsNotExist(err) {
						t.Errorf("expected no installed toolchain, got %q, %v", installed, err)
					}
					kept, err := os.ReadFile(archivePath)
					if tt.keepArchive && tt.wantDownload {
						if err != nil || !bytes.Equal(kept, archive.Bytes()) {
							t.Errorf("expected downloaded archive to be kept unchanged: %v", err)
						}
					} else if !os.IsNotExist(err) {
						t.Errorf("expected no downloaded archive, got error %v", err)
					}
				})
			}
		})
	}
}

func TestInstall(t *testing.T) {
	if !*download {
		t.Skip("skipping test that downloads Microsoft build of Go from the internet; use -download to run it")
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
