// Package server shares BMP's music with other devices: BMP uploads the songs
// it downloads and its playlists, and phones play them through Subsonic apps.
package server

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"bmp/internal/musiccache"
	"bmp/internal/musicstore"
)

type Server struct {
	data     string
	users    []User
	failures failures

	mu        sync.Mutex
	libraries map[string]*library
}

// library is one user's music folder and database, under data/<user>.
type library struct {
	music *musiccache.Cache
	store *musicstore.Store
}

// New reads the users of data, the folder holding users.json and each
// user's music.
func New(data string) (*Server, error) {
	users, err := loadUsers(data)
	if err != nil {
		return nil, err
	}
	return &Server{data: data, users: users, libraries: make(map[string]*library)}, nil
}

func (server *Server) Users() int {
	return len(server.users)
}

func (server *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /bmp/v1/songs", server.bmp(server.listSongs))
	mux.HandleFunc("GET /bmp/v1/songs/{id}", server.bmp(server.getSong))
	mux.HandleFunc("PUT /bmp/v1/songs/{id}", server.bmp(server.putSong))
	mux.HandleFunc("PUT /bmp/v1/library", server.bmp(server.putLibrary))
	mux.HandleFunc("GET /bmp/v1/plays", server.bmp(server.getPlays))
	mux.HandleFunc("POST /bmp/v1/plays", server.bmp(server.addPlays))
	mux.HandleFunc("GET /bmp/v1/edits/{id}", server.bmp(server.getEdits))
	mux.HandleFunc("PUT /bmp/v1/edits/{id}", server.bmp(server.putEdits))
	mux.HandleFunc("/rest/{method}", server.subsonic)
	return mux
}

// open returns user's library, opening it on first use.
func (server *Server) open(user User) (*library, error) {
	server.mu.Lock()
	defer server.mu.Unlock()
	if opened := server.libraries[user.Name]; opened != nil {
		return opened, nil
	}
	folder := filepath.Join(server.data, user.Name)
	music, err := musiccache.New(filepath.Join(folder, "music"))
	if err != nil {
		return nil, err
	}
	store, err := musicstore.Open(filepath.Join(folder, "bmp.db"))
	if err != nil {
		return nil, err
	}
	opened := &library{music: music, store: store}
	server.libraries[user.Name] = opened
	return opened, nil
}

func (server *Server) Close() {
	server.mu.Lock()
	defer server.mu.Unlock()
	for _, opened := range server.libraries {
		_ = opened.store.Close()
	}
}

// bmp checks the BMP API's Basic login before handle runs with the user's
// library.
func (server *Server) bmp(handle func(http.ResponseWriter, *http.Request, *library)) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		from := address(request)
		if server.failures.blocked(from) {
			http.Error(writer, "too many failed logins", http.StatusTooManyRequests)
			return
		}
		name, password, _ := request.BasicAuth()
		user, ok := server.passwordUser(name, password)
		if !ok {
			server.failures.add(from)
			writer.Header().Set("WWW-Authenticate", `Basic realm="bmp"`)
			http.Error(writer, "wrong user or password", http.StatusUnauthorized)
			return
		}
		opened, err := server.open(user)
		if err != nil {
			serverError(writer, err)
			return
		}
		handle(writer, request, opened)
	}
}

func serverError(writer http.ResponseWriter, err error) {
	log.Print(err)
	http.Error(writer, "server error", http.StatusInternalServerError)
}

