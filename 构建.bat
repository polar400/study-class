@echo off
chcp 65001 >nul
cd /d "%~dp0"
powershell -NoProfile -ExecutionPolicy Bypass -File "%~dp0备份.ps1"
if errorlevel 1 exit /b 1
go build -trimpath -o xianshua.exe .
pause
