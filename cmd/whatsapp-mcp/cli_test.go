package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/riknog/whatsapp-mcp/internal/clock"
	"github.com/riknog/whatsapp-mcp/internal/config"
	"github.com/riknog/whatsapp-mcp/internal/identity"
	"github.com/riknog/whatsapp-mcp/internal/lock"
	"github.com/riknog/whatsapp-mcp/internal/mcpserver"
	"github.com/riknog/whatsapp-mcp/internal/privacy"
	"github.com/riknog/whatsapp-mcp/internal/store"
	"github.com/riknog/whatsapp-mcp/internal/wa"
)

const (
	jidMom   = "5511900000001@s.whatsapp.net"
	jidDad   = "5511900000002@s.whatsapp.net"
	jidAna   = "5511900000003@s.whatsapp.net"
	jidGroup = "120363000000000001@g.us"
)

// cliHome is a data directory with three contacts and a group, each with
// messages, used through $WHATSAPP_MCP_HOME.
func cliHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	if err := os.Chmod(home, 0o700); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Setenv(config.EnvHome, home)
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(home, dataFileName), clock.Real{})
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	defer func() { _ = st.Close() }()
	refs, err := identity.LoadRefs(home)
	if err != nil {
		t.Fatalf("refs: %v", err)
	}
	now := time.Now()
	seed := []struct {
		jid, kind, name, text string
		age                   time.Duration
	}{
		{jidMom, "direct", "Mãe", "abacaxi da mãe", time.Hour},
		{jidDad, "direct", "Pai", "abacaxi do pai", 2 * time.Hour},
		{jidAna, "direct", "Ana Souza", "mensagem antiga", 60 * 24 * time.Hour},
		{jidGroup, "group", "Família", "abacaxi no grupo", 3 * time.Hour},
	}
	for i, s := range seed {
		ts := now.Add(-s.age).Unix()
		if err := st.UpsertChat(ctx, store.Chat{JID: s.jid, Ref: refs.Ref(s.jid), Kind: s.kind, DisplayName: s.name, LastMessageAt: ts}); err != nil {
			t.Fatalf("chat: %v", err)
		}
		if s.kind == "direct" {
			if err := st.UpsertContact(ctx, store.Contact{JID: s.jid, FullName: s.name, UpdatedAt: ts}); err != nil {
				t.Fatalf("contact: %v", err)
			}
		}
		for j := 0; j < 2; j++ {
			m := store.Message{ChatJID: s.jid, ID: "MSG" + string(rune('A'+i)) + string(rune('A'+j)), SenderJID: s.jid, TS: ts + int64(j), Kind: "text", Text: s.text}
			if _, err := st.InsertMessage(ctx, m); err != nil {
				t.Fatalf("message: %v", err)
			}
		}
	}
	return home
}

// cli runs a command and returns its exit code and stderr. Nothing may reach
// stdout, and no output may carry a phone number or JID.
func cli(t *testing.T, args ...string) (int, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code := run(args, &out, &errOut)
	if out.Len() != 0 {
		t.Fatalf("%v wrote to stdout: %q", args, out.String())
	}
	// The data directory is a temporary path with digits; it is not personal data.
	checked := strings.ReplaceAll(errOut.String(), os.Getenv(config.EnvHome), "<home>")
	if err := privacy.AssertNoPII(checked); err != nil {
		t.Fatalf("%v: %v\n%s", args, err, errOut.String())
	}
	return code, errOut.String()
}

func mustCLI(t *testing.T, args ...string) string {
	t.Helper()
	code, out := cli(t, args...)
	if code != exitOK {
		t.Fatalf("%v: exit %d\n%s", args, code, out)
	}
	return out
}

// openStore opens the test home's data.db for checks.
func openStore(t *testing.T, home string) *store.Store {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(home, dataFileName), clock.Real{})
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func countMessages(t *testing.T, st *store.Store, jid string) int64 {
	t.Helper()
	n, err := st.CountMessages(context.Background(), jid)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	return n
}

func searchChats(t *testing.T, st *store.Store, q string) map[string]int {
	t.Helper()
	hits, _, err := st.Search(context.Background(), store.SearchFilter{Query: q, Limit: 30})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	out := map[string]int{}
	for _, h := range hits {
		out[h.Message.ChatJID]++
	}
	return out
}

