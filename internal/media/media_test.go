package media

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/riknog/whatsapp-mcp/internal/config"
)

// The test binary doubles as the external programs: with MEDIA_HELPER set, it
// acts on its arguments and exits instead of running the tests.
func TestMain(m *testing.M) {
	if os.Getenv("MEDIA_HELPER") == "1" {
		os.Exit(helper(os.Args[1:]))
	}
	os.Exit(m.Run())
}

func helper(args []string) int {
	if len(args) == 0 {
		return 2
	}
	switch args[0] {
	case "-nostdin": // FFmpeg: copy -i <in> to the last argument, marked
		var in string
		for i, a := range args {
			if a == "-i" && i+1 < len(args) {
				in = args[i+1]
			}
		}
		b, err := os.ReadFile(in)
		if err != nil {
			return 1
		}
		if err := os.WriteFile(args[len(args)-1], append([]byte("WAV:"), b...), 0o600); err != nil {
			return 1
		}
		return 0
	case "cat": // print the file
		b, err := os.ReadFile(args[1])
		if err != nil {
			return 1
		}
		_, _ = os.Stderr.Write([]byte("log line\n"))
		return writeOut(string(b) + "\n\n  fim  \r\n")
	case "txt": // write a .txt into the directory, print progress
		if err := os.WriteFile(filepath.Join(args[1], "input.txt"), []byte(args[2]), 0o600); err != nil {
			return 1
		}
		return writeOut("[00:00.000 --> 00:01.000] progress\n")
	case "fail":
		_, _ = os.Stderr.Write([]byte("segredo 12345678909\n"))
		return 3
	case "sleep":
		time.Sleep(30 * time.Second)
		return 0
	}
	return 2
}

// writeOut writes to the process's standard output. syscall.Stdout is 1 on
// Unix and the console handle on Windows, where descriptor 1 does not exist.
func writeOut(s string) int {
	f := os.NewFile(uintptr(syscall.Stdout), "out")
	if _, err := f.WriteString(s); err != nil {
		return 1
	}
	return 0
}

func newExtractor(t *testing.T, mutate func(*config.MediaConfig)) *Extractor {
	t.Helper()
	t.Setenv("MEDIA_HELPER", "1")
	cfg := config.Defaults().Media
	cfg.Enabled = true
	cfg.TimeoutS = 20
	cfg.FFmpeg = os.Args[0]
	mutate(&cfg)
	return New(cfg, filepath.Join(t.TempDir(), "tmp"))
}

func self(args ...string) []string { return append([]string{os.Args[0]}, args...) }

func TestTranscribeReadsStdout(t *testing.T) {
	e := newExtractor(t, func(c *config.MediaConfig) { c.AudioCommand = self("cat", "{input}") })
	if !e.CanTranscribe() || e.CanOCR() {
		t.Fatalf("CanTranscribe=%v CanOCR=%v", e.CanTranscribe(), e.CanOCR())
	}
	got, err := e.Transcribe(context.Background(), []byte("bom\ndia"), "audio/ogg; codecs=opus")
	if err != nil || got != "bom dia fim" {
		t.Fatalf("Transcribe = %q, %v", got, err)
	}
	assertScratchEmpty(t, e)
}

func TestTranscribeConvertsToWav(t *testing.T) {
	e := newExtractor(t, func(c *config.MediaConfig) { c.AudioCommand = self("cat", "{wav}") })
	got, err := e.Transcribe(context.Background(), []byte("abc"), "audio/ogg")
	if err != nil || got != "WAV:abc fim" {
		t.Fatalf("Transcribe = %q, %v", got, err)
	}
}

func TestOutdirTextWinsOverProgress(t *testing.T) {
	e := newExtractor(t, func(c *config.MediaConfig) { c.OCRCommand = self("txt", "{outdir}", "placa ABC 1234") })
	got, err := e.OCR(context.Background(), []byte{0xff, 0xd8}, "image/jpeg")
	if err != nil || got != "placa ABC 1234" {
		t.Fatalf("OCR = %q, %v", got, err)
	}
}

func TestNotConfigured(t *testing.T) {
	e := newExtractor(t, func(*config.MediaConfig) {})
	if _, err := e.Transcribe(context.Background(), nil, "audio/ogg"); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("Transcribe = %v", err)
	}
	if _, err := e.OCR(context.Background(), nil, "image/png"); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("OCR = %v", err)
	}
}

