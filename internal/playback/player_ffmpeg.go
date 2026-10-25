package playback

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ebitengine/oto/v3"

	"bmp/internal/spectrum"
)

const (
	sampleRate    = 48000
	channelCount  = 2
	frameBytes    = channelCount * 4
	initialVolume = 10
	// outputBuffer is the system audio buffer, which holds this much audio
	// ahead of what plays.
	outputBuffer = 200 * time.Millisecond
)

var (
	audioOnce   sync.Once
	audioOutput *oto.Context
	audioErr    error
)

// openAudio starts the process-wide audio output. oto allows one context per
// process, and it recovers from device changes and system sleep on its own.
func openAudio() (*oto.Context, error) {
	audioOnce.Do(func() {
		output, ready, err := oto.NewContext(&oto.NewContextOptions{
			SampleRate: sampleRate, ChannelCount: channelCount, Format: oto.FormatFloat32LE,
			BufferSize: outputBuffer, ApplicationName: "BMP",
		})
		if err != nil {
			audioErr = fmt.Errorf("open audio output: %w", err)
			return
		}
		<-ready
		if err := output.Err(); err != nil {
			audioErr = fmt.Errorf("open audio output: %w", err)
			return
		}
		audioOutput = output
	})
	return audioOutput, audioErr
}

// ffmpegPlayer decodes the current file with ffmpeg and plays the PCM through
// the system audio output, so any format YouTube serves plays at full quality.
type ffmpegPlayer struct {
	ffmpeg   string
	audio    *oto.Context
	analyzer *spectrum.Analyzer

	mu      sync.Mutex
	source  Source
	volume  int
	muted   bool
	stream  *stream
	stopped bool
	closed  bool
}

// stream is one ffmpeg decode of the source, starting at start.
type stream struct {
	cancel  context.CancelFunc
	ahead   *readAhead
	reader  *pcmReader
	output  *oto.Player
	log     *ffmpegLog
	start   time.Duration
	paused  bool
	exitErr error
}

// New creates a player. The analyzer, when set, receives the PCM as it plays.
func New(analyzer *spectrum.Analyzer) (Player, error) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		return nil, errors.New("ffmpeg is required for playback; run install-deps.cmd")
	}
	raisePriority()
	audio, err := openAudio()
	if err != nil {
		return nil, err
	}
	return &ffmpegPlayer{ffmpeg: ffmpeg, audio: audio, analyzer: analyzer, volume: initialVolume}, nil
}

func (player *ffmpegPlayer) Play(_ context.Context, source Source, start time.Duration, paused bool) error {
	if strings.TrimSpace(source.Path) == "" {
		return errors.New("audio source path is empty")
	}
	source.Path = filepath.Clean(source.Path)
	info, err := os.Stat(source.Path)
	if err != nil {
		return fmt.Errorf("open cached audio: %w", err)
	}
	if info.IsDir() {
		return errors.New("cached audio path is a directory")
	}
	player.mu.Lock()
	defer player.mu.Unlock()
	if player.closed {
		return ErrClosed
	}
	player.source = source
	player.stopped = false
	return player.startLocked(max(0, start), paused)
}

