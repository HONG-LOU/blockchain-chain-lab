package node

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

const dataDirLockFileName = ".chainlab.lock"

var ErrDataDirLocked = errors.New("data directory is already locked by another node")
var ErrNodeClosed = errors.New("node is closed")

type dataDirLock struct {
	file *os.File
	once sync.Once
	err  error
}

func prepareDataDirectory(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve data directory: %w", err)
	}
	absolute = filepath.Clean(absolute)
	if err := ensureDurableDirectoryWithSync(absolute, 0o755, syncDirectory); err != nil {
		return "", err
	}
	return absolute, nil
}

func ensureDurableDirectoryWithSync(
	path string,
	mode os.FileMode,
	directorySync func(string) error,
) error {
	info, err := os.Lstat(path)
	if err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("data directory %s must not be a symbolic link", path)
		}
		if !info.IsDir() {
			return fmt.Errorf("data directory %s must be a directory", path)
		}
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect data directory %s: %w", path, err)
	}

	parent := filepath.Dir(path)
	if parent == path {
		return fmt.Errorf("data directory root %s does not exist", path)
	}
	if err := ensureDurableDirectoryWithSync(parent, mode, directorySync); err != nil {
		return err
	}
	if err := os.Mkdir(path, mode); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return fmt.Errorf("create data directory %s: %w", path, err)
		}
		info, statErr := os.Lstat(path)
		if statErr != nil {
			return fmt.Errorf("inspect concurrently created data directory %s: %w", path, statErr)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("concurrently created data directory %s is not a real directory", path)
		}
	}
	if err := directorySync(path); err != nil {
		return fmt.Errorf("%w after creating data directory %s: sync directory: %v", ErrAtomicCommitUncertain, path, err)
	}
	if err := directorySync(parent); err != nil {
		return fmt.Errorf("%w after creating data directory %s: sync parent: %v", ErrAtomicCommitUncertain, path, err)
	}
	return nil
}

func acquireDataDirLock(dataDir string) (*dataDirLock, error) {
	path := filepath.Join(dataDir, dataDirLockFileName)
	file, err := openAndLockDataDirFile(path)
	if err != nil {
		return nil, err
	}
	return &dataDirLock{file: file}, nil
}

func (lock *dataDirLock) Close() error {
	if lock == nil {
		return nil
	}
	lock.once.Do(func() {
		lock.err = unlockAndCloseDataDirFile(lock.file)
	})
	return lock.err
}
