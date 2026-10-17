package spectrum

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"math/cmplx"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	sampleRate = 44100
	windowSize = 4096
	bandCount  = 96
)

// Snapshot is a concurrency-safe view of the latest frequency analysis.
type Snapshot struct {
	Bands  []float64
	Active bool
	Err    error
}

// Analyzer uses ffmpeg to decode the current local track to mono PCM and
// computes logarithmic frequency bands without capturing system audio.
type Analyzer struct {
	ffmpeg string

	mu         sync.RWMutex
	bands      []float64
	active     bool
	err        error
	source     string
	generation uint64
	cancel     context.CancelFunc
	closed     bool
	workers    sync.WaitGroup
}

// New locates ffmpeg and creates an idle analyzer.
func New() (*Analyzer, error) {
	path, err := exec.LookPath("ffmpeg")
	if err != nil {
		return nil, errors.New("ffmpeg is required for the music spectrum")
	}
	return &Analyzer{ffmpeg: path, bands: make([]float64, bandCount)}, nil
}

// Sync starts or stops analysis to match the owned player's current source.
// A stopped analyzer restarts at position, which keeps pause/resume aligned.
func (analyzer *Analyzer) Sync(path string, playing bool, position time.Duration) {
	path = strings.TrimSpace(path)
	analyzer.mu.Lock()
	if analyzer.closed {
		analyzer.mu.Unlock()
		return
	}
	if !playing || path == "" {
		analyzer.stopLocked()
		analyzer.mu.Unlock()
		return
	}
	if analyzer.active && analyzer.source == path {
		analyzer.mu.Unlock()
		return
	}
	analyzer.stopLocked()
	analyzer.generation++
	generation := analyzer.generation
	ctx, cancel := context.WithCancel(context.Background())
	analyzer.cancel = cancel
	analyzer.source = path
	analyzer.active = true
	analyzer.err = nil
	analyzer.workers.Add(1)
	analyzer.mu.Unlock()

	go analyzer.run(ctx, generation, path, max(0, position))
}

// Snapshot returns a copy safe for rendering.
func (analyzer *Analyzer) Snapshot() Snapshot {
	analyzer.mu.RLock()
	defer analyzer.mu.RUnlock()
	bands := append([]float64(nil), analyzer.bands...)
	return Snapshot{Bands: bands, Active: analyzer.active, Err: analyzer.err}
}

// Close terminates ffmpeg and waits for its output reader to finish.
func (analyzer *Analyzer) Close() error {
	analyzer.mu.Lock()
	if analyzer.closed {
		analyzer.mu.Unlock()
		return nil
	}
	analyzer.closed = true
	analyzer.stopLocked()
	analyzer.mu.Unlock()
	analyzer.workers.Wait()
	return nil
}

func (analyzer *Analyzer) stopLocked() {
	if analyzer.cancel != nil {
		analyzer.cancel()
		analyzer.cancel = nil
	}
	analyzer.active = false
	analyzer.source = ""
}

func (analyzer *Analyzer) run(ctx context.Context, generation uint64, path string, position time.Duration) {
	defer analyzer.workers.Done()
	seek := strconv.FormatFloat(position.Seconds(), 'f', 3, 64)
	command := exec.CommandContext(ctx, analyzer.ffmpeg,
		"-hide_banner", "-loglevel", "error", "-nostdin",
		"-re", "-ss", seek, "-i", path,
		"-map", "0:a:0", "-vn", "-ac", "1", "-ar", strconv.Itoa(sampleRate),
		"-f", "f32le", "pipe:1",
	)
	configureCommand(command)
	stdout, err := command.StdoutPipe()
	if err != nil {
		analyzer.finish(generation, fmt.Errorf("open ffmpeg spectrum output: %w", err))
		return
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		analyzer.finish(generation, fmt.Errorf("start ffmpeg spectrum: %w", err))
		return
	}

	windowBytes := make([]byte, windowSize*4)
	previous := make([]float64, bandCount)
	for {
		_, readErr := io.ReadFull(stdout, windowBytes)
		if readErr != nil {
			break
		}
		bands := analyzeWindow(windowBytes, previous)
		copy(previous, bands)
		analyzer.publish(generation, bands)
	}
	waitErr := command.Wait()
	if ctx.Err() != nil {
		analyzer.finish(generation, nil)
		return
	}
	if waitErr != nil {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = waitErr.Error()
		}
		analyzer.finish(generation, fmt.Errorf("ffmpeg spectrum: %s", message))
		return
	}
	analyzer.finish(generation, nil)
}

