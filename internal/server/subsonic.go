package server

import (
	"cmp"
	"context"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"bmp/internal/musiccache"
	"bmp/internal/musicstore"
	"bmp/internal/youtubemusic"
)

// The Subsonic API, as far as phone apps need it to list playlists, play,
// search and count plays. Songs are identified by their video ID, and so are
// their covers.

type response struct {
	XMLName       xml.Name `xml:"subsonic-response" json:"-"`
	Namespace     string   `xml:"xmlns,attr" json:"-"`
	Status        string   `xml:"status,attr" json:"status"`
	Version       string   `xml:"version,attr" json:"version"`
	Type          string   `xml:"type,attr" json:"type"`
	ServerVersion string   `xml:"serverVersion,attr" json:"serverVersion"`
	OpenSubsonic  bool     `xml:"openSubsonic,attr" json:"openSubsonic"`

	Error         *subsonicError `xml:"error,omitempty" json:"error,omitempty"`
	License       *license       `xml:"license,omitempty" json:"license,omitempty"`
	Extensions    []extension    `xml:"openSubsonicExtensions,omitempty" json:"openSubsonicExtensions,omitempty"`
	MusicFolders  *musicFolders  `xml:"musicFolders,omitempty" json:"musicFolders,omitempty"`
	User          *subsonicUser  `xml:"user,omitempty" json:"user,omitempty"`
	Playlists     *playlists     `xml:"playlists,omitempty" json:"playlists,omitempty"`
	Playlist      *playlist      `xml:"playlist,omitempty" json:"playlist,omitempty"`
	Song          *child         `xml:"song,omitempty" json:"song,omitempty"`
	SearchResult3 *songs         `xml:"searchResult3,omitempty" json:"searchResult3,omitempty"`
	RandomSongs   *songs         `xml:"randomSongs,omitempty" json:"randomSongs,omitempty"`
	ScanStatus    *scanStatus    `xml:"scanStatus,omitempty" json:"scanStatus,omitempty"`
	// Empty answers for the parts of a music library BMP does not have,
	// which apps still ask for when they start.
	Artists    *index `xml:"artists,omitempty" json:"artists,omitempty"`
	Indexes    *index `xml:"indexes,omitempty" json:"indexes,omitempty"`
	AlbumList  *empty `xml:"albumList,omitempty" json:"albumList,omitempty"`
	AlbumList2 *empty `xml:"albumList2,omitempty" json:"albumList2,omitempty"`
	Starred    *empty `xml:"starred,omitempty" json:"starred,omitempty"`
	Starred2   *empty `xml:"starred2,omitempty" json:"starred2,omitempty"`
	Genres     *empty `xml:"genres,omitempty" json:"genres,omitempty"`
}

type subsonicError struct {
	Code    int    `xml:"code,attr" json:"code"`
	Message string `xml:"message,attr" json:"message"`
}

type license struct {
	Valid bool `xml:"valid,attr" json:"valid"`
}

type extension struct {
	Name     string `xml:"name,attr" json:"name"`
	Versions []int  `xml:"versions" json:"versions"`
}

type musicFolders struct {
	Folders []musicFolder `xml:"musicFolder" json:"musicFolder"`
}

type musicFolder struct {
	ID   int    `xml:"id,attr" json:"id"`
	Name string `xml:"name,attr" json:"name"`
}

type subsonicUser struct {
	Name              string `xml:"username,attr" json:"username"`
	ScrobblingEnabled bool   `xml:"scrobblingEnabled,attr" json:"scrobblingEnabled"`
	StreamRole        bool   `xml:"streamRole,attr" json:"streamRole"`
	DownloadRole      bool   `xml:"downloadRole,attr" json:"downloadRole"`
	PlaylistRole      bool   `xml:"playlistRole,attr" json:"playlistRole"`
	CoverArtRole      bool   `xml:"coverArtRole,attr" json:"coverArtRole"`
	Folders           []int  `xml:"folder" json:"folder"`
}

type playlists struct {
	Playlists []playlist `xml:"playlist" json:"playlist"`
}

type playlist struct {
	ID        string  `xml:"id,attr" json:"id"`
	Name      string  `xml:"name,attr" json:"name"`
	Owner     string  `xml:"owner,attr" json:"owner"`
	Public    bool    `xml:"public,attr" json:"public"`
	SongCount int     `xml:"songCount,attr" json:"songCount"`
	Duration  int     `xml:"duration,attr" json:"duration"`
	Created   string  `xml:"created,attr" json:"created"`
	Changed   string  `xml:"changed,attr" json:"changed"`
	CoverArt  string  `xml:"coverArt,attr,omitempty" json:"coverArt,omitempty"`
	Entries   []child `xml:"entry" json:"entry,omitempty"`
}

