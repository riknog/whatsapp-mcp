# whatsapp-mcp installer for Windows.
#
#   irm https://raw.githubusercontent.com/riknog/whatsapp-mcp/main/scripts/install.ps1 | iex
#
# Downloads the latest release binary, checks its SHA-256, puts it in
# %LOCALAPPDATA%\Programs\whatsapp-mcp, adds that folder to the user PATH,
# registers the "whatsapp" MCP server in Claude Code and Claude Desktop (when
# installed) and runs "whatsapp-mcp login".
#
# Environment variables:
#   WHATSAPP_MCP_NO_LOGIN=1     skip the login step (e.g. when Claude runs this script)
#   WHATSAPP_MCP_VERSION=v0.1.0 install that release instead of the latest
#
# Messages are ASCII on purpose: Windows PowerShell 5.1 reads saved scripts
# without a BOM as ANSI.

$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
[Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12

$Repo = 'riknog/whatsapp-mcp'

function Say($msg) { Write-Host "[whatsapp-mcp] $msg" }

function Get-Arch {
    $a = $env:PROCESSOR_ARCHITEW6432
    if (-not $a) { $a = $env:PROCESSOR_ARCHITECTURE }
    switch ($a) {
        'AMD64' { return 'amd64' }
        'ARM64' { return 'arm64' }
        default { throw "Arquitetura nao suportada: $a" }
    }
}

function Get-BaseUrl {
    if ($env:WHATSAPP_MCP_VERSION) {
        return "https://github.com/$Repo/releases/download/$($env:WHATSAPP_MCP_VERSION)"
    }
    return "https://github.com/$Repo/releases/latest/download"
}

# Install-Binary downloads the binary to a temp file, checks it against
# checksums.txt and moves it into place.
function Install-Binary($dir) {
    $asset = "whatsapp-mcp_windows_$(Get-Arch).exe"
    $base = Get-BaseUrl
    $tmp = Join-Path $env:TEMP "whatsapp-mcp-$([guid]::NewGuid()).exe"

    Say "Baixando $asset..."
    Invoke-WebRequest -UseBasicParsing -Uri "$base/$asset" -OutFile $tmp
    $sums = (Invoke-WebRequest -UseBasicParsing -Uri "$base/checksums.txt").Content
    if ($sums -is [byte[]]) { $sums = [Text.Encoding]::UTF8.GetString($sums) }

    $line = ($sums -split "`n") | Where-Object { $_ -match "\s\*?$([regex]::Escape($asset))\s*$" } | Select-Object -First 1
    if (-not $line) { Remove-Item $tmp -Force; throw "checksums.txt nao lista $asset" }
    $want = ($line -split '\s+')[0].ToLower()
    $got = (Get-FileHash -Algorithm SHA256 $tmp).Hash.ToLower()
    if ($want -ne $got) { Remove-Item $tmp -Force; throw "SHA-256 nao confere para $asset (download corrompido?)" }

    New-Item -ItemType Directory -Force -Path $dir | Out-Null
    $exe = Join-Path $dir 'whatsapp-mcp.exe'
    try {
        Move-Item -Force $tmp $exe
    } catch {
        Remove-Item $tmp -Force -ErrorAction SilentlyContinue
        throw "Nao consegui substituir $exe. Feche o Claude (Desktop e Code) e rode de novo."
    }
    return $exe
}

function Add-ToUserPath($dir) {
    $path = [Environment]::GetEnvironmentVariable('Path', 'User')
    $parts = @()
    if ($path) { $parts = $path -split ';' | Where-Object { $_ } }
    if ($parts -notcontains $dir) {
        [Environment]::SetEnvironmentVariable('Path', (($parts + $dir) -join ';'), 'User')
        Say "Pasta adicionada ao PATH do usuario: $dir"
    }
    if (($env:Path -split ';') -notcontains $dir) { $env:Path = "$env:Path;$dir" }
}

function Register-ClaudeCode($exe) {
    if (-not (Get-Command claude -ErrorAction SilentlyContinue)) {
        Say 'Claude Code (comando "claude") nao encontrado; pulando.'
        return
    }
    $prev = $ErrorActionPreference
    $ErrorActionPreference = 'Continue'
    & claude mcp remove whatsapp --scope user 2>$null | Out-Null
    & claude mcp add --scope user whatsapp -- "$exe" serve | Out-Null
    $ok = $LASTEXITCODE -eq 0
    $ErrorActionPreference = $prev
    if ($ok) { Say 'Registrado no Claude Code (escopo do usuario).' }
    else { Say 'Nao consegui registrar no Claude Code. Rode: claude mcp add --scope user whatsapp -- "<caminho do exe>" serve' }
}

# Set-DesktopConfig adds or replaces mcpServers.whatsapp in one
# claude_desktop_config.json, keeping a backup of the previous file.
function Set-DesktopConfig($file, $exe) {
    $cfg = New-Object PSObject
    if (Test-Path $file) {
        $raw = [IO.File]::ReadAllText($file)
        if ($raw.Trim()) { $cfg = $raw | ConvertFrom-Json }
        Copy-Item -Force $file "$file.bak"
    }
    if (-not ($cfg.PSObject.Properties.Name -contains 'mcpServers')) {
        $cfg | Add-Member -NotePropertyName mcpServers -NotePropertyValue (New-Object PSObject)
    }
    $server = [PSCustomObject]@{ command = $exe; args = @('serve') }
    if ($cfg.mcpServers.PSObject.Properties.Name -contains 'whatsapp') {
        $cfg.mcpServers.whatsapp = $server
    } else {
        $cfg.mcpServers | Add-Member -NotePropertyName whatsapp -NotePropertyValue $server
    }
    $json = $cfg | ConvertTo-Json -Depth 32
    [IO.File]::WriteAllText($file, $json, (New-Object Text.UTF8Encoding $false))
    Say "Registrado no Claude Desktop: $file"
}

function Register-ClaudeDesktop($exe) {
    $dirs = @()
    $classic = Join-Path $env:APPDATA 'Claude'
    if (Test-Path $classic) { $dirs += $classic }
    # Microsoft Store (MSIX) installs keep AppData inside the package folder.
    Get-ChildItem -Path (Join-Path $env:LOCALAPPDATA 'Packages') -Directory -Filter 'Claude_*' -ErrorAction SilentlyContinue |
        ForEach-Object {
            $d = Join-Path $_.FullName 'LocalCache\Roaming\Claude'
            if (Test-Path $d) { $dirs += $d }
        }
    if ($dirs.Count -eq 0) {
        Say 'Claude Desktop nao encontrado; pulando.'
        return
    }
    foreach ($d in $dirs) {
        try { Set-DesktopConfig (Join-Path $d 'claude_desktop_config.json') $exe }
        catch { Say "Nao consegui editar a configuracao em ${d}: $($_.Exception.Message)" }
    }
}

$dir = Join-Path $env:LOCALAPPDATA 'Programs\whatsapp-mcp'
$exe = Install-Binary $dir
Add-ToUserPath $dir
& $exe version
Register-ClaudeCode $exe
Register-ClaudeDesktop $exe

if ($env:WHATSAPP_MCP_NO_LOGIN -eq '1') {
    Say 'Instalado. Falta vincular o WhatsApp: abra um terminal e rode  whatsapp-mcp login'
} else {
    Say 'Agora vincule o WhatsApp: no celular, WhatsApp > Aparelhos conectados > Conectar um aparelho, e leia o QR abaixo.'
    & $exe login
}

Say 'Pronto. Reinicie o Claude (Desktop ou Code) para ele carregar o WhatsApp.'
Say 'Se o Claude Desktop e o Claude Code estiverem abertos juntos, so o primeiro consegue usar o WhatsApp.'