func (analyzer *Analyzer) publish(generation uint64, bands []float64) {
	analyzer.mu.Lock()
	defer analyzer.mu.Unlock()
	if analyzer.closed || generation != analyzer.generation {
		return
	}
	copy(analyzer.bands, bands)
}

func (analyzer *Analyzer) finish(generation uint64, err error) {
	analyzer.mu.Lock()
	defer analyzer.mu.Unlock()
	if generation != analyzer.generation {
		return
	}
	analyzer.active = false
	analyzer.cancel = nil
	if err != nil {
		analyzer.err = err
	}
}

func analyzeWindow(raw []byte, previous []float64) []float64 {
	samples := make([]complex128, windowSize)
	for index := range samples {
		bits := binary.LittleEndian.Uint32(raw[index*4 : index*4+4])
		value := float64(math.Float32frombits(bits))
		window := 0.5 - 0.5*math.Cos(2*math.Pi*float64(index)/float64(windowSize-1))
		samples[index] = complex(value*window, 0)
	}
	fft(samples)

	bands := make([]float64, bandCount)
	const minimumFrequency = 45.0
	const maximumFrequency = 16000.0
	magnitudes := make([]float64, windowSize/2)
	for bin := 1; bin < len(magnitudes); bin++ {
		magnitudes[bin] = cmplx.Abs(samples[bin]) / (windowSize / 2)
	}
	for band := range bands {
		low := minimumFrequency * math.Pow(maximumFrequency/minimumFrequency, float64(band)/bandCount)
		high := minimumFrequency * math.Pow(maximumFrequency/minimumFrequency, float64(band+1)/bandCount)
		start := max(1, int(math.Ceil(low*windowSize/sampleRate)))
		end := min(len(magnitudes), int(math.Floor(high*windowSize/sampleRate))+1)
		magnitude := 0.0
		if end-start >= 2 {
			power := 0.0
			for bin := start; bin < end; bin++ {
				power += magnitudes[bin] * magnitudes[bin]
			}
			magnitude = math.Sqrt(power / float64(end-start))
		} else {
			// Narrow low-frequency bands can be smaller than one FFT bin. Use
			// interpolation at the logarithmic center rather than duplicating
			// one bin across several adjacent bands.
			center := math.Sqrt(low * high)
			position := center * windowSize / sampleRate
			left := min(len(magnitudes)-2, max(1, int(math.Floor(position))))
			fraction := position - float64(left)
			magnitude = magnitudes[left]*(1-fraction) + magnitudes[left+1]*fraction
		}
		decibels := 20 * math.Log10(max(magnitude, 0.000001))
		level := min(1, max(0, (decibels+72)/66))
		if band < len(previous) {
			if level >= previous[band] {
				level = previous[band]*0.25 + level*0.75
			} else {
				level = previous[band]*0.82 + level*0.18
			}
		}
		bands[band] = level
	}
	return bands
}

func fft(values []complex128) {
	for i, j := 1, 0; i < len(values); i++ {
		bit := len(values) >> 1
		for ; j&bit != 0; bit >>= 1 {
			j ^= bit
		}
		j ^= bit
		if i < j {
			values[i], values[j] = values[j], values[i]
		}
	}
	for length := 2; length <= len(values); length <<= 1 {
		angle := -2 * math.Pi / float64(length)
		root := cmplx.Rect(1, angle)
		for offset := 0; offset < len(values); offset += length {
			factor := complex(1, 0)
			for index := 0; index < length/2; index++ {
				even := values[offset+index]
				odd := values[offset+index+length/2] * factor
				values[offset+index] = even + odd
				values[offset+index+length/2] = even - odd
				factor *= root
			}
		}
	}
}
