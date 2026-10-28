param([string]$Root = (Split-Path -Parent $PSScriptRoot))
$ErrorActionPreference = 'Stop'
Add-Type -AssemblyName System.IO.Compression
Add-Type -AssemblyName System.IO.Compression.FileSystem
$rootPath = (Resolve-Path -LiteralPath $Root).Path.TrimEnd('\')
$backupPath = [IO.Path]::GetFullPath((Join-Path $rootPath '备份'))
if (!$backupPath.StartsWith($rootPath+'\',[StringComparison]::OrdinalIgnoreCase)) { throw 'Invalid backup path.' }
New-Item -ItemType Directory -Path $backupPath -Force | Out-Null
$files = @(Get-ChildItem -LiteralPath $rootPath -File -Force | Where-Object {
    ($_.Extension -in @('.go','.mod','.sum','.exe','.json','.md','.bat','.ps1','.txt') -or $_.Name -eq '.gitignore') -and $_.Name -notlike '*.new.exe'
})
foreach ($directory in @('src','docs','scripts')) {
    $path = Join-Path $rootPath $directory
    if (Test-Path -LiteralPath $path -PathType Container) {
        $files += @(Get-ChildItem -LiteralPath $path -File -Recurse -Force | Where-Object {
            $_.FullName.Substring($rootPath.Length+1) -notmatch '(^|\\)(\.git|logs|state|reports|备份|dist)(\\|$)' -and $_.Extension -ne '.tmp'
        })
    }
}
$mainFile = Join-Path $rootPath 'src\main.go'
if (Test-Path -LiteralPath $mainFile -PathType Leaf) {
    $match = [regex]::Match([IO.File]::ReadAllText($mainFile), '(?m)^const version = "([0-9]+\.[0-9]+\.[0-9]+)"')
    if ($match.Success) {
        $exe = Join-Path $rootPath ('dist\study-class-v'+$match.Groups[1].Value+'-windows-amd64\app\study-class.exe')
        if (Test-Path -LiteralPath $exe -PathType Leaf) { $files += Get-Item -LiteralPath $exe }
    }
}
$files = @($files | Sort-Object FullName -Unique)
if ($files.Count -eq 0) { throw 'No project files to back up.' }
$destination = Join-Path $backupPath ('study-class_'+(Get-Date -Format 'yyyyMMdd_HHmmss_fff')+'.zip')
$archive = [IO.Compression.ZipFile]::Open($destination,[IO.Compression.ZipArchiveMode]::Create)
try {
    foreach ($file in $files) {
        if (!$file.FullName.StartsWith($rootPath+'\',[StringComparison]::OrdinalIgnoreCase)) { throw 'Backup input outside the project.' }
        $relative = $file.FullName.Substring($rootPath.Length+1).Replace('\','/')
        [IO.Compression.ZipFileExtensions]::CreateEntryFromFile($archive,$file.FullName,$relative,[IO.Compression.CompressionLevel]::Optimal) | Out-Null
    }
} finally { $archive.Dispose() }
foreach ($old in @(Get-ChildItem -LiteralPath $backupPath -File -Filter 'study-class_*.zip' | Sort-Object Name -Descending | Select-Object -Skip 5)) {
    if ($old.DirectoryName -ne $backupPath) { throw 'Refusing to remove a file outside the backup directory.' }
    Remove-Item -LiteralPath $old.FullName
}
Write-Output ('Backup created: ' + $destination)
Write-Output ('Backups retained: '+@(Get-ChildItem -LiteralPath $backupPath -File -Filter 'study-class_*.zip').Count+' / 5')
Write-Output '备份可能含个人配置，只留本机，不上传GitHub。'
