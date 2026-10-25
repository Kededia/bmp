package app

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"bmp/internal/musicstore"
	"bmp/internal/youtubemusic"
)

// searchDelay waits for a pause in typing, so each keystroke does not send a
// request.
const searchDelay = 300 * time.Millisecond

// maxLocalResults keeps downloaded matches from pushing YouTube Music's
// results out of view. Downloaded songs among those are still marked LOCAL.
const maxLocalResults = 10

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

// openSearch starts a search of downloaded songs and YouTube Music, which also
// takes a YouTube link. From a library playlist the picked song is added to
// it; otherwise it plays.
func (m *Model) openSearch() tea.Cmd {
	m.youtubeMusicSearchOpen = true
	m.youtubeMusicSearchAdding = m.youtubeMusicView == youtubeMusicPlaylistView && m.editable()
	m.youtubeMusicSearchInput.SetValue("")
	m.youtubeMusicSearchResults = nil
	m.youtubeMusicSearchSelected = 0
	m.youtubeMusicSearchLoading = false
	m.youtubeMusicSearchErr = nil
	m.youtubeMusicSearchLocal = nil
	if m.youtubeMusicCache != nil {
		if tracks, err := m.youtubeMusicCache.Tracks(); err == nil {
			m.youtubeMusicSearchLocal = tracks
			for _, track := range tracks {
				m.youtubeMusicCached[track.VideoID] = track.Path
			}
		}
	}
	return m.youtubeMusicSearchInput.Focus()
}

func (m *Model) closeSearch() {
	m.youtubeMusicSearchOpen = false
	m.youtubeMusicSearchInput.Blur()
	m.youtubeMusicSearchLocal = nil
	// Drop any search still in flight.
	m.youtubeMusicSearchGeneration++
}

func (m Model) searchQuery() string {
	return strings.TrimSpace(m.youtubeMusicSearchInput.Value())
}

