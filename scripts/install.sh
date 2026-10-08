#!/bin/sh
# whatsapp-mcp installer for macOS and Linux.
#
#   curl -fsSL https://raw.githubusercontent.com/riknog/whatsapp-mcp/main/scripts/install.sh | sh
#
# Downloads the latest release binary, checks its SHA-256, puts it in
# ~/.local/bin, registers the "whatsapp" MCP server in Claude Code and (on
# macOS) Claude Desktop when installed, and runs "whatsapp-mcp login".
#
# Environment variables:
#   WHATSAPP_MCP_NO_LOGIN=1     skip the login step (e.g. when Claude runs this script)
#   WHATSAPP_MCP_VERSION=v0.1.0 install that release instead of the latest
#   WHATSAPP_MCP_BIN_DIR=...    install somewhere else than ~/.local/bin
set -eu

REPO="riknog/whatsapp-mcp"
BIN_DIR="${WHATSAPP_MCP_BIN_DIR:-$HOME/.local/bin}"

say() { printf '[whatsapp-mcp] %s\n' "$*"; }
die() { say "erro: $*" >&2; exit 1; }

case "$(uname -s)" in
  Darwin) os=darwin ;;
  Linux) os=linux ;;
  *) die "sistema não suportado: $(uname -s) (no Windows use o install.ps1)" ;;
esac
case "$(uname -m)" in
  x86_64 | amd64) arch=amd64 ;;
  arm64 | aarch64) arch=arm64 ;;
  *) die "arquitetura não suportada: $(uname -m)" ;;
esac

if [ -n "${WHATSAPP_MCP_VERSION:-}" ]; then
  base="https://github.com/$REPO/releases/download/$WHATSAPP_MCP_VERSION"
else
  base="https://github.com/$REPO/releases/latest/download"
fi

fetch() {
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL -o "$2" "$1"
  elif command -v wget >/dev/null 2>&1; then
    wget -q -O "$2" "$1"
  else
    die "preciso de curl ou wget"
  fi
}

sha256() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | cut -d' ' -f1
  else
    shasum -a 256 "$1" | cut -d' ' -f1
  fi
}

asset="whatsapp-mcp_${os}_${arch}"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

say "Baixando $asset..."
fetch "$base/$asset" "$tmp/$asset"
fetch "$base/checksums.txt" "$tmp/checksums.txt"
want="$(awk -v a="$asset" '$2 == a || $2 == "*" a { print $1 }' "$tmp/checksums.txt")"
[ -n "$want" ] || die "checksums.txt não lista $asset"
got="$(sha256 "$tmp/$asset")"
[ "$want" = "$got" ] || die "SHA-256 não confere para $asset (download corrompido?)"

mkdir -p "$BIN_DIR"
exe="$BIN_DIR/whatsapp-mcp"
chmod +x "$tmp/$asset"
mv -f "$tmp/$asset" "$exe"
"$exe" version

# PATH: add BIN_DIR to the shell profiles when it is missing.
case ":$PATH:" in
  *":$BIN_DIR:"*) ;;
  *)
    line="export PATH=\"$BIN_DIR:\$PATH\""
    for rc in "$HOME/.zshrc" "$HOME/.bashrc" "$HOME/.profile"; do
      if [ -f "$rc" ] || [ "$rc" = "$HOME/.profile" ]; then
        grep -qsF "$line" "$rc" || printf '\n%s\n' "$line" >>"$rc"
      fi
    done
    PATH="$BIN_DIR:$PATH"
    say "Pasta adicionada ao PATH: $BIN_DIR (abra um terminal novo para valer)"
    ;;
esac

# Claude Code.
if command -v claude >/dev/null 2>&1; then
  claude mcp remove whatsapp --scope user >/dev/null 2>&1 || true
  if claude mcp add --scope user whatsapp -- "$exe" serve >/dev/null; then
    say "Registrado no Claude Code (escopo do usuário)."
  else
    say "Não consegui registrar no Claude Code. Rode: claude mcp add --scope user whatsapp -- \"$exe\" serve"
  fi
else
  say 'Claude Code (comando "claude") não encontrado; pulando.'
fi

# Claude Desktop (macOS only; there is no official Linux build). The JSON is
# edited with JavaScript for Automation, which ships with macOS.
desktop_dir="$HOME/Library/Application Support/Claude"
if [ "$os" = darwin ] && [ -d "$desktop_dir" ]; then
  cfg="$desktop_dir/claude_desktop_config.json"
  [ -f "$cfg" ] && cp -f "$cfg" "$cfg.bak"
  if osascript -l JavaScript - "$cfg" "$exe" >/dev/null <<'JXA'
ObjC.import('Foundation');
function run(argv) {
  var path = argv[0], exe = argv[1], cfg = {};
  var raw = $.NSString.stringWithContentsOfFileEncodingError(path, $.NSUTF8StringEncoding, null);
  if (raw && raw.js.trim() !== '') cfg = JSON.parse(raw.js);
  cfg.mcpServers = cfg.mcpServers || {};
  cfg.mcpServers.whatsapp = { command: exe, args: ['serve'] };
  var out = $(JSON.stringify(cfg, null, 2) + '\n');
  if (!out.writeToFileAtomicallyEncodingError(path, true, $.NSUTF8StringEncoding, null)) {
    throw new Error('write failed');
  }
}
JXA
  then
    say "Registrado no Claude Desktop: $cfg"
  else
    say "Não consegui editar $cfg; veja o README para configurar à mão."
  fi
else
  say "Claude Desktop não encontrado; pulando."
fi

if [ "${WHATSAPP_MCP_NO_LOGIN:-}" = 1 ]; then
  say "Instalado. Falta vincular o WhatsApp: abra um terminal e rode  whatsapp-mcp login"
else
  say "Agora vincule o WhatsApp: no celular, WhatsApp > Aparelhos conectados > Conectar um aparelho, e leia o QR abaixo."
  # With "curl | sh" stdin is the script itself; give login the terminal.
  if [ -r /dev/tty ]; then
    "$exe" login </dev/tty || say "O login não terminou. Rode de novo: whatsapp-mcp login"
  else
    "$exe" login || say "O login não terminou. Rode de novo: whatsapp-mcp login"
  fi
fi

say "Pronto. Reinicie o Claude (Desktop ou Code) para ele carregar o WhatsApp."
say "Se o Claude Desktop e o Claude Code estiverem abertos juntos, só o primeiro consegue usar o WhatsApp."
