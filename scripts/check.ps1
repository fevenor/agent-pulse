# 普通 Go 检查在 Windows 原生执行，竞态检测继续由 Linux 检查入口承担。
param([switch]$GoOnly, [switch]$Tray, [switch]$Integration)
$ErrorActionPreference = 'Stop'
if ($Integration -and -not $Tray) { throw '-Integration 需要同时指定 -Tray' }
$projectRoot = Split-Path $PSScriptRoot -Parent
function Invoke-Checked {
    param([string]$Tool, [string[]]$Arguments)
    & $Tool @Arguments
    if ($LASTEXITCODE -ne 0) { throw "$Tool 执行失败，退出码 $LASTEXITCODE" }
}
$previousToolchain = $env:GOTOOLCHAIN
$previousPnpm = $env:VOLTA_FEATURE_PNPM
$previousConsoleEncoding = [Console]::OutputEncoding
$previousOutputEncoding = $OutputEncoding
try {
    # Rust 诊断使用 UTF-8；暂时统一当前进程编码，避免中文系统代码页误解码隔离断言。
    [Console]::OutputEncoding = [Text.UTF8Encoding]::new($false)
    $OutputEncoding = [Console]::OutputEncoding
    $env:GOTOOLCHAIN = 'local'
    Invoke-Checked go @('version')
    Push-Location (Join-Path $projectRoot 'go')
    try {
        Invoke-Checked go @('test', './...')
        Invoke-Checked go @('vet', './...')
    } finally { Pop-Location }
    Write-Output 'Windows 已执行普通 Go 测试和安装器测试；竞态检测请在 Linux 执行。'
    if (-not $GoOnly) {
        $env:VOLTA_FEATURE_PNPM = '1'
        foreach ($project in @('adapters/opencode', 'web/hub')) {
            Push-Location (Join-Path $projectRoot $project)
            try {
                $package = Get-Content -Raw package.json | ConvertFrom-Json
                $versionArgs = @('run', '--node', $package.volta.node, '--pnpm', ($package.packageManager -replace '^pnpm@', ''))
                Invoke-Checked volta ($versionArgs + @('node', '--version'))
                Invoke-Checked volta ($versionArgs + @('pnpm', '--version'))
                Invoke-Checked volta ($versionArgs + @('pnpm', 'install', '--frozen-lockfile', '--ignore-scripts'))
                if ($project -eq 'adapters/opencode') {
                    Invoke-Checked volta ($versionArgs + @('pnpm', 'check'))
                    Invoke-Checked volta ($versionArgs + @('pnpm', 'test'))
                }
                Invoke-Checked volta ($versionArgs + @('pnpm', 'build'))
            } finally { Pop-Location }
        }
    }
    if ($Tray) {
        . (Join-Path $PSScriptRoot 'windows-tools.ps1')
        Initialize-CppTools
        Push-Location (Join-Path $projectRoot 'tray')
        try {
            Invoke-Checked cargo @('fmt', '--check')
            Invoke-Checked cargo @('clippy', '--release', '--locked', '--all-targets', '--', '-D', 'warnings')
            Invoke-Checked cargo @('test', '--release', '--locked')
            if ($Integration) { Invoke-Checked cargo @('test', '--release', '--locked', '--test', 'sync_services', '--', '--ignored') }
            $guardOutput = (& cargo check --release --locked --features dev-demo 2>&1 | Out-String)
            if ($LASTEXITCODE -eq 0 -or -not $guardOutput.Contains('发布构建禁止包含演示功能')) { throw '演示发布隔离检查失败' }
            # 上一步故意验证编译失败；清除已确认的预期退出码，避免 CI 将整个检查判失败。
            $global:LASTEXITCODE = 0
            Write-Output '演示发布隔离检查通过：release + dev-demo 按预期拒绝编译。'
        } finally { Pop-Location }
    }
} finally {
    [Console]::OutputEncoding = $previousConsoleEncoding
    $OutputEncoding = $previousOutputEncoding
    $env:GOTOOLCHAIN = $previousToolchain
    $env:VOLTA_FEATURE_PNPM = $previousPnpm
}
