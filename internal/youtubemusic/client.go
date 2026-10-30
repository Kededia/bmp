package youtubemusic

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os/exec"
	"regexp"
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
	Thumbnail string `json:",omitempty"`
	// SetVideoID identifies the song's entry in a playlist, which YouTube
	// Music needs to remove or move it.
	SetVideoID string `json:",omitempty"`
}

var thumbnailSize = regexp.MustCompile(`=w\d+-h\d+`)

// CoverURL asks for a YouTube Music thumbnail large enough for any window,
// falling back to the video's own thumbnail for songs added without one.
func CoverURL(track Track) string {
	if track.Thumbnail != "" {
		return thumbnailSize.ReplaceAllString(track.Thumbnail, "=w544-h544")
	}
	return "https://i.ytimg.com/vi/" + track.VideoID + "/hqdefault.jpg"
}

type Playlist struct {
	ID         string
	Title      string
	TrackCount int
	Tracks     []Track
	// Owned is set for playlists the account owns, which BMP edits on
	// YouTube Music itself.
	Owned bool
}

type Client struct {
	api      *innertube.Client
	resolver *streamresolver.YTDLPResolver
}

// ProfileBrowser is the browser setting for BMP's own sign-in window, which
// works whichever browser is installed, Chrome included.
const ProfileBrowser = auth.ProfileBrowser

var ErrNotSignedIn = auth.ErrNotSignedIn

// SignIn opens BMP's sign-in window and returns once the person signed in.
func SignIn(ctx context.Context) error {
	return auth.SignIn(ctx)
}

// New signs in with the browser chosen in setup, by default BMP's own
// sign-in window.
func New(ctx context.Context, browser string) (*Client, error) {
	var cookies string
	var err error
	if browser = strings.TrimSpace(browser); browser == "" || browser == ProfileBrowser {
		cookies, err = auth.CookiesFromProfile(ctx)
	} else {
		cookies, err = auth.CookiesFromBrowser(ctx, browser)
		err = retryAfterUpdate(ctx, err, func() error {
			cookies, err = auth.CookiesFromBrowser(ctx, browser)
			return err
		})
	}
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrAuthentication, err)
	}
	session, err := auth.NewSession(cookies)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrAuthentication, err)
	}
	if _, err := session.Authorization(time.Now()); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrAuthentication, err)
	}
	return &Client{
		api:      innertube.New(session, "0", ""),
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
	playlist.Owned = hasKey(payload, "musicEditablePlaylistDetailHeaderRenderer")
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

// CreatePlaylist creates a private playlist named title, holding videoIDs.
// With songs, it reads the playlist back for their entry IDs, which moving
// and removing them need, retrying while YouTube Music catches up.
func (client *Client) CreatePlaylist(ctx context.Context, title string, videoIDs []string) (Playlist, error) {
	if client == nil || client.api == nil {
		return Playlist{}, errors.New("YouTube Music client is not configured")
	}
	payload, err := client.api.CreatePlaylist(ctx, title, videoIDs)
	if err != nil {
		return Playlist{}, classifyError(err)
	}
	id := stringValue(payload["playlistId"])
	if id == "" {
		return Playlist{}, errors.New("YouTube Music did not create the playlist")
	}
	for attempt := 0; attempt < 5 && len(videoIDs) > 0; attempt++ {
		select {
		case <-ctx.Done():
			return Playlist{}, ctx.Err()
		case <-time.After(time.Second):
		}
		if loaded, err := client.GetPlaylist(ctx, id); err == nil && len(loaded.Tracks) >= len(videoIDs) {
			return loaded, nil
		}
	}
	return Playlist{ID: id, Title: title, Owned: true}, nil
}

// AddToPlaylist adds a song to an owned playlist and returns its entry's ID.
func (client *Client) AddToPlaylist(ctx context.Context, playlistID, videoID string) (string, error) {
	payload, err := client.editPlaylist(ctx, playlistID, map[string]any{
		"action": "ACTION_ADD_VIDEO", "addedVideoId": videoID, "dedupeOption": "DEDUPE_OPTION_SKIP",
	})
	if err != nil {
		return "", err
	}
	return findStringKey(payload, "setVideoId"), nil
}

// RemoveFromPlaylist removes a song's entry from an owned playlist.
func (client *Client) RemoveFromPlaylist(ctx context.Context, playlistID, videoID, setVideoID string) error {
	_, err := client.editPlaylist(ctx, playlistID, map[string]any{
		"action": "ACTION_REMOVE_VIDEO", "removedVideoId": videoID, "setVideoId": setVideoID,
	})
	return err
}

// MoveInPlaylist moves an entry of an owned playlist before another entry,
// or to the end when before is empty.
func (client *Client) MoveInPlaylist(ctx context.Context, playlistID, setVideoID, before string) error {
	action := map[string]any{"action": "ACTION_MOVE_VIDEO_BEFORE", "setVideoId": setVideoID}
	if before != "" {
		action["movedSetVideoIdSuccessor"] = before
	}
	_, err := client.editPlaylist(ctx, playlistID, action)
	return err
}

func (client *Client) editPlaylist(ctx context.Context, playlistID string, action map[string]any) (map[string]any, error) {
	if client == nil || client.api == nil {
		return nil, errors.New("YouTube Music client is not configured")
	}
	payload, err := client.api.EditPlaylist(ctx, playlistID, []map[string]any{action})
	if err != nil {
		return nil, classifyError(err)
	}
	if status := stringValue(payload["status"]); status != "STATUS_SUCCEEDED" {
		return nil, fmt.Errorf("YouTube Music did not change the playlist (%s)", status)
	}
	return payload, nil
}

// songsFilter is the search params YouTube Music's Songs filter sends.
const songsFilter = "EgWKAQIIAWoKEAkQBRAKEAMQBA%3D%3D"

// Search returns the songs YouTube Music finds for query, best match first.
func (client *Client) Search(ctx context.Context, query string) ([]Track, error) {
	if client == nil || client.api == nil {
		return nil, errors.New("YouTube Music client is not configured")
	}
	payload, err := client.api.Search(ctx, query, songsFilter)
	if err != nil {
		return nil, classifyError(err)
	}
	var tracks []Track
	for _, track := range parseTrackRenderers(payload) {
		if track.Available {
			tracks = append(tracks, track)
		}
	}
	return tracks, nil
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

// UpdateYTDLP updates yt-dlp unless this session already did. An outdated
// yt-dlp can keep working while YouTube serves it only worse formats.
func UpdateYTDLP(ctx context.Context) {
	_, _ = streamresolver.UpdateOnce(ctx)
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
