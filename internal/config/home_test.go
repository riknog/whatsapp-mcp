package config

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestResolveHomeUsesEnvAndCreates0700(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	got, err := resolveHome(dir, func() (string, error) { t.Fatal("userHomeDir must not be called"); return "", nil })
	if err != nil {
		t.Fatalf("resolveHome: %v", err)
	}
	if got != dir {
		t.Fatalf("got %q, want %q", got, dir)
	}
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(dir)
		if err != nil {
			t.Fatal(err)
		}
		if p := fi.Mode().Perm(); p != 0o700 {
			t.Fatalf("mode = %04o, want 0700", p)
		}
	}
}

func TestResolveHomeDefaultsUnderUserHome(t *testing.T) {
	userHome := t.TempDir()
	got, err := resolveHome("", func() (string, error) { return userHome, nil })
	if err != nil {
		t.Fatalf("resolveHome: %v", err)
	}
	want := filepath.Join(userHome, ".whatsapp-mcp")
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("default directory not created: %v", err)
	}
}

func TestResolveHomeUserHomeError(t *testing.T) {
	_, err := resolveHome("", func() (string, error) { return "", errors.New("no home") })
	if err == nil {
		t.Fatal("expected error when user home is unknown")
	}
}

func TestHomeReadsEnvVar(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(EnvHome, dir)
	got, err := Home()
	if err != nil {
		t.Fatalf("Home: %v", err)
	}
	if got != dir {
		t.Fatalf("Home() = %q, want %q", got, dir)
	}
}

func TestCheckPermissionsOK(t *testing.T) {
	skipOnWindows(t)
	home := privateHome(t)
	writeFile(t, filepath.Join(home, "session.db"), 0o600)
	writeFile(t, filepath.Join(home, "data.db"), 0o600)
	writeFile(t, filepath.Join(home, "data.db-wal"), 0o600)
	writeFile(t, filepath.Join(home, "ref.key"), 0o600)
	writeFile(t, filepath.Join(home, "notes.txt"), 0o644) // not sensitive
	if err := CheckPermissions(home); err != nil {
		t.Fatalf("CheckPermissions: %v", err)
	}
}

func TestCheckPermissionsRejectsOpenFiles(t *testing.T) {
	skipOnWindows(t)
	tests := []struct {
		name string
		file string
	}{
		{"database 0644", "session.db"},
		{"wal sidecar 0644", "data.db-wal"},
		{"reference key 0644", "ref.key"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			home := privateHome(t)
			writeFile(t, filepath.Join(home, tc.file), 0o644)
			err := CheckPermissions(home)
			if err == nil {
				t.Fatalf("CheckPermissions accepted %s with mode 0644", tc.file)
			}
			if !containsAll(err.Error(), tc.file, "chmod 600") {
				t.Fatalf("error should name the file and the fix: %v", err)
			}
		})
	}
}

func TestCheckPermissionsRejectsOpenDirectory(t *testing.T) {
	skipOnWindows(t)
	home := privateHome(t)
	if err := os.Chmod(home, 0o755); err != nil {
		t.Fatal(err)
	}
	err := CheckPermissions(home)
	if err == nil {
		t.Fatal("CheckPermissions accepted directory 0755")
	}
	if !containsAll(err.Error(), "chmod 700") {
		t.Fatalf("error should suggest chmod 700: %v", err)
	}
}

func TestCheckPermissionsMissingDirectoryIsNotAnError(t *testing.T) {
	skipOnWindows(t)
	if err := CheckPermissions(filepath.Join(t.TempDir(), "absent")); err != nil {
		t.Fatalf("missing directory: %v", err)
	}
}

func TestCheckPermissionsOnWindowsIsNoop(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows only")
	}
	home := t.TempDir()
	writeFile(t, filepath.Join(home, "session.db"), 0o644)
	if err := CheckPermissions(home); err != nil {
		t.Fatalf("Windows must be a no-op: %v", err)
	}
}

func skipOnWindows(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("file modes are not enforced on Windows")
	}
}

// privateHome returns a temp directory with mode 0700 (t.TempDir may be looser
// than the umask-independent mode we need).
func privateHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	if err := os.Chmod(home, 0o700); err != nil {
		t.Fatal(err)
	}
	return home
}

func writeFile(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte("x"), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func containsAll(s string, parts ...string) bool {
	for _, p := range parts {
		if !strings.Contains(s, p) {
			return false
		}
	}
	return true
}
