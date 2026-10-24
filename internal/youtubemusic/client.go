package youtubemusic

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"bmp/internal/youtubemusic/auth"
	"bmp/internal/youtubemusic/innertube"
	streamresolver "bmp/internal/youtubemusic/stream"
)

const maxPlaylistPages = 100

type Account struct {
	Name string
}

type Artist struct {
	Name string
}

type Track struct {
	VideoID   string
	Title     string
	Artists   []Artist
	Duration  time.Duration
	Available bool
}

type Playlist struct {
	ID         string
	Title      string
	TrackCount int
	Tracks     []Track
	Local      bool
}

type Client struct {
	api      *innertube.Client
	resolver *streamresolver.YTDLPResolver
}

// NewFromEnvironmentContext signs in with YTMUSIC_BROWSER or YTMUSIC_COOKIES
// when either is set, else with browser, the one chosen in setup.
func NewFromEnvironmentContext(ctx context.Context, browser string) (*Client, error) {
	cookies := strings.TrimSpace(os.Getenv("YTMUSIC_COOKIES"))
	if value := strings.TrimSpace(os.Getenv("YTMUSIC_BROWSER")); value != "" || cookies != "" {
		browser = value
	}
	return newClient(ctx, browser, cookies)
}

// NewForBrowser signs in with browser alone, so setup can check its choice
// even when the environment would override it.
func NewForBrowser(ctx context.Context, browser string) (*Client, error) {
	return newClient(ctx, browser, "")
}

func newClient(ctx context.Context, browser, cookies string) (*Client, error) {
	if browser = strings.TrimSpace(browser); browser != "" {
		exported, err := auth.CookiesFromBrowser(ctx, browser)
		err = retryAfterUpdate(ctx, err, func() error {
			exported, err = auth.CookiesFromBrowser(ctx, browser)
			return err
		})
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrAuthentication, err)
		}
		cookies = exported
	} else if cookies == "" {
		return nil, fmt.Errorf("%w: choose your browser in setup (Ctrl+P)", ErrNotConfigured)
	}

	authUser := 0
	if value := strings.TrimSpace(os.Getenv("YTMUSIC_AUTH_USER")); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 0 {
			return nil, errors.New("YTMUSIC_AUTH_USER must be a non-negative integer")
		}
		authUser = parsed
	}

	session, err := auth.NewSession(cookies)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrAuthentication, err)
	}
	if _, err := session.Authorization(time.Now()); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrAuthentication, err)
	}
	return &Client{
		api:      innertube.New(session, strconv.Itoa(authUser), strings.TrimSpace(os.Getenv("YTMUSIC_PAGE_ID"))),
		resolver: streamresolver.NewYTDLPResolver(session.Netscape()),
	}, nil
}

// GetAccount returns the profile selected by the current auth-user slot.
func (client *Client) GetAccount(ctx context.Context) (Account, error) {
	if client == nil || client.api == nil {
		return Account{}, errors.New("YouTube Music client is not configured")
	}
	payload, err := client.api.AccountMenu(ctx)
	if err != nil {
		return Account{}, classifyError(err)
	}
	if responseLoggedOut(payload) {
		return Account{}, ErrAuthentication
	}
	account := parseAccount(payload)
	if account.Name == "" {
		return Account{}, fmt.Errorf("%w: no active account details (response: %s)", ErrAuthentication, responseShape(payload))
	}
	return account, nil
}

// GetLibraryPlaylists returns playlists saved in the authenticated account.
func (client *Client) GetLibraryPlaylists(ctx context.Context) ([]Playlist, error) {
	if client == nil || client.api == nil {
		return nil, errors.New("YouTube Music client is not configured")
	}
	payload, err := client.api.Browse(ctx, "FEmusic_liked_playlists", "")
	if err != nil {
		return nil, classifyError(err)
	}
	if responseLoggedOut(payload) {
		return nil, ErrAuthentication
	}
	playlists := parsePlaylists(payload)
	continuation := findLibraryContinuation(payload)
	seen := make(map[string]bool)
	for page := 1; continuation != "" && page < maxPlaylistPages; page++ {
		if seen[continuation] {
			return nil, errors.New("YouTube Music playlist-library continuation loop")
		}
		seen[continuation] = true
		payload, err = client.api.Browse(ctx, "", continuation)
		if err != nil {
			return nil, classifyError(err)
		}
		playlists = append(playlists, parsePlaylists(payload)...)
		continuation = findLibraryContinuation(payload)
	}
	if continuation != "" {
		return nil, fmt.Errorf("YouTube Music playlist library exceeded %d pages", maxPlaylistPages)
	}
	playlists = uniquePlaylists(playlists)
	if len(playlists) == 0 {
		if _, accountErr := client.GetAccount(ctx); accountErr != nil {
			return nil, accountErr
		}
	}
	return playlists, nil
}

