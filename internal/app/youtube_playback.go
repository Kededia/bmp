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
	"bmp/internal/spectrum"
	"bmp/internal/youtubemusic"
)

type musicCachePhase string

const (
	musicCacheChecking    musicCachePhase = "checking"
	musicCacheResolving   musicCachePhase = "resolving"
	musicCacheDownloading musicCachePhase = "downloading"
	musicCacheReady       musicCachePhase = "ready"
	musicCacheFailed      musicCachePhase = "failed"
)

type youtubeMusicCacheEvent struct {
	generation int
	phase      musicCachePhase
	trackIndex int
	videoID    string
	downloaded int64
	totalBytes int64
	path       string
	err        error
	done       bool
}

type musicCacheSizeMsg struct {
	cache *musiccache.Cache
	size  int64
	err   error
}

func readMusicCacheSize(cache *musiccache.Cache) tea.Cmd {
	if cache == nil {
		return nil
	}
	return func() tea.Msg {
		size, err := cache.Size()
		return musicCacheSizeMsg{cache: cache, size: size, err: err}
	}
}

func waitForYouTubeMusicCacheEvent(generation int, events <-chan youtubeMusicCacheEvent) tea.Cmd {
	return func() tea.Msg {
		event, open := <-events
		if !open {
			return youtubeMusicCacheEvent{generation: generation, done: true}
		}
		return event
	}
}

// downloadYouTubeMusicTrack stores one track in the cache, reporting each
// phase on events.
func downloadYouTubeMusicTrack(
	ctx context.Context,
	generation int,
	client *youtubemusic.Client,
	cache *musiccache.Cache,
	track youtubemusic.Track,
	index int,
	events chan<- youtubeMusicCacheEvent,
) {
	defer close(events)
	emit := func(event youtubeMusicCacheEvent) bool {
		event.generation = generation
		select {
		case events <- event:
			return true
		case <-ctx.Done():
			return false
		}
	}
	base := youtubeMusicCacheEvent{phase: musicCacheChecking, trackIndex: index, videoID: track.VideoID}
	if !emit(base) {
		return
	}
	path, found, err := cache.Lookup(track.VideoID)
	if err != nil {
		base.phase, base.err = musicCacheFailed, err
		_ = emit(base)
		return
	}
	if found && !musiccache.Legacy(path) {
		base.phase, base.path = musicCacheReady, path
		_ = emit(base)
		return
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
		_ = emit(base)
		return
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
		_ = emit(base)
		return
	}
	base.phase, base.path, base.downloaded, base.totalBytes = musicCacheReady, path, 0, 0
	_ = emit(base)
}

func playCachedYouTubeMusicTrack(
	parent context.Context,
	generation int,
	player playback.Player,
	analyzer *spectrum.Analyzer,
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
			player, err = playback.New(analyzer)
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

type resumeMsg struct{}

// resumeFromCache starts the saved song from the music folder at once,
// rather than after signing in to YouTube Music and loading its playlist,
// which takes seconds. Until the playlist replaces it, the queue holds only
// this song.
func (m *Model) resumeFromCache() tea.Cmd {
	restored := m.persistedSettings.Music
	if !restored.Valid || m.youtubeMusicCache == nil || m.youtubeMusicPlayer != nil || m.youtubeMusicResolving {
		return nil
	}
	tracks, err := m.youtubeMusicCache.Tracks()
	if err != nil {
		return nil
	}
	for _, local := range tracks {
		if local.VideoID != restored.VideoID {
			continue
		}
		track := youtubemusic.Track{VideoID: local.VideoID, Title: local.Title, Available: true}
		if local.Artist != "" {
			track.Artists = []youtubemusic.Artist{{Name: local.Artist}}
		}
		queue := youtubemusic.Playlist{ID: restored.PlaylistID, TrackCount: 1, Tracks: []youtubemusic.Track{track}}
		if searchPlaylist(queue) {
			queue.Title = "Search: " + strings.TrimPrefix(queue.ID, "search:")
		}
		m.youtubeMusicQueue = queue
		m.musicRestoreTrackIndex = 0
		m.musicRestorePosition = max(0, time.Duration(restored.PositionMillis)*time.Millisecond)
		m.musicRestorePaused = restored.Paused
		m.syncSearchRow()
		return m.beginCachedYouTubeTrack(0, local.Path)
	}
	return nil
}

// recoverMusicAfterSleep replaces the player session and any in-flight
// download, which can hang after system sleep and block every later play
// request, then reopens the current track paused at its last position.
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
	if path := m.cachedTrackPath(index); path != "" {
		return m.beginCachedYouTubeTrack(index, path)
	}
	if m.youtubeMusicCacheErr != nil {
		m.youtubeMusicPlaybackErr = m.youtubeMusicCacheErr
		return nil
	}
	if m.youtubeMusicCache == nil || m.youtubeMusicClient == nil {
		m.youtubeMusicPlaybackErr = errors.New("music cache or YouTube Music is not configured")
		return nil
	}
	command := m.downloadTrack(index)
	m.youtubeMusicPendingIndex = index
	m.youtubeMusicCacheStatusNow = true
	m.youtubeMusicResolving = true
	m.youtubeMusicPlaybackErr = nil
	return command
}

