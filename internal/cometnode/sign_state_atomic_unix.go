//go:build !windows

package cometnode

import "os"

func isRetryableSignStateRename(error) bool {
	return false
}

func syncSignStateDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
