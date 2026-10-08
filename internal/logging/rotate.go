package logging

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sync"
)

// errRotatingClosed is returned by Write after Close.
var errRotatingClosed = errors.New("logging: arquivo de log fechado")

// rotatingFile is an io.WriteCloser that starts a new file when the current one
// would exceed maxSize. It keeps maxFiles files in total: the current file and
// maxFiles-1 numbered backups (path.1 is the newest backup).
type rotatingFile struct {
	mu       sync.Mutex
	path     string
	maxSize  int64
	maxFiles int
	f        *os.File // nil only after a failed open or rotation; Write retries
	size     int64
	closed   bool
}

// openRotating opens path for appending, creating it with mode 0600.
func openRotating(path string, maxSize int64, maxFiles int) (*rotatingFile, error) {
	if maxFiles < 2 {
		return nil, fmt.Errorf("logging: maxFiles debe ser >= 2 (recebido %d)", maxFiles)
	}
	r := &rotatingFile{path: path, maxSize: maxSize, maxFiles: maxFiles}
	if err := r.open(); err != nil {
		return nil, err
	}
	return r, nil
}

// open appends to the current file. A file that already existed may have a
// wider mode, so its mode is set to 0600 as well.
func (r *rotatingFile) open() error {
	f, err := os.OpenFile(r.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("logging: abrir %s: %w", r.path, err)
	}
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return fmt.Errorf("logging: permissões de %s: %w", r.path, err)
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return fmt.Errorf("logging: verificar %s: %w", r.path, err)
	}
	r.f = f
	r.size = info.Size()
	return nil
}

// Write appends p, rotating first if the write would exceed the size limit. A
// single record larger than the limit is written whole into an empty file. If
// rotation fails, the record is still written to the current file whenever that
// file can be reopened, and the rotation error is returned with the result.
func (r *rotatingFile) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return 0, errRotatingClosed
	}
	if r.f == nil {
		if err := r.open(); err != nil {
			return 0, err
		}
	}
	var rotErr error
	if r.size > 0 && r.size+int64(len(p)) > r.maxSize {
		rotErr = r.rotate()
		if r.f == nil {
			return 0, rotErr
		}
	}
	n, err := r.f.Write(p)
	r.size += int64(n)
	return n, errors.Join(rotErr, err)
}

// rotate closes the current file, shifts the backups up by one, and reopens
// path. Whatever part of the shift fails, path is reopened in append mode, so
// logging continues.
func (r *rotatingFile) rotate() error {
	var errs []error
	if err := r.f.Close(); err != nil {
		errs = append(errs, fmt.Errorf("logging: fechar %s: %w", r.path, err))
	}
	r.f = nil
	errs = append(errs, r.shift())
	if err := r.open(); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// shift drops the oldest backup, renames each backup to the next number, moves
// the current file to path.1, and tightens every backup to 0600.
func (r *rotatingFile) shift() error {
	if err := removeIfExists(r.backup(r.maxFiles - 1)); err != nil {
		return err
	}
	for i := r.maxFiles - 2; i >= 1; i-- {
		if err := renameIfExists(r.backup(i), r.backup(i+1)); err != nil {
			return err
		}
	}
	if err := renameIfExists(r.path, r.backup(1)); err != nil {
		return err
	}
	for i := 1; i < r.maxFiles; i++ {
		if err := chmodIfExists(r.backup(i), 0o600); err != nil {
			return err
		}
	}
	return nil
}

func (r *rotatingFile) backup(i int) string {
	return fmt.Sprintf("%s.%d", r.path, i)
}

// Close closes the file. Later writes return errRotatingClosed; a second Close is a no-op.
func (r *rotatingFile) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = true
	if r.f == nil {
		return nil
	}
	err := r.f.Close()
	r.f = nil
	return err
}

func removeIfExists(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("logging: remover %s: %w", path, err)
	}
	return nil
}

func renameIfExists(from, to string) error {
	if err := os.Rename(from, to); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("logging: renomear %s: %w", from, err)
	}
	return nil
}

func chmodIfExists(path string, mode os.FileMode) error {
	if err := os.Chmod(path, mode); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("logging: permissões de %s: %w", path, err)
	}
	return nil
}
