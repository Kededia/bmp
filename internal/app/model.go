package app

import (
	"context"
	"errors"
	"fmt"
	"image"
	"slices"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"bmp/internal/musiccache"
	"bmp/internal/musicstore"
	"bmp/internal/playback"
	"bmp/internal/settings"
	"bmp/internal/spectrum"
	"bmp/internal/theme"
	"bmp/internal/ui"
	"bmp/internal/youtubemusic"
)

type tickMsg time.Time
type spectrumTickMsg time.Time

type youtubeMusicLibraryMsg struct {
	client    *youtubemusic.Client
	account   youtubemusic.Account
	playlists []youtubemusic.Playlist
	err       error
}

type youtubeMusicPlaylistMsg struct {
	playlist youtubemusic.Playlist
	err      error
}

type youtubeMusicPlaybackMsg struct {
	generation int
	player     playback.Player
	trackIndex int
	videoID    string
	err        error
}

type youtubeMusicControlMsg struct {
	snapshot playback.Snapshot
	err      error
	// volumeSet marks a volume change made by the person, which the playing
	// song keeps.
	volumeSet bool
}

type youtubeMusicView int

const (
	youtubeMusicLibraryView youtubeMusicView = iota
	youtubeMusicPlaylistView
)

type youtubeMusicDeleteTarget int

const (
	youtubeMusicDeleteNone youtubeMusicDeleteTarget = iota
	youtubeMusicDeleteTrack
)

const (
	playlistScroll = "playlists"
	trackScroll    = "tracks"
)

type settingsSavedMsg struct {
	generation int
	savedAt    time.Time
	err        error
}

type Model struct {
	appContext                   context.Context
	appCancel                    context.CancelFunc
	width                        int
	height                       int
	terminalTitle                string
	paletteOpen                  bool
	paletteSelected              int
	setupOpen                    bool
	setupStep                    setupStep
	setupSelected                int
	setupChecking                bool
	setupStatus                  string
	setupStatusError             bool
	setupBrowser                 string
	setupFolder                  textinput.Model
	now                          time.Time
	colors                       theme.Theme
	listScroll                   map[string]int
	youtubeMusicClient           *youtubemusic.Client
	youtubeMusicAccount          youtubemusic.Account
	youtubeMusicPlaylists        []youtubemusic.Playlist
	youtubeMusicPlaylistSelected int
	youtubeMusicPlaylist         youtubemusic.Playlist
	// youtubeMusicQueue is the playlist playback runs from. The playing, loop,
	// pending and shuffle-history positions index it, not the open playlist.
	youtubeMusicQueue          youtubemusic.Playlist
	youtubeMusicShuffleHistory []int
	youtubeMusicShuffleBack    bool
	// youtubeMusicEdits are the open playlist's edits made in BMP.
	youtubeMusicEdits             musicstore.Edits
	youtubeMusicTrackSelected     int
	youtubeMusicView              youtubeMusicView
	youtubeMusicNaming            bool
	youtubeMusicNameInput         textinput.Model
	youtubeMusicPicking           bool
	youtubeMusicPickTracks        []youtubemusic.Track
	youtubeMusicPicked            []bool
	youtubeMusicPickSelected      int
	youtubeMusicSyncs             []playlistSync
	youtubeMusicSyncing           bool
	youtubeMusicEntryIDs          map[string]string
	youtubeMusicSearchOpen        bool
	youtubeMusicSearchAdding      bool
	youtubeMusicSearchInput       textinput.Model
	youtubeMusicSearchResults     []youtubemusic.Track
	youtubeMusicSearchSelected    int
	youtubeMusicSearchGeneration  int
	youtubeMusicSearchLoading     bool
	youtubeMusicSearchErr         error
	youtubeMusicSearchLocal       []musiccache.LocalTrack
	youtubeMusicURLResolving      bool
	youtubeMusicDeletePending     youtubeMusicDeleteTarget
	youtubeMusicDeleteStatus      string
	youtubeMusicDeleteStatusError bool
	youtubeMusicLoading           bool
	youtubeMusicErr               error
	youtubeMusicPlayer            playback.Player
	youtubeMusicPlayback          playback.Snapshot
	youtubeMusicPlaybackErr       error
	youtubeMusicResolving         bool
	youtubeMusicPlayingIndex      int
	youtubeMusicPlayingVideoID    string
	youtubeMusicPlayGeneration    int
	youtubeMusicLoopTrackIndex    int
	youtubeMusicShuffle           bool
	youtubeMusicCache             *musiccache.Cache
	youtubeMusicCacheErr          error
	youtubeMusicCached            map[string]string
	youtubeMusicCacheEvents       <-chan youtubeMusicCacheEvent
	youtubeMusicCacheCancel       context.CancelFunc
	youtubeMusicCacheGeneration   int
	youtubeMusicCacheStatus       youtubeMusicCacheEvent
	youtubeMusicPendingIndex      int
	youtubeMusicDownloadIndex     int
	youtubeMusicNextIndex         int
	youtubeMusicFailed            map[int]bool
	youtubeMusicSkipDownload      map[string]bool
	youtubeMusicCacheStatusNow    bool
	youtubeMusicCacheSize         int64
	youtubeMusicStore             *musicstore.Store
	youtubeMusicStoreErr          error
	youtubeMusicPlayCounts        map[string]uint64
	spectrumAnalyzer              *spectrum.Analyzer
	coverURL                      string
	coverImageURL                 string
	coverImage                    image.Image
	coverRender                   *coverRender
	spectrumTicking               bool
	persistedSettings             settings.State
	settingsErr                   error
	settingsDirty                 bool
	settingsSaving                bool
	settingsLastSaved             time.Time
	settingsGeneration            int
	musicRestorePending           bool
	musicRestoreTrackIndex        int
	musicRestorePosition          time.Duration
	musicRestorePaused            bool
}

