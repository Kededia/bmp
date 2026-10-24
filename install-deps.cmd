@echo off
setlocal
rem Downloads yt-dlp, Node.js, ffmpeg and bgutil-pot into bin\, which bmp.cmd
rem adds to PATH, skipping any already found on PATH. bgutil-pot and its yt-dlp
rem plugin create the tokens YouTube requires for YouTube Music Premium audio.
rem Run it again to update the bin\ copies.
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
where /q ffmpeg && echo ffmpeg is already on PATH, skipping. && goto pot
echo Downloading ffmpeg...
"%CURL%" -L --fail -o "%WORK%\ffmpeg.zip" https://github.com/yt-dlp/FFmpeg-Builds/releases/download/latest/ffmpeg-master-latest-win64-gpl.zip || goto failed
"%TAR%" -xf "%WORK%\ffmpeg.zip" -C "%WORK%" || goto failed
for /d %%d in ("%WORK%\ffmpeg-*") do copy /y "%%d\bin\ffmpeg.exe" bin\ >nul || goto failed

:pot
where /q bgutil-pot && echo bgutil-pot is already on PATH, skipping. && goto done
echo Downloading bgutil-pot...
"%CURL%" -L --fail -o bin\bgutil-pot.exe https://github.com/jim60105/bgutil-ytdlp-pot-provider-rs/releases/latest/download/bgutil-pot-windows-x86_64.exe || goto failed
if not exist bin\yt-dlp-plugins mkdir bin\yt-dlp-plugins
"%CURL%" -L --fail -o bin\yt-dlp-plugins\bgutil-ytdlp-pot-provider-rs.zip https://github.com/jim60105/bgutil-ytdlp-pot-provider-rs/releases/latest/download/bgutil-ytdlp-pot-provider-rs.zip || goto failed

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
