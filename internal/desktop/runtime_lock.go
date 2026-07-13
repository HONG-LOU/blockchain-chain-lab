package desktop

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

var ErrAlreadyRunning = errors.New("desktop data directory is already managed by another process")

type runtimeLock struct {
	file *os.File
	once sync.Once
	err  error
}

func acquireRuntimeLock(root string) (*runtimeLock, error) {
	file, err := openAndLockRuntimeFile(filepath.Join(root, ".desktop.lock"))
	if err != nil {
		return nil, err
	}
	if err := file.Truncate(0); err != nil {
		_ = unlockAndCloseRuntimeFile(file)
		return nil, fmt.Errorf("truncate desktop lock: %w", err)
	}
	if _, err := fmt.Fprintf(file, "%d\n", os.Getpid()); err != nil {
		_ = unlockAndCloseRuntimeFile(file)
		return nil, fmt.Errorf("write desktop lock: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = unlockAndCloseRuntimeFile(file)
		return nil, fmt.Errorf("sync desktop lock: %w", err)
	}
	return &runtimeLock{file: file}, nil
}

func (lock *runtimeLock) Close() error {
	if lock == nil {
		return nil
	}
	lock.once.Do(func() { lock.err = unlockAndCloseRuntimeFile(lock.file) })
	return lock.err
}