// mcpSession runs serve's MCP server over the same data.db, as Claude would.
func mcpSession(t *testing.T, home string) *mcp.ClientSession {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	serverT, clientT := mcp.NewInMemoryTransports()
	done := make(chan error, 1)
	fake := wa.NewFake(clock.Real{})
	fake.SetLoggedIn(true)
	fake.SetConnected(true)
	go func() {
		err := mcpserver.Serve(ctx, mcpserver.ServeOptions{Home: home, Version: "test", Transport: serverT, Client: fake})
		done <- err
		if err != nil {
			// A server that failed to start never reads the pipe, and the
			// client's initialize write ignores ctx: close the server end.
			if conn, cerr := serverT.Connect(context.Background()); cerr == nil {
				_ = conn.Close()
			}
			cancel()
		}
	}()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).Connect(ctx, clientT, nil)
	if err != nil {
		cancel()
		t.Fatalf("connect: %v (serve: %v)", err, <-done)
	}
	t.Cleanup(func() {
		_ = cs.Close()
		cancel()
		<-done
	})
	return cs
}

func callText(t *testing.T, cs *mcp.ClientSession, tool string, args map[string]any) string {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", tool, err)
	}
	b, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}

// TestHideRemovesChatFromToolsImmediately checks that after hide,
// the running server no longer shows the chat, its messages or its contact.
func TestHideRemovesChatFromToolsImmediately(t *testing.T) {
	home := cliHome(t)
	cs := mcpSession(t, home)

	if got := callText(t, cs, "list_chats", map[string]any{}); !strings.Contains(got, "Mãe") {
		t.Fatalf("before hide, list_chats = %s", got)
	}
	out := mustCLI(t, "hide", "Mãe")
	if !strings.Contains(out, "oculta") {
		t.Fatalf("hide output = %q", out)
	}
	for _, c := range []struct {
		tool string
		args map[string]any
	}{
		{"list_chats", map[string]any{}},
		{"search_messages", map[string]any{"query": "abacaxi"}},
		{"search_contacts", map[string]any{"query": "Mãe"}},
		{"list_contacts", map[string]any{}},
		{"get_chat_messages", map[string]any{"contact": "Mãe"}},
	} {
		got := callText(t, cs, c.tool, c.args)
		if strings.Contains(got, "Mãe") || strings.Contains(got, "abacaxi da mãe") {
			t.Errorf("%s still shows the hidden chat: %s", c.tool, got)
		}
	}
	if got := callText(t, cs, "search_messages", map[string]any{"query": "abacaxi"}); !strings.Contains(got, "abacaxi do pai") {
		t.Errorf("search lost the other chats: %s", got)
	}

	out = mustCLI(t, "hidden")
	if !strings.Contains(out, "Mãe (c_") {
		t.Fatalf("hidden = %q", out)
	}
	if out := mustCLI(t, "hide", "Mãe"); !strings.Contains(out, "Nada a fazer") {
		t.Fatalf("second hide = %q", out)
	}
	mustCLI(t, "unhide", "Mãe")
	if got := callText(t, cs, "list_chats", map[string]any{}); !strings.Contains(got, "Mãe") {
		t.Fatalf("after unhide, list_chats = %s", got)
	}
	if out := mustCLI(t, "hidden"); !strings.Contains(out, "Nenhuma") {
		t.Fatalf("hidden after unhide = %q", out)
	}
}

func TestHideErrors(t *testing.T) {
	cliHome(t)
	if code, _ := cli(t, "hide"); code != exitUsage {
		t.Fatalf("hide without name: exit %d", code)
	}
	code, out := cli(t, "hide", "Ninguém Existe")
	if code != exitFailure || !strings.Contains(out, "whatsapp-mcp hide:") {
		t.Fatalf("unknown: exit %d, %q", code, out)
	}
	code, out = cli(t, "hide", "+55 11 90000-0001")
	if code != exitFailure || !strings.Contains(out, "whatsapp-mcp hide:") {
		t.Fatalf("phone: exit %d, %q", code, out)
	}
}

