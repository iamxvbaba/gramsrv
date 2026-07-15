// Command stickerfetch 从官方 Telegram 拉取贴纸集(messages.getStickerSet),写成 telesrv
// SeedMedia 可直接导入的 seed 目录格式:<out>/<setdir>/set_info.json + stickers/<docid>.tgs。
// 这样 masks / custom-emoji / 系统集等只需把 seed 文件放进 data/sticker-seed 即可被现有
// SeedMedia + handler 识别下发,无需改服务端代码。
//
// 支持的 set spec(命令行参数,可多个):
//
//	short:<shortName>            按 short_name 拉(普通/mask/emoji 集)
//	emoji_default_statuses       inputStickerSetEmojiDefaultStatuses
//	emoji_channel_default_statuses inputStickerSetEmojiChannelDefaultStatuses
//	emoji_default_topic_icons    inputStickerSetEmojiDefaultTopicIcons
//	premium_gifts                inputStickerSetPremiumGifts
//	ton_gifts                    inputStickerSetTonGifts
//
// 需登录会话(复用 appearancefetch 的 /tmp/appearance.session,SESSION env 可覆盖)。
//
// 用法: SESSION=/tmp/appearance.session stickerfetch <out_dir> <spec> [spec...]
package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/iamxvbaba/td/telegram"
	"github.com/iamxvbaba/td/telegram/downloader"
	"github.com/iamxvbaba/td/tg"
)

const (
	tdesktopAPIID   = 17349
	tdesktopAPIHash = "344583e45741c457fe1862106095a5eb"
)

func sessionPath() string {
	if p := os.Getenv("SESSION"); p != "" {
		return p
	}
	return "/tmp/appearance.session"
}

func specToInput(spec string) (tg.InputStickerSetClass, string, error) {
	switch {
	case strings.HasPrefix(spec, "short:"):
		name := strings.TrimPrefix(spec, "short:")
		return &tg.InputStickerSetShortName{ShortName: name}, name, nil
	case spec == "emoji_default_statuses":
		return &tg.InputStickerSetEmojiDefaultStatuses{}, "EmojiDefaultStatuses", nil
	case spec == "emoji_channel_default_statuses":
		return &tg.InputStickerSetEmojiChannelDefaultStatuses{}, "EmojiChannelDefaultStatuses", nil
	case spec == "emoji_default_topic_icons":
		return &tg.InputStickerSetEmojiDefaultTopicIcons{}, "EmojiDefaultTopicIcons", nil
	case spec == "premium_gifts":
		return &tg.InputStickerSetPremiumGifts{}, "PremiumGifts", nil
	case spec == "ton_gifts":
		return &tg.InputStickerSetTonGifts{}, "TonGifts", nil
	default:
		return nil, "", fmt.Errorf("unknown spec %q", spec)
	}
}

// ---- seed JSON 结构(字段对齐 internal/app/files/seed.go 的解析) ----

type attrJSON struct {
	Type              string           `json:"_"`
	W                 int              `json:"w,omitempty"`
	H                 int              `json:"h,omitempty"`
	Alt               string           `json:"alt,omitempty"`
	Mask              bool             `json:"mask,omitempty"`
	Free              bool             `json:"free,omitempty"`
	TextColor         bool             `json:"text_color,omitempty"`
	FileName          string           `json:"file_name,omitempty"`
	Duration          float64          `json:"duration,omitempty"`
	RoundMessage      bool             `json:"round_message,omitempty"`
	SupportsStreaming bool             `json:"supports_streaming,omitempty"`
	Voice             bool             `json:"voice,omitempty"`
	Title             string           `json:"title,omitempty"`
	Performer         string           `json:"performer,omitempty"`
	Stickerset        *inputSetRefJSON `json:"stickerset,omitempty"`
}

type inputSetRefJSON struct {
	ID         int64 `json:"id"`
	AccessHash int64 `json:"access_hash"`
}

type documentJSON struct {
	ID            int64      `json:"id"`
	AccessHash    int64      `json:"access_hash"`
	FileReference string     `json:"file_reference"`
	Date          string     `json:"date"`
	MimeType      string     `json:"mime_type"`
	Size          int64      `json:"size"`
	DCID          int        `json:"dc_id"`
	Attributes    []attrJSON `json:"attributes"`
}

