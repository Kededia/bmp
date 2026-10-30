package app

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"bmp/internal/musiccache"
	"bmp/internal/musicstore"
	"bmp/internal/serverapi"
	"bmp/internal/youtubemusic"
)

// With a BMP server set up, BMP shares the music there: it uploads the songs
// of its music folder, sends its playlists and plays, and fills the folder
// with the songs of its playlists in the background so phones have them.

// likedSongsID is the Liked Songs playlist, which the background download
// leaves out: its songs reach the music folder only once played.
const likedSongsID = "LM"

// backgroundPause separates background downloads, keeping them slow.
const backgroundPause = 10 * time.Second

type serverSyncedMsg struct {
	songs   map[string]string
	library []youtubemusic.Playlist
	counts  map[string]uint64
	err     error
}

type serverReachedMsg struct {
	songs map[string]string
	err   error
}

type serverUploadedMsg struct {
	songs map[string]string
	err   error
}

type backgroundDownloadedMsg struct {
	generation int
	videoID    string
	err        error
}

type backgroundTickMsg struct{}

// syncServer exchanges plays with the server and, signed in to YouTube
// Music, sends it the library: every playlist read again, with its BMP edits
// applied.
func syncServer(parent context.Context, server *serverapi.Client, store *musicstore.Store, client *youtubemusic.Client, playlists []youtubemusic.Playlist) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(parent, 10*time.Minute)
		defer cancel()
		var msg serverSyncedMsg
		if store != nil {
			msg.counts, msg.err = syncPlays(ctx, server, store)
		}
		if client != nil {
			library := make([]youtubemusic.Playlist, 0, len(playlists))
			for _, listed := range playlists {
				if searchPlaylist(listed) {
					continue
				}
				playlist, err := client.GetPlaylist(ctx, listed.ID)
				if err != nil {
					continue
				}
				if store != nil && !playlist.Owned {
					if edits, err := store.Edits(playlist.ID); err == nil {
						playlist = applyEdits(playlist, edits)
					}
				}
				library = append(library, playlist)
			}
			if err := server.PutLibrary(ctx, musicstore.Library{Playlists: library}); err != nil {
				msg.err = err
			}
			msg.library = library
		}
		songs, err := server.Songs(ctx)
		if err != nil {
			msg.err = err
		}
		msg.songs = songs
		return msg
	}
}

// reachServer asks the server for its songs alone, so the server dot shows
// at once whether it answers, while the sync reads the playlists.
func reachServer(parent context.Context, server *serverapi.Client) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(parent, 15*time.Second)
		defer cancel()
		songs, err := server.Songs(ctx)
		return serverReachedMsg{songs: songs, err: err}
	}
}

// syncPlays sends the plays the server does not have and takes its totals.
// The first sync with a server sends every play counted so far.
func syncPlays(ctx context.Context, server *serverapi.Client, store *musicstore.Store) (map[string]uint64, error) {
	pending, err := store.PendingPlays()
	if err != nil {
		return nil, err
	}
	synced, err := store.SyncedServer()
	if err != nil {
		return nil, err
	}
	send := pending
	if synced != server.Address() {
		if send, err = store.AllCounts(); err != nil {
			return nil, err
		}
	}
	totals, err := server.AddPlays(ctx, send)
	if err != nil {
		return nil, err
	}
	if err := store.SyncPlays(pending, totals); err != nil {
		return nil, err
	}
	if synced != server.Address() {
		if err := store.SetSyncedServer(server.Address()); err != nil {
			return nil, err
		}
	}
	return totals, nil
}

// uploadSongs uploads, one at a time, the music folder's songs the server
// does not have. Songs still in the old 360p format wait for their upgrade.
func uploadSongs(parent context.Context, server *serverapi.Client, cache *musiccache.Cache) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(parent, time.Minute)
		songs, err := server.Songs(ctx)
		cancel()
		if err != nil {
			return serverUploadedMsg{err: err}
		}
		tracks, err := cache.Tracks()
		if err != nil {
			return serverUploadedMsg{songs: songs, err: err}
		}
		for _, track := range tracks {
			if _, found := songs[track.VideoID]; found || musiccache.Legacy(track.Path) {
				continue
			}
			extension := strings.TrimPrefix(filepath.Ext(track.Path), ".")
			ctx, cancel := context.WithTimeout(parent, 30*time.Minute)
			err := server.Upload(ctx, track.VideoID, track.Title, track.Artist, extension, track.Path)
			cancel()
			if err != nil {
				return serverUploadedMsg{songs: songs, err: err}
			}
			songs[track.VideoID] = extension
		}
		return serverUploadedMsg{songs: songs}
	}
}

// downloadInBackground stores one song in the music folder, like the queue's
// downloads but reporting only once it is done.
func downloadInBackground(ctx context.Context, generation int, client *youtubemusic.Client, cache *musiccache.Cache, server *serverapi.Client, serverExtension string, track youtubemusic.Track) tea.Cmd {
	return func() tea.Msg {
		events := make(chan youtubeMusicCacheEvent, 16)
		go downloadYouTubeMusicTrack(ctx, 0, client, cache, server, serverExtension, track, -1, events)
		var err error
		for event := range events {
			if event.phase == musicCacheFailed {
				err = event.err
			}
		}
		if err == nil {
			err = ctx.Err()
		}
		return backgroundDownloadedMsg{generation: generation, videoID: track.VideoID, err: err}
	}
}