// TestAmbiguousNameListsCandidatesWithRefs checks that the CLI shows name and
// ref, and that the ref then selects the contact.
func TestAmbiguousNameListsCandidatesWithRefs(t *testing.T) {
	home := cliHome(t)
	st := openStore(t, home)
	refs, err := identity.LoadRefs(home)
	if err != nil {
		t.Fatal(err)
	}
	other := "5511900000009@s.whatsapp.net"
	if err := st.UpsertContact(context.Background(), store.Contact{JID: other, FullName: "Ana Souza"}); err != nil {
		t.Fatal(err)
	}
	code, out := cli(t, "hide", "Ana Souza")
	if code != exitFailure || strings.Count(out, "  - Ana Souza  (c_") != 2 || !strings.Contains(out, "contact_ref") {
		t.Fatalf("ambiguous: exit %d\n%s", code, out)
	}
	ref := refs.Ref(jidAna)
	mustCLI(t, "hide", ref)
	c, err := st.GetChat(context.Background(), jidAna)
	if err != nil || !c.Hidden {
		t.Fatalf("chat = %+v, %v", c, err)
	}
}

// TestPurgeContactRemovesOnlyThatChat checks that messages and FTS
// entries of that chat go, the others stay.
func TestPurgeContactRemovesOnlyThatChat(t *testing.T) {
	home := cliHome(t)
	out := mustCLI(t, "purge", "--contact", "Mãe", "--yes")
	if !strings.Contains(out, "Apagadas 2") {
		t.Fatalf("purge output = %q", out)
	}
	st := openStore(t, home)
	if n := countMessages(t, st, jidMom); n != 0 {
		t.Fatalf("Mãe still has %d messages", n)
	}
	if n := countMessages(t, st, jidDad); n != 2 {
		t.Fatalf("Pai has %d messages, want 2", n)
	}
	hits := searchChats(t, st, "abacaxi")
	if hits[jidMom] != 0 || hits[jidDad] != 2 || hits[jidGroup] != 2 {
		t.Fatalf("FTS hits = %v", hits)
	}
	if hits := searchChats(t, st, "mãe"); len(hits) != 0 {
		t.Fatalf("FTS still finds the purged text: %v", hits)
	}
}

// TestPurgeWithoutYesDeletesNothing checks that, in every mode, purge without --yes deletes nothing.
func TestPurgeWithoutYesDeletesNothing(t *testing.T) {
	home := cliHome(t)
	for _, args := range [][]string{
		{"purge", "--contact", "Mãe"},
		{"purge", "--older-than", "30d"},
		{"purge", "--all"},
	} {
		code, out := cli(t, args...)
		if code != exitFailure || !strings.Contains(out, "Nada foi apagado") {
			t.Fatalf("%v: exit %d, %q", args, code, out)
		}
	}
	st := openStore(t, home)
	tot, err := st.Totals(context.Background())
	if err != nil || tot.Messages != 8 {
		t.Fatalf("totals = %+v, %v", tot, err)
	}
}

func TestPurgeOlderThanAndAll(t *testing.T) {
	home := cliHome(t)
	out := mustCLI(t, "purge", "--older-than", "30d", "--yes")
	if !strings.Contains(out, "Apagadas 2") {
		t.Fatalf("older-than = %q", out)
	}
	st := openStore(t, home)
	if n := countMessages(t, st, jidAna); n != 0 {
		t.Fatalf("old messages left: %d", n)
	}
	if n := countMessages(t, st, jidMom); n != 2 {
		t.Fatalf("recent messages deleted: %d", n)
	}
	mustCLI(t, "purge", "--all", "--yes")
	tot, err := st.Totals(context.Background())
	if err != nil || tot.Messages != 0 {
		t.Fatalf("after --all: %+v, %v", tot, err)
	}
}

func TestPurgeUsage(t *testing.T) {
	cliHome(t)
	for _, args := range [][]string{
		{"purge"},
		{"purge", "--all", "--contact", "Mãe"},
		{"purge", "--bogus"},
		{"purge", "extra"},
	} {
		if code, _ := cli(t, args...); code != exitUsage {
			t.Errorf("%v: exit %d, want %d", args, code, exitUsage)
		}
	}
	for _, v := range []string{"abc", "0d", "-3h", "xd"} {
		if code, _ := cli(t, "purge", "--older-than", v); code != exitFailure {
			t.Errorf("--older-than %s: exit %d", v, code)
		}
	}
}

