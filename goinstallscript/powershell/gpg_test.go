// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package powershell_test

import (
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const mockGpg = `
$script:TestGpgMissing = $false
$script:TestGpgImportExitCode = 0
$script:TestGpgExitCode = 0
$script:TestGpgStatus = "[GNUPG:] VALIDSIG B035FAC203AB9A45AAC802B4811005347B8E732C 2026-01-01 1767225600 0 4 0 1 8 00 B035FAC203AB9A45AAC802B4811005347B8E732C"
$env:GNUPGHOME = Join-Path $PSScriptRoot "existing keyring"
[void][System.IO.Directory]::CreateDirectory($env:GNUPGHOME)
[System.IO.File]::WriteAllText((Join-Path $env:GNUPGHOME "gpg.conf"), "existing configuration")
function Get-Command([string]$Name, [string]$CommandType, [string]$ErrorAction) {
	if ($Name -ne "gpg" -or $CommandType -ne "Application") {
		throw "Unexpected command lookup"
	}
	if (-not $script:TestGpgMissing) {
		return [pscustomobject]@{ Source = "Invoke-TestGpg" }
	}
}
function Invoke-TestGpg {
	$call = @{
		Arguments = @($args)
		Home = Get-Absolute-Path $args[[Array]::IndexOf($args, "--homedir") + 1]
	}
	[System.IO.File]::AppendAllText((Join-Path $PSScriptRoot "gpg-calls"), (ConvertTo-Json -Compress -InputObject $call) + [Environment]::NewLine)
	if ($args -contains "--import") {
		if ([System.IO.File]::ReadAllText($args[-1]) -cne (Get-MicrosoftGoSigningKey).PublicKey) {
			throw "Unexpected imported key"
		}
		$global:LASTEXITCODE = $script:TestGpgImportExitCode
		Write-Error "import diagnostic"
		return
	}
	if ($args -notcontains "--verify") {
		throw "Unexpected GnuPG command"
	}
	$global:LASTEXITCODE = $script:TestGpgExitCode
	Write-Error "verification diagnostic"
	if ((Get-Content -LiteralPath $args[-2] -Raw) -eq "untrusted signature") {
		Write-Output "[GNUPG:] VALIDSIG 0000000000000000000000000000000000000000 2026-01-01"
		return
	}
	Write-Output $script:TestGpgStatus
}
`

func checkGpgCalls(t *testing.T, dir string, want int) {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(dir, "gpg-calls"))
	if want == 0 {
		if !os.IsNotExist(err) {
			t.Errorf("unexpected GnuPG invocation: %s, %v", content, err)
		}
	} else {
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(strings.TrimSpace(string(content)), "\n")
		if len(lines) != want {
			t.Fatalf("GnuPG calls = %d, want %d: %s", len(lines), want, content)
		}
		var home string
		for i, line := range lines {
			var call struct {
				Arguments []string
				Home      string
			}
			if err := json.Unmarshal([]byte(line), &call); err != nil {
				t.Fatal(err)
			}
			args := call.Arguments
			for _, required := range []string{"--no-options", "--batch", "--no-tty", "--no-auto-key-retrieve", "--no-autostart", "--status-fd"} {
				if !slices.Contains(args, required) {
					t.Errorf("missing %s in GnuPG arguments: %q", required, args)
				}
			}
			pos := slices.Index(args, "--homedir")
			if pos < 0 || pos+1 >= len(args) {
				t.Fatalf("missing GnuPG home: %q", args)
			}
			if i == 0 {
				home = call.Home
				if !slices.Contains(args, "--import") {
					t.Errorf("first GnuPG call must import the pinned key: %q", args)
				}
			} else if call.Home != home || !slices.Contains(args, "--verify") {
				t.Errorf("verification must use the isolated keyring: %q", args)
			}
		}
		if home == filepath.Join(dir, "existing keyring") {
			t.Fatal("GnuPG used the user's keyring")
		}
		if _, err := os.Stat(home); !os.IsNotExist(err) {
			t.Errorf("temporary GnuPG home was not removed: %v", err)
		}
	}
	config, err := os.ReadFile(filepath.Join(dir, "existing keyring", "gpg.conf"))
	if err != nil || string(config) != "existing configuration" {
		t.Errorf("user GnuPG configuration changed: %q, %v", config, err)
	}
}