var themeCommands = []theme.Theme{
	theme.Cyberpunk, theme.Phosphor, theme.Amethyst, theme.Nord,
	theme.Synthwave, theme.Ember, theme.Glacier, theme.Acid, theme.Gold,
}

func themeByName(name string) theme.Theme {
	for _, candidate := range themeCommands {
		if strings.EqualFold(candidate.Name, strings.TrimSpace(name)) {
			return candidate
		}
	}
	return theme.Cyberpunk
}

func New() Model {
	persistedSettings, settingsError := settings.Load()
	selectedTheme := themeByName(persistedSettings.Theme)

	searchInput := textinput.New()
	searchInput.Prompt = "search> "
	searchInput.Placeholder = "song, artist or album"
	searchInput.CharLimit = 256
	applyTextInputTheme(&searchInput, selectedTheme)

	nameInput := textinput.New()
	nameInput.Prompt = "name> "
	nameInput.CharLimit = 150
	applyTextInputTheme(&nameInput, selectedTheme)

	setupFolderInput := textinput.New()
	setupFolderInput.Prompt = "folder> "
	setupFolderInput.CharLimit = 1024
	applyTextInputTheme(&setupFolderInput, selectedTheme)

	musicCache, musicCacheError := musiccache.New(persistedSettings.MusicDir)
	musicStore, musicStoreError := musicstore.New()
	spectrumAnalyzer := spectrum.New()
	appContext, appCancel := context.WithCancel(context.Background())
	m := Model{
		appContext:                 appContext,
		appCancel:                  appCancel,
		now:                        time.Now(),
		colors:                     selectedTheme,
		setupOpen:                  setupNeeded(persistedSettings.Browser),
		setupFolder:                setupFolderInput,
		youtubeMusicLoading:        !setupNeeded(persistedSettings.Browser),
		youtubeMusicPlayingIndex:   -1,
		youtubeMusicLoopTrackIndex: -1,
		youtubeMusicShuffle:        persistedSettings.Shuffle,
		youtubeMusicCache:          musicCache,
		youtubeMusicCacheErr:       musicCacheError,
		youtubeMusicCached:         make(map[string]string),
		youtubeMusicPendingIndex:   -1,
		youtubeMusicDownloadIndex:  -1,
		youtubeMusicNextIndex:      -1,
		youtubeMusicCacheSize:      -1,
		youtubeMusicStore:          musicStore,
		youtubeMusicStoreErr:       musicStoreError,
		youtubeMusicPlayCounts:     make(map[string]uint64),
		spectrumAnalyzer:           spectrumAnalyzer,
		coverRender:                &coverRender{},
		listScroll:                 make(map[string]int),
		youtubeMusicSearchInput:    searchInput,
		youtubeMusicNameInput:      nameInput,
		youtubeMusicEntryIDs:       make(map[string]string),
		persistedSettings:          persistedSettings,
		settingsErr:                settingsError,
		settingsLastSaved:          time.Now(),
		musicRestorePending:        persistedSettings.Music.Valid,
		musicRestoreTrackIndex:     -1,
	}
	m.terminalTitle = m.windowTitle()
	return m
}

// Close cancels background work owned by the application model.
func (m Model) Close() {
	m.saveSettingsNow()
	if m.youtubeMusicCacheCancel != nil {
		m.youtubeMusicCacheCancel()
	}
	if m.youtubeMusicPlayer != nil {
		_ = m.youtubeMusicPlayer.Close()
	}
	if m.youtubeMusicStore != nil {
		_ = m.youtubeMusicStore.Close()
	}
	if m.appCancel != nil {
		m.appCancel()
	}
}

func (m *Model) applyTheme(selected theme.Theme) {
	m.colors = selected
	m.persistedSettings.Theme = selected.Name
	m.settingsDirty = true
	m.settingsGeneration++
	applyTextInputTheme(&m.youtubeMusicSearchInput, selected)
	applyTextInputTheme(&m.youtubeMusicNameInput, selected)
	applyTextInputTheme(&m.setupFolder, selected)
}

func applyTextInputTheme(input *textinput.Model, colors theme.Theme) {
	base := lipgloss.NewStyle().Background(colors.Background)
	input.PromptStyle = base.Foreground(colors.Cyan).Bold(true)
	input.TextStyle = base.Foreground(colors.Foreground)
	input.PlaceholderStyle = base.Foreground(colors.Muted)
	input.Cursor.Style = base.Foreground(colors.Foreground)
}

