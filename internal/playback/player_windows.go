//go:build windows

package playback

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-ole/go-ole"
	"github.com/go-ole/go-ole/oleutil"
)

type operation int

const initialVolume = 10

const (
	operationPlay operation = iota
	operationToggle
	operationStop
	operationSeek
	operationSetVolume
	operationAdjustVolume
	operationToggleMute
	operationClose
)

type playerCommand struct {
	operation operation
	source    Source
	mediaURL  string
	volume    int
	delta     int
	position  time.Duration
	paused    bool
	result    chan error
}

type windowsPlayer struct {
	commands  chan playerCommand
	done      chan struct{}
	closeOnce sync.Once

	mu            sync.RWMutex
	snapshot      Snapshot
	mediaFile     string
	volumePending bool

	// A silent start plays muted from 0:00 until audio runs, then seeks to
	// restorePosition and pauses when restorePaused is set. holdMute keeps
	// the output muted until the poll after that, so no pre-seek audio leaks.
	restorePending  bool
	restorePosition time.Duration
	restorePaused   bool
	holdMute        bool
}

// New creates a Windows Media Player COM session on a dedicated OS thread.
func New() (Player, error) {
	player := &windowsPlayer{
		commands: make(chan playerCommand),
		done:     make(chan struct{}),
		snapshot: Snapshot{State: StateIdle},
	}
	ready := make(chan error, 1)
	go player.run(ready)
	if err := <-ready; err != nil {
		return nil, err
	}
	return player, nil
}

func (player *windowsPlayer) Play(ctx context.Context, source Source, start time.Duration, paused bool) error {
	if strings.TrimSpace(source.Path) == "" {
		return errors.New("audio source path is empty")
	}
	mediaURL := filepath.Clean(source.Path)
	info, err := os.Stat(mediaURL)
	if err != nil {
		return fmt.Errorf("open cached audio: %w", err)
	}
	if info.IsDir() {
		return errors.New("cached audio path is a directory")
	}
	return player.send(ctx, playerCommand{
		operation: operationPlay, source: source, mediaURL: mediaURL, position: max(0, start), paused: paused, result: make(chan error, 1),
	})
}

func (player *windowsPlayer) Toggle(ctx context.Context) error {
	return player.send(ctx, playerCommand{operation: operationToggle, result: make(chan error, 1)})
}

func (player *windowsPlayer) Stop(ctx context.Context) error {
	return player.send(ctx, playerCommand{operation: operationStop, result: make(chan error, 1)})
}

func (player *windowsPlayer) Seek(ctx context.Context, position time.Duration) error {
	position = max(0, position)
	return player.send(ctx, playerCommand{operation: operationSeek, position: position, result: make(chan error, 1)})
}

func (player *windowsPlayer) SetVolume(ctx context.Context, volume int) error {
	volume = max(0, min(100, volume))
	return player.send(ctx, playerCommand{operation: operationSetVolume, volume: volume, result: make(chan error, 1)})
}

func (player *windowsPlayer) AdjustVolume(ctx context.Context, delta int) error {
	return player.send(ctx, playerCommand{operation: operationAdjustVolume, delta: delta, result: make(chan error, 1)})
}

func (player *windowsPlayer) ToggleMute(ctx context.Context) error {
	return player.send(ctx, playerCommand{operation: operationToggleMute, result: make(chan error, 1)})
}

func (player *windowsPlayer) Snapshot() Snapshot {
	player.mu.RLock()
	defer player.mu.RUnlock()
	return player.snapshot
}

func (player *windowsPlayer) Close() error {
	var closeErr error
	player.closeOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		closeErr = player.send(ctx, playerCommand{operation: operationClose, result: make(chan error, 1)})
	})
	return closeErr
}

