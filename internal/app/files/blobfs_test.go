package files

import (
	"bytes"
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestLocalFSPutGetRoundTrip(t *testing.T) {
	fs, err := NewLocalFS(t.TempDir())
	if err != nil {
		t.Fatalf("new local fs: %v", err)
	}
	ctx := context.Background()
	data := []byte("hello telesrv media blob 你好")

	key, err := fs.Put(ctx, data)
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	if key == "" {
		t.Fatal("empty object key")
	}

	// Content-addressed: identical content should get the same key (dedup).
	key2, err := fs.Put(ctx, data)
	if err != nil {
		t.Fatalf("put again: %v", err)
	}
	if key != key2 {
		t.Fatalf("expected dedup key %q == %q", key, key2)
	}

	got, err := fs.Get(ctx, key)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("roundtrip mismatch: got %q want %q", got, data)
	}
}

func TestLocalFSPutReaderRoundTrip(t *testing.T) {
	fs, err := NewLocalFS(t.TempDir())
	if err != nil {
		t.Fatalf("new local fs: %v", err)
	}
	ctx := context.Background()
	data := strings.Repeat("streamed-", 1024)

	key, size, sum, err := fs.PutReader(ctx, strings.NewReader(data))
	if err != nil {
		t.Fatalf("put reader: %v", err)
	}
	if key == "" || size != int64(len(data)) || len(sum) != 32 {
		t.Fatalf("stream metadata key=%q size=%d sha=%d", key, size, len(sum))
	}
	got, err := fs.Get(ctx, key)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if string(got) != data {
		t.Fatalf("roundtrip mismatch")
	}
}

func TestLocalFSDeleteExpiredUploadParts(t *testing.T) {
	fs, err := NewLocalFS(t.TempDir())
	if err != nil {
		t.Fatalf("new local fs: %v", err)
	}
	ctx := context.Background()
	oldPart, err := fs.PutUploadPart(ctx, 10, 100, 0, []byte("old"))
	if err != nil {
		t.Fatalf("put old upload part: %v", err)
	}
	freshPart, err := fs.PutUploadPart(ctx, 10, 100, 1, []byte("fresh"))
	if err != nil {
		t.Fatalf("put fresh upload part: %v", err)
	}
	oldPath, err := fs.uploadPartPath(oldPart.ObjectKey)
	if err != nil {
		t.Fatalf("old upload part path: %v", err)
	}
	oldTime := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(oldPath, oldTime, oldTime); err != nil {
		t.Fatalf("age old upload part: %v", err)
	}

	deleted, err := fs.DeleteExpiredUploadParts(ctx, time.Now().Add(-24*time.Hour), 10)
	if err != nil {
		t.Fatalf("delete expired upload parts: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("deleted = %d, want 1", deleted)
	}
	if _, err := fs.GetUploadPart(ctx, oldPart.ObjectKey); err == nil {
		t.Fatalf("old upload part still exists")
	}
	if data, err := fs.GetUploadPart(ctx, freshPart.ObjectKey); err != nil || string(data) != "fresh" {
		t.Fatalf("fresh upload part = %q err=%v", data, err)
	}
}

func TestLocalFSDistinctContent(t *testing.T) {
	fs, err := NewLocalFS(t.TempDir())
	if err != nil {
		t.Fatalf("new local fs: %v", err)
	}
	ctx := context.Background()
	k1, _ := fs.Put(ctx, []byte("aaa"))
	k2, _ := fs.Put(ctx, []byte("bbb"))
	if k1 == k2 {
		t.Fatal("distinct content must yield distinct keys")
	}
}

func TestLocalFSGetRange(t *testing.T) {
	fs, err := NewLocalFS(t.TempDir())
	if err != nil {
		t.Fatalf("new local fs: %v", err)
	}
	ctx := context.Background()
	key, err := fs.Put(ctx, []byte("0123456789"))
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	cases := []struct {
		name          string
		offset, limit int64
		want          string
	}{
		{"head", 0, 4, "0123"},
		{"middle", 3, 4, "3456"},
		{"limit-exceeds-remaining", 7, 100, "789"},
		{"zero-limit-reads-to-end", 2, 0, "23456789"},
		{"offset-at-end", 10, 5, ""},
		{"offset-past-end", 20, 5, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			data, total, err := fs.GetRange(ctx, key, tc.offset, tc.limit)
			if err != nil {
				t.Fatalf("getrange: %v", err)
			}
			if string(data) != tc.want {
				t.Errorf("data = %q, want %q", data, tc.want)
			}
			if total != 10 {
				t.Errorf("total = %d, want 10", total)
			}
		})
	}
}

