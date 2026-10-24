package playback

import (
	"context"
	"errors"
	"time"
)

var (
	ErrUnsupported = errors.New("audio playback is not supported on this platform")
	ErrClosed      = errors.New("audio player is closed")
)

type State string

const (
	StateIdle      State = "idle"
	StateOpening   State = "opening"
	StatePlaying   State = "playing"
	StatePaused    State = "paused"
	StateBuffering State = "buffering"
	StateEnded     State = "ended"
	StateStopped   State = "stopped"
	StateError     State = "error"
)

// Source describes one resolved audio item.
type Source struct {
	Path     string
	Title    string
	Artist   string
	Duration time.Duration
}

// Snapshot is a concurrency-safe view of the current player state.
type Snapshot struct {
	State    State
	Source   Source
	Position time.Duration
	Duration time.Duration
	Volume   int
	Muted    bool
	Err      error
}

// Player controls one local audio output session.
type Player interface {
	// Play opens source at start, left paused there when paused is set. A
	// non-zero start or a paused open is silent until it takes effect.
	Play(ctx context.Context, source Source, start time.Duration, paused bool) error
	Toggle(ctx context.Context) error
	Stop(ctx context.Context) error
	Seek(ctx context.Context, position time.Duration) error
	SetVolume(ctx context.Context, volume int) error
	AdjustVolume(ctx context.Context, delta int) error
	ToggleMute(ctx context.Context) error
	Snapshot() Snapshot
	Close() error
}
