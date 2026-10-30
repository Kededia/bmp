// Package musiccache stores resolved YouTube Music tracks in one flat local directory.
package musiccache

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"bmp/internal/settings"
)

// stallTimeout aborts a download that receives no data for this long, such as
// one whose connection died during system sleep.
const stallTimeout = 30 * time.Second

var errStalled = errors.New("download stalled")

const maximumTrackSize = int64(256 << 20)

// Progress reports an in-progress track download.
type Progress struct {
	Downloaded int64
	Total      int64
}

type LocalTrack struct {
	VideoID string
	Title   string
	Artist  string
	Path    string
}

// Cache stores every track at the root of a single directory. A video's ID is
// embedded in its filename, so the same song referenced by several playlists
// occupies only one file.
type Cache struct {
	directory string

	mu    sync.Mutex
	index map[string]string // video ID to path, loaded on first Lookup
}

// New opens the cache in directory, the folder chosen in setup, else in the
// default folder.
func New(directory string) (*Cache, error) {
	// Like the plays database, the cache lives beside the settings
	// file, so it does not depend on the directory BMP is launched from.
	settingsPath, err := settings.Path()
	if err != nil {
		return nil, err
	}
	directory = strings.TrimSpace(directory)
	if directory == "" {
		directory = "music"
		if strings.HasPrefix(filepath.Base(settingsPath), ".") {
			directory = ".bmp-music"
		}
	}
	absolute := directory
	if !filepath.IsAbs(directory) {
		absolute = filepath.Join(filepath.Dir(settingsPath), directory)
	}
	if err := os.MkdirAll(absolute, 0o755); err != nil {
		return nil, fmt.Errorf("create music cache directory: %w", err)
	}
	return &Cache{directory: filepath.Clean(absolute)}, nil
}

func (cache *Cache) Directory() string {
	if cache == nil {
		return ""
	}
	return cache.directory
}

func (cache *Cache) Tracks() ([]LocalTrack, error) {
	entries, err := os.ReadDir(cache.directory)
	if err != nil {
		return nil, err
	}
	tracks := make([]LocalTrack, 0, len(entries))
	positions := make(map[string]int, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || strings.HasSuffix(entry.Name(), ".part") {
			continue
		}
		name := strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))
		end := strings.LastIndex(name, "]")
		start := strings.LastIndex(name, " [")
		if start < 0 || end != len(name)-1 || start+2 >= end {
			continue
		}
		display, videoID := name[:start], name[start+2:end]
		artist, title := "", display
		if before, after, found := strings.Cut(display, " - "); found {
			artist, title = before, after
		}
		track := LocalTrack{
			VideoID: videoID, Title: title, Artist: artist,
			Path: filepath.Join(cache.directory, entry.Name()),
		}
		// An upgraded song can briefly sit beside its old file; list it once.
		if position, seen := positions[videoID]; seen {
			if Legacy(tracks[position].Path) && !Legacy(track.Path) {
				tracks[position] = track
			}
			continue
		}
		positions[videoID] = len(tracks)
		tracks = append(tracks, track)
	}
	sort.Slice(tracks, func(i, j int) bool { return tracks[i].Title < tracks[j].Title })
	return tracks, nil
}

// Size returns the total size of the finished tracks in the cache.
func (cache *Cache) Size() (int64, error) {
	entries, err := os.ReadDir(cache.directory)
	if err != nil {
		return 0, fmt.Errorf("read music cache: %w", err)
	}
	var total int64
	for _, entry := range entries {
		if entry.IsDir() || strings.HasSuffix(entry.Name(), ".part") {
			continue
		}
		if info, err := entry.Info(); err == nil {
			total += info.Size()
		}
	}
	return total, nil
}

// Lookup finds any completed file carrying videoID, independently of title or
// artist changes. Partial downloads are ignored.
func (cache *Cache) Lookup(videoID string) (string, bool, error) {
	if cache == nil {
		return "", false, errors.New("music cache is not configured")
	}
	videoID = strings.TrimSpace(videoID)
	if videoID == "" {
		return "", false, errors.New("video ID is empty")
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if cache.index == nil {
		if err := cache.scan(); err != nil {
			return "", false, err
		}
	}
	path, found := cache.index[videoID]
	if found {
		if _, err := os.Stat(path); err != nil {
			// The file was renamed or removed outside BMP; rescan once.
			if err := cache.scan(); err != nil {
				return "", false, err
			}
			path, found = cache.index[videoID]
		}
	}
	return path, found, nil
}

// scan rebuilds the video ID index from the directory. The caller holds mu.
func (cache *Cache) scan() error {
	entries, err := os.ReadDir(cache.directory)
	if err != nil {
		return fmt.Errorf("read music cache: %w", err)
	}
	index := make(map[string]string, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || strings.HasSuffix(entry.Name(), ".part") {
			continue
		}
		name := strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))
		start := strings.LastIndex(name, " [")
		if start < 0 || !strings.HasSuffix(name, "]") || start+2 >= len(name)-1 {
			continue
		}
		videoID, path := name[start+2:len(name)-1], filepath.Join(cache.directory, entry.Name())
		if existing, seen := index[videoID]; seen {
			// Keep the upgraded file and remove an old one left behind while
			// it was playing.
			if Legacy(path) {
				_ = os.Remove(path)
				continue
			}
			if Legacy(existing) {
				_ = os.Remove(existing)
			}
		}
		index[videoID] = path
	}
	cache.index = index
	return nil
}

// Legacy reports whether path is a song stored in BMP's old 360p video format,
// which is replaced by the best audio-only stream when it comes up to play.
func Legacy(path string) bool {
	return strings.EqualFold(filepath.Ext(path), ".mp4")
}