func (m Model) Init() tea.Cmd {
	commands := []tea.Cmd{tea.SetWindowTitle(m.terminalTitle), tick(), readMusicCacheSize(m.youtubeMusicCache)}
	if !m.setupOpen {
		commands = append(commands, readYouTubeMusicLibrary(m.appContext, m.persistedSettings.Browser))
	}
	if m.persistedSettings.Music.Valid {
		commands = append(commands, func() tea.Msg { return resumeMsg{} })
	}
	return tea.Batch(commands...)
}

func tick() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func spectrumTick() tea.Cmd {
	return tea.Tick(100*time.Millisecond, func(t time.Time) tea.Msg { return spectrumTickMsg(t) })
}

func spectrumRefreshActive(state playback.State) bool {
	return state == playback.StateOpening || state == playback.StateBuffering || state == playback.StatePlaying
}

func readYouTubeMusicLibrary(parent context.Context, browser string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(parent, 2*time.Minute)
		defer cancel()
		client, err := youtubemusic.New(ctx, browser)
		if err != nil {
			return youtubeMusicLibraryMsg{err: err}
		}
		// The browser cookie export is done, so yt-dlp is free to update itself.
		go youtubemusic.UpdateYTDLP(parent)
		account, accountErr := client.GetAccount(ctx)
		playlists, err := client.GetLibraryPlaylists(ctx)
		if err != nil {
			return youtubeMusicLibraryMsg{client: client, err: err}
		}
		if accountErr != nil {
			account = youtubemusic.Account{}
		}
		return youtubeMusicLibraryMsg{client: client, account: account, playlists: playlists}
	}
}

func readYouTubeMusicPlaylist(parent context.Context, client *youtubemusic.Client, id string) tea.Cmd {
	return func() tea.Msg {
		if client == nil {
			return youtubeMusicPlaylistMsg{err: errors.New("YouTube Music is not configured")}
		}
		ctx, cancel := context.WithTimeout(parent, 2*time.Minute)
		defer cancel()
		playlist, err := client.GetPlaylist(ctx, id)
		return youtubeMusicPlaylistMsg{playlist: playlist, err: err}
	}
}