// GetPlaylist loads playlist metadata and transparently follows all track
// continuation pages. It accepts either a raw playlist ID or a VL-prefixed ID.
func (client *Client) GetPlaylist(ctx context.Context, id string) (Playlist, error) {
	if client == nil || client.api == nil {
		return Playlist{}, errors.New("YouTube Music client is not configured")
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return Playlist{}, errors.New("playlist ID is empty")
	}
	browseID := id
	if !strings.HasPrefix(browseID, "VL") {
		browseID = "VL" + browseID
	}
	payload, err := client.api.Browse(ctx, browseID, "")
	if err != nil {
		return Playlist{}, classifyError(err)
	}
	if responseLoggedOut(payload) {
		return Playlist{}, ErrAuthentication
	}
	playlist := parsePlaylist(payload)
	// The browse header can contain the owner's channel browse ID. The caller's
	// requested ID is the only authoritative playlist identity.
	playlist.ID = strings.TrimPrefix(browseID, "VL")
	playlist.Tracks = append(playlist.Tracks, parseTracks(payload)...)
	continuation := findPlaylistContinuation(payload)
	seen := make(map[string]bool)
	for page := 1; continuation != "" && page < maxPlaylistPages; page++ {
		if seen[continuation] {
			return Playlist{}, errors.New("YouTube Music playlist continuation loop")
		}
		seen[continuation] = true
		payload, err = client.api.Browse(ctx, "", continuation)
		if err != nil {
			return Playlist{}, classifyError(err)
		}
		playlist.Tracks = append(playlist.Tracks, parseTracks(payload)...)
		continuation = findPlaylistContinuation(payload)
	}
	if continuation != "" {
		return Playlist{}, fmt.Errorf("YouTube Music playlist exceeded %d pages", maxPlaylistPages)
	}
	if playlist.TrackCount == 0 {
		playlist.TrackCount = len(playlist.Tracks)
	}
	if playlist.Title == "" && len(playlist.Tracks) == 0 {
		return Playlist{}, ErrUnavailable
	}
	return playlist, nil
}

// ResolveStream resolves a temporary playable audio URL using the configured resolver.
func (client *Client) ResolveStream(ctx context.Context, videoID string) (streamresolver.Result, error) {
	if client == nil || client.resolver == nil {
		return streamresolver.Result{}, errors.New("YouTube Music stream resolver is not configured")
	}
	resolved, err := client.resolver.Resolve(ctx, videoID)
	err = retryAfterUpdate(ctx, err, func() error {
		resolved, err = client.resolver.Resolve(ctx, videoID)
		return err
	})
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return streamresolver.Result{}, err
		}
		var executionError *exec.Error
		if errors.As(err, &executionError) {
			return streamresolver.Result{}, fmt.Errorf("%w: %w", ErrResolverUnavailable, err)
		}
		return streamresolver.Result{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	return resolved, nil
}

// retryAfterUpdate retries a failed yt-dlp call once after updating yt-dlp,
// since YouTube changes regularly break older releases.
func retryAfterUpdate(ctx context.Context, err error, retry func() error) error {
	if err == nil || ctx.Err() != nil {
		return err
	}
	var executionError *exec.Error
	if errors.As(err, &executionError) {
		return err
	}
	updated, updateErr := streamresolver.UpdateOnce(ctx)
	if updateErr != nil {
		return fmt.Errorf("%w (automatic yt-dlp update failed: %v)", err, updateErr)
	}
	if !updated {
		return err
	}
	return retry()
}

func classifyError(err error) error {
	var httpError *innertube.HTTPError
	if !errors.As(err, &httpError) {
		return err
	}
	switch httpError.StatusCode {
	case http.StatusUnauthorized:
		return fmt.Errorf("%w: %w", ErrSessionExpired, err)
	case http.StatusForbidden:
		return fmt.Errorf("%w: %w", ErrAuthentication, err)
	case http.StatusNotFound:
		return fmt.Errorf("%w: %w", ErrUnavailable, err)
	case http.StatusTooManyRequests:
		return fmt.Errorf("%w: retry after %s: %w", ErrRateLimited, httpError.RetryAfter, err)
	default:
		return err
	}
}