// Store downloads a stream atomically into the flat cache. Existing files for
// the same video ID are reused rather than duplicated.
func (cache *Cache) Store(
	ctx context.Context,
	videoID, title, artist, container, rawURL string,
	headers map[string]string,
	progress func(Progress),
) (path string, returnErr error) {
	// An old 360p file is downloaded again and replaced.
	previous, found, err := cache.Lookup(videoID)
	if err != nil {
		return "", err
	}
	if found && !Legacy(previous) {
		return previous, nil
	}
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	stall := time.AfterFunc(stallTimeout, func() { cancel(errStalled) })
	defer stall.Stop()
	defer func() {
		// Report a stall as a failure rather than a cancellation, so the queue
		// moves on instead of stopping as if the user had cancelled it.
		if returnErr != nil && errors.Is(context.Cause(ctx), errStalled) {
			returnErr = fmt.Errorf("download music: no data for %s", stallTimeout)
		}
	}()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", fmt.Errorf("prepare music download: %w", err)
	}
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return "", fmt.Errorf("download music: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", fmt.Errorf("download music: HTTP %s", response.Status)
	}
	if response.ContentLength > maximumTrackSize {
		return "", fmt.Errorf("download music: track is too large (%d bytes)", response.ContentLength)
	}
	reader := &progressReader{reader: response.Body, total: response.ContentLength, report: progress, stall: stall}
	if path, err = cache.write(videoID, title, artist, container, reader, previous, found); err != nil {
		return "", err
	}
	if progress != nil {
		progress(Progress{Downloaded: reader.downloaded, Total: response.ContentLength})
	}
	return path, nil
}

// Put saves a song received whole, such as one uploaded to the server.
func (cache *Cache) Put(videoID, title, artist, container string, body io.Reader) (string, error) {
	previous, found, err := cache.Lookup(videoID)
	if err != nil {
		return "", err
	}
	if found && !Legacy(previous) {
		return previous, nil
	}
	return cache.write(videoID, title, artist, container, body, previous, found)
}

// write saves body as videoID's file, through a partial file renamed once
// complete, and replaces previous, the song's old file when found.
func (cache *Cache) write(videoID, title, artist, container string, body io.Reader, previous string, found bool) (path string, returnErr error) {
	filename := trackFilename(videoID, title, artist, safeExtension(container))
	path = filepath.Join(cache.directory, filename)
	partial := path + ".part"
	file, err := os.OpenFile(partial, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return "", fmt.Errorf("create cached track: %w", err)
	}
	defer func() {
		if returnErr != nil {
			_ = file.Close()
			_ = os.Remove(partial)
			path = ""
		}
	}()

	written, err := io.Copy(file, io.LimitReader(body, maximumTrackSize+1))
	if err != nil {
		return "", fmt.Errorf("download music: %w", err)
	}
	if written > maximumTrackSize {
		return "", errors.New("download music: track exceeded 256 MiB")
	}
	if written == 0 {
		return "", errors.New("download music: track was empty")
	}
	if err := file.Sync(); err != nil {
		return "", fmt.Errorf("flush cached track: %w", err)
	}
	if err := file.Close(); err != nil {
		return "", fmt.Errorf("close cached track: %w", err)
	}
	if existing, found, err := cache.Lookup(videoID); err != nil {
		return "", err
	} else if found && !Legacy(existing) {
		_ = os.Remove(partial)
		return existing, nil
	}
	if err := os.Rename(partial, path); err != nil {
		return "", fmt.Errorf("finish cached track: %w", err)
	}
	cache.mu.Lock()
	if cache.index != nil {
		cache.index[videoID] = path
	}
	cache.mu.Unlock()
	if found && previous != path {
		// This fails while the old file is playing; the next scan removes it.
		_ = os.Remove(previous)
	}
	return path, nil
}

type progressReader struct {
	reader     io.Reader
	downloaded int64
	total      int64
	report     func(Progress)
	stall      *time.Timer
}

func (reader *progressReader) Read(buffer []byte) (int, error) {
	count, err := reader.reader.Read(buffer)
	if count > 0 {
		reader.stall.Reset(stallTimeout)
	}
	reader.downloaded += int64(count)
	if count > 0 && reader.report != nil {
		reader.report(Progress{Downloaded: reader.downloaded, Total: reader.total})
	}
	return count, err
}

func trackFilename(videoID, title, artist, extension string) string {
	videoID = safeComponent(videoID, 24)
	title = safeComponent(title, 100)
	artist = safeComponent(artist, 60)
	base := title
	if artist != "" {
		base = artist + " - " + title
	}
	if base == "" {
		base = "Unknown track"
	}
	return base + " [" + videoID + "]." + extension
}

func safeExtension(extension string) string {
	extension = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(extension), "."))
	if extension == "" || strings.IndexFunc(extension, func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < '0' || r > '9')
	}) >= 0 {
		return "m4a"
	}
	return extension
}

func safeComponent(value string, maximum int) string {
	value = strings.TrimSpace(value)
	value = strings.Map(func(r rune) rune {
		if r < 32 || strings.ContainsRune(`<>:"/\\|?*`, r) {
			return '-'
		}
		if unicode.IsSpace(r) {
			return ' '
		}
		return r
	}, value)
	value = strings.Join(strings.Fields(value), " ")
	value = strings.Trim(value, ". ")
	runes := []rune(value)
	if len(runes) > maximum {
		value = strings.TrimSpace(string(runes[:maximum]))
	}
	return value
}
