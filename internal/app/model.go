package app

import (
	"context"
	"errors"
	"fmt"
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
	player     playback.Player
	trackIndex int
	videoID    string
	err        error
}

type youtubeMusicControlMsg struct {
	snapshot playback.Snapshot
	seeked   bool
	err      error
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
	youtubeMusicDeletePlaylist
)

const (
	playlistScroll   = "playlists"
	trackScroll      = "tracks"
	localTrackScroll = "local-tracks"
)

type settingsSavedMsg struct {
	generation int
	savedAt    time.Time
	err        error
}

type Model struct {
	appContext                    context.Context
	appCancel                     context.CancelFunc
	width                         int
	height                        int
	terminalTitle                 string
	paletteOpen                   bool
	paletteSelected               int
	now                           time.Time
	colors                        theme.Theme
	listScroll                    map[string]int
	youtubeMusicClient            *youtubemusic.Client
	youtubeMusicAccount           youtubemusic.Account
	youtubeMusicPlaylists         []youtubemusic.Playlist
	youtubeMusicPlaylistSelected  int
	youtubeMusicPlaylist          youtubemusic.Playlist
	youtubeMusicLocalPlaylists    []youtubemusic.Playlist
	youtubeMusicTrackSelected     int
	youtubeMusicView              youtubeMusicView
	youtubeMusicAddingLocal       bool
	youtubeMusicLocalTracks       []musiccache.LocalTrack
	youtubeMusicLocalSelected     int
	youtubeMusicLocalSearch       textinput.Model
	youtubeMusicLocalSearching    bool
	youtubeMusicURLPrompt         bool
	youtubeMusicURLResolving      bool
	youtubeMusicURLInput          textinput.Model
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
	youtubeMusicStore             *musicstore.Store
	youtubeMusicStoreErr          error
	youtubeMusicPlayCounts        map[string]uint64
	spectrumAnalyzer              *spectrum.Analyzer
	spectrumErr                   error
	spectrumTicking               bool
	persistedSettings             settings.State
	settingsErr                   error
	settingsDirty                 bool
	settingsSaving                bool
	settingsLastSaved             time.Time
	settingsGeneration            int
	musicRestorePending           bool
	musicRestoreLocalPlaylist     youtubemusic.Playlist
	musicRestoreTrackIndex        int
	musicRestorePosition          time.Duration
	musicRestorePaused            bool
}

