package spectrum

import (
	"encoding/binary"
	"math"
	"math/cmplx"
	"sync"
	"time"
)

const (
	sampleRate = 48000
	windowSize = 4096
	bandCount  = 96
	// historySize keeps about 1.4 seconds of audio, enough to reach back
	// past the output buffers to what plays now.
	historySize = 1 << 16
	// analysisStep is how far the window moves before it is analyzed again,
	// so extra renders do not speed up the smoothing.
	analysisStep = 1024
)

// Snapshot is a concurrency-safe view of the latest frequency analysis.
type Snapshot struct {
	Bands []float64
}

// Analyzer turns the PCM the player sends to the speakers into logarithmic
// frequency bands, so the spectrum needs no decoder of its own. The PCM
// arrives before it plays, as it then waits in the output buffers, so the
// analyzer keeps recent audio and analyzes what plays now.
type Analyzer struct {
	mu          sync.Mutex
	history     []float32
	written     int64
	analyzedEnd int64
	window      []float64
	samples     []complex128
	magnitudes  []float64
	bands       []float64
}

func New() *Analyzer {
	return &Analyzer{
		history:    make([]float32, historySize),
		window:     make([]float64, windowSize),
		samples:    make([]complex128, windowSize),
		magnitudes: make([]float64, windowSize/2),
		bands:      make([]float64, bandCount),
	}
}

// Push adds interleaved stereo float32 little-endian PCM at 48 kHz.
func (analyzer *Analyzer) Push(pcm []byte) {
	analyzer.mu.Lock()
	defer analyzer.mu.Unlock()
	for offset := 0; offset+8 <= len(pcm); offset += 8 {
		left := math.Float32frombits(binary.LittleEndian.Uint32(pcm[offset:]))
		right := math.Float32frombits(binary.LittleEndian.Uint32(pcm[offset+4:]))
		analyzer.history[analyzer.written%historySize] = (left + right) / 2
		analyzer.written++
	}
}

// Reset clears the bands when playback stops or jumps.
func (analyzer *Analyzer) Reset() {
	analyzer.mu.Lock()
	defer analyzer.mu.Unlock()
	analyzer.written, analyzer.analyzedEnd = 0, 0
	clear(analyzer.bands)
}

// Snapshot returns the bands of the audio playing now, which is delay older
// than the newest pushed audio, as a copy safe for rendering.
func (analyzer *Analyzer) Snapshot(delay time.Duration) Snapshot {
	analyzer.mu.Lock()
	defer analyzer.mu.Unlock()
	end := analyzer.written - int64(delay.Seconds()*sampleRate)
	end = max(end, analyzer.written-historySize+windowSize)
	if end >= windowSize && (end-analyzer.analyzedEnd >= analysisStep || end < analyzer.analyzedEnd) {
		for index := range analyzer.window {
			analyzer.window[index] = float64(analyzer.history[(end-windowSize+int64(index))%historySize])
		}
		analyzer.analyze()
		analyzer.analyzedEnd = end
	}
	return Snapshot{Bands: append([]float64(nil), analyzer.bands...)}
}

// analyze turns the window into bands, smoothed against the previous ones.
// The caller holds mu.
func (analyzer *Analyzer) analyze() {
	samples := analyzer.samples
	for index, value := range analyzer.window {
		window := 0.5 - 0.5*math.Cos(2*math.Pi*float64(index)/float64(windowSize-1))
		samples[index] = complex(value*window, 0)
	}
	fft(samples)

	const minimumFrequency = 45.0
	const maximumFrequency = 16000.0
	magnitudes := analyzer.magnitudes
	for bin := 1; bin < len(magnitudes); bin++ {
		magnitudes[bin] = cmplx.Abs(samples[bin]) / (windowSize / 2)
	}
	for band := range analyzer.bands {
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
		previous := analyzer.bands[band]
		if level >= previous {
			level = previous*0.25 + level*0.75
		} else {
			level = previous*0.82 + level*0.18
		}
		analyzer.bands[band] = level
	}
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
