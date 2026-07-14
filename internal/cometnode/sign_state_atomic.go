package cometnode

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

const (
	signStateRenameAttempts = 24
	signStateInitialDelay   = 10 * time.Millisecond
	signStateMaximumDelay   = 250 * time.Millisecond
)

func writeSignStateAtomically(path string, contents []byte, mode os.FileMode) error {
	directory := filepath.Dir(path)
	info, err := os.Lstat(directory)
	if err != nil {
		return fmt.Errorf("inspect private validator state directory: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("private validator state directory must be a real directory")
	}
	file, err := os.CreateTemp(directory, ".priv-validator-state-*")
	if err != nil {
		return err
	}
	temporaryPath := file.Name()
	defer os.Remove(temporaryPath)
	if err := file.Chmod(mode); err != nil {
		_ = file.Close()
		return err
	}
	written, writeErr := file.Write(contents)
	if writeErr == nil && written != len(contents) {
		writeErr = io.ErrShortWrite
	}
	if writeErr == nil {
		writeErr = file.Sync()
	}
	closeErr := file.Close()
	if writeErr != nil {
		return writeErr
	}
	if closeErr != nil {
		return closeErr
	}
	if err := renameSignStateWithRetry(temporaryPath, path, os.Rename, isRetryableSignStateRename, time.Sleep); err != nil {
		return fmt.Errorf("replace private validator state: %w", err)
	}
	if err := syncSignStateDirectory(directory); err != nil {
		return fmt.Errorf("sync private validator state directory: %w", err)
	}
	return nil
}

func renameSignStateWithRetry(
	source, destination string,
	rename func(string, string) error,
	retryable func(error) bool,
	sleep func(time.Duration),
) error {
	delay := signStateInitialDelay
	var err error
	for attempt := 0; attempt < signStateRenameAttempts; attempt++ {
		err = rename(source, destination)
		if err == nil || !retryable(err) {
			return err
		}
		if attempt+1 == signStateRenameAttempts {
			break
		}
		sleep(delay)
		delay *= 2
		if delay > signStateMaximumDelay {
			delay = signStateMaximumDelay
		}
	}
	return err
}
