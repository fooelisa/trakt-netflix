// Package pathutil provides utility functions for working with file paths.
package pathutil

import (
	"os"
)

// ConfigDir returns the path to the config directory
func ConfigDir() string {
	// Explicit override. Needed on container runtimes that don't create
	// /.dockerenv (containerd, CRI-O), for example Kubernetes.
	if dir := os.Getenv("CONFIG_DIR"); dir != "" {
		return dir
	}

	_, err := os.Stat("/.dockerenv")
	if err == nil {
		return "/config"
	}

	// fallback on current directory
	return ""
}
