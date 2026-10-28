param([string]$Root = $PSScriptRoot)
$ErrorActionPreference = 'Stop'
$rootPath = (Resolve-Path -LiteralPath $Root).Path.TrimEnd('\')
$backupPath = [IO.Path]::GetFullPath((Join-Path $rootPath '备份'))
if (-not $backupPath.StartsWith($rootPath + '\', [StringComparison]::OrdinalIgnoreCase)) { throw 'Invalid backup path.' }
New-Item -ItemType Directory -Path $backupPath -Force | Out-Null
$files = @(Get-ChildItem -LiteralPath $rootPath -File | Where-Object {
    $_.Extension -in @('.go', '.mod', '.sum', '.exe', '.json', '.md', '.bat', '.ps1', '.txt') -and $_.Name -notlike '*.new.exe'
})
if ($files.Count -eq 0) { throw 'No program files to back up.' }
$destination = Join-Path $backupPath ('先刷_' + (Get-Date -Format 'yyyyMMdd_HHmmss_fff') + '.zip')
Compress-Archive -LiteralPath $files.FullName -DestinationPath $destination
$archives = @(Get-ChildItem -LiteralPath $backupPath -Filter '先刷_*.zip' -File | Sort-Object Name -Descending)
foreach ($archive in @($archives | Select-Object -Skip 5)) {
    if ($archive.DirectoryName -ne $backupPath) { throw 'Refusing to remove a file outside the backup directory.' }
    Remove-Item -LiteralPath $archive.FullName
}
Write-Output ('Backup created: ' + $destination)
Write-Output ('Backups retained: ' + @(Get-ChildItem -LiteralPath $backupPath -Filter '先刷_*.zip' -File).Count + ' / 5')
