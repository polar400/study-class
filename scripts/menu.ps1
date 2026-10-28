param(
    [ValidateSet('Menu','Check','Run','Background','Status','Stop','Preview','Config','Help')]
    [string]$Action = 'Menu'
)
$ErrorActionPreference = 'Stop'
$packageRoot = Split-Path -Parent $PSScriptRoot
$appRoot = $PSScriptRoot
$programPath = Join-Path $appRoot 'study-class.exe'
$configPath = Join-Path $packageRoot 'config.json'
$script:ActionExitCode = 0

function Get-StatusUri {
    $config = Get-Content -LiteralPath $configPath -Raw -Encoding UTF8 | ConvertFrom-Json
    $address = [string]$config.global.server
    if ([string]::IsNullOrWhiteSpace($address)) { $address = '127.0.0.1:10087' }
    $uri = [Uri]('http://' + $address + '/')
    if (!$uri.IsLoopback -or $uri.Scheme -ne 'http' -or $uri.UserInfo) {
        throw '状态页地址必须是本机回环地址。'
    }
    return $uri
}

function Invoke-MenuAction([string]$SelectedAction) {
    $script:ActionExitCode = 0
    try {
        if ($SelectedAction -in @('Check','Run','Background','Status','Stop','Config') -and !(Test-Path -LiteralPath $configPath -PathType Leaf)) {
            Write-Host '找不到根目录的 config.json，请重新解压完整下载包。'
            $script:ActionExitCode = 2
            return
        }
        if ($SelectedAction -in @('Check','Run','Background','Preview') -and !(Test-Path -LiteralPath $programPath -PathType Leaf)) {
            Write-Host '找不到 app\study-class.exe，请重新解压完整下载包。'
            $script:ActionExitCode = 2
            return
        }
        switch ($SelectedAction) {
            'Check' {
                Push-Location -LiteralPath $appRoot
                try {
                    & $programPath --config $configPath --mode check --interactive --serve=false
                    $script:ActionExitCode = $LASTEXITCODE
                } finally { Pop-Location }
            }
            'Run' {
                Push-Location -LiteralPath $appRoot
                try {
                    & $programPath --config $configPath --mode study --interactive
                    $script:ActionExitCode = $LASTEXITCODE
                } finally { Pop-Location }
            }
            'Background' {
                $arguments = @('--mode','study','--config',('"{0}"' -f $configPath))
                Start-Process -FilePath $programPath -WorkingDirectory $appRoot -ArgumentList $arguments -WindowStyle Hidden | Out-Null
                Write-Host '已发送后台启动请求。可用菜单4查看状态；如需验证码，请改用前台运行。'
            }
            'Status' {
                $uri = Get-StatusUri
                Start-Process -FilePath $uri.AbsoluteUri | Out-Null
            }
            'Stop' {
                & powershell.exe -NoProfile -ExecutionPolicy Bypass -File (Join-Path $appRoot 'stop.ps1') -ConfigPath $configPath
                $script:ActionExitCode = $LASTEXITCODE
            }
            'Preview' {
                Push-Location -LiteralPath $appRoot
                try {
                    & $programPath --preview
                    $script:ActionExitCode = $LASTEXITCODE
                } finally { Pop-Location }
            }
            'Config' {
                Start-Process -FilePath 'notepad.exe' -ArgumentList ('"{0}"' -f $configPath) | Out-Null
            }
            'Help' {
                $guidePath = Join-Path $packageRoot 'docs\使用教程.html'
                if (!(Test-Path -LiteralPath $guidePath -PathType Leaf)) { throw '缺少本地教程。' }
                Start-Process -FilePath $guidePath | Out-Null
            }
        }
    } catch {
        Write-Host '操作未完成。请确认解压完整、配置为有效JSON、本机服务地址正确；详细运行错误可查看 app\logs。'
        $script:ActionExitCode = 1
    }
}

if ($Action -ne 'Menu') {
    Invoke-MenuAction $Action
    exit $script:ActionExitCode
}

$choices = @{ '1'='Check'; '2'='Run'; '3'='Background'; '4'='Status'; '5'='Stop'; '6'='Preview'; '7'='Config'; '8'='Help' }
while ($true) {
    Write-Host ''
    Write-Host 'study class  |  作者标识 POLAR'
    Write-Host '首次使用：7填写并保存配置 -> 1检查 -> 2运行。'
    Write-Host '1  检查账号与课程（只读）'
    Write-Host '2  前台运行'
    Write-Host '3  后台运行'
    Write-Host '4  打开本机状态页'
    Write-Host '5  停止本程序'
    Write-Host '6  预览Logo与日志（不联网）'
    Write-Host '7  打开配置文件'
    Write-Host '8  打开使用教程'
    Write-Host '0  退出菜单'
    $choice = Read-Host '请输入数字'
    if ($null -eq $choice -or $choice -eq '0') { break }
    if (!$choices.ContainsKey($choice)) { Write-Host '请输入0到8。'; continue }
    Invoke-MenuAction $choices[$choice]
    if ($script:ActionExitCode -ne 0) { Write-Host ('本次操作退出码：' + $script:ActionExitCode) }
    [void](Read-Host '按Enter返回菜单')
}
exit 0
