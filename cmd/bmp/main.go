package main

import (
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"slices"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"bmp/internal/app"
	"bmp/internal/settings"
	"bmp/internal/update"
)

func main() {
	update.Cleanup()
	openLog()
	if !app.DetectSixel() && relaunchInWindowsTerminal() {
		return
	}
	prepareTools()
	model := app.New()
	program := tea.NewProgram(model, tea.WithAltScreen(), tea.WithOutput(syncedOutput{os.Stdout}))
	finalModel, err := program.Run()
	final, ok := finalModel.(app.Model)
	if ok {
		final.Close()
	} else {
		model.Close()
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "bmp: %v\n", err)
		os.Exit(1)
	}
	if ok && final.Restart() {
		restart()
	}
}

// restart runs the installed release in this terminal and waits for it, as
// Windows cannot replace the running program. This one, set aside as
// bmp.exe.old, gives its memory back meanwhile.
func restart() {
	if update.Executable == "" {
		return
	}
	command := exec.Command(update.Executable, "--no-relaunch")
	command.Stdin, command.Stdout, command.Stderr = os.Stdin, os.Stdout, os.Stderr
	debug.FreeOSMemory()
	if err := command.Run(); err != nil {
		var exitError *exec.ExitError
		if errors.As(err, &exitError) {
			os.Exit(exitError.ExitCode())
		}
		fmt.Fprintf(os.Stderr, "bmp: restart: %v\n", err)
		os.Exit(1)
	}
}

// openLog sends the log to bmp.log beside the settings, keeping the previous
// one as bmp.log.old once it passes 1 MB. Without it the log is dropped, as
// writing it to the terminal would break the screen.
func openLog() {
	log.SetOutput(io.Discard)
	path, err := settings.Path()
	if err != nil {
		return
	}
	path = filepath.Join(filepath.Dir(path), "bmp.log")
	if info, err := os.Stat(path); err == nil && info.Size() > 1<<20 {
		_ = os.Rename(path, path+".old")
	}
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	if file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err == nil {
		log.SetOutput(file)
	}
}

// relaunchInWindowsTerminal reopens BMP in Windows Terminal when it started
// in a terminal that cannot show the album cover, such as the console a
// double-click opens on Windows 10. The flag it passes keeps a Windows
// Terminal without Sixel from reopening it again.
func relaunchInWindowsTerminal() bool {
	if slices.Contains(os.Args[1:], "--no-relaunch") {
		return false
	}
	terminal, err := exec.LookPath("wt")
	if err != nil {
		return false
	}
	executable, err := os.Executable()
	if err != nil {
		return false
	}
	command := exec.Command(terminal, "--title", "BMP", "-d", filepath.Dir(executable), executable, "--no-relaunch")
	return command.Start() == nil
}

// syncedOutput wraps each frame Bubble Tea writes, one write per frame, in a
// synchronized update, so the terminal shows the frame whole. Otherwise it
// can show a line redrawn over the album cover before the cover drawn after
// it. Terminals without synchronized updates ignore the markers.
type syncedOutput struct {
	*os.File
}

func (output syncedOutput) Write(data []byte) (int, error) {
	frame := make([]byte, 0, len(data)+16)
	frame = append(append(append(frame, "\x1b[?2026h"...), data...), "\x1b[?2026l"...)
	if _, err := output.File.Write(frame); err != nil {
		return 0, err
	}
	return len(data), nil
}

// prepareTools adds the executable's folder and its bin\ folder to PATH, where
// install-deps.cmd puts yt-dlp, Node.js, ffmpeg and bgutil-pot, and runs
// install-deps.cmd when any of them is missing, as on a first launch. The
// script sits beside bmp.exe in a release and one folder up in a source build.
func prepareTools() {
	executable, err := os.Executable()
	if err != nil {
		return
	}
	directory := filepath.Dir(executable)
	os.Setenv("PATH", strings.Join([]string{os.Getenv("PATH"), directory, filepath.Join(directory, "bin")}, string(os.PathListSeparator)))
	for _, tool := range []string{"yt-dlp", "node", "ffmpeg", "bgutil-pot"} {
		if _, err := exec.LookPath(tool); err == nil {
			continue
		}
		for _, script := range []string{filepath.Join(directory, "install-deps.cmd"), filepath.Join(filepath.Dir(directory), "install-deps.cmd")} {
			if _, err := os.Stat(script); err != nil {
				continue
			}
			fmt.Println("BMP is downloading the tools it needs. This only happens once.")
			command := exec.Command(script)
			command.Stdin, command.Stdout, command.Stderr = os.Stdin, os.Stdout, os.Stderr
			_ = command.Run()
			return
		}
		return
	}
}
