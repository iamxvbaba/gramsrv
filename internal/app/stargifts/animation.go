package stargifts

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"path/filepath"
	"strings"
	"time"

	"telesrv/internal/domain"
)

// PrepareAnimation normalizes a .tgs or plain Lottie JSON (.json/.lottie) into the
// single canonical pair used by both the Telegram download path and admin preview.
func (s *Service) PrepareAnimation(fileName string, data []byte) (domain.StarGiftAnimation, error) {
	return prepareAnimation(fileName, data)
}

func prepareAnimation(fileName string, data []byte) (domain.StarGiftAnimation, error) {
	fileName = strings.TrimSpace(filepath.Base(fileName))
	ext := strings.ToLower(filepath.Ext(fileName))
	format := domain.StarGiftAnimationLottie
	var rawJSON []byte
	if ext == ".tgs" || isGzip(data) {
		format = domain.StarGiftAnimationTGS
		if int64(len(data)) == 0 || int64(len(data)) > domain.MaxStarGiftTGSBytes {
			return domain.StarGiftAnimation{}, domain.ErrStarGiftFileInvalid
		}
		var err error
		rawJSON, err = decompressSingleTGS(data)
		if err != nil {
			return domain.StarGiftAnimation{}, err
		}
	} else {
		if ext != ".json" && ext != ".lottie" {
			return domain.StarGiftAnimation{}, fmt.Errorf("%w: expected .tgs, .json or plain .lottie", domain.ErrStarGiftFileInvalid)
		}
		if int64(len(data)) == 0 || int64(len(data)) > domain.MaxStarGiftLottieBytes {
			return domain.StarGiftAnimation{}, domain.ErrStarGiftFileInvalid
		}
		rawJSON = data
	}

	normalized, meta, err := normalizeAndValidateLottie(rawJSON)
	if err != nil {
		return domain.StarGiftAnimation{}, err
	}
	tgs, err := gzipLottie(normalized)
	if err != nil || int64(len(tgs)) > domain.MaxStarGiftTGSBytes {
		return domain.StarGiftAnimation{}, domain.ErrStarGiftFileInvalid
	}
	sum := sha256.Sum256(tgs)
	return domain.StarGiftAnimation{
		SourceName:   fileName,
		SourceFormat: format,
		JSON:         normalized,
		TGS:          tgs,
		SHA256:       append([]byte(nil), sum[:]...),
		Width:        meta.W,
		Height:       meta.H,
		FrameRate:    meta.FrameRate,
		InPoint:      meta.InPoint,
		OutPoint:     meta.OutPoint,
	}, nil
}

type lottieMetadata struct {
	Version   string            `json:"v"`
	W         int               `json:"w"`
	H         int               `json:"h"`
	FrameRate float64           `json:"fr"`
	InPoint   float64           `json:"ip"`
	OutPoint  float64           `json:"op"`
	Layers    []json.RawMessage `json:"layers"`
	Assets    []json.RawMessage `json:"assets"`
}

func normalizeAndValidateLottie(data []byte) ([]byte, lottieMetadata, error) {
	data = bytes.TrimSpace(bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF}))
	if len(data) == 0 || int64(len(data)) > domain.MaxStarGiftLottieBytes || !json.Valid(data) {
		return nil, lottieMetadata{}, domain.ErrStarGiftFileInvalid
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var root any
	if err := dec.Decode(&root); err != nil {
		return nil, lottieMetadata{}, domain.ErrStarGiftFileInvalid
	}
	if _, ok := root.(map[string]any); !ok {
		return nil, lottieMetadata{}, domain.ErrStarGiftFileInvalid
	}
	if containsLottieExpression(root) {
		return nil, lottieMetadata{}, fmt.Errorf("%w: expressions are not allowed", domain.ErrStarGiftFileInvalid)
	}
	var meta lottieMetadata
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, lottieMetadata{}, domain.ErrStarGiftFileInvalid
	}
	frameSpan := meta.OutPoint - meta.InPoint
	if meta.Version == "" || meta.W != 512 || meta.H != 512 ||
		math.IsNaN(meta.FrameRate) || math.IsInf(meta.FrameRate, 0) || meta.FrameRate <= 0 || meta.FrameRate > domain.MaxStarGiftAnimationFrameRate ||
		math.IsNaN(meta.InPoint) || math.IsInf(meta.InPoint, 0) || meta.InPoint < 0 ||
		math.IsNaN(meta.OutPoint) || math.IsInf(meta.OutPoint, 0) || meta.OutPoint <= meta.InPoint ||
		frameSpan > meta.FrameRate*domain.MaxStarGiftAnimationSeconds || len(meta.Layers) == 0 {
		return nil, lottieMetadata{}, domain.ErrStarGiftFileInvalid
	}
	// Telegram animated stickers are self-contained. Reject remote or embedded image assets;
	// pre-composition assets with only an id/layers payload remain valid.
	for _, raw := range meta.Assets {
		var asset map[string]json.RawMessage
		if json.Unmarshal(raw, &asset) != nil {
			return nil, lottieMetadata{}, domain.ErrStarGiftFileInvalid
		}
		for _, key := range []string{"p", "u"} {
			if value := asset[key]; len(value) > 0 && string(value) != `""` && string(value) != "null" {
				return nil, lottieMetadata{}, fmt.Errorf("%w: external assets are not allowed", domain.ErrStarGiftFileInvalid)
			}
		}
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, data); err != nil || int64(compact.Len()) > domain.MaxStarGiftLottieBytes {
		return nil, lottieMetadata{}, domain.ErrStarGiftFileInvalid
	}
	return compact.Bytes(), meta, nil
}

func containsLottieExpression(value any) bool {
	switch node := value.(type) {
	case map[string]any:
		for key, child := range node {
			if key == "x" {
				if expression, ok := child.(string); ok && strings.TrimSpace(expression) != "" {
					return true
				}
			}
			if containsLottieExpression(child) {
				return true
			}
		}
	case []any:
		for _, child := range node {
			if containsLottieExpression(child) {
				return true
			}
		}
	}
	return false
}

func isGzip(data []byte) bool {
	return len(data) >= 2 && data[0] == 0x1f && data[1] == 0x8b
}

func decompressSingleTGS(data []byte) ([]byte, error) {
	reader := bytes.NewReader(data)
	gz, err := gzip.NewReader(reader)
	if err != nil {
		return nil, domain.ErrStarGiftFileInvalid
	}
	gz.Multistream(false)
	raw, readErr := io.ReadAll(io.LimitReader(gz, domain.MaxStarGiftLottieBytes+1))
	closeErr := gz.Close()
	if readErr != nil || closeErr != nil || int64(len(raw)) > domain.MaxStarGiftLottieBytes || reader.Len() != 0 {
		return nil, domain.ErrStarGiftFileInvalid
	}
	return raw, nil
}

func gzipLottie(data []byte) ([]byte, error) {
	var out bytes.Buffer
	gz, err := gzip.NewWriterLevel(&out, gzip.BestCompression)
	if err != nil {
		return nil, err
	}
	gz.Header.ModTime = time.Unix(0, 0)
	gz.Header.OS = 255
	if _, err := gz.Write(data); err != nil {
		_ = gz.Close()
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}
