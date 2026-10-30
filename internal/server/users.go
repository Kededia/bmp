package server

import (
	"crypto/md5"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// User is one account of users.json. The password is generated, never one a
// person picked: Subsonic's token login needs it readable here, and a long
// random one keeps that login's weak MD5 out of reach of guessing.
type User struct {
	Name     string `json:"name"`
	Password string `json:"password"`
	// APIKey holds the SHA-256 of the key newer Subsonic apps log in with.
	APIKey string `json:"api_key_sha256"`
}

var userName = regexp.MustCompile(`^[a-z0-9_-]{1,32}$`)

// AddUser creates name in the users file of data and returns its password
// and API key, which are shown once.
func AddUser(data, name string) (password, apiKey string, err error) {
	if !userName.MatchString(name) {
		return "", "", errors.New("user names use lowercase letters, digits, - and _")
	}
	users, err := loadUsers(data)
	if err != nil {
		return "", "", err
	}
	for _, user := range users {
		if user.Name == name {
			return "", "", fmt.Errorf("user %s already exists", name)
		}
	}
	password, apiKey = randomText(24), randomText(32)
	users = append(users, User{Name: name, Password: password, APIKey: hashKey(apiKey)})
	encoded, err := json.MarshalIndent(users, "", "  ")
	if err != nil {
		return "", "", err
	}
	if err := os.MkdirAll(data, 0o700); err != nil {
		return "", "", err
	}
	if err := os.WriteFile(filepath.Join(data, "users.json"), encoded, 0o600); err != nil {
		return "", "", fmt.Errorf("save users: %w", err)
	}
	return password, apiKey, nil
}

func loadUsers(data string) ([]User, error) {
	encoded, err := os.ReadFile(filepath.Join(data, "users.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read users: %w", err)
	}
	var users []User
	if err := json.Unmarshal(encoded, &users); err != nil {
		return nil, fmt.Errorf("read users: %w", err)
	}
	return users, nil
}

// randomText returns length characters picked at random from letters and
// digits that are easy to read apart, about 5.8 bits each.
func randomText(length int) string {
	const alphabet = "abcdefghijkmnopqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	text := make([]byte, 0, length)
	random := make([]byte, 1)
	for len(text) < length {
		_, _ = rand.Read(random)
		// Bytes past the last whole round of the alphabet would favor its start.
		if int(random[0]) < 256-256%len(alphabet) {
			text = append(text, alphabet[int(random[0])%len(alphabet)])
		}
	}
	return string(text)
}

func hashKey(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

func same(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func (server *Server) user(name string) (User, bool) {
	for _, user := range server.users {
		if user.Name == name {
			return user, true
		}
	}
	return User{}, false
}

// passwordUser checks a name and password, as the BMP API and Subsonic's
// plain password login send them.
func (server *Server) passwordUser(name, password string) (User, bool) {
	user, found := server.user(name)
	return user, found && same(user.Password, password)
}

// tokenUser checks Subsonic's token login: the MD5 of the password and salt.
func (server *Server) tokenUser(name, token, salt string) (User, bool) {
	user, found := server.user(name)
	if !found {
		return User{}, false
	}
	sum := md5.Sum([]byte(user.Password + salt))
	return user, same(hex.EncodeToString(sum[:]), strings.ToLower(token))
}

func (server *Server) keyUser(key string) (User, bool) {
	hash := hashKey(key)
	for _, user := range server.users {
		if same(user.APIKey, hash) {
			return user, true
		}
	}
	return User{}, false
}

// Failed logins are counted per address; past maximumFailures within
// failureWindow, the address is refused until the window ends.
const (
	maximumFailures = 10
	failureWindow   = 15 * time.Minute
)

type failures struct {
	mu        sync.Mutex
	addresses map[string]*failure
}

type failure struct {
	count int
	first time.Time
}

func address(request *http.Request) string {
	host, _, err := net.SplitHostPort(request.RemoteAddr)
	if err != nil {
		return request.RemoteAddr
	}
	return host
}

func (failures *failures) blocked(address string) bool {
	failures.mu.Lock()
	defer failures.mu.Unlock()
	entry := failures.addresses[address]
	if entry == nil {
		return false
	}
	if time.Since(entry.first) > failureWindow {
		delete(failures.addresses, address)
		return false
	}
	return entry.count >= maximumFailures
}

func (failures *failures) add(address string) {
	failures.mu.Lock()
	defer failures.mu.Unlock()
	if failures.addresses == nil {
		failures.addresses = make(map[string]*failure)
	}
	entry := failures.addresses[address]
	if entry == nil || time.Since(entry.first) > failureWindow {
		entry = &failure{first: time.Now()}
		failures.addresses[address] = entry
	}
	entry.count++
}
