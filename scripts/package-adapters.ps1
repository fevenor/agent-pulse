# 生成 Windows 原生适配包与未签名索引；发布签名另行执行。
param([string]$Repository = 'dist/repository-windows', [uint64]$Sequence = 1, [switch]$NativeOnly)
$ErrorActionPreference = 'Stop'
if ($Sequence -lt 1) { throw '发布序号必须大于零' }
$projectRoot = Split-Path $PSScriptRoot -Parent
& (Join-Path $PSScriptRoot 'build.ps1') -DevelopmentTools
$repositoryPath = [IO.Path]::GetFullPath($Repository)
New-Item -ItemType Directory -Force $repositoryPath | Out-Null
$packages = @()
foreach ($adapter in @('codex', 'antigravity', 'qoderclicn')) {
    $packageDir = Join-Path $projectRoot "dist/adapter-build-windows/$adapter"
    New-Item -ItemType Directory -Force $packageDir | Out-Null
    Copy-Item -LiteralPath (Join-Path $projectRoot "bin/dev/agent-pulse-adapter-$adapter.exe") -Destination (Join-Path $packageDir 'adapter.exe')
    $manifest = Get-Content -Raw (Join-Path $projectRoot "adapters/$adapter/manifest.json") | ConvertFrom-Json
    $manifest.platform = 'windows/amd64'
    $manifest.entry = 'adapter.exe'
    $manifest.min_bridge = '0.3.0'
    $manifestPath = Join-Path $projectRoot "dist/adapter-build-windows/$adapter-manifest.json"
    [IO.File]::WriteAllText($manifestPath, ($manifest | ConvertTo-Json -Depth 12))
    $packagePath = Join-Path $repositoryPath "$adapter.json"
    & (Join-Path $projectRoot 'bin/dev/agent-pulse-adapters.exe') pack --source $packageDir --manifest $manifestPath --repository $repositoryPath --out $packagePath
    if ($LASTEXITCODE -ne 0) { throw "适配包 $adapter 构建失败" }
    $packages += Get-Content -Raw $packagePath | ConvertFrom-Json
}
# 合并跨平台仓库时只构建 Windows 命令包，通用插件统一使用 Linux 发布产物。
if (-not $NativeOnly) {
    $pluginDir = Join-Path $projectRoot 'adapters/opencode'
    $package = Get-Content -Raw (Join-Path $pluginDir 'package.json') | ConvertFrom-Json
    $previousPnpm = $env:VOLTA_FEATURE_PNPM
    try {
        $env:VOLTA_FEATURE_PNPM = '1'
        & volta run --node $package.volta.node --pnpm ($package.packageManager -replace '^pnpm@', '') pnpm --dir $pluginDir build
        if ($LASTEXITCODE -ne 0) { throw 'OpenCode 插件构建失败' }
    } finally { $env:VOLTA_FEATURE_PNPM = $previousPnpm }
    $packagePath = Join-Path $repositoryPath 'opencode.json'
    & (Join-Path $projectRoot 'bin/dev/agent-pulse-adapters.exe') pack --source (Join-Path $pluginDir 'dist') --manifest (Join-Path $pluginDir 'manifest.json') --repository $repositoryPath --out $packagePath
    if ($LASTEXITCODE -ne 0) { throw 'OpenCode 打包失败' }
    $packages += Get-Content -Raw $packagePath | ConvertFrom-Json
}
[IO.File]::WriteAllText((Join-Path $repositoryPath 'index-input.json'), (@{sequence=$Sequence;packages=$packages} | ConvertTo-Json -Depth 20))
Write-Output 'Windows 适配包和未签名索引已生成；宿主原生 hook 仍需按版本验收。'
