@echo off
setlocal

cd /d "%~dp0"

if "%APP_NAME%"=="" set "APP_NAME=BilibiliLiveToolsGover"
if not exist dist mkdir dist
if exist dist\linux-amd64 rmdir /s /q dist\linux-amd64
if exist dist\linux-arm64 rmdir /s /q dist\linux-arm64
if exist dist\linux-armv7 rmdir /s /q dist\linux-armv7
if exist dist\windows-amd64 rmdir /s /q dist\windows-amd64
del /q dist\*_windows_amd64.exe 2>nul
del /q dist\*_linux_amd64 2>nul
del /q dist\*_linux_arm64 2>nul
del /q dist\*_linux_armv7 2>nul

call :build windows amd64 ""
if errorlevel 1 exit /b 1

call :build linux amd64 ""
if errorlevel 1 exit /b 1

call :build linux arm64 ""
if errorlevel 1 exit /b 1

call :build linux arm 7
if errorlevel 1 exit /b 1

echo Build finished. Output directory: %cd%\dist
exit /b 0

:build
set "TARGET_OS=%~1"
set "TARGET_ARCH=%~2"
set "TARGET_ARM=%~3"
set "SUFFIX=%TARGET_ARCH%"
if not "%TARGET_ARM%"=="" set "SUFFIX=armv%TARGET_ARM%"
set "EXT="
if /I "%TARGET_OS%"=="windows" set "EXT=.exe"
set "OUTPUT=dist\%APP_NAME%_%TARGET_OS%_%SUFFIX%%EXT%"

echo ==^> Building %TARGET_OS%/%TARGET_ARCH% %TARGET_ARM%
if "%TARGET_ARM%"=="" (
  set "CGO_ENABLED=0"
  set "GOOS=%TARGET_OS%"
  set "GOARCH=%TARGET_ARCH%"
  set "GOARM="
  go build -trimpath -ldflags "-s -w" -o "%OUTPUT%" .
) else (
  set "CGO_ENABLED=0"
  set "GOOS=%TARGET_OS%"
  set "GOARCH=%TARGET_ARCH%"
  set "GOARM=%TARGET_ARM%"
  go build -trimpath -ldflags "-s -w" -o "%OUTPUT%" .
)

if errorlevel 1 (
  echo Build failed for %TARGET_OS%/%TARGET_ARCH%
  exit /b 1
)
exit /b 0
