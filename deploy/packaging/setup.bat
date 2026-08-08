@echo off
REM serverdesk one-click setup - double-click this file (runs as administrator required)
net session >nul 2>&1
if %errorlevel% neq 0 (
  echo [FAIL] right-click -^> Run as administrator
  pause
  exit /b 1
)
powershell -NoProfile -ExecutionPolicy Bypass -File "%~dp0install-windows.ps1"
pause
