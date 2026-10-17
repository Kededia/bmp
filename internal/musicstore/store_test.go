package musicstore

import (
	"path/filepath"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"

	"bmp/internal/youtubemusic"
)

func TestDeletePlaylist(t *testing.T) {
	database, err := bolt.Open(filepath.Join(t.TempDir(), "music.db"), 0o600, &bolt.Options{Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	store := &Store{db: database}
	playlist := youtubemusic.Playlist{ID: "local-1", Local: true, Title: "Local"}
	if err := store.SavePlaylist(playlist); err != nil {
		t.Fatal(err)
	}
	if err := store.DeletePlaylist(playlist.ID); err != nil {
		t.Fatal(err)
	}
	playlists, err := store.Playlists()
	if err != nil {
		t.Fatal(err)
	}
	if len(playlists) != 0 {
		t.Fatalf("playlists after deletion = %v", playlists)
	}
}