func (m Model) Update(msg tea.Msg) (model tea.Model, command tea.Cmd) {
	defer func() {
		updated, ok := model.(Model)
		if !ok {
			return
		}
		title := updated.windowTitle()
		if title == updated.terminalTitle {
			return
		}
		updated.terminalTitle = title
		model = updated
		if command == nil {
			command = tea.SetWindowTitle(title)
		} else {
			command = tea.Batch(command, tea.SetWindowTitle(title))
		}
	}()
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.youtubeMusicSearchInput.Width = max(10, m.width-13)

	case tickMsg:
		previous := m.now
		m.now = time.Time(msg)
		commands := []tea.Cmd{tick()}
		// Ticks arrive every second, so a long wall-clock gap means the system slept.
		if m.now.Round(0).Sub(previous.Round(0)) > 30*time.Second {
			if command := m.recoverMusicAfterSleep(); command != nil {
				commands = append(commands, command)
			}
		}
		if m.youtubeMusicPlayer != nil {
			if command := m.refreshPlayback(); command != nil {
				commands = append(commands, command)
			}
			if spectrumRefreshActive(m.youtubeMusicPlayback.State) && !m.spectrumTicking {
				m.spectrumTicking = true
				commands = append(commands, spectrumTick())
			}
			m.markSettingsDirty()
		}
		if command := m.settingsSaveCommand(m.now); command != nil {
			commands = append(commands, command)
		}
		return m, tea.Batch(commands...)

	case spectrumTickMsg:
		if m.youtubeMusicPlayer == nil {
			m.spectrumTicking = false
			return m, nil
		}
		command := m.refreshPlayback()
		if !spectrumRefreshActive(m.youtubeMusicPlayback.State) {
			m.spectrumTicking = false
			return m, command
		}
		return m, tea.Batch(command, spectrumTick())

	case settingsSavedMsg:
		m.settingsSaving = false
		m.settingsErr = msg.err
		if msg.err == nil {
			if msg.generation == m.settingsGeneration {
				m.settingsDirty = false
			}
			m.settingsLastSaved = msg.savedAt
		}

	case youtubeMusicLibraryMsg:
		m.youtubeMusicLoading = false
		m.youtubeMusicErr = msg.err
		m.youtubeMusicClient = msg.client
		m.youtubeMusicAccount = msg.account
		m.youtubeMusicPlaylists = msg.playlists
		m.youtubeMusicPlaylistSelected = min(m.youtubeMusicPlaylistSelected, max(0, len(m.youtubeMusicPlaylists)-1))
		m.syncSearchRow()
		m.youtubeMusicView = youtubeMusicLibraryView
		m.youtubeMusicPlaylist = youtubemusic.Playlist{}
		m.youtubeMusicTrackSelected = 0
		if m.musicRestorePending && m.persistedSettings.Music.PlaylistID != "" {
			restoredPlaylistID := m.persistedSettings.Music.PlaylistID
			for index, playlist := range m.youtubeMusicPlaylists {
				// Search results are not saved, so only the resumed song remains of them.
				if playlist.ID != restoredPlaylistID || searchPlaylist(playlist) {
					continue
				}
				m.youtubeMusicPlaylistSelected = index
				m.youtubeMusicLoading = true
				return m, readYouTubeMusicPlaylist(m.appContext, m.youtubeMusicClient, restoredPlaylistID)
			}
			m.musicRestorePending = false
		}

	case youtubeMusicPlaylistMsg:
		m.youtubeMusicLoading = false
		m.youtubeMusicErr = msg.err
		// Restore is one-shot: it applies only to the saved playlist, and any
		// playlist result, including a failed fetch, ends it.
		restoring := m.musicRestorePending && msg.err == nil && msg.playlist.ID == m.persistedSettings.Music.PlaylistID
		m.musicRestorePending = false
		if msg.err == nil {
			m.youtubeMusicEdits = musicstore.Edits{}
			// Owned playlists change on YouTube Music itself, so they keep no edits.
			if m.youtubeMusicStore != nil && !searchPlaylist(msg.playlist) && !msg.playlist.Owned {
				m.youtubeMusicEdits, m.youtubeMusicStoreErr = m.youtubeMusicStore.Edits(msg.playlist.ID)
			}
			m.rememberEntries(msg.playlist)
			msg.playlist = applyEdits(msg.playlist, m.youtubeMusicEdits)
			m.youtubeMusicPlaylist = msg.playlist
			m.youtubeMusicTrackSelected = 0
			// The queue of a resumed song holds only that song until its
			// playlist arrives here and replaces it.
			if m.browsingQueue() && !restoring {
				// Show the queue itself so its rows match the positions playback uses.
				m.youtubeMusicPlaylist = m.youtubeMusicQueue
				m.youtubeMusicPlaylist.Tracks = append([]youtubemusic.Track(nil), m.youtubeMusicQueue.Tracks...)
				m.youtubeMusicTrackSelected = max(0, m.youtubeMusicPlayingIndex)
			}
			for index, playlist := range m.youtubeMusicPlaylists {
				if playlist.ID == msg.playlist.ID {
					m.youtubeMusicPlaylistSelected = index
					break
				}
			}
			m.youtubeMusicView = youtubeMusicPlaylistView
			if m.youtubeMusicCache != nil {
				if cachedTracks, cacheErr := m.youtubeMusicCache.Tracks(); cacheErr == nil {
					for _, track := range cachedTracks {
						m.youtubeMusicCached[track.VideoID] = track.Path
					}
				}
			}
			if m.youtubeMusicStore != nil {
				videoIDs := make([]string, 0, len(m.youtubeMusicPlaylist.Tracks))
				for _, track := range m.youtubeMusicPlaylist.Tracks {
					videoIDs = append(videoIDs, track.VideoID)
				}
				m.youtubeMusicPlayCounts, m.youtubeMusicStoreErr = m.youtubeMusicStore.Counts(videoIDs)
			}
			var playCommand tea.Cmd
			if restoring {
				restored := m.persistedSettings.Music
				index := restored.TrackIndex
				if index < 0 || index >= len(msg.playlist.Tracks) || msg.playlist.Tracks[index].VideoID != restored.VideoID {
					index = -1
					for candidate, track := range msg.playlist.Tracks {
						if track.VideoID == restored.VideoID {
							index = candidate
							break
						}
					}
				}
				if index >= 0 {
					// The song may already play, resumed from the music folder.
					resumed := m.youtubeMusicPlayingVideoID == restored.VideoID || (m.youtubeMusicResolving && m.youtubeMusicPendingIndex < 0)
					m.queueBrowsedPlaylist()
					m.youtubeMusicTrackSelected = index
					m.youtubeMusicLoopTrackIndex = restored.LoopTrackIndex
					if m.youtubeMusicLoopTrackIndex < 0 || m.youtubeMusicLoopTrackIndex >= len(msg.playlist.Tracks) {
						m.youtubeMusicLoopTrackIndex = -1
					}
					if resumed {
						return m, tea.Batch(m.prefetchNextTrack(), m.coverCommand())
					}
					m.musicRestoreTrackIndex = index
					m.musicRestorePosition = max(0, time.Duration(restored.PositionMillis)*time.Millisecond)
					m.musicRestorePaused = restored.Paused
					playCommand = m.requestYouTubeTrack(index)
				}
			}
			return m, playCommand
		}

	case youtubeMusicCacheEvent:
		if msg.generation != m.youtubeMusicCacheGeneration {
			return m, nil
		}
		if msg.done {
			m.youtubeMusicCacheEvents = nil
			m.youtubeMusicCacheCancel = nil
			m.youtubeMusicDownloadIndex = -1
			if m.youtubeMusicPendingIndex >= 0 {
				m.youtubeMusicResolving = false
				m.youtubeMusicPendingIndex = -1
			}
			if m.youtubeMusicCacheStatus.phase != musicCacheFailed {
				m.youtubeMusicCacheStatus = youtubeMusicCacheEvent{}
			}
			return m, tea.Batch(readMusicCacheSize(m.youtubeMusicCache), m.prefetchTracks())
		}
		// The running download's position moves with its song when songs move.
		msg.trackIndex = m.youtubeMusicDownloadIndex
		pending := msg.trackIndex == m.youtubeMusicPendingIndex && msg.trackIndex >= 0 && msg.trackIndex < len(m.youtubeMusicQueue.Tracks) &&
			m.youtubeMusicQueue.Tracks[msg.trackIndex].VideoID == msg.videoID
		m.youtubeMusicCacheStatus = msg
		m.youtubeMusicCacheStatusNow = pending
		commands := make([]tea.Cmd, 0, 2)
		if msg.path != "" && msg.videoID != "" {
			m.youtubeMusicCached[msg.videoID] = msg.path
			if pending {
				commands = append(commands, m.beginCachedYouTubeTrack(msg.trackIndex, msg.path))
			}
		}
		if msg.err != nil && !pending {
			// A failed prefetch is retried only when the track comes up to play.
			m.skipDownload(msg.videoID)
		}
		if msg.err != nil && pending {
			m.youtubeMusicPlaybackErr = msg.err
			m.youtubeMusicPendingIndex = -1
			m.youtubeMusicResolving = false
			commands = append(commands, m.skipFailedTrack(msg.trackIndex))
		}
		// Skipping may have replaced this download; its own waiter is already queued.
		if msg.generation == m.youtubeMusicCacheGeneration && m.youtubeMusicCacheEvents != nil {
			commands = append(commands, waitForYouTubeMusicCacheEvent(m.youtubeMusicCacheGeneration, m.youtubeMusicCacheEvents))
		}
		return m, tea.Batch(commands...)

	case musicCacheSizeMsg:
		if msg.err == nil && msg.cache == m.youtubeMusicCache {
			m.youtubeMusicCacheSize = msg.size
		}

	case youtubeMusicPlaybackMsg:
		if msg.generation != m.youtubeMusicPlayGeneration {
			// A newer request superseded this one; close any session it created.
			if player := msg.player; player != nil && player != m.youtubeMusicPlayer {
				return m, func() tea.Msg {
					_ = player.Close()
					return nil
				}
			}
			return m, nil
		}
		m.youtubeMusicResolving = false
		m.youtubeMusicPlaybackErr = msg.err
		if msg.player != nil {
			m.youtubeMusicPlayer = msg.player
		}
		if msg.err == nil {
			if index := msg.trackIndex; index < 0 || index >= len(m.youtubeMusicQueue.Tracks) || m.youtubeMusicQueue.Tracks[index].VideoID != msg.videoID {
				// The queue changed while the song opened, as when a resumed
				// song's playlist arrives; find the song in it.
				msg.trackIndex = slices.IndexFunc(m.youtubeMusicQueue.Tracks, func(track youtubemusic.Track) bool { return track.VideoID == msg.videoID })
			}
			if previous := m.youtubeMusicPlayingIndex; previous != msg.trackIndex {
				history := m.youtubeMusicShuffleHistory
				if count := len(history); m.youtubeMusicShuffleBack && count > 0 && history[count-1] == msg.trackIndex {
					m.youtubeMusicShuffleHistory = history[:count-1]
				} else if m.youtubeMusicShuffle && previous >= 0 {
					m.youtubeMusicShuffleHistory = append(history, previous)
				}
			}
			m.youtubeMusicShuffleBack = false
			m.youtubeMusicFailed = nil
			m.youtubeMusicPlayingIndex = msg.trackIndex
			m.youtubeMusicPlayingVideoID = msg.videoID
			if m.browsingQueue() {
				m.youtubeMusicTrackSelected = msg.trackIndex
			}
			m.youtubeMusicPlayback = m.youtubeMusicPlayer.Snapshot()
			// A song plays at the volume last set while it played, else at
			// the last volume used.
			volume := m.persistedSettings.Volume
			if m.youtubeMusicStore != nil {
				if saved, found, err := m.youtubeMusicStore.Volume(msg.videoID); err == nil && found {
					volume = saved
				}
			}
			commands := []tea.Cmd{applyYouTubeMusicPreferences(
				m.appContext, m.youtubeMusicPlayer, volume, m.persistedSettings.Muted,
			)}
			if m.youtubeMusicStore != nil {
				count, err := m.youtubeMusicStore.Increment(msg.videoID)
				m.youtubeMusicStoreErr = err
				if err == nil {
					m.youtubeMusicPlayCounts[msg.videoID] = count
				}
			}
			if !m.spectrumTicking {
				m.spectrumTicking = true
				commands = append(commands, spectrumTick())
			}
			commands = append(commands, m.prefetchNextTrack(), m.coverCommand())
			return m, tea.Batch(commands...)
		}

	case youtubeMusicControlMsg:
		m.youtubeMusicPlaybackErr = msg.err
		if msg.err == nil {
			m.youtubeMusicPlayback = msg.snapshot
			m.markSettingsDirty()
			if msg.volumeSet && m.youtubeMusicStore != nil && m.youtubeMusicPlayingVideoID != "" {
				m.youtubeMusicStoreErr = m.youtubeMusicStore.SaveVolume(m.youtubeMusicPlayingVideoID, msg.snapshot.Volume)
			}
		}

	case setupCheckMsg:
		return m, m.handleSetupCheck(msg)

	case coverMsg:
		m.handleCover(msg)

	case playlistSyncMsg:
		return m, m.handleSync(msg)

	case playlistCreatedMsg:
		m.handleCreated(msg)

	case resumeMsg:
		return m, m.resumeFromCache()

	case youtubeMusicSearchTickMsg:
		return m, m.handleSearchTick(msg)

	case youtubeMusicSearchMsg:
		m.handleSearchResults(msg)

	case youtubeMusicURLTrackMsg:
		m.youtubeMusicURLResolving = false
		m.youtubeMusicPlaybackErr = msg.err
		if msg.err == nil && msg.playlistID == "" {
			return m, m.playSearchTracks(msg.track.Title, []youtubemusic.Track{msg.track}, 0)
		}
		if msg.err == nil && msg.playlistID == m.youtubeMusicPlaylist.ID {
			return m, m.addTrack(msg.track)
		}

	case tea.KeyMsg:
		if msg.String() == "ctrl+d" {
			return m, tea.Quit
		}
		if m.setupOpen {
			return m, m.handleSetupKey(msg)
		}
		if m.youtubeMusicSearchOpen {
			return m, m.handleSearchKey(msg)
		}
		if m.youtubeMusicNaming {
			return m, m.handleNameKey(msg)
		}
		if m.youtubeMusicPicking {
			return m, m.handlePickKey(msg)
		}
		if m.paletteOpen {
			switch msg.String() {
			case "esc", "ctrl+p":
				m.paletteOpen = false
			case "enter":
				m.paletteOpen = false
				if m.paletteSelected == len(themeCommands) {
					m.openSetup()
				} else {
					m.applyTheme(themeCommands[m.paletteSelected])
				}
			case "up":
				// The entry after the themes opens setup.
				m.paletteSelected = (m.paletteSelected + len(themeCommands)) % (len(themeCommands) + 1)
			case "down":
				m.paletteSelected = (m.paletteSelected + 1) % (len(themeCommands) + 1)
			}
			return m, nil
		}
		if m.youtubeMusicDeletePending != youtubeMusicDeleteNone {
			switch msg.String() {
			case "d", "D":
				return m, m.confirmYouTubeMusicDelete()
			case "esc":
				m.cancelYouTubeMusicDelete()
				return m, nil
			default:
				m.cancelYouTubeMusicDelete()
			}
		}
		switch msg.String() {
		case "ctrl+p":
			m.paletteOpen = true
			m.paletteSelected = 0
			for index, candidate := range themeCommands {
				if candidate.Name == m.colors.Name {
					m.paletteSelected = index
				}
			}
		case "ctrl+r":
			if !m.youtubeMusicLoading {
				m.youtubeMusicLoading = true
				m.youtubeMusicErr = nil
				return m, readYouTubeMusicLibrary(m.appContext, m.persistedSettings.Browser)
			}
		case "enter":
			if m.youtubeMusicView == youtubeMusicLibraryView && !m.youtubeMusicLoading && len(m.youtubeMusicPlaylists) > 0 {
				selected := m.youtubeMusicPlaylists[m.youtubeMusicPlaylistSelected]
				m.youtubeMusicLoading = true
				m.youtubeMusicErr = nil
				if selected.ID == m.youtubeMusicQueue.ID {
					// The playing playlist opens as it plays, without a reload.
					queue := m.youtubeMusicQueue
					return m, func() tea.Msg { return youtubeMusicPlaylistMsg{playlist: queue} }
				}
				return m, readYouTubeMusicPlaylist(m.appContext, m.youtubeMusicClient, selected.ID)
			}
			if m.youtubeMusicView == youtubeMusicPlaylistView && !m.youtubeMusicResolving {
				if !m.browsingQueue() {
					m.queueBrowsedPlaylist()
				}
				return m, m.requestYouTubeTrack(m.youtubeMusicTrackSelected)
			}
		case "up":
			if !m.youtubeMusicLoading {
				if m.youtubeMusicView == youtubeMusicLibraryView && m.youtubeMusicPlaylistSelected > 0 {
					m.youtubeMusicPlaylistSelected--
				} else if m.youtubeMusicView == youtubeMusicPlaylistView && m.youtubeMusicTrackSelected > 0 {
					m.youtubeMusicTrackSelected--
				}
			}
		case "down":
			if !m.youtubeMusicLoading {
				if m.youtubeMusicView == youtubeMusicLibraryView && m.youtubeMusicPlaylistSelected < len(m.youtubeMusicPlaylists)-1 {
					m.youtubeMusicPlaylistSelected++
				} else if m.youtubeMusicView == youtubeMusicPlaylistView && m.youtubeMusicTrackSelected < len(m.youtubeMusicPlaylist.Tracks)-1 {
					m.youtubeMusicTrackSelected++
				}
			}
		case "right":
			if m.youtubeMusicPlayer != nil {
				return m, m.requestYouTubeTrack(m.upcomingTrackIndex())
			}
		case "left":
			if m.youtubeMusicPlayer != nil {
				previous := loopingPlaylistTrackIndex(m.youtubeMusicQueue.Tracks, m.youtubeMusicPlayingIndex-1, -1)
				if count := len(m.youtubeMusicShuffleHistory); m.youtubeMusicShuffle && count > 0 {
					previous = m.youtubeMusicShuffleHistory[count-1]
					m.youtubeMusicShuffleBack = true
				}
				return m, m.requestYouTubeTrack(previous)
			}
		case "backspace":
			if m.youtubeMusicView == youtubeMusicPlaylistView {
				m.youtubeMusicView = youtubeMusicLibraryView
				m.youtubeMusicErr = nil
			}
		case " ", "space":
			state := m.youtubeMusicPlayback.State
			stalled := m.youtubeMusicPlayer == nil || state == playback.StateError || state == playback.StateStopped ||
				state == playback.StateEnded || state == playback.StateIdle
			if stalled && m.youtubeMusicPlayingIndex >= 0 {
				// Reopen the track rather than toggling a session that cannot
				// resume, such as one lost to system sleep. Keep any saved
				// position, but play instead of restoring paused.
				m.musicRestorePaused = false
				return m, m.requestYouTubeTrack(m.youtubeMusicPlayingIndex)
			}
			if m.youtubeMusicPlayer != nil {
				return m, toggleYouTubeMusicPlayback(m.appContext, m.youtubeMusicPlayer)
			}
		case "+", "=":
			if m.youtubeMusicPlayer != nil {
				return m, setYouTubeMusicVolume(m.appContext, m.youtubeMusicPlayer, 1)
			}
		case "-":
			if m.youtubeMusicPlayer != nil {
				return m, setYouTubeMusicVolume(m.appContext, m.youtubeMusicPlayer, -1)
			}
		case "alt++", "alt+=":
			if m.youtubeMusicPlayer != nil {
				return m, setYouTubeMusicVolume(m.appContext, m.youtubeMusicPlayer, 5)
			}
		case "alt+-":
			if m.youtubeMusicPlayer != nil {
				return m, setYouTubeMusicVolume(m.appContext, m.youtubeMusicPlayer, -5)
			}
		case "shift+left", "shift+right":
			if m.youtubeMusicPlayer != nil {
				delta := 10 * time.Second
				if msg.String() == "shift+left" {
					delta = -delta
				}
				return m, seekYouTubeMusic(m.appContext, m.youtubeMusicPlayer, delta)
			}
		case "s", "S":
			m.youtubeMusicShuffle = !m.youtubeMusicShuffle
			m.markSettingsDirty()
			// The upcoming track depends on shuffle; pick and fetch it again.
			return m, m.prefetchNextTrack()
		case "/":
			if !m.youtubeMusicLoading {
				return m, m.openSearch()
			}
		case "n", "N":
			if !m.youtubeMusicLoading && m.youtubeMusicClient != nil {
				return m, m.beginNewPlaylist()
			}
		case "pgup", "alt+up", "pgdown", "alt+down":
			if m.youtubeMusicView == youtubeMusicPlaylistView {
				direction := 1
				if msg.String() == "pgup" || msg.String() == "alt+up" {
					direction = -1
				}
				return m, m.moveSelectedTrack(direction)
			}
		case "d", "D":
			if m.youtubeMusicView == youtubeMusicPlaylistView {
				m.beginYouTubeMusicDelete(youtubeMusicDeleteTrack)
			}
		case "l", "L":
			// Loop marks a queue position, so it applies only while browsing the queue.
			if m.youtubeMusicView == youtubeMusicPlaylistView && m.browsingQueue() &&
				m.youtubeMusicTrackSelected >= 0 && m.youtubeMusicTrackSelected < len(m.youtubeMusicPlaylist.Tracks) {
				selected := m.youtubeMusicTrackSelected
				track := m.youtubeMusicPlaylist.Tracks[selected]
				if track.Available && strings.TrimSpace(track.VideoID) != "" {
					if m.youtubeMusicLoopTrackIndex == selected {
						m.youtubeMusicLoopTrackIndex = -1
					} else {
						m.youtubeMusicLoopTrackIndex = selected
					}
					m.markSettingsDirty()
				}
			}
		case "m", "M":
			if m.youtubeMusicPlayer != nil {
				return m, toggleYouTubeMusicMute(m.appContext, m.youtubeMusicPlayer)
			}
		}
	}

	return m, nil
}

