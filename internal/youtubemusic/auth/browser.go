package auth

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const browserCookieProbeURL = "https://www.youtube.com/watch?v=jNQXAC9IVRw"

type browserExtractor func(ctx context.Context, browserSpec, destination string) error

// CookiesFromBrowser asks yt-dlp to export cookies from a browser/profile. The
// browser specification uses yt-dlp syntax, such as "edge" or
// "chrome:Profile 1". Exported cookies are returned in Netscape format.
func CookiesFromBrowser(ctx context.Context, browserSpec string) (string, error) {
	return cookiesFromBrowser(ctx, browserSpec, runYTDLPBrowserExport)
}

func cookiesFromBrowser(ctx context.Context, browserSpec string, extract browserExtractor) (cookies string, returnErr error) {
	browserSpec = strings.TrimSpace(browserSpec)
	if browserSpec == "" {
		return "", errors.New("YouTube Music browser/profile is empty")
	}
	if strings.ContainsAny(browserSpec, "\r\n\x00") {
		return "", errors.New("YouTube Music browser/profile contains invalid characters")
	}

	directory, err := os.MkdirTemp("", "bmp-ytmusic-browser-*")
	if err != nil {
		return "", fmt.Errorf("create temporary browser-cookie directory: %w", err)
	}
	defer func() {
		if err := os.RemoveAll(directory); err != nil && returnErr == nil {
			cookies = ""
			returnErr = fmt.Errorf("remove temporary browser-cookie directory: %w", err)
		}
	}()
	if err := os.Chmod(directory, 0o700); err != nil {
		return "", fmt.Errorf("secure temporary browser-cookie directory: %w", err)
	}
	destination := filepath.Join(directory, "cookies.txt")
	extractErr := extract(ctx, browserSpec, destination)

	file, err := os.Open(destination)
	if err != nil {
		if extractErr != nil {
			return "", fmt.Errorf("extract cookies from browser %q: %w", browserSpec, extractErr)
		}
		return "", fmt.Errorf("open exported browser cookies: %w", err)
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, 8<<20))
	if err != nil {
		return "", fmt.Errorf("read exported browser cookies: %w", err)
	}
	if len(content) == 0 {
		if extractErr != nil {
			return "", fmt.Errorf("extract cookies from browser %q: %w", browserSpec, extractErr)
		}
		return "", errors.New("yt-dlp exported an empty browser cookie file")
	}
	session, err := NewSession(string(content))
	if err != nil {
		return "", fmt.Errorf("validate exported browser cookies: %w", err)
	}
	if _, err := session.Authorization(time.Now()); err != nil {
		return "", fmt.Errorf("browser %q has no authenticated YouTube session: %w", browserSpec, err)
	}
	// Return a newly rendered jar containing only YouTube-domain cookies; do not
	// retain or pass unrelated browser cookies to the rest of the application.
	return session.Netscape(), nil
}

func runYTDLPBrowserExport(ctx context.Context, browserSpec, destination string) error {
	command := exec.CommandContext(ctx, "yt-dlp",
		"--ignore-config",
		"--cookies-from-browser", browserSpec,
		"--cookies", destination,
		"--simulate",
		"--skip-download",
		"--no-playlist",
		"--quiet",
		browserCookieProbeURL,
	)
	if output, err := command.CombinedOutput(); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return errors.New("yt-dlp is not available on PATH")
		}
		message := strings.TrimSpace(string(output))
		if len(message) > 300 {
			message = message[len(message)-300:]
		}
		if message != "" {
			return fmt.Errorf("yt-dlp failed: %s", message)
		}
		return fmt.Errorf("yt-dlp failed: %w", err)
	}
	return nil
}
