package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVersionWritesToStdout(t *testing.T) {
	var out, errOut bytes.Buffer
	code := run([]string{"version"}, &out, &errOut)
	if code != exitOK {
		t.Fatalf("exit code = %d, want %d", code, exitOK)
	}
	if !strings.HasPrefix(out.String(), "whatsapp-mcp ") || !strings.Contains(out.String(), "whatsmeow") {
		t.Fatalf("stdout = %q, want version line", out.String())
	}
	if errOut.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", errOut.String())
	}
}

func TestNoArgsAndUnknownCommandAreUsageErrors(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run(nil, &out, &errOut); code != exitUsage {
		t.Fatalf("no args: exit code = %d, want %d", code, exitUsage)
	}
	if out.Len() != 0 {
		t.Fatalf("no args: wrote to stdout: %q", out.String())
	}

	errOut.Reset()
	if code := run([]string{"bogus"}, &out, &errOut); code != exitUsage {
		t.Fatalf("unknown: exit code = %d, want %d", code, exitUsage)
	}
	if !strings.Contains(errOut.String(), `unknown command "bogus"`) {
		t.Fatalf("unknown: stderr = %q", errOut.String())
	}
}

func TestHelpWritesUsageToStderr(t *testing.T) {
	for _, arg := range []string{"help", "-h", "--help"} {
		var out, errOut bytes.Buffer
		if code := run([]string{arg}, &out, &errOut); code != exitOK {
			t.Errorf("%s: exit code = %d, want %d", arg, code, exitOK)
		}
		if out.Len() != 0 || !strings.Contains(errOut.String(), "usage:") {
			t.Errorf("%s: stdout=%q stderr=%q", arg, out.String(), errOut.String())
		}
	}
}

// TestServeFailsOnBadConfigWithoutTouchingStdout checks the serve subcommand's
// error path: a broken config stops it with a message on stderr only.
func TestServeFailsOnBadConfigWithoutTouchingStdout(t *testing.T) {
	home := t.TempDir()
	if err := os.Chmod(home, 0o700); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte("[privacy]\nbogus = 1\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("WHATSAPP_MCP_HOME", home)
	var errOut bytes.Buffer
	if code := serve(&errOut); code != exitFailure {
		t.Fatalf("exit code = %d, want %d", code, exitFailure)
	}
	if !strings.Contains(errOut.String(), "whatsapp-mcp serve:") {
		t.Fatalf("stderr = %q", errOut.String())
	}
}

// TestServeReportsHomeError checks that an unusable data directory is reported on stderr.
func TestServeReportsHomeError(t *testing.T) {
	file := filepath.Join(t.TempDir(), "arquivo")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	t.Setenv("WHATSAPP_MCP_HOME", filepath.Join(file, "sub"))
	var errOut bytes.Buffer
	if code := serve(&errOut); code != exitFailure {
		t.Fatalf("exit code = %d, want %d", code, exitFailure)
	}
	if !strings.Contains(errOut.String(), "whatsapp-mcp serve:") {
		t.Fatalf("stderr = %q", errOut.String())
	}
}
