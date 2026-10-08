// Command whatsapp-mcp is a local MCP server for WhatsApp.
//
// stdout belongs to the MCP protocol. Only the "version" and "watch"
// subcommands write to stdout (watch's lines are read by Claude Code's Monitor
// tool or a hook); every other command writes to stderr.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"syscall"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/riknog/whatsapp-mcp/internal/config"
	"github.com/riknog/whatsapp-mcp/internal/lock"
	"github.com/riknog/whatsapp-mcp/internal/mcpserver"
	"github.com/riknog/whatsapp-mcp/internal/privacy"
)

// version is overridden at build time with -ldflags "-X main.version=...".
var version = "dev"

const (
	exitOK      = 0
	exitFailure = 1
	exitUsage   = 2
)

// command runs with the data directory open and writes to out (stderr).
type command func(ctx context.Context, a *app, out io.Writer, args []string) error

var commands = map[string]command{
	"login":  cmdLogin,
	"status": cmdStatus,
	"hide": func(ctx context.Context, a *app, out io.Writer, args []string) error {
		return cmdHide(ctx, a, out, args, true)
	},
	"unhide": func(ctx context.Context, a *app, out io.Writer, args []string) error {
		return cmdHide(ctx, a, out, args, false)
	},
	"hidden":    cmdHidden,
	"shareable": cmdShareable,
	"category":  cmdCategory,
	"purge":     cmdPurge,
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run executes the CLI and returns the process exit code. It takes its writers
// as parameters so tests can capture output.
func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		usage(stderr)
		return exitUsage
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "version":
		writeVersion(stdout)
		return exitOK
	case "help", "-h", "--help":
		usage(stderr)
		return exitOK
	case "serve":
		return serve(stderr)
	case "logout":
		return logout(rest, stderr)
	case "watch":
		return watch(rest, stdout, stderr)
	}
	fn, ok := commands[cmd]
	if !ok {
		fmt.Fprintf(stderr, "whatsapp-mcp: unknown command %q\n", cmd)
		usage(stderr)
		return exitUsage
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	a, err := openApp(ctx, stderr)
	if err != nil {
		return fail(stderr, cmd, err)
	}
	defer a.close()
	return fail(stderr, cmd, fn(ctx, a, stderr, rest))
}

// notifyContext ends on SIGINT or SIGTERM.
func notifyContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

// fail turns a command's error into the exit code, printing it first.
func fail(stderr io.Writer, cmd string, err error) int {
	switch {
	case err == nil:
		return exitOK
	case errors.Is(err, errUsage):
		usage(stderr)
		return exitUsage
	case errors.Is(err, errNotConfirmed):
		return exitFailure
	}
	report(stderr, cmd, err)
	return exitFailure
}

// logout runs apart from the other commands: --wipe deletes the files after
// the database and the lock are closed.
func logout(args []string, stderr io.Writer) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	a, err := openApp(ctx, stderr)
	if err != nil {
		return fail(stderr, "logout", err)
	}
	doWipe, err := cmdLogout(ctx, a, stderr, args)
	a.close()
	if err != nil {
		return fail(stderr, "logout", err)
	}
	if doWipe {
		return fail(stderr, "logout", wipe(a.home, stderr))
	}
	return exitOK
}

// serve runs the MCP server on stdin and stdout until the client disconnects or
// the process gets SIGINT or SIGTERM. stdout is the protocol channel here, so
// nothing else may be written to it.
func serve(stderr io.Writer) int {
	home, err := config.Home()
	if err != nil {
		fmt.Fprintf(stderr, "whatsapp-mcp serve: %s\n", privacy.RedactLog(err.Error()))
		return exitFailure
	}
	l, err := lock.Acquire(filepath.Join(home, lockFileName))
	if errors.Is(err, lock.ErrBusy) {
		fmt.Fprintf(stderr, "whatsapp-mcp serve: %s\n", errLocked)
		return exitFailure
	}
	if err != nil {
		fmt.Fprintf(stderr, "whatsapp-mcp serve: %s\n", privacy.RedactLog(err.Error()))
		return exitFailure
	}
	defer func() { _ = l.Release() }()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	err = mcpserver.Serve(ctx, mcpserver.ServeOptions{
		Home:      home,
		Version:   version,
		Transport: &mcp.StdioTransport{},
	})
	if err != nil {
		fmt.Fprintf(stderr, "whatsapp-mcp serve: %s\n", privacy.RedactLog(err.Error()))
		return exitFailure
	}
	return exitOK
}

// writeVersion prints the version, the commit and the whatsmeow version.
func writeVersion(w io.Writer) {
	commit, wm := "desconhecido", "desconhecido"
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, s := range bi.Settings {
			if s.Key == "vcs.revision" && len(s.Value) >= 12 {
				commit = s.Value[:12]
			}
		}
		for _, d := range bi.Deps {
			if d.Path == "go.mau.fi/whatsmeow" {
				wm = d.Version
			}
		}
	}
	fmt.Fprintf(w, "whatsapp-mcp %s (commit %s, whatsmeow %s)\n", version, commit, wm)
}

func usage(w io.Writer) {
	fmt.Fprint(w, `usage: whatsapp-mcp <command>

  login [--pair-phone 5511...]        vincula este computador (QR ou código)
  serve                               servidor MCP (usado pelo Claude)
  status                              sessão, conexão e dados guardados
  hide "Nome" | unhide "Nome"         oculta ou mostra uma conversa ao Claude
  hidden                              lista as conversas ocultas
  shareable add|remove "Nome"         contatos que o Claude pode compartilhar
  shareable list
  category add|remove "Cat" "Nome"    categorias locais
  category list
  purge --older-than 30d | --contact "Nome" | --all  [--yes]
  watch [--interval 5s] [--include-groups] [--once]
                                      avisa (no stdout) quando chega mensagem nova
  logout [--wipe]                     desvincula; --wipe apaga os dados
  version
`)
}
