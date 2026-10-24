package app

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"bmp/internal/musiccache"
	"bmp/internal/playback"
	"bmp/internal/youtubemusic"
)

type musicCachePhase string

const (
	musicCacheChecking    musicCachePhase = "checking"
	musicCacheResolving   musicCachePhase = "resolving"
	musicCacheDownloading musicCachePhase = "downloading"
	musicCacheReady       musicCachePhase = "ready"
	musicCacheFailed      musicCachePhase = "failed"
	musicCacheComplete    musicCachePhase = "complete"
)

type youtubeMusicCacheEvent struct {
	generation    int
	phase         musicCachePhase
	trackIndex    int
	videoID       string
	queuePosition int
	queueTotal    int
	downloaded    int64
	totalBytes    int64
	path          string
	err           error
	done          bool
}

func waitForYouTubeMusicCacheEvent(generation int, events <-chan youtubeMusicCacheEvent) tea.Cmd {
	return func() tea.Msg {
		event, open := <-events
		if !open {
			return youtubeMusicCacheEvent{generation: generation, done: true, phase: musicCacheComplete}
		}
		return event
	}
}

func cacheYouTubeMusicQueue(
	ctx context.Context,
	generation int,
	client *youtubemusic.Client,
	cache *musiccache.Cache,
	tracks []youtubemusic.Track,
	start int,
	events chan<- youtubeMusicCacheEvent,
) {
	defer close(events)
	start = max(0, min(len(tracks), start))
	indices := make([]int, 0, len(tracks))
	appendPlayable := func(from, to int) {
		for index := from; index < to; index++ {
			if tracks[index].Available && strings.TrimSpace(tracks[index].VideoID) != "" {
				indices = append(indices, index)
			}
		}
	}
	appendPlayable(start, len(tracks))
	// Finish the rest of the playlist after the playback queue has been
	// prepared, so every playlist track eventually exists in the flat cache.
	appendPlayable(0, start)
	emit := func(event youtubeMusicCacheEvent) bool {
		event.generation = generation
		event.queueTotal = len(indices)
		select {
		case events <- event:
			return true
		case <-ctx.Done():
			return false
		}
	}
	for position, index := range indices {
		track := tracks[index]
		base := youtubeMusicCacheEvent{trackIndex: index, videoID: track.VideoID, queuePosition: position + 1}
		base.phase = musicCacheChecking
		if !emit(base) {
			return
		}
		path, found, err := cache.Lookup(track.VideoID)
		if err != nil {
			base.phase, base.err = musicCacheFailed, err
			if !emit(base) {
				return
			}
			continue
		}
		if found {
			base.phase, base.path = musicCacheReady, path
			if !emit(base) {
				return
			}
			continue
		}

		base.phase = musicCacheResolving
		if !emit(base) {
			return
		}
		resolveCtx, cancelResolve := context.WithTimeout(ctx, 2*time.Minute)
		resolved, err := client.ResolveStream(resolveCtx, track.VideoID)
		cancelResolve()
		if err != nil {
			base.phase, base.err = musicCacheFailed, err
			if !emit(base) {
				return
			}
			continue
		}
		base.phase = musicCacheDownloading
		base.totalBytes = resolved.ContentLength
		if !emit(base) {
			return
		}
		lastReport := time.Time{}
		lastPercent := -1
		artist := trackArtistNames(track.Artists)
		if artist == "" {
			artist = resolved.Artist
		}
		path, err = cache.Store(ctx, track.VideoID, track.Title, artist, resolved.Container, resolved.URL, resolved.HTTPHeaders, func(progress musiccache.Progress) {
			percent := -1
			if progress.Total > 0 {
				percent = int(progress.Downloaded * 100 / progress.Total)
			}
			if percent == lastPercent && time.Since(lastReport) < 150*time.Millisecond {
				return
			}
			if time.Since(lastReport) < 150*time.Millisecond && progress.Downloaded != progress.Total {
				return
			}
			lastReport, lastPercent = time.Now(), percent
			progressEvent := base
			progressEvent.downloaded = progress.Downloaded
			progressEvent.totalBytes = progress.Total
			_ = emit(progressEvent)
		})
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return
			}
			base.phase, base.err = musicCacheFailed, err
			if !emit(base) {
				return
			}
			continue
		}
		base.phase, base.path, base.downloaded, base.totalBytes = musicCacheReady, path, 0, 0
		if !emit(base) {
			return
		}
	}
	_ = emit(youtubeMusicCacheEvent{phase: musicCacheComplete, queuePosition: len(indices), done: true})
}

