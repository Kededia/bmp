package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"bmp/internal/musicstore"
	"bmp/internal/youtubemusic"
)

// playlistSync is a change made in BMP to a playlist the account owns, sent
// to YouTube Music. Changes go one at a time, in order, so fast moves arrive
// as made and a song added a moment ago has its entry ID before it moves.
type playlistSync struct {
	kind       string // "add", "remove" or "move"
	playlistID string
	videoID    string
	// beforeVideoID is the song a moved one now precedes, empty at the end.
	beforeVideoID string
}

type playlistSyncMsg struct {
	sync       playlistSync
	setVideoID string
	err        error
}

const pickScroll = "pick"

type playlistCreatedMsg struct {
	playlist youtubemusic.Playlist
	err      error
}

func entryKey(playlistID, videoID string) string {
	return playlistID + "/" + videoID
}

// rememberEntries keeps the entry IDs of an owned playlist's songs, which
// YouTube Music needs to remove or move them.
func (m *Model) rememberEntries(playlist youtubemusic.Playlist) {
	if !playlist.Owned {
		return
	}
	for _, track := range playlist.Tracks {
		if track.SetVideoID != "" {
			m.youtubeMusicEntryIDs[entryKey(playlist.ID, track.VideoID)] = track.SetVideoID
		}
	}
}

func (m *Model) queueSync(sync playlistSync) tea.Cmd {
	m.youtubeMusicSyncs = append(m.youtubeMusicSyncs, sync)
	return m.nextSync()
}

// nextSync sends the oldest waiting change once the previous one finished.
func (m *Model) nextSync() tea.Cmd {
	if m.youtubeMusicSyncing || len(m.youtubeMusicSyncs) == 0 {
		return nil
	}
	sync := m.youtubeMusicSyncs[0]
	m.youtubeMusicSyncs = m.youtubeMusicSyncs[1:]
	m.youtubeMusicSyncing = true
	client, parent := m.youtubeMusicClient, m.appContext
	entry := m.youtubeMusicEntryIDs[entryKey(sync.playlistID, sync.videoID)]
	before := ""
	if sync.beforeVideoID != "" {
		before = m.youtubeMusicEntryIDs[entryKey(sync.playlistID, sync.beforeVideoID)]
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(parent, 30*time.Second)
		defer cancel()
		missing := errors.New("YouTube Music has not confirmed that song yet")
		var setVideoID string
		var err error
		switch {
		case sync.kind == "add":
			setVideoID, err = client.AddToPlaylist(ctx, sync.playlistID, sync.videoID)
		case entry == "" || (sync.beforeVideoID != "" && before == ""):
			err = missing
		case sync.kind == "remove":
			err = client.RemoveFromPlaylist(ctx, sync.playlistID, sync.videoID, entry)
		default:
			err = client.MoveInPlaylist(ctx, sync.playlistID, entry, before)
		}
		return playlistSyncMsg{sync: sync, setVideoID: setVideoID, err: err}
	}
}

func (m *Model) handleSync(msg playlistSyncMsg) tea.Cmd {
	m.youtubeMusicSyncing = false
	key := entryKey(msg.sync.playlistID, msg.sync.videoID)
	switch {
	case msg.err != nil:
		m.youtubeMusicDeleteStatus = "Not saved to YouTube Music: " + msg.err.Error()
		m.youtubeMusicDeleteStatusError = true
	case msg.sync.kind == "add" && msg.setVideoID != "":
		m.youtubeMusicEntryIDs[key] = msg.setVideoID
	case msg.sync.kind == "remove":
		delete(m.youtubeMusicEntryIDs, key)
	}
	return m.nextSync()
}

// beginNewPlaylist asks for a new playlist's name. From an open playlist,
// songs of it are then picked to start the new one with.
func (m *Model) beginNewPlaylist() tea.Cmd {
	m.youtubeMusicNaming = true
	m.youtubeMusicPickTracks = nil
	if m.youtubeMusicView == youtubeMusicPlaylistView {
		m.youtubeMusicPickTracks = slices.DeleteFunc(slices.Clone(m.youtubeMusicPlaylist.Tracks), func(track youtubemusic.Track) bool {
			return !track.Available || track.VideoID == ""
		})
	}
	m.youtubeMusicNameInput.SetValue("")
	return m.youtubeMusicNameInput.Focus()
}

// createPlaylist creates the playlist with tracks. Should YouTube Music not
// list them yet, the new playlist shows them without entry IDs until reloaded.
func createPlaylist(parent context.Context, client *youtubemusic.Client, title string, tracks []youtubemusic.Track) tea.Cmd {
	videoIDs := make([]string, 0, len(tracks))
	for _, track := range tracks {
		videoIDs = append(videoIDs, track.VideoID)
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(parent, time.Minute)
		defer cancel()
		playlist, err := client.CreatePlaylist(ctx, title, videoIDs)
		if err != nil {
			return playlistCreatedMsg{err: err}
		}
		if len(playlist.Tracks) == 0 {
			playlist.Tracks = tracks
		}
		return playlistCreatedMsg{playlist: playlist}
	}
}