var videoID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,24}$`)

func writeJSON(writer http.ResponseWriter, value any) {
	writer.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(writer).Encode(value)
}

// listSongs answers the songs on the server, each video ID with its file
// extension, which BMP needs to name a song it downloads from here.
func (server *Server) listSongs(writer http.ResponseWriter, _ *http.Request, opened *library) {
	tracks, err := opened.music.Tracks()
	if err != nil {
		serverError(writer, err)
		return
	}
	songs := make(map[string]string, len(tracks))
	for _, track := range tracks {
		songs[track.VideoID] = strings.TrimPrefix(filepath.Ext(track.Path), ".")
	}
	writeJSON(writer, songs)
}

func (server *Server) getSong(writer http.ResponseWriter, request *http.Request, opened *library) {
	serveSong(writer, request, opened, request.PathValue("id"))
}

func serveSong(writer http.ResponseWriter, request *http.Request, opened *library, id string) bool {
	if !videoID.MatchString(id) {
		http.NotFound(writer, request)
		return false
	}
	path, found, err := opened.music.Lookup(id)
	if err != nil || !found {
		http.NotFound(writer, request)
		return false
	}
	file, err := os.Open(path)
	if err != nil {
		http.NotFound(writer, request)
		return false
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		serverError(writer, err)
		return false
	}
	writer.Header().Set("Content-Type", contentType(path))
	// ServeContent answers byte ranges, which players seek with.
	http.ServeContent(writer, request, "", info.ModTime(), file)
	return true
}

func contentType(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".webm":
		return "audio/webm"
	case ".opus", ".ogg":
		return "audio/ogg"
	case ".mp3":
		return "audio/mpeg"
	default:
		return "audio/mp4"
	}
}

// putSong saves an uploaded song, named from the title, artist and file
// extension BMP sends along.
func (server *Server) putSong(writer http.ResponseWriter, request *http.Request, opened *library) {
	id := request.PathValue("id")
	if !videoID.MatchString(id) {
		http.Error(writer, "bad video ID", http.StatusBadRequest)
		return
	}
	query := request.URL.Query()
	if _, err := opened.music.Put(id, query.Get("title"), query.Get("artist"), query.Get("ext"), request.Body); err != nil {
		serverError(writer, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (server *Server) putLibrary(writer http.ResponseWriter, request *http.Request, opened *library) {
	var snapshot musicstore.Library
	if err := json.NewDecoder(io.LimitReader(request.Body, 64<<20)).Decode(&snapshot); err != nil {
		http.Error(writer, "bad library", http.StatusBadRequest)
		return
	}
	snapshot.Updated = time.Now()
	if err := opened.store.SaveLibrary(snapshot); err != nil {
		serverError(writer, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (server *Server) getPlays(writer http.ResponseWriter, _ *http.Request, opened *library) {
	counts, err := opened.store.AllCounts()
	if err != nil {
		serverError(writer, err)
		return
	}
	writeJSON(writer, counts)
}

// addPlays adds the plays a client counted since it last sent them and
// answers every total.
func (server *Server) addPlays(writer http.ResponseWriter, request *http.Request, opened *library) {
	var plays map[string]uint64
	if err := json.NewDecoder(io.LimitReader(request.Body, 16<<20)).Decode(&plays); err != nil {
		http.Error(writer, "bad plays", http.StatusBadRequest)
		return
	}
	counts, err := opened.store.AddPlays(plays)
	if err != nil {
		serverError(writer, err)
		return
	}
	writeJSON(writer, counts)
}

func (server *Server) getEdits(writer http.ResponseWriter, request *http.Request, opened *library) {
	edits, err := opened.store.Edits(request.PathValue("id"))
	if err != nil {
		serverError(writer, err)
		return
	}
	writeJSON(writer, edits)
}

// putEdits keeps a playlist's edits unless the server already has newer ones.
func (server *Server) putEdits(writer http.ResponseWriter, request *http.Request, opened *library) {
	var edits musicstore.Edits
	if err := json.NewDecoder(io.LimitReader(request.Body, 16<<20)).Decode(&edits); err != nil {
		http.Error(writer, "bad edits", http.StatusBadRequest)
		return
	}
	id := request.PathValue("id")
	current, err := opened.store.Edits(id)
	if err != nil {
		serverError(writer, err)
		return
	}
	if current.Updated.After(edits.Updated) {
		writer.WriteHeader(http.StatusConflict)
		return
	}
	if err := opened.store.SaveEdits(id, edits); err != nil {
		serverError(writer, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}
