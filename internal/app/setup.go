package app

import (
	"context"
	"errors"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"bmp/internal/musiccache"
	"bmp/internal/ui"
	"bmp/internal/youtubemusic"
)

type setupStep int

const (
	setupBrowserStep setupStep = iota
	setupFolderStep
)

type setupCheckMsg struct {
	browser string
	account youtubemusic.Account
	err     error
	// signedIn is set after the sign-in window, which is not opened twice.
	signedIn bool
}

// setupBrowsers lists BMP's own sign-in window, then the Windows browsers
// yt-dlp can read cookies from. Chrome is not among those: it encrypts its
// cookies so only Chrome can read them, so Chrome users take the window.
var setupBrowsers = []struct{ label, spec string }{
	{"Sign-in window (any browser, Chrome included)", youtubemusic.ProfileBrowser},
	{"Microsoft Edge", "edge"},
	{"Mozilla Firefox", "firefox"},
	{"Brave", "brave"},
	{"Opera", "opera"},
	{"Vivaldi", "vivaldi"},
}

// setupNeeded reports whether setup has not run yet.
func setupNeeded(browser string) bool {
	return browser == ""
}

func (m *Model) openSetup() {
	m.setupOpen = true
	m.setupStep = setupBrowserStep
	m.setupChecking = false
	m.setupStatus = ""
	m.setupSelected = 0
	for index, browser := range setupBrowsers {
		if browser.spec == m.persistedSettings.Browser {
			m.setupSelected = index
		}
	}
}

func checkSetupBrowser(parent context.Context, browser string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(parent, 2*time.Minute)
		defer cancel()
		client, err := youtubemusic.New(ctx, browser)
		if err != nil {
			return setupCheckMsg{browser: browser, err: err}
		}
		account, err := client.GetAccount(ctx)
		return setupCheckMsg{browser: browser, account: account, err: err}
	}
}

// signInSetup opens BMP's sign-in window, then checks the sign-in.
func signInSetup(parent context.Context) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(parent, 15*time.Minute)
		defer cancel()
		if err := youtubemusic.SignIn(ctx); err != nil {
			return setupCheckMsg{browser: youtubemusic.ProfileBrowser, err: err, signedIn: true}
		}
		msg := checkSetupBrowser(parent, youtubemusic.ProfileBrowser)().(setupCheckMsg)
		msg.signedIn = true
		return msg
	}
}

func (m *Model) handleSetupCheck(msg setupCheckMsg) tea.Cmd {
	if errors.Is(msg.err, youtubemusic.ErrNotSignedIn) && !msg.signedIn {
		m.setupStatus = "Sign in to YouTube Music in the browser window that opened, then close the window."
		m.setupStatusError = false
		return signInSetup(m.appContext)
	}
	m.setupChecking = false
	if msg.err != nil {
		m.setupStatus = msg.err.Error()
		m.setupStatusError = true
		return nil
	}
	m.setupBrowser = msg.browser
	m.setupStatus = "Signed in as " + msg.account.Name
	m.setupStatusError = false
	m.setupStep = setupFolderStep
	m.setupFolder.SetValue(m.youtubeMusicCache.Directory())
	m.setupFolder.CursorEnd()
	return m.setupFolder.Focus()
}

func (m *Model) handleSetupKey(msg tea.KeyMsg) tea.Cmd {
	if m.setupChecking {
		return nil
	}
	if m.setupStep == setupFolderStep {
		switch msg.String() {
		case "esc":
			m.setupStep = setupBrowserStep
			m.setupStatus = ""
			m.setupFolder.Blur()
			return nil
		case "enter":
			return m.finishSetup()
		}
		var command tea.Cmd
		m.setupFolder, command = m.setupFolder.Update(msg)
		return command
	}
	switch msg.String() {
	case "up":
		m.setupSelected = (m.setupSelected - 1 + len(setupBrowsers)) % len(setupBrowsers)
	case "down":
		m.setupSelected = (m.setupSelected + 1) % len(setupBrowsers)
	case "enter":
		m.setupChecking = true
		m.setupStatus = ""
		return checkSetupBrowser(m.appContext, setupBrowsers[m.setupSelected].spec)
	case "esc":
		m.setupOpen = false
		if m.youtubeMusicClient == nil && !m.youtubeMusicLoading {
			// Load anyway so the library explains what is missing.
			m.youtubeMusicLoading = true
			return readYouTubeMusicLibrary(m.appContext, m.persistedSettings.Browser)
		}
	}
	return nil
}