// startLocked replaces the current decode with one starting at start. Paused
// playback is silent from the first sample, which makes restores seamless.
func (player *ffmpegPlayer) startLocked(start time.Duration, paused bool) error {
	player.stopStreamLocked()
	if player.analyzer != nil {
		player.analyzer.Reset()
	}
	ctx, cancel := context.WithCancel(context.Background())
	command := exec.CommandContext(ctx, player.ffmpeg,
		"-hide_banner", "-nostdin", "-nostats", "-loglevel", "level+info",
		"-ss", strconv.FormatFloat(start.Seconds(), 'f', 3, 64), "-i", player.source.Path,
		"-map", "0:a:0", "-vn", "-ac", strconv.Itoa(channelCount), "-ar", strconv.Itoa(sampleRate),
		"-f", "f32le", "pipe:1",
	)
	configureCommand(command)
	// A separate pipe, rather than StdoutPipe, lets Wait run while the audio
	// output is still reading the last decoded samples.
	pipeReader, pipeWriter, err := os.Pipe()
	if err != nil {
		cancel()
		return fmt.Errorf("open audio decoder: %w", err)
	}
	log := &ffmpegLog{}
	command.Stdout, command.Stderr = pipeWriter, log
	if err := command.Start(); err != nil {
		cancel()
		_ = pipeReader.Close()
		_ = pipeWriter.Close()
		return fmt.Errorf("start ffmpeg: %w", err)
	}
	_ = pipeWriter.Close()

	// Two seconds ahead of the audio output costs under 1 MB.
	ahead := newReadAhead(pipeReader, 2*sampleRate*frameBytes)
	reader := &pcmReader{source: ahead, analyzer: player.analyzer}
	output := player.audio.NewPlayer(reader)
	// About 100 ms ahead keeps memory low and the spectrum close to what plays.
	output.SetBufferSize(sampleRate * frameBytes / 10)
	output.SetVolume(player.gainLocked())
	current := &stream{cancel: cancel, ahead: ahead, reader: reader, output: output, log: log, start: start, paused: paused}
	player.stream = current
	go func() {
		err := command.Wait()
		player.mu.Lock()
		defer player.mu.Unlock()
		if err != nil && ctx.Err() == nil {
			current.exitErr = log.failure(err)
		}
	}()
	if !paused {
		output.Play()
	}
	return nil
}

func (player *ffmpegPlayer) stopStreamLocked() {
	if player.stream == nil {
		return
	}
	player.stream.output.Pause()
	player.stream.cancel()
	_ = player.stream.ahead.Close()
	player.stream = nil
}

func (player *ffmpegPlayer) gainLocked() float64 {
	if player.muted {
		return 0
	}
	return float64(player.volume) / 100
}

func (player *ffmpegPlayer) Toggle(context.Context) error {
	player.mu.Lock()
	defer player.mu.Unlock()
	current := player.stream
	if current == nil {
		return nil
	}
	current.paused = !current.paused
	if current.paused {
		current.output.Pause()
	} else {
		current.output.Play()
	}
	return nil
}

func (player *ffmpegPlayer) Stop(context.Context) error {
	player.mu.Lock()
	defer player.mu.Unlock()
	player.stopStreamLocked()
	player.stopped = true
	if player.analyzer != nil {
		player.analyzer.Reset()
	}
	return nil
}

func (player *ffmpegPlayer) Seek(_ context.Context, position time.Duration) error {
	player.mu.Lock()
	defer player.mu.Unlock()
	if player.stream == nil {
		return nil
	}
	return player.startLocked(max(0, position), player.stream.paused)
}

func (player *ffmpegPlayer) SetVolume(_ context.Context, volume int) error {
	player.mu.Lock()
	defer player.mu.Unlock()
	player.volume = max(0, min(100, volume))
	player.applyGainLocked()
	return nil
}

func (player *ffmpegPlayer) AdjustVolume(ctx context.Context, delta int) error {
	player.mu.Lock()
	volume := player.volume + delta
	player.mu.Unlock()
	return player.SetVolume(ctx, volume)
}

func (player *ffmpegPlayer) ToggleMute(context.Context) error {
	player.mu.Lock()
	defer player.mu.Unlock()
	player.muted = !player.muted
	player.applyGainLocked()
	return nil
}

func (player *ffmpegPlayer) applyGainLocked() {
	if player.stream != nil {
		player.stream.output.SetVolume(player.gainLocked())
	}
}

