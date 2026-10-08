package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	"github.com/riknog/whatsapp-mcp/internal/clock"

	_ "modernc.org/sqlite" // registers the "sqlite" driver (pure Go, no CGO)
)

// readPoolSize bounds concurrent readers. WAL lets them run beside the writer.
const readPoolSize = 4

// Store is the handle to data.db. Writes use w (one connection, serialized);
// reads use r (a small pool with query_only set).
type Store struct {
	w    *sql.DB
	r    *sql.DB
	clk  clock.Clock
	path string
}

// Open creates or opens data.db at path, applies pending migrations and returns
// a Store. The file is created with mode 0600 (and chmod 0600 if it already
// exists); its parent directory is created 0700 if missing. A nil clk means
// clock.Real.
func Open(ctx context.Context, path string, clk clock.Clock) (*Store, error) {
	if path == "" {
		return nil, errors.New("store: empty database path")
	}
	if clk == nil {
		clk = clock.Real{}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("store: criar diretório do banco: %w", err)
	}
	if err := ensureFileMode(path); err != nil {
		return nil, err
	}

	w, err := sql.Open("sqlite", dsn(path, false))
	if err != nil {
		return nil, fmt.Errorf("store: abrir escrita: %w", err)
	}
	w.SetMaxOpenConns(1)

	r, err := sql.Open("sqlite", dsn(path, true))
	if err != nil {
		_ = w.Close()
		return nil, fmt.Errorf("store: abrir leitura: %w", err)
	}
	r.SetMaxOpenConns(readPoolSize)

	s := &Store{w: w, r: r, clk: clk, path: path}
	if err := s.migrate(ctx); err != nil {
		_ = s.Close()
		return nil, fmt.Errorf("store: migrar: %w", err)
	}
	return s, nil
}

// Close releases both handles.
func (s *Store) Close() error {
	return errors.Join(s.r.Close(), s.w.Close())
}

// dsn builds the modernc DSN. The pragmas run on every new connection.
// The write handle uses BEGIN IMMEDIATE so that two processes never upgrade a
// read transaction into a write (which WAL refuses with SQLITE_BUSY at once).
func dsn(path string, readOnly bool) string {
	q := url.Values{}
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "foreign_keys(1)")
	// Freed pages are overwritten with zeros, so purged text does not linger in the file.
	q.Add("_pragma", "secure_delete(1)")
	q.Add("_pragma", "journal_mode(WAL)")
	if readOnly {
		q.Add("_pragma", "query_only(1)")
	} else {
		q.Set("_txlock", "immediate")
	}
	return path + "?" + q.Encode()
}

// ensureFileMode creates the database file with 0600 if missing and forces 0600
// if it exists. SQLite gives the -wal and -shm sidecars the same mode.
func ensureFileMode(path string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600) // #nosec G304 -- path is data.db inside the data directory
	if err != nil {
		return fmt.Errorf("store: criar arquivo do banco: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("store: fechar arquivo do banco: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("store: permissões do banco: %w", err)
	}
	return nil
}

// now returns the current Unix time in seconds.
func (s *Store) now() int64 { return s.clk.Now().Unix() }
