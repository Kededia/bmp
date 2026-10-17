package stream

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
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

	command := exec.CommandContext(ctx, "yt-dlp",
		"--dump-single-json",
		"--no-playlist",
		"--no-warnings",
		"--js-runtimes", "node",
		// Legacy Windows Media Player cannot decode YouTube's fragmented
		// audio-only M4A representation reliably. Prefer a progressive MP4
		// containing AAC audio, with audio-only formats as a fallback.
		"--format", "best[ext=mp4][height<=360]/bestaudio[ext=m4a]/bestaudio/best",
		"--cookies", cookiePath,
		"https://music.youtube.com/watch?v="+videoID,
	)
	output, err := command.Output()
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