func (m *Model) handleNameKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "esc":
		m.youtubeMusicNaming = false
		m.youtubeMusicNameInput.Blur()
		return nil
	case "enter":
		title := strings.TrimSpace(m.youtubeMusicNameInput.Value())
		if title == "" {
			return nil
		}
		m.youtubeMusicNaming = false
		m.youtubeMusicNameInput.Blur()
		if len(m.youtubeMusicPickTracks) > 0 {
			m.youtubeMusicPicking = true
			m.youtubeMusicPicked = make([]bool, len(m.youtubeMusicPickTracks))
			m.youtubeMusicPickSelected = 0
			return nil
		}
		return m.startCreate(nil)
	}
	var command tea.Cmd
	m.youtubeMusicNameInput, command = m.youtubeMusicNameInput.Update(msg)
	return command
}

func (m *Model) startCreate(tracks []youtubemusic.Track) tea.Cmd {
	title := strings.TrimSpace(m.youtubeMusicNameInput.Value())
	m.youtubeMusicDeleteStatus = "Creating " + title + "…"
	m.youtubeMusicDeleteStatusError = false
	return createPlaylist(m.appContext, m.youtubeMusicClient, title, tracks)
}

// handlePickKey ticks the songs the new playlist starts with.
func (m *Model) handlePickKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "esc":
		m.youtubeMusicPicking = false
	case "up":
		m.youtubeMusicPickSelected = max(0, m.youtubeMusicPickSelected-1)
	case "down":
		m.youtubeMusicPickSelected = min(len(m.youtubeMusicPickTracks)-1, m.youtubeMusicPickSelected+1)
	case " ", "space":
		m.youtubeMusicPicked[m.youtubeMusicPickSelected] = !m.youtubeMusicPicked[m.youtubeMusicPickSelected]
	case "a", "A":
		// Tick every song, or untick them all when all are ticked.
		all := !slices.Contains(m.youtubeMusicPicked, false)
		for index := range m.youtubeMusicPicked {
			m.youtubeMusicPicked[index] = !all
		}
	case "enter":
		var tracks []youtubemusic.Track
		for index, track := range m.youtubeMusicPickTracks {
			if m.youtubeMusicPicked[index] {
				tracks = append(tracks, track)
			}
		}
		m.youtubeMusicPicking = false
		return m.startCreate(tracks)
	}
	return nil
}

func (m Model) youtubeMusicPickView(width, height int) string {
	muted := lipgloss.NewStyle().Foreground(m.colors.Muted)
	count := 0
	for _, picked := range m.youtubeMusicPicked {
		if picked {
			count++
		}
	}
	lines := []string{
		lipgloss.NewStyle().Foreground(m.colors.Cyan).Bold(true).Render(truncate("NEW PLAYLIST  ·  "+strings.TrimSpace(m.youtubeMusicNameInput.Value()), width)),
		muted.Render(fmt.Sprintf("Tick the songs to start it with from %s  ·  %d ticked", m.youtubeMusicPlaylist.Title, count)),
		"",
	}
	selectedIndex := min(max(0, m.youtubeMusicPickSelected), len(m.youtubeMusicPickTracks)-1)
	start, end := m.selectionWindow(pickScroll, len(m.youtubeMusicPickTracks), selectedIndex, max(1, height-len(lines)))
	for index := start; index < end; index++ {
		track := m.youtubeMusicPickTracks[index]
		selected := index == selectedIndex
		cursor := "  "
		if selected {
			cursor = lipgloss.NewStyle().Foreground(m.colors.Magenta).Bold(true).Render("> ")
		}
		box := muted.Render("[ ] ")
		if m.youtubeMusicPicked[index] {
			box = lipgloss.NewStyle().Foreground(m.colors.Green).Bold(true).Render("[x] ")
		}
		details := trackArtistNames(track.Artists)
		if details != "" {
			details = "  " + details
		}
		available := max(8, width-6-lipgloss.Width(details))
		title := lipgloss.NewStyle().Foreground(m.colors.Foreground).Bold(selected).Underline(selected).Render(truncate(track.Title, available))
		lines = append(lines, cursor+box+title+muted.Render(details))
	}
	return strings.Join(lines, "\n")
}

// handleCreated lists the new playlist at the top of the library, below
// played search results, and opens it.
func (m *Model) handleCreated(msg playlistCreatedMsg) {
	if msg.err != nil {
		m.youtubeMusicDeleteStatus = "Could not create the playlist: " + msg.err.Error()
		m.youtubeMusicDeleteStatusError = true
		return
	}
	at := 0
	if len(m.youtubeMusicPlaylists) > 0 && searchPlaylist(m.youtubeMusicPlaylists[0]) {
		at = 1
	}
	m.youtubeMusicPlaylists = slices.Insert(m.youtubeMusicPlaylists, at, msg.playlist)
	m.youtubeMusicPlaylistSelected = at
	m.rememberEntries(msg.playlist)
	m.youtubeMusicPlaylist = msg.playlist
	m.youtubeMusicEdits = musicstore.Edits{}
	m.youtubeMusicView = youtubeMusicPlaylistView
	m.youtubeMusicTrackSelected = 0
	m.youtubeMusicDeleteStatus = fmt.Sprintf("Created %s with %d songs. Press / to add more.", msg.playlist.Title, len(msg.playlist.Tracks))
	m.youtubeMusicDeleteStatusError = false
}

func (m Model) youtubeMusicNameView() string {
	muted := lipgloss.NewStyle().Foreground(m.colors.Muted)
	return lipgloss.NewStyle().Foreground(m.colors.Cyan).Bold(true).Render("NEW PLAYLIST") + "\n" +
		m.youtubeMusicNameInput.View() + "\n" +
		muted.Render("A private playlist in your YouTube Music account.")
}
