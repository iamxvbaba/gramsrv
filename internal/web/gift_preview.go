package web

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"go.uber.org/zap"

	"telesrv/internal/domain"
)

// Gift-preview image rendering: the same radial-gradient backdrop, tiled
// pattern glyph and model artwork real Telegram composites for a collectible
// gift's link-preview image (og:image), served from /_public/gift-preview/
// so the plain /nft/{slug} landing page -- and therefore any app's generic
// link-preview fetcher, ShuzaGram's own clients included -- gets a correct
// image without special-casing this host.
//
// Rendering a Lottie/TGS animation frame has no reasonable pure-Go path, so
// the actual compositing happens in a small sidecar process (see
// /opt/giftrender-venv on the host) reached over loopback -- this process and
// that one both run with Docker host networking, so 127.0.0.1 crosses the
// container boundary with no extra wiring. This handler owns everything
// sidecar-adjacent: resolving the gift's model/pattern documents through the
// existing blob store, calling the sidecar, and caching the PNG to disk so a
// given collectible only ever gets rendered once.

const (
	maxGiftPreviewDocumentBytes = 4 << 20
	giftPreviewSize             = 512
	giftPreviewSidecarTimeout   = 20 * time.Second
)

type giftPreviewRenderRequest struct {
	ModelB64     string `json:"model_b64"`
	PatternB64   string `json:"pattern_b64"`
	CenterColor  int    `json:"center_color"`
	EdgeColor    int    `json:"edge_color"`
	PatternColor int    `json:"pattern_color"`
	Size         int    `json:"size"`
}

func (h *handler) publicGiftPreviewURL(slug string) string {
	return h.publicBaseURL + "/_public/gift-preview/" + slug
}

// giftPreviewImage serves (rendering and caching on first request) a
// collectible's composited preview PNG.
func (h *handler) giftPreviewImage(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	if h.uniqueGifts == nil || h.photos == nil || !validStarGiftSlugPath(slug) {
		http.NotFound(w, r)
		return
	}
	unique, found, err := h.uniqueGifts.UniqueBySlug(r.Context(), slug)
	if err != nil {
		h.logger.Error("Gift preview: lookup failed", zap.String("slug", slug), zap.Error(err))
		http.Error(w, "gift lookup failed", http.StatusInternalServerError)
		return
	}
	if !found || unique.Model.Document == nil || unique.Pattern.Document == nil {
		http.NotFound(w, r)
		return
	}

	etag := fmt.Sprintf("\"gift-preview-%s\"", unique.Slug)
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}

	png, err := h.loadOrRenderGiftPreview(r.Context(), unique)
	if err != nil {
		h.logger.Error("Gift preview: render failed", zap.String("slug", slug), zap.Error(err))
		http.Error(w, "gift preview render failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Content-Length", strconv.Itoa(len(png)))
	_, _ = w.Write(png)
}

func (h *handler) loadOrRenderGiftPreview(ctx context.Context, unique domain.UniqueStarGift) ([]byte, error) {
	cachePath := h.giftPreviewCachePath(unique.Slug)
	if cachePath != "" {
		if data, err := os.ReadFile(cachePath); err == nil {
			return data, nil
		}
	}

	modelBytes, err := h.fetchDocumentBytes(ctx, unique.Model.Document.ID)
	if err != nil {
		return nil, fmt.Errorf("fetch model document: %w", err)
	}
	patternBytes, err := h.fetchDocumentBytes(ctx, unique.Pattern.Document.ID)
	if err != nil {
		return nil, fmt.Errorf("fetch pattern document: %w", err)
	}

	png, err := h.renderGiftPreviewViaSidecar(ctx, modelBytes, patternBytes,
		unique.Backdrop.CenterColor, unique.Backdrop.EdgeColor, unique.Backdrop.PatternColor)
	if err != nil {
		return nil, err
	}

	if cachePath != "" {
		if err := os.MkdirAll(filepath.Dir(cachePath), 0o750); err == nil {
			tmp := cachePath + ".tmp-" + strconv.FormatInt(time.Now().UnixNano(), 36)
			if err := os.WriteFile(tmp, png, 0o640); err == nil {
				_ = os.Rename(tmp, cachePath)
			} else {
				_ = os.Remove(tmp)
			}
		}
	}
	return png, nil
}

func (h *handler) fetchDocumentBytes(ctx context.Context, documentID int64) ([]byte, error) {
	chunk, found, err := h.photos.GetFile(ctx, domain.FileDownloadRequest{
		LocationKey: fmt.Sprintf("doc:%d", documentID),
		Limit:       maxGiftPreviewDocumentBytes + 1,
	})
	if err != nil {
		return nil, err
	}
	if !found || len(chunk.Bytes) == 0 || len(chunk.Bytes) > maxGiftPreviewDocumentBytes {
		return nil, fmt.Errorf("document %d unavailable or too large", documentID)
	}
	return chunk.Bytes, nil
}

func (h *handler) renderGiftPreviewViaSidecar(ctx context.Context, modelBytes, patternBytes []byte,
	centerColor, edgeColor, patternColor int) ([]byte, error) {
	if h.giftRenderSidecarURL == "" {
		return nil, fmt.Errorf("gift render sidecar is not configured")
	}
	payload, err := json.Marshal(giftPreviewRenderRequest{
		ModelB64:     base64.StdEncoding.EncodeToString(modelBytes),
		PatternB64:   base64.StdEncoding.EncodeToString(patternBytes),
		CenterColor:  centerColor,
		EdgeColor:    edgeColor,
		PatternColor: patternColor,
		Size:         giftPreviewSize,
	})
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, giftPreviewSidecarTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.giftRenderSidecarURL, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("sidecar request: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxGiftPreviewDocumentBytes))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("sidecar returned %d: %s", resp.StatusCode, string(body))
	}
	return body, nil
}

func (h *handler) giftPreviewCachePath(slug string) string {
	if h.giftPreviewDir == "" || !validStarGiftSlugPath(slug) {
		return ""
	}
	return filepath.Join(h.giftPreviewDir, slug+".png")
}