func TestLocalFSReusesOpenBlobFileWhileActive(t *testing.T) {
	fs, err := NewLocalFS(t.TempDir())
	if err != nil {
		t.Fatalf("new local fs: %v", err)
	}
	ctx := context.Background()
	key, err := fs.Put(ctx, []byte("0123456789"))
	if err != nil {
		t.Fatalf("put: %v", err)
	}

	first, err := fs.openBlobFile(key)
	if err != nil {
		t.Fatalf("open first: %v", err)
	}

	second, err := fs.openBlobFile(key)
	if err != nil {
		t.Fatalf("open second: %v", err)
	}
	firstReleased, secondReleased := false, false
	t.Cleanup(func() {
		if !firstReleased {
			fs.releaseBlobFile(first)
		}
		if !secondReleased {
			fs.releaseBlobFile(second)
		}
	})
	if first != second {
		t.Fatal("same active blob should reuse one open file handle")
	}
	fs.mu.Lock()
	open, refs := len(fs.openBlobFiles), first.refs
	fs.mu.Unlock()
	if open != 1 || refs != 2 {
		t.Fatalf("active files=%d refs=%d, want 1/2", open, refs)
	}

	fs.releaseBlobFile(first)
	firstReleased = true
	fs.mu.Lock()
	open, refs = len(fs.openBlobFiles), second.refs
	fs.mu.Unlock()
	if open != 1 || refs != 1 {
		t.Fatalf("after first release active files=%d refs=%d, want 1/1", open, refs)
	}

	var buf [4]byte
	if n, err := second.file.ReadAt(buf[:], 3); err != nil || n != len(buf) || string(buf[:]) != "3456" {
		t.Fatalf("shared file ReadAt n=%d err=%v bytes=%q, want 3456", n, err, buf[:])
	}

	fs.releaseBlobFile(second)
	secondReleased = true
	fs.mu.Lock()
	open = len(fs.openBlobFiles)
	fs.mu.Unlock()
	if open != 0 {
		t.Fatalf("after final release active files=%d, want 0", open)
	}
}

func TestLocalFSReusesOpenBlobFileUnderConcurrentOpen(t *testing.T) {
	fs, err := NewLocalFS(t.TempDir())
	if err != nil {
		t.Fatalf("new local fs: %v", err)
	}
	ctx := context.Background()
	key, err := fs.Put(ctx, []byte(strings.Repeat("x", 1024)))
	if err != nil {
		t.Fatalf("put: %v", err)
	}

	const readers = 32
	start := make(chan struct{})
	files := make([]*sharedBlobFile, readers)
	errs := make([]error, readers)
	var wg sync.WaitGroup
	for i := range files {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			files[i], errs[i] = fs.openBlobFile(key)
		}(i)
	}
	close(start)
	wg.Wait()

	released := false
	t.Cleanup(func() {
		if released {
			return
		}
		for _, f := range files {
			if f != nil {
				fs.releaseBlobFile(f)
			}
		}
	})
	for i, err := range errs {
		if err != nil {
			t.Fatalf("open %d: %v", i, err)
		}
	}
	first := files[0]
	if first == nil {
		t.Fatal("first open returned nil")
	}
	for i, f := range files {
		if f != first {
			t.Fatalf("file %d = %p, want shared %p", i, f, first)
		}
	}
	fs.mu.Lock()
	open, refs := len(fs.openBlobFiles), first.refs
	fs.mu.Unlock()
	if open != 1 || refs != readers {
		t.Fatalf("active files=%d refs=%d, want 1/%d", open, refs, readers)
	}

	wg = sync.WaitGroup{}
	for _, f := range files {
		wg.Add(1)
		go func(f *sharedBlobFile) {
			defer wg.Done()
			fs.releaseBlobFile(f)
		}(f)
	}
	wg.Wait()
	released = true
	fs.mu.Lock()
	open = len(fs.openBlobFiles)
	fs.mu.Unlock()
	if open != 0 {
		t.Fatalf("after concurrent release active files=%d, want 0", open)
	}
}
