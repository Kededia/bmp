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

## Build from source

With Go installed, `bmp.cmd` builds and runs BMP; `install-deps.cmd` fetches the tools.
