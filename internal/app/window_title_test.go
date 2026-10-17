package app

import (
	"testing"

	"bmp/internal/playback"
)

func TestWindowTitleUsesActiveMusic(t *testing.T) {
	model := Model{
		youtubeMusicPlayback: playback.Snapshot{
			State:  playback.StatePlaying,
			Source: playback.Source{Artist: "Artist", Title: "Track"},
		},
	}
	if got := model.windowTitle(); got != "Artist - Track" {
		t.Fatalf("window title = %q", got)
	}

	model.youtubeMusicPlayback.State = playback.StatePaused
	if got := model.windowTitle(); got != "Artist - Track" {
		t.Fatalf("paused window title = %q", got)
	}

	model.youtubeMusicPlayback.Source.Artist = ""
	if got := model.windowTitle(); got != "Track" {
		t.Fatalf("title without artist = %q", got)
	}
}

func TestWindowTitleFallsBackToMusic(t *testing.T) {
	model := Model{}
	if got := model.windowTitle(); got != "Music" {
		t.Fatalf("idle title = %q", got)
	}

	model.youtubeMusicPlayback.State = playback.StateStopped
	model.youtubeMusicPlayback.Source = playback.Source{Artist: "Artist", Title: "Track"}
	if got := model.windowTitle(); got != "Music" {
		t.Fatalf("stopped title = %q", got)
	}
}
