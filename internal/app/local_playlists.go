package app

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"bmp/internal/musiccache"
	"bmp/internal/playback"
	"bmp/internal/youtubemusic"
)

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

func (m *Model) beginLocalTrackPicker() {
	if m.youtubeMusicCache == nil {
		m.youtubeMusicPlaybackErr = errors.New("music cache is unavailable")
		return
	}
	tracks, err := m.youtubeMusicCache.Tracks()
	if err != nil {
		m.youtubeMusicPlaybackErr = err
		return
	}
	m.youtubeMusicLocalTracks = tracks
	m.youtubeMusicLocalSelected = 0
	m.youtubeMusicLocalSearch.SetValue("")
	m.youtubeMusicLocalSearch.Blur()
	m.youtubeMusicLocalSearching = false
	m.youtubeMusicAddingLocal = true
}

func applyCachedTrackArtists(playlists []youtubemusic.Playlist, cachedTracks []musiccache.LocalTrack) bool {
	artists := make(map[string]string, len(cachedTracks))
	for _, track := range cachedTracks {
		if track.Artist != "" {
			artists[track.VideoID] = track.Artist
		}
	}
	updated := false
	for playlistIndex := range playlists {
		for trackIndex := range playlists[playlistIndex].Tracks {
			track := &playlists[playlistIndex].Tracks[trackIndex]
			if trackArtistNames(track.Artists) != "" {
				continue
			}
			if artist := artists[track.VideoID]; artist != "" {
				track.Artists = []youtubemusic.Artist{{Name: artist}}
				updated = true
			}
		}
	}
	return updated
}

