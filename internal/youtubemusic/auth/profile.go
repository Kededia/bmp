package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/coder/websocket"
)

// ProfileBrowser is the browser setting that signs in through BMP's own
// browser profile instead of reading an existing browser's cookies. Chrome
// encrypts its cookies so only Chrome can read them, but a browser hands its
// cookies to whoever controls it through its DevTools port, which Chrome
// allows on a profile other than the everyday one.
const ProfileBrowser = "bmp"

const profileStartURL = "https://music.youtube.com/"

var ErrNotSignedIn = errors.New("not signed in to YouTube Music in BMP's browser window; sign in from setup (Ctrl+P)")

// profileBrowser finds Chrome, else Microsoft Edge, which every Windows PC
// has, and the profile BMP keeps for it.
func profileBrowser() (executable, profile string, err error) {
	var candidates []string
	for _, root := range []string{os.Getenv("ProgramFiles"), os.Getenv("ProgramFiles(x86)"), os.Getenv("LOCALAPPDATA")} {
		if root != "" {
			candidates = append(candidates, filepath.Join(root, "Google", "Chrome", "Application", "chrome.exe"))
		}
	}
	for _, root := range []string{os.Getenv("ProgramFiles(x86)"), os.Getenv("ProgramFiles")} {
		if root != "" {
			candidates = append(candidates, filepath.Join(root, "Microsoft", "Edge", "Application", "msedge.exe"))
		}
	}
	for _, candidate := range candidates {
		if _, err := os.Stat(candidate); err != nil {
			continue
		}
		cache, err := os.UserCacheDir()
		if err != nil {
			return "", "", fmt.Errorf("locate local AppData: %w", err)
		}
		name := strings.TrimSuffix(filepath.Base(candidate), ".exe")
		return candidate, filepath.Join(cache, "BMP", "signin-"+name), nil
	}
	return "", "", errors.New("neither Google Chrome nor Microsoft Edge is installed")
}

// devtools is a connection to a browser's DevTools port.
type devtools struct {
	command *exec.Cmd
	exited  chan struct{}
	conn    *websocket.Conn
	lastID  int
}