func TestToolFailureHidesOutput(t *testing.T) {
	e := newExtractor(t, func(c *config.MediaConfig) { c.AudioCommand = self("fail") })
	_, err := e.Transcribe(context.Background(), []byte("x"), "audio/ogg")
	if !errors.Is(err, ErrTool) || !strings.Contains(err.Error(), "código de saída 3") {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), "12345678909") {
		t.Errorf("stderr leaked into the error: %v", err)
	}
	assertScratchEmpty(t, e)
}

func TestMissingProgram(t *testing.T) {
	e := newExtractor(t, func(c *config.MediaConfig) {
		c.OCRCommand = []string{filepath.Join(t.TempDir(), "nao-existe")}
	})
	_, err := e.OCR(context.Background(), []byte("x"), "image/png")
	if !errors.Is(err, ErrTool) || !strings.Contains(err.Error(), "não encontrado") {
		t.Fatalf("err = %v", err)
	}
}

func TestFFmpegFailureStops(t *testing.T) {
	e := newExtractor(t, func(c *config.MediaConfig) {
		c.FFmpeg = filepath.Join(t.TempDir(), "sem-ffmpeg")
		c.AudioCommand = self("cat", "{wav}")
	})
	if _, err := e.Transcribe(context.Background(), []byte("x"), "audio/ogg"); !errors.Is(err, ErrTool) {
		t.Fatalf("err = %v", err)
	}
}

func TestTimeout(t *testing.T) {
	e := newExtractor(t, func(c *config.MediaConfig) {
		c.TimeoutS = 1
		c.AudioCommand = self("sleep")
	})
	start := time.Now()
	_, err := e.Transcribe(context.Background(), []byte("x"), "audio/ogg")
	if !errors.Is(err, ErrTool) || !strings.Contains(err.Error(), "tempo limite") {
		t.Fatalf("err = %v", err)
	}
	if time.Since(start) > 15*time.Second {
		t.Errorf("timeout took %v", time.Since(start))
	}
}

func TestScratchCannotBeCreated(t *testing.T) {
	file := filepath.Join(t.TempDir(), "arquivo")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults().Media
	cfg.AudioCommand = self("cat", "{input}")
	e := New(cfg, filepath.Join(file, "sub"))
	if _, err := e.Transcribe(context.Background(), []byte("x"), "audio/ogg"); err == nil || errors.Is(err, ErrTool) {
		t.Fatalf("err = %v", err)
	}
}

func TestExtension(t *testing.T) {
	for in, want := range map[string]string{
		"audio/ogg; codecs=opus": ".ogg", "audio/mpeg": ".mp3", "audio/mp4": ".m4a", "audio/wav": ".wav",
		"image/jpeg": ".jpg", "IMAGE/PNG": ".png", "image/webp": ".webp", "image/gif": ".gif", "": ".bin",
	} {
		if got := extension(in); got != want {
			t.Errorf("extension(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCleanAndLimited(t *testing.T) {
	if got := clean([]byte("a\xffb\n\n c ")); got != "ab c" {
		t.Errorf("clean = %q", got)
	}
	if got := clean([]byte(strings.Repeat("a", maxOutput+10))); len(got) != maxOutput {
		t.Errorf("clean length = %d", len(got))
	}
	l := limited{max: 4}
	for _, s := range []string{"ab", "cde", "fg"} {
		if n, err := l.Write([]byte(s)); n != len(s) || err != nil {
			t.Fatalf("Write = %d, %v", n, err)
		}
	}
	if string(l.Bytes()) != "abcd" {
		t.Errorf("limited = %q", l.Bytes())
	}
	if got := exitText(fmt.Errorf("outro")); got != "não foi possível executar" {
		t.Errorf("exitText = %q", got)
	}
	if _, ok := readText(filepath.Join(t.TempDir(), "nada")); ok {
		t.Error("readText on missing dir")
	}
}

func assertScratchEmpty(t *testing.T, e *Extractor) {
	t.Helper()
	entries, err := os.ReadDir(e.scratch)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("scratch files left behind: %d", len(entries))
	}
}