func (m Model) youtubeMusicFilteredLocalTracks() []musiccache.LocalTrack {
	query := strings.TrimSpace(m.youtubeMusicLocalSearch.Value())
	if query == "" {
		return m.youtubeMusicLocalTracks
	}
	tracks := make([]musiccache.LocalTrack, 0, len(m.youtubeMusicLocalTracks))
	for _, track := range m.youtubeMusicLocalTracks {
		if fuzzyLocalTrackMatch(track.Title+" "+track.Artist, query) {
			tracks = append(tracks, track)
		}
	}
	return tracks
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

func (m *Model) addCachedTrack(track musiccache.LocalTrack) tea.Cmd {
	artists := []youtubemusic.Artist(nil)
	if track.Artist != "" {
		artists = []youtubemusic.Artist{{Name: track.Artist}}
	}
	return m.addTrack(youtubemusic.Track{
		VideoID: track.VideoID, Title: track.Title, Artists: artists, Available: true,
	})
}

// addTrack appends, so existing track indices stay valid and a running
// download can keep going and start its pending track.
func (m *Model) addTrack(track youtubemusic.Track) tea.Cmd {
	queued := m.browsingQueue()
	m.makeCurrentPlaylistLocal()
	m.youtubeMusicPlaylist.Tracks = append(m.youtubeMusicPlaylist.Tracks, track)
	m.youtubeMusicPlaylist.TrackCount = len(m.youtubeMusicPlaylist.Tracks)
	m.saveCurrentLocalPlaylist()
	m.youtubeMusicTrackSelected = len(m.youtubeMusicPlaylist.Tracks) - 1
	if !queued {
		return nil
	}
	m.syncQueue()
	if m.youtubeMusicShuffle {
		// Keep the random pick that is already downloaded.
		return nil
	}
	// The new track becomes next when the last one is playing.
	return m.prefetchNextTrack()
}

func (m *Model) removeSelectedTrack() tea.Cmd {
	index := m.youtubeMusicTrackSelected
	if index < 0 || index >= len(m.youtubeMusicPlaylist.Tracks) {
		return nil
	}
	queued := m.browsingQueue()
	state := m.youtubeMusicPlayback.State
	if queued && index == m.youtubeMusicPlayingIndex &&
		state != playback.StateEnded && state != playback.StateStopped && state != playback.StateError {
		m.youtubeMusicPlaybackErr = errors.New("change tracks before removing the playing song")
		return nil
	}
	m.makeCurrentPlaylistLocal()
	m.youtubeMusicPlaylist.Tracks = append(
		m.youtubeMusicPlaylist.Tracks[:index], m.youtubeMusicPlaylist.Tracks[index+1:]...,
	)
	m.youtubeMusicPlaylist.TrackCount = len(m.youtubeMusicPlaylist.Tracks)
	m.youtubeMusicTrackSelected = min(index, max(0, len(m.youtubeMusicPlaylist.Tracks)-1))
	m.saveCurrentLocalPlaylist()
	if !queued {
		return nil
	}
	// Queue positions after index shift, so the running download, the
	// shuffle history and the failed tracks no longer match.
	m.cancelYouTubeQueue()
	m.youtubeMusicShuffleHistory = nil
	m.youtubeMusicFailed = nil
	if index < m.youtubeMusicPlayingIndex {
		m.youtubeMusicPlayingIndex--
	} else if index == m.youtubeMusicPlayingIndex {
		m.youtubeMusicPlayingIndex = -1
	}
	if index < m.youtubeMusicLoopTrackIndex {
		m.youtubeMusicLoopTrackIndex--
	} else if index == m.youtubeMusicLoopTrackIndex {
		m.youtubeMusicLoopTrackIndex = -1
	}
	m.syncQueue()
	return m.prefetchNextTrack()
}

// syncQueue copies an edit of the open playlist into the queue it shows.
func (m *Model) syncQueue() {
	m.youtubeMusicQueue = m.youtubeMusicPlaylist
	m.youtubeMusicQueue.Tracks = append([]youtubemusic.Track(nil), m.youtubeMusicPlaylist.Tracks...)
}

func (m *Model) beginYouTubeMusicDelete(target youtubeMusicDeleteTarget) {
	m.youtubeMusicDeleteStatus = ""
	m.youtubeMusicDeleteStatusError = false
	switch target {
	case youtubeMusicDeleteTrack:
		if m.youtubeMusicTrackSelected < 0 || m.youtubeMusicTrackSelected >= len(m.youtubeMusicPlaylist.Tracks) {
			return
		}
		m.youtubeMusicDeletePending = target
		m.youtubeMusicDeleteStatus = "Press D again to remove track"
	case youtubeMusicDeletePlaylist:
		if m.youtubeMusicPlaylistSelected < 0 || m.youtubeMusicPlaylistSelected >= len(m.youtubeMusicPlaylists) ||
			!m.youtubeMusicPlaylists[m.youtubeMusicPlaylistSelected].Local {
			m.youtubeMusicDeleteStatus = "Only local playlists can be deleted"
			m.youtubeMusicDeleteStatusError = true
			return
		}
		m.youtubeMusicDeletePending = target
		m.youtubeMusicDeleteStatus = "Press D again to delete playlist"
	}
	m.youtubeMusicDeleteStatusError = true
}

func (m *Model) cancelYouTubeMusicDelete() {
	m.youtubeMusicDeletePending = youtubeMusicDeleteNone
	m.youtubeMusicDeleteStatus = ""
	m.youtubeMusicDeleteStatusError = false
}

func (m *Model) confirmYouTubeMusicDelete() tea.Cmd {
	target := m.youtubeMusicDeletePending
	m.youtubeMusicDeletePending = youtubeMusicDeleteNone
	m.youtubeMusicDeleteStatus = ""
	m.youtubeMusicDeleteStatusError = false
	if target == youtubeMusicDeleteTrack {
		command := m.removeSelectedTrack()
		if m.youtubeMusicPlaybackErr == nil {
			m.youtubeMusicDeleteStatus = "Removed track"
		}
		return command
	}
	if target != youtubeMusicDeletePlaylist || m.youtubeMusicPlaylistSelected < 0 || m.youtubeMusicPlaylistSelected >= len(m.youtubeMusicPlaylists) {
		return nil
	}
	playlist := m.youtubeMusicPlaylists[m.youtubeMusicPlaylistSelected]
	if !playlist.Local || m.youtubeMusicStore == nil {
		m.youtubeMusicDeleteStatus = "Unable to delete playlist"
		m.youtubeMusicDeleteStatusError = true
		return nil
	}
	if err := m.youtubeMusicStore.DeletePlaylist(playlist.ID); err != nil {
		m.youtubeMusicStoreErr = err
		m.youtubeMusicDeleteStatus = "Unable to delete playlist"
		m.youtubeMusicDeleteStatusError = true
		return nil
	}
	m.youtubeMusicLocalPlaylists = removeLocalPlaylist(m.youtubeMusicLocalPlaylists, playlist.ID)
	m.youtubeMusicPlaylists = removeLocalPlaylist(m.youtubeMusicPlaylists, playlist.ID)
	m.youtubeMusicPlaylistSelected = min(m.youtubeMusicPlaylistSelected, max(0, len(m.youtubeMusicPlaylists)-1))
	m.youtubeMusicDeleteStatus = "Deleted playlist"
	if m.youtubeMusicPlaylist.ID == playlist.ID {
		m.youtubeMusicPlaylist = youtubemusic.Playlist{}
		m.youtubeMusicTrackSelected = 0
	}
	if m.youtubeMusicQueue.ID != playlist.ID {
		return nil
	}
	m.cancelYouTubeQueue()
	m.youtubeMusicQueue = youtubemusic.Playlist{}
	m.youtubeMusicShuffleHistory = nil
	m.youtubeMusicPlayingIndex = -1
	m.youtubeMusicPlayingVideoID = ""
	m.youtubeMusicLoopTrackIndex = -1
	m.persistedSettings.Music.Valid = false
	m.settingsDirty = true
	m.settingsGeneration++
	if m.youtubeMusicPlayer == nil {
		return nil
	}
	return stopYouTubeMusicPlayback(m.appContext, m.youtubeMusicPlayer)
}

func removeLocalPlaylist(playlists []youtubemusic.Playlist, id string) []youtubemusic.Playlist {
	for index, playlist := range playlists {
		if playlist.ID == id {
			return append(playlists[:index], playlists[index+1:]...)
		}
	}
	return playlists
}

func (m *Model) makeCurrentPlaylistLocal() {
	if m.youtubeMusicPlaylist.Local {
		return
	}
	m.youtubeMusicPlaylist.Local = true
	m.youtubeMusicPlaylist.ID = "local-" + time.Now().Format("20060102-150405.000000000")
}

func (m *Model) saveCurrentLocalPlaylist() {
	if m.youtubeMusicStore == nil {
		m.youtubeMusicStoreErr = errors.New("music database is unavailable")
		return
	}
	m.youtubeMusicStoreErr = m.youtubeMusicStore.SavePlaylist(m.youtubeMusicPlaylist)
	if m.youtubeMusicStoreErr != nil {
		return
	}
	updated := false
	for index := range m.youtubeMusicLocalPlaylists {
		if m.youtubeMusicLocalPlaylists[index].ID == m.youtubeMusicPlaylist.ID {
			m.youtubeMusicLocalPlaylists[index] = m.youtubeMusicPlaylist
			updated = true
			break
		}
	}
	if !updated {
		m.youtubeMusicLocalPlaylists = append(m.youtubeMusicLocalPlaylists, m.youtubeMusicPlaylist)
		m.youtubeMusicPlaylists = append(m.youtubeMusicPlaylists, m.youtubeMusicPlaylist)
		m.youtubeMusicPlaylistSelected = len(m.youtubeMusicPlaylists) - 1
		return
	}
	for index := range m.youtubeMusicPlaylists {
		if m.youtubeMusicPlaylists[index].ID == m.youtubeMusicPlaylist.ID {
			m.youtubeMusicPlaylists[index] = m.youtubeMusicPlaylist
			break
		}
	}
}
