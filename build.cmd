@echo off
rem Double-click to build BombeCam from source and start it. Needs Go (https://go.dev/dl/).
rem Most people should download the ready-made ZIP from the Releases page instead.
setlocal
cd /d "%~dp0"
where go >nul 2>nul
if errorlevel 1 (
  echo Go is not installed. Install it with:  winget install GoLang.Go
  echo or from https://go.dev/dl/ , then run build.cmd again.
  pause
  exit /b 1
)
rem The Osaio server key and app ID: asks once and saves them to
rem osaio-setup.txt (Enter skips one).
go run ./tools/setup -if-missing
if errorlevel 1 (
  echo The Osaio server key or app ID could not be set up, see above.
  pause
  exit /b 1
)
echo Building BombeCam, this takes a minute the first time...
powershell -NoProfile -ExecutionPolicy Bypass -File "%~dp0build.ps1"
if errorlevel 1 (
  echo Build failed.
  pause
  exit /b 1
)
echo.
echo Built bombecam.exe in this folder.
choice /m "Start BombeCam now"
if errorlevel 2 exit /b 0
start "" "%~dp0bombecam.exe"