// cachedTrackPath returns the cached file of a queue track, or "".
func (m *Model) cachedTrackPath(index int) string {
	videoID := m.youtubeMusicQueue.Tracks[index].VideoID
	if path := m.youtubeMusicCached[videoID]; path != "" {
		return path
	}
	if m.youtubeMusicCache != nil {
		if path, found, err := m.youtubeMusicCache.Lookup(videoID); err == nil && found {
			m.youtubeMusicCached[videoID] = path
			return path
		}
	}
	return ""
}

// followingTrackIndex picks the track played after current: a random one in
// shuffle, else the next playable one.
func (m Model) followingTrackIndex(current int) int {
	if m.youtubeMusicShuffle {
		return shuffleTrackIndex(m.youtubeMusicQueue.Tracks, current)
	}
	return loopingPlaylistTrackIndex(m.youtubeMusicQueue.Tracks, current+1, 1)
}

// upcomingTrackIndex is the track Right and auto-advance move to: the one
// picked and prefetched when the current track started, while still valid.
func (m Model) upcomingTrackIndex() int {
	next := m.youtubeMusicNextIndex
	if next < 0 || next >= len(m.youtubeMusicQueue.Tracks) || next == m.youtubeMusicPlayingIndex {
		return m.followingTrackIndex(m.youtubeMusicPlayingIndex)
	}
	return next
}

// prefetchNextTrack picks the track after the playing one, then fetches what
// the two still need.
func (m *Model) prefetchNextTrack() tea.Cmd {
	m.youtubeMusicNextIndex = -1
	if m.youtubeMusicPlayingIndex >= 0 {
		m.youtubeMusicNextIndex = m.followingTrackIndex(m.youtubeMusicPlayingIndex)
	}
	return m.prefetchTracks()
}

// prefetchTracks downloads, one at a time, what the playing track and the
// picked next one need: a missing file, or the best-quality replacement of
// an old one. Nothing further is fetched; songs reach the cache only as they
// come up to play.
func (m *Model) prefetchTracks() tea.Cmd {
	// A requested track or a running download comes first; each prefetches
	// again once it finishes.
	if m.youtubeMusicPendingIndex >= 0 || m.youtubeMusicResolving || m.youtubeMusicCacheCancel != nil ||
		m.youtubeMusicCache == nil || m.youtubeMusicClient == nil || m.youtubeMusicCacheErr != nil {
		return nil
	}
	for _, index := range []int{m.youtubeMusicPlayingIndex, m.youtubeMusicNextIndex} {
		if index >= 0 && index < len(m.youtubeMusicQueue.Tracks) && m.needsDownload(index) {
			return m.downloadTrack(index)
		}
	}
	return nil
}

// needsDownload reports whether prefetching should fetch a queue track: it
// is missing or only in the old 360p format, and no attempt this session
// already failed or upgraded it.
func (m *Model) needsDownload(index int) bool {
	track := m.youtubeMusicQueue.Tracks[index]
	if !track.Available || strings.TrimSpace(track.VideoID) == "" || m.youtubeMusicSkipDownload[track.VideoID] {
		return false
	}
	path := m.cachedTrackPath(index)
	return path == "" || musiccache.Legacy(path)
}

func (m *Model) skipDownload(videoID string) {
	if m.youtubeMusicSkipDownload == nil {
		m.youtubeMusicSkipDownload = make(map[string]bool)
	}
	m.youtubeMusicSkipDownload[videoID] = true
}

// skipFailedTrack moves on after a requested track fails to download,
// stopping once the skips reach a track that already failed.
func (m *Model) skipFailedTrack(failed int) tea.Cmd {
	if m.youtubeMusicFailed == nil {
		m.youtubeMusicFailed = make(map[int]bool)
	}
	m.youtubeMusicFailed[failed] = true
	next := m.followingTrackIndex(failed)
	if next < 0 || m.youtubeMusicFailed[next] {
		return nil
	}
	err := m.youtubeMusicPlaybackErr
	command := m.requestYouTubeTrack(next)
	if m.youtubeMusicPlaybackErr == nil {
		// Keep showing why the previous track was skipped.
		m.youtubeMusicPlaybackErr = err
	}
	return command
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
		m.appContext, m.youtubeMusicPlayGeneration, m.youtubeMusicPlayer, m.spectrumAnalyzer, m.youtubeMusicQueue.Tracks[index], index, path, start, paused,
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
	m.youtubeMusicNextIndex = -1
	m.youtubeMusicShuffleHistory = nil
	m.youtubeMusicFailed = nil
	m.musicRestoreTrackIndex = -1
	m.youtubeMusicQueueStale = false
	m.syncSearchRow()
}