type child struct {
	ID          string `xml:"id,attr" json:"id"`
	IsDir       bool   `xml:"isDir,attr" json:"isDir"`
	Title       string `xml:"title,attr" json:"title"`
	Artist      string `xml:"artist,attr,omitempty" json:"artist,omitempty"`
	CoverArt    string `xml:"coverArt,attr" json:"coverArt"`
	Size        int64  `xml:"size,attr" json:"size"`
	ContentType string `xml:"contentType,attr" json:"contentType"`
	Suffix      string `xml:"suffix,attr" json:"suffix"`
	Duration    int    `xml:"duration,attr,omitempty" json:"duration,omitempty"`
	Path        string `xml:"path,attr" json:"path"`
	PlayCount   uint64 `xml:"playCount,attr,omitempty" json:"playCount,omitempty"`
	Type        string `xml:"type,attr" json:"type"`
	IsVideo     bool   `xml:"isVideo,attr" json:"isVideo"`
	MediaType   string `xml:"-" json:"mediaType"`
}

type songs struct {
	Songs []child `xml:"song" json:"song"`
}

type scanStatus struct {
	Scanning bool `xml:"scanning,attr" json:"scanning"`
	Count    int  `xml:"count,attr" json:"count"`
}

type index struct {
	IgnoredArticles string `xml:"ignoredArticles,attr" json:"ignoredArticles"`
}

type empty struct{}

// Subsonic error codes.
const (
	errorGeneric       = 0
	errorMissing       = 10
	errorLogin         = 40
	errorConflicting   = 43
	errorKey           = 44
	errorNotFound      = 70
	subsonicAPIVersion = "1.16.1"
)

func (server *Server) subsonic(writer http.ResponseWriter, request *http.Request) {
	_ = request.ParseForm()
	form := request.Form
	write := func(answer response) {
		answer.Namespace = "http://subsonic.org/restapi"
		answer.Version, answer.Type, answer.ServerVersion, answer.OpenSubsonic = subsonicAPIVersion, "bmp", "1", true
		if answer.Status == "" {
			answer.Status = "ok"
		}
		if form.Get("f") == "json" {
			writer.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(writer).Encode(map[string]response{"subsonic-response": answer})
			return
		}
		writer.Header().Set("Content-Type", "text/xml; charset=utf-8")
		_, _ = io.WriteString(writer, xml.Header)
		_ = xml.NewEncoder(writer).Encode(answer)
	}
	fail := func(code int, message string) {
		write(response{Status: "failed", Error: &subsonicError{Code: code, Message: message}})
	}

	method := strings.TrimSuffix(request.PathValue("method"), ".view")
	switch method {
	case "ping", "getLicense", "getOpenSubsonicExtensions":
		// Apps ask these before logging in, to learn what the server is.
		switch method {
		case "getLicense":
			write(response{License: &license{Valid: true}})
		case "getOpenSubsonicExtensions":
			write(response{Extensions: []extension{{Name: "apiKeyAuthentication", Versions: []int{1}}}})
		default:
			if _, code, message := server.subsonicLogin(request); code >= 0 {
				fail(code, message)
				return
			}
			write(response{})
		}
		return
	}

	user, code, message := server.subsonicLogin(request)
	if code >= 0 {
		fail(code, message)
		return
	}
	opened, err := server.open(user)
	if err != nil {
		fail(errorGeneric, err.Error())
		return
	}
	switch method {
	case "getMusicFolders":
		write(response{MusicFolders: &musicFolders{Folders: []musicFolder{{ID: 1, Name: "Music"}}}})
	case "getUser":
		write(response{User: &subsonicUser{
			Name: user.Name, ScrobblingEnabled: true, StreamRole: true, DownloadRole: true,
			PlaylistRole: true, CoverArtRole: true, Folders: []int{1},
		}})
	case "getScanStatus":
		catalog, err := opened.catalog()
		if err != nil {
			fail(errorGeneric, err.Error())
			return
		}
		write(response{ScanStatus: &scanStatus{Count: len(catalog.files)}})
	case "getPlaylists":
		catalog, err := opened.catalog()
		if err != nil {
			fail(errorGeneric, err.Error())
			return
		}
		list := make([]playlist, 0, len(catalog.library.Playlists))
		for _, source := range catalog.library.Playlists {
			converted := catalog.playlist(source, user.Name)
			converted.Entries = nil
			list = append(list, converted)
		}
		write(response{Playlists: &playlists{Playlists: list}})
	case "getPlaylist":
		catalog, err := opened.catalog()
		if err != nil {
			fail(errorGeneric, err.Error())
			return
		}
		for _, source := range catalog.library.Playlists {
			if source.ID == form.Get("id") {
				converted := catalog.playlist(source, user.Name)
				write(response{Playlist: &converted})
				return
			}
		}
		fail(errorNotFound, "playlist not found")
	case "getSong":
		catalog, err := opened.catalog()
		if err != nil {
			fail(errorGeneric, err.Error())
			return
		}
		song, found := catalog.song(form.Get("id"))
		if !found {
			fail(errorNotFound, "song not found")
			return
		}
		write(response{Song: &song})
	case "search3", "search2":
		catalog, err := opened.catalog()
		if err != nil {
			fail(errorGeneric, err.Error())
			return
		}
		count, offset := number(form.Get("songCount"), 20), number(form.Get("songOffset"), 0)
		found := catalog.search(form.Get("query"))
		found = found[min(offset, len(found)):]
		write(response{SearchResult3: &songs{Songs: found[:min(count, len(found))]}})
	case "getRandomSongs":
		catalog, err := opened.catalog()
		if err != nil {
			fail(errorGeneric, err.Error())
			return
		}
		all := catalog.search("")
		rand.Shuffle(len(all), func(i, j int) { all[i], all[j] = all[j], all[i] })
		write(response{RandomSongs: &songs{Songs: all[:min(number(form.Get("size"), 10), len(all))]}})
	case "stream", "download":
		if !serveSong(writer, request, opened, form.Get("id")) {
			return
		}
	case "getCoverArt":
		server.serveCover(writer, request, opened, form.Get("id"))
	case "scrobble":
		// Apps send a scrobble when a song starts, marked as not a
		// submission, and another once it played long enough.
		if form.Get("submission") != "false" {
			plays := make(map[string]uint64)
			for _, id := range form["id"] {
				plays[id]++
			}
			if _, err := opened.store.AddPlays(plays); err != nil {
				fail(errorGeneric, err.Error())
				return
			}
		}
		write(response{})
	case "star", "unstar", "savePlayQueue", "getPlayQueue":
		write(response{})
	case "getArtists":
		write(response{Artists: &index{}})
	case "getIndexes":
		write(response{Indexes: &index{}})
	case "getAlbumList":
		write(response{AlbumList: &empty{}})
	case "getAlbumList2":
		write(response{AlbumList2: &empty{}})
	case "getStarred":
		write(response{Starred: &empty{}})
	case "getStarred2":
		write(response{Starred2: &empty{}})
	case "getGenres":
		write(response{Genres: &empty{}})
	default:
		fail(errorGeneric, method+" is not supported")
	}
}