func TestMicrosoftGoSigningKey(t *testing.T) {
	for _, interpreter := range currentOSInterpreters(t) {
		t.Run(interpreter, func(t *testing.T) {
			t.Parallel()
			out := runTestFile(t, interpreter, makeTestFile(t, `
			Get-MicrosoftGoSigningKey | ConvertTo-Json | Write-Output
			exit
			`))
			var key struct {
				Fingerprint string
				PublicKey   string
			}
			if err := json.Unmarshal([]byte(out), &key); err != nil {
				t.Fatal(err)
			}
			_, encoded, ok := strings.Cut(strings.ReplaceAll(key.PublicKey, "\r\n", "\n"), "\n\n")
			if !ok {
				t.Fatal("missing public-key armor headers")
			}
			encoded, _, _ = strings.Cut(encoded, "\n=")
			packet, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(encoded, "\n", ""))
			if err != nil {
				t.Fatal(err)
			}
			if len(packet) < 3 || packet[0] != 0x99 {
				t.Fatal("expected a v4 public-key packet")
			}
			length := int(binary.BigEndian.Uint16(packet[1:3])) + 3
			if length > len(packet) {
				t.Fatal("truncated public-key packet")
			}
			const want = "B035FAC203AB9A45AAC802B4811005347B8E732C"
			if got := fmt.Sprintf("%X", sha1.Sum(packet[:length])); got != want || key.Fingerprint != want {
				t.Fatalf("pinned key fingerprints = %s, %s; want %s", got, key.Fingerprint, want)
			}
		})
	}
}

func TestVerifyArchiveSignature(t *testing.T) {
	for _, interpreter := range currentOSInterpreters(t) {
		t.Run(interpreter, func(t *testing.T) {
			for _, tt := range []struct {
				name      string
				setup     string
				wantCalls int
				wantError string
			}{
				{name: "valid", wantCalls: 2},
				{name: "import-error", setup: "$script:TestGpgImportExitCode = 2", wantCalls: 1, wantError: "import diagnostic"},
				{name: "verification-error-with-valid-status", setup: "$script:TestGpgExitCode = 1", wantCalls: 2, wantError: "verification diagnostic"},
				{name: "missing-status", setup: `$script:TestGpgStatus = "Good signature"`, wantCalls: 2, wantError: "not signed by the pinned Microsoft key"},
				{name: "wrong-signer", setup: `$script:TestGpgStatus = "[GNUPG:] VALIDSIG 0000000000000000000000000000000000000000 2026-01-01"`, wantCalls: 2, wantError: "not signed by the pinned Microsoft key"},
				{name: "fingerprint-prefix", setup: `$script:TestGpgStatus = "[GNUPG:] VALIDSIG B035FAC203AB9A45AAC802B4811005347B8E732CX 2026-01-01"`, wantCalls: 2, wantError: "not signed by the pinned Microsoft key"},
				{name: "not-a-status-line", setup: `$script:TestGpgStatus = "gpg: " + $script:TestGpgStatus`, wantCalls: 2, wantError: "not signed by the pinned Microsoft key"},
			} {
				t.Run(tt.name, func(t *testing.T) {
					t.Parallel()
					script := makeTestFile(t, mockGpg+"\n"+tt.setup+`
					trap {
						if ((Get-Location).Path -ne $location.Path) {
							Write-Output "Working directory changed"
							exit 1
						}
						Write-Output $_.Exception.Message
						exit 1
					}
					$signature = Join-Path $PSScriptRoot "detached signature"
					[System.IO.File]::WriteAllText($signature, "test signature")
					$location = Get-Location
					Verify-ArchiveSignature -ArchivePath (Join-Path $PSScriptRoot "archive [signed]") -SignaturePath $signature -GpgPath "Invoke-TestGpg"
					if ($ErrorActionPreference -ne "Stop") { throw "Error preference changed" }
					if ((Get-Location).Path -ne $location.Path) { throw "Working directory changed" }
					Write-Output "verified"
					exit
					`)
					out, err := testFileCommand(interpreter, script).CombinedOutput()
					if tt.wantError == "" {
						if err != nil || strings.TrimSpace(string(out)) != "verified" {
							t.Fatalf("verification failed: %v; output:\n%s", err, out)
						}
					} else if err == nil || !strings.Contains(string(out), tt.wantError) {
						t.Fatalf("expected error containing %q, got %v; output:\n%s", tt.wantError, err, out)
					}
					checkGpgCalls(t, filepath.Dir(script), tt.wantCalls)
				})
			}
		})
	}
}

