package app

import (
	"errors"
	"slices"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"bmp/internal/musicstore"
	"bmp/internal/playback"
	"bmp/internal/youtubemusic"
)

// applyEdits arranges a playlist fetched from YouTube by the edits made in
// BMP: songs keep the saved order, songs added in BMP stay, songs removed in
// BMP stay hidden, and songs new on YouTube go in after the song that comes
// before them there, or at the top when none does. Songs without a video ID
// cannot be arranged, so an edited playlist leaves them out.
func applyEdits(playlist youtubemusic.Playlist, edits musicstore.Edits) youtubemusic.Playlist {
	if len(edits.Order) == 0 {
		return playlist
	}
	fetched := make(map[string]youtubemusic.Track, len(playlist.Tracks))
	for _, track := range playlist.Tracks {
		if _, found := fetched[track.VideoID]; !found {
			fetched[track.VideoID] = track
		}
	}
	added := make(map[string]youtubemusic.Track, len(edits.Added))
	for _, track := range edits.Added {
		added[track.VideoID] = track
	}
	tracks := make([]youtubemusic.Track, 0, len(playlist.Tracks)+len(edits.Added))
	placed := make(map[string]bool, cap(tracks))
	for _, videoID := range edits.Order {
		track, found := fetched[videoID]
		if !found {
			track, found = added[videoID]
		}
		if found && videoID != "" && !placed[videoID] {
			tracks = append(tracks, track)
			placed[videoID] = true
		}
	}
	previous := ""
	for _, track := range playlist.Tracks {
		videoID := track.VideoID
		if videoID == "" || slices.Contains(edits.Removed, videoID) {
			continue
		}
		if !placed[videoID] {
			at := 0
			if previous != "" {
				at = slices.IndexFunc(tracks, func(placed youtubemusic.Track) bool { return placed.VideoID == previous }) + 1
			}
			tracks = slices.Insert(tracks, at, track)
			placed[videoID] = true
		}
		previous = videoID
	}
	playlist.Tracks = tracks
	return playlist
}

func searchPlaylist(playlist youtubemusic.Playlist) bool {
	return strings.HasPrefix(playlist.ID, "search:")
}

// editable reports whether the open playlist keeps edits. Search results are
// rebuilt by each search, so only library playlists do.
func (m Model) editable() bool {
	return m.youtubeMusicPlaylist.ID != "" && !searchPlaylist(m.youtubeMusicPlaylist)
}

// saveEdits stores the open playlist's edits, recording its current order.
// An owned playlist keeps none: its changes go to YouTube Music.
func (m *Model) saveEdits() {
	if m.youtubeMusicPlaylist.Owned {
		return
	}
	order := make([]string, 0, len(m.youtubeMusicPlaylist.Tracks))
	for _, track := range m.youtubeMusicPlaylist.Tracks {
		if track.VideoID != "" {
			order = append(order, track.VideoID)
		}
	}
	m.youtubeMusicEdits.Order = order
	if m.youtubeMusicStore == nil {
		m.youtubeMusicStoreErr = errors.New("music database is unavailable")
		return
	}
	m.youtubeMusicStoreErr = m.youtubeMusicStore.SaveEdits(m.youtubeMusicPlaylist.ID, m.youtubeMusicEdits)
}

func withoutTrack(tracks []youtubemusic.Track, videoID string) []youtubemusic.Track {
	return slices.DeleteFunc(tracks, func(track youtubemusic.Track) bool { return track.VideoID == videoID })
}

// addTrack appends, so existing track indices stay valid and a running
// download can keep going and start its pending track.
func (m *Model) addTrack(track youtubemusic.Track) tea.Cmd {
	if !m.editable() {
		return nil
	}
	for index, existing := range m.youtubeMusicPlaylist.Tracks {
		if existing.VideoID == track.VideoID {
			m.youtubeMusicTrackSelected = index
			m.youtubeMusicDeleteStatus = "Already in this playlist"
			m.youtubeMusicDeleteStatusError = false
			return nil
		}
	}
	queued := m.browsingQueue()
	var sync tea.Cmd
	if m.youtubeMusicPlaylist.Owned {
		sync = m.queueSync(playlistSync{kind: "add", playlistID: m.youtubeMusicPlaylist.ID, videoID: track.VideoID})
	} else {
		m.youtubeMusicEdits.Removed = slices.DeleteFunc(m.youtubeMusicEdits.Removed, func(videoID string) bool { return videoID == track.VideoID })
		m.youtubeMusicEdits.Added = append(withoutTrack(m.youtubeMusicEdits.Added, track.VideoID), track)
	}
	m.youtubeMusicPlaylist.Tracks = append(m.youtubeMusicPlaylist.Tracks, track)
	m.youtubeMusicPlaylist.TrackCount = len(m.youtubeMusicPlaylist.Tracks)
	m.saveEdits()
	m.youtubeMusicTrackSelected = len(m.youtubeMusicPlaylist.Tracks) - 1
	if !queued {
		return sync
	}
	m.syncQueue()
	if m.youtubeMusicShuffle {
		// Keep the random pick that is already downloaded.
		return sync
	}
	// The new track becomes next when the last one is playing.
	return tea.Batch(sync, m.prefetchNextTrack())
}