// finishSetup saves both choices, moves the cache to the chosen folder and
// reloads the library with the chosen browser. Existing songs stay where
// they are.
func (m *Model) finishSetup() tea.Cmd {
	folder := strings.TrimSpace(m.setupFolder.Value())
	if m.youtubeMusicCache == nil || folder != m.youtubeMusicCache.Directory() {
		cache, err := musiccache.New(folder)
		if err != nil {
			m.setupStatus = err.Error()
			m.setupStatusError = true
			return nil
		}
		m.cancelYouTubeQueue()
		m.youtubeMusicCache, m.youtubeMusicCacheErr = cache, nil
		m.youtubeMusicCached = make(map[string]string)
		m.youtubeMusicCacheSize = -1
		m.persistedSettings.MusicDir = folder
	}
	m.persistedSettings.Browser = m.setupBrowser
	m.settingsDirty = true
	m.settingsGeneration++
	m.setupOpen = false
	m.setupFolder.Blur()
	m.youtubeMusicLoading = true
	m.youtubeMusicErr = nil
	return tea.Batch(readYouTubeMusicLibrary(m.appContext, m.persistedSettings.Browser), readMusicCacheSize(m.youtubeMusicCache))
}

func (m Model) setupView() string {
	boxWidth := min(72, m.width-8)
	contentWidth := max(10, boxWidth-4)
	muted := lipgloss.NewStyle().Foreground(m.colors.Muted)
	lines := []string{lipgloss.NewStyle().Foreground(m.colors.Magenta).Bold(true).Render("BMP SETUP"), ""}
	var hints []ui.Hint
	if m.setupStep == setupBrowserStep {
		lines = append(lines,
			lipgloss.NewStyle().Foreground(m.colors.Cyan).Bold(true).Render("STEP 1 OF 2  ·  BROWSER"),
			muted.Render("BMP signs in to YouTube Music through a browser. The sign-in window works on any PC; you sign in once. Or pick a browser where you are already signed in to music.youtube.com."),
			"",
		)
		for index, browser := range setupBrowsers {
			cursor := "  "
			if index == m.setupSelected {
				cursor = lipgloss.NewStyle().Foreground(m.colors.Magenta).Bold(true).Render("> ")
			}
			lines = append(lines, cursor+lipgloss.NewStyle().Foreground(m.colors.Foreground).Bold(index == m.setupSelected).Render(browser.label))
		}
		hints = []ui.Hint{{Key: "↑↓", Label: "select"}, {Key: "↵", Label: "sign in"}, {Key: "esc", Label: "skip"}}
	} else {
		input := m.setupFolder
		input.Width = max(1, contentWidth-lipgloss.Width(input.Prompt)-1)
		lines = append(lines,
			lipgloss.NewStyle().Foreground(m.colors.Cyan).Bold(true).Render("STEP 2 OF 2  ·  MUSIC FOLDER"),
			muted.Render("Downloaded songs are saved here. Clear it to use the default folder."),
			"",
			input.View(),
		)
		hints = []ui.Hint{{Key: "↵", Label: "finish"}, {Key: "esc", Label: "back"}}
	}
	lines = append(lines, "")
	switch {
	case m.setupChecking && m.setupStatus != "":
		lines = append(lines, lipgloss.NewStyle().Foreground(m.colors.Yellow).Render(m.setupStatus))
	case m.setupChecking:
		lines = append(lines, lipgloss.NewStyle().Foreground(m.colors.Yellow).Render("Checking "+setupBrowsers[m.setupSelected].label+" sign-in…"))
	case m.setupStatus != "":
		color := m.colors.Green
		if m.setupStatusError {
			color = m.colors.Red
		}
		lines = append(lines, lipgloss.NewStyle().Foreground(color).Render(m.setupStatus))
	}
	lines = append(lines, "", ui.Shortcuts(m.colors, hints...))
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
