package app

import (
	"context"
	"errors"
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

func (m *Model) beginNewPlaylist() tea.Cmd {
	m.youtubeMusicNaming = true
	m.youtubeMusicNameInput.SetValue("")
	return m.youtubeMusicNameInput.Focus()
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
		m.youtubeMusicDeleteStatus = "Creating " + title + "…"
		m.youtubeMusicDeleteStatusError = false
		client, parent := m.youtubeMusicClient, m.appContext
		return func() tea.Msg {
			ctx, cancel := context.WithTimeout(parent, 30*time.Second)
			defer cancel()
			playlist, err := client.CreatePlaylist(ctx, title)
			return playlistCreatedMsg{playlist: playlist, err: err}
		}
	}
	var command tea.Cmd
	m.youtubeMusicNameInput, command = m.youtubeMusicNameInput.Update(msg)
	return command
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
	m.youtubeMusicPlaylist = msg.playlist
	m.youtubeMusicEdits = musicstore.Edits{}
	m.youtubeMusicView = youtubeMusicPlaylistView
	m.youtubeMusicTrackSelected = 0
	m.youtubeMusicDeleteStatus = "Created " + msg.playlist.Title + ". Press / to add songs."
	m.youtubeMusicDeleteStatusError = false
}

func (m Model) youtubeMusicNameView() string {
	muted := lipgloss.NewStyle().Foreground(m.colors.Muted)
	return lipgloss.NewStyle().Foreground(m.colors.Cyan).Bold(true).Render("NEW PLAYLIST") + "\n" +
		m.youtubeMusicNameInput.View() + "\n" +
		muted.Render("A private playlist in your YouTube Music account.")
}