func playCachedYouTubeMusicTrack(
	parent context.Context,
	generation int,
	player playback.Player,
	track youtubemusic.Track,
	trackIndex int,
	path string,
	start time.Duration,
	paused bool,
) tea.Cmd {
	return func() tea.Msg {
		var err error
		created := false
		if player == nil {
			player, err = playback.New()
			if err != nil {
				return youtubeMusicPlaybackMsg{generation: generation, trackIndex: trackIndex, videoID: track.VideoID, err: fmt.Errorf("create audio player: %w", err)}
			}
			created = true
		}
		ctx, cancel := context.WithTimeout(parent, 10*time.Second)
		defer cancel()
		source := playback.Source{Path: path, Title: track.Title, Artist: trackArtistNames(track.Artists), Duration: track.Duration}
		if err := player.Play(ctx, source, start, paused); err != nil {
			if created {
				_ = player.Close()
				player = nil
			}
			return youtubeMusicPlaybackMsg{generation: generation, player: player, trackIndex: trackIndex, videoID: track.VideoID, err: err}
		}
		return youtubeMusicPlaybackMsg{generation: generation, player: player, trackIndex: trackIndex, videoID: track.VideoID}
	}
}

// recoverMusicAfterSleep replaces the Windows Media Player session and any
// in-flight download, which can hang after system sleep and block every later
// play request, then reopens the current track paused at its last position.
func (m *Model) recoverMusicAfterSleep() tea.Cmd {
	http.DefaultTransport.(*http.Transport).CloseIdleConnections()
	m.cancelYouTubeQueue()
	// Abandon any play still in flight on the old session.
	m.youtubeMusicPlayGeneration++
	m.youtubeMusicResolving = false
	player := m.youtubeMusicPlayer
	if player == nil {
		return nil
	}
	snapshot := m.youtubeMusicPlayback
	m.youtubeMusicPlayer = nil
	if m.spectrumAnalyzer != nil {
		m.spectrumAnalyzer.Sync("", false, 0)
	}
	closePlayer := func() tea.Msg {
		_ = player.Close()
		return nil
	}
	index := m.youtubeMusicPlayingIndex
	if index < 0 || index >= len(m.youtubeMusicQueue.Tracks) || snapshot.State == playback.StateEnded ||
		snapshot.State == playback.StateStopped || snapshot.State == playback.StateIdle {
		m.youtubeMusicPlayback = playback.Snapshot{State: playback.StateStopped, Volume: snapshot.Volume, Muted: snapshot.Muted}
		return closePlayer
	}
	m.musicRestoreTrackIndex = index
	m.musicRestorePosition = snapshot.Position
	m.musicRestorePaused = true
	return tea.Batch(closePlayer, m.requestYouTubeTrack(index))
}

func (m *Model) requestYouTubeTrack(index int) tea.Cmd {
	if index < 0 || index >= len(m.youtubeMusicQueue.Tracks) || m.youtubeMusicResolving {
		return nil
	}
	track := m.youtubeMusicQueue.Tracks[index]
	if !track.Available || strings.TrimSpace(track.VideoID) == "" {
		m.youtubeMusicPlaybackErr = youtubemusic.ErrUnavailable
		return nil
	}
	if path := m.youtubeMusicCached[track.VideoID]; path != "" {
		return m.beginCachedYouTubeTrack(index, path)
	}
	if m.youtubeMusicCache != nil {
		if path, found, err := m.youtubeMusicCache.Lookup(track.VideoID); err == nil && found {
			m.youtubeMusicCached[track.VideoID] = path
			return m.beginCachedYouTubeTrack(index, path)
		}
	}
	return m.beginYouTubeQueue(index)
}

func (m *Model) beginCachedYouTubeTrack(index int, path string) tea.Cmd {
	if index < 0 || index >= len(m.youtubeMusicQueue.Tracks) || path == "" {
		return nil
	}
	start, paused := time.Duration(0), false
	if index == m.musicRestoreTrackIndex {
		start, paused = m.musicRestorePosition, m.musicRestorePaused
		m.musicRestoreTrackIndex, m.musicRestorePosition, m.musicRestorePaused = -1, 0, false
	}
	m.youtubeMusicResolving = true
	m.youtubeMusicPendingIndex = -1
	m.youtubeMusicPlaybackErr = nil
	m.youtubeMusicPlayback.State = playback.StateOpening
	m.youtubeMusicPlayGeneration++
	return playCachedYouTubeMusicTrack(
		m.appContext, m.youtubeMusicPlayGeneration, m.youtubeMusicPlayer, m.youtubeMusicQueue.Tracks[index], index, path, start, paused,
	)
}

