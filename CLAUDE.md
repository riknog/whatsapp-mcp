# CLAUDE.md — regras para quem implementa

Projeto: servidor MCP local para WhatsApp. Leia antes de codar:
`docs/01-DESIGN.md`, `docs/02-TOOLS.md`, `docs/03-SEGURANCA-LGPD.md`.

## Stack (fechada)
- Go (versão mínima = a exigida pelo `go.mod` do whatsmeow; usar a toolchain estável mais recente).
- Dependências permitidas: `go.mau.fi/whatsmeow` (+ suas transitivas), `github.com/modelcontextprotocol/go-sdk`,
  `modernc.org/sqlite`, `github.com/BurntSushi/toml`, `github.com/mdp/qrterminal/v3`, `golang.org/x/text`.
  Testes: só stdlib (`testing`), sem testify. Qualquer outra → pare e reporte.

## Regras inegociáveis
1. **stdout é do protocolo MCP.** Log só via `internal/logging` (stderr/arquivo). Nunca `fmt.Print*`.
2. **Nunca** expor telefone ou JID em saída de tool, erro, ou log. Use `identity.Ref()` e `privacy.Redact()`.
3. Todo acesso ao WhatsApp passa pela interface `wa.Client` (mockável). Nada de whatsmeow fora de `internal/wa` e `internal/ingest`.
4. Todo envio passa por `sendqueue`. Nenhum caminho de código chama `wa.Client.SendText` direto.
5. Tempo e aleatoriedade injetáveis (`clock.Clock`, `rand.Source`) em tudo que tem cooldown/timeout.
6. SQL sempre com parâmetros (`?`), nunca concatenação.
7. Arquivos de dados criados com 0600, diretórios 0700.
8. Não mude os docs de design. Contradição ou impossibilidade → registre no relatório da tarefa e pare.

## Comandos
- `make check` — fmt + vet + staticcheck + testes com `-race` (obrigatório antes de entregar).
- `make build` — gera `bin/whatsapp-mcp`.
- `make security` — govulncheck + gosec.

## Estilo
- Pacotes pequenos, interfaces no consumidor, erros com `fmt.Errorf("contexto: %w", err)`.
- Erros de tool: tipo `toolerr.Error{Code, Message, Details}` com os códigos de `docs/02-TOOLS.md`.
- Comentários e identificadores em inglês; docs para o usuário em português.
