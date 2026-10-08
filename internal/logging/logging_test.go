package logging

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/riknog/whatsapp-mcp/internal/config"
)

// lockedBuffer is the stderr stand-in. The handler already serializes writes.
type lockedBuffer struct{ bytes.Buffer }

func newTestLogger(t *testing.T, level slog.Level) (*slog.Logger, *lockedBuffer, string) {
	t.Helper()
	home := t.TempDir()
	stderr := &lockedBuffer{}
	l, closer, err := newLogger(&config.Config{Home: home}, stderr, level)
	if err != nil {
		t.Fatalf("newLogger: %v", err)
	}
	t.Cleanup(func() { _ = closer.Close() })
	return l, stderr, filepath.Join(home, LogsDirName, LogFileName)
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestNewRequiresHome(t *testing.T) {
	if _, _, err := New(&config.Config{}); err == nil {
		t.Fatal("New accepted a config without Home")
	}
	if _, _, err := New(nil); err == nil {
		t.Fatal("New accepted nil config")
	}
}

func TestRecordsAreRedactedInFileAndStderr(t *testing.T) {
	l, stderr, path := newTestLogger(t, slog.LevelInfo)

	l.Info("enviado para 5511987654321@s.whatsapp.net")
	l.Info("ligar para +55 (11) 98765-4321 agora")
	l.Info("data 07/10/2026 19:30:00 total R$ 1.234,56")
	l.Info("attrs", "jid", "5511987654321@lid", "phone", "98765-4321", "count", 42)
	l.Info("anything", slog.Any("err", fmt.Errorf("falha com 5511987654321@s.whatsapp.net")))
	l.With("chat", "5511987654321@g.us").Warn("com with")
	l.WithGroup("req").Info("grupo", "to", "+5511987654321")
	l.Info("group attr", slog.Group("g", slog.String("who", "5511987654321@s.whatsapp.net")))

	for name, out := range map[string]string{"file": readFile(t, path), "stderr": stderr.String()} {
		for _, want := range []string{
			"enviado para <jid>",
			"ligar para <phone> agora",
			"data 07/10/2026 19:30:00 total R$ 1.234,56",
			`jid=<jid>`,
			`phone=<phone>`,
			"count=42",
			`err="falha com <jid>"`,
			`chat=<jid>`,
			"req.to=<phone>",
			"g.who=<jid>",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("%s: missing %q in:\n%s", name, want, out)
			}
		}
		if strings.Contains(out, "5511987654321") || strings.Contains(out, "98765-4321") || strings.Contains(out, "@s.whatsapp.net") {
			t.Errorf("%s leaks a phone or JID:\n%s", name, out)
		}
	}
}

func TestLevelFiltersDebug(t *testing.T) {
	l, stderr, path := newTestLogger(t, slog.LevelInfo)
	l.Debug("secret debug 5511987654321@s.whatsapp.net")
	if strings.Contains(stderr.String(), "debug") || strings.Contains(readFile(t, path), "debug") {
		t.Fatal("debug record written at info level")
	}
}

func TestLogFileAndDirPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file modes are not enforced on Windows")
	}
	l, _, path := newTestLogger(t, slog.LevelInfo)
	l.Info("hello")

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if p := fi.Mode().Perm(); p != 0o600 {
		t.Errorf("log file mode = %04o, want 0600", p)
	}
	di, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if p := di.Mode().Perm(); p != 0o700 {
		t.Errorf("logs dir mode = %04o, want 0700", p)
	}
}