// subsonicLogin checks the login a Subsonic request carries: an API key, a
// token and salt, or a password, plain or hex encoded. code is -1 when it
// succeeds.
func (server *Server) subsonicLogin(request *http.Request) (User, int, string) {
	form := request.Form
	from := address(request)
	if server.failures.blocked(from) {
		return User{}, errorLogin, "too many failed logins, try again later"
	}
	var user User
	var ok bool
	name := form.Get("u")
	switch {
	case form.Get("apiKey") != "":
		if name != "" {
			return User{}, errorConflicting, "use either an API key or a user name"
		}
		if user, ok = server.keyUser(form.Get("apiKey")); !ok {
			server.failures.add(from)
			return User{}, errorKey, "invalid API key"
		}
		return user, -1, ""
	case form.Get("t") != "" && form.Get("s") != "":
		user, ok = server.tokenUser(name, form.Get("t"), form.Get("s"))
	case form.Get("p") != "":
		password := form.Get("p")
		if encoded, found := strings.CutPrefix(password, "enc:"); found {
			decoded, err := hex.DecodeString(encoded)
			if err != nil {
				return User{}, errorLogin, "wrong user name or password"
			}
			password = string(decoded)
		}
		user, ok = server.passwordUser(name, password)
	default:
		return User{}, errorMissing, "log in with a user name and password, or an API key"
	}
	if !ok {
		server.failures.add(from)
		return User{}, errorLogin, "wrong user name or password"
	}
	return user, -1, ""
}

func number(value string, fallback int) int {
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < 0 {
		return fallback
	}
	return parsed
}

// catalog is what a request needs to describe a user's songs: the files on
// the server, the playlists BMP sent and the play counts.
type catalog struct {
	files   map[string]musiccache.LocalTrack
	tracks  map[string]youtubemusic.Track
	library musicstore.Library
	counts  map[string]uint64
}