type packJSON struct {
	Emoticon  string  `json:"emoticon"`
	Documents []int64 `json:"documents"`
}

type setJSON struct {
	ID         int64  `json:"id"`
	AccessHash int64  `json:"access_hash"`
	Title      string `json:"title"`
	ShortName  string `json:"short_name"`
	Count      int    `json:"count"`
	Hash       int    `json:"hash"`
	Official   bool   `json:"official"`
	Masks      bool   `json:"masks"`
	Emojis     bool   `json:"emojis"`
}

type resultJSON struct {
	Type      string         `json:"_"`
	Set       setJSON        `json:"set"`
	Packs     []packJSON     `json:"packs"`
	Documents []documentJSON `json:"documents"`
}

type fileJSON struct {
	APICall      string     `json:"api_call"`
	InputSetType string     `json:"input_set_type"`
	Result       resultJSON `json:"result"`
}

func mapAttrs(in []tg.DocumentAttributeClass) []attrJSON {
	out := make([]attrJSON, 0, len(in))
	for _, a := range in {
		switch v := a.(type) {
		case *tg.DocumentAttributeImageSize:
			out = append(out, attrJSON{Type: "DocumentAttributeImageSize", W: v.W, H: v.H})
		case *tg.DocumentAttributeAnimated:
			out = append(out, attrJSON{Type: "DocumentAttributeAnimated"})
		case *tg.DocumentAttributeSticker:
			aj := attrJSON{Type: "DocumentAttributeSticker", Alt: v.Alt, Mask: v.Mask}
			if id, ok := v.Stickerset.(*tg.InputStickerSetID); ok {
				aj.Stickerset = &inputSetRefJSON{ID: id.ID, AccessHash: id.AccessHash}
			}
			out = append(out, aj)
		case *tg.DocumentAttributeCustomEmoji:
			aj := attrJSON{Type: "DocumentAttributeCustomEmoji", Alt: v.Alt, Free: v.Free, TextColor: v.TextColor}
			if id, ok := v.Stickerset.(*tg.InputStickerSetID); ok {
				aj.Stickerset = &inputSetRefJSON{ID: id.ID, AccessHash: id.AccessHash}
			}
			out = append(out, aj)
		case *tg.DocumentAttributeVideo:
			out = append(out, attrJSON{Type: "DocumentAttributeVideo", W: v.W, H: v.H, Duration: v.Duration, RoundMessage: v.RoundMessage, SupportsStreaming: v.SupportsStreaming})
		case *tg.DocumentAttributeFilename:
			out = append(out, attrJSON{Type: "DocumentAttributeFilename", FileName: v.FileName})
		}
	}
	return out
}

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: SESSION=/tmp/appearance.session stickerfetch <out_dir> <spec> [spec...]\n  spec: short:<name> | emoji_default_statuses | emoji_channel_default_statuses | emoji_default_topic_icons | premium_gifts | ton_gifts | effects")
		os.Exit(2)
	}
	out := os.Args[1]
	specs := os.Args[2:]

	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()

	client := telegram.NewClient(tdesktopAPIID, tdesktopAPIHash, telegram.Options{
		SessionStorage: &telegram.FileSessionStorage{Path: sessionPath()},
	})
	if err := client.Run(ctx, func(ctx context.Context) error {
		api := client.API()
		if status, err := client.Auth().Status(ctx); err != nil || !status.Authorized {
			return fmt.Errorf("session %s 未授权(先用 appearancefetch 登录): %v", sessionPath(), err)
		}
		dl := downloader.NewDownloader()
		for _, spec := range specs {
			var err error
			if spec == "effects" {
				err = fetchEffects(ctx, api, dl, out)
			} else {
				err = fetchSet(ctx, api, dl, out, spec)
			}
			if err != nil {
				fmt.Fprintf(os.Stderr, "skip %s: %v\n", spec, err)
			}
		}
		return nil
	}); err != nil {
		fmt.Fprintln(os.Stderr, "ERROR:", err)
		os.Exit(1)
	}
}