// TestStdoutIsNeverWritten runs the logger in a child process and checks that
// its standard output stays empty. Standard output belongs to the MCP transport.
// The Makefile rejects the stdout symbol in every Go file, so this comment does
// not name it.
func TestStdoutIsNeverWritten(t *testing.T) {
	if os.Getenv("WHATSAPP_MCP_LOGGING_HELPER") == "1" {
		l, closer, err := New(&config.Config{Home: os.Getenv("WHATSAPP_MCP_LOGGING_HOME")})
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		l.Info("enviado para 5511987654321@s.whatsapp.net")
		l.With("module", "Client").Info("conectado 5511987654321@s.whatsapp.net")
		_ = closer.Close()
		// Exit before the testing framework prints PASS on stdout.
		os.Exit(0)
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestStdoutIsNeverWritten$")
	cmd.Env = append(os.Environ(),
		"WHATSAPP_MCP_LOGGING_HELPER=1",
		"WHATSAPP_MCP_LOGGING_HOME="+t.TempDir(),
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("helper process failed: %v\n%s", err, stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout must stay empty, got %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "enviado para <jid>") {
		t.Fatalf("expected redacted record on stderr, got %q", stderr.String())
	}
}

// TestModuleAttributeIsRedacted: whatsmeow records carry a module attribute and
// its text may hold JIDs; the root handler must redact both.
func TestModuleAttributeIsRedacted(t *testing.T) {
	l, stderr, path := newTestLogger(t, slog.LevelDebug)
	mod := l.With("module", "Client/Recv")

	mod.Warn("evento de 5511987654321@lid")
	l.With("module", "Client").Error("falha +55 (11) 98765-4321")

	out := readFile(t, path)
	for _, want := range []string{"evento de <jid>", "module=Client/Recv", "falha <phone>", "module=Client"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "5511987654321") || strings.Contains(out, "98765-4321") {
		t.Errorf("logger leaks PII:\n%s", out)
	}
	if stderr.String() != out {
		t.Errorf("stderr and file differ")
	}
}

func TestRotationKeepsThreeFilesWithinLimit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, LogFileName)
	r, err := openRotating(path, 100, 3)
	if err != nil {
		t.Fatal(err)
	}
	line := []byte(strings.Repeat("x", 39) + "\n") // 40 bytes
	for i := 0; i < 20; i++ {
		if _, err := r.Write(line); err != nil {
			t.Fatalf("Write %d: %v", i, err)
		}
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{LogFileName, LogFileName + ".1", LogFileName + ".2"} {
		fi, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("%s missing: %v", name, err)
		}
		if fi.Size() > 100 {
			t.Errorf("%s is %d bytes, over the 100-byte limit", name, fi.Size())
		}
	}
	if _, err := os.Stat(path + ".3"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a fourth file was kept: %v", err)
	}
}

func TestRotationWritesRecordLargerThanLimitWhole(t *testing.T) {
	path := filepath.Join(t.TempDir(), LogFileName)
	r, err := openRotating(path, 10, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	big := []byte(strings.Repeat("y", 50) + "\n")
	if _, err := r.Write([]byte("a\n")); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Write(big); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, path); got != string(big) {
		t.Fatalf("large record not written whole into a fresh file: %q", got)
	}
}

func TestRotatingFileErrors(t *testing.T) {
	if _, err := openRotating(filepath.Join(t.TempDir(), "x.log"), 10, 1); err == nil {
		t.Fatal("maxFiles 1 accepted")
	}
	if _, err := openRotating(filepath.Join(t.TempDir(), "missing-dir", "x.log"), 10, 3); err == nil {
		t.Fatal("open in a missing directory succeeded")
	}
	r, err := openRotating(filepath.Join(t.TempDir(), "x.log"), 10, 3)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("second Close must be a no-op: %v", err)
	}
	if _, err := r.Write([]byte("late\n")); !errors.Is(err, errRotatingClosed) {
		t.Fatalf("Write after Close = %v, want errRotatingClosed", err)
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("stderr gone") }

func TestTeeKeepsFileWhenStderrFails(t *testing.T) {
	var file bytes.Buffer
	tw := tee{a: &file, b: failingWriter{}}
	n, err := tw.Write([]byte("line\n"))
	if err == nil {
		t.Fatal("tee must report the stderr error")
	}
	if n != len("line\n") || file.String() != "line\n" {
		t.Fatalf("file did not get the record: n=%d file=%q", n, file.String())
	}
}

func TestNewReportsUnwritableHome(t *testing.T) {
	// A regular file where the logs directory should be makes MkdirAll fail.
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, LogsDirName), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := newLogger(&config.Config{Home: home}, &lockedBuffer{}, slog.LevelInfo); err == nil {
		t.Fatal("newLogger succeeded with a file in place of the logs directory")
	}
}

func TestNumericAttributesAreRedacted(t *testing.T) {
	l, stderr, path := newTestLogger(t, slog.LevelInfo)
	l.Info("nums",
		slog.Int64("n", 5511987654321),
		slog.Uint64("u", 5511987654321),
		slog.Float64("f", 5511987654321),
		slog.Int64("small", 42),
		slog.Float64("ratio", 1.5),
	)
	for name, out := range map[string]string{"file": readFile(t, path), "stderr": stderr.String()} {
		for _, want := range []string{"n=<phone>", "u=<phone>", "f=<phone>", "small=42", "ratio=1.5"} {
			if !strings.Contains(out, want) {
				t.Errorf("%s: missing %q in:\n%s", name, want, out)
			}
		}
		if strings.Contains(out, "5511987654321") {
			t.Errorf("%s leaks a numeric phone:\n%s", name, out)
		}
	}
}

