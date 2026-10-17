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

func NewFromEnvironment() (*Cache, error) {
	// Like the plays database, the cache lives beside the settings
	// file, so it does not depend on the directory BMP is launched from.
	settingsPath, err := settings.Path()
	if err != nil {
		return nil, err
	}
	directory := strings.TrimSpace(os.Getenv("BMP_MUSIC_DIR"))
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
		tracks = append(tracks, LocalTrack{
			VideoID: videoID, Title: title, Artist: artist,
			Path: filepath.Join(cache.directory, entry.Name()),
		})
	}
	sort.Slice(tracks, func(i, j int) bool { return tracks[i].Title < tracks[j].Title })
	return tracks, nil
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
		index[name[start+2:len(name)-1]] = filepath.Join(cache.directory, entry.Name())
	}
	cache.index = index
	return nil
}

// Store downloads a stream atomically into the flat cache. Existing files for
// the same video ID are reused rather than duplicated.
func (cache *Cache) Store(
	ctx context.Context,
	videoID, title, artist, container, rawURL string,
	headers map[string]string,
	progress func(Progress),
) (path string, returnErr error) {
	if existing, found, err := cache.Lookup(videoID); err != nil {
		return "", err
	} else if found {
		return existing, nil
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

	extension := safeExtension(container)
	filename := trackFilename(videoID, title, artist, extension)
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

	reader := &progressReader{reader: io.LimitReader(response.Body, maximumTrackSize+1), total: response.ContentLength, report: progress, stall: stall}
	written, err := io.Copy(file, reader)
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
	} else if found {
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
	if progress != nil {
		progress(Progress{Downloaded: written, Total: response.ContentLength})
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
		return "mp4"
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
