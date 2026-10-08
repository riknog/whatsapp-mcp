package main

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/riknog/whatsapp-mcp/internal/clock"
	"github.com/riknog/whatsapp-mcp/internal/privacy"
	"github.com/riknog/whatsapp-mcp/internal/store"
)

// watchCLI runs watch and returns its exit code, stdout and stderr.
func watchCLI(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code := run(append([]string{"watch"}, args...), &out, &errOut)
	if err := privacy.AssertNoPII(out.String()); err != nil {
		t.Fatalf("watch stdout: %v\n%s", err, out.String())
	}
	return code, out.String(), errOut.String()
}

func TestWatchOnceListsUnseenChatsWithoutText(t *testing.T) {
	home := cliHome(t)
	code, out, errOut := watchCLI(t, "--once")
	if code != exitOK {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	for _, want := range []string{"mensagens não vistas", "Mãe (c_", "Pai (c_", "Ana Souza (c_", "2 não vistas"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "abacaxi") || strings.Contains(out, "Família") {
		t.Errorf("output has message text or a group:\n%s", out)
	}
	if _, out, _ := watchCLI(t, "--once", "--include-groups"); !strings.Contains(out, "Família (c_") {
		t.Errorf("groups missing:\n%s", out)
	}

	// A hidden chat is never announced, and a seen chat no longer counts.
	mustCLI(t, "hide", "Mãe")
	st := openStore(t, home)
	if err := st.SetOwnerReadAt(context.Background(), jidDad, time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	_, out, _ = watchCLI(t, "--once")
	if strings.Contains(out, "Mãe") || strings.Contains(out, "Pai") || !strings.Contains(out, "Ana Souza") {
		t.Errorf("after hide and read:\n%s", out)
	}
}

func TestWatchOnceIsSilentWithNothingNew(t *testing.T) {
	home := cliHome(t)
	st := openStore(t, home)
	for _, jid := range []string{jidMom, jidDad, jidAna} {
		if err := st.SetOwnerReadAt(context.Background(), jid, time.Now().Unix()); err != nil {
			t.Fatal(err)
		}
	}
	if code, out, errOut := watchCLI(t, "--once"); code != exitOK || out != "" {
		t.Fatalf("exit %d, stdout %q, stderr %q", code, out, errOut)
	}
}

func TestWatchArguments(t *testing.T) {
	cliHome(t)
	if code, _, _ := watchCLI(t, "extra"); code != exitUsage {
		t.Errorf("extra argument: exit %d", code)
	}
	if code, _, _ := watchCLI(t, "--nope"); code != exitUsage {
		t.Errorf("unknown flag: exit %d", code)
	}
	if code, _, errOut := watchCLI(t, "--interval", "100ms"); code != exitFailure || !strings.Contains(errOut, "mínimo") {
		t.Errorf("short interval: exit %d, %q", code, errOut)
	}
}

// syncBuffer is a bytes.Buffer safe for the watch goroutine and the test.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func TestWatchLoopReportsOnlyNewArrivals(t *testing.T) {
	home := cliHome(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var stderr bytes.Buffer
	a, err := openApp(ctx, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	defer a.close()

	var out syncBuffer
	done := make(chan error, 1)
	go func() {
		done <- runWatch(ctx, a, &out, watchOptions{interval: 10 * time.Millisecond}, clock.Real{})
	}()
	time.Sleep(50 * time.Millisecond) // the watcher takes its starting point
	st := openStore(t, home)
	now := time.Now().Unix()
	for _, m := range []store.Message{
		{ChatJID: jidDad, ID: "NEWMINE", TS: now, Kind: "text", Text: "minha", FromMe: true},
		{ChatJID: jidGroup, ID: "NEWGROUP", SenderJID: jidDad, TS: now, Kind: "text", Text: "grupo"},
		{ChatJID: jidMom, ID: "NEWMOM", SenderJID: jidMom, TS: now, Kind: "text", Text: "segredo novo"},
	} {
		if _, err := st.InsertMessage(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(out.String(), "Mãe") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond) // a later tick must not repeat the line
	cancel()
	if err := <-done; err == nil {
		t.Error("runWatch returned nil after cancel")
	}
	got := out.String()
	if strings.Count(got, "Nova mensagem no WhatsApp: Mãe (c_") != 1 || !strings.Contains(got, "1 não vista") {
		t.Errorf("output = %q", got)
	}
	for _, bad := range []string{"Pai", "Família", "segredo"} {
		if strings.Contains(got, bad) {
			t.Errorf("output has %q: %q", bad, got)
		}
	}
}

func TestClipNameAndPlural(t *testing.T) {
	if got := clipName(strings.Repeat("a", 50)); len([]rune(got)) != maxWatchName {
		t.Errorf("clipName = %q", got)
	}
	if clipName("Ana") != "Ana" || plural(1) != "1 não vista" || plural(2) != "2 não vistas" {
		t.Error("short name or plural changed")
	}
}

func TestHideContactWithoutChat(t *testing.T) {
	home := cliHome(t)
	st := openStore(t, home)
	const bia, caio = "5511900000007@s.whatsapp.net", "5511900000008@s.whatsapp.net"
	for jid, name := range map[string]string{bia: "Bia Lima", caio: "Caio Prado"} {
		if err := st.UpsertContact(context.Background(), store.Contact{JID: jid, FullName: name}); err != nil {
			t.Fatal(err)
		}
	}
	if code, out := cli(t, "unhide", "Caio Prado"); code != exitFailure || !strings.Contains(out, "Não há conversa") {
		t.Fatalf("unhide without chat: exit %d, %q", code, out)
	}
	if out := mustCLI(t, "hide", "Bia Lima"); !strings.Contains(out, "oculta") {
		t.Fatalf("hide output = %q", out)
	}
	c, err := st.GetChat(context.Background(), bia)
	if err != nil || !c.Hidden || c.Kind != "direct" {
		t.Fatalf("chat = %+v, %v", c, err)
	}
	// Her first message arrives hidden.
	if _, err := st.InsertMessage(context.Background(), store.Message{ChatJID: bia, ID: "BIA1", SenderJID: bia,
		TS: time.Now().Unix(), Kind: "text", Text: "oi"}); err != nil {
		t.Fatal(err)
	}
	if _, out, _ := watchCLI(t, "--once"); strings.Contains(out, "Bia") {
		t.Errorf("hidden contact announced:\n%s", out)
	}
	mustCLI(t, "unhide", "Bia Lima")
}