// queueBrowsedPlaylist makes the open playlist the one playback runs from.
// Positions in the previous queue no longer apply.
func (m *Model) queueBrowsedPlaylist() {
	m.cancelYouTubeQueue()
	m.youtubeMusicQueue = m.youtubeMusicPlaylist
	m.youtubeMusicQueue.Tracks = append([]youtubemusic.Track(nil), m.youtubeMusicPlaylist.Tracks...)
	m.youtubeMusicPlayingIndex = -1
	for index, track := range m.youtubeMusicQueue.Tracks {
		if track.VideoID == m.youtubeMusicPlayingVideoID {
			m.youtubeMusicPlayingIndex = index
			break
		}
	}
	m.youtubeMusicLoopTrackIndex = -1
	m.youtubeMusicShuffleHistory = nil
	m.musicRestoreTrackIndex = -1
}

// browsingQueue reports whether the open playlist is the queue, so its rows
// share the queue's playing, loop and download positions.
func (m Model) browsingQueue() bool {
	return m.youtubeMusicQueue.ID != "" && m.youtubeMusicPlaylist.ID == m.youtubeMusicQueue.ID
}

// refreshPlayback reads the player and, when the track has just ended,
// returns the command that starts the next one.
func (m *Model) refreshPlayback() tea.Cmd {
	previousState := m.youtubeMusicPlayback.State
	m.youtubeMusicPlayback = m.youtubeMusicPlayer.Snapshot()
	m.syncMusicSpectrum()
	if m.youtubeMusicPlayback.Err != nil {
		m.youtubeMusicPlaybackErr = m.youtubeMusicPlayback.Err
	}
	if previousState == playback.StateEnded || m.youtubeMusicPlayback.State != playback.StateEnded || m.youtubeMusicResolving {
		return nil
	}
	next := automaticTrackIndex(m.youtubeMusicQueue.Tracks, m.youtubeMusicPlayingIndex, m.youtubeMusicLoopTrackIndex, m.youtubeMusicShuffle)
	return m.requestYouTubeTrack(next)
}

func (m *Model) cancelYouTubeQueue() {
	if m.youtubeMusicCacheCancel != nil {
		m.youtubeMusicCacheCancel()
		m.youtubeMusicCacheCancel = nil
	}
	m.youtubeMusicCacheGeneration++
	m.youtubeMusicCacheEvents = nil
	m.youtubeMusicCacheStatus = youtubeMusicCacheEvent{}
	// Without a pending index, resolving means a cached play is in flight; it
	// keeps blocking new requests so two plays never race on one player.
	if m.youtubeMusicPendingIndex >= 0 {
		m.youtubeMusicResolving = false
	}
	m.youtubeMusicPendingIndex = -1
}

func (m *Model) beginYouTubeQueue(index int) tea.Cmd {
	if m.youtubeMusicCacheErr != nil {
		m.youtubeMusicPlaybackErr = m.youtubeMusicCacheErr
		return nil
	}
	if m.youtubeMusicCache == nil || m.youtubeMusicClient == nil {
		m.youtubeMusicPlaybackErr = errors.New("music cache or YouTube Music is not configured")
		return nil
	}
	m.cancelYouTubeQueue()
	ctx, cancel := context.WithCancel(m.appContext)
	m.youtubeMusicCacheCancel = cancel
	generation := m.youtubeMusicCacheGeneration
	events := make(chan youtubeMusicCacheEvent, 16)
	m.youtubeMusicCacheEvents = events
	m.youtubeMusicPendingIndex = index
	m.youtubeMusicResolving = true
	m.youtubeMusicPlaybackErr = nil
	m.youtubeMusicCacheStatus = youtubeMusicCacheEvent{
		generation: generation, phase: musicCacheChecking, trackIndex: index, videoID: m.youtubeMusicQueue.Tracks[index].VideoID,
	}
	tracks := append([]youtubemusic.Track(nil), m.youtubeMusicQueue.Tracks...)
	go cacheYouTubeMusicQueue(ctx, generation, m.youtubeMusicClient, m.youtubeMusicCache, tracks, index, events)
	return waitForYouTubeMusicCacheEvent(generation, events)
}

func applyYouTubeMusicPreferences(parent context.Context, player playback.Player, volume int, muted bool) tea.Cmd {
	return func() tea.Msg {
		if player == nil {
			return youtubeMusicControlMsg{err: errors.New("no active YouTube Music playback")}
		}
		ctx, cancel := context.WithTimeout(parent, 3*time.Second)
		defer cancel()
		if err := player.SetVolume(ctx, max(0, min(100, volume))); err != nil {
			return youtubeMusicControlMsg{snapshot: player.Snapshot(), err: err}
		}
		if player.Snapshot().Muted != muted {
			if err := player.ToggleMute(ctx); err != nil {
				return youtubeMusicControlMsg{snapshot: player.Snapshot(), err: err}
			}
		}
		return youtubeMusicControlMsg{snapshot: player.Snapshot()}
	}
}