func TestRotationKeepsNewestRecordsInOrderWithModes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, LogFileName)
	r, err := openRotating(path, 50, 3) // 5-byte lines: 10 per file
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 40; i++ {
		if _, err := r.Write([]byte(fmt.Sprintf("%04d\n", i))); err != nil {
			t.Fatalf("Write %d: %v", i, err)
		}
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}

	want := map[string]string{
		path:        "0030\n0031\n0032\n0033\n0034\n0035\n0036\n0037\n0038\n0039\n",
		path + ".1": "0020\n0021\n0022\n0023\n0024\n0025\n0026\n0027\n0028\n0029\n",
		path + ".2": "0010\n0011\n0012\n0013\n0014\n0015\n0016\n0017\n0018\n0019\n",
	}
	for p, content := range want {
		if got := readFile(t, p); got != content {
			t.Errorf("%s content:\n got %q\nwant %q", filepath.Base(p), got, content)
		}
		if runtime.GOOS == "windows" {
			continue
		}
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if mode := fi.Mode().Perm(); mode != 0o600 {
			t.Errorf("%s mode = %04o, want 0600", filepath.Base(p), mode)
		}
	}
}

func TestRotationFailureKeepsWriting(t *testing.T) {
	path := filepath.Join(t.TempDir(), LogFileName)
	r, err := openRotating(path, 10, 3)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if _, err := r.Write([]byte("a\n")); err != nil {
		t.Fatal(err)
	}
	// A non-empty directory where the oldest backup goes makes the shift fail.
	blocker := path + ".2"
	if err := os.Mkdir(blocker, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(blocker, "keep"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	n, err := r.Write([]byte("bbbbbbbb\n"))
	if err == nil {
		t.Fatal("Write must report the failed rotation")
	}
	if n != len("bbbbbbbb\n") {
		t.Fatalf("record not written: n=%d", n)
	}
	// Every later write still tries to rotate and fails the same way, so the
	// error is expected here; the record must still reach the file.
	_, _ = r.Write([]byte("cc\n"))
	got := readFile(t, path)
	if !strings.Contains(got, "a\nbbbbbbbb\n") || !strings.HasSuffix(got, "cc\n") {
		t.Fatalf("records lost after failed rotation: %q", got)
	}
}

func TestRotationConcurrentWritesStayWholeLines(t *testing.T) {
	for _, maxSize := range []int64{1 << 20, 200} {
		t.Run(fmt.Sprintf("max%d", maxSize), func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, LogFileName)
			r, err := openRotating(path, maxSize, 3)
			if err != nil {
				t.Fatal(err)
			}
			const goroutines, perG = 8, 100
			var wg sync.WaitGroup
			for g := 0; g < goroutines; g++ {
				wg.Add(1)
				go func(g int) {
					defer wg.Done()
					for i := 0; i < perG; i++ {
						if _, err := r.Write([]byte(fmt.Sprintf("g%d-%03d\n", g, i))); err != nil {
							t.Errorf("Write: %v", err)
							return
						}
					}
				}(g)
			}
			wg.Wait()
			if err := r.Close(); err != nil {
				t.Fatal(err)
			}

			total := 0
			for _, name := range []string{LogFileName, LogFileName + ".1", LogFileName + ".2"} {
				p := filepath.Join(dir, name)
				if _, err := os.Stat(p); err != nil {
					continue
				}
				for _, line := range strings.Split(strings.TrimSuffix(readFile(t, p), "\n"), "\n") {
					if !regexp.MustCompile(`^g\d-\d{3}$`).MatchString(line) {
						t.Fatalf("torn or corrupt line %q in %s", line, name)
					}
					total++
				}
			}
			if maxSize > 1000 && total != goroutines*perG {
				t.Fatalf("lines = %d, want %d (no rotation expected)", total, goroutines*perG)
			}
		})
	}
}

func TestOpenRotatingTightensExistingFileMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file modes are not enforced on Windows")
	}
	path := filepath.Join(t.TempDir(), LogFileName)
	if err := os.WriteFile(path, []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	r, err := openRotating(path, 1<<20, 3)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if mode := fi.Mode().Perm(); mode != 0o600 {
		t.Fatalf("existing file mode = %04o, want 0600", mode)
	}
}

func TestNewLoggerTightensExistingLogsDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file modes are not enforced on Windows")
	}
	home := t.TempDir()
	logs := filepath.Join(home, LogsDirName)
	if err := os.Mkdir(logs, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(logs, 0o755); err != nil {
		t.Fatal(err)
	}
	_, closer, err := newLogger(&config.Config{Home: home}, &lockedBuffer{}, slog.LevelInfo)
	if err != nil {
		t.Fatal(err)
	}
	defer closer.Close()
	fi, err := os.Stat(logs)
	if err != nil {
		t.Fatal(err)
	}
	if mode := fi.Mode().Perm(); mode != 0o700 {
		t.Fatalf("logs dir mode = %04o, want 0700", mode)
	}
}
