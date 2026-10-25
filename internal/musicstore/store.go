package musicstore

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"

	"bmp/internal/settings"
	"bmp/internal/youtubemusic"
)

var (
	playCountsBucket = []byte("play-counts")
	editsBucket      = []byte("playlist-edits")
)

type Store struct {
	db *bolt.DB
}

func New() (*Store, error) {
	settingsPath, err := settings.Path()
	if err != nil {
		return nil, err
	}
	name := "plays.db"
	if strings.HasPrefix(filepath.Base(settingsPath), ".") {
		name = ".bmp-plays.db"
	}
	path := filepath.Join(filepath.Dir(settingsPath), name)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create music database directory: %w", err)
	}
	db, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: time.Second})
	if err != nil {
		return nil, fmt.Errorf("open music database: %w", err)
	}
	return &Store{db: db}, nil
}

// Increment atomically adds one play for trackID and returns the new total.
func (store *Store) Increment(trackID string) (uint64, error) {
	var count uint64
	err := store.db.Update(func(tx *bolt.Tx) error {
		bucket, err := tx.CreateBucketIfNotExists(playCountsBucket)
		if err != nil {
			return err
		}
		if value := bucket.Get([]byte(trackID)); len(value) == 8 {
			count = binary.BigEndian.Uint64(value)
		}
		count++
		encoded := make([]byte, 8)
		binary.BigEndian.PutUint64(encoded, count)
		return bucket.Put([]byte(trackID), encoded)
	})
	if err != nil {
		return 0, fmt.Errorf("increment play count: %w", err)
	}
	return count, nil
}

// Counts returns the stored totals for the supplied track IDs. Unknown tracks
// are omitted from the result.
func (store *Store) Counts(trackIDs []string) (map[string]uint64, error) {
	counts := make(map[string]uint64)
	err := store.db.View(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(playCountsBucket)
		if bucket == nil {
			return nil
		}
		for _, trackID := range trackIDs {
			if value := bucket.Get([]byte(trackID)); len(value) == 8 {
				counts[trackID] = binary.BigEndian.Uint64(value)
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("read play counts: %w", err)
	}
	return counts, nil
}

// Edits are the changes made in BMP to a YouTube playlist, kept apart from
// it so songs added on YouTube still show up.
type Edits struct {
	// Order lists every song's video ID as last arranged in BMP.
	Order []string `json:"order"`
	// Added holds songs added in BMP, which YouTube does not list.
	Added []youtubemusic.Track `json:"added,omitempty"`
	// Removed holds video IDs hidden in BMP.
	Removed []string `json:"removed,omitempty"`
}

// Edits returns the stored edits of a playlist, empty when it has none.
func (store *Store) Edits(playlistID string) (Edits, error) {
	var edits Edits
	err := store.db.View(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(editsBucket)
		if bucket == nil {
			return nil
		}
		if value := bucket.Get([]byte(playlistID)); value != nil {
			return json.Unmarshal(value, &edits)
		}
		return nil
	})
	if err != nil {
		return Edits{}, fmt.Errorf("read playlist edits: %w", err)
	}
	return edits, nil
}

func (store *Store) SaveEdits(playlistID string, edits Edits) error {
	data, err := json.Marshal(edits)
	if err != nil {
		return err
	}
	err = store.db.Update(func(tx *bolt.Tx) error {
		bucket, err := tx.CreateBucketIfNotExists(editsBucket)
		if err != nil {
			return err
		}
		return bucket.Put([]byte(playlistID), data)
	})
	if err != nil {
		return fmt.Errorf("save playlist edits: %w", err)
	}
	return nil
}

func (store *Store) Close() error {
	return store.db.Close()
}
