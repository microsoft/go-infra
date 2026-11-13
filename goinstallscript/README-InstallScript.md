# go-install.ps1

[`go-install.ps1`](powershell/go-install.ps1) is a PowerShell script that installs the [Microsoft build of Go](https://github.com/microsoft/go) toolset.
The script works with Windows PowerShell and PowerShell (`pwsh`) and can install all [supported prebuilt Microsoft build of Go toolset platforms](https://github.com/microsoft/go?tab=readme-ov-file#download-and-install).
It installs the Microsoft build of Go toolset into a directory of your choice, or defaults to a directory in the user-specific data directory.

> [!TIP]
> Run `Get-Help -Detailed go-install.ps1` in a PowerShell terminal to see more information about the script's parameters and defaults.
>
> Alternatively, if you're using [`github.com/microsoft/go-infra/goinstallscript/cmd/goinstall`](README.md), run:
>
> ```
> go run github.com/microsoft/go-infra/goinstallscript/cmd/goinstall -help-script
> ```

The script is intended for use in CI/CD pipelines or to reproduce the results of those CI/CD pipelines locally.

We recommend using tools from the `github.com/microsoft/go-infra/goinstallscript` module described in [the module README](README.md) to set up the script in your repository and make sure it stays up to date.

## Archive verification

Before extracting a new download, the script requires a 64-character hexadecimal `sha256` digest in the selected asset's manifest entry and checks it against the archive.
Missing, malformed, or mismatched digests cause installation to fail.

When GnuPG (`gpg`) is available on `PATH`, the script also downloads the detached OpenPGP signature from `pgpSignatureUrl`, or from the archive URL with `.sig` appended when that field is absent or empty.
It verifies the signature against the Microsoft Managed Languages Compiler public key embedded in the script, using an isolated temporary keyring without the user's GnuPG configuration or automatic key retrieval.
The key's OpenPGP fingerprint is `B035FAC203AB9A45AAC802B4811005347B8E732C`; the published key is available at <https://aka.ms/managed-lang-compiler-public-key>.

If GnuPG is missing, the script warns and continues with SHA-256 verification only.
This checks integrity, not authenticity: an attacker able to replace both the manifest and archive can supply a matching digest.
Use **`-RequireSignature`** to fail before downloading the archive when GnuPG is missing.
When GnuPG is available, missing or invalid signatures and GnuPG errors always stop installation before extraction; the script never falls back to checksum-only verification after a failure.
This also means older unsigned releases, including older Windows archives such as `go1.21.2-1`, cannot be installed when GnuPG is available.

Signature verification authenticates the archive, not the remotely supplied version or platform metadata; it does not prevent substitution with another archive signed by the same key.
Dry runs and already-installed versions do not download or verify archives.
Temporary signature files and keyrings are removed; `-KeepArchive` retains only the downloaded archive, including on verification failure.

## Prerequisites

On non-Windows platforms, install [PowerShell (`pwsh`)](https://learn.microsoft.com/en-us/powershell/scripting/install/installing-powershell).

On Windows, either Windows PowerShell or PowerShell can be used.

For signature verification, install [GnuPG](https://gnupg.org/download/) and make sure `gpg` is on `PATH`.
GnuPG is required when using `-RequireSignature`; otherwise it is optional.

> [!NOTE]
> PowerShell was formerly known as PowerShell Core.
> Now [Windows PowerShell and PowerShell](https://learn.microsoft.com/en-us/powershell/scripting/what-is-windows-powershell) are the names used by Microsoft for these products.

## Usage

For any platform: run the script using the `pwsh` command:

```bash
pwsh ./go-install.ps1
```

If you're using Azure Pipelines, pass `-AzurePipelinePath` to make `go` commands work in future steps.

Pass `-h` to show help.

> [!NOTE]
> If you use a PowerShell terminal, you can choose to run the script directly:
>
> ```
> .\go-install.ps1
> ```
>
> Running the script directly has a benefit: it allows the script to change the terminal's `PATH` so the installed Go binary is then available in the current PowerShell session as `go`.
>
> Note that in typical CI/CD pipelines, each step is run in a fresh process and the `PATH` change will not be preserved in future steps.
> For that, use `-AzurePipelinePath` or preserve the `PATH` change in another way.
