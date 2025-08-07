# Microsoft Go PMC Setup Script

This directory contains a setup script to simplify the installation of Microsoft's build of Go on Ubuntu systems.

## Quick Start

Run the setup script with a single command:

```bash
/bin/bash -c "$(curl -fsSL https://raw.githubusercontent.com/microsoft/go-infra/HEAD/pmc/setup-pmc.sh)"
```

Or download and run locally:

```bash
wget https://raw.githubusercontent.com/microsoft/go-infra/HEAD/pmc/setup-pmc.sh
chmod +x setup-pmc.sh
./setup-pmc.sh
```

## What It Does

The `setup-pmc.sh` script automates the process of:

1. **System Validation**: Checks if you're running on a supported Ubuntu version
2. **Repository Setup**: Downloads and installs the Microsoft packages repository configuration
3. **Package List Update**: Automatically updates the apt package list
4. **Installation Instructions**: Provides the correct command to install Microsoft Go

## Features

### Smart Privilege Detection
The script automatically detects whether you're running as root or a regular user and:
- Uses `apt-get` commands directly when running as root
- Uses `sudo` when running as a regular user
- Provides appropriate installation commands based on your privileges

### Robust Error Handling
- Validates Ubuntu version compatibility
- Checks for required tools (wget/curl, sudo if needed)
- Detects if the repository is already configured
- Uses `set -eu` for strict error handling

### Download Flexibility
- Primary method: `wget`
- Fallback method: `curl`
- Clear error messages if neither is available

### Safe Execution
- Runs in a subshell to avoid affecting your current environment
- Automatically cleans up downloaded files
- Exits cleanly on errors or successful completion

## After Running the Script

Once the script completes successfully, you can install Microsoft Go:

```bash
# For regular users:
sudo apt-get install msft-golang

# For root users:
apt-get install msft-golang
```

## Manual Installation (Alternative)

If you prefer to set up the repository manually:

1. **Download the repository configuration:**
   ```bash
   wget https://packages.microsoft.com/config/ubuntu/$(. /etc/os-release; echo $VERSION_ID)/packages-microsoft-prod.deb
   ```

2. **Install the configuration:**
   ```bash
   sudo dpkg -i packages-microsoft-prod.deb
   ```

3. **Update package list:**
   ```bash
   sudo apt-get update
   ```

4. **Install Microsoft Go:**
   ```bash
   sudo apt-get install msft-golang
   ```

## Troubleshooting

### Unsupported Ubuntu Version
If you see "Unsupported Ubuntu release", you're running an Ubuntu version that doesn't have Microsoft Go packages available. Consider:
- Upgrading to Ubuntu 22.04 or 24.04
- Using alternative Go installation methods
- Building from source

### Repository Already Configured
If the script detects the repository is already set up, it will skip the configuration steps and provide the installation command.

### Missing Tools
The script requires either `wget` or `curl`. If neither is available:
```bash
# Install wget:
sudo apt-get install wget

# Or install curl:
sudo apt-get install curl
```

### Permission Issues
- The script handles both root and non-root execution
- For non-root users, `sudo` must be available and configured
- If running in restricted environments, run as root instead

## More Information

- **Microsoft Go Project**: https://github.com/microsoft/go
- **Package Repository**: https://packages.microsoft.com/
- **Go Documentation**: https://golang.org/doc/

## Script Details

The setup script (`setup-pmc.sh`):
- Is designed to be idempotent (safe to run multiple times)
- Uses Ubuntu's `/etc/os-release` for version detection (no `lsb_release` dependency)
- Implements comprehensive error checking and user feedback
- Follows shell scripting best practices with `set -eu`
