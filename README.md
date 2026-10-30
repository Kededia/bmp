# BMP

A terminal YouTube Music player for Windows, built to use as little memory as possible: about 100 MB while playing, decoder included, where Electron players take around 1 GB. It plays your YouTube Music library at the best quality your account gets (Opus at about 256 kbps with Premium), keeps the songs it plays in a music folder, and shows a spectrum and the album cover.

## Install

1. Download `bmp-windows-x64.zip` from [Releases](https://github.com/Kededia/bmp/releases) and unzip it anywhere.
2. Run `bmp.exe`. The first launch downloads the tools it needs (yt-dlp, Node.js, ffmpeg and bgutil-pot, about 250 MB) into a `bin` folder next to it.
3. In setup, sign in to YouTube Music in the window that opens, close it, then pick a music folder.

BMP updates itself from new releases. It needs Windows 10 or 11 with Chrome or Edge; the album cover shows in Windows Terminal, which BMP switches to when it is installed.

## Keys

| Key | Action |
| --- | --- |
| `↑` `↓` `Enter` `Backspace` | Browse, open or play, go back |
| `Space` | Play or pause |
| `←` `→` / `Shift+←` `→` | Previous or next song / seek |
| `+` `-` (`Alt` for bigger steps), `M` | Volume, mute |
| `S` / `L` | Shuffle / loop the selected song |
| `/` | Search YouTube Music, your downloads or a YouTube link; inside a playlist it adds the song |
| `N` | New playlist; inside a playlist, start one from songs you tick |
| `PgUp` `PgDn` (or `Alt+↑` `↓`), `D` | Move or delete a song in a playlist |
| `Ctrl+R` / `Ctrl+P` / `Ctrl+D` | Reload the library / themes and setup / quit |

## Share with phones (BMP server)

`bmpd` keeps your songs, playlists and play counts on a server so phones can play them through a Subsonic app (Ultrasonic, Tempo or Symfonium on Android). BMP stays the one talking to YouTube: it uploads the songs it downloads and, while it runs, slowly downloads the rest of your playlists for the server (Liked Songs only as you play its songs).

1. Build it for the server, from PowerShell: `$env:GOOS="linux"; go build -o bmpd ./cmd/bmpd; $env:GOOS=""`, and copy `bmpd` there.
2. Add a user: `bmpd -data /var/lib/bmpd adduser fabien`. Note the password and API key it prints; they are shown once.
3. Run it with a domain pointing at the server: `bmpd -data /var/lib/bmpd -domain music.example.com`. It gets its HTTPS certificate from Let's Encrypt (ports 80 and 443 must be open). As a systemd service: `ExecStart=/usr/local/bin/bmpd -data /var/lib/bmpd -domain music.example.com`.
4. In BMP, `Ctrl+P` → BMP server: enter the address, user and password.
5. In the phone app, add a server with the same address, user and password (or the API key where the app offers it).

To try it at home first, run `bmpd` without `-domain`: it serves plain HTTP on port 4533. Use `http://localhost:4533` in BMP and `http://<PC address>:4533` in the phone app on the same Wi-Fi.

## Build from source

With Go installed, `bmp.cmd` builds and runs BMP; `install-deps.cmd` fetches the tools.
