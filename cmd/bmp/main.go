package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"bmp/internal/app"
	"bmp/internal/configenv"
)

func main() {
	if err := configenv.Load(); err != nil {
		fmt.Fprintf(os.Stderr, "bmp: %v\n", err)
		os.Exit(1)
	}
	prepareTools()
	model := app.New()
	program := tea.NewProgram(model, tea.WithAltScreen())
	finalModel, err := program.Run()
	if final, ok := finalModel.(app.Model); ok {
		final.Close()
	} else {
		model.Close()
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "bmp: %v\n", err)
		os.Exit(1)
	}
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
