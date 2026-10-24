package spectrum

import (
	"encoding/binary"
	"math"
	"math/cmplx"
	"sync"
)

const (
	sampleRate = 48000
	windowSize = 4096
	bandCount  = 96
)

// Snapshot is a concurrency-safe view of the latest frequency analysis.
type Snapshot struct {
	Bands []float64
}

// Analyzer turns the PCM the player sends to the speakers into logarithmic
// frequency bands, so the spectrum needs no decoder of its own.
type Analyzer struct {
	mu         sync.Mutex
	window     []float64
	samples    []complex128
	magnitudes []float64
	bands      []float64
}

func New() *Analyzer {
	return &Analyzer{
		window:     make([]float64, 0, windowSize),
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
		analyzer.window = append(analyzer.window, float64(left+right)/2)
		if len(analyzer.window) == windowSize {
			analyzer.analyze()
			analyzer.window = analyzer.window[:0]
		}
	}
}

// Reset clears the bands when playback stops or jumps.
func (analyzer *Analyzer) Reset() {
	analyzer.mu.Lock()
	defer analyzer.mu.Unlock()
	analyzer.window = analyzer.window[:0]
	clear(analyzer.bands)
}

// Snapshot returns a copy safe for rendering.
func (analyzer *Analyzer) Snapshot() Snapshot {
	analyzer.mu.Lock()
	defer analyzer.mu.Unlock()
	return Snapshot{Bands: append([]float64(nil), analyzer.bands...)}
}

// analyze turns the full window into bands, smoothed against the previous
// ones. The caller holds mu.
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