func (m Model) View() string {
	if m.width == 0 || m.height == 0 {
		return "Starting BMP…"
	}
	if m.width < 40 || m.height < 10 {
		message := lipgloss.NewStyle().
			Foreground(m.colors.Yellow).
			Background(m.colors.Background).
			Padding(1, 2).
			Render(fmt.Sprintf("BMP needs a larger window\n\ncurrent: %dx%d  minimum: 40x10\n\nCtrl+D quit", m.width, m.height))
		return enforceThemeBackground(message, m.colors.Background)
	}

	if m.setupOpen {
		return enforceThemeBackground(m.setupView(), m.colors.Background)
	}
	if m.paletteOpen {
		return enforceThemeBackground(m.paletteView(), m.colors.Background)
	}

	// Hints go most useful first; the footer drops the last ones that do not fit.
	playLabel := "play"
	if m.youtubeMusicPlayback.State == playback.StatePlaying {
		playLabel = "pause"
	}
	var playing []ui.Hint
	if m.youtubeMusicPlayer != nil {
		playing = []ui.Hint{{Key: "space", Label: playLabel}, {Key: "←→", Label: "track"}, {Key: "⇧←→", Label: "seek"}, {Key: "+-", Label: "volume"}, {Key: "M", Label: "mute"}, {Key: "S", Label: "shuffle"}}
	}
	general := []ui.Hint{{Key: "^P", Label: "menu"}, {Key: "^D", Label: "quit"}}
	hints := []ui.Hint{{Key: "↑↓", Label: "select"}, {Key: "↵", Label: "open"}, {Key: "/", Label: "search"}, {Key: "N", Label: "new playlist"}}
	hints = append(append(append(hints, playing...), ui.Hint{Key: "^R", Label: "refresh"}), general...)
	if m.youtubeMusicNaming {
		next := "create"
		if len(m.youtubeMusicPickTracks) > 0 {
			next = "pick songs"
		}
		hints = []ui.Hint{{Key: "↵", Label: next}, {Key: "esc", Label: "cancel"}}
	} else if m.youtubeMusicPicking {
		hints = []ui.Hint{{Key: "↑↓"}, {Key: "space", Label: "tick"}, {Key: "A", Label: "all"}, {Key: "↵", Label: "create"}, {Key: "esc", Label: "cancel"}}
	} else if m.youtubeMusicSearchOpen {
		pick := "play"
		if m.youtubeMusicSearchAdding {
			pick = "add"
		}
		hints = []ui.Hint{{Key: "↑↓"}, {Key: "↵", Label: pick}, {Key: "esc", Label: "close"}}
	} else if m.youtubeMusicDeletePending != youtubeMusicDeleteNone {
		hints = []ui.Hint{{Key: "D", Label: "confirm delete"}, {Key: "esc", Label: "cancel"}}
	} else if m.youtubeMusicView == youtubeMusicPlaylistView {
		hints = []ui.Hint{{Key: "↑↓"}, {Key: "↵", Label: "play"}, {Key: "⌫", Label: "back"}}
		if m.editable() {
			hints = append(hints, ui.Hint{Key: "/", Label: "add"}, ui.Hint{Key: "PgUp/Dn", Label: "move"}, ui.Hint{Key: "D", Label: "remove"})
		} else {
			hints = append(hints, ui.Hint{Key: "/", Label: "search"})
		}
		if m.browsingQueue() {
			hints = append(hints, ui.Hint{Key: "L", Label: "loop"})
		}
		hints = append(hints, ui.Hint{Key: "N", Label: "new from this"})
		hints = append(append(hints, playing...), general...)
	}
	folderSize := ""
	if m.youtubeMusicCacheSize >= 0 {
		folderSize = "MUSIC FOLDER " + formatBytes(m.youtubeMusicCacheSize)
	}
	panel := ui.Panel("MUSIC", m.musicMainView(m.width, m.height), m.width, m.height, true, m.colors, folderSize, hints...)

	rendered := lipgloss.NewStyle().Background(m.colors.Background).Render(panel)
	return m.withCover(enforceThemeBackground(rendered, m.colors.Background))
}