var themeCommands = []theme.Theme{theme.Cyberpunk, theme.Phosphor, theme.Amethyst, theme.Nord}

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

	musicURLInput := textinput.New()
	musicURLInput.Prompt = "url> "
	musicURLInput.Placeholder = "YouTube or YouTube Music URL"
	musicURLInput.CharLimit = 2048
	applyTextInputTheme(&musicURLInput, selectedTheme)

	localSearchInput := textinput.New()
	localSearchInput.Prompt = "search> "
	localSearchInput.Placeholder = "title or artist"
	localSearchInput.CharLimit = 256
	applyTextInputTheme(&localSearchInput, selectedTheme)

	musicCache, musicCacheError := musiccache.NewFromEnvironment()
	musicStore, musicStoreError := musicstore.New()
	var localPlaylists []youtubemusic.Playlist
	if musicStoreError == nil {
		localPlaylists, musicStoreError = musicStore.Playlists()
	}
	if musicStoreError == nil && musicCacheError == nil {
		if cachedTracks, err := musicCache.Tracks(); err == nil && applyCachedTrackArtists(localPlaylists, cachedTracks) {
			for _, playlist := range localPlaylists {
				if err := musicStore.SavePlaylist(playlist); err != nil {
					musicStoreError = err
					break
				}
			}
		}
	}
	var localRestorePlaylist youtubemusic.Playlist
	if persistedSettings.Music.Valid {
		for _, playlist := range localPlaylists {
			if playlist.ID == persistedSettings.Music.PlaylistID {
				localRestorePlaylist = playlist
				break
			}
		}
	}
	spectrumAnalyzer, spectrumError := spectrum.New()
	appContext, appCancel := context.WithCancel(context.Background())
	m := Model{
		appContext:                 appContext,
		appCancel:                  appCancel,
		now:                        time.Now(),
		colors:                     selectedTheme,
		youtubeMusicLoading:        true,
		youtubeMusicPlaylists:      append([]youtubemusic.Playlist(nil), localPlaylists...),
		youtubeMusicLocalPlaylists: localPlaylists,
		youtubeMusicPlayingIndex:   -1,
		youtubeMusicLoopTrackIndex: -1,
		youtubeMusicShuffle:        persistedSettings.Shuffle,
		youtubeMusicCache:          musicCache,
		youtubeMusicCacheErr:       musicCacheError,
		youtubeMusicCached:         make(map[string]string),
		youtubeMusicPendingIndex:   -1,
		youtubeMusicStore:          musicStore,
		youtubeMusicStoreErr:       musicStoreError,
		youtubeMusicPlayCounts:     make(map[string]uint64),
		spectrumAnalyzer:           spectrumAnalyzer,
		spectrumErr:                spectrumError,
		listScroll:                 make(map[string]int),
		youtubeMusicURLInput:       musicURLInput,
		youtubeMusicLocalSearch:    localSearchInput,
		persistedSettings:          persistedSettings,
		settingsErr:                settingsError,
		settingsLastSaved:          time.Now(),
		musicRestorePending:        persistedSettings.Music.Valid,
		musicRestoreLocalPlaylist:  localRestorePlaylist,
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
	if m.spectrumAnalyzer != nil {
		_ = m.spectrumAnalyzer.Close()
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
	applyTextInputTheme(&m.youtubeMusicURLInput, selected)
	applyTextInputTheme(&m.youtubeMusicLocalSearch, selected)
}

func applyTextInputTheme(input *textinput.Model, colors theme.Theme) {
	base := lipgloss.NewStyle().Background(colors.Background)
	input.PromptStyle = base.Foreground(colors.Cyan).Bold(true)
	input.TextStyle = base.Foreground(colors.Foreground)
	input.PlaceholderStyle = base.Foreground(colors.Muted)
	input.Cursor.Style = base.Foreground(colors.Foreground)
}

func (m Model) Init() tea.Cmd {
	commands := []tea.Cmd{tea.SetWindowTitle(m.terminalTitle), tick(), readYouTubeMusicLibrary(m.appContext)}
	if m.musicRestoreLocalPlaylist.ID != "" {
		playlist := m.musicRestoreLocalPlaylist
		commands = append(commands, func() tea.Msg { return youtubeMusicPlaylistMsg{playlist: playlist} })
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

func (m *Model) syncMusicSpectrum() {
	if m.spectrumAnalyzer == nil {
		return
	}
	snapshot := m.youtubeMusicPlayback
	m.spectrumAnalyzer.Sync(snapshot.Source.Path, snapshot.State == playback.StatePlaying, snapshot.Position)
}

func readYouTubeMusicLibrary(parent context.Context) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(parent, 2*time.Minute)
		defer cancel()
		client, err := youtubemusic.NewFromEnvironmentContext(ctx)
		if err != nil {
			return youtubeMusicLibraryMsg{err: err}
		}
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
		m.youtubeMusicURLInput.Width = max(10, m.width-10)

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
			previousState := m.youtubeMusicPlayback.State
			m.youtubeMusicPlayback = m.youtubeMusicPlayer.Snapshot()
			m.syncMusicSpectrum()
			if spectrumRefreshActive(m.youtubeMusicPlayback.State) && !m.spectrumTicking {
				m.spectrumTicking = true
				commands = append(commands, spectrumTick())
			}
			if m.youtubeMusicPlayback.Err != nil {
				m.youtubeMusicPlaybackErr = m.youtubeMusicPlayback.Err
			}
			if command := m.restoreMusicPositionCommand(); command != nil {
				commands = append(commands, command)
			}
			if previousState != playback.StateEnded && m.youtubeMusicPlayback.State == playback.StateEnded && !m.youtubeMusicResolving {
				next := automaticTrackIndex(m.youtubeMusicPlaylist.Tracks, m.youtubeMusicPlayingIndex, m.youtubeMusicLoopTrackIndex, m.youtubeMusicShuffle)
				if command := m.requestYouTubeTrack(next); command != nil {
					commands = append(commands, command)
				}
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
		previousState := m.youtubeMusicPlayback.State
		m.youtubeMusicPlayback = m.youtubeMusicPlayer.Snapshot()
		m.syncMusicSpectrum()
		if command := m.restoreMusicPositionCommand(); command != nil {
			return m, tea.Batch(command, spectrumTick())
		}
		if !spectrumRefreshActive(m.youtubeMusicPlayback.State) {
			m.spectrumTicking = false
			if previousState != playback.StateEnded && m.youtubeMusicPlayback.State == playback.StateEnded && !m.youtubeMusicResolving {
				next := automaticTrackIndex(m.youtubeMusicPlaylist.Tracks, m.youtubeMusicPlayingIndex, m.youtubeMusicLoopTrackIndex, m.youtubeMusicShuffle)
				return m, m.requestYouTubeTrack(next)
			}
			return m, nil
		}
		return m, spectrumTick()

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
		m.youtubeMusicPlaylists = append(append([]youtubemusic.Playlist(nil), msg.playlists...), m.youtubeMusicLocalPlaylists...)
		m.youtubeMusicPlaylistSelected = min(m.youtubeMusicPlaylistSelected, max(0, len(m.youtubeMusicPlaylists)-1))
		if m.youtubeMusicView == youtubeMusicPlaylistView && m.youtubeMusicPlaylist.Local {
			for index, playlist := range m.youtubeMusicPlaylists {
				if playlist.ID == m.youtubeMusicPlaylist.ID {
					m.youtubeMusicPlaylistSelected = index
					break
				}
			}
			return m, nil
		}
		m.youtubeMusicView = youtubeMusicLibraryView
		m.youtubeMusicPlaylist = youtubemusic.Playlist{}
		m.youtubeMusicTrackSelected = 0
		if m.musicRestorePending && m.persistedSettings.Music.PlaylistID != "" {
			restoredPlaylistID := m.persistedSettings.Music.PlaylistID
			for index, playlist := range m.youtubeMusicPlaylists {
				if playlist.ID != restoredPlaylistID {
					continue
				}
				m.youtubeMusicPlaylistSelected = index
				m.youtubeMusicLoading = true
				if playlist.Local {
					if playlist.ID == m.musicRestoreLocalPlaylist.ID {
						return m, nil
					}
					return m, func() tea.Msg { return youtubeMusicPlaylistMsg{playlist: playlist} }
				}
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
			m.cancelYouTubeQueue()
			m.youtubeMusicPlaylist = msg.playlist
			m.youtubeMusicPlayingIndex = -1
			for index, track := range msg.playlist.Tracks {
				if track.VideoID == m.youtubeMusicPlayingVideoID {
					m.youtubeMusicPlayingIndex = index
					break
				}
			}
			for index, playlist := range m.youtubeMusicPlaylists {
				if playlist.ID == msg.playlist.ID {
					m.youtubeMusicPlaylistSelected = index
					break
				}
			}
			m.youtubeMusicTrackSelected = 0
			m.youtubeMusicLoopTrackIndex = -1
			m.youtubeMusicView = youtubeMusicPlaylistView
			if m.youtubeMusicCache != nil {
				if cachedTracks, cacheErr := m.youtubeMusicCache.Tracks(); cacheErr == nil {
					for _, track := range cachedTracks {
						m.youtubeMusicCached[track.VideoID] = track.Path
					}
				}
			}
			if m.youtubeMusicStore != nil {
				videoIDs := make([]string, 0, len(msg.playlist.Tracks))
				for _, track := range msg.playlist.Tracks {
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
					m.youtubeMusicTrackSelected = index
					m.youtubeMusicLoopTrackIndex = restored.LoopTrackIndex
					if m.youtubeMusicLoopTrackIndex < 0 || m.youtubeMusicLoopTrackIndex >= len(msg.playlist.Tracks) {
						m.youtubeMusicLoopTrackIndex = -1
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
		m.youtubeMusicCacheStatus = msg
		commands := make([]tea.Cmd, 0, 2)
		if msg.path != "" && msg.videoID != "" {
			m.youtubeMusicCached[msg.videoID] = msg.path
			if msg.trackIndex == m.youtubeMusicPendingIndex && msg.trackIndex >= 0 && msg.trackIndex < len(m.youtubeMusicPlaylist.Tracks) &&
				m.youtubeMusicPlaylist.Tracks[msg.trackIndex].VideoID == msg.videoID {
				commands = append(commands, m.beginCachedYouTubeTrack(msg.trackIndex, msg.path))
			}
		}
		if msg.err != nil && msg.trackIndex == m.youtubeMusicPendingIndex && msg.trackIndex >= 0 && msg.trackIndex < len(m.youtubeMusicPlaylist.Tracks) &&
			m.youtubeMusicPlaylist.Tracks[msg.trackIndex].VideoID == msg.videoID {
			m.youtubeMusicPlaybackErr = msg.err
			m.youtubeMusicPendingIndex = playableTrackIndex(m.youtubeMusicPlaylist.Tracks, msg.trackIndex+1, 1)
			m.youtubeMusicResolving = m.youtubeMusicPendingIndex >= 0
		}
		if msg.done {
			m.youtubeMusicCacheEvents = nil
			m.youtubeMusicCacheCancel = nil
			if m.youtubeMusicPendingIndex >= 0 {
				m.youtubeMusicResolving = false
				m.youtubeMusicPendingIndex = -1
			}
		} else if m.youtubeMusicCacheEvents != nil {
			commands = append(commands, waitForYouTubeMusicCacheEvent(m.youtubeMusicCacheGeneration, m.youtubeMusicCacheEvents))
		}
		return m, tea.Batch(commands...)

	case youtubeMusicPlaybackMsg:
		m.youtubeMusicResolving = false
		m.youtubeMusicPlaybackErr = msg.err
		if msg.player != nil {
			m.youtubeMusicPlayer = msg.player
		}
		if msg.err == nil {
			m.youtubeMusicPlayingIndex = msg.trackIndex
			m.youtubeMusicPlayingVideoID = msg.videoID
			m.youtubeMusicTrackSelected = msg.trackIndex
			m.youtubeMusicPlayback = m.youtubeMusicPlayer.Snapshot()
			m.syncMusicSpectrum()
			commands := []tea.Cmd{applyYouTubeMusicPreferences(
				m.appContext, m.youtubeMusicPlayer, m.persistedSettings.Volume, m.persistedSettings.Muted,
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
			return m, tea.Batch(commands...)
		}

	case youtubeMusicControlMsg:
		m.youtubeMusicPlaybackErr = msg.err
		if msg.err == nil {
			if msg.seeked && m.spectrumAnalyzer != nil {
				// The analyzer decodes on its own clock; restart it at the new position.
				m.spectrumAnalyzer.Sync("", false, 0)
			}
			m.youtubeMusicPlayback = msg.snapshot
			m.syncMusicSpectrum()
			m.markSettingsDirty()
		}

	case youtubeMusicURLTrackMsg:
		m.youtubeMusicURLResolving = false
		m.youtubeMusicPlaybackErr = msg.err
		if msg.err == nil && msg.playlistID == m.youtubeMusicPlaylist.ID {
			m.addTrack(msg.track)
		}

	case tea.KeyMsg:
		if msg.String() == "ctrl+d" {
			return m, tea.Quit
		}
		if m.youtubeMusicURLPrompt {
			switch msg.String() {
			case "esc":
				m.youtubeMusicURLPrompt = false
				m.youtubeMusicURLInput.Blur()
				return m, nil
			case "enter":
				rawURL := strings.TrimSpace(m.youtubeMusicURLInput.Value())
				if rawURL == "" {
					return m, nil
				}
				m.youtubeMusicURLPrompt = false
				m.youtubeMusicURLResolving = true
				m.youtubeMusicURLInput.Blur()
				m.youtubeMusicPlaybackErr = nil
				return m, resolveYouTubeMusicURL(m.appContext, m.youtubeMusicClient, m.youtubeMusicPlaylist.ID, rawURL)
			default:
				var command tea.Cmd
				m.youtubeMusicURLInput, command = m.youtubeMusicURLInput.Update(msg)
				return m, command
			}
		}
		if m.youtubeMusicAddingLocal {
			filteredTracks := m.youtubeMusicFilteredLocalTracks()
			switch msg.String() {
			case "esc":
				if m.youtubeMusicLocalSearching {
					m.youtubeMusicLocalSearching = false
					m.youtubeMusicLocalSearch.SetValue("")
					m.youtubeMusicLocalSearch.Blur()
					m.youtubeMusicLocalSelected = 0
				} else {
					m.youtubeMusicAddingLocal = false
				}
			case "backspace":
				if m.youtubeMusicLocalSearching {
					var command tea.Cmd
					m.youtubeMusicLocalSearch, command = m.youtubeMusicLocalSearch.Update(msg)
					m.youtubeMusicLocalSelected = 0
					return m, command
				}
				m.youtubeMusicAddingLocal = false
			case "/":
				if !m.youtubeMusicLocalSearching {
					m.youtubeMusicLocalSearching = true
					return m, m.youtubeMusicLocalSearch.Focus()
				}
			case "up":
				m.youtubeMusicLocalSelected = max(0, m.youtubeMusicLocalSelected-1)
			case "down":
				m.youtubeMusicLocalSelected = min(max(0, len(filteredTracks)-1), m.youtubeMusicLocalSelected+1)
			case "enter":
				if m.youtubeMusicLocalSelected >= 0 && m.youtubeMusicLocalSelected < len(filteredTracks) {
					m.addCachedTrack(filteredTracks[m.youtubeMusicLocalSelected])
					m.youtubeMusicAddingLocal = false
					m.youtubeMusicLocalSearching = false
					m.youtubeMusicLocalSearch.Blur()
				}
			default:
				if m.youtubeMusicLocalSearching {
					var command tea.Cmd
					m.youtubeMusicLocalSearch, command = m.youtubeMusicLocalSearch.Update(msg)
					m.youtubeMusicLocalSelected = 0
					return m, command
				}
			}
			return m, nil
		}
		if m.paletteOpen {
			switch msg.String() {
			case "esc", "ctrl+p":
				m.paletteOpen = false
			case "enter":
				m.applyTheme(themeCommands[m.paletteSelected])
				m.paletteOpen = false
			case "up":
				m.paletteSelected = (m.paletteSelected - 1 + len(themeCommands)) % len(themeCommands)
			case "down":
				m.paletteSelected = (m.paletteSelected + 1) % len(themeCommands)
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
		case "ctrl+r":
			if !m.youtubeMusicLoading {
				m.cancelYouTubeQueue()
				m.youtubeMusicLoading = true
				m.youtubeMusicErr = nil
				return m, readYouTubeMusicLibrary(m.appContext)
			}
		case "enter":
			if m.youtubeMusicView == youtubeMusicLibraryView && !m.youtubeMusicLoading && len(m.youtubeMusicPlaylists) > 0 {
				selected := m.youtubeMusicPlaylists[m.youtubeMusicPlaylistSelected]
				m.cancelYouTubeQueue()
				m.youtubeMusicCacheStatus = youtubeMusicCacheEvent{}
				m.youtubeMusicLoading = true
				m.youtubeMusicErr = nil
				if selected.Local {
					return m, func() tea.Msg { return youtubeMusicPlaylistMsg{playlist: selected} }
				}
				return m, readYouTubeMusicPlaylist(m.appContext, m.youtubeMusicClient, selected.ID)
			}
			if m.youtubeMusicView == youtubeMusicPlaylistView {
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
				next := loopingPlaylistTrackIndex(m.youtubeMusicPlaylist.Tracks, m.youtubeMusicPlayingIndex+1, 1)
				if m.youtubeMusicShuffle {
					next = shuffleTrackIndex(m.youtubeMusicPlaylist.Tracks, m.youtubeMusicPlayingIndex)
				}
				return m, m.requestYouTubeTrack(next)
			}
		case "left":
			if m.youtubeMusicPlayer != nil {
				previous := loopingPlaylistTrackIndex(m.youtubeMusicPlaylist.Tracks, m.youtubeMusicPlayingIndex-1, -1)
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
		case "a", "A":
			if m.youtubeMusicView == youtubeMusicPlaylistView {
				m.beginLocalTrackPicker()
			}
		case "u", "U":
			if m.youtubeMusicView == youtubeMusicPlaylistView {
				m.youtubeMusicURLInput.SetValue("")
				m.youtubeMusicURLPrompt = true
				return m, m.youtubeMusicURLInput.Focus()
			}
		case "d", "D":
			if m.youtubeMusicView == youtubeMusicPlaylistView {
				m.beginYouTubeMusicDelete(youtubeMusicDeleteTrack)
			} else {
				m.beginYouTubeMusicDelete(youtubeMusicDeletePlaylist)
			}
		case "l", "L":
			if m.youtubeMusicView == youtubeMusicPlaylistView &&
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

	if m.paletteOpen {
		return enforceThemeBackground(m.paletteView(), m.colors.Background)
	}

	hints := []ui.Hint{{Key: "↑↓", Label: "select"}, {Key: "↵", Label: "open"}, {Key: "^R", Label: "refresh"}}
	if m.youtubeMusicDeletePending != youtubeMusicDeleteNone {
		hints = []ui.Hint{{Key: "D", Label: "confirm delete"}, {Key: "esc", Label: "cancel"}}
	} else if m.youtubeMusicView == youtubeMusicPlaylistView {
		playLabel := "play"
		if m.youtubeMusicPlayback.State == playback.StatePlaying {
			playLabel = "pause"
		}
		hints = []ui.Hint{{Key: "↑↓"}, {Key: "↵", Label: "play"}, {Key: "space", Label: playLabel}, {Key: "⇧←→", Label: "seek"}, {Key: "S", Label: "shuffle"}, {Key: "A", Label: "local"}, {Key: "U", Label: "url"}, {Key: "D", Label: "remove"}, {Key: "⌫"}}
		if m.youtubeMusicAddingLocal {
			hints = []ui.Hint{{Key: "↑↓"}, {Key: "↵", Label: "add"}, {Key: "/", Label: "search"}, {Key: "esc", Label: "cancel"}}
			if m.youtubeMusicLocalSearching {
				hints = []ui.Hint{{Key: "↑↓"}, {Key: "↵", Label: "add"}, {Key: "esc", Label: "clear"}}
			}
		} else if m.youtubeMusicURLPrompt {
			hints = []ui.Hint{{Key: "↵", Label: "add"}, {Key: "esc", Label: "cancel"}}
		}
	}
	panel := ui.Panel("MUSIC", m.musicMainView(m.width, m.height), m.width, m.height, true, m.colors, hints...)

	rendered := lipgloss.NewStyle().Background(m.colors.Background).Render(panel)
	return enforceThemeBackground(rendered, m.colors.Background)
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
