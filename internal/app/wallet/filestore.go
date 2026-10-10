package wallet

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/gofrs/flock"
	"telesrv/internal/domain"
)

var ErrWalletStore = errors.New("wallet state storage unavailable")

// FileStore is single-writer, durable wallet storage independent of PostgreSQL.
// A process lock prevents two server instances from racing over the same files.
// Per-user state changes use fsync + atomic rename, including challenge use and
// wallet replacement in the same record. No private wallet keys are stored.
type FileStore struct {
	mu     sync.Mutex
	dir    string
	lock   *flock.Flock
	closed bool
}

type storedChallenge struct {
	Challenge   domain.WalletChallenge `json:"challenge"`
	Consumed    bool                   `json:"consumed"`
	RequestHash []byte                 `json:"request_hash,omitempty"`
}

type walletRecord struct {
	Version    int                        `json:"version"`
	Link       *domain.WalletLink         `json:"link,omitempty"`
	Challenges map[string]storedChallenge `json:"challenges"`
}

func NewFileStore(dir string) (*FileStore, error) {
	if dir == "" {
		return nil, ErrWalletStore
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	info, err := os.Stat(dir)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return nil, fmt.Errorf("wallet state directory must have mode 0700")
	}
	lock := flock.New(filepath.Join(dir, ".lock"), flock.SetPermissions(0600))
	ok, err := lock.TryLock()
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errors.New("wallet state is locked by another server")
	}
	return &FileStore{dir: dir, lock: lock}, nil
}

func (s *FileStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	return s.lock.Close()
}

func (s *FileStore) read(userID int64) (walletRecord, error) {
	empty := walletRecord{Version: 1, Challenges: map[string]storedChallenge{}}
	if s.closed || userID <= 0 {
		return empty, ErrWalletStore
	}
	f, err := os.Open(filepath.Join(s.dir, strconv.FormatInt(userID, 10)+".json"))
	if errors.Is(err, os.ErrNotExist) {
		return empty, nil
	}
	if err != nil {
		return empty, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 65537))
	if err != nil || len(data) > 65536 {
		return empty, ErrWalletStore
	}
	var record walletRecord
	if json.Unmarshal(data, &record) != nil || record.Version != 1 || len(record.Challenges) > 32 {
		return empty, ErrWalletStore
	}
	if record.Link != nil && (record.Link.UserID != userID || len(record.Link.PublicKey) != 32 || record.Link.Address == "") {
		return empty, ErrWalletStore
	}
	for key, c := range record.Challenges {
		if c.Challenge.UserID != userID || key != hex.EncodeToString(c.Challenge.AuthKeyID[:]) {
			return empty, ErrWalletStore
		}
	}
	if record.Challenges == nil {
		record.Challenges = map[string]storedChallenge{}
	}
	return record, nil
}

func (s *FileStore) write(userID int64, record walletRecord) error {
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	if len(data) > 65536 {
		return ErrWalletStore
	}
	f, err := os.CreateTemp(s.dir, ".wallet-tmp-")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if _, err = f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(name, filepath.Join(s.dir, strconv.FormatInt(userID, 10)+".json")); err != nil {
		return err
	}
	dir, err := os.Open(s.dir)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func (s *FileStore) PutWalletChallenge(ctx context.Context, c domain.WalletChallenge) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	record, err := s.read(c.UserID)
	if err != nil {
		return err
	}
	for key, old := range record.Challenges {
		if !c.IssuedAt.Before(old.Challenge.ExpiresAt) {
			delete(record.Challenges, key)
		}
	}
	key := hex.EncodeToString(c.AuthKeyID[:])
	if _, exists := record.Challenges[key]; !exists && len(record.Challenges) >= 32 {
		return ErrWalletStore
	}
	record.Challenges[key] = storedChallenge{Challenge: c}
	return s.write(c.UserID, record)
}

func (s *FileStore) WalletChallenge(ctx context.Context, userID int64, key [8]byte) (domain.WalletChallenge, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return domain.WalletChallenge{}, false, err
	}
	record, err := s.read(userID)
	if err != nil {
		return domain.WalletChallenge{}, false, err
	}
	c, ok := record.Challenges[hex.EncodeToString(key[:])]
	return c.Challenge, ok, nil
}

func (s *FileStore) CommitWalletLink(ctx context.Context, c domain.WalletChallenge, link domain.WalletLink, hash [32]byte, allowReplacement bool, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if c.UserID != link.UserID || len(link.PublicKey) != 32 || link.Address == "" {
		return domain.ErrWalletProofInvalid
	}
	record, err := s.read(c.UserID)
	if err != nil {
		return err
	}
	key := hex.EncodeToString(c.AuthKeyID[:])
	stored, found := record.Challenges[key]
	if !found || stored.Challenge.Payload != c.Payload || stored.Challenge.Domain != c.Domain || !now.Before(stored.Challenge.ExpiresAt) {
		return domain.ErrWalletProofInvalid
	}
	if stored.Consumed {
		if !bytes.Equal(stored.RequestHash, hash[:]) || record.Link == nil || !bytes.Equal(record.Link.PublicKey, link.PublicKey) {
			return domain.ErrWalletProofInvalid
		}
		return nil
	}
	if record.Link != nil && !allowReplacement && !bytes.Equal(record.Link.PublicKey, link.PublicKey) {
		return domain.ErrWalletPasswordRequired
	}
	stored.Consumed = true
	stored.RequestHash = bytes.Clone(hash[:])
	record.Challenges[key] = stored
	link.PublicKey = bytes.Clone(link.PublicKey)
	record.Link = &link
	return s.write(c.UserID, record)
}

func (s *FileStore) WalletLinks(ctx context.Context, ids []int64) ([]domain.WalletLink, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	result := make([]domain.WalletLink, 0, len(ids))
	seen := make(map[int64]bool)
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		r, err := s.read(id)
		if err != nil {
			return nil, err
		}
		if r.Link != nil {
			result = append(result, *r.Link)
		}
	}
	return result, nil
}
