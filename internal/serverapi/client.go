// Package serverapi is BMP's side of the BMP server: it uploads songs and
// sends playlists, plays and edits there, and downloads songs from it.
package serverapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"

	"bmp/internal/musicstore"
)

type Client struct {
	base, user, password string
}

// New returns a client for the server at address, or nil when no server is
// set up.
func New(address, user, password string) *Client {
	address = strings.TrimRight(strings.TrimSpace(address), "/")
	if address == "" {
		return nil
	}
	if !strings.Contains(address, "://") {
		address = "https://" + address
	}
	return &Client{base: address, user: user, password: password}
}

// Address identifies the server and account, so BMP knows which one already
// has its plays.
func (client *Client) Address() string {
	return client.user + "@" + client.base
}

// Headers authorize a request, such as a song download BMP's music folder
// makes itself.
func (client *Client) Headers() map[string]string {
	login := base64.StdEncoding.EncodeToString([]byte(client.user + ":" + client.password))
	return map[string]string{"Authorization": "Basic " + login}
}

func (client *Client) SongURL(videoID string) string {
	return client.base + "/bmp/v1/songs/" + url.PathEscape(videoID)
}

// Songs returns the video IDs of the songs on the server with their file
// extensions.
func (client *Client) Songs(ctx context.Context) (map[string]string, error) {
	var songs map[string]string
	return songs, client.call(ctx, http.MethodGet, "/bmp/v1/songs", nil, &songs)
}

// Upload sends the song file at path, named on the server from title, artist
// and extension like in the music folder.
func (client *Client) Upload(ctx context.Context, videoID, title, artist, extension, path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	query := url.Values{"title": {title}, "artist": {artist}, "ext": {extension}}
	return client.call(ctx, http.MethodPut, "/bmp/v1/songs/"+url.PathEscape(videoID)+"?"+query.Encode(), file, nil)
}

func (client *Client) PutLibrary(ctx context.Context, library musicstore.Library) error {
	return client.sendJSON(ctx, http.MethodPut, "/bmp/v1/library", library, nil)
}

// AddPlays sends plays counted here and returns the server's totals.
func (client *Client) AddPlays(ctx context.Context, plays map[string]uint64) (map[string]uint64, error) {
	var totals map[string]uint64
	return totals, client.sendJSON(ctx, http.MethodPost, "/bmp/v1/plays", plays, &totals)
}

func (client *Client) Edits(ctx context.Context, playlistID string) (musicstore.Edits, error) {
	var edits musicstore.Edits
	return edits, client.call(ctx, http.MethodGet, "/bmp/v1/edits/"+url.PathEscape(playlistID), nil, &edits)
}

// PutEdits sends a playlist's edits; the server keeps newer ones it has.
func (client *Client) PutEdits(ctx context.Context, playlistID string, edits musicstore.Edits) error {
	err := client.sendJSON(ctx, http.MethodPut, "/bmp/v1/edits/"+url.PathEscape(playlistID), edits, nil)
	if statusError, ok := err.(httpStatusError); ok && int(statusError) == http.StatusConflict {
		return nil
	}
	return err
}

func (client *Client) sendJSON(ctx context.Context, method, path string, value, answer any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return client.call(ctx, method, path, bytes.NewReader(encoded), answer)
}

type httpStatusError int

func (status httpStatusError) Error() string {
	if status == http.StatusUnauthorized {
		return "BMP server: wrong user or password"
	}
	return fmt.Sprintf("BMP server: %d %s", int(status), http.StatusText(int(status)))
}

func (client *Client) call(ctx context.Context, method, path string, body io.Reader, answer any) error {
	request, err := http.NewRequestWithContext(ctx, method, client.base+path, body)
	if err != nil {
		return err
	}
	for name, value := range client.Headers() {
		request.Header.Set(name, value)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return fmt.Errorf("BMP server: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return httpStatusError(response.StatusCode)
	}
	if answer == nil {
		return nil
	}
	return json.NewDecoder(io.LimitReader(response.Body, 64<<20)).Decode(answer)
}
