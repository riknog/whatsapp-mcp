package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/riknog/whatsapp-mcp/internal/clock"
	"github.com/riknog/whatsapp-mcp/internal/toolerr"

	_ "modernc.org/sqlite"
)

// testStart is the fake clock's origin: 2026-10-07 12:00 UTC.
var testStart = time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)

func newTestStore(t testing.TB) (*Store, *clock.Fake) {
	t.Helper()
	clk := clock.NewFake(testStart)
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "data.db"), clk)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, clk
}

// TestFTS5Available fails fast if the modernc build has no FTS5.
func TestFTS5Available(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer db.Close()
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `CREATE VIRTUAL TABLE probe USING fts5(body)`); err != nil {
		t.Fatalf("FTS5 indisponível no build do modernc: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO probe (body) VALUES ('coração partido')`); err != nil {
		t.Fatalf("insert fts5: %v", err)
	}
	var n int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM probe WHERE probe MATCH '"coração"'`).Scan(&n); err != nil {
		t.Fatalf("match fts5: %v", err)
	}
	if n != 1 {
		t.Fatalf("MATCH retornou %d linhas, quero 1", n)
	}
}

func TestMigrationsFromZeroAndIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.db")
	ctx := context.Background()

	s, err := Open(ctx, path, nil)
	if err != nil {
		t.Fatalf("primeira abertura: %v", err)
	}
	v, err := s.SchemaVersion(ctx)
	if err != nil || v != 2 {
		t.Fatalf("SchemaVersion = %d, %v; quero 2", v, err)
	}
	if err := s.migrate(ctx); err != nil {
		t.Fatalf("migrate repetido: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	s2, err := Open(ctx, path, nil)
	if err != nil {
		t.Fatalf("segunda abertura: %v", err)
	}
	defer s2.Close()
	if v, _ := s2.SchemaVersion(ctx); v != 2 {
		t.Fatalf("versão após reabrir = %d, quero 2", v)
	}
}

func TestLoadMigrationsEmbedded(t *testing.T) {
	migs, err := loadMigrations()
	if err != nil {
		t.Fatalf("loadMigrations: %v", err)
	}
	if len(migs) != 2 || migs[0].name != "001_init.sql" || migs[0].version != 1 ||
		migs[1].name != "002_media.sql" || migs[1].version != 2 {
		t.Fatalf("migrations = %+v", migs)
	}
	if !strings.Contains(migs[0].sql, "messages_fts_ai") {
		t.Fatal("001_init.sql sem os triggers FTS")
	}
}

func TestSchemaHasExpectedObjects(t *testing.T) {
	s, _ := newTestStore(t)
	want := []string{
		"kv", "chats", "jid_aliases", "contacts", "messages", "messages_fts",
		"shareable_contacts", "labels", "chat_labels", "send_queue", "audit_log",
		"messages_fts_ai", "messages_fts_ad", "messages_fts_au",
		"messages_chat_ts_idx", "messages_ts_idx", "send_queue_status_idx", "chat_labels_label_idx",
	}
	for _, name := range want {
		var n int
		if err := s.r.QueryRowContext(context.Background(),
			`SELECT COUNT(*) FROM sqlite_master WHERE name = ?`, name).Scan(&n); err != nil {
			t.Fatalf("consulta %s: %v", name, err)
		}
		if n != 1 {
			t.Errorf("objeto %q ausente no schema", name)
		}
	}
}

func TestOpenRejectsBadPaths(t *testing.T) {
	ctx := context.Background()
	if _, err := Open(ctx, "", nil); err == nil {
		t.Error("caminho vazio aceito")
	}
	dir := t.TempDir()
	if _, err := Open(ctx, dir, nil); err == nil {
		t.Error("diretório aceito como arquivo de banco")
	}
}

func TestFilePermissions(t *testing.T) {
	ctx := context.Background()
	base := t.TempDir()
	dir := filepath.Join(base, "home")
	path := filepath.Join(dir, "data.db")

	s, err := Open(ctx, path, nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	if err := s.UpsertChat(ctx, Chat{JID: "x@s.whatsapp.net", Ref: "c_x", Kind: "direct"}); err != nil {
		t.Fatalf("UpsertChat: %v", err)
	}
	assertMode(t, dir, 0o700)
	assertMode(t, path, 0o600)
	// The WAL and shared-memory files exist while connections are open. Their mode is required.
	assertMode(t, path+"-wal", 0o600)
	assertMode(t, path+"-shm", 0o600)
}

func TestExistingFileIsForcedTo0600(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.db")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	s, err := Open(context.Background(), path, nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()
	assertMode(t, path, 0o600)
}

// assertMode checks a Unix mode. On Windows it only checks that the file
// exists: Perm() always reports 0666 or 0777 there.
func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat %s: %v", path, err)
	}
	if runtime.GOOS == "windows" {
		return
	}
	if got := fi.Mode().Perm(); got != want {
		t.Errorf("%s: permissões %04o, quero %04o", filepath.Base(path), got, want)
	}
}

func TestCanceledContextIsReturned(t *testing.T) {
	s, _ := newTestStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := s.Search(ctx, SearchFilter{Query: "oi"}); err == nil {
		t.Error("Search com contexto cancelado não retornou erro")
	}
	if err := s.UpsertChat(ctx, Chat{JID: "a", Ref: "c_a", Kind: "direct"}); err == nil {
		t.Error("UpsertChat com contexto cancelado não retornou erro")
	}
}

// TestConcurrentReadWrite: 10 goroutines write and read at once. Run with -race.
// The write handle is one connection, so there must be no "database is locked".
func TestConcurrentReadWrite(t *testing.T) {
	s, clk := newTestStore(t)
	ctx := context.Background()
	const workers, rounds = 10, 30

	var wg sync.WaitGroup
	errs := make(chan error, workers*rounds*4)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			chat := fmt.Sprintf("chat%02d@s.whatsapp.net", w)
			if err := s.UpsertChat(ctx, Chat{JID: chat, Ref: "c_" + chat, Kind: "direct"}); err != nil {
				errs <- err
				return
			}
			for i := 0; i < rounds; i++ {
				ts := clk.Now().Unix() + int64(i)
				m := Message{ChatJID: chat, ID: fmt.Sprintf("w%02d-%03d", w, i), TS: ts, Kind: "text", Text: "mensagem concorrente"}
				if _, err := s.InsertMessage(ctx, m); err != nil {
					errs <- fmt.Errorf("insert: %w", err)
				}
				if _, err := s.RecentMessages(ctx, chat, 5, 0); err != nil {
					errs <- fmt.Errorf("recent: %w", err)
				}
				if _, _, err := s.Search(ctx, SearchFilter{Query: "concorrente", Limit: 5}); err != nil {
					errs <- fmt.Errorf("search: %w", err)
				}
				if _, err := s.NewInbound(ctx, InboundFilter{}); err != nil {
					errs <- fmt.Errorf("inbound: %w", err)
				}
			}
		}(w)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if strings.Contains(err.Error(), "locked") {
			t.Errorf("database is locked: %v", err)
		} else {
			t.Errorf("erro concorrente: %v", err)
		}
	}
	var total int64
	if err := s.r.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages`).Scan(&total); err != nil {
		t.Fatalf("contar: %v", err)
	}
	if total != workers*rounds {
		t.Errorf("mensagens = %d, quero %d", total, workers*rounds)
	}
}

