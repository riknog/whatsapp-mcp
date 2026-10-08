// Package media turns a downloaded audio into text with an external
// transcriber, and an image into text with an external OCR command. The
// programs are named in [media] of config.toml and run without a shell; the
// file goes to them through a scratch directory that is removed afterwards.
// What they print is returned to the caller and never written to stdout.
package media

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/riknog/whatsapp-mcp/internal/config"
)

// ErrNotConfigured means the command needed for this kind of media is not set.
var ErrNotConfigured = errors.New("media: comando não configurado")

// ErrTool means the transcriber, the OCR program or FFmpeg failed or ran out of time.
var ErrTool = errors.New("media: o comando externo falhou")

// maxOutput bounds what is kept of a command's output.
const maxOutput = 256 << 10

// Extractor runs the configured commands. The zero value is not usable; use New.
type Extractor struct {
	cfg     config.MediaConfig
	scratch string // parent of the per-call scratch directories
}

// New returns an Extractor that keeps its scratch files under scratch, which
// should be inside the data directory.
func New(cfg config.MediaConfig, scratch string) *Extractor {
	return &Extractor{cfg: cfg, scratch: scratch}
}

// CanTranscribe reports whether an audio command is set.
func (e *Extractor) CanTranscribe() bool { return len(e.cfg.AudioCommand) > 0 }

// CanOCR reports whether an OCR command is set.
func (e *Extractor) CanOCR() bool { return len(e.cfg.OCRCommand) > 0 }

// Transcribe returns the text spoken in an audio file.
func (e *Extractor) Transcribe(ctx context.Context, data []byte, mimetype string) (string, error) {
	return e.extract(ctx, e.cfg.AudioCommand, data, mimetype)
}

// OCR returns the text written in an image.
func (e *Extractor) OCR(ctx context.Context, data []byte, mimetype string) (string, error) {
	return e.extract(ctx, e.cfg.OCRCommand, data, mimetype)
}

func (e *Extractor) extract(ctx context.Context, command []string, data []byte, mimetype string) (string, error) {
	if len(command) == 0 {
		return "", ErrNotConfigured
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(e.cfg.TimeoutS)*time.Second)
	defer cancel()

	if err := os.MkdirAll(e.scratch, 0o700); err != nil { // #nosec G301 -- owner-only scratch directory
		return "", fmt.Errorf("media: criar pasta temporária: %w", err)
	}
	dir, err := os.MkdirTemp(e.scratch, "media-")
	if err != nil {
		return "", fmt.Errorf("media: criar pasta temporária: %w", err)
	}
	defer os.RemoveAll(dir)

	input := filepath.Join(dir, "input"+extension(mimetype))
	if err := os.WriteFile(input, data, 0o600); err != nil {
		return "", fmt.Errorf("media: gravar arquivo temporário: %w", err)
	}
	outdir := filepath.Join(dir, "out")
	if err := os.Mkdir(outdir, 0o700); err != nil {
		return "", fmt.Errorf("media: criar pasta temporária: %w", err)
	}
	wav := filepath.Join(dir, "audio.wav")
	if uses(command, "{wav}") {
		if _, err := run(ctx, e.cfg.FFmpeg, []string{"-nostdin", "-loglevel", "error", "-y",
			"-i", input, "-ar", "16000", "-ac", "1", wav}); err != nil {
			return "", err
		}
	}

	r := strings.NewReplacer("{input}", input, "{wav}", wav, "{outdir}", outdir)
	args := make([]string, 0, len(command)-1)
	for _, a := range command[1:] {
		args = append(args, r.Replace(a))
	}
	out, err := run(ctx, command[0], args)
	if err != nil {
		return "", err
	}
	// Transcribers such as whisper write a .txt into {outdir} and print progress
	// on stdout: the file wins when there is one.
	if uses(command, "{outdir}") {
		if text, ok := readText(outdir); ok {
			return clean(text), nil
		}
	}
	return clean(out), nil
}

// run executes name with args, without a shell, and returns its stdout.
func run(ctx context.Context, name string, args []string) ([]byte, error) {
	// #nosec G204 -- the program and its arguments come from the owner's config.toml;
	// only paths of our scratch files are substituted, and no shell is involved.
	cmd := exec.CommandContext(ctx, name, args...)
	var stdout, stderr limited
	stdout.max, stderr.max = maxOutput, 4<<10
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	cmd.Stdin = nil
	cmd.WaitDelay = 2 * time.Second
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("%w: %s excedeu o tempo limite", ErrTool, filepath.Base(name))
		}
		// stderr is not shown: it can repeat what the audio or image says.
		return nil, fmt.Errorf("%w: %s: %s", ErrTool, filepath.Base(name), exitText(err))
	}
	return stdout.Bytes(), nil
}

// exitText describes how a command ended without echoing its output.
func exitText(err error) string {
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return fmt.Sprintf("código de saída %d", exit.ExitCode())
	}
	if errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
		return "programa não encontrado"
	}
	return "não foi possível executar"
}

// readText returns the content of the .txt files in dir, by name.
func readText(dir string) ([]byte, bool) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, false
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.EqualFold(filepath.Ext(e.Name()), ".txt") {
			names = append(names, e.Name())
		}
	}
	if len(names) == 0 {
		return nil, false
	}
	sort.Strings(names)
	var buf bytes.Buffer
	for _, n := range names {
		b, err := os.ReadFile(filepath.Join(dir, n)) // #nosec G304 -- a file inside our scratch directory
		if err != nil {
			return nil, false
		}
		buf.Write(b)
		buf.WriteByte('\n')
		if buf.Len() > maxOutput {
			break
		}
	}
	return buf.Bytes(), true
}

// clean keeps valid UTF-8 and collapses runs of spaces and line breaks.
func clean(b []byte) string {
	if len(b) > maxOutput {
		b = b[:maxOutput]
	}
	return strings.Join(strings.Fields(strings.ToValidUTF8(string(b), "")), " ")
}

func uses(command []string, placeholder string) bool {
	for _, a := range command[1:] {
		if strings.Contains(a, placeholder) {
			return true
		}
	}
	return false
}

// extension picks a file name extension the external programs recognize.
func extension(mimetype string) string {
	base, _, _ := strings.Cut(strings.ToLower(mimetype), ";")
	switch strings.TrimSpace(base) {
	case "audio/ogg", "audio/opus":
		return ".ogg"
	case "audio/mpeg", "audio/mp3":
		return ".mp3"
	case "audio/mp4", "audio/m4a", "audio/aac", "audio/x-m4a":
		return ".m4a"
	case "audio/wav", "audio/x-wav":
		return ".wav"
	case "image/jpeg", "image/jpg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/webp":
		return ".webp"
	case "image/gif":
		return ".gif"
	}
	return ".bin"
}

// limited is a writer that keeps the first max bytes and drops the rest.
type limited struct {
	buf bytes.Buffer
	max int
}

func (l *limited) Write(p []byte) (int, error) {
	if room := l.max - l.buf.Len(); room > 0 {
		if len(p) > room {
			l.buf.Write(p[:room])
		} else {
			l.buf.Write(p)
		}
	}
	return len(p), nil
}

func (l *limited) Bytes() []byte { return l.buf.Bytes() }
