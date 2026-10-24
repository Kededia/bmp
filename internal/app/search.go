package app

import (
	"context"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"bmp/internal/youtubemusic"
)

// searchDelay waits for a pause in typing, so each keystroke does not send a
// request.
const searchDelay = 300 * time.Millisecond

const searchScroll = "search"

type youtubeMusicSearchTickMsg struct {
	generation int
}

type youtubeMusicSearchMsg struct {
	generation int
	tracks     []youtubemusic.Track
	err        error
}

func searchYouTubeMusic(parent context.Context, client *youtubemusic.Client, generation int, query string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(parent, 30*time.Second)
		defer cancel()
		tracks, err := client.Search(ctx, query)
		return youtubeMusicSearchMsg{generation: generation, tracks: tracks, err: err}
	}
}

// openSearch starts a YouTube Music song search. From a playlist the picked
// song is added to it, like A and U do; from the library it plays.
func (m *Model) openSearch() tea.Cmd {
	m.youtubeMusicSearchOpen = true
	m.youtubeMusicSearchAdding = m.youtubeMusicView == youtubeMusicPlaylistView
	m.youtubeMusicSearchInput.SetValue("")
	m.youtubeMusicSearchResults = nil
	m.youtubeMusicSearchSelected = 0
	m.youtubeMusicSearchLoading = false
	m.youtubeMusicSearchErr = nil
	return m.youtubeMusicSearchInput.Focus()
}

func (m *Model) closeSearch() {
	m.youtubeMusicSearchOpen = false
	m.youtubeMusicSearchInput.Blur()
	// Drop any search still in flight.
	m.youtubeMusicSearchGeneration++
}

func (m *Model) handleSearchKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "esc":
		m.closeSearch()
		return nil
	case "up":
		m.youtubeMusicSearchSelected = max(0, m.youtubeMusicSearchSelected-1)
		return nil
	case "down":
		m.youtubeMusicSearchSelected = min(max(0, len(m.youtubeMusicSearchResults)-1), m.youtubeMusicSearchSelected+1)
		return nil
	case "enter":
		return m.pickSearchResult()
	}
	previous := m.youtubeMusicSearchInput.Value()
	var command tea.Cmd
	m.youtubeMusicSearchInput, command = m.youtubeMusicSearchInput.Update(msg)
	if m.youtubeMusicSearchInput.Value() == previous {
		return command
	}
	m.youtubeMusicSearchGeneration++
	m.youtubeMusicSearchErr = nil
	if strings.TrimSpace(m.youtubeMusicSearchInput.Value()) == "" {
		m.youtubeMusicSearchResults = nil
		m.youtubeMusicSearchLoading = false
		return command
	}
	m.youtubeMusicSearchLoading = true
	generation := m.youtubeMusicSearchGeneration
	return tea.Batch(command, tea.Tick(searchDelay, func(time.Time) tea.Msg {
		return youtubeMusicSearchTickMsg{generation: generation}
	}))
}

func (m *Model) handleSearchTick(msg youtubeMusicSearchTickMsg) tea.Cmd {
	if msg.generation != m.youtubeMusicSearchGeneration {
		return nil
	}
	query := strings.TrimSpace(m.youtubeMusicSearchInput.Value())
	return searchYouTubeMusic(m.appContext, m.youtubeMusicClient, msg.generation, query)
}

func (m *Model) handleSearchResults(msg youtubeMusicSearchMsg) {
	if msg.generation != m.youtubeMusicSearchGeneration {
		return
	}
	m.youtubeMusicSearchLoading = false
	m.youtubeMusicSearchResults, m.youtubeMusicSearchErr = msg.tracks, msg.err
	m.youtubeMusicSearchSelected = 0
	if m.youtubeMusicCache == nil {
		return
	}
	for _, track := range msg.tracks {
		if path, found, err := m.youtubeMusicCache.Lookup(track.VideoID); err == nil && found {
			m.youtubeMusicCached[track.VideoID] = path
		}
	}
}