// moveSelectedTrack swaps the selected song with its neighbor in direction.
func (m *Model) moveSelectedTrack(direction int) tea.Cmd {
	from := m.youtubeMusicTrackSelected
	to := from + direction
	tracks := m.youtubeMusicPlaylist.Tracks
	if !m.editable() || from < 0 || from >= len(tracks) || to < 0 || to >= len(tracks) {
		return nil
	}
	tracks[from], tracks[to] = tracks[to], tracks[from]
	m.youtubeMusicTrackSelected = to
	m.saveEdits()
	var sync tea.Cmd
	if m.youtubeMusicPlaylist.Owned {
		before := ""
		if to+1 < len(tracks) {
			before = tracks[to+1].VideoID
		}
		sync = m.queueSync(playlistSync{kind: "move", playlistID: m.youtubeMusicPlaylist.ID, videoID: tracks[to].VideoID, beforeVideoID: before})
	}
	if !m.browsingQueue() {
		return sync
	}
	m.swapQueuePositions(from, to)
	m.syncQueue()
	if m.youtubeMusicShuffle {
		return sync
	}
	// A song moved right after the playing one plays next.
	return tea.Batch(sync, m.prefetchNextTrack())
}

// swapQueuePositions keeps the queue's positions on the same songs after the
// songs at a and b swap places.
func (m *Model) swapQueuePositions(a, b int) {
	swap := func(index *int) {
		if *index == a {
			*index = b
		} else if *index == b {
			*index = a
		}
	}
	for _, index := range []*int{
		&m.youtubeMusicPlayingIndex, &m.youtubeMusicLoopTrackIndex, &m.youtubeMusicNextIndex, &m.youtubeMusicPendingIndex,
		&m.youtubeMusicDownloadIndex, &m.youtubeMusicCacheStatus.trackIndex, &m.musicRestoreTrackIndex,
	} {
		swap(index)
	}
	for index := range m.youtubeMusicShuffleHistory {
		swap(&m.youtubeMusicShuffleHistory[index])
	}
	m.youtubeMusicFailed = nil
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
	if !m.editable() {
		return nil
	}
	videoID := m.youtubeMusicPlaylist.Tracks[index].VideoID
	var sync tea.Cmd
	if m.youtubeMusicPlaylist.Owned {
		sync = m.queueSync(playlistSync{kind: "remove", playlistID: m.youtubeMusicPlaylist.ID, videoID: videoID})
	} else {
		m.youtubeMusicEdits.Added = withoutTrack(m.youtubeMusicEdits.Added, videoID)
		m.youtubeMusicEdits.Removed = append(m.youtubeMusicEdits.Removed, videoID)
	}
	m.youtubeMusicPlaylist.Tracks = append(
		m.youtubeMusicPlaylist.Tracks[:index], m.youtubeMusicPlaylist.Tracks[index+1:]...,
	)
	m.youtubeMusicPlaylist.TrackCount = len(m.youtubeMusicPlaylist.Tracks)
	m.youtubeMusicTrackSelected = min(index, max(0, len(m.youtubeMusicPlaylist.Tracks)-1))
	m.saveEdits()
	if !queued {
		return sync
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
	return tea.Batch(sync, m.prefetchNextTrack())
}

// syncQueue copies an edit of the open playlist into the queue it shows.
func (m *Model) syncQueue() {
	m.youtubeMusicQueue = m.youtubeMusicPlaylist
	m.youtubeMusicQueue.Tracks = append([]youtubemusic.Track(nil), m.youtubeMusicPlaylist.Tracks...)
}

func (m *Model) beginYouTubeMusicDelete(target youtubeMusicDeleteTarget) {
	m.youtubeMusicDeleteStatus = ""
	m.youtubeMusicDeleteStatusError = false
	if !m.editable() || m.youtubeMusicTrackSelected < 0 || m.youtubeMusicTrackSelected >= len(m.youtubeMusicPlaylist.Tracks) {
		return
	}
	m.youtubeMusicDeletePending = target
	m.youtubeMusicDeleteStatus = "Press D again to remove track"
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
	if target != youtubeMusicDeleteTrack {
		return nil
	}
	command := m.removeSelectedTrack()
	if m.youtubeMusicPlaybackErr == nil {
		m.youtubeMusicDeleteStatus = "Removed track"
	}
	return command
}
