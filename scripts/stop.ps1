param([string]$ConfigPath = (Join-Path (Split-Path -Parent $PSScriptRoot) 'config.json'))
$ErrorActionPreference = 'Stop'
try {
    $config = Get-Content -LiteralPath $ConfigPath -Raw -Encoding UTF8 | ConvertFrom-Json
    $address = [string]$config.global.server
    if ([string]::IsNullOrWhiteSpace($address)) { $address = '127.0.0.1:10087' }
    $target = [Uri]('http://' + $address + '/stop')
    if (!$target.IsLoopback -or $target.Scheme -ne 'http' -or $target.UserInfo) { throw 'The stop endpoint must use a loopback address.' }
    Invoke-WebRequest -Uri $target -Method Post -UseBasicParsing -TimeoutSec 5 | Out-Null
    Write-Host '已请求 study class 停止。程序会关闭活动会话后退出。'
    exit 0
} catch {
    Write-Host '无法连接 study class 本机状态服务，请确认程序正在运行且配置中的端口一致。'
    exit 1
}
