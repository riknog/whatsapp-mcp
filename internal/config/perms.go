package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
)

// sensitiveFiles are the files in the data directory that hold account data or
// keys: the SQLite databases (and their WAL/SHM/journal sidecars) and the
// reference secret.
var sensitiveFiles = []string{"*.db", "*.db-wal", "*.db-shm", "*.db-journal", "ref.key"}

// CheckPermissions refuses data that other users could read. On Unix the
// directory must be 0700 and each sensitive file 0600. A missing directory or
// file is not an error. On Windows it does nothing, because file modes do not
// describe access there.
func CheckPermissions(home string) error {
	if runtime.GOOS == "windows" {
		return nil
	}

	var errs []error
	info, err := os.Stat(home)
	switch {
	case err == nil:
		if p := info.Mode().Perm(); p != 0o700 {
			errs = append(errs, fmt.Errorf("config: diretório %s tem permissões %04o; deve ser 0700 (rode: chmod 700 %q)", home, p, home))
		}
	case !errors.Is(err, fs.ErrNotExist):
		return fmt.Errorf("config: verificar %s: %w", home, err)
	default:
		return nil
	}

	entries, err := os.ReadDir(home)
	if err != nil {
		return fmt.Errorf("config: listar %s: %w", home, err)
	}
	for _, e := range entries {
		if e.IsDir() || !isSensitive(e.Name()) {
			continue
		}
		path := filepath.Join(home, e.Name())
		fi, err := e.Info()
		if err != nil {
			errs = append(errs, fmt.Errorf("config: verificar %s: %w", path, err))
			continue
		}
		if p := fi.Mode().Perm(); p != 0o600 {
			errs = append(errs, fmt.Errorf("config: arquivo %s tem permissões %04o; deve ser 0600 (rode: chmod 600 %q)", path, p, path))
		}
	}
	return errors.Join(errs...)
}

func isSensitive(name string) bool {
	for _, pattern := range sensitiveFiles {
		if ok, _ := filepath.Match(pattern, name); ok {
			return true
		}
	}
	return false
}
