package app

import (
	"strings"

	"bmp/internal/playback"
)

func (m Model) windowTitle() string {
	snapshot := m.youtubeMusicPlayback
	if snapshot.State == playback.StateOpening || snapshot.State == playback.StateBuffering ||
		snapshot.State == playback.StatePlaying || snapshot.State == playback.StatePaused {
		track := windowTitleText(snapshot.Source.Title)
		if track != "" {
			artist := windowTitleText(snapshot.Source.Artist)
			if artist != "" {
				return artist + " - " + track
			}
			return track
		}
	}
	return "Music"
}

func windowTitleText(value string) string {
	value = strings.Join(strings.Fields(sanitizeTerminalText(value, false)), " ")
	characters := []rune(value)
	if len(characters) > 160 {
		return string(characters[:160])
	}
	return value
}
