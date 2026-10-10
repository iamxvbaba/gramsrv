package wallet

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"telesrv/internal/domain"
	"testing"
	"time"
)

type balanceProvider struct{ err error }

func (p *balanceProvider) Perform(context.Context, string, *string, *string) (string, error) {
	return `{"ok":true,"result":"1234567890"}`, p.err
}

func signChallenge(t *testing.T, c domain.WalletChallenge, seed byte) ([]byte, []byte) {
	t.Helper()
	private := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{seed}, 32))
	public := []byte(private.Public().(ed25519.PublicKey))
	addr, err := InitialAddress(public, false)
	if err != nil {
		t.Fatal(err)
	}
	hash, err := ProofHash(0, addr.Data(), c.Domain, uint64(c.IssuedAt.Unix()), c.Payload)
	if err != nil {
		t.Fatal(err)
	}
	return public, ed25519.Sign(private, hash[:])
}

func TestFileWalletLifecycle(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "wallet")
	store, err := NewFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { store.Close() }()
	provider := &balanceProvider{}
	svc := NewService(store, provider, "wallet.example")
	now := time.Now().UTC().Truncate(time.Second)
	svc.now = func() time.Time { return now }
	key := [8]byte{1}
	c, err := svc.GetProofChallenge(ctx, 42, key)
	if err != nil {
		t.Fatal(err)
	}
	public, sig := signChallenge(t, c, 1)
	replace := func(k [8]byte, pub, signature []byte, allow bool) error {
		_, _, err := svc.ReplaceImported(ctx, 42, k, pub, int(c.IssuedAt.Unix()), signature, allow)
		return err
	}
	if err := replace([8]byte{2}, public, sig, false); !errors.Is(err, domain.ErrWalletProofInvalid) {
		t.Fatalf("cross-session: %v", err)
	}
	bad := bytes.Clone(sig)
	bad[0] ^= 1
	if err := replace(key, public, bad, false); !errors.Is(err, domain.ErrWalletProofInvalid) {
		t.Fatalf("bad proof: %v", err)
	}
	provider.err = ErrProviderUnavailable
	if err := replace(key, public, sig, false); !errors.Is(err, ErrProviderUnavailable) {
		t.Fatalf("provider: %v", err)
	}
	links, err := svc.Links(ctx, []int64{42})
	if err != nil || len(links) != 0 {
		t.Fatalf("failed provider persisted link: %v %v", links, err)
	}
	provider.err = nil
	link, balance, err := svc.ReplaceImported(ctx, 42, key, public, int(c.IssuedAt.Unix()), sig, false)
	if err != nil || balance != 1234567890 || link.Address == "" {
		t.Fatalf("link: %+v %d %v", link, balance, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = NewFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	svc.store = store
	if err := replace(key, public, sig, false); err != nil {
		t.Fatalf("retry after restart: %v", err)
	}
	links, err = svc.Links(ctx, []int64{42, 42})
	if err != nil || len(links) != 1 || !bytes.Equal(links[0].PublicKey, public) {
		t.Fatalf("persisted links: %v %v", links, err)
	}
	oldSig := sig
	c, err = svc.GetProofChallenge(ctx, 42, key)
	if err != nil {
		t.Fatal(err)
	}
	if err := replace(key, public, oldSig, false); !errors.Is(err, domain.ErrWalletProofInvalid) {
		t.Fatalf("superseded challenge: %v", err)
	}
	public, sig = signChallenge(t, c, 2)
	if err := replace(key, public, sig, false); !errors.Is(err, domain.ErrWalletPasswordRequired) {
		t.Fatalf("replacement without password: %v", err)
	}
	if err := replace(key, public, sig, true); err != nil {
		t.Fatal(err)
	}
	now = c.ExpiresAt
	if err := replace(key, public, sig, true); !errors.Is(err, domain.ErrWalletProofInvalid) {
		t.Fatalf("expiry: %v", err)
	}
	info, err := os.Stat(filepath.Join(dir, "42.json"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("private file: %v %v", info, err)
	}
}

func TestFileWalletConcurrentClaims(t *testing.T) {
	store, err := NewFileStore(filepath.Join(t.TempDir(), "wallet"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	svc := NewService(store, &balanceProvider{}, "wallet.example")
	ctx := context.Background()
	var challenges [2]domain.WalletChallenge
	for i := range challenges {
		challenges[i], err = svc.GetProofChallenge(ctx, 42, [8]byte{byte(i + 1)})
		if err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i, c := range challenges {
		pub, sig := signChallenge(t, c, byte(i+1))
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, err := svc.ReplaceImported(ctx, 42, c.AuthKeyID, pub, int(c.IssuedAt.Unix()), sig, false)
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	success, denied := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else if errors.Is(err, domain.ErrWalletPasswordRequired) {
			denied++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || denied != 1 {
		t.Fatalf("success=%d denied=%d", success, denied)
	}
}

func TestFileWalletLockAndCorruption(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "wallet")
	store, err := NewFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	second, err := NewFileStore(dir)
	if err == nil {
		second.Close()
		t.Fatal("accepted second writer")
	}
	path := filepath.Join(dir, "42.json")
	bad := []byte(`{"version":1,"link":`)
	if err := os.WriteFile(path, bad, 0600); err != nil {
		t.Fatal(err)
	}
	svc := NewService(store, &balanceProvider{}, "wallet.example")
	if _, err := svc.GetProofChallenge(context.Background(), 42, [8]byte{1}); !errors.Is(err, ErrWalletStore) {
		t.Fatalf("corrupt state: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(data, bad) {
		t.Fatal("corrupt record overwritten")
	}
}