func TestParseAge(t *testing.T) {
	for in, want := range map[string]time.Duration{"30d": 30 * 24 * time.Hour, "12h": 12 * time.Hour, "90m": 90 * time.Minute} {
		got, err := parseAge(in)
		if err != nil || got != want {
			t.Errorf("parseAge(%q) = %v, %v", in, got, err)
		}
	}
}

// TestSecondServeFailsWithLockError checks that while one process
// holds the session, serve exits with a clear message and touches nothing.
func TestSecondServeFailsWithLockError(t *testing.T) {
	home := cliHome(t)
	l, err := lock.Acquire(filepath.Join(home, lockFileName))
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer func() { _ = l.Release() }()
	var errOut bytes.Buffer
	if code := serve(&errOut); code != exitFailure {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(errOut.String(), "outra instância") {
		t.Fatalf("stderr = %q", errOut.String())
	}
	for _, args := range [][]string{{"login"}, {"logout"}} {
		code, out := cli(t, args...)
		if code != exitFailure || !strings.Contains(out, "outra instância") {
			t.Errorf("%v: exit %d, %q", args, code, out)
		}
	}
	// status still works and says why the connection was not tested.
	if out := mustCLI(t, "status"); !strings.Contains(out, "em uso pelo serve") {
		t.Errorf("status = %q", out)
	}
}

func TestShareable(t *testing.T) {
	home := cliHome(t)
	if out := mustCLI(t, "shareable", "list"); !strings.Contains(out, "Nenhum") {
		t.Fatalf("empty list = %q", out)
	}
	mustCLI(t, "shareable", "add", "Pai")
	out := mustCLI(t, "shareable", "list")
	if !strings.Contains(out, "Pai (c_") {
		t.Fatalf("list = %q", out)
	}
	st := openStore(t, home)
	if ok, err := st.IsShareable(context.Background(), jidDad); err != nil || !ok {
		t.Fatalf("IsShareable = %v, %v", ok, err)
	}
	if code, out := cli(t, "shareable", "add", "Família"); code != exitFailure || !strings.Contains(out, "Grupos") {
		t.Fatalf("group: exit %d, %q", code, out)
	}
	mustCLI(t, "shareable", "remove", "Pai")
	if ok, _ := st.IsShareable(context.Background(), jidDad); ok {
		t.Fatal("still shareable")
	}
	for _, args := range [][]string{{"shareable"}, {"shareable", "add"}, {"shareable", "list", "x"}, {"shareable", "bogus", "x"}} {
		if code, _ := cli(t, args...); code != exitUsage {
			t.Errorf("%v: exit %d", args, code)
		}
	}
}

func TestCategory(t *testing.T) {
	home := cliHome(t)
	if out := mustCLI(t, "category", "list"); !strings.Contains(out, "Nenhuma") {
		t.Fatalf("empty list = %q", out)
	}
	mustCLI(t, "category", "add", "Clientes", "Pai")
	mustCLI(t, "category", "add", "Clientes", "Mãe")
	out := mustCLI(t, "category", "list")
	if !strings.Contains(out, "Clientes: 2 contato(s), local") {
		t.Fatalf("list = %q", out)
	}
	mustCLI(t, "category", "remove", "Clientes", "Mãe")
	st := openStore(t, home)
	labels, err := st.LabelsOf(context.Background(), jidMom)
	if err != nil || len(labels) != 0 {
		t.Fatalf("labels of Mãe = %v, %v", labels, err)
	}
	if code, _ := cli(t, "category", "remove", "Inexistente", "Pai"); code != exitFailure {
		t.Fatalf("unknown category: exit %d", code)
	}
	for _, args := range [][]string{{"category"}, {"category", "add", "x"}, {"category", "list", "x"}, {"category", "bogus"}} {
		if code, _ := cli(t, args...); code != exitUsage {
			t.Errorf("%v: exit %d", args, code)
		}
	}
}

// fakeSession stands in for the WhatsApp adapter in login, status and logout.
type fakeSession struct {
	paired     bool
	connects   bool
	logoutErr  error
	loginCalls int
	pairPhone  string
	events     chan any
	closed     bool
}

func (f *fakeSession) Paired() bool { return f.paired }
func (f *fakeSession) Login(ctx context.Context, out io.Writer, pairPhone string, sink wa.Sink) error {
	f.loginCalls++
	f.pairPhone = pairPhone
	f.paired = true
	return nil
}
func (f *fakeSession) CheckConnection(context.Context, time.Duration) bool { return f.connects }
func (f *fakeSession) Logout(context.Context) error {
	if !f.paired {
		return wa.ErrNotLoggedIn
	}
	f.paired = false
	return f.logoutErr
}
func (f *fakeSession) Events() <-chan any { return f.events }
func (f *fakeSession) Close() error       { f.closed = true; return nil }

func useFakeSession(t *testing.T, f *fakeSession) {
	t.Helper()
	f.events = make(chan any)
	prev := openSession
	openSession = func(context.Context, *app) (session, error) { return f, nil }
	t.Cleanup(func() { openSession = prev })
}

func TestLogin(t *testing.T) {
	cliHome(t)
	f := &fakeSession{}
	useFakeSession(t, f)
	out := mustCLI(t, "login", "--pair-phone", "+55 (11) 90000-0001")
	if f.loginCalls != 1 || f.pairPhone != "5511900000001" || !f.closed {
		t.Fatalf("login: %+v", f)
	}
	if !strings.Contains(out, "Pronto") {
		t.Fatalf("out = %q", out)
	}
	out = mustCLI(t, "login")
	if f.loginCalls != 1 || !strings.Contains(out, "Já existe") {
		t.Fatalf("second login: calls %d, %q", f.loginCalls, out)
	}
	if code, _ := cli(t, "login", "extra"); code != exitUsage {
		t.Fatalf("usage: exit %d", code)
	}
}

func TestStatus(t *testing.T) {
	cliHome(t)
	f := &fakeSession{}
	useFakeSession(t, f)
	out := mustCLI(t, "status")
	for _, want := range []string{"não vinculada", "Conversas: 4 (0 ocultas)", "Mensagens guardadas: 8", "Fila de envio: 0", "Tamanho: dados"} {
		if !strings.Contains(out, want) {
			t.Errorf("status lacks %q:\n%s", want, out)
		}
	}
	f.paired, f.connects = true, true
	if out := mustCLI(t, "status"); !strings.Contains(out, "Conexão: ok") {
		t.Errorf("status = %q", out)
	}
	f.connects = false
	if out := mustCLI(t, "status"); !strings.Contains(out, "Conexão: falhou") {
		t.Errorf("status = %q", out)
	}
	if code, _ := cli(t, "status", "x"); code != exitUsage {
		t.Fatalf("usage: exit %d", code)
	}
}

func TestLogoutAndWipe(t *testing.T) {
	home := cliHome(t)
	f := &fakeSession{paired: true, logoutErr: wa.ErrLogoutOffline}
	useFakeSession(t, f)
	if out := mustCLI(t, "logout"); !strings.Contains(out, "Aparelhos conectados") {
		t.Fatalf("offline logout = %q", out)
	}
	if out := mustCLI(t, "logout"); !strings.Contains(out, "Nenhuma sessão") {
		t.Fatalf("second logout = %q", out)
	}
	f.paired, f.logoutErr = true, nil
	keep := filepath.Join(home, "meu-arquivo.txt")
	if err := os.WriteFile(keep, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	out := mustCLI(t, "logout", "--wipe")
	if !strings.Contains(out, "desvinculado") || !strings.Contains(out, "mantidos") {
		t.Fatalf("wipe = %q", out)
	}
	entries, err := os.ReadDir(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "meu-arquivo.txt" {
		t.Fatalf("left after wipe: %v", entries)
	}
	if err := os.Remove(keep); err != nil {
		t.Fatal(err)
	}
	f.paired = true
	if out := mustCLI(t, "logout", "--wipe"); !strings.Contains(out, "apagado") {
		t.Fatalf("wipe of empty dir = %q", out)
	}
	if _, err := os.Stat(home); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("home still exists: %v", err)
	}
	if code, _ := cli(t, "logout", "x"); code != exitUsage {
		t.Fatalf("usage: exit %d", code)
	}
}

// TestRealSessionUnpaired opens a real, empty session.db: nothing connects.
func TestRealSessionUnpaired(t *testing.T) {
	cliHome(t)
	if out := mustCLI(t, "status"); !strings.Contains(out, "não vinculada") {
		t.Fatalf("status = %q", out)
	}
	if out := mustCLI(t, "logout"); !strings.Contains(out, "Nenhuma sessão") {
		t.Fatalf("logout = %q", out)
	}
}