func fetchSet(ctx context.Context, api *tg.Client, dl *downloader.Downloader, outRoot, spec string) error {
	input, label, err := specToInput(spec)
	if err != nil {
		return err
	}
	res, err := api.MessagesGetStickerSet(ctx, &tg.MessagesGetStickerSetRequest{Stickerset: input, Hash: 0})
	if err != nil {
		return fmt.Errorf("getStickerSet: %w", err)
	}
	full, ok := res.(*tg.MessagesStickerSet)
	if !ok {
		return fmt.Errorf("got %T, want messagesStickerSet", res)
	}
	setName := full.Set.ShortName
	if setName == "" {
		setName = label
	}
	setDir := filepath.Join(outRoot, fmt.Sprintf("%s_%d", setName, full.Set.ID))
	stickersDir := filepath.Join(setDir, "stickers")
	if err := os.MkdirAll(stickersDir, 0o755); err != nil {
		return err
	}

	fmt.Printf("[%s] set=%q id=%d docs=%d downloading...\n", spec, setName, full.Set.ID, len(full.Documents))
	docs := make([]documentJSON, 0, len(full.Documents))
	for i, d := range full.Documents {
		doc, ok := d.(*tg.Document)
		if !ok {
			continue
		}
		// 下载主体 blob → stickers/<docid>.<ext>
		ext := ".tgs"
		if doc.MimeType == "video/webm" {
			ext = ".webm"
		} else if doc.MimeType == "image/webp" {
			ext = ".webp"
		}
		path := filepath.Join(stickersDir, fmt.Sprintf("%d%s", doc.ID, ext))
		if !completeExistingFile(path, doc.Size) {
			loc := &tg.InputDocumentFileLocation{ID: doc.ID, AccessHash: doc.AccessHash, FileReference: doc.FileReference}
			if err := downloadToFile(ctx, dl, api, loc, stickersDir, path); err != nil {
				return fmt.Errorf("download doc %d (%d/%d): %w", doc.ID, i+1, len(full.Documents), err)
			}
		}
		docs = append(docs, documentJSON{
			ID:            doc.ID,
			AccessHash:    doc.AccessHash,
			FileReference: hex.EncodeToString(doc.FileReference),
			Date:          time.Unix(int64(doc.Date), 0).UTC().Format(time.RFC3339),
			MimeType:      doc.MimeType,
			Size:          doc.Size,
			DCID:          doc.DCID,
			Attributes:    mapAttrs(doc.Attributes),
		})
		if (i+1)%25 == 0 || i+1 == len(full.Documents) {
			fmt.Printf("[%s] docs %d/%d\n", spec, i+1, len(full.Documents))
		}
	}

	packs := make([]packJSON, 0, len(full.Packs))
	for _, p := range full.Packs {
		packs = append(packs, packJSON{Emoticon: p.Emoticon, Documents: append([]int64(nil), p.Documents...)})
	}

	out := fileJSON{
		APICall:      "messages.getStickerSet",
		InputSetType: fmt.Sprintf("%T", input),
		Result: resultJSON{
			Type: "StickerSet",
			Set: setJSON{
				ID:         full.Set.ID,
				AccessHash: full.Set.AccessHash,
				Title:      full.Set.Title,
				ShortName:  full.Set.ShortName,
				Count:      full.Set.Count,
				Hash:       full.Set.Hash,
				Official:   full.Set.Official,
				Masks:      full.Set.Masks,
				Emojis:     full.Set.Emojis,
			},
			Packs:     packs,
			Documents: docs,
		},
	}
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(setDir, "set_info.json"), append(b, '\n'), 0o644); err != nil {
		return err
	}
	fmt.Printf("[%s] set=%q id=%d docs=%d masks=%v emojis=%v\n", spec, setName, full.Set.ID, len(docs), full.Set.Masks, full.Set.Emojis)
	return nil
}

func completeExistingFile(path string, wantSize int64) bool {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return false
	}
	if wantSize > 0 {
		return info.Size() == wantSize
	}
	return info.Size() > 0
}

