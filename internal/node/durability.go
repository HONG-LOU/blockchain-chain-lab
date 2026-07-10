package node

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

var ErrAtomicCommitUncertain = errors.New("atomic file replacement commit is uncertain")

func writeFileAtomically(path string, contents []byte, mode os.FileMode) error {
	return writeFileAtomicallyWithDirectorySync(path, contents, mode, syncDirectory)
}

func readBoundedRegularFile(path string, label string, maxBytes int64) ([]byte, error) {
	file, err := openRegularFile(path, label)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if info.Size() < 0 || info.Size() > maxBytes {
		return nil, fmt.Errorf("%s exceeds %d bytes", label, maxBytes)
	}
	raw, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > maxBytes {
		return nil, fmt.Errorf("%s exceeds %d bytes", label, maxBytes)
	}
	return raw, nil
}

func writeFileAtomicallyWithDirectorySync(
	path string,
	contents []byte,
	mode os.FileMode,
	directorySync func(string) error,
) error {
	directory := filepath.Dir(path)
	info, err := os.Lstat(directory)
	if err != nil {
		return fmt.Errorf("inspect atomic write directory: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("atomic write directory must be a real directory")
	}
	file, err := os.CreateTemp(directory, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := file.Name()
	if err := file.Chmod(mode); err != nil {
		_ = file.Close()
		_ = os.Remove(tmp)
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
		_ = os.Remove(tmp)
		return writeErr
	}
	if closeErr != nil {
		_ = os.Remove(tmp)
		return closeErr
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := directorySync(directory); err != nil {
		return fmt.Errorf("%w after replacing %s: %v", ErrAtomicCommitUncertain, filepath.Base(path), err)
	}
	return nil
}
