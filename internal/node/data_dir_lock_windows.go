//go:build windows

package node

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

const dataDirLockBytes = uint32(0xffffffff)

func openAndLockDataDirFile(path string) (*os.File, error) {
	pathPointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, fmt.Errorf("encode data directory lock path: %w", err)
	}
	handle, err := windows.CreateFile(
		pathPointer,
		windows.GENERIC_READ|windows.GENERIC_WRITE,
		0,
		nil,
		windows.OPEN_ALWAYS,
		windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		if errors.Is(err, windows.ERROR_SHARING_VIOLATION) || errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
			return nil, fmt.Errorf("%w: %s", ErrDataDirLocked, path)
		}
		return nil, fmt.Errorf("open data directory lock: %w", err)
	}
	file := os.NewFile(uintptr(handle), path)
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("inspect data directory lock: %w", err)
	}
	if info.FileAttributes&(windows.FILE_ATTRIBUTE_DIRECTORY|windows.FILE_ATTRIBUTE_REPARSE_POINT) != 0 {
		_ = file.Close()
		return nil, errors.New("data directory lock path must be a regular file")
	}
	var overlapped windows.Overlapped
	if err := windows.LockFileEx(
		handle,
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0,
		dataDirLockBytes,
		dataDirLockBytes,
		&overlapped,
	); err != nil {
		_ = file.Close()
		if errors.Is(err, windows.ERROR_LOCK_VIOLATION) || errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
			return nil, fmt.Errorf("%w: %s", ErrDataDirLocked, path)
		}
		return nil, fmt.Errorf("lock data directory: %w", err)
	}
	return file, nil
}

func unlockAndCloseDataDirFile(file *os.File) error {
	if file == nil {
		return nil
	}
	var overlapped windows.Overlapped
	unlockErr := windows.UnlockFileEx(
		windows.Handle(file.Fd()),
		0,
		dataDirLockBytes,
		dataDirLockBytes,
		&overlapped,
	)
	closeErr := file.Close()
	return errors.Join(unlockErr, closeErr)
}
