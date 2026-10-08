package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/riknog/whatsapp-mcp/internal/config"
)

// readmeBlocks returns the fenced code blocks of README.md with the given language.
func readmeBlocks(t *testing.T, lang string) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "README.md"))
	if err != nil {
		t.Fatalf("read README: %v", err)
	}
	var blocks []string
	rest := string(b)
	fence := "```" + lang + "\n"
	for {
		i := strings.Index(rest, fence)
		if i < 0 {
			return blocks
		}
		rest = rest[i+len(fence):]
		j := strings.Index(rest, "```")
		if j < 0 {
			t.Fatalf("unclosed %s block", lang)
		}
		blocks = append(blocks, rest[:j])
		rest = rest[j+3:]
	}
}

// TestReadmeClaudeDesktopConfigIsValid checks that the Claude
// Desktop block is valid JSON that starts this binary's serve command.
func TestReadmeClaudeDesktopConfigIsValid(t *testing.T) {
	blocks := readmeBlocks(t, "json")
	if len(blocks) != 1 {
		t.Fatalf("json blocks = %d, want 1", len(blocks))
	}
	var cfg struct {
		MCPServers map[string]struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
		} `json:"mcpServers"`
	}
	dec := json.NewDecoder(strings.NewReader(blocks[0]))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	srv, ok := cfg.MCPServers["whatsapp"]
	if !ok || srv.Command != "whatsapp-mcp" || !reflect.DeepEqual(srv.Args, []string{"serve"}) {
		t.Fatalf("config = %+v", cfg)
	}
}

func TestReadmeClaudeCodeCommand(t *testing.T) {
	for _, b := range readmeBlocks(t, "sh") {
		if strings.TrimSpace(b) == "claude mcp add whatsapp -- whatsapp-mcp serve" {
			return
		}
	}
	t.Fatal("README lacks the claude mcp add block")
}

// TestReadmeConfigMatchesDefaults checks that the documented config.toml loads
// and equals the defaults it claims to show.
func TestReadmeConfigMatchesDefaults(t *testing.T) {
	blocks := readmeBlocks(t, "toml")
	if len(blocks) != 1 {
		t.Fatalf("toml blocks = %d, want 1", len(blocks))
	}
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, config.ConfigFileName), []byte(blocks[0]), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := config.Load(home)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	want := config.Defaults()
	want.Home = home
	if !reflect.DeepEqual(*got, want) {
		t.Fatalf("README config = %+v\nwant defaults %+v", *got, want)
	}
}
