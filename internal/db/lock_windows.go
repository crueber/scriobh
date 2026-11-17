//go:build windows

package db

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

// Lock takes the data directory for this process by opening its lock file with no sharing: a second process's open
// fails with a sharing violation until this one closes the file or ends. It is the single-instance lock of the
// desktop build on Windows. The returned function releases it.
func Lock(dataDir string) (func(), error) {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, err
	}
	name, err := syscall.UTF16PtrFromString(filepath.Join(dataDir, "scriobh.lock"))
	if err != nil {
		return nil, err
	}
	h, err := syscall.CreateFile(name, syscall.GENERIC_READ|syscall.GENERIC_WRITE, 0, nil, syscall.OPEN_ALWAYS, syscall.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		const sharingViolation, lockViolation = syscall.Errno(32), syscall.Errno(33)
		if errors.Is(err, sharingViolation) || errors.Is(err, lockViolation) {
			return nil, &LockedError{Dir: dataDir}
		}
		return nil, err
	}
	return func() { _ = syscall.CloseHandle(h) }, nil
}
