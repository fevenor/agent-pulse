# 生成可解压运行的 Windows amd64 目录和 ZIP，不创建自启动或改写宿主配置。
param([string]$Output = 'dist/windows-amd64')
$ErrorActionPreference = 'Stop'
$projectRoot = Split-Path $PSScriptRoot -Parent
& (Join-Path $PSScriptRoot 'build.ps1') -Tray
$outputPath = [IO.Path]::GetFullPath($(if ([IO.Path]::IsPathRooted($Output)) { $Output } else { Join-Path $projectRoot $Output }))
$distRoot = [IO.Path]::GetFullPath((Join-Path $projectRoot 'dist')) + [IO.Path]::DirectorySeparatorChar
if (-not $outputPath.StartsWith($distRoot, [StringComparison]::OrdinalIgnoreCase)) { throw '打包输出必须位于仓库 dist 的子目录' }
if (Test-Path -LiteralPath $outputPath) {
    # 既有产物完整移到旁边，避免旧辅助程序混入新发布包。
    Move-Item -LiteralPath $outputPath -Destination "$outputPath.previous-$([DateTime]::Now.ToString('yyyyMMddHHmmss'))"
}
New-Item -ItemType Directory -Force $outputPath | Out-Null
foreach ($name in @('agent-pulse-tray.exe')) {
    Copy-Item -LiteralPath (Join-Path $projectRoot "bin/$name") -Destination (Join-Path $outputPath $name)
}
Copy-Item -LiteralPath (Join-Path $projectRoot 'config/tray.example.yaml') -Destination $outputPath
Copy-Item -LiteralPath (Join-Path $projectRoot 'config/bridge.example.yaml') -Destination $outputPath
Copy-Item -LiteralPath (Join-Path $projectRoot 'docs/windows-client.md') -Destination (Join-Path $outputPath '使用说明.md')
$zipPath = "$outputPath.zip"
Compress-Archive -Path (Join-Path $outputPath '*') -DestinationPath $zipPath -Force
Get-FileHash -Algorithm SHA256 $zipPath | Select-Object Hash
