@echo off
chcp 65001 >nul
cd /d "%~dp0"
xianshua.exe --mode study --interactive
pause
