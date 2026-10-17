package main

import (
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"

	"bmp/internal/app"
	"bmp/internal/configenv"
)

func main() {
	if err := configenv.Load(); err != nil {
		fmt.Fprintf(os.Stderr, "bmp: %v\n", err)
		os.Exit(1)
	}
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
