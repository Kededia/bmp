package settings

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// State contains small, user-specific values that should survive restarts.
type State struct {
	Theme   string     `json:"theme,omitempty"`
	Volume  int        `json:"volume"`
	Muted   bool       `json:"muted,omitempty"`
	Shuffle bool       `json:"shuffle,omitempty"`
	Music   MusicState `json:"music,omitempty"`
}

// MusicState identifies the exact playlist row, including intentional duplicates.
type MusicState struct {
	Valid          bool   `json:"valid,omitempty"`
	PlaylistID     string `json:"playlist_id,omitempty"`
	VideoID        string `json:"video_id,omitempty"`
	TrackIndex     int    `json:"track_index"`
	PositionMillis int64  `json:"position_ms,omitempty"`
	Paused         bool   `json:"paused,omitempty"`
	LoopTrackIndex int    `json:"loop_track_index"`
}

func defaultState() State {
	return State{Volume: 10, Music: MusicState{TrackIndex: -1, LoopTrackIndex: -1}}
}

// Load reads the settings file. A missing file is a normal first launch.
func Load() (State, error) {
	state := defaultState()
	path, err := Path()
	if err != nil {
		return state, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return state, nil
	}
	if err != nil {
		return state, fmt.Errorf("read settings: %w", err)
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return defaultState(), fmt.Errorf("decode settings: %w", err)
	}
	state.Volume = max(0, min(100, state.Volume))
	return state, nil
}

// Save atomically replaces the settings file using only the standard library.
func Save(state State) error {
	path, err := Path()
	if err != nil {
		return err
	}
	state.Volume = max(0, min(100, state.Volume))
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("encode settings: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create settings directory: %w", err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".bmp-settings-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary settings: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("protect settings: %w", err)
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write settings: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("flush settings: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close settings: %w", err)
	}
	if err := replaceFile(temporaryPath, path); err != nil {
		return fmt.Errorf("replace settings: %w", err)
	}
	return nil
}
