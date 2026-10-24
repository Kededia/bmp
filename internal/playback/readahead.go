package playback

import (
	"io"
	"sync"
)

// readAhead keeps up to its capacity of ffmpeg's output decoded ahead of the
// audio output, filled from a goroutine, so a short stall of ffmpeg, such as
// while another program starts, is not heard.
type readAhead struct {
	source io.ReadCloser
	mu     sync.Mutex
	cond   *sync.Cond
	data   []byte
	start  int
	length int
	err    error
	closed bool
}

func newReadAhead(source io.ReadCloser, capacity int) *readAhead {
	ahead := &readAhead{source: source, data: make([]byte, capacity)}
	ahead.cond = sync.NewCond(&ahead.mu)
	go ahead.fill()
	return ahead
}

func (ahead *readAhead) fill() {
	ahead.mu.Lock()
	defer ahead.mu.Unlock()
	for ahead.err == nil {
		for ahead.length == len(ahead.data) && !ahead.closed {
			ahead.cond.Wait()
		}
		if ahead.closed {
			return
		}
		// Read lock-free into the free region, which Read never touches.
		end := (ahead.start + ahead.length) % len(ahead.data)
		free := min(len(ahead.data)-ahead.length, len(ahead.data)-end)
		ahead.mu.Unlock()
		count, err := ahead.source.Read(ahead.data[end : end+free])
		ahead.mu.Lock()
		ahead.length += count
		ahead.err = err
		ahead.cond.Broadcast()
	}
}

// Read returns buffered output, waiting only when none is buffered. Once
// ffmpeg's output ends, it returns the buffer's remainder and then the error.
func (ahead *readAhead) Read(buffer []byte) (int, error) {
	ahead.mu.Lock()
	defer ahead.mu.Unlock()
	for ahead.length == 0 && ahead.err == nil && !ahead.closed {
		ahead.cond.Wait()
	}
	if ahead.closed {
		return 0, io.EOF
	}
	if ahead.length == 0 {
		return 0, ahead.err
	}
	count := copy(buffer, ahead.data[ahead.start:min(len(ahead.data), ahead.start+ahead.length)])
	ahead.start = (ahead.start + count) % len(ahead.data)
	ahead.length -= count
	ahead.cond.Broadcast()
	return count, nil
}

func (ahead *readAhead) Close() error {
	ahead.mu.Lock()
	ahead.closed = true
	ahead.cond.Broadcast()
	ahead.mu.Unlock()
	return ahead.source.Close()
}
