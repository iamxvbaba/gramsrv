package rpc

import (
	"context"
	"testing"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
)

func TestStorageFileTypePrefersMagicOverMime(t *testing.T) {
	webp := []byte{'R', 'I', 'F', 'F', 0, 0, 0, 0, 'W', 'E', 'B', 'P'}
	if _, ok := storageFileType("image/jpeg", webp).(*tg.StorageFileWebp); !ok {
		t.Fatalf("webp bytes mislabeled as jpeg should return StorageFileWebp")
	}
}

func TestStorageFileTypeFallsBackToMime(t *testing.T) {
	if _, ok := storageFileType("image/png", nil).(*tg.StorageFilePng); !ok {
		t.Fatalf("png mime without bytes should return StorageFilePng")
	}
}

func TestUploadGetFileRejectsInvalidRanges(t *testing.T) {
	r := &Router{deps: Deps{Files: &fakeFiles{}}}
	location := &tg.InputDocumentFileLocation{ID: 42}
	tests := []struct {
		name   string
		offset int64
		limit  int
	}{
		{name: "negative offset", offset: -1, limit: 1},
		{name: "zero limit", offset: 0, limit: 0},
		{name: "negative limit", offset: 0, limit: -1},
		{name: "too large limit", offset: 0, limit: maxUploadGetFileChunkLimit + 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := r.onUploadGetFile(context.Background(), &tg.UploadGetFileRequest{
				Location: location,
				Offset:   tt.offset,
				Limit:    tt.limit,
			})
			if !tgerr.Is(err, "LIMIT_INVALID") {
				t.Fatalf("err = %v, want LIMIT_INVALID", err)
			}
		})
	}
}

func TestFileLocationKeyUsesDocumentID(t *testing.T) {
	key, ok := fileLocationKey(&tg.InputDocumentFileLocation{
		ID:        5382305375846410902,
		ThumbSize: "m",
	})
	if !ok {
		t.Fatal("fileLocationKey returned !ok")
	}
	const want = "doc:5382305375846410902:m"
	if key != want {
		t.Fatalf("key = %q, want %q", key, want)
	}
}

func TestFileLocationKeyMapsLegacyAndroidPhotoLocations(t *testing.T) {
	tests := []struct {
		name     string
		location tg.InputFileLocationClass
		want     string
	}{
		{
			name:     "plain small avatar",
			location: &tg.InputFileLocation{VolumeID: -3999, LocalID: int('a')},
			want:     "photo:3999:a",
		},
		{
			name:     "plain transient response avatar",
			location: &tg.InputFileLocation{VolumeID: -3999, LocalID: int('s')},
			want:     "photo:3999:s",
		},
		{
			name:     "plain big avatar",
			location: &tg.InputFileLocation{VolumeID: -3999, LocalID: int('c')},
			want:     "photo:3999:c",
		},
		{
			name:     "plain animated avatar video",
			location: &tg.InputFileLocation{VolumeID: -3999, LocalID: int('u')},
			want:     "photo:3999:u",
		},
		{
			name:     "photo legacy with id",
			location: &tg.InputPhotoLegacyFileLocation{ID: 4001, VolumeID: -3999, LocalID: int('a')},
			want:     "photo:4001:a",
		},
		{
			name:     "peer legacy big",
			location: &tg.InputPeerPhotoFileLocationLegacy{VolumeID: -4002, LocalID: int('a'), Big: true, Peer: &tg.InputPeerSelf{}},
			want:     "photo:4002:c",
		},
		{
			name:     "modern response transient size",
			location: &tg.InputPhotoFileLocation{ID: 4003, ThumbSize: "s"},
			want:     "photo:4003:s",
		},
		{
			name:     "modern peer small remains canonical a",
			location: &tg.InputPeerPhotoFileLocation{PhotoID: 4004, Peer: &tg.InputPeerSelf{}},
			want:     "photo:4004:a",
		},
		{
			name:     "document thumb",
			location: &tg.InputFileLocation{VolumeID: -1129957402753368786, LocalID: 1000 + int('m')},
			want:     "doc:1129957402753368786:m",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key, ok := fileLocationKey(tt.location)
			if !ok {
				t.Fatal("fileLocationKey returned !ok")
			}
			if key != tt.want {
				t.Fatalf("key = %q, want %q", key, tt.want)
			}
		})
	}
}

func TestFileLocationKeyRejectsUnknownLegacyLocations(t *testing.T) {
	tests := []tg.InputFileLocationClass{
		&tg.InputFileLocation{VolumeID: 3999, LocalID: int('a')},
		&tg.InputFileLocation{VolumeID: -3999, LocalID: 7},
		&tg.InputPhotoLegacyFileLocation{VolumeID: -3999, LocalID: 7},
	}
	for _, location := range tests {
		if key, ok := fileLocationKey(location); ok {
			t.Fatalf("fileLocationKey(%T) = %q, true; want false", location, key)
		}
	}
}