func (m *Model) startServerSync() tea.Cmd {
	if m.server == nil || m.serverSyncing {
		return nil
	}
	m.serverSyncing = true
	var playlists []youtubemusic.Playlist
	if m.youtubeMusicClient != nil {
		playlists = slices.Clone(m.youtubeMusicPlaylists)
	}
	return tea.Batch(reachServer(m.appContext, m.server),
		syncServer(m.appContext, m.server, m.youtubeMusicStore, m.youtubeMusicClient, playlists))
}

// startUploads uploads the songs the server lacks, or once more after the
// uploads under way when they already run.
func (m *Model) startUploads() tea.Cmd {
	if m.server == nil || m.youtubeMusicCache == nil {
		return nil
	}
	if m.serverUploading {
		m.serverUploadAgain = true
		return nil
	}
	m.serverUploading, m.serverUploadAgain = true, false
	return uploadSongs(m.appContext, m.server, m.youtubeMusicCache)
}

// nextBackgroundDownload starts downloading, while the queue downloads
// nothing, the next song of the library missing from the music folder.
func (m *Model) nextBackgroundDownload() tea.Cmd {
	if m.server == nil || m.backgroundCancel != nil || m.youtubeMusicCacheCancel != nil || m.youtubeMusicPendingIndex >= 0 ||
		m.youtubeMusicClient == nil || m.youtubeMusicCache == nil {
		return nil
	}
	for _, playlist := range m.serverLibrary {
		if playlist.ID == likedSongsID {
			continue
		}
		for _, track := range playlist.Tracks {
			if !track.Available || track.VideoID == "" || m.backgroundSkip[track.VideoID] {
				continue
			}
			if _, found, err := m.youtubeMusicCache.Lookup(track.VideoID); err != nil || found {
				continue
			}
			ctx, cancel := context.WithCancel(m.appContext)
			m.backgroundCancel = cancel
			m.backgroundGeneration++
			return downloadInBackground(ctx, m.backgroundGeneration, m.youtubeMusicClient, m.youtubeMusicCache, m.server, m.serverSongs[track.VideoID], track)
		}
	}
	return nil
}

// cancelBackground stops the background download, making way for one the
// queue needs. The next pause in the queue's downloads resumes it.
func (m *Model) cancelBackground() {
	if m.backgroundCancel != nil {
		m.backgroundCancel()
		m.backgroundCancel = nil
	}
}

func (m *Model) handleServerSynced(msg serverSyncedMsg) tea.Cmd {
	m.serverSyncing = false
	m.serverErr = msg.err
	if msg.library != nil {
		m.serverLibrary = msg.library
	}
	if msg.songs != nil {
		m.serverSongs = msg.songs
	}
	for _, track := range m.youtubeMusicPlaylist.Tracks {
		if count, found := msg.counts[track.VideoID]; found {
			m.youtubeMusicPlayCounts[track.VideoID] = count
		}
	}
	return tea.Batch(m.startUploads(), m.nextBackgroundDownload())
}

func (m *Model) handleServerReached(msg serverReachedMsg) {
	m.serverErr = msg.err
	if msg.songs != nil && m.serverSongs == nil {
		m.serverSongs = msg.songs
	}
}

func (m *Model) handleServerUploaded(msg serverUploadedMsg) tea.Cmd {
	m.serverUploading = false
	if msg.songs != nil {
		m.serverSongs = msg.songs
	}
	if msg.err != nil || m.serverErr == nil {
		m.serverErr = msg.err
	}
	if m.serverUploadAgain && msg.err == nil {
		return m.startUploads()
	}
	return nil
}

func (m *Model) handleBackgroundDownloaded(msg backgroundDownloadedMsg) tea.Cmd {
	if msg.generation == m.backgroundGeneration {
		m.backgroundCancel = nil
	}
	if errors.Is(msg.err, context.Canceled) {
		return nil
	}
	if msg.err != nil {
		if m.backgroundSkip == nil {
			m.backgroundSkip = make(map[string]bool)
		}
		m.backgroundSkip[msg.videoID] = true
	}
	return tea.Batch(m.startUploads(), tea.Tick(backgroundPause, func(time.Time) tea.Msg { return backgroundTickMsg{} }))
}

// shareEdits sends a playlist's edits to the server; when it cannot, loading
// the playlist later sends them again, as they are then newer.
func shareEdits(parent context.Context, server *serverapi.Client, playlistID string, edits musicstore.Edits) {
	if server == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(parent, time.Minute)
		defer cancel()
		_ = server.PutEdits(ctx, playlistID, edits)
	}()
}

// newestEdits picks between a playlist's edits kept here and those on the
// server, saving the server's here when they are newer and sending these
// there when they are.
func (m *Model) newestEdits(playlistID string, local musicstore.Edits, shared *musicstore.Edits) musicstore.Edits {
	switch {
	case shared == nil:
		return local
	case local.Updated.IsZero() && shared.Updated.IsZero() && len(local.Order) > 0:
		// Edits saved before BMP shared them get a date to share them with.
		local.Updated = time.Now()
		if m.youtubeMusicStore != nil {
			m.youtubeMusicStoreErr = m.youtubeMusicStore.SaveEdits(playlistID, local)
		}
		shareEdits(m.appContext, m.server, playlistID, local)
	case shared.Updated.After(local.Updated):
		if m.youtubeMusicStore != nil {
			m.youtubeMusicStoreErr = m.youtubeMusicStore.SaveEdits(playlistID, *shared)
		}
		return *shared
	case local.Updated.After(shared.Updated):
		shareEdits(m.appContext, m.server, playlistID, local)
	}
	return local
}
