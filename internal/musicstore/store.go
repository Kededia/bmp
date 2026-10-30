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
	pendingBucket    = []byte("pending-plays")
	libraryBucket    = []byte("library")
	syncBucket       = []byte("server-sync")
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
	return Open(filepath.Join(filepath.Dir(settingsPath), name))
}

// Open opens the database at path, as the server does for each user.
func Open(path string) (*Store, error) {
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
// The play also waits among the pending ones until the server has it.
func (store *Store) Increment(trackID string) (uint64, error) {
	var count uint64
	err := store.db.Update(func(tx *bolt.Tx) error {
		if _, err := addCount(tx, pendingBucket, trackID, 1); err != nil {
			return err
		}
		var err error
		count, err = addCount(tx, playCountsBucket, trackID, 1)
		return err
	})
	if err != nil {
		return 0, fmt.Errorf("increment play count: %w", err)
	}
	return count, nil
}

// AddPlays adds plays counted elsewhere and returns every total.
func (store *Store) AddPlays(plays map[string]uint64) (map[string]uint64, error) {
	err := store.db.Update(func(tx *bolt.Tx) error {
		for trackID, count := range plays {
			if _, err := addCount(tx, playCountsBucket, trackID, count); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("add play counts: %w", err)
	}
	return store.AllCounts()
}

// AllCounts returns every stored total.
func (store *Store) AllCounts() (map[string]uint64, error) {
	return store.readCounts(playCountsBucket)
}

// PendingPlays returns the plays the server does not have yet.
func (store *Store) PendingPlays() (map[string]uint64, error) {
	return store.readCounts(pendingBucket)
}

// SyncPlays records that the server took sent, the pending plays read
// before, and replaces the totals with the server's.
func (store *Store) SyncPlays(sent, totals map[string]uint64) error {
	err := store.db.Update(func(tx *bolt.Tx) error {
		if bucket := tx.Bucket(pendingBucket); bucket != nil {
			for trackID, count := range sent {
				// Plays made while sending stay pending.
				left := uint64(0)
				if value := bucket.Get([]byte(trackID)); len(value) == 8 {
					left = binary.BigEndian.Uint64(value) - min(count, binary.BigEndian.Uint64(value))
				}
				if err := putCount(bucket, trackID, left); err != nil {
					return err
				}
			}
		}
		bucket, err := tx.CreateBucketIfNotExists(playCountsBucket)
		if err != nil {
			return err
		}
		for trackID, count := range totals {
			if err := putCount(bucket, trackID, count); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("save synced play counts: %w", err)
	}
	return nil
}

// SyncedServer returns the server whose totals hold this database's plays,
// or "" before the first sync.
func (store *Store) SyncedServer() (string, error) {
	var server string
	err := store.db.View(func(tx *bolt.Tx) error {
		if bucket := tx.Bucket(syncBucket); bucket != nil {
			server = string(bucket.Get([]byte("server")))
		}
		return nil
	})
	return server, err
}

func (store *Store) SetSyncedServer(server string) error {
	return store.db.Update(func(tx *bolt.Tx) error {
		bucket, err := tx.CreateBucketIfNotExists(syncBucket)
		if err != nil {
			return err
		}
		return bucket.Put([]byte("server"), []byte(server))
	})
}

func (store *Store) readCounts(name []byte) (map[string]uint64, error) {
	counts := make(map[string]uint64)
	err := store.db.View(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(name)
		if bucket == nil {
			return nil
		}
		return bucket.ForEach(func(key, value []byte) error {
			if len(value) == 8 && binary.BigEndian.Uint64(value) > 0 {
				counts[string(key)] = binary.BigEndian.Uint64(value)
			}
			return nil
		})
	})
	if err != nil {
		return nil, fmt.Errorf("read play counts: %w", err)
	}
	return counts, nil
}

func addCount(tx *bolt.Tx, name []byte, trackID string, count uint64) (uint64, error) {
	bucket, err := tx.CreateBucketIfNotExists(name)
	if err != nil {
		return 0, err
	}
	if value := bucket.Get([]byte(trackID)); len(value) == 8 {
		count += binary.BigEndian.Uint64(value)
	}
	return count, putCount(bucket, trackID, count)
}

func putCount(bucket *bolt.Bucket, trackID string, count uint64) error {
	if count == 0 {
		return bucket.Delete([]byte(trackID))
	}
	encoded := make([]byte, 8)
	binary.BigEndian.PutUint64(encoded, count)
	return bucket.Put([]byte(trackID), encoded)
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
	// Updated is when they last changed, so the newest copy wins between
	// BMP and the server.
	Updated time.Time `json:"updated,omitzero"`
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

// Library is the snapshot of a user's playlists that BMP sends the server.
type Library struct {
	Updated   time.Time               `json:"updated"`
	Playlists []youtubemusic.Playlist `json:"playlists"`
}

func (store *Store) Library() (Library, error) {
	var library Library
	err := store.db.View(func(tx *bolt.Tx) error {
		if bucket := tx.Bucket(libraryBucket); bucket != nil {
			if value := bucket.Get([]byte("library")); value != nil {
				return json.Unmarshal(value, &library)
			}
		}
		return nil
	})
	if err != nil {
		return Library{}, fmt.Errorf("read library: %w", err)
	}
	return library, nil
}

func (store *Store) SaveLibrary(library Library) error {
	data, err := json.Marshal(library)
	if err != nil {
		return err
	}
	err = store.db.Update(func(tx *bolt.Tx) error {
		bucket, err := tx.CreateBucketIfNotExists(libraryBucket)
		if err != nil {
			return err
		}
		return bucket.Put([]byte("library"), data)
	})
	if err != nil {
		return fmt.Errorf("save library: %w", err)
	}
	return nil
}

func (store *Store) Close() error {
	return store.db.Close()
}
