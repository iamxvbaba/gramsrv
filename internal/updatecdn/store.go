package updatecdn

import (
	"fmt"
	"os"
	"sync"
	"time"
)

// Store atomically reloads the catalog when the manifest changes. A broken new
// manifest is never mixed with the previous snapshot.
type Store struct {
	manifestPath string
	filesDir     string

	mu       sync.RWMutex
	catalog  *Catalog
	modTime  time.Time
	fileSize int64
}

func NewStore(manifestPath, filesDir string) (*Store, error) {
	store := &Store{manifestPath: manifestPath, filesDir: filesDir}
	if _, err := store.Snapshot(); err != nil {
		return nil, err
	}
	return store, nil
}

// emptyCatalog is served when no manifest has been published yet. The update
// service is an optional feature (auto-update notifications), and operators
// who never publish a manifest -- or haven't gotten to it on a fresh install
// -- shouldn't see telesrv-update crash-loop over a file that simply hasn't
// been created. A malformed *existing* manifest still fails closed via
// LoadCatalog below; only a genuinely absent file gets this fallback.
var emptyCatalog = &Catalog{files: map[string]fileRecord{}}

func (s *Store) Snapshot() (*Catalog, error) {
	info, err := os.Stat(s.manifestPath)
	if err != nil {
		if os.IsNotExist(err) {
			return s.useEmptyCatalogLocked()
		}
		return nil, fmt.Errorf("stat manifest: %w", err)
	}
	s.mu.RLock()
	if s.catalog != nil && info.ModTime().Equal(s.modTime) && info.Size() == s.fileSize {
		catalog := s.catalog
		s.mu.RUnlock()
		return catalog, nil
	}
	s.mu.RUnlock()

	s.mu.Lock()
	defer s.mu.Unlock()
	info, err = os.Stat(s.manifestPath)
	if err != nil {
		if os.IsNotExist(err) {
			return s.setEmptyCatalogLocked()
		}
		return nil, fmt.Errorf("stat manifest: %w", err)
	}
	if s.catalog != nil && info.ModTime().Equal(s.modTime) && info.Size() == s.fileSize {
		return s.catalog, nil
	}
	catalog, err := LoadCatalog(s.manifestPath, s.filesDir)
	if err != nil {
		return nil, err
	}
	s.catalog = catalog
	s.modTime = info.ModTime()
	s.fileSize = info.Size()
	return catalog, nil
}

// useEmptyCatalogLocked serves the cached empty catalog without re-acquiring
// the write lock when a prior call already established it.
func (s *Store) useEmptyCatalogLocked() (*Catalog, error) {
	s.mu.RLock()
	if s.catalog == emptyCatalog {
		catalog := s.catalog
		s.mu.RUnlock()
		return catalog, nil
	}
	s.mu.RUnlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.setEmptyCatalogLocked()
}

// setEmptyCatalogLocked installs the empty catalog under the write lock. Zero
// modTime/fileSize ensure a manifest that later appears on disk (any real
// mtime/size) is detected as a change and loaded on the next Snapshot call.
func (s *Store) setEmptyCatalogLocked() (*Catalog, error) {
	s.catalog = emptyCatalog
	s.modTime = time.Time{}
	s.fileSize = 0
	return s.catalog, nil
}