// pickSearchResult adds or plays the selected result. Results shown while a
// newer search runs belong to the previous query, so they wait for it.
func (m *Model) pickSearchResult() tea.Cmd {
	tracks, selected := m.youtubeMusicSearchResults, m.youtubeMusicSearchSelected
	if m.youtubeMusicSearchLoading || selected < 0 || selected >= len(tracks) {
		return nil
	}
	query := strings.TrimSpace(m.youtubeMusicSearchInput.Value())
	m.closeSearch()
	if m.youtubeMusicSearchAdding {
		return m.addTrack(tracks[selected])
	}
	// The results open as a playlist and become the queue, so playback
	// continues through them.
	m.youtubeMusicPlaylist = youtubemusic.Playlist{
		ID: "search:" + query, Title: "Search: " + query, TrackCount: len(tracks), Tracks: tracks,
	}
	m.youtubeMusicView = youtubeMusicPlaylistView
	m.youtubeMusicTrackSelected = selected
	m.youtubeMusicErr = nil
	if m.youtubeMusicStore != nil {
		videoIDs := make([]string, 0, len(tracks))
		for _, track := range tracks {
			videoIDs = append(videoIDs, track.VideoID)
		}
		m.youtubeMusicPlayCounts, m.youtubeMusicStoreErr = m.youtubeMusicStore.Counts(videoIDs)
	}
	m.queueBrowsedPlaylist()
	return m.requestYouTubeTrack(selected)
}

func (m Model) youtubeMusicSearchView(width, height int) string {
	muted := lipgloss.NewStyle().Foreground(m.colors.Muted)
	title := "SEARCH YOUTUBE MUSIC"
	if m.youtubeMusicSearchAdding {
		title = "ADD FROM YOUTUBE MUSIC"
	}
	lines := []string{
		lipgloss.NewStyle().Foreground(m.colors.Cyan).Bold(true).Render(title),
		m.youtubeMusicSearchInput.View(),
	}
	switch {
	case m.youtubeMusicSearchErr != nil:
		lines = append(lines, lipgloss.NewStyle().Foreground(m.colors.Red).Render(truncate(m.youtubeMusicSearchErr.Error(), width)))
	case m.youtubeMusicSearchLoading:
		lines = append(lines, lipgloss.NewStyle().Foreground(m.colors.Yellow).Render("Searching…"))
	case len(m.youtubeMusicSearchResults) == 0 && strings.TrimSpace(m.youtubeMusicSearchInput.Value()) != "":
		lines = append(lines, muted.Render("No songs found."))
	default:
		lines = append(lines, "")
	}
	tracks := m.youtubeMusicSearchResults
	if len(tracks) == 0 {
		return strings.Join(lines, "\n")
	}
	selectedIndex := min(max(0, m.youtubeMusicSearchSelected), len(tracks)-1)
	visible := max(1, height-len(lines))
	start, end := m.selectionWindow(searchScroll, len(tracks), selectedIndex, visible)
	for index := start; index < end; index++ {
		track := tracks[index]
		selected := index == selectedIndex
		cursor := "  "
		if selected {
			cursor = lipgloss.NewStyle().Foreground(m.colors.Magenta).Bold(true).Render("> ")
		}
		details := trackArtistNames(track.Artists)
		if track.Duration > 0 {
			if details != "" {
				details += "  ·  "
			}
			details += formatMediaTime(track.Duration)
		}
		if m.youtubeMusicCached[track.VideoID] != "" {
			if details != "" {
				details += "  ·  "
			}
			details += "LOCAL"
		}
		if details != "" {
			details = "  " + details
		}
		available := max(8, width-2-lipgloss.Width(details))
		trackTitle := lipgloss.NewStyle().Foreground(m.colors.Foreground).Bold(selected).Underline(selected).Render(truncate(track.Title, available))
		lines = append(lines, cursor+trackTitle+muted.Render(details))
	}
	return strings.Join(lines, "\n")
}
