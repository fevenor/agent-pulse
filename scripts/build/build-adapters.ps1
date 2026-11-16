# 仅构建Go接入和适配器；不要求Rust、MSVC或Windows SDK。
param([string]$Output = 'bin/dev')
$ErrorActionPreference = 'Stop'
$projectRoot = Split-Path (Split-Path $PSScriptRoot -Parent) -Parent
 $target = & go env GOOS GOARCH
if ($LASTEXITCODE -ne 0 -or $target[0] -ne 'windows' -or $target[1] -ne 'amd64') { throw '本入口要求Windows amd64 Go目标；Linux可显式交叉编译后核验产物' }
$destination = [IO.Path]::GetFullPath((Join-Path $projectRoot $Output))
New-Item -ItemType Directory -Force $destination | Out-Null
Push-Location (Join-Path $projectRoot 'go')
try {
    & go build -trimpath -buildvcs=false -o "$destination/" ./cmd/agent-pulse-bridge ./cmd/agent-pulse-adapters
    if ($LASTEXITCODE -ne 0) { throw '接入构建失败' }
    foreach ($adapter in @('zcode','codex','antigravity')) {
        & go build -trimpath -buildvcs=false -ldflags "-s -w -X main.adapterID=$adapter" -o (Join-Path $destination "agent-pulse-adapter-$adapter.exe") ./cmd/agent-pulse-adapter
        if ($LASTEXITCODE -ne 0) { throw "适配器 $adapter 构建失败" }
    }
} finally { Pop-Location }