func TestInvokeGpgExitCode(t *testing.T) {
	for _, interpreter := range currentOSInterpreters(t) {
		t.Run(interpreter, func(t *testing.T) {
			for _, code := range []int{0, 7} {
				t.Run(fmt.Sprint(code), func(t *testing.T) {
					t.Parallel()
					script := makeTestFile(t, fmt.Sprintf(`
					trap {
						Write-Output $_.Exception.Message
						exit 1
					}
					$executable = (Get-Command pwsh -CommandType Application | Select-Object -First 1).Source
					$result = Invoke-Gpg -GpgPath $executable -Arguments @(
						"-NoProfile", "-Command", "[Console]::Error.WriteLine('native diagnostic'); exit %d"
					)
					if ($result -notcontains "native diagnostic") { throw "Lost native diagnostics" }
					if ($ErrorActionPreference -ne "Stop") { throw "Error preference changed" }
					Write-Output "completed"
					exit
					`, code))
					out, err := testFileCommand(interpreter, script).CombinedOutput()
					if code == 0 {
						if err != nil || strings.TrimSpace(string(out)) != "completed" {
							t.Fatalf("native success was rejected: %v; output:\n%s", err, out)
						}
					} else if err == nil || !strings.Contains(string(out), "exit code '7'") || !strings.Contains(string(out), "native diagnostic") {
						t.Fatalf("native failure was not reported: %v; output:\n%s", err, out)
					}
				})
			}
		})
	}
}

func TestDryRunSwitchParameters(t *testing.T) {
	for _, interpreter := range currentOSInterpreters(t) {
		t.Run(interpreter, func(t *testing.T) {
			t.Parallel()
			out := runTestFile(t, interpreter, makeTestFile(t, `
			$ScriptName = "go-install.ps1"
			$SpecificVersion = "1.21.2-1"
			$InstallRoot = "install"
			$CLIOS = "windows"
			$CLIArchitecture = "amd64"
			function Show-Options([switch]$RequireSignature, [switch]$NoPath) {
				PrintDryRunOutput $MyInvocation
			}
			Show-Options -RequireSignature -NoPath:$false
			exit
			`))
			for _, want := range []string{"-RequireSignature:$True", "-NoPath:$False"} {
				if !strings.Contains(out, want) {
					t.Errorf("repeatable invocation is missing %q:\n%s", want, out)
				}
			}
		})
	}
}

func TestVerifyPublishedSignature(t *testing.T) {
	if !*download {
		t.Skip("use -download to verify a published Microsoft archive and reject tampering")
	}
	if _, err := exec.LookPath("gpg"); err != nil {
		t.Fatal("GnuPG must be on PATH for published-signature verification")
	}
	for _, interpreter := range currentOSInterpreters(t) {
		t.Run(interpreter, func(t *testing.T) {
			t.Parallel()
			out := runTestFile(t, interpreter, makeTestFile(t, `
			$gpg = (Get-Command gpg -CommandType Application | Select-Object -First 1).Source
			($asset, $specificVersion) = Get-AssetInformation "go1.21.2-1" "linux" "amd64"
			$archive = Join-Path $PSScriptRoot "archive.tar.gz"
			$signature = Join-Path $PSScriptRoot "archive.sig"
			DownloadFile -Source $asset.url -OutPath $archive
			DownloadFile -Source $asset.pgpSignatureUrl -OutPath $signature
			Verify-ArchiveSignature -ArchivePath $archive -SignaturePath $signature -GpgPath $gpg
			$stream = [System.IO.File]::Open($archive, [System.IO.FileMode]::Open, [System.IO.FileAccess]::ReadWrite)
			try {
				$first = $stream.ReadByte()
				$stream.Position = 0
				$stream.WriteByte($first -bxor 1)
			}
			finally {
				$stream.Dispose()
			}
			try {
				Verify-ArchiveSignature -ArchivePath $archive -SignaturePath $signature -GpgPath $gpg
			}
			catch {
				if ($_.Exception.Message.Contains("[GNUPG:] BADSIG ")) {
					Write-Output "tampering rejected"
					exit
				}
				throw
			}
			throw "Tampered archive was accepted"
			`))
			if !strings.Contains(out, "tampering rejected") {
				t.Fatalf("tampering was not rejected:\n%s", out)
			}
		})
	}
}