func TestToolerrCodeOnInvalidArgument(t *testing.T) {
	s, _ := newTestStore(t)
	_, _, err := s.Search(context.Background(), SearchFilter{Query: "   "})
	var te toolerr.Error
	if !errors.As(err, &te) || te.Code != toolerr.CodeInvalidArgument {
		t.Fatalf("erro = %v, quero invalid_argument", err)
	}
}

// TestBrokenMigrationRollsBack: a migration that fails halfway must leave no
// partial tables and must not bump schema_version.
func TestBrokenMigrationRollsBack(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	bad := migration{version: 3, name: "003_bad.sql", sql: `CREATE TABLE half (x INTEGER); THIS IS NOT SQL;`}
	if err := s.applyMigration(ctx, bad); err == nil {
		t.Fatal("migration inválida aceita")
	}
	var n int
	if err := s.r.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE name = 'half'`).Scan(&n); err != nil {
		t.Fatalf("consulta: %v", err)
	}
	if n != 0 {
		t.Error("tabela parcial sobreviveu ao rollback")
	}
	if v, _ := s.SchemaVersion(ctx); v != 2 {
		t.Errorf("versão = %d após falha, quero 2", v)
	}
	// An already-applied version is a no-op, even if its SQL is broken.
	if err := s.applyMigration(ctx, migration{version: 1, name: "001_init.sql", sql: "bogus"}); err != nil {
		t.Errorf("migration já aplicada não deveria executar: %v", err)
	}
}
