package config

import (
	"fmt"
	"os"
	"path/filepath"
)

// EnvHome overrides the data directory.
const EnvHome = "WHATSAPP_MCP_HOME"

// DefaultDirName is the data directory under the user's home when EnvHome is unset.
const DefaultDirName = ".whatsapp-mcp"

// Home returns the data directory: $WHATSAPP_MCP_HOME if set, else
// ~/.whatsapp-mcp. The directory is created with mode 0700 if it does not exist.
func Home() (string, error) {
	return resolveHome(os.Getenv(EnvHome), os.UserHomeDir)
}

func resolveHome(env string, userHomeDir func() (string, error)) (string, error) {
	dir := env
	if dir == "" {
		u, err := userHomeDir()
		if err != nil {
			return "", fmt.Errorf("config: diretório home do usuário: %w", err)
		}
		dir = filepath.Join(u, DefaultDirName)
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("config: caminho %q: %w", dir, err)
	}
	if err := os.MkdirAll(abs, 0o700); err != nil { // #nosec G703 -- the owner chooses the data directory
		return "", fmt.Errorf("config: criar diretório %s: %w", abs, err)
	}
	return abs, nil
}
