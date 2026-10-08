package mcpserver

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/riknog/whatsapp-mcp/internal/clock"
	"github.com/riknog/whatsapp-mcp/internal/ingest"
	"github.com/riknog/whatsapp-mcp/internal/wa"
)

// blockingClient is a WhatsApp client whose connection never comes up, as when
// the network is down. Its Connect blocks until the context ends.
type blockingClient struct {
	*wa.Fake
}

func (blockingClient) Connect(ctx context.Context) error {
	<-ctx.Done()
	return ctx.Err()
}

func (blockingClient) IsConnected() bool { return false }

// TestServeAnswersBeforeWhatsAppConnects checks that initialize is
// answered within initializeBudget while WhatsApp is still unreachable, and the server
// stops when the client disconnects.
func TestServeAnswersBeforeWhatsAppConnects(t *testing.T) {
	home := testHome(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	serverT, clientT := mcp.NewInMemoryTransports()
	done := make(chan error, 1)
	go func() {
		done <- Serve(ctx, ServeOptions{
			Home:      home,
			Version:   "test",
			Transport: serverT,
			Client:    blockingClient{wa.NewFake(clock.Real{})},
		})
	}()

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0"}, nil)
	start := time.Now()
	type connected struct {
		cs  *mcp.ClientSession
		err error
	}
	ch := make(chan connected, 1)
	go func() {
		cs, err := client.Connect(ctx, clientT, nil)
		ch <- connected{cs, err}
	}()
	var cs *mcp.ClientSession
	select {
	case c := <-ch:
		if c.err != nil {
			t.Fatalf("initialize: %v", c.err)
		}
		cs = c.cs
	case err := <-done:
		t.Fatalf("Serve returned before initialize: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatalf("initialize was not answered within 5 s")
	}
	if elapsed := time.Since(start); initializeBudget > 0 && elapsed > initializeBudget {
		t.Errorf("initialize took %s, want < %s", elapsed, initializeBudget)
	}

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "whatsapp_status"})
	if err != nil || res.IsError {
		t.Fatalf("whatsapp_status: %v %+v", err, res)
	}
	var out statusOut
	decodeInto(t, res, &out)
	if out.Connected {
		t.Errorf("connected = true while the connection is down")
	}

	if err := cs.Close(); err != nil {
		t.Fatalf("close client: %v", err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("Serve did not return after the client closed")
	}
}

// TestServeRejectsBadConfig checks that a config.toml with an unknown key stops
// the server before it starts, with an error that names the key.
func TestServeRejectsBadConfig(t *testing.T) {
	home := testHome(t)
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte("[privacy]\nbogus = 1\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	serverT, _ := mcp.NewInMemoryTransports()
	err := Serve(context.Background(), ServeOptions{Home: home, Transport: serverT, Client: blockingClient{wa.NewFake(clock.Real{})}})
	if err == nil || !strings.Contains(err.Error(), "carregar configuração") {
		t.Fatalf("Serve = %v, want a configuration error", err)
	}
}

// testHome returns a data directory with the mode Serve requires (0700).
// t.TempDir creates subdirectories with 0755, so the mode is set here.
func testHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	if err := os.Chmod(home, 0o700); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	return home
}

// TestRetainDoesNothingWithoutRetention checks that retention 0 means never delete.
func TestRetainDoesNothingWithoutRetention(t *testing.T) {
	done := make(chan struct{})
	go func() {
		retain(context.Background(), nil, 0, clock.Real{}, nil)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("retain with 0 days did not return")
	}
}

// TestServeIngestsEventsAndRunsTheQueue checks the wiring of serve: an event of
// the WhatsApp client reaches the store, and the status shows the send queue.
func TestServeIngestsEventsAndRunsTheQueue(t *testing.T) {
	home := testHome(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fk := wa.NewFake(clock.Real{})
	fk.SetLoggedIn(true)
	fk.SetAccount("Dono", "personal")

	serverT, clientT := mcp.NewInMemoryTransports()
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, ServeOptions{Home: home, Version: "test", Transport: serverT, Client: fk}) }()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0"}, nil).Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("initialize: %v", err)
	}
	defer func() {
		_ = cs.Close()
		<-done
	}()

	if !fk.Inject(ingest.MessageEvent{Chat: "5511900000001@s.whatsapp.net", Sender: "5511900000001@s.whatsapp.net",
		ID: "3EB0SERVE", Time: time.Now(), Kind: "text", Text: "chegou pelo serve"}) {
		t.Fatal("event not accepted by the fake")
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "list_new_messages", Arguments: map[string]any{"peek": true}})
		if err != nil {
			t.Fatalf("list_new_messages: %v", err)
		}
		if strings.Contains(textOf(res), "chegou pelo serve") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("event never reached the store: %s", textOf(res))
		}
		time.Sleep(20 * time.Millisecond)
	}
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "whatsapp_status"})
	if err != nil || res.IsError {
		t.Fatalf("whatsapp_status: %v %+v", err, res)
	}
	if !strings.Contains(textOf(res), `"queue"`) {
		t.Errorf("status without the queue: %s", textOf(res))
	}
}
