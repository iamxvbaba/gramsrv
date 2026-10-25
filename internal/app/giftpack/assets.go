// Adapted from owpengram-server dev@55c3e84 under Apache-2.0; see NOTICE.md.
package giftpack

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
)

// maxAssetBytes bounds a single asset read from a pack; PrepareAnimation
// enforces the real (tighter) per-format ceilings afterwards, this is just a
// defensive cap against a hostile zip entry before that point.
const (
	MaxArchiveBytes      = 32 << 20
	maxAssetBytes        = 8 << 20
	maxManifestBytes     = 1 << 20
	maxEntries           = 512
	maxUncompressedBytes = 128 << 20
)

// AssetResolver resolves a path referenced by a Manifest (e.g.
// GiftSpec.BaseAnimation, AttrSpec.Animation) to its file bytes.
type AssetResolver interface {
	Open(path string) ([]byte, error)
}

// ZipAssetResolver resolves paths against an uploaded pack .zip. pack.json
// is expected at the archive root.
type ZipAssetResolver struct {
	files map[string]*zip.File
}

// NewZipAssetResolver opens a pack archive from raw zip bytes.
func NewZipAssetResolver(data []byte) (*ZipAssetResolver, error) {
	if len(data) == 0 || len(data) > MaxArchiveBytes {
		return nil, fmt.Errorf("pack zip must be 1..%d bytes", MaxArchiveBytes)
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("open pack zip: %w", err)
	}
	if len(zr.File) == 0 || len(zr.File) > maxEntries {
		return nil, fmt.Errorf("pack zip has too many entries")
	}
	files := make(map[string]*zip.File, len(zr.File))
	var total uint64
	for _, f := range zr.File {
		name := f.Name
		if strings.HasSuffix(name, "/") {
			if !validAssetPath(strings.TrimSuffix(name, "/")) {
				return nil, fmt.Errorf("invalid pack directory %q", name)
			}
			continue
		}
		if !validAssetPath(name) || f.FileInfo().Mode()&os.ModeSymlink != 0 || f.FileInfo().IsDir() {
			return nil, fmt.Errorf("invalid pack entry %q", name)
		}
		if _, ok := files[name]; ok {
			return nil, fmt.Errorf("duplicate pack entry %q", name)
		}
		limit := uint64(maxAssetBytes)
		if name == "pack.json" {
			limit = maxManifestBytes
		}
		if f.UncompressedSize64 == 0 || f.UncompressedSize64 > limit || total > maxUncompressedBytes-f.UncompressedSize64 {
			return nil, fmt.Errorf("pack entry %q exceeds size limit", name)
		}
		total += f.UncompressedSize64
		files[name] = f
	}
	if files["pack.json"] == nil {
		return nil, fmt.Errorf("pack.json is required at zip root")
	}
	return &ZipAssetResolver{files: files}, nil
}

// Manifest returns the raw bytes of pack.json.
func (z *ZipAssetResolver) Manifest() ([]byte, error) {
	return z.Open("pack.json")
}

func (z *ZipAssetResolver) Open(path string) ([]byte, error) {
	if !validAssetPath(path) {
		return nil, fmt.Errorf("invalid pack asset path %q", path)
	}
	f := z.files[path]
	if f == nil {
		return nil, fmt.Errorf("asset %q not found in pack", path)
	}
	r, err := f.Open()
	if err != nil {
		return nil, fmt.Errorf("open %q in pack: %w", path, err)
	}
	defer r.Close()
	limit := maxAssetBytes
	if path == "pack.json" {
		limit = maxManifestBytes
	}
	data, err := io.ReadAll(io.LimitReader(r, int64(limit)+1))
	if err != nil {
		return nil, fmt.Errorf("read %q in pack: %w", path, err)
	}
	if len(data) > limit {
		return nil, fmt.Errorf("%q in pack exceeds %d bytes", path, limit)
	}
	return data, nil
}

func validAssetPath(name string) bool {
	return name != "" && !strings.ContainsAny(name, "\\:\x00") && !strings.HasPrefix(name, "/") &&
		path.Clean(name) == name && name != "." && name != ".." && !strings.HasPrefix(name, "../")
}

// MapAssetResolver is useful for validating a generated pack without a ZIP.
type MapAssetResolver map[string][]byte

func (m MapAssetResolver) Open(path string) ([]byte, error) {
	data, ok := m[path]
	if !ok {
		return nil, fmt.Errorf("asset %q not found in pack", path)
	}
	return data, nil
}