// openProfile starts a hidden browser on BMP's profile with its DevTools port
// open, and connects to it.
func openProfile(ctx context.Context) (*devtools, error) {
	executable, profile, err := profileBrowser()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(profile, 0o700); err != nil {
		return nil, fmt.Errorf("create browser profile: %w", err)
	}
	// The browser writes its DevTools port here once it listens.
	portFile := filepath.Join(profile, "DevToolsActivePort")
	_ = os.Remove(portFile)
	command := exec.Command(executable, "--user-data-dir="+profile, "--remote-debugging-port=0",
		"--no-first-run", "--no-default-browser-check", "--disable-background-mode", "--headless=new", "about:blank")
	if err := command.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", filepath.Base(executable), err)
	}
	exited := make(chan struct{})
	go func() {
		_ = command.Wait()
		close(exited)
	}()
	fail := func(err error) (*devtools, error) {
		_ = command.Process.Kill()
		return nil, err
	}
	deadline := time.After(20 * time.Second)
	for {
		if data, err := os.ReadFile(portFile); err == nil {
			if port, path, found := strings.Cut(strings.TrimSpace(string(data)), "\n"); found {
				conn, _, err := websocket.Dial(ctx, "ws://127.0.0.1:"+strings.TrimSpace(port)+strings.TrimSpace(path), nil)
				if err != nil {
					return fail(fmt.Errorf("connect to the browser: %w", err))
				}
				conn.SetReadLimit(16 << 20)
				return &devtools{command: command, exited: exited, conn: conn}, nil
			}
		}
		select {
		case <-ctx.Done():
			return fail(ctx.Err())
		case <-exited:
			return nil, errors.New("the browser closed at once; close any window BMP opened for sign-in and try again")
		case <-deadline:
			return fail(errors.New("the browser did not start in time"))
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func (tools *devtools) call(ctx context.Context, method string, result any) error {
	tools.lastID++
	request, err := json.Marshal(map[string]any{"id": tools.lastID, "method": method})
	if err != nil {
		return err
	}
	if err := tools.conn.Write(ctx, websocket.MessageText, request); err != nil {
		return fmt.Errorf("browser %s: %w", method, err)
	}
	for {
		_, data, err := tools.conn.Read(ctx)
		if err != nil {
			return fmt.Errorf("browser %s: %w", method, err)
		}
		var response struct {
			ID     int             `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(data, &response); err != nil || response.ID != tools.lastID {
			continue
		}
		if response.Error != nil {
			return fmt.Errorf("browser %s: %s", method, response.Error.Message)
		}
		if result == nil {
			return nil
		}
		return json.Unmarshal(response.Result, result)
	}
}

// close shuts the browser down, by request and then by force.
func (tools *devtools) close() {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = tools.call(ctx, "Browser.close", nil)
	_ = tools.conn.CloseNow()
	select {
	case <-tools.exited:
	case <-ctx.Done():
		_ = tools.command.Process.Kill()
	}
}

// cookies returns the browser's YouTube and Google cookies in Netscape
// format, and whether they sign in to YouTube Music.
func (tools *devtools) cookies(ctx context.Context) (string, bool, error) {
	var result struct {
		Cookies []struct {
			Name    string  `json:"name"`
			Value   string  `json:"value"`
			Domain  string  `json:"domain"`
			Path    string  `json:"path"`
			Expires float64 `json:"expires"`
			Secure  bool    `json:"secure"`
		} `json:"cookies"`
	}
	if err := tools.call(ctx, "Storage.getCookies", &result); err != nil {
		return "", false, err
	}
	var lines []string
	for _, cookie := range result.Cookies {
		domain := strings.TrimPrefix(cookie.Domain, ".")
		if domain != "youtube.com" && !strings.HasSuffix(domain, ".youtube.com") && domain != "google.com" && !strings.HasSuffix(domain, ".google.com") {
			continue
		}
		subdomains, secure := "FALSE", "FALSE"
		if strings.HasPrefix(cookie.Domain, ".") {
			subdomains = "TRUE"
		}
		if cookie.Secure {
			secure = "TRUE"
		}
		lines = append(lines, strings.Join([]string{
			cookie.Domain, subdomains, cookie.Path, secure, fmt.Sprint(max(0, int64(cookie.Expires))), cookie.Name, cookie.Value,
		}, "\t"))
	}
	content := "# Netscape HTTP Cookie File\n" + strings.Join(lines, "\n") + "\n"
	signedIn := false
	if session, err := NewSession(content); err == nil {
		_, err = session.Authorization(time.Now())
		signedIn = err == nil
	}
	return content, signedIn, nil
}

// CookiesFromProfile reads the cookies of BMP's browser profile from a hidden
// browser.
func CookiesFromProfile(ctx context.Context) (string, error) {
	tools, err := openProfile(ctx)
	if err != nil {
		return "", err
	}
	defer tools.close()
	content, signedIn, err := tools.cookies(ctx)
	if err != nil {
		return "", err
	}
	if !signedIn {
		return "", ErrNotSignedIn
	}
	return content, nil
}

// SignIn opens a browser window on BMP's profile at YouTube Music and returns
// once it is closed. It is a plain window: Google refuses to sign in to a
// browser whose DevTools port is open.
func SignIn(ctx context.Context) error {
	executable, profile, err := profileBrowser()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(profile, 0o700); err != nil {
		return fmt.Errorf("create browser profile: %w", err)
	}
	command := exec.CommandContext(ctx, executable, "--user-data-dir="+profile,
		"--no-first-run", "--no-default-browser-check", "--disable-background-mode", "--new-window", profileStartURL)
	if err := command.Run(); err != nil && ctx.Err() == nil {
		return fmt.Errorf("run %s: %w", filepath.Base(executable), err)
	}
	return ctx.Err()
}
