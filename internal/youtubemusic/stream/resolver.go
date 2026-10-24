package stream

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

var urlPattern = regexp.MustCompile(`https?://\S+`)

type Result struct {
	URL           string
	Container     string
	ContentLength int64
	HTTPHeaders   map[string]string
	Title         string
	Artist        string
	Duration      time.Duration
}

// YTDLPResolver uses yt-dlp JSON output. yt-dlp must be available on PATH.
type YTDLPResolver struct {
	cookies string
}

func NewYTDLPResolver(netscapeCookies string) *YTDLPResolver {
	return &YTDLPResolver{cookies: netscapeCookies}
}

func (resolver *YTDLPResolver) Resolve(ctx context.Context, videoID string) (resolved Result, returnErr error) {
	videoID = strings.TrimSpace(videoID)
	if videoID == "" || strings.ContainsAny(videoID, "&?/#\\") {
		return Result{}, errors.New("invalid YouTube video ID")
	}
	cookieFile, err := os.CreateTemp("", "bmp-ytmusic-cookies-*.txt")
	if err != nil {
		return Result{}, fmt.Errorf("create temporary yt-dlp cookie file: %w", err)
	}
	cookiePath := cookieFile.Name()
	defer func() {
		if err := os.Remove(cookiePath); err != nil && !errors.Is(err, os.ErrNotExist) && returnErr == nil {
			resolved = Result{}
			returnErr = fmt.Errorf("remove temporary yt-dlp cookie file: %w", err)
		}
	}()
	if err := cookieFile.Chmod(0o600); err == nil {
		_, err = cookieFile.WriteString(resolver.cookies)
	} else {
		_ = cookieFile.Close()
		return Result{}, fmt.Errorf("secure temporary yt-dlp cookie file: %w", err)
	}
	closeErr := cookieFile.Close()
	if err != nil {
		return Result{}, fmt.Errorf("write temporary yt-dlp cookie file: %w", err)
	}
	if closeErr != nil {
		return Result{}, fmt.Errorf("close temporary yt-dlp cookie file: %w", closeErr)
	}

	arguments := []string{
		"--ignore-config",
		"--dump-single-json",
		"--no-playlist",
		"--no-warnings",
		"--js-runtimes", "node",
		// ffmpeg plays any codec, so take the best audio-only stream: Opus at
		// about 256 kbps for YouTube Music Premium when bgutil-pot provides
		// the PO token, else Opus at about 130 kbps. Only direct downloads
		// qualify: an HLS format would be saved as its playlist.
		// Dynamic-range-compressed variants are a last resort.
		"--format", "bestaudio[protocol=https][format_id!*=drc]/bestaudio[protocol=https]/best[protocol=https]",
		"--cookies", cookiePath,
	}
	arguments = append(arguments, tokenArguments()...)
	command := exec.CommandContext(ctx, "yt-dlp", append(arguments, "https://music.youtube.com/watch?v="+videoID)...)
	update.RLock()
	output, err := command.Output()
	update.RUnlock()
	if err != nil {
		if executionError, ok := err.(*exec.Error); ok {
			return Result{}, fmt.Errorf("yt-dlp is unavailable: %w", executionError)
		}
		if exitError, ok := err.(*exec.ExitError); ok {
			message := strings.TrimSpace(string(exitError.Stderr))
			if message != "" {
				return Result{}, fmt.Errorf("yt-dlp failed: %s", sanitize(message))
			}
		}
		return Result{}, fmt.Errorf("run yt-dlp: %w", err)
	}
	return parseOutput(output)
}

// tokenArguments points yt-dlp at bgutil-pot, which yt-dlp starts on demand
// to create the PO token YouTube requires for Premium audio formats. Its
// plugin is loaded from a yt-dlp-plugins folder beside it, as install-deps.cmd
// lays them out, and from yt-dlp's default plugin folders.
func tokenArguments() []string {
	executable, err := exec.LookPath("bgutil-pot")
	if err != nil {
		return nil
	}
	if absolute, err := filepath.Abs(executable); err == nil {
		executable = absolute
	}
	arguments := []string{"--extractor-args", "youtubepot-bgutilcli:cli_path=" + executable}
	plugins := filepath.Join(filepath.Dir(executable), "yt-dlp-plugins")
	if info, err := os.Stat(plugins); err == nil && info.IsDir() {
		arguments = append(arguments, "--plugin-dirs", "default", "--plugin-dirs", plugins)
	}
	return arguments
}

// update serializes self-updates against resolves, so no resolve starts
// yt-dlp while the updater is replacing it.
var update struct {
	sync.RWMutex
	tried bool
}

// UpdateOnce runs yt-dlp's self-updater the first time it is called and
// reports whether that changed the installed version. Later calls do nothing,
// so a session never spends more than one update on failing requests.
func UpdateOnce(ctx context.Context) (bool, error) {
	update.Lock()
	defer update.Unlock()
	if update.tried {
		return false, nil
	}
	update.tried = true
	before, err := version(ctx)
	if err != nil {
		return false, err
	}
	if output, err := exec.CommandContext(ctx, "yt-dlp", "--ignore-config", "-U").CombinedOutput(); err != nil {
		if message := strings.TrimSpace(string(output)); message != "" {
			return false, fmt.Errorf("update yt-dlp: %s", sanitize(message))
		}
		return false, fmt.Errorf("update yt-dlp: %w", err)
	}
	after, err := version(ctx)
	if err != nil {
		return false, err
	}
	return after != before, nil
}

func version(ctx context.Context) (string, error) {
	output, err := exec.CommandContext(ctx, "yt-dlp", "--version").Output()
	if err != nil {
		return "", fmt.Errorf("read yt-dlp version: %w", err)
	}
	return strings.TrimSpace(string(output)), nil
}

func parseOutput(output []byte) (Result, error) {
	var payload struct {
		URL       string            `json:"url"`
		Extension string            `json:"ext"`
		FileSize  int64             `json:"filesize"`
		Headers   map[string]string `json:"http_headers"`
		Title     string            `json:"title"`
		Artist    string            `json:"artist"`
		Uploader  string            `json:"uploader"`
		Duration  float64           `json:"duration"`
	}
	if err := json.Unmarshal(output, &payload); err != nil {
		return Result{}, fmt.Errorf("decode yt-dlp output: %w", err)
	}
	if strings.TrimSpace(payload.URL) == "" {
		return Result{}, errors.New("yt-dlp returned no playable URL")
	}
	artist := strings.TrimSpace(payload.Artist)
	if artist == "" {
		artist = strings.TrimSpace(payload.Uploader)
	}
	return Result{
		URL: payload.URL, Container: payload.Extension, ContentLength: payload.FileSize,
		HTTPHeaders: payload.Headers, Title: payload.Title, Artist: artist,
		Duration: time.Duration(payload.Duration * float64(time.Second)),
	}, nil
}

func sanitize(message string) string {
	lines := strings.Split(strings.ReplaceAll(message, "\r\n", "\n"), "\n")
	if len(lines) > 3 {
		lines = lines[len(lines)-3:]
	}
	message = urlPattern.ReplaceAllString(strings.Join(lines, " "), "[URL omitted]")
	if len(message) > 500 {
		message = message[:500] + "…"
	}
	return message
}
