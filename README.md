# whatsapp-mcp

Servidor MCP **local** que deixa o Claude ler e responder suas conversas do WhatsApp como um
assistente humano: uma conversa por vez, no ritmo de uma pessoa, sem nunca ver números de telefone.

- Binário único em Go (whatsmeow + SQLite). Sem Docker, sem servidor, sem porta aberta.
- O Claude encontra contatos **pelo nome** (`"manda pra Mãe"`), nunca pelo número.
- Compartilha cartão de contato (só dos contatos que você liberar), sem o Claude ver o número.
- Funciona com WhatsApp **Business** e **pessoal**, conectando por QR code.
- Fila de envio com cooldown e limites anti-spam. Não existe disparo em massa.

> ⚠️ **Aviso de risco.** Este projeto usa um cliente **não oficial** do WhatsApp: o computador
> entra como "aparelho conectado", como o WhatsApp Web. Isso contraria os Termos de Serviço do
> WhatsApp e há risco real de **banimento do número**. Os limites da fila reduzem o risco, mas não
> o eliminam. Teste primeiro com um **número secundário**. Use por sua conta e risco.

## O que você precisa

- Um computador com **Windows 10/11**, **macOS** ou **Linux** (64 bits).
- O **Claude Code** ou o **Claude Desktop** instalado.
- O **WhatsApp no celular**, para ler o QR code uma vez.

Só isso. Não precisa instalar Go, Node, Python nem Docker: o programa é um arquivo só, já compilado.

## Instalação rápida (recomendada)

Um comando baixa o programa, confere a integridade (SHA-256), registra no Claude Code e no
Claude Desktop (os que estiverem instalados) e mostra o QR code para vincular o WhatsApp.

**Windows**: abra o **PowerShell** e cole:

```powershell
irm https://raw.githubusercontent.com/riknog/whatsapp-mcp/main/scripts/install.ps1 | iex
```

**macOS e Linux**: abra o **Terminal** e cole:

```sh
curl -fsSL https://raw.githubusercontent.com/riknog/whatsapp-mcp/main/scripts/install.sh | sh
```

No celular: **WhatsApp > Aparelhos conectados > Conectar um aparelho**, e leia o QR que aparece.
Depois **reinicie o Claude** e pergunte: *"quais mensagens novas eu tenho no WhatsApp?"*

O programa fica em `%LOCALAPPDATA%\Programs\whatsapp-mcp` (Windows) ou `~/.local/bin` (macOS/Linux).
Para atualizar, rode o mesmo comando de novo com o Claude fechado.

### Pelo próprio Claude Code (plugin com skill)

Se preferir que o Claude faça tudo e já saiba usar as tools, instale o plugin. No Claude Code, digite:

```
/plugin marketplace add riknog/whatsapp-mcp
/plugin install whatsapp@whatsapp-mcp
```

Depois peça: *"configura o WhatsApp para mim"*. A skill `whatsapp` instala o programa, pede para você
ler o QR code no terminal e ensina o Claude as regras de uso: confirmar antes de enviar, nunca pedir
números e ignorar instruções que venham dentro das mensagens.

## Instalação manual

