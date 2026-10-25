package admin

import (
	"context"
	"strings"
	"testing"

	"telesrv/internal/domain"
)

type fakeGifCatalogService struct {
	entries     []domain.GifCatalogEntry
	listCalls   int
	uploadCalls int
	createCalls int
}

func (s *fakeGifCatalogService) ValidateGifUpload(_ string, data []byte) (string, bool) {
	return "image/gif", strings.HasPrefix(string(data), "GIF89a")
}
func (s *fakeGifCatalogService) AdminUploadGifMaterial(context.Context, string, []byte) (domain.Document, error) {
	s.uploadCalls++
	return domain.Document{ID: 91}, nil
}
func (s *fakeGifCatalogService) AdminCreateGifCatalogEntry(context.Context, string, int64, string) (domain.GifCatalogEntry, error) {
	s.createCalls++
	entry := domain.GifCatalogEntry{ID: 92, DocumentID: 91, Title: "Wave", Enabled: true}
	s.entries = append(s.entries, entry)
	return entry, nil
}
func (s *fakeGifCatalogService) AdminListGifCatalog(context.Context) ([]domain.GifCatalogEntry, error) {
	s.listCalls++
	return append([]domain.GifCatalogEntry(nil), s.entries...), nil
}
func (*fakeGifCatalogService) AdminSetGifCatalogEnabled(context.Context, int64, bool) (bool, error) {
	return true, nil
}
func (*fakeGifCatalogService) AdminSetGifCatalogSortOrder(context.Context, int64, int) (bool, error) {
	return true, nil
}
func (*fakeGifCatalogService) AdminSetGifCatalogCategory(context.Context, int64, string) (bool, error) {
	return true, nil
}
func (*fakeGifCatalogService) AdminDeleteGifCatalogEntry(context.Context, int64) (bool, error) {
	return true, nil
}
func (*fakeGifCatalogService) GetFile(context.Context, domain.FileDownloadRequest) (domain.FileChunk, bool, error) {
	return domain.FileChunk{}, false, nil
}

func TestCreateGifCatalogEntryReplayAndContentFingerprint(t *testing.T) {
	ctx := context.Background()
	repo := newMemoryCommandRepo()
	gifs := &fakeGifCatalogService{}
	svc := NewService(Dependencies{Commands: repo, GifCatalog: gifs, Now: fixedNow})
	req := CreateGifCatalogEntryRequest{
		CommandMeta: CommandMeta{CommandID: "gif-create-1", Actor: "ops", Reason: "catalog"},
		Title:       "Wave",
		FileName:    "wave.gif",
		Data:        []byte("GIF89a-one"),
	}
	first, err := svc.CreateGifCatalogEntry(ctx, req)
	if err != nil || first.Status != string(domain.AdminCommandCompleted) {
		t.Fatalf("first create = %+v err=%v", first, err)
	}
	if gifs.listCalls != 1 || gifs.uploadCalls != 1 || gifs.createCalls != 1 {
		t.Fatalf("first calls list/upload/create=%d/%d/%d", gifs.listCalls, gifs.uploadCalls, gifs.createCalls)
	}

	// Even when the catalog has become full, the same completed command must be
	// replayed before capacity preflight and must not upload another blob.
	gifs.entries = make([]domain.GifCatalogEntry, domain.MaxGifCatalogEntries)
	replay, err := svc.CreateGifCatalogEntry(ctx, req)
	if err != nil || !replay.AlreadyExecuted {
		t.Fatalf("replay = %+v err=%v", replay, err)
	}
	if gifs.listCalls != 1 || gifs.uploadCalls != 1 || gifs.createCalls != 1 {
		t.Fatalf("replay calls list/upload/create=%d/%d/%d", gifs.listCalls, gifs.uploadCalls, gifs.createCalls)
	}

	conflict := req
	conflict.Data = []byte("GIF89a-two")
	if _, err := svc.CreateGifCatalogEntry(ctx, conflict); err == nil || err.Error() != "COMMAND_ID_CONFLICT" {
		t.Fatalf("different content with same command id err=%v", err)
	}
	if gifs.listCalls != 1 || gifs.uploadCalls != 1 || gifs.createCalls != 1 {
		t.Fatalf("conflict mutated list/upload/create=%d/%d/%d", gifs.listCalls, gifs.uploadCalls, gifs.createCalls)
	}
}

func TestSetGifCatalogCategoryValidatesAndAudits(t *testing.T) {
	ctx := context.Background()
	gifs := &fakeGifCatalogService{}
	svc := NewService(Dependencies{Commands: newMemoryCommandRepo(), GifCatalog: gifs, Now: fixedNow})
	meta := CommandMeta{CommandID: "gif-category-1", Actor: "ops", Reason: "catalog"}
	if _, err := svc.SetGifCatalogCategory(ctx, SetGifCatalogCategoryRequest{CommandMeta: meta, ID: 92, Category: "invalid"}); err != domain.ErrGifCatalogEntryInvalid {
		t.Fatalf("invalid category err=%v", err)
	}
	result, err := svc.SetGifCatalogCategory(ctx, SetGifCatalogCategoryRequest{CommandMeta: meta, ID: 92, Category: domain.GifCategoryAnimals})
	if err != nil || result.Status != string(domain.AdminCommandCompleted) {
		t.Fatalf("set category = %+v err=%v", result, err)
	}
	meta.CommandID = "gif-category-2"
	result, err = svc.SetGifCatalogCategory(ctx, SetGifCatalogCategoryRequest{CommandMeta: meta, ID: 92, Category: ""})
	if err != nil || result.Status != string(domain.AdminCommandCompleted) {
		t.Fatalf("restore auto = %+v err=%v", result, err)
	}
}

func TestCreateGifCatalogEntryRejectsOversizeFilenameBeforeUpload(t *testing.T) {
	gifs := &fakeGifCatalogService{}
	svc := NewService(Dependencies{Commands: newMemoryCommandRepo(), GifCatalog: gifs, Now: fixedNow})
	_, err := svc.CreateGifCatalogEntry(context.Background(), CreateGifCatalogEntryRequest{
		CommandMeta: CommandMeta{CommandID: "gif-long-name", Actor: "ops", Reason: "catalog"},
		Title:       "Cat", FileName: strings.Repeat("x", domain.MaxGifCatalogFileNameLen+1), Data: []byte("GIF89a-one"),
	})
	if err != domain.ErrGifCatalogEntryInvalid || gifs.uploadCalls != 0 {
		t.Fatalf("err=%v uploadCalls=%d", err, gifs.uploadCalls)
	}
}
