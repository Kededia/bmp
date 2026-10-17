package app

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"bmp/internal/playback"
	"bmp/internal/settings"
)

const settingsSaveInterval = 5 * time.Second

func saveSettings(state settings.State, generation int) tea.Cmd {
	return func() tea.Msg {
		err := settings.Save(state)
		return settingsSavedMsg{generation: generation, savedAt: time.Now(), err: err}
	}
}

func (m *Model) captureSettings() {
	m.persistedSettings.Theme = m.colors.Name
	m.persistedSettings.Shuffle = m.youtubeMusicShuffle
	if m.youtubeMusicPlayer != nil {
		snapshot := m.youtubeMusicPlayer.Snapshot()
		m.persistedSettings.Volume = snapshot.Volume
		m.persistedSettings.Muted = snapshot.Muted
		if m.youtubeMusicPlayingIndex >= 0 && m.youtubeMusicPlayingIndex < len(m.youtubeMusicPlaylist.Tracks) {
			track := m.youtubeMusicPlaylist.Tracks[m.youtubeMusicPlayingIndex]
			m.persistedSettings.Music = settings.MusicState{
				Valid:          true,
				PlaylistID:     m.youtubeMusicPlaylist.ID,
				VideoID:        track.VideoID,
				TrackIndex:     m.youtubeMusicPlayingIndex,
				PositionMillis: snapshot.Position.Milliseconds(),
				Paused:         snapshot.State == playback.StatePaused,
				LoopTrackIndex: m.youtubeMusicLoopTrackIndex,
			}
		}
	}
}

func (m *Model) markSettingsDirty() {
	previous := m.persistedSettings
	m.captureSettings()
	if m.persistedSettings == previous {
		return
	}
	m.settingsDirty = true
	m.settingsGeneration++
}

func (m *Model) settingsSaveCommand(now time.Time) tea.Cmd {
	if !m.settingsDirty || m.settingsSaving || now.Sub(m.settingsLastSaved) < settingsSaveInterval {
		return nil
	}
	m.captureSettings()
	m.settingsSaving = true
	return saveSettings(m.persistedSettings, m.settingsGeneration)
}

func (m *Model) restoreMusicPositionCommand() tea.Cmd {
	if m.musicRestoreTrackIndex < 0 || m.youtubeMusicPlayingIndex != m.musicRestoreTrackIndex ||
		m.youtubeMusicPlayback.State != playback.StatePlaying {
		return nil
	}
	position := m.musicRestorePosition
	paused := m.musicRestorePaused
	m.musicRestoreTrackIndex = -1
	m.musicRestorePosition = 0
	m.musicRestorePaused = false
	return restoreYouTubeMusicPosition(m.appContext, m.youtubeMusicPlayer, position, paused)
}

func (m *Model) saveSettingsNow() {
	m.captureSettings()
	_ = settings.Save(m.persistedSettings)
	m.settingsDirty = false
	m.settingsLastSaved = time.Now()
}