func (player *ffmpegPlayer) Snapshot() Snapshot {
	player.mu.Lock()
	defer player.mu.Unlock()
	snapshot := Snapshot{
		State: StateIdle, Source: player.source, Duration: player.source.Duration,
		Volume: player.volume, Muted: player.muted,
	}
	current := player.stream
	if player.stopped {
		snapshot.State = StateStopped
		return snapshot
	}
	if current == nil {
		return snapshot
	}
	if duration := current.log.duration(); duration > 0 {
		snapshot.Duration = duration
	}
	buffered := int64(current.output.BufferedSize())
	snapshot.Latency = outputBuffer + time.Duration(buffered)*time.Second/(sampleRate*frameBytes)
	played := max(0, current.reader.frames.Load()*frameBytes-buffered) / frameBytes
	snapshot.Position = current.start + time.Duration(played)*time.Second/sampleRate
	err := current.exitErr
	if err == nil {
		err = current.output.Err()
	}
	switch {
	case err != nil:
		snapshot.State, snapshot.Err = StateError, err
	case current.paused:
		snapshot.State = StatePaused
	case current.output.IsPlaying():
		snapshot.State = StatePlaying
		if current.reader.frames.Load() == 0 {
			snapshot.State = StateOpening
		}
	case current.reader.eof.Load():
		snapshot.State = StateEnded
		if snapshot.Duration > 0 {
			snapshot.Position = snapshot.Duration
		}
	default:
		snapshot.State = StateOpening
	}
	return snapshot
}

func (player *ffmpegPlayer) Close() error {
	player.mu.Lock()
	defer player.mu.Unlock()
	player.stopStreamLocked()
	player.closed = true
	return nil
}

// pcmReader hands ffmpeg's PCM to the audio output in whole frames, counting
// them for the position and copying them to the spectrum analyzer.
type pcmReader struct {
	source   io.Reader
	analyzer *spectrum.Analyzer
	frames   atomic.Int64
	eof      atomic.Bool
}

func (reader *pcmReader) Read(buffer []byte) (int, error) {
	buffer = buffer[:len(buffer)/frameBytes*frameBytes]
	if len(buffer) == 0 {
		return 0, nil
	}
	count, err := io.ReadFull(reader.source, buffer)
	count -= count % frameBytes
	if count > 0 {
		reader.frames.Add(int64(count / frameBytes))
		if reader.analyzer != nil {
			reader.analyzer.Push(buffer[:count])
		}
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		reader.eof.Store(true)
		return count, io.EOF
	}
	return count, err
}

var durationPattern = regexp.MustCompile(`Duration: (\d+):(\d{2}):(\d{2}(?:\.\d+)?)`)

// ffmpegLog reads ffmpeg's stderr for the input duration and the last error.
type ffmpegLog struct {
	mu        sync.Mutex
	pending   []byte
	length    time.Duration
	lastError string
}

func (log *ffmpegLog) Write(data []byte) (int, error) {
	log.mu.Lock()
	defer log.mu.Unlock()
	log.pending = append(log.pending, data...)
	for {
		index := bytes.IndexAny(log.pending, "\r\n")
		if index < 0 {
			break
		}
		line := strings.TrimSpace(string(log.pending[:index]))
		log.pending = log.pending[index+1:]
		if match := durationPattern.FindStringSubmatch(line); match != nil && log.length == 0 {
			hours, _ := strconv.Atoi(match[1])
			minutes, _ := strconv.Atoi(match[2])
			seconds, _ := strconv.ParseFloat(match[3], 64)
			log.length = time.Duration(hours)*time.Hour + time.Duration(minutes)*time.Minute + time.Duration(seconds*float64(time.Second))
		} else if strings.Contains(line, "[error]") || strings.Contains(line, "[fatal]") {
			log.lastError = line
		}
	}
	if len(log.pending) > 4096 {
		log.pending = log.pending[:0]
	}
	return len(data), nil
}

func (log *ffmpegLog) duration() time.Duration {
	log.mu.Lock()
	defer log.mu.Unlock()
	return log.length
}

func (log *ffmpegLog) failure(err error) error {
	log.mu.Lock()
	defer log.mu.Unlock()
	if log.lastError != "" {
		return fmt.Errorf("ffmpeg: %s", log.lastError)
	}
	return fmt.Errorf("ffmpeg: %w", err)
}