func (player *windowsPlayer) send(ctx context.Context, command playerCommand) error {
	select {
	case <-player.done:
		return ErrClosed
	case <-ctx.Done():
		return ctx.Err()
	case player.commands <- command:
	}
	select {
	case err := <-command.result:
		return err
	case <-player.done:
		if command.operation == operationClose {
			return nil
		}
		return ErrClosed
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (player *windowsPlayer) run(ready chan<- error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	defer close(player.done)

	if err := ole.CoInitialize(0); err != nil {
		ready <- fmt.Errorf("initialize Windows audio COM: %w", err)
		return
	}
	defer ole.CoUninitialize()

	unknown, err := oleutil.CreateObject("WMPlayer.OCX")
	if err != nil {
		ready <- fmt.Errorf("create Windows Media Player: %w", err)
		return
	}
	defer unknown.Release()
	dispatch, err := unknown.QueryInterface(ole.IID_IDispatch)
	if err != nil {
		ready <- fmt.Errorf("open Windows Media Player interface: %w", err)
		return
	}
	defer dispatch.Release()

	controlsVariant, err := oleutil.GetProperty(dispatch, "controls")
	if err != nil {
		ready <- fmt.Errorf("open Windows Media Player controls: %w", err)
		return
	}
	defer controlsVariant.Clear()
	controls := controlsVariant.ToIDispatch()
	if controls == nil {
		ready <- errors.New("Windows Media Player returned no controls")
		return
	}

	settingsVariant, err := oleutil.GetProperty(dispatch, "settings")
	if err != nil {
		ready <- fmt.Errorf("open Windows Media Player settings: %w", err)
		return
	}
	defer settingsVariant.Clear()
	settings := settingsVariant.ToIDispatch()
	if settings == nil {
		ready <- errors.New("Windows Media Player returned no settings")
		return
	}
	if err := putProperty(settings, "autoStart", false); err != nil {
		ready <- fmt.Errorf("configure Windows Media Player: %w", err)
		return
	}
	if err := putProperty(settings, "volume", initialVolume); err != nil {
		ready <- fmt.Errorf("set initial Windows Media Player volume: %w", err)
		return
	}
	if err := putProperty(settings, "mute", false); err != nil {
		ready <- fmt.Errorf("unmute Windows Media Player: %w", err)
		return
	}
	player.mu.Lock()
	player.snapshot.Volume = initialVolume
	player.snapshot.Muted = false
	player.mu.Unlock()

	ready <- nil
	messageTicker := time.NewTicker(15 * time.Millisecond)
	defer messageTicker.Stop()
	pollTicker := time.NewTicker(250 * time.Millisecond)
	defer pollTicker.Stop()
	for {
		select {
		case command := <-player.commands:
			err := player.handleCommand(dispatch, controls, settings, command)
			command.result <- err
			if command.operation == operationClose {
				return
			}
		case <-messageTicker.C:
			pumpWindowsMessages()
		case <-pollTicker.C:
			pumpWindowsMessages()
			player.poll(dispatch, controls, settings)
		}
	}
}

func (player *windowsPlayer) handleCommand(dispatch, controls, settings *ole.IDispatch, command playerCommand) error {
	switch command.operation {
	case operationPlay:
		_ = callMethod(controls, "stop")
		player.releaseMedia(dispatch)
		// The error queue outlives the media it reports on; a stale entry would
		// turn every later stop, including a normal track end, into an error.
		clearMediaErrors(dispatch)
		if result, err := oleutil.PutProperty(dispatch, "URL", command.mediaURL); err != nil {
			return player.commandError("open audio", err)
		} else if result != nil {
			_ = result.Clear()
		}
		snapshot := player.Snapshot()
		if err := putProperty(settings, "volume", snapshot.Volume); err != nil {
			return player.commandError("set volume", err)
		}
		player.restorePending = command.position > 0 || command.paused
		player.restorePosition = command.position
		player.restorePaused = command.paused
		player.holdMute = player.restorePending
		if err := putProperty(settings, "mute", player.holdMute || snapshot.Muted); err != nil {
			return player.commandError("set mute", err)
		}
		if err := callMethod(controls, "play"); err != nil {
			return player.commandError("play audio", err)
		}
		player.mediaFile = command.mediaURL
		player.volumePending = true
		player.mu.Lock()
		volume, muted := player.snapshot.Volume, player.snapshot.Muted
		player.snapshot = Snapshot{State: StateOpening, Source: command.source, Duration: command.source.Duration, Volume: volume, Muted: muted}
		player.mu.Unlock()
		return nil
	case operationToggle:
		if player.restorePending {
			player.restorePaused = !player.restorePaused
			return nil
		}
		state := player.Snapshot().State
		method := "play"
		if state == StatePlaying || state == StateBuffering || state == StateOpening {
			method = "pause"
		}
		if err := callMethod(controls, method); err != nil {
			return player.commandError("toggle audio", err)
		}
		return nil
	case operationStop:
		if err := callMethod(controls, "stop"); err != nil {
			return player.commandError("stop audio", err)
		}
		player.restorePending = false
		player.releaseMute(settings)
		player.mu.Lock()
		player.snapshot.State = StateStopped
		player.snapshot.Position = 0
		player.mu.Unlock()
		return nil
	case operationSeek:
		if player.restorePending {
			player.restorePosition = command.position
			return nil
		}
		seconds := command.position.Seconds()
		if err := putProperty(controls, "currentPosition", seconds); err != nil {
			return player.commandError("seek audio", err)
		}
		player.mu.Lock()
		player.snapshot.Position = command.position
		player.mu.Unlock()
		return nil
	case operationSetVolume:
		if err := player.applyVolume(settings, command.volume); err != nil {
			return player.commandError("set volume", err)
		}
		return nil
	case operationAdjustVolume:
		volume := player.Snapshot().Volume + command.delta
		if err := player.applyVolume(settings, volume); err != nil {
			return player.commandError("adjust volume", err)
		}
		return nil
	case operationToggleMute:
		muted := !player.Snapshot().Muted
		if !player.holdMute {
			if err := putProperty(settings, "mute", muted); err != nil {
				return player.commandError("toggle mute", err)
			}
		}
		player.mu.Lock()
		player.snapshot.Muted = muted
		player.mu.Unlock()
		return nil
	case operationClose:
		if err := callMethod(controls, "stop"); err != nil {
			return player.commandError("stop audio", err)
		}
		player.releaseMedia(dispatch)
		player.mu.Lock()
		player.snapshot.State = StateStopped
		player.snapshot.Position = 0
		player.mu.Unlock()
		return nil
	default:
		return errors.New("unknown audio player operation")
	}
}

func (player *windowsPlayer) applyVolume(settings *ole.IDispatch, volume int) error {
	volume = max(0, min(100, volume))
	if err := putProperty(settings, "volume", volume); err != nil {
		return err
	}
	player.mu.Lock()
	player.snapshot.Volume = volume
	player.mu.Unlock()
	return nil
}

// releaseMute ends a silent start's forced mute, applying the user's setting.
func (player *windowsPlayer) releaseMute(settings *ole.IDispatch) {
	if !player.holdMute {
		return
	}
	player.holdMute = false
	_ = putProperty(settings, "mute", player.Snapshot().Muted)
}

func (player *windowsPlayer) releaseMedia(dispatch *ole.IDispatch) {
	if player.mediaFile == "" {
		return
	}
	if result, err := oleutil.PutProperty(dispatch, "URL", ""); err == nil && result != nil {
		_ = result.Clear()
	}
	player.mediaFile = ""
}

func (player *windowsPlayer) poll(dispatch, controls, settings *ole.IDispatch) {
	stateVariant, err := oleutil.GetProperty(dispatch, "playState")
	if err != nil {
		player.setError(fmt.Errorf("read playback state: %w", err))
		return
	}
	stateValue, ok := integerValue(stateVariant.Value())
	_ = stateVariant.Clear()
	if !ok {
		player.setError(errors.New("Windows Media Player returned an invalid playback state"))
		return
	}

	state := mapPlayState(stateValue)
	if state == StateStopped {
		if playbackErr := mediaError(dispatch); playbackErr != nil {
			player.setError(playbackErr)
			return
		}
	}
	position := durationProperty(controls, "currentPosition")
	duration := time.Duration(0)
	if mediaVariant, mediaErr := oleutil.GetProperty(dispatch, "currentMedia"); mediaErr == nil {
		if media := mediaVariant.ToIDispatch(); media != nil {
			duration = durationProperty(media, "duration")
		}
		_ = mediaVariant.Clear()
	}

	if !player.restorePending {
		player.releaseMute(settings)
	}
	if state == StatePlaying && player.restorePending {
		player.restorePending = false
		_ = putProperty(controls, "currentPosition", player.restorePosition.Seconds())
		position = player.restorePosition
		if player.restorePaused {
			_ = callMethod(controls, "pause")
		}
		// Report opening until the next poll reads the settled state, so the
		// spectrum never starts from the pre-seek position.
		state = StateOpening
	}

	if state == StatePlaying && player.volumePending {
		// A fresh WMPlayer.OCX session, notably after system sleep, can ignore
		// the volume set before playback starts. Re-apply it once audio runs,
		// changing it first because an unchanged value is not pushed again.
		player.volumePending = false
		volume := player.Snapshot().Volume
		nudge := volume - 1
		if volume == 0 {
			nudge = 1
		}
		_ = putProperty(settings, "volume", nudge)
		_ = putProperty(settings, "volume", volume)
	}

	player.mu.Lock()
	if state == StateStopped && (player.snapshot.State == StateEnded || playbackReachedEnd(player.snapshot, duration)) {
		// WMPlayer.OCX commonly jumps directly from playing to stopped and
		// resets currentPosition to zero, never exposing playState 8. Preserve
		// a durable ended state so queue and repeat-one logic can restart it.
		state = StateEnded
	}
	player.snapshot.State = state
	player.snapshot.Position = position
	if !player.holdMute {
		player.snapshot.Muted = boolProperty(settings, "mute")
	}
	if duration > 0 {
		player.snapshot.Duration = duration
	}
	player.snapshot.Err = nil
	player.mu.Unlock()
}

func playbackReachedEnd(previous Snapshot, currentDuration time.Duration) bool {
	if previous.State != StatePlaying && previous.State != StateBuffering {
		return false
	}
	duration := previous.Duration
	if currentDuration > 0 {
		duration = currentDuration
	}
	if duration <= 0 {
		return false
	}
	tolerance := min(1500*time.Millisecond, max(250*time.Millisecond, duration/4))
	return previous.Position >= duration-tolerance
}

func (player *windowsPlayer) commandError(action string, err error) error {
	wrapped := fmt.Errorf("%s: %w", action, err)
	player.setError(wrapped)
	return wrapped
}

func (player *windowsPlayer) setError(err error) {
	player.mu.Lock()
	player.snapshot.State = StateError
	player.snapshot.Err = err
	player.mu.Unlock()
}

func callMethod(dispatch *ole.IDispatch, method string) error {
	result, err := oleutil.CallMethod(dispatch, method)
	if result != nil {
		_ = result.Clear()
	}
	return err
}

func putProperty(dispatch *ole.IDispatch, name string, value any) error {
	result, err := oleutil.PutProperty(dispatch, name, value)
	if result != nil {
		_ = result.Clear()
	}
	return err
}

func boolProperty(dispatch *ole.IDispatch, name string) bool {
	variant, err := oleutil.GetProperty(dispatch, name)
	if err != nil {
		return false
	}
	defer variant.Clear()
	switch value := variant.Value().(type) {
	case bool:
		return value
	case int16:
		return value != 0
	case int32:
		return value != 0
	default:
		return false
	}
}

func clearMediaErrors(dispatch *ole.IDispatch) {
	errorVariant, err := oleutil.GetProperty(dispatch, "error")
	if err != nil {
		return
	}
	defer errorVariant.Clear()
	if errorDispatch := errorVariant.ToIDispatch(); errorDispatch != nil {
		_ = callMethod(errorDispatch, "clearErrorQueue")
	}
}

func mediaError(dispatch *ole.IDispatch) error {
	errorVariant, err := oleutil.GetProperty(dispatch, "error")
	if err != nil {
		return nil
	}
	defer errorVariant.Clear()
	errorDispatch := errorVariant.ToIDispatch()
	if errorDispatch == nil {
		return nil
	}
	countVariant, err := oleutil.GetProperty(errorDispatch, "errorCount")
	if err != nil {
		return nil
	}
	count, ok := integerValue(countVariant.Value())
	_ = countVariant.Clear()
	if !ok || count == 0 {
		return nil
	}
	itemVariant, err := oleutil.GetProperty(errorDispatch, "item", 0)
	if err != nil {
		return errors.New("Windows Media Player reported a playback error")
	}
	defer itemVariant.Clear()
	item := itemVariant.ToIDispatch()
	if item == nil {
		return errors.New("Windows Media Player reported a playback error")
	}
	descriptionVariant, err := oleutil.GetProperty(item, "errorDescription")
	if err != nil {
		return errors.New("Windows Media Player reported a playback error")
	}
	defer descriptionVariant.Clear()
	description, _ := descriptionVariant.Value().(string)
	if description == "" {
		description = "Windows Media Player reported a playback error"
	}
	return errors.New(description)
}

func durationProperty(dispatch *ole.IDispatch, name string) time.Duration {
	value, err := oleutil.GetProperty(dispatch, name)
	if err != nil {
		return 0
	}
	defer value.Clear()
	seconds, ok := floatValue(value.Value())
	if !ok || seconds <= 0 {
		return 0
	}
	return time.Duration(seconds * float64(time.Second))
}

func integerValue(value any) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case int32:
		return int(typed), true
	case uint32:
		return int(typed), true
	case int64:
		return int(typed), true
	case string:
		parsed, err := strconv.Atoi(typed)
		return parsed, err == nil
	default:
		return 0, false
	}
}

func floatValue(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	case int32:
		return float64(typed), true
	case string:
		parsed, err := strconv.ParseFloat(typed, 64)
		return parsed, err == nil
	default:
		return 0, false
	}
}

func mapPlayState(state int) State {
	switch state {
	case 1:
		return StateStopped
	case 2:
		return StatePaused
	case 3:
		return StatePlaying
	case 6, 7, 9, 11:
		return StateBuffering
	case 8:
		return StateEnded
	case 10:
		return StateOpening
	default:
		return StateIdle
	}
}