func (m Model) paletteView() string {
	boxWidth := min(64, m.width-8)
	lines := []string{
		lipgloss.NewStyle().Foreground(m.colors.Magenta).Bold(true).Render("COMMAND PALETTE"),
		"",
		lipgloss.NewStyle().Foreground(m.colors.Muted).Render("THEMES"),
	}
	for index, candidate := range themeCommands {
		cursor := "  "
		if index == m.paletteSelected {
			cursor = lipgloss.NewStyle().Foreground(m.colors.Magenta).Bold(true).Render("> ")
		}
		label := "theme " + strings.ToLower(candidate.Name)
		if candidate.Name == m.colors.Name {
			label += "  ✓"
		}
		style := lipgloss.NewStyle().Foreground(m.colors.Foreground).Bold(index == m.paletteSelected)
		lines = append(lines, cursor+style.Render(label))
	}
	cursor := "  "
	if m.paletteSelected == len(themeCommands) {
		cursor = lipgloss.NewStyle().Foreground(m.colors.Magenta).Bold(true).Render("> ")
	}
	lines = append(lines, "", lipgloss.NewStyle().Foreground(m.colors.Muted).Render("SETUP"),
		cursor+lipgloss.NewStyle().Foreground(m.colors.Foreground).Bold(m.paletteSelected == len(themeCommands)).Render("browser and music folder"))
	lines = append(lines, "", ui.Shortcuts(m.colors,
		ui.Hint{Key: "↑↓", Label: "select"},
		ui.Hint{Key: "↵", Label: "apply"},
		ui.Hint{Key: "esc", Label: "close"},
	))
	content := strings.Join(lines, "\n")
	box := lipgloss.NewStyle().
		Width(boxWidth).
		Padding(1, 2).
		Border(lipgloss.DoubleBorder()).
		BorderForeground(m.colors.Magenta).
		Foreground(m.colors.Foreground).
		Background(m.colors.Background).
		Render(content)
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, box,
		lipgloss.WithWhitespaceBackground(m.colors.Background))
}
