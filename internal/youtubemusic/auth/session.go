package auth

import (
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
)

const Origin = "https://music.youtube.com"

// Session stores the cookies used by InnerTube and yt-dlp. Cookie values must
// never be written to logs or included in errors.
type Session struct {
	cookies map[string]string
}

// NewSession accepts either a Cookie request-header value (name=value; ...) or
// the contents of a Netscape cookie export.
func NewSession(content string) (*Session, error) {
	cookies := parseCookies(content)
	if len(cookies) == 0 {
		return nil, errors.New("YouTube Music cookies are empty or invalid")
	}
	return &Session{cookies: cookies}, nil
}

func parseCookies(content string) map[string]string {
	cookies := make(map[string]string)
	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	netscape := false
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#HttpOnly_") {
			line = strings.TrimPrefix(line, "#HttpOnly_")
		} else if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) >= 7 {
			netscape = true
			domain := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(fields[0])), ".")
			if domain != "youtube.com" && !strings.HasSuffix(domain, ".youtube.com") {
				continue
			}
			name := strings.TrimSpace(fields[5])
			if name != "" {
				cookies[name] = strings.TrimSpace(fields[6])
			}
		}
	}
	if netscape {
		return cookies
	}
	for _, part := range strings.Split(strings.ReplaceAll(content, "\n", ";"), ";") {
		name, value, ok := strings.Cut(strings.TrimSpace(part), "=")
		if ok && strings.TrimSpace(name) != "" {
			cookies[strings.TrimSpace(name)] = strings.TrimSpace(value)
		}
	}
	return cookies
}

// Header returns the canonical Cookie request-header value.
func (session *Session) Header() string {
	if session == nil {
		return ""
	}
	names := make([]string, 0, len(session.cookies))
	for name := range session.cookies {
		names = append(names, name)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, name := range names {
		parts = append(parts, name+"="+session.cookies[name])
	}
	return strings.Join(parts, "; ")
}

// Authorization returns the SAPISIDHASH authorization used by authenticated
// YouTube web clients.
func (session *Session) Authorization(now time.Time) (string, error) {
	if session == nil {
		return "", errors.New("YouTube Music session is nil")
	}
	secret := session.cookies["__Secure-3PAPISID"]
	if secret == "" {
		secret = session.cookies["SAPISID"]
	}
	if secret == "" {
		return "", errors.New("cookies do not contain SAPISID or __Secure-3PAPISID")
	}
	timestamp := now.Unix()
	digest := sha1.Sum([]byte(fmt.Sprintf("%d %s %s", timestamp, secret, Origin)))
	return fmt.Sprintf("SAPISIDHASH %d_%s", timestamp, hex.EncodeToString(digest[:])), nil
}

// Apply adds authentication headers to a request.
func (session *Session) Apply(request *http.Request, now time.Time) error {
	authorization, err := session.Authorization(now)
	if err != nil {
		return err
	}
	request.Header.Set("Cookie", session.Header())
	request.Header.Set("Authorization", authorization)
	request.Header.Set("Origin", Origin)
	request.Header.Set("X-Origin", Origin)
	return nil
}

// Netscape renders the session in the format accepted by yt-dlp.
func (session *Session) Netscape() string {
	if session == nil {
		return ""
	}
	var output strings.Builder
	output.WriteString("# Netscape HTTP Cookie File\n")
	names := make([]string, 0, len(session.cookies))
	for name := range session.cookies {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		secure := "FALSE"
		if strings.HasPrefix(name, "__Secure-") || strings.HasPrefix(name, "__Host-") {
			secure = "TRUE"
		}
		fmt.Fprintf(&output, ".youtube.com\tTRUE\t/\t%s\t0\t%s\t%s\n", secure, name, session.cookies[name])
	}
	return output.String()
}