// searchResults lists downloaded songs matching the query, all of them for an
// empty one, then YouTube Music's results not among them. local counts the
// downloaded ones.
func (m Model) searchResults() (tracks []youtubemusic.Track, local int) {
	query := m.searchQuery()
	if youtubeVideoID(query) != "" {
		return nil, 0
	}
	listed := make(map[string]bool)
	for _, track := range m.youtubeMusicSearchLocal {
		if query != "" && (local == maxLocalResults || !fuzzyLocalTrackMatch(track.Title+" "+track.Artist, query)) {
			continue
		}
		converted := youtubemusic.Track{VideoID: track.VideoID, Title: track.Title, Available: true}
		if track.Artist != "" {
			converted.Artists = []youtubemusic.Artist{{Name: track.Artist}}
		}
		tracks = append(tracks, converted)
		listed[track.VideoID] = true
		local++
	}
	for _, track := range m.youtubeMusicSearchResults {
		if !listed[track.VideoID] {
			tracks = append(tracks, track)
		}
	}
	return tracks, local
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
		tracks, _ := m.searchResults()
		m.youtubeMusicSearchSelected = min(max(0, len(tracks)-1), m.youtubeMusicSearchSelected+1)
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
	m.youtubeMusicSearchSelected = 0
	m.youtubeMusicSearchErr = nil
	if query := m.searchQuery(); query == "" || youtubeVideoID(query) != "" {
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
	return searchYouTubeMusic(m.appContext, m.youtubeMusicClient, msg.generation, m.searchQuery())
}

func (m *Model) handleSearchResults(msg youtubeMusicSearchMsg) {
	if msg.generation != m.youtubeMusicSearchGeneration {
		return
	}
	m.youtubeMusicSearchLoading = false
	m.youtubeMusicSearchResults, m.youtubeMusicSearchErr = msg.tracks, msg.err
}

// pickSearchResult adds or plays the selected result, or the pasted link.
// YouTube Music results shown while a newer search runs belong to the
// previous query, so they wait for it; downloaded matches are always current.
func (m *Model) pickSearchResult() tea.Cmd {
	query := m.searchQuery()
	playlistID := ""
	if m.youtubeMusicSearchAdding {
		playlistID = m.youtubeMusicPlaylist.ID
	}
	if youtubeVideoID(query) != "" {
		m.closeSearch()
		m.youtubeMusicURLResolving = true
		m.youtubeMusicPlaybackErr = nil
		return resolveYouTubeMusicURL(m.appContext, m.youtubeMusicClient, playlistID, query)
	}
	tracks, local := m.searchResults()
	selected := m.youtubeMusicSearchSelected
	if selected < 0 || selected >= len(tracks) || (selected >= local && m.youtubeMusicSearchLoading) {
		return nil
	}
	m.closeSearch()
	if m.youtubeMusicSearchAdding {
		return m.addTrack(tracks[selected])
	}
	return m.playSearchTracks(query, tracks, selected)
}

// playSearchTracks opens search results as a playlist and plays one, making
// them the queue so playback continues through them.
func (m *Model) playSearchTracks(query string, tracks []youtubemusic.Track, selected int) tea.Cmd {
	title := "Search: " + query
	if query == "" {
		title = "Downloaded songs"
	}
	m.youtubeMusicPlaylist = youtubemusic.Playlist{
		ID: "search:" + query, Title: title, TrackCount: len(tracks), Tracks: tracks,
	}
	m.youtubeMusicEdits = musicstore.Edits{}
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
	title := "SEARCH"
	pick := "play"
	if m.youtubeMusicSearchAdding {
		title = "ADD TO " + strings.ToUpper(m.youtubeMusicPlaylist.Title)
		pick = "add"
	}
	lines := []string{
		lipgloss.NewStyle().Foreground(m.colors.Cyan).Bold(true).Render(truncate(title, width)),
		m.youtubeMusicSearchInput.View(),
	}
	tracks, _ := m.searchResults()
	query := m.searchQuery()
	switch {
	case youtubeVideoID(query) != "":
		lines = append(lines, lipgloss.NewStyle().Foreground(m.colors.Green).Render("Enter to "+pick+" this link"))
	case m.youtubeMusicSearchErr != nil:
		lines = append(lines, lipgloss.NewStyle().Foreground(m.colors.Red).Render(truncate(m.youtubeMusicSearchErr.Error(), width)))
	case m.youtubeMusicSearchLoading:
		lines = append(lines, lipgloss.NewStyle().Foreground(m.colors.Yellow).Render("Searching YouTube Music…"))
	case len(tracks) == 0 && query != "":
		lines = append(lines, muted.Render("No songs found."))
	case len(tracks) == 0:
		lines = append(lines, muted.Render("Type to search, or paste a YouTube link."))
	default:
		lines = append(lines, "")
	}
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

type youtubeMusicURLTrackMsg struct {
	playlistID string
	track      youtubemusic.Track
	err        error
}

func resolveYouTubeMusicURL(parent context.Context, client *youtubemusic.Client, playlistID, rawURL string) tea.Cmd {
	return func() tea.Msg {
		videoID := youtubeVideoID(rawURL)
		if videoID == "" {
			return youtubeMusicURLTrackMsg{playlistID: playlistID, err: errors.New("invalid YouTube URL")}
		}
		if client == nil {
			return youtubeMusicURLTrackMsg{playlistID: playlistID, err: errors.New("YouTube Music is not configured")}
		}
		ctx, cancel := context.WithTimeout(parent, 2*time.Minute)
		defer cancel()
		resolved, err := client.ResolveStream(ctx, videoID)
		if err != nil {
			return youtubeMusicURLTrackMsg{playlistID: playlistID, err: err}
		}
		track := youtubemusic.Track{
			VideoID: videoID, Title: strings.TrimSpace(resolved.Title),
			Duration: resolved.Duration, Available: true,
		}
		if track.Title == "" {
			track.Title = videoID
		}
		if resolved.Artist != "" {
			track.Artists = []youtubemusic.Artist{{Name: resolved.Artist}}
		}
		return youtubeMusicURLTrackMsg{playlistID: playlistID, track: track}
	}
}

func youtubeVideoID(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	host := strings.TrimPrefix(strings.ToLower(parsed.Hostname()), "www.")
	var videoID string
	switch host {
	case "youtu.be":
		videoID = strings.Split(strings.Trim(parsed.Path, "/"), "/")[0]
	case "youtube.com", "music.youtube.com", "m.youtube.com":
		videoID = parsed.Query().Get("v")
		if videoID == "" {
			parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
			if len(parts) == 2 && (parts[0] == "shorts" || parts[0] == "embed") {
				videoID = parts[1]
			}
		}
	}
	videoID = strings.TrimSpace(videoID)
	if videoID == "" || strings.ContainsAny(videoID, "&?/#\\") {
		return ""
	}
	return videoID
}

func fuzzyLocalTrackMatch(value, query string) bool {
	valueRunes := []rune(strings.ToLower(value))
	for _, term := range strings.Fields(strings.ToLower(query)) {
		position := 0
		for _, character := range term {
			for position < len(valueRunes) && valueRunes[position] != character {
				position++
			}
			if position == len(valueRunes) {
				return false
			}
			position++
		}
	}
	return true
}
