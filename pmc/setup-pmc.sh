#!/bin/bash

# Microsoft build of Go PMC Setup Script
# This script sets up the Microsoft packages repository for Ubuntu systems
# to enable installation of Microsoft build of Go packages.

(
  set -eu
  
  # Function to show the appropriate install command based on user privileges
  show_install_command() {
    if [ "$(id -u)" = "0" ]; then
      echo "You can now install Microsoft build of Go with: apt-get install msft-golang"
    else
      echo "You can now install Microsoft build of Go with: sudo apt-get install msft-golang"
    fi
  }
  
  # Check if we're running on Ubuntu
  if [ ! -f /etc/os-release ]; then
    echo "Error: /etc/os-release not found. This script only supports Ubuntu systems." >&2
    exit 1
  fi
  
  # Get Ubuntu version without using lsb_release
  # shellcheck disable=SC1091
  ubuntu_release=$(. /etc/os-release; echo "$VERSION_ID")
  supported_versions='22.04 24.04'
  
  echo "Detected Ubuntu version: $ubuntu_release"
  echo "Checking if this version is supported..."
  
  # Check if the current Ubuntu version is supported
  version_supported=false
  for v in $supported_versions; do
    if [ "$v" = "$ubuntu_release" ]; then
      version_supported=true
      break
    fi
  done
  
  if [ "$version_supported" = false ]; then
    echo "Unsupported Ubuntu release: $ubuntu_release. Supported: $supported_versions. Consider using a different install method." >&2
    exit 1
  fi
  
  echo "Ubuntu $ubuntu_release is supported. Setting up Microsoft packages repository..."
  
  # Check if packages.microsoft.com repository is already configured
  if [ -f /etc/apt/sources.list.d/microsoft-prod.list ] || dpkg -l | grep -q packages-microsoft-prod; then
    echo "Microsoft packages repository is already configured."
    show_install_command
    exit 0
  fi
  
  # Download and install the Microsoft repository configuration
  echo "Downloading Microsoft repository configuration..."
  
  # Try wget first, fallback to curl if not available
  if command -v wget >/dev/null 2>&1; then
    wget "https://packages.microsoft.com/config/ubuntu/${ubuntu_release}/packages-microsoft-prod.deb" -O packages-microsoft-prod.deb
  elif command -v curl >/dev/null 2>&1; then
    curl -fsSL "https://packages.microsoft.com/config/ubuntu/${ubuntu_release}/packages-microsoft-prod.deb" -o packages-microsoft-prod.deb
  else
    echo "Error: Neither wget nor curl is available. Please install one of them and try again." >&2
    exit 1
  fi
  
  echo "Installing Microsoft repository configuration..."
  
  # Check if running as root (no sudo needed)
  if [ "$(id -u)" = "0" ]; then
    dpkg -i packages-microsoft-prod.deb
  else
    if ! command -v sudo >/dev/null 2>&1; then
      echo "Error: sudo is required but not available. Please run as root or install sudo." >&2
      exit 1
    fi
    sudo dpkg -i packages-microsoft-prod.deb
  fi
  
  # Clean up the downloaded package
  rm -f packages-microsoft-prod.deb
  
  echo "Updating package list..."
  
  # Update package list using the same privilege escalation logic
  if [ "$(id -u)" = "0" ]; then
    apt-get update
  else
    sudo apt-get update
  fi
  
  echo ""
  echo "✓ Microsoft packages repository has been successfully configured and package list updated!"
  echo ""
  echo "Getting Started:"
  show_install_command
  echo ""
  echo "For more information, visit: https://github.com/microsoft/go"
  
  exit 0
)
