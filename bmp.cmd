@echo off
pushd "%~dp0"
go build -o bin\bmp.exe .\cmd\bmp || (popd & exit /b 1)
bin\bmp.exe %*
popd