func seekYouTubeMusic(parent context.Context, player playback.Player, delta time.Duration) tea.Cmd {
	return func() tea.Msg {
		if player == nil {
			return youtubeMusicControlMsg{err: errors.New("no active YouTube Music playback")}
		}
		ctx, cancel := context.WithTimeout(parent, 3*time.Second)
		defer cancel()
		snapshot := player.Snapshot()
		position := max(0, snapshot.Position+delta)
		if snapshot.Duration > time.Second {
			position = min(position, snapshot.Duration-time.Second)
		}
		err := player.Seek(ctx, position)
		return youtubeMusicControlMsg{snapshot: player.Snapshot(), seeked: true, err: err}
	}
}

func toggleYouTubeMusicPlayback(parent context.Context, player playback.Player) tea.Cmd {
	return func() tea.Msg {
		if player == nil {
			return youtubeMusicControlMsg{err: errors.New("no active YouTube Music playback")}
		}
		ctx, cancel := context.WithTimeout(parent, 3*time.Second)
		defer cancel()
		err := player.Toggle(ctx)
		return youtubeMusicControlMsg{snapshot: player.Snapshot(), err: err}
	}
}

func stopYouTubeMusicPlayback(parent context.Context, player playback.Player) tea.Cmd {
	return func() tea.Msg {
		if player == nil {
			return youtubeMusicControlMsg{err: errors.New("no active YouTube Music playback")}
		}
		ctx, cancel := context.WithTimeout(parent, 3*time.Second)
		defer cancel()
		err := player.Stop(ctx)
		return youtubeMusicControlMsg{snapshot: player.Snapshot(), err: err}
	}
}

func setYouTubeMusicVolume(parent context.Context, player playback.Player, delta int) tea.Cmd {
	return func() tea.Msg {
		if player == nil {
			return youtubeMusicControlMsg{err: errors.New("no active YouTube Music playback")}
		}
		ctx, cancel := context.WithTimeout(parent, 3*time.Second)
		defer cancel()
		err := player.AdjustVolume(ctx, delta)
		return youtubeMusicControlMsg{snapshot: player.Snapshot(), err: err}
	}
}

func toggleYouTubeMusicMute(parent context.Context, player playback.Player) tea.Cmd {
	return func() tea.Msg {
		if player == nil {
			return youtubeMusicControlMsg{err: errors.New("no active YouTube Music playback")}
		}
		ctx, cancel := context.WithTimeout(parent, 3*time.Second)
		defer cancel()
		err := player.ToggleMute(ctx)
		return youtubeMusicControlMsg{snapshot: player.Snapshot(), err: err}
	}
}

func loopingPlaylistTrackIndex(tracks []youtubemusic.Track, start, direction int) int {
	if index := playableTrackIndex(tracks, start, direction); index >= 0 {
		return index
	}
	if direction > 0 {
		return playableTrackIndex(tracks, 0, 1)
	}
	return playableTrackIndex(tracks, len(tracks)-1, -1)
}

func automaticTrackIndex(tracks []youtubemusic.Track, current, loopTrack int, shuffle bool) int {
	if loopTrack >= 0 && loopTrack < len(tracks) && current == loopTrack && tracks[loopTrack].Available && strings.TrimSpace(tracks[loopTrack].VideoID) != "" {
		return loopTrack
	}
	if shuffle {
		return shuffleTrackIndex(tracks, current)
	}
	return loopingPlaylistTrackIndex(tracks, current+1, 1)
}

// shuffleTrackIndex picks a random playable track other than current, falling
// back to current when it is the only playable one.
func shuffleTrackIndex(tracks []youtubemusic.Track, current int) int {
	candidates := make([]int, 0, len(tracks))
	for index, track := range tracks {
		if index != current && track.Available && strings.TrimSpace(track.VideoID) != "" {
			candidates = append(candidates, index)
		}
	}
	if len(candidates) == 0 {
		return playableTrackIndex(tracks, current, 1)
	}
	return candidates[rand.IntN(len(candidates))]
}

func playableTrackIndex(tracks []youtubemusic.Track, start, direction int) int {
	if direction == 0 {
		return -1
	}
	for index := start; index >= 0 && index < len(tracks); index += direction {
		if tracks[index].Available && strings.TrimSpace(tracks[index].VideoID) != "" {
			return index
		}
	}
	return -1
}
