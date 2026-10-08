package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/riknog/whatsapp-mcp/internal/clock"
	"github.com/riknog/whatsapp-mcp/internal/ingest"
	"github.com/riknog/whatsapp-mcp/internal/lock"
	"github.com/riknog/whatsapp-mcp/internal/logging"
	"github.com/riknog/whatsapp-mcp/internal/wa"
)

// statusConnectTimeout is how long status waits for the connection test.
const statusConnectTimeout = 10 * time.Second

// session is the part of the WhatsApp adapter that the CLI uses. Tests replace
// openSession with a fake; production opens session.db with wa.Open.
type session interface {
	Paired() bool
	Login(ctx context.Context, out io.Writer, pairPhone string, sink wa.Sink) error
	CheckConnection(ctx context.Context, timeout time.Duration) bool
	Logout(ctx context.Context) error
	Events() <-chan any
	Close() error
}

var openSession = func(ctx context.Context, a *app) (session, error) {
	return a.openWA(ctx)
}

// withSession takes the lock, opens session.db and writes the events that
// arrive meanwhile to data.db. fn runs while all of that is held.
func withSession(ctx context.Context, a *app, fn func(ctx context.Context, s session) error) error {
	l, err := a.lock()
	if errors.Is(err, lock.ErrBusy) {
		return errBusy
	}
	if err != nil {
		return err
	}
	defer func() { _ = l.Release() }()
	s, err := openSession(ctx, a)
	if err != nil {
		return err
	}
	pumpCtx, stop := context.WithCancel(ctx)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); a.pump(pumpCtx, s.Events()) }()
	err = fn(ctx, s)
	stop()
	wg.Wait()
	if cerr := s.Close(); err == nil && cerr != nil {
		err = cerr
	}
	return err
}

// cmdLogin pairs this computer as a WhatsApp device (QR code or pairing code).
func cmdLogin(ctx context.Context, a *app, out io.Writer, args []string) error {
	fs := flag.NewFlagSet("login", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	pairPhone := fs.String("pair-phone", "", "")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		return errUsage
	}
	phone := strings.NewReplacer("+", "", " ", "", "-", "", "(", "", ")", "").Replace(*pairPhone)
	return withSession(ctx, a, func(ctx context.Context, s session) error {
		if s.Paired() {
			fmt.Fprintln(out, "Já existe uma sessão vinculada. Para trocar de conta: whatsapp-mcp logout e depois login.")
			return nil
		}
		if phone == "" {
			fmt.Fprintln(out, "No celular: WhatsApp > Aparelhos conectados > Conectar um aparelho, e leia o QR abaixo.")
		}
		in := ingest.New(a.st, a.refs, clock.Real{}, a.log)
		if err := s.Login(ctx, out, phone, in.Handle); err != nil {
			return err
		}
		a.audit(ctx, "login", "", "cli")
		fmt.Fprintln(out, "Pronto: sessão vinculada. Configure o Claude (veja o README) e ele usa o whatsapp-mcp serve.")
		return nil
	})
}

// cmdStatus shows the session, the connection and the size of the local data.
// The connection test runs only when serve is not running.
func cmdStatus(ctx context.Context, a *app, out io.Writer, args []string) error {
	if len(args) != 0 {
		return errUsage
	}
	fmt.Fprintf(out, "Diretório de dados: %s\n", a.home)
	err := withSession(ctx, a, func(ctx context.Context, s session) error {
		if !s.Paired() {
			fmt.Fprintln(out, "Sessão do WhatsApp: não vinculada (rode: whatsapp-mcp login)")
			return nil
		}
		fmt.Fprintln(out, "Sessão do WhatsApp: vinculada")
		if s.CheckConnection(ctx, statusConnectTimeout) {
			fmt.Fprintln(out, "Conexão: ok")
		} else {
			fmt.Fprintln(out, "Conexão: falhou (sem internet, ou o aparelho foi removido no celular)")
		}
		return nil
	})
	if errors.Is(err, errBusy) {
		fmt.Fprintln(out, "Sessão do WhatsApp: em uso pelo serve (o Claude está aberto); conexão não testada")
	} else if err != nil {
		return err
	}
	t, err := a.st.Totals(ctx)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "Conversas: %d (%d ocultas)\n", t.Chats, t.Hidden)
	fmt.Fprintf(out, "Mensagens guardadas: %d\n", t.Messages)
	fmt.Fprintf(out, "Fila de envio: %d pendente(s)\n", t.Pending)
	fmt.Fprintf(out, "Tamanho: dados %s, sessão %s\n",
		humanSize(filesSize(a.home, dataFileName)), humanSize(filesSize(a.home, sessionFileName)))
	return nil
}

// cmdLogout unlinks the device. --wipe then deletes the data directory.
func cmdLogout(ctx context.Context, a *app, out io.Writer, args []string) (wipe bool, err error) {
	fs := flag.NewFlagSet("logout", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	w := fs.Bool("wipe", false, "")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		return false, errUsage
	}
	err = withSession(ctx, a, func(ctx context.Context, s session) error {
		err := s.Logout(ctx)
		switch {
		case errors.Is(err, wa.ErrNotLoggedIn):
			fmt.Fprintln(out, "Nenhuma sessão vinculada.")
		case errors.Is(err, wa.ErrLogoutOffline):
			fmt.Fprintln(out, "Sessão local apagada, mas o WhatsApp não respondeu.")
			fmt.Fprintln(out, "Remova o aparelho no celular: WhatsApp > Aparelhos conectados.")
		case err != nil:
			return err
		default:
			fmt.Fprintln(out, "Aparelho desvinculado do WhatsApp.")
		}
		a.audit(ctx, "logout", "", "cli")
		return nil
	})
	return *w, err
}

// wipeFiles are the entries of the data directory that --wipe deletes. Nothing
// else is touched: the directory may be one the owner chose.
var wipeFiles = []string{
	dataFileName, dataFileName + "-wal", dataFileName + "-shm",
	sessionFileName, sessionFileName + "-wal", sessionFileName + "-shm",
	refKeyFileName, configFileName, lockFileName,
}

// wipe deletes the data directory's files. It runs after the app is closed.
func wipe(home string, out io.Writer) error {
	for _, name := range wipeFiles {
		if err := os.Remove(filepath.Join(home, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("apagar %s: %w", name, err)
		}
	}
	if err := os.RemoveAll(filepath.Join(home, logging.LogsDirName)); err != nil {
		return fmt.Errorf("apagar logs: %w", err)
	}
	if err := os.Remove(home); err == nil {
		fmt.Fprintf(out, "Diretório %s apagado.\n", home)
	} else {
		fmt.Fprintf(out, "Dados do whatsapp-mcp apagados de %s (outros arquivos da pasta foram mantidos).\n", home)
	}
	return nil
}

// filesSize is the size of a SQLite database with its WAL.
func filesSize(home, name string) int64 {
	var total int64
	for _, n := range []string{name, name + "-wal"} {
		if fi, err := os.Stat(filepath.Join(home, n)); err == nil {
			total += fi.Size()
		}
	}
	return total
}

func humanSize(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}
