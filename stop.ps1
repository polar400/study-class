$ErrorActionPreference = 'Stop'
try {
    $configPath = Join-Path $PSScriptRoot 'config.json'
    $config = Get-Content -LiteralPath $configPath -Raw -Encoding UTF8 | ConvertFrom-Json
    $address = $config.global.server
    if (-not $address) { $address = '127.0.0.1:10087' }
    $target = [Uri]('http://' + $address + '/stop')
    if (-not [System.Net.IPAddress]::IsLoopback([System.Net.IPAddress]::Parse($target.Host))) {
        throw 'The stop endpoint must use a loopback address.'
    }
    Invoke-WebRequest -Uri $target -Method Post -UseBasicParsing -TimeoutSec 5 | Out-Null
    Write-Host 'Stop requested. Active sessions will be closed before exit.'
} catch {
    Write-Host 'Unable to contact the local status service. Check that XianShua is running and its port matches config.json.'
    exit 1
}
