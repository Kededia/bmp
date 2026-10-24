@echo off
setlocal
set "PATH=%PATH%;%~dp0bin"
pushd "%~dp0"
go build -o bin\bmp.exe .\cmd\bmp || (popd & exit /b 1)
bin\bmp.exe %*
popd
