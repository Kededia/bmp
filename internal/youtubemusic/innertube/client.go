package innertube

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"bmp/internal/youtubemusic/auth"
)

const (
	baseURL            = "https://music.youtube.com"
	fallbackAPIKey     = "AIzaSyC9XL3ZjWddXya6X74dJoCTL-WEYFDNX30"
	fallbackClientName = "WEB_REMIX"
	maxResponseBytes   = 16 << 20
)

var configPatterns = map[string]*regexp.Regexp{
	"apiKey":        regexp.MustCompile(`"INNERTUBE_API_KEY"\s*:\s*"([^"]+)"`),
	"clientVersion": regexp.MustCompile(`"INNERTUBE_CLIENT_VERSION"\s*:\s*"([^"]+)"`),
	"visitorData":   regexp.MustCompile(`"VISITOR_DATA"\s*:\s*"([^"]+)"`),
}

// HTTPError is returned when InnerTube responds outside the 2xx range.
type HTTPError struct {
	StatusCode int
	Status     string
	Endpoint   string
	RetryAfter time.Duration
}

func (err *HTTPError) Error() string {
	return fmt.Sprintf("YouTube Music %s: %s", err.Endpoint, err.Status)
}

// Client performs authenticated requests to the YouTube Music InnerTube API.
type Client struct {
	session    *auth.Session
	httpClient *http.Client

	mu            sync.Mutex
	bootstrapped  bool
	apiKey        string
	clientName    string
	clientVersion string
	visitorData   string
	authUser      string
	pageID        string
}

func New(session *auth.Session, authUser, pageID string) *Client {
	return &Client{
		session: session, httpClient: &http.Client{Timeout: 15 * time.Second},
		authUser: authUser, pageID: strings.TrimSpace(pageID),
	}
}

func (client *Client) ensureBootstrap(ctx context.Context) error {
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.bootstrapped {
		return nil
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/", nil)
	if err != nil {
		return err
	}
	// The visitor-data bootstrap is an ordinary page request. Sending an
	// InnerTube Authorization header here causes music.youtube.com to reject it.
	request.Header.Set("Cookie", client.session.Header())
	request.Header.Set("Origin", auth.Origin)
	request.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/131 Safari/537.36")
	response, err := client.httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("bootstrap YouTube Music: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return newHTTPError(response, "bootstrap")
	}
	body, err := readLimited(response.Body, 4<<20)
	if err != nil {
		return fmt.Errorf("read YouTube Music bootstrap: %w", err)
	}
	values := make(map[string]string)
	for name, pattern := range configPatterns {
		match := pattern.FindSubmatch(body)
		if len(match) == 2 {
			values[name] = string(match[1])
		}
	}
	client.apiKey = values["apiKey"]
	if client.apiKey == "" {
		client.apiKey = fallbackAPIKey
	}
	client.clientName = fallbackClientName
	client.clientVersion = values["clientVersion"]
	if client.clientVersion == "" {
		client.clientVersion = "1." + time.Now().UTC().Format("20060102") + ".01.00"
	}
	client.visitorData = values["visitorData"]
	client.bootstrapped = true
	return nil
}

// AccountMenu returns details about the selected authenticated account.
func (client *Client) AccountMenu(ctx context.Context) (map[string]any, error) {
	return client.call(ctx, "account/account_menu", map[string]any{})
}

// Browse invokes the browse endpoint. Continuation may be empty for an initial request.
func (client *Client) Browse(ctx context.Context, browseID, continuation string) (map[string]any, error) {
	body := make(map[string]any)
	if continuation != "" {
		body["continuation"] = continuation
	} else {
		body["browseId"] = browseID
	}
	return client.call(ctx, "browse", body)
}

// CreatePlaylist creates a private playlist in the signed-in account.
func (client *Client) CreatePlaylist(ctx context.Context, title string) (map[string]any, error) {
	return client.call(ctx, "playlist/create", map[string]any{"title": title, "description": "", "privacyStatus": "PRIVATE"})
}

// EditPlaylist applies actions, such as adding, removing or moving videos, to
// a playlist the signed-in account owns.
func (client *Client) EditPlaylist(ctx context.Context, playlistID string, actions []map[string]any) (map[string]any, error) {
	return client.call(ctx, "browse/edit_playlist", map[string]any{"playlistId": playlistID, "actions": actions})
}

// Search invokes the search endpoint. Params selects a result filter.
func (client *Client) Search(ctx context.Context, query, params string) (map[string]any, error) {
	return client.call(ctx, "search", map[string]any{"query": query, "params": params})
}

func (client *Client) call(ctx context.Context, endpoint string, body map[string]any) (map[string]any, error) {
	if client == nil || client.session == nil {
		return nil, errors.New("YouTube Music client is not configured")
	}
	if err := client.ensureBootstrap(ctx); err != nil {
		return nil, err
	}

	client.mu.Lock()
	apiKey := client.apiKey
	clientName := client.clientName
	clientVersion := client.clientVersion
	visitorData := client.visitorData
	authUser := client.authUser
	pageID := client.pageID
	client.mu.Unlock()
	contextClient := map[string]any{
		"clientName":    clientName,
		"clientVersion": clientVersion,
		"hl":            "en",
		"gl":            "US",
	}
	if visitorData != "" {
		contextClient["visitorData"] = visitorData
	}
	body["context"] = map[string]any{"client": contextClient, "user": map[string]any{"lockedSafetyMode": false}}
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("encode InnerTube request: %w", err)
	}
	endpointURL := baseURL + "/youtubei/v1/" + endpoint + "?" + url.Values{"key": {apiKey}, "prettyPrint": {"false"}}.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpointURL, bytes.NewReader(encoded))
	if err != nil {
		return nil, err
	}
	if err := client.session.Apply(request, time.Now()); err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Youtube-Client-Name", "67")
	request.Header.Set("X-Youtube-Client-Version", clientVersion)
	request.Header.Set("X-Goog-AuthUser", authUser)
	request.Header.Set("X-Youtube-Bootstrap-Logged-In", "true")
	if pageID != "" {
		request.Header.Set("X-Goog-PageId", pageID)
	}
	if visitorData != "" {
		request.Header.Set("X-Goog-Visitor-Id", visitorData)
	}
	request.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/131 Safari/537.36")
	response, err := client.httpClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("call YouTube Music %s: %w", endpoint, err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, newHTTPError(response, endpoint)
	}
	responseBody, err := readLimited(response.Body, maxResponseBytes)
	if err != nil {
		return nil, fmt.Errorf("read YouTube Music %s response: %w", endpoint, err)
	}
	var payload map[string]any
	if err := json.Unmarshal(responseBody, &payload); err != nil {
		return nil, fmt.Errorf("decode YouTube Music %s response: %w", endpoint, err)
	}
	return payload, nil
}

func readLimited(reader io.Reader, maximum int64) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(reader, maximum+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > maximum {
		return nil, fmt.Errorf("response exceeds %d bytes", maximum)
	}
	return body, nil
}

func newHTTPError(response *http.Response, endpoint string) *HTTPError {
	retryAfter := time.Duration(0)
	if value := strings.TrimSpace(response.Header.Get("Retry-After")); value != "" {
		if seconds, err := strconv.Atoi(value); err == nil && seconds >= 0 {
			retryAfter = time.Duration(seconds) * time.Second
		} else if retryAt, err := http.ParseTime(value); err == nil {
			retryAfter = max(0, time.Until(retryAt))
		}
	}
	return &HTTPError{StatusCode: response.StatusCode, Status: response.Status, Endpoint: endpoint, RetryAfter: retryAfter}
}