// refreshQueue replaces the queue with the freshly read copy of its playlist
// now open, so songs added elsewhere join it. The playing and looped songs
// keep their places.
func (m *Model) refreshQueue() {
	loop := ""
	if index := m.youtubeMusicLoopTrackIndex; index >= 0 && index < len(m.youtubeMusicQueue.Tracks) {
		loop = m.youtubeMusicQueue.Tracks[index].VideoID
	}
	m.queueBrowsedPlaylist()
	for index, track := range m.youtubeMusicQueue.Tracks {
		if loop != "" && track.VideoID == loop {
			m.youtubeMusicLoopTrackIndex = index
		}
	}
}

// syncSearchRow lists search results being played at the top of the library,
// so the playing playlist can always be found and reopened there.
func (m *Model) syncSearchRow() {
	selectedID := ""
	if m.youtubeMusicPlaylistSelected >= 0 && m.youtubeMusicPlaylistSelected < len(m.youtubeMusicPlaylists) {
		selectedID = m.youtubeMusicPlaylists[m.youtubeMusicPlaylistSelected].ID
	}
	playlists := make([]youtubemusic.Playlist, 0, len(m.youtubeMusicPlaylists)+1)
	if searchPlaylist(m.youtubeMusicQueue) {
		playlists = append(playlists, m.youtubeMusicQueue)
	}
	for _, playlist := range m.youtubeMusicPlaylists {
		if !searchPlaylist(playlist) {
			playlists = append(playlists, playlist)
		}
	}
	m.youtubeMusicPlaylists = playlists
	m.youtubeMusicPlaylistSelected = 0
	for index, playlist := range playlists {
		if playlist.ID == selectedID {
			m.youtubeMusicPlaylistSelected = index
		}
	}
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
	if m.youtubeMusicPlayback.Err != nil {
		m.youtubeMusicPlaybackErr = m.youtubeMusicPlayback.Err
	}
	state := m.youtubeMusicPlayback.State
	if state == playback.StateError && previousState != playback.StateError && !m.youtubeMusicResolving && m.youtubeMusicPlayingIndex >= 0 {
		// A file that cannot play is skipped like a failed download.
		return m.skipFailedTrack(m.youtubeMusicPlayingIndex)
	}
	if previousState == playback.StateEnded || state != playback.StateEnded || m.youtubeMusicResolving {
		return nil
	}
	next := m.upcomingTrackIndex()
	if loop := m.youtubeMusicLoopTrackIndex; loop >= 0 && loop == m.youtubeMusicPlayingIndex {
		next = loop
	}
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
	m.youtubeMusicDownloadIndex = -1
	// Without a pending index, resolving means a cached play is in flight; it
	// keeps blocking new requests so two plays never race on one player.
	if m.youtubeMusicPendingIndex >= 0 {
		m.youtubeMusicResolving = false
	}
	m.youtubeMusicPendingIndex = -1
}

// downloadTrack starts downloading one queue track, replacing any other
// download. A download already running for index is kept.
func (m *Model) downloadTrack(index int) tea.Cmd {
	if m.youtubeMusicCacheCancel != nil && m.youtubeMusicDownloadIndex == index {
		return nil
	}
	m.cancelYouTubeQueue()
	ctx, cancel := context.WithCancel(m.appContext)
	m.youtubeMusicCacheCancel = cancel
	m.youtubeMusicDownloadIndex = index
	generation := m.youtubeMusicCacheGeneration
	events := make(chan youtubeMusicCacheEvent, 16)
	m.youtubeMusicCacheEvents = events
	track := m.youtubeMusicQueue.Tracks[index]
	if m.cachedTrackPath(index) != "" {
		// Only old files are downloaded again; upgrade each once per session.
		m.skipDownload(track.VideoID)
	}
	m.youtubeMusicCacheStatus = youtubeMusicCacheEvent{generation: generation, phase: musicCacheChecking, trackIndex: index, videoID: track.VideoID}
	m.youtubeMusicCacheStatusNow = false
	go downloadYouTubeMusicTrack(ctx, generation, m.youtubeMusicClient, m.youtubeMusicCache, track, index, events)
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
		return youtubeMusicControlMsg{snapshot: player.Snapshot(), err: err}
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
		return youtubeMusicControlMsg{snapshot: player.Snapshot(), err: err, volumeSet: true}
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
