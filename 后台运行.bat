@echo off
set "XIANSHUA_ROOT=%~dp0"
powershell -NoProfile -Command "Start-Process -FilePath (Join-Path $env:XIANSHUA_ROOT 'xianshua.exe') -WorkingDirectory $env:XIANSHUA_ROOT -ArgumentList @('--mode','study') -WindowStyle Hidden"
