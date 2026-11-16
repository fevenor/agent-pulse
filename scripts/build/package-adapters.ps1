# 生成 Windows 原生适配包与未签名索引；发布签名另行执行。
param([string]$Repository = 'dist/repository-windows', [uint64]$Sequence = 1, [switch]$NativeOnly)
$ErrorActionPreference = 'Stop'
if ($Sequence -lt 1) { throw '发布序号必须大于零' }
$projectRoot = Split-Path (Split-Path $PSScriptRoot -Parent) -Parent
& (Join-Path $PSScriptRoot 'build-adapters.ps1')
$repositoryPath = [IO.Path]::GetFullPath($Repository)
New-Item -ItemType Directory -Force $repositoryPath | Out-Null
$packages = @()
foreach ($adapter in @('zcode','codex','antigravity')) {
    $packageDir = Join-Path $projectRoot "dist/adapter-build-windows/$adapter"
    New-Item -ItemType Directory -Force $packageDir | Out-Null
    Copy-Item -LiteralPath (Join-Path $projectRoot "bin/dev/agent-pulse-adapter-$adapter.exe") -Destination (Join-Path $packageDir 'adapter.exe')
    $manifest = Get-Content -Raw (Join-Path $projectRoot "adapters/$adapter/manifest.json") | ConvertFrom-Json
    $manifest.platform = 'windows/amd64'
    $manifest.entry = 'adapter.exe'
    if ($adapter -eq 'antigravity') { $manifest.command = 'Antigravity.exe'; $manifest.agent_min = '2.32.1'; $manifest.agent_max = '2.32.1' }
    $manifestPath = Join-Path $projectRoot "dist/adapter-build-windows/$adapter-manifest.json"
    [IO.File]::WriteAllText($manifestPath, ($manifest | ConvertTo-Json -Depth 12))
    $packagePath = Join-Path $repositoryPath "$adapter.json"
    & (Join-Path $projectRoot 'bin/dev/agent-pulse-adapters.exe') pack --source $packageDir --manifest $manifestPath --repository $repositoryPath --out $packagePath
    if ($LASTEXITCODE -ne 0) { throw "适配包 $adapter 构建失败" }
    $packages += Get-Content -Raw $packagePath | ConvertFrom-Json
}
# Windows交付三类原生包；-NativeOnly保留为既有构建参数。
# OpenCode 通用插件随 Linux 发布，不代表 Windows 宿主属于当前支持范围。
[IO.File]::WriteAllText((Join-Path $repositoryPath 'index-input.json'), (@{sequence=$Sequence;packages=$packages} | ConvertTo-Json -Depth 20))
Write-Output 'Windows 适配包和未签名索引已生成；当前范围为 ZCode、Codex与Antigravity，真实加载与已验能力分别记录。'
