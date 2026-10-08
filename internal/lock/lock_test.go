package lock

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func TestSecondAcquireIsBusyUntilRelease(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "serve.lock")
	l, err := Acquire(path)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if _, err := Acquire(path); !errors.Is(err, ErrBusy) {
		t.Fatalf("second Acquire = %v, want ErrBusy", err)
	}
	if err := l.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
	l2, err := Acquire(path)
	if err != nil {
		t.Fatalf("Acquire after Release: %v", err)
	}
	defer func() { _ = l2.Release() }()
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o600 {
			t.Errorf("mode = %04o, want 0600", fi.Mode().Perm())
		}
	}
}

func TestReleaseTwiceAndNil(t *testing.T) {
	l, err := Acquire(filepath.Join(t.TempDir(), "x.lock"))
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Release(); err != nil {
		t.Fatal(err)
	}
	if err := l.Release(); err != nil {
		t.Fatalf("second Release: %v", err)
	}
	var nilLock *Lock
	if err := nilLock.Release(); err != nil {
		t.Fatalf("nil Release: %v", err)
	}
}

// TestLockHeldByAnotherProcess runs this test binary as a helper that takes the
// lock, then checks that this process sees it busy.
func TestLockHeldByAnotherProcess(t *testing.T) {
	if p := os.Getenv("LOCK_HELPER_PATH"); p != "" {
		l, err := Acquire(p)
		if err != nil {
			os.Exit(3)
		}
		_, _ = os.Stderr.WriteString("held\n")
		buf := make([]byte, 1)
		_, _ = os.Stdin.Read(buf) // hold until the parent closes stdin
		_ = l.Release()
		os.Exit(0)
	}
	path := filepath.Join(t.TempDir(), "serve.lock")
	cmd := exec.Command(os.Args[0], "-test.run=^TestLockHeldByAnotherProcess$")
	cmd.Env = append(os.Environ(), "LOCK_HELPER_PATH="+path)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 5)
	if _, err := out.Read(buf); err != nil || string(buf) != "held\n" {
		t.Fatalf("helper did not take the lock: %q %v", buf, err)
	}
	if _, err := Acquire(path); !errors.Is(err, ErrBusy) {
		t.Errorf("Acquire while helper holds it = %v, want ErrBusy", err)
	}
	_ = stdin.Close()
	if err := cmd.Wait(); err != nil {
		t.Fatalf("helper: %v", err)
	}
	l, err := Acquire(path)
	if err != nil {
		t.Fatalf("Acquire after helper exit: %v", err)
	}
	_ = l.Release()
}
