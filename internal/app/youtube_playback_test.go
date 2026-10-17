package app

import (
	"context"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"bmp/internal/playback"
	"bmp/internal/youtubemusic"
)

func TestCacheEventUsesStableVideoID(t *testing.T) {
	m := Model{
		youtubeMusicPlaylist:        youtubemusic.Playlist{Tracks: []youtubemusic.Track{{VideoID: "new-video", Available: true}}},
		youtubeMusicCached:          make(map[string]string),
		youtubeMusicCacheGeneration: 7,
		youtubeMusicPendingIndex:    0,
		youtubeMusicResolving:       true,
	}

	updatedModel, _ := m.Update(youtubeMusicCacheEvent{
		generation: 7,
		trackIndex: 0,
		videoID:    "old-video",
		path:       "old-video.mp4",
	})
	updated := updatedModel.(Model)

	if got := updated.youtubeMusicCached["old-video"]; got != "old-video.mp4" {
		t.Fatalf("cached path stored under event video ID = %q", got)
	}
	if got := updated.youtubeMusicCached["new-video"]; got != "" {
		t.Fatalf("cached path incorrectly assigned to current row = %q", got)
	}
	if updated.youtubeMusicPendingIndex != 0 {
		t.Fatalf("mismatched event started pending row; pending index = %d", updated.youtubeMusicPendingIndex)
	}
}

func TestRemovingTrackCancelsCacheQueue(t *testing.T) {
	canceled := false
	m := Model{
		youtubeMusicPlaylist: youtubemusic.Playlist{
			Local:  true,
			Tracks: []youtubemusic.Track{{VideoID: "video", Available: true}},
		},
		youtubeMusicTrackSelected:   0,
		youtubeMusicPlayingIndex:    -1,
		youtubeMusicLoopTrackIndex:  -1,
		youtubeMusicPendingIndex:    0,
		youtubeMusicResolving:       true,
		youtubeMusicCacheGeneration: 3,
		youtubeMusicCacheCancel: func() {
			canceled = true
		},
	}

	m.removeSelectedTrack()

	if !canceled {
		t.Fatal("removing a track did not cancel the active cache queue")
	}
	if m.youtubeMusicCacheGeneration != 4 {
		t.Fatalf("cache generation = %d, want 4", m.youtubeMusicCacheGeneration)
	}
}

func TestMusicTrackDeletionRequiresConfirmation(t *testing.T) {
	m := Model{
		youtubeMusicView: youtubeMusicPlaylistView,
		youtubeMusicPlaylist: youtubemusic.Playlist{
			Local:  true,
			Tracks: []youtubemusic.Track{{VideoID: "first"}, {VideoID: "second"}},
		},
		youtubeMusicPlayingIndex:   -1,
		youtubeMusicLoopTrackIndex: -1,
	}

	updatedModel, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("d")})
	updated := updatedModel.(Model)
	if updated.youtubeMusicDeletePending != youtubeMusicDeleteTrack || len(updated.youtubeMusicPlaylist.Tracks) != 2 {
		t.Fatal("first D did not begin track deletion confirmation")
	}

	updatedModel, _ = updated.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("d")})
	updated = updatedModel.(Model)
	if updated.youtubeMusicDeletePending != youtubeMusicDeleteNone || len(updated.youtubeMusicPlaylist.Tracks) != 1 {
		t.Fatal("second D did not remove the selected track")
	}
}

func TestMusicTrackDeletionCanBeCanceled(t *testing.T) {
	m := Model{
		youtubeMusicView:         youtubeMusicPlaylistView,
		youtubeMusicPlaylist:     youtubemusic.Playlist{Local: true, Tracks: []youtubemusic.Track{{VideoID: "track"}}},
		youtubeMusicPlayingIndex: -1,
	}

	updatedModel, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("d")})
	updated := updatedModel.(Model)
	updatedModel, _ = updated.Update(tea.KeyMsg{Type: tea.KeyEsc})
	updated = updatedModel.(Model)
	if updated.youtubeMusicDeletePending != youtubeMusicDeleteNone || len(updated.youtubeMusicPlaylist.Tracks) != 1 {
		t.Fatal("Esc did not cancel track deletion")
	}
}

func TestVolumeCommandUsesRelativeAdjustment(t *testing.T) {
	player := &recordingPlayer{snapshot: playback.Snapshot{Volume: 10}}

	for range 2 {
		message := setYouTubeMusicVolume(context.Background(), player, 5)()
		if control := message.(youtubeMusicControlMsg); control.err != nil {
			t.Fatalf("adjust volume: %v", control.err)
		}
	}

	if len(player.adjustments) != 2 || player.adjustments[0] != 5 || player.adjustments[1] != 5 {
		t.Fatalf("relative adjustments = %v, want [5 5]", player.adjustments)
	}
	if player.snapshot.Volume != 20 {
		t.Fatalf("volume = %d, want 20", player.snapshot.Volume)
	}
	if player.setVolumeCalls != 0 {
		t.Fatalf("absolute SetVolume called %d times", player.setVolumeCalls)
	}
}

type recordingPlayer struct {
	snapshot       playback.Snapshot
	adjustments    []int
	setVolumeCalls int
}

func (player *recordingPlayer) Play(context.Context, playback.Source) error { return nil }
func (player *recordingPlayer) Toggle(context.Context) error                { return nil }
func (player *recordingPlayer) Stop(context.Context) error                  { return nil }
func (player *recordingPlayer) Seek(context.Context, time.Duration) error   { return nil }
func (player *recordingPlayer) ToggleMute(context.Context) error            { return nil }
func (player *recordingPlayer) SetVolume(_ context.Context, volume int) error {
	player.setVolumeCalls++
	player.snapshot.Volume = volume
	return nil
}
func (player *recordingPlayer) AdjustVolume(_ context.Context, delta int) error {
	player.adjustments = append(player.adjustments, delta)
	player.snapshot.Volume = max(0, min(100, player.snapshot.Volume+delta))
	return nil
}
func (player *recordingPlayer) Snapshot() playback.Snapshot { return player.snapshot }
func (player *recordingPlayer) Close() error                { return nil }