func downloadToFile(ctx context.Context, dl *downloader.Downloader, api *tg.Client, loc tg.InputFileLocationClass, dir, path string) error {
	tmp, err := os.CreateTemp(dir, ".download-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() {
		_ = os.Remove(tmpPath)
	}()
	if _, err := dl.Download(api, loc).Stream(ctx, tmp); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	_ = os.Remove(path)
	return os.Rename(tmpPath, path)
}

// ---- 消息特效(messages.getAvailableEffects)----

type effectJSON struct {
	ID                int64  `json:"id"`
	Emoticon          string `json:"emoticon"`
	StaticIconID      int64  `json:"static_icon_id,omitempty"`
	EffectStickerID   int64  `json:"effect_sticker_id"`
	EffectAnimationID int64  `json:"effect_animation_id,omitempty"`
	PremiumRequired   bool   `json:"premium_required,omitempty"`
}

type effectsFileJSON struct {
	APICall string `json:"api_call"`
	Result  struct {
		Effects   []effectJSON   `json:"effects"`
		Documents []documentJSON `json:"documents"`
	} `json:"result"`
}

// downloadDoc 下载一个文档主体到 dir/<docid>.<ext> 并返回其 seed 元数据。
func downloadDoc(ctx context.Context, api *tg.Client, dl *downloader.Downloader, dir string, doc *tg.Document) (documentJSON, error) {
	ext := ".tgs"
	switch doc.MimeType {
	case "video/webm":
		ext = ".webm"
	case "image/webp":
		ext = ".webp"
	}
	path := filepath.Join(dir, fmt.Sprintf("%d%s", doc.ID, ext))
	f, err := os.Create(path)
	if err != nil {
		return documentJSON{}, err
	}
	loc := &tg.InputDocumentFileLocation{ID: doc.ID, AccessHash: doc.AccessHash, FileReference: doc.FileReference}
	if _, err := dl.Download(api, loc).Stream(ctx, f); err != nil {
		f.Close()
		return documentJSON{}, fmt.Errorf("download doc %d: %w", doc.ID, err)
	}
	f.Close()
	return documentJSON{
		ID:            doc.ID,
		AccessHash:    doc.AccessHash,
		FileReference: hex.EncodeToString(doc.FileReference),
		Date:          time.Unix(int64(doc.Date), 0).UTC().Format(time.RFC3339),
		MimeType:      doc.MimeType,
		Size:          doc.Size,
		DCID:          doc.DCID,
		Attributes:    mapAttrs(doc.Attributes),
	}, nil
}

func fetchEffects(ctx context.Context, api *tg.Client, dl *downloader.Downloader, outRoot string) error {
	res, err := api.MessagesGetAvailableEffects(ctx, 0)
	if err != nil {
		return fmt.Errorf("getAvailableEffects: %w", err)
	}
	full, ok := res.(*tg.MessagesAvailableEffects)
	if !ok {
		return fmt.Errorf("got %T, want messagesAvailableEffects", res)
	}
	dir := filepath.Join(outRoot, "telegram_effects_export")
	docsDir := filepath.Join(dir, "documents")
	if err := os.MkdirAll(docsDir, 0o755); err != nil {
		return err
	}
	var out effectsFileJSON
	out.APICall = "messages.getAvailableEffects"
	for _, d := range full.Documents {
		doc, ok := d.(*tg.Document)
		if !ok {
			continue
		}
		dj, err := downloadDoc(ctx, api, dl, docsDir, doc)
		if err != nil {
			return err
		}
		out.Result.Documents = append(out.Result.Documents, dj)
	}
	for _, e := range full.Effects {
		ej := effectJSON{ID: e.ID, Emoticon: e.Emoticon, EffectStickerID: e.EffectStickerID, PremiumRequired: e.PremiumRequired}
		if v, ok := e.GetStaticIconID(); ok {
			ej.StaticIconID = v
		}
		if v, ok := e.GetEffectAnimationID(); ok {
			ej.EffectAnimationID = v
		}
		out.Result.Effects = append(out.Result.Effects, ej)
	}
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "effects.json"), append(b, '\n'), 0o644); err != nil {
		return err
	}
	fmt.Printf("[effects] effects=%d documents=%d\n", len(out.Result.Effects), len(out.Result.Documents))
	return nil
}
