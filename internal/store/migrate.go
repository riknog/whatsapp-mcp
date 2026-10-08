package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strconv"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// migrationName matches NNN_name.sql, for example 001_init.sql.
var migrationName = regexp.MustCompile(`^(\d{3})_[a-z0-9_]+\.sql$`)

type migration struct {
	version int
	name    string
	sql     string
}

// loadMigrations reads the embedded files in version order. Versions must start
// at 1 and be contiguous, so a gap or a duplicate is a build-time bug.
func loadMigrations() ([]migration, error) {
	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("listar migrations: %w", err)
	}
	var out []migration
	for _, e := range entries {
		m := migrationName.FindStringSubmatch(e.Name())
		if m == nil {
			return nil, fmt.Errorf("nome de migration inválido: %s", e.Name())
		}
		v, err := strconv.Atoi(m[1])
		if err != nil {
			return nil, fmt.Errorf("versão de %s: %w", e.Name(), err)
		}
		body, err := migrationFS.ReadFile("migrations/" + e.Name())
		if err != nil {
			return nil, fmt.Errorf("ler %s: %w", e.Name(), err)
		}
		out = append(out, migration{version: v, name: e.Name(), sql: string(body)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })
	for i, m := range out {
		if m.version != i+1 {
			return nil, fmt.Errorf("migrations fora de sequência em %s (esperado %03d)", m.name, i+1)
		}
	}
	return out, nil
}

// migrate applies every migration above the stored schema_version. Each one
// runs with its version bump in one transaction. The version is read inside
// that transaction, so a second process that starts at the same time skips work
// that is already done.
func (s *Store) migrate(ctx context.Context) error {
	migs, err := loadMigrations()
	if err != nil {
		return err
	}
	for _, m := range migs {
		if err := s.applyMigration(ctx, m); err != nil {
			return fmt.Errorf("%s: %w", m.name, err)
		}
	}
	return nil
}

func (s *Store) applyMigration(ctx context.Context, m migration) error {
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	current, err := schemaVersion(ctx, tx)
	if err != nil {
		return err
	}
	if current >= m.version {
		return nil // already applied; the deferred Rollback releases the lock
	}
	if _, err := tx.ExecContext(ctx, m.sql); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO kv (key, value) VALUES ('schema_version', ?)
		 ON CONFLICT (key) DO UPDATE SET value = excluded.value`,
		strconv.Itoa(m.version)); err != nil {
		return err
	}
	return tx.Commit()
}

// schemaVersion returns 0 when the kv table does not exist yet.
func schemaVersion(ctx context.Context, tx *sql.Tx) (int, error) {
	var n int
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'kv'`).Scan(&n); err != nil {
		return 0, err
	}
	if n == 0 {
		return 0, nil
	}
	var v string
	err := tx.QueryRowContext(ctx, `SELECT value FROM kv WHERE key = 'schema_version'`).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(v)
}

// SchemaVersion reports the applied schema version.
func (s *Store) SchemaVersion(ctx context.Context) (int, error) {
	var v string
	err := s.r.QueryRowContext(ctx, `SELECT value FROM kv WHERE key = 'schema_version'`).Scan(&v)
	if err != nil {
		return 0, fmt.Errorf("store: ler versão: %w", err)
	}
	return strconv.Atoi(v)
}
