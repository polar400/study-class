param([switch]$SkipBackup)
$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
$versionLine = [IO.File]::ReadAllText((Join-Path $root 'src\main.go'))
$versionMatch = [regex]::Match($versionLine, '(?m)^const version = "([0-9]+\.[0-9]+\.[0-9]+)"')
if (!$versionMatch.Success) { throw '无法读取源码版本号。' }
$version = $versionMatch.Groups[1].Value
$package = Join-Path $root ('dist\study-class-v' + $version + '-windows-amd64')
$app = Join-Path $package 'app'
$docs = Join-Path $package 'docs'

if (!$SkipBackup) { & (Join-Path $PSScriptRoot '备份.ps1') -Root $root }
if (Test-Path -LiteralPath $package) {
    $unexpected = @(Get-ChildItem -LiteralPath $package -Force | Where-Object { $_.Name -notin @('启动.bat','config.json','app','docs') })
    if ($unexpected.Count -ne 0) { throw '现有构建目录包含额外文件，请另存后再构建。' }
}
New-Item -ItemType Directory -Path $app,$docs -Force | Out-Null
Push-Location -LiteralPath $root
try {
    & go build -trimpath -o (Join-Path $app 'study-class.exe') ./src
    if ($LASTEXITCODE -ne 0) { throw 'Go构建失败。' }
} finally { Pop-Location }

Copy-Item -LiteralPath (Join-Path $PSScriptRoot '启动.bat') -Destination (Join-Path $package '启动.bat') -Force
foreach ($name in @('menu.ps1','stop.ps1')) { Copy-Item -LiteralPath (Join-Path $PSScriptRoot $name) -Destination (Join-Path $app $name) -Force }
foreach ($file in @(Get-ChildItem -LiteralPath (Join-Path $root 'docs') -File)) { Copy-Item -LiteralPath $file.FullName -Destination (Join-Path $docs $file.Name) -Force }
Copy-Item -LiteralPath (Join-Path $root 'LICENSE.md') -Destination (Join-Path $docs 'LICENSE.md') -Force
if (!(Test-Path -LiteralPath (Join-Path $package 'config.json'))) {
    Copy-Item -LiteralPath (Join-Path $root 'config.example.json') -Destination (Join-Path $package 'config.json')
}
if (!(Test-Path -LiteralPath (Join-Path $docs '使用教程.html'))) { throw '缺少docs\使用教程.html，尚不能交付下载包。' }
Write-Output ('Build complete: ' + $package)
Write-Output '运行者填写根目录config.json，再双击启动.bat。'
