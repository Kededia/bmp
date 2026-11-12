@echo off
rem Builds bmpd for a Linux server (x64) as bmpd in this folder.
setlocal
pushd "%~dp0"
set "GOOS=linux"
set "GOARCH=amd64"
set "CGO_ENABLED=0"
go build -trimpath -o bmpd .\cmd\bmpd || (popd & exit /b 1)
echo Built %~dp0bmpd
popd
