// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package releaseui

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
)

// OpenBrowser asks the operating system to open target in the user's default browser.
func OpenBrowser(target string) error {
	isWSL := os.Getenv("WSL_INTEROP") != "" || os.Getenv("WSL_DISTRO_NAME") != ""
	command, args, err := browserCommand(runtime.GOOS, isWSL, target)
	if err != nil {
		return err
	}
	if err := exec.Command(command, args...).Start(); err != nil {
		return fmt.Errorf("start browser command: %w", err)
	}
	return nil
}

func browserCommand(goos string, isWSL bool, target string) (string, []string, error) {
	switch goos {
	case "darwin":
		return "open", []string{target}, nil
	case "linux":
		if isWSL {
			return "rundll32.exe", []string{"url.dll,FileProtocolHandler", target}, nil
		}
		return "xdg-open", []string{target}, nil
	case "windows":
		return "rundll32", []string{"url.dll,FileProtocolHandler", target}, nil
	default:
		return "", nil, fmt.Errorf("opening a browser is unsupported on %s", goos)
	}
}
