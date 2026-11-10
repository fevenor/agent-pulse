# 默认仅构建 Windows 用户入口；测试服务通过 -DevelopmentTools 单独生成。
param([switch]$DevelopmentTools, [switch]$Demo, [switch]$Tray)
$ErrorActionPreference = 'Stop'
$projectRoot = Split-Path $PSScriptRoot -Parent
function Invoke-Checked {
    param([string]$Tool, [string[]]$Arguments)
    & $Tool @Arguments
    if ($LASTEXITCODE -ne 0) { throw "$Tool 执行失败，退出码 $LASTEXITCODE" }
}
$previousToolchain = $env:GOTOOLCHAIN
$previousEmbedded = $env:AGENT_PULSE_BRIDGE_BINARY
try {
    $env:GOTOOLCHAIN = 'local'
    $target = & go env GOOS GOARCH
    if ($LASTEXITCODE -ne 0 -or $target[0] -ne 'windows' -or $target[1] -ne 'amd64') { throw '本入口要求 Windows amd64 Go 工具链' }
    $internalOutput = Join-Path $projectRoot '.cache/build/windows'
    New-Item -ItemType Directory -Force $internalOutput | Out-Null
    Push-Location (Join-Path $projectRoot 'go')
    try {
        Invoke-Checked go @('build','-trimpath','-buildvcs=false','-ldflags','-s -w','-o', (Join-Path $internalOutput 'bridge.exe'),'./cmd/agent-pulse-bridge')
        if ($DevelopmentTools) {
            $devOutput = Join-Path $projectRoot 'bin/dev'
            New-Item -ItemType Directory -Force $devOutput | Out-Null
            Invoke-Checked go @('build','-trimpath','-buildvcs=false','-o', "$devOutput/", './cmd/agent-pulse-hub','./cmd/agent-pulse-relay','./cmd/agent-pulse-bridge','./cmd/agent-pulse-client','./cmd/agent-pulse-adapters','./cmd/agent-pulse-push-test')
            foreach ($adapter in @('codex','antigravity','qoderclicn')) {
                Invoke-Checked go @('build','-trimpath','-buildvcs=false','-ldflags',"-s -w -X main.adapterID=$adapter",'-o',(Join-Path $devOutput "agent-pulse-adapter-$adapter.exe"),'./cmd/agent-pulse-adapter')
            }
        }
    } finally { Pop-Location }
    . (Join-Path $PSScriptRoot 'windows-tools.ps1')
    Initialize-CppTools
    $env:AGENT_PULSE_BRIDGE_BINARY = Join-Path $internalOutput 'bridge.exe'
    Push-Location (Join-Path $projectRoot 'tray')
    try {
        if ($Demo) { Invoke-Checked cargo @('build','--locked','--profile','preview','--features','dev-demo') }
        else { Invoke-Checked cargo @('build','--locked','--release') }
    } finally { Pop-Location }
    $profile = if ($Demo) { 'preview' } else { 'release' }
    $destination = if ($Demo) { Join-Path $projectRoot 'bin/dev/agent-pulse-tray-demo.exe' } else { Join-Path $projectRoot 'bin/agent-pulse-tray.exe' }
    New-Item -ItemType Directory -Force (Split-Path $destination -Parent) | Out-Null
    Copy-Item -LiteralPath (Join-Path $projectRoot "tray/target/$profile/agent-pulse-tray.exe") -Destination $destination
    Write-Output "构建完成：$destination"
} finally { $env:GOTOOLCHAIN = $previousToolchain; $env:AGENT_PULSE_BRIDGE_BINARY = $previousEmbedded }
