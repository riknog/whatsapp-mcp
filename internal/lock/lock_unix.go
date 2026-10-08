//go:build !windows

package lock

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

func openLocked(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600) // #nosec G304 -- path is serve.lock inside the data directory
	if err != nil {
		return nil, fmt.Errorf("lock: abrir: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil { // #nosec G115 -- fd fits in int
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrBusy
		}
		return nil, fmt.Errorf("lock: travar: %w", err)
	}
	return f, nil
}