func (opened *library) catalog() (catalog, error) {
	files, err := opened.music.Tracks()
	if err != nil {
		return catalog{}, err
	}
	snapshot, err := opened.store.Library()
	if err != nil {
		return catalog{}, err
	}
	counts, err := opened.store.AllCounts()
	if err != nil {
		return catalog{}, err
	}
	result := catalog{
		files: make(map[string]musiccache.LocalTrack, len(files)), tracks: make(map[string]youtubemusic.Track),
		library: snapshot, counts: counts,
	}
	for _, file := range files {
		result.files[file.VideoID] = file
	}
	for _, source := range snapshot.Playlists {
		for _, track := range source.Tracks {
			result.tracks[track.VideoID] = track
		}
	}
	return result, nil
}

// song describes id when its file is on the server, with the playlists'
// details where BMP sent them and the file name's otherwise.
func (catalog catalog) song(id string) (child, bool) {
	file, found := catalog.files[id]
	if !found {
		return child{}, false
	}
	song := child{
		ID: id, Title: file.Title, Artist: file.Artist, CoverArt: id, Path: filepath.Base(file.Path),
		ContentType: contentType(file.Path), Suffix: strings.TrimPrefix(filepath.Ext(file.Path), "."),
		PlayCount: catalog.counts[id], Type: "music", MediaType: "song",
	}
	if track, found := catalog.tracks[id]; found {
		song.Title, song.Duration = track.Title, int(track.Duration.Seconds())
		names := make([]string, 0, len(track.Artists))
		for _, artist := range track.Artists {
			names = append(names, artist.Name)
		}
		if len(names) > 0 {
			song.Artist = strings.Join(names, ", ")
		}
	}
	if info, err := os.Stat(file.Path); err == nil {
		song.Size = info.Size()
	}
	return song, true
}

// playlist converts a playlist BMP sent, keeping only the songs on the server.
func (catalog catalog) playlist(source youtubemusic.Playlist, owner string) playlist {
	stamp := catalog.library.Updated.UTC().Format(time.RFC3339)
	converted := playlist{ID: source.ID, Name: source.Title, Owner: owner, Created: stamp, Changed: stamp, Entries: []child{}}
	for _, track := range source.Tracks {
		song, found := catalog.song(track.VideoID)
		if !found {
			continue
		}
		converted.Entries = append(converted.Entries, song)
		converted.Duration += song.Duration
	}
	converted.SongCount = len(converted.Entries)
	if converted.SongCount > 0 {
		converted.CoverArt = converted.Entries[0].ID
	}
	return converted
}

// search returns the songs whose title or artist holds every word of query,
// or every song for an empty query, as apps ask to copy the whole library.
func (catalog catalog) search(query string) []child {
	words := strings.Fields(strings.ToLower(strings.Trim(query, `"`)))
	found := make([]child, 0)
	for id := range catalog.files {
		song, _ := catalog.song(id)
		text := strings.ToLower(song.Title + " " + song.Artist)
		matches := true
		for _, word := range words {
			if !strings.Contains(text, word) {
				matches = false
				break
			}
		}
		if matches {
			found = append(found, song)
		}
	}
	// Map order is random; a fixed order keeps apps' paging consistent.
	slices.SortFunc(found, func(a, b child) int {
		return cmp.Or(strings.Compare(a.Title, b.Title), strings.Compare(a.ID, b.ID))
	})
	return found
}

// serveCover answers a song's cover from the covers folder, the same one BMP
// keeps, downloading it from YouTube's image servers the first time.
func (server *Server) serveCover(writer http.ResponseWriter, request *http.Request, opened *library, id string) {
	if !videoID.MatchString(id) {
		http.NotFound(writer, request)
		return
	}
	folder := filepath.Join(opened.music.Directory(), "covers")
	for _, name := range []string{id + ".jpg", id + "-video.jpg"} {
		if data, err := os.ReadFile(filepath.Join(folder, name)); err == nil {
			writer.Header().Set("Content-Type", "image/jpeg")
			_, _ = writer.Write(data)
			return
		}
	}
	track := youtubemusic.Track{VideoID: id}
	if catalog, err := opened.catalog(); err == nil {
		if known, found := catalog.tracks[id]; found {
			track = known
		}
	}
	data, err := downloadImage(request.Context(), youtubemusic.CoverURL(track))
	if err != nil {
		http.NotFound(writer, request)
		return
	}
	name := id + ".jpg"
	if track.Thumbnail == "" {
		name = id + "-video.jpg"
	}
	if err := os.MkdirAll(folder, 0o755); err == nil {
		_ = os.WriteFile(filepath.Join(folder, name), data, 0o644)
	}
	writer.Header().Set("Content-Type", "image/jpeg")
	_, _ = writer.Write(data)
}

func downloadImage(parent context.Context, url string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(parent, 15*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download cover: %s", response.Status)
	}
	return io.ReadAll(io.LimitReader(response.Body, 4<<20))
}
