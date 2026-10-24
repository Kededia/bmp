@echo off
setlocal
rem Downloads yt-dlp, Node.js and ffmpeg into bin\, which bmp.cmd adds to PATH,
rem skipping any already found on PATH. Run it again to update the bin\ copies.
pushd "%~dp0"
set "CURL=%SystemRoot%\System32\curl.exe"
set "TAR=%SystemRoot%\System32\tar.exe"
set "WORK=%TEMP%\bmp-deps"
if not exist bin mkdir bin
if exist "%WORK%" rd /s /q "%WORK%"
mkdir "%WORK%"

where /q yt-dlp && echo yt-dlp is already on PATH, skipping. && goto node
echo Downloading yt-dlp...
"%CURL%" -L --fail -o bin\yt-dlp.exe https://github.com/yt-dlp/yt-dlp/releases/latest/download/yt-dlp.exe || goto failed

:node
where /q node && echo Node.js is already on PATH, skipping. && goto ffmpeg
echo Downloading Node.js...
set "NODE_ZIP="
for /f "tokens=2" %%f in ('%CURL% -sL --fail https://nodejs.org/dist/latest/SHASUMS256.txt ^| findstr /l /c:"-win-x64.zip"') do set "NODE_ZIP=%%f"
if not defined NODE_ZIP goto failed
"%CURL%" -L --fail -o "%WORK%\node.zip" https://nodejs.org/dist/latest/%NODE_ZIP% || goto failed
"%TAR%" -xf "%WORK%\node.zip" -C "%WORK%" || goto failed
for /d %%d in ("%WORK%\node-*") do copy /y "%%d\node.exe" bin\ >nul || goto failed

:ffmpeg
where /q ffmpeg && echo ffmpeg is already on PATH, skipping. && goto done
echo Downloading ffmpeg...
"%CURL%" -L --fail -o "%WORK%\ffmpeg.zip" https://github.com/yt-dlp/FFmpeg-Builds/releases/download/latest/ffmpeg-master-latest-win64-gpl.zip || goto failed
"%TAR%" -xf "%WORK%\ffmpeg.zip" -C "%WORK%" || goto failed
for /d %%d in ("%WORK%\ffmpeg-*") do copy /y "%%d\bin\ffmpeg.exe" bin\ >nul || goto failed

:done
rd /s /q "%WORK%"
popd
echo Done.
exit /b 0

:failed
echo Installation failed.
rd /s /q "%WORK%" 2>nul
popd
exit /b 1
