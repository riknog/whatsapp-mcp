//go:build windows

package lock

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// errSharingViolation is ERROR_SHARING_VIOLATION: another handle has the file open.
const errSharingViolation syscall.Errno = 32

// openLocked opens the file with no sharing: while this handle is open, every
// other open of the file fails, which is the lock.
func openLocked(path string) (*os.File, error) {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, fmt.Errorf("lock: caminho: %w", err)
	}
	h, err := syscall.CreateFile(p, syscall.GENERIC_READ|syscall.GENERIC_WRITE, 0, nil,
		syscall.OPEN_ALWAYS, syscall.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		if errors.Is(err, errSharingViolation) {
			return nil, ErrBusy
		}
		return nil, fmt.Errorf("lock: abrir: %w", err)
	}
	return os.NewFile(uintptr(h), path), nil
}