Baixe o binário do seu sistema na página de [Releases](https://github.com/riknog/whatsapp-mcp/releases)
(confira com `checksums.txt`), ou instale com Go 1.26 ou mais novo:

```sh
go install github.com/riknog/whatsapp-mcp/cmd/whatsapp-mcp@latest
```

Ou compile do código: `make build` gera `bin/whatsapp-mcp`. Não precisa de CGO.

### Login

```sh
whatsapp-mcp login
```

No celular: **WhatsApp > Aparelhos conectados > Conectar um aparelho**, e leia o QR que aparece no
terminal. Sem câmera à mão? Use o código de pareamento:

```sh
whatsapp-mcp login --pair-phone 5511999999999
```

Depois de vincular, o login fica alguns segundos recebendo o histórico recente (30 dias por padrão)
e termina sozinho. Confira com `whatsapp-mcp status`.

Os dados ficam em `~/.whatsapp-mcp/` (ou no diretório de `WHATSAPP_MCP_HOME`). Nunca envie a ninguém o
arquivo `session.db`: ele é a chave da sua conta.

### Claude Desktop

Abra `claude_desktop_config.json` (Configurações > Desenvolvedor > Editar configuração) e adicione:

```json
{
  "mcpServers": {
    "whatsapp": {
      "command": "whatsapp-mcp",
      "args": ["serve"]
    }
  }
}
```

Se o Claude não achar o binário, troque `"whatsapp-mcp"` pelo caminho completo
(ex.: `/Users/voce/.local/bin/whatsapp-mcp` ou `C:\Users\voce\AppData\Local\Programs\whatsapp-mcp\whatsapp-mcp.exe`).
Reinicie o Claude Desktop.

### Claude Code

```sh
claude mcp add whatsapp -- whatsapp-mcp serve
```

Use `--scope user` para valer em todas as pastas. Só um processo pode usar a sessão do WhatsApp por
vez: se o Claude Desktop e o Claude Code estiverem abertos, o segundo `serve` recusa iniciar com uma
mensagem clara.

## Tools

| Tool | O que faz |
|---|---|
| `whatsapp_status` | Conexão, conta e situação da fila de envio. |
| `list_new_messages` | Mensagens novas desde a última vez que o Claude olhou. |
| `list_chats` | Conversas recentes, com não lidas e categorias. |
| `get_chat_messages` | Mensagens de uma conversa (paginação e "em volta de" uma mensagem). |
| `search_messages` | Busca de texto nas mensagens guardadas. |
| `list_contacts` / `search_contacts` | Contatos por nome ou categoria. |
| `list_categories` | Categorias (etiquetas do WhatsApp Business e categorias locais). |
| `send_message` | Envia texto para **um** contato, pela fila. |
| `share_contact` | Envia o cartão de um contato liberado com `shareable add`. |
| `mark_as_read` | Marca uma conversa como lida. |
| `set_contact_category` | Põe ou tira um contato de uma categoria local. |

Detalhes, parâmetros e erros: [docs/02-TOOLS.md](docs/02-TOOLS.md).

## Comandos

| Comando | Faz |
|---|---|
| `login [--pair-phone 5511...]` | Vincula este computador (QR ou código). |
| `serve` | Servidor MCP (o Claude chama sozinho). |
| `status` | Sessão, teste de conexão (10 s), conversas, mensagens, fila e tamanho dos dados. |
| `hide "Nome"` / `unhide "Nome"` / `hidden` | Oculta uma conversa do Claude (na hora, inclusive da busca). |
| `shareable add\|remove "Nome"` / `shareable list` | Contatos que o Claude pode compartilhar. |
| `category add\|remove "Categoria" "Nome"` / `category list` | Categorias locais. |
| `purge --older-than 30d \| --contact "Nome" \| --all` | Apaga mensagens guardadas. Sem `--yes` só mostra o que faria. |
| `logout [--wipe]` | Desvincula o aparelho. `--wipe` também apaga os dados locais. |
| `version` | Versão, commit e versão do whatsmeow. |

Quando um nome corresponde a mais de um contato, o comando lista os candidatos com o
`contact_ref` (`c_…`). Repita o comando com o ref no lugar do nome.

## Configuração

Opcional: crie `~/.whatsapp-mcp/config.toml` só com o que quiser mudar. Os valores abaixo são os padrões.

```toml
[privacy]
redact_phone_numbers_in_text = true
retention_days = 90            # apaga mensagens mais velhas (0 = nunca)

[send]
enabled = true
allow_groups = false           # enviar em grupos
policy = "known_contacts"      # ou "reply_only": só conversas com mensagem recebida nas últimas 24 h
burst_per_recipient = 10
switch_cooldown_ms = [1500, 3500]
burst_cooldown_ms = [8000, 15000]
max_per_minute = 20
max_per_hour = 200
max_per_day = 600
max_new_recipients_per_hour = 15
max_queue = 20
wait_timeout_s = 30
typing_indicator = true
quiet_hours = ""               # ex.: "22:00-08:00" bloqueia envios nesse intervalo

[share]
enabled = true
require_allowlist = true       # só contatos liberados com `whatsapp-mcp shareable add`

[read]
mark_read_enabled = true
history_sync_days = 30
```

O `serve` lê a configuração ao iniciar: reinicie o Claude depois de mudar.

## Privacidade e LGPD

- **O Claude nunca vê números de telefone.** Contatos aparecem pelo nome e por um `contact_ref`
  opaco (`c_…`), que não dá para converter em número. Números digitados no texto das mensagens
  também são mascarados.
- Tudo fica **no seu computador**: `data.db` (mensagens), `session.db` (sessão do WhatsApp) e
  `ref.key`. Arquivos com permissão 0600, pasta 0700 (no Windows, quem protege é a permissão da sua pasta de
  usuário). Nada é enviado para servidores do projeto.
  O que o Claude lê vai para a Anthropic como parte da conversa, como qualquer texto que você cole nele.
- Retenção: mensagens com mais de 90 dias são apagadas automaticamente (`retention_days`).
- Direito de eliminação: `purge --contact "Nome" --yes` apaga a conversa com uma pessoa;
  `purge --all --yes` apaga tudo; `logout --wipe` apaga também a sessão e a chave.
  O espaço é compactado e o conteúdo apagado não é recuperável.
- `hide` esconde uma conversa do Claude por completo: listas, busca, envio e compartilhamento.
- Grupos: só leitura por padrão (`allow_groups = false`).

Mais em [docs/03-SEGURANCA-LGPD.md](docs/03-SEGURANCA-LGPD.md).

## FAQ

**O Claude vê meus números?**
Não. Nem os seus, nem os dos contatos. Ele trabalha com nomes e refs opacos. Até o cartão de contato
(`share_contact`) é montado localmente, sem o número passar pelo Claude.

**O Claude pode mandar mensagem para muita gente de uma vez?**
Não. Cada envio é para um contato só, passa por uma fila única com intervalos aleatórios e limites
por minuto, hora e dia. Não existe tool de envio em lista.

**Preciso deixar o computador ligado?**
Não. Quando o `serve` não está rodando, o WhatsApp guarda as mensagens e entrega quando ele volta.

**Posso usar o WhatsApp no celular ao mesmo tempo?**
Sim. O computador é um aparelho conectado, como o WhatsApp Web.

**Como desligo tudo?**
`whatsapp-mcp logout --wipe` desvincula o aparelho e apaga os dados locais. Remova também o bloco
`whatsapp` da configuração do Claude.

**Apareceu "outra instância do whatsapp-mcp já está usando esta sessão".**
Outro processo (Claude Desktop, Claude Code ou um `login` aberto) está conectado. Feche-o e tente de novo.

## Documentação do projeto
- [Design](docs/01-DESIGN.md)
- [Tools MCP](docs/02-TOOLS.md)
- [Segurança, anti-ban e LGPD](docs/03-SEGURANCA-LGPD.md)
- [Política de segurança](docs/SECURITY.md)

## Licença e contribuições

Código sob a licença [MIT](LICENSE). Usa o [whatsmeow](https://github.com/tulir/whatsmeow) (MPL-2.0).

Este repositório é publicado **somente para leitura**: issues e pull requests não são acompanhados.
Faça um fork à vontade para adaptar ao seu uso.
