package app

import (
	"context"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"bmp/internal/serverapi"
	"bmp/internal/settings"
	"bmp/internal/ui"
)

type serverCheckMsg struct {
	server *serverapi.Client
	err    error
}

func newServerInputs() [3]textinput.Model {
	var inputs [3]textinput.Model
	for index, prompt := range []string{"server> ", "user> ", "password> "} {
		inputs[index] = textinput.New()
		inputs[index].Prompt = prompt
		inputs[index].CharLimit = 256
	}
	inputs[0].Placeholder = "music.example.com or http://192.168.1.10:4533"
	inputs[2].EchoMode = textinput.EchoPassword
	return inputs
}

func (m *Model) openServerSetup() tea.Cmd {
	m.serverSetupOpen = true
	m.serverChecking = false
	m.serverSetupStatus = ""
	if m.serverErr != nil {
		m.serverSetupStatus, m.serverSetupError = m.serverErr.Error(), true
	}
	saved := m.persistedSettings.Server
	for index, value := range []string{saved.URL, saved.User, saved.Password} {
		m.serverInputs[index].SetValue(value)
		m.serverInputs[index].CursorEnd()
		m.serverInputs[index].Blur()
	}
	m.serverFocus = 0
	return m.serverInputs[0].Focus()
}

// checkServer reaches the server with the login typed, before it is saved.
func checkServer(parent context.Context, server *serverapi.Client) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(parent, 15*time.Second)
		defer cancel()
		_, err := server.Songs(ctx)
		return serverCheckMsg{server: server, err: err}
	}
}

func (m *Model) handleServerSetupKey(msg tea.KeyMsg) tea.Cmd {
	if m.serverChecking {
		return nil
	}
	switch msg.String() {
	case "esc":
		m.serverSetupOpen = false
		return nil
	case "up", "shift+tab", "down", "tab":
		step := 1
		if msg.String() == "up" || msg.String() == "shift+tab" {
			step = len(m.serverInputs) - 1
		}
		m.serverInputs[m.serverFocus].Blur()
		m.serverFocus = (m.serverFocus + step) % len(m.serverInputs)
		return m.serverInputs[m.serverFocus].Focus()
	case "enter":
		address := strings.TrimSpace(m.serverInputs[0].Value())
		if address == "" {
			// No address stops sharing.
			m.cancelBackground()
			m.server, m.serverErr = nil, nil
			m.persistedSettings.Server = settings.Server{}
			m.markSettingsDirty()
			m.serverSetupOpen = false
			return nil
		}
		m.serverChecking = true
		m.serverSetupStatus = "Reaching the server…"
		m.serverSetupError = false
		return checkServer(m.appContext, serverapi.New(address, strings.TrimSpace(m.serverInputs[1].Value()), m.serverInputs[2].Value()))
	}
	var command tea.Cmd
	m.serverInputs[m.serverFocus], command = m.serverInputs[m.serverFocus].Update(msg)
	return command
}

func (m *Model) handleServerCheck(msg serverCheckMsg) tea.Cmd {
	m.serverChecking = false
	if msg.err != nil {
		m.serverSetupStatus, m.serverSetupError = msg.err.Error(), true
		return nil
	}
	m.persistedSettings.Server = settings.Server{
		URL:      strings.TrimSpace(m.serverInputs[0].Value()),
		User:     strings.TrimSpace(m.serverInputs[1].Value()),
		Password: m.serverInputs[2].Value(),
	}
	m.markSettingsDirty()
	m.serverSetupOpen = false
	m.cancelBackground()
	m.server, m.serverErr, m.serverSongs, m.serverLibrary = msg.server, nil, nil, nil
	return m.startServerSync()
}

func (m Model) serverSetupView() string {
	boxWidth := min(72, m.width-8)
	contentWidth := max(10, boxWidth-4)
	lines := []string{
		lipgloss.NewStyle().Foreground(m.colors.Magenta).Bold(true).Render("BMP SERVER"),
		"",
		lipgloss.NewStyle().Foreground(m.colors.Muted).Render("Share your music with phones through a BMP server: BMP uploads the songs it downloads and your playlists. Leave the address empty to stop sharing."),
		"",
	}
	for _, input := range m.serverInputs {
		input.Width = max(1, contentWidth-lipgloss.Width(input.Prompt)-1)
		lines = append(lines, input.View())
	}
	if m.serverSetupStatus != "" {
		color := m.colors.Yellow
		if !m.serverChecking {
			color = m.colors.Green
			if m.serverSetupError {
				color = m.colors.Red
			}
		}
		lines = append(lines, "", lipgloss.NewStyle().Foreground(color).Render(m.serverSetupStatus))
	}
	lines = append(lines, "", ui.Shortcuts(m.colors, ui.Hint{Key: "↑↓", Label: "field"}, ui.Hint{Key: "↵", Label: "save"}, ui.Hint{Key: "esc", Label: "cancel"}))
	box := lipgloss.NewStyle().
		Width(boxWidth).
		Padding(1, 2).
		Border(lipgloss.DoubleBorder()).
		BorderForeground(m.colors.Magenta).
		Foreground(m.colors.Foreground).
		Background(m.colors.Background).
		Render(strings.Join(lines, "\n"))
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, box,
		lipgloss.WithWhitespaceBackground(m.colors.Background))
}
