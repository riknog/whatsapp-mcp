// Package lock is an exclusive, process-wide lock on a file. The operating
// system releases it when the process ends, so a crash never leaves a stale lock.
//
// serve, login, logout and the connection test of status take the same lock:
// two connections with one WhatsApp session make the server drop one of them.
package lock

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// ErrBusy means another process holds the lock.
var ErrBusy = errors.New("lock: em uso por outro processo")

// Lock is a held lock. Release it with Release.
type Lock struct {
	f *os.File
}

// Acquire takes the lock on path without waiting. The file is created with
// mode 0600 (its directory 0700) and holds the PID of the owner, for humans.
func Acquire(path string) (*Lock, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("lock: criar diretório: %w", err)
	}
	f, err := openLocked(path)
	if err != nil {
		return nil, err
	}
	if err := f.Truncate(0); err == nil {
		_, _ = fmt.Fprintf(f, "%d\n", os.Getpid())
	}
	return &Lock{f: f}, nil
}

// Release frees the lock. The file stays, so that the next Acquire reuses it.
func (l *Lock) Release() error {
	if l == nil || l.f == nil {
		return nil
	}
	err := l.f.Close()
	l.f = nil
	return err
}
