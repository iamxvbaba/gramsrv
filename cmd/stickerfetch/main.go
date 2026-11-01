// Command stickerfetch pulls sticker sets from official Telegram
// (messages.getStickerSet) and writes them as a seed directory format that telesrv
// SeedMedia can import directly: <out>/<setdir>/set_info.json + stickers/<docid>.tgs.
// This way masks / custom-emoji / system sets etc. only need their seed files
// placed in data/sticker-seed to be recognized and served by the existing
// SeedMedia + handler, with no server code change.
//
// Supported set specs (command-line arguments, can be multiple):
//
//	short:<shortName>            pull by short_name (normal/mask/emoji sets)
//	emoji_default_statuses       inputStickerSetEmojiDefaultStatuses
//	emoji_channel_default_statuses inputStickerSetEmojiChannelDefaultStatuses
//	emoji_default_topic_icons    inputStickerSetEmojiDefaultTopicIcons
//	premium_gifts                inputStickerSetPremiumGifts
//	ton_gifts                    inputStickerSetTonGifts
//	id:<id>:<access_hash>        inputStickerSetID (any set, e.g. from featured lists)
//	dice:<emoji>                 inputStickerSetDice
//	animated_emoji               inputStickerSetAnimatedEmoji
//	animated_emoji_animations    inputStickerSetAnimatedEmojiAnimations
//	emoji_generic_animations     inputStickerSetEmojiGenericAnimations
//	effects                      messages.getAvailableEffects
//	reactions                    messages.getAvailableReactions -> telegram_reactions_export/
//	featured_emoji               every set from messages.getFeaturedEmojiStickers
//	featured_stickers            every set from messages.getFeaturedStickers
//
// Documents are exported with their thumbs: Android renders .tgs stickers and
// custom emoji as such only when thumbs are present.
//
// Login session required (reuses appearancefetch's /tmp/appearance.session, overridable via SESSION env).
// TG_API_ID / TG_API_HASH override the built-in TDesktop credentials, which
// Telegram may answer with API_ID_PUBLISHED_FLOOD.
//
// Usage: SESSION=/tmp/appearance.session stickerfetch [-timeout 12m] <out_dir> <spec> [spec...]
package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
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
	case strings.HasPrefix(spec, "id:"):
		parts := strings.Split(strings.TrimPrefix(spec, "id:"), ":")
		if len(parts) != 2 {
			return nil, "", fmt.Errorf("bad spec %q, want id:<id>:<access_hash>", spec)
		}
		id, err1 := strconv.ParseInt(parts[0], 10, 64)
		hash, err2 := strconv.ParseInt(parts[1], 10, 64)
		if err1 != nil || err2 != nil {
			return nil, "", fmt.Errorf("bad spec %q", spec)
		}
		return &tg.InputStickerSetID{ID: id, AccessHash: hash}, "", nil
	case strings.HasPrefix(spec, "dice:"):
		emoticon := strings.TrimPrefix(spec, "dice:")
		return &tg.InputStickerSetDice{Emoticon: emoticon}, "Dice", nil
	case spec == "animated_emoji":
		return &tg.InputStickerSetAnimatedEmoji{}, "AnimatedEmoji", nil
	case spec == "animated_emoji_animations":
		return &tg.InputStickerSetAnimatedEmojiAnimations{}, "AnimatedEmojiAnimations", nil
	case spec == "emoji_generic_animations":
		return &tg.InputStickerSetEmojiGenericAnimations{}, "EmojiGenericAnimations", nil
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

// ---- seed JSON structures (fields aligned with internal/app/files/seed.go's parsing) ----

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
	ID            int64       `json:"id"`
	AccessHash    int64       `json:"access_hash"`
	FileReference string      `json:"file_reference"`
	Date          string      `json:"date"`
	MimeType      string      `json:"mime_type"`
	Size          int64       `json:"size"`
	DCID          int         `json:"dc_id"`
	Attributes    []attrJSON  `json:"attributes"`
	Thumbs        []thumbJSON `json:"thumbs,omitempty"`
}

// thumbJSON mirrors internal/app/files seedThumbJSON. Inline sizes carry their
// bytes as hex; downloadable PhotoSize files sit next to the document as
// <docid>_thumb<N>_PhotoSize_type<t>.jpg (the name SeedMedia's index expects;
// the content may be WebP, the server sniffs the MIME type).
type thumbJSON struct {
	Type     string `json:"_"`
	SizeType string `json:"type"`
	W        int    `json:"w,omitempty"`
	H        int    `json:"h,omitempty"`
	Size     int    `json:"size,omitempty"`
	Bytes    string `json:"bytes,omitempty"`
	Sizes    []int  `json:"sizes,omitempty"`
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

	Thumbs          []thumbJSON `json:"thumbs,omitempty"`
	ThumbDCID       int         `json:"thumb_dc_id,omitempty"`
	ThumbVersion    int         `json:"thumb_version,omitempty"`
	ThumbDocumentID *int64      `json:"thumb_document_id,omitempty"`
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

const usageSpecs = "short:<name> | id:<id>:<access_hash> | emoji_default_statuses | emoji_channel_default_statuses |\n" +
	"  emoji_default_topic_icons | premium_gifts | ton_gifts | dice:<emoji> | animated_emoji |\n" +
	"  animated_emoji_animations | emoji_generic_animations | effects | reactions | featured_emoji | featured_stickers"

func main() {
	timeout := flag.Duration("timeout", 12*time.Minute, "overall run timeout; featured_emoji/featured_stickers exports can take hours")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: SESSION=/tmp/appearance.session stickerfetch [-timeout 12m] <out_dir> <spec> [spec...]\n  spec: %s\n", usageSpecs)
		flag.PrintDefaults()
	}
	flag.Parse()
	if flag.NArg() < 2 {
		flag.Usage()
		os.Exit(2)
	}
	out := flag.Arg(0)
	specs := flag.Args()[1:]

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	id, hash := apiCredentials(tdesktopAPIID, tdesktopAPIHash)
	client := telegram.NewClient(id, hash, telegram.Options{
		SessionStorage: &telegram.FileSessionStorage{Path: sessionPath()},
	})
	if err := client.Run(ctx, func(ctx context.Context) error {
		api := client.API()
		if status, err := client.Auth().Status(ctx); err != nil || !status.Authorized {
			return fmt.Errorf("session %s unauthorized (log in with appearancefetch first): %v", sessionPath(), err)
		}
		dl := downloader.NewDownloader()
		for _, spec := range specs {
			var err error
			if spec == "effects" {
				err = fetchEffects(ctx, api, dl, out)
			} else if spec == "reactions" {
				err = fetchReactions(ctx, api, dl, out)
			} else if spec == "featured_emoji" || spec == "featured_stickers" {
				err = fetchFeatured(ctx, api, dl, out, spec == "featured_emoji")
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
		// Download the body blob → stickers/<docid>.<ext>
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
		thumbs, err := exportThumbs(ctx, fetcher(api, dl), stickersDir, doc)
		if err != nil {
			return fmt.Errorf("thumbs of doc %d: %w", doc.ID, err)
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
			Thumbs:        thumbs,
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

				Thumbs:          inlineThumbs(full.Set.Thumbs),
				ThumbDCID:       full.Set.ThumbDCID,
				ThumbVersion:    full.Set.ThumbVersion,
				ThumbDocumentID: optionalInt64(full.Set.GetThumbDocumentID()),
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

// fetchFunc stores one file location at path. Production uses downloadToFile;
// tests substitute a fake so the export format can be checked offline.
type fetchFunc func(ctx context.Context, loc tg.InputFileLocationClass, dir, path string) error

func fetcher(api *tg.Client, dl *downloader.Downloader) fetchFunc {
	return func(ctx context.Context, loc tg.InputFileLocationClass, dir, path string) error {
		return downloadToFile(ctx, dl, api, loc, dir, path)
	}
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
	// os.CreateTemp creates 0600 files; seeds are read by the server under another uid.
	if err := os.Chmod(tmpPath, 0o644); err != nil {
		return err
	}
	_ = os.Remove(path)
	return os.Rename(tmpPath, path)
}

// ---- Message effects (messages.getAvailableEffects) ----

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

// downloadDoc downloads a document body to dir/<docid>.<ext> and returns its seed metadata.
func downloadDoc(ctx context.Context, fetch fetchFunc, dir string, doc *tg.Document) (documentJSON, error) {
	ext := ".tgs"
	switch doc.MimeType {
	case "video/webm":
		ext = ".webm"
	case "image/webp":
		ext = ".webp"
	}
	path := filepath.Join(dir, fmt.Sprintf("%d%s", doc.ID, ext))
	if !completeExistingFile(path, doc.Size) {
		loc := &tg.InputDocumentFileLocation{ID: doc.ID, AccessHash: doc.AccessHash, FileReference: doc.FileReference}
		if err := fetch(ctx, loc, dir, path); err != nil {
			return documentJSON{}, fmt.Errorf("download doc %d: %w", doc.ID, err)
		}
	}
	thumbs, err := exportThumbs(ctx, fetch, dir, doc)
	if err != nil {
		return documentJSON{}, fmt.Errorf("thumbs of doc %d: %w", doc.ID, err)
	}
	return documentJSON{
		ID:            doc.ID,
		AccessHash:    doc.AccessHash,
		FileReference: hex.EncodeToString(doc.FileReference),
		Date:          time.Unix(int64(doc.Date), 0).UTC().Format(time.RFC3339),
		MimeType:      doc.MimeType,
		Size:          doc.Size,
		DCID:          doc.DCID,
		Attributes:    mapAttrs(doc.Attributes),
		Thumbs:        thumbs,
	}, nil
}

func fetchEffects(ctx context.Context, api *tg.Client, dl *downloader.Downloader, outRoot string) error {
	fetch := fetcher(api, dl)
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
		dj, err := downloadDoc(ctx, fetch, docsDir, doc)
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

// ---- Available reactions (messages.getAvailableReactions) ----
// Written in the layout SeedMedia.seedReactions reads:
// <out>/telegram_reactions_export/global_json/available_reactions_raw.json + reactions/<docid>.<ext>

type reactionJSON struct {
	Reaction          string        `json:"reaction"`
	Title             string        `json:"title"`
	Inactive          bool          `json:"inactive,omitempty"`
	Premium           bool          `json:"premium,omitempty"`
	StaticIcon        *documentJSON `json:"static_icon,omitempty"`
	AppearAnimation   *documentJSON `json:"appear_animation,omitempty"`
	SelectAnimation   *documentJSON `json:"select_animation,omitempty"`
	ActivateAnimation *documentJSON `json:"activate_animation,omitempty"`
	EffectAnimation   *documentJSON `json:"effect_animation,omitempty"`
	AroundAnimation   *documentJSON `json:"around_animation,omitempty"`
	CenterIcon        *documentJSON `json:"center_icon,omitempty"`
}

func fetchReactions(ctx context.Context, api *tg.Client, dl *downloader.Downloader, outRoot string) error {
	res, err := api.MessagesGetAvailableReactions(ctx, 0)
	if err != nil {
		return fmt.Errorf("getAvailableReactions: %w", err)
	}
	full, ok := res.(*tg.MessagesAvailableReactions)
	if !ok {
		return fmt.Errorf("got %T, want messagesAvailableReactions", res)
	}
	return writeReactionsExport(ctx, fetcher(api, dl), outRoot, full.Reactions)
}

// writeReactionsExport writes the layout seedReactions imports:
// telegram_reactions_export/global_json/available_reactions_raw.json plus the
// documents and their thumbs under telegram_reactions_export/reactions.
func writeReactionsExport(ctx context.Context, fetch fetchFunc, outRoot string, list []tg.AvailableReaction) error {
	dir := filepath.Join(outRoot, "telegram_reactions_export")
	docsDir := filepath.Join(dir, "reactions")
	if err := os.MkdirAll(docsDir, 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(dir, "global_json"), 0o755); err != nil {
		return err
	}
	seen := map[int64]*documentJSON{}
	get := func(d tg.DocumentClass) (*documentJSON, error) {
		doc, ok := d.(*tg.Document)
		if !ok {
			return nil, nil
		}
		if dj, ok := seen[doc.ID]; ok {
			return dj, nil
		}
		dj, err := downloadDoc(ctx, fetch, docsDir, doc)
		if err != nil {
			return nil, err
		}
		seen[doc.ID] = &dj
		return &dj, nil
	}
	reactions := make([]reactionJSON, 0, len(list))
	for i, r := range list {
		rj := reactionJSON{Reaction: r.Reaction, Title: r.Title, Inactive: r.Inactive, Premium: r.Premium}
		docs := []struct {
			dst **documentJSON
			src tg.DocumentClass
		}{
			{&rj.StaticIcon, r.StaticIcon},
			{&rj.AppearAnimation, r.AppearAnimation},
			{&rj.SelectAnimation, r.SelectAnimation},
			{&rj.ActivateAnimation, r.ActivateAnimation},
			{&rj.EffectAnimation, r.EffectAnimation},
		}
		if v, ok := r.GetAroundAnimation(); ok {
			docs = append(docs, struct {
				dst **documentJSON
				src tg.DocumentClass
			}{&rj.AroundAnimation, v})
		}
		if v, ok := r.GetCenterIcon(); ok {
			docs = append(docs, struct {
				dst **documentJSON
				src tg.DocumentClass
			}{&rj.CenterIcon, v})
		}
		for _, d := range docs {
			if d.src == nil {
				continue
			}
			dj, err := get(d.src)
			if err != nil {
				return fmt.Errorf("reaction %s: %w", r.Reaction, err)
			}
			*d.dst = dj
		}
		reactions = append(reactions, rj)
		fmt.Printf("[reactions] %d/%d %s\n", i+1, len(list), r.Reaction)
	}
	b, err := json.MarshalIndent(map[string]any{"result": map[string]any{"reactions": reactions}}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "global_json", "available_reactions_raw.json"), append(b, '\n'), 0o644); err != nil {
		return err
	}
	fmt.Printf("[reactions] reactions=%d documents=%d\n", len(reactions), len(seen))
	return nil
}

// fetchFeatured pulls the sticker/emoji sets Telegram currently features
// (messages.getFeatured[Emoji]Stickers), exporting each by ID.
func fetchFeatured(ctx context.Context, api *tg.Client, dl *downloader.Downloader, outRoot string, emoji bool) error {
	var res tg.MessagesFeaturedStickersClass
	var err error
	if emoji {
		res, err = api.MessagesGetFeaturedEmojiStickers(ctx, 0)
	} else {
		res, err = api.MessagesGetFeaturedStickers(ctx, 0)
	}
	if err != nil {
		return fmt.Errorf("getFeatured: %w", err)
	}
	full, ok := res.(*tg.MessagesFeaturedStickers)
	if !ok {
		return fmt.Errorf("got %T, want messagesFeaturedStickers", res)
	}
	fmt.Printf("[featured] emoji=%v sets=%d\n", emoji, len(full.Sets))
	for _, covered := range full.Sets {
		set := covered.GetSet()
		spec := fmt.Sprintf("id:%d:%d", set.ID, set.AccessHash)
		if err := fetchSet(ctx, api, dl, outRoot, spec); err != nil {
			fmt.Fprintf(os.Stderr, "skip featured %s (%s): %v\n", set.ShortName, spec, err)
		}
	}
	return nil
}

// exportThumbs maps a document's thumbs to seed JSON, downloading PhotoSize
// renditions next to the document. Android only renders .tgs stickers and
// custom emoji as such when thumbs are present, and the server draws emoji
// avatars from them, so they are not optional.
func exportThumbs(ctx context.Context, fetch fetchFunc, dir string, doc *tg.Document) ([]thumbJSON, error) {
	out := make([]thumbJSON, 0, len(doc.Thumbs))
	for i, th := range doc.Thumbs {
		switch v := th.(type) {
		case *tg.PhotoPathSize:
			out = append(out, thumbJSON{Type: "PhotoPathSize", SizeType: v.Type, Bytes: hex.EncodeToString(v.Bytes)})
		case *tg.PhotoStrippedSize:
			out = append(out, thumbJSON{Type: "PhotoStrippedSize", SizeType: v.Type, Bytes: hex.EncodeToString(v.Bytes)})
		case *tg.PhotoCachedSize:
			out = append(out, thumbJSON{Type: "PhotoCachedSize", SizeType: v.Type, W: v.W, H: v.H, Bytes: hex.EncodeToString(v.Bytes)})
		case *tg.PhotoSize, *tg.PhotoSizeProgressive:
			var typ string
			var w, h, size int
			var sizes []int
			kind := "PhotoSize"
			if ps, ok := v.(*tg.PhotoSize); ok {
				typ, w, h, size = ps.Type, ps.W, ps.H, ps.Size
			} else {
				pp := v.(*tg.PhotoSizeProgressive)
				typ, w, h, sizes, kind = pp.Type, pp.W, pp.H, pp.Sizes, "PhotoSizeProgressive"
				if len(sizes) > 0 {
					size = sizes[len(sizes)-1]
				}
			}
			path := filepath.Join(dir, fmt.Sprintf("%d_thumb%d_PhotoSize_type%s.jpg", doc.ID, i, typ))
			if !completeExistingFile(path, int64(size)) {
				loc := &tg.InputDocumentFileLocation{ID: doc.ID, AccessHash: doc.AccessHash, FileReference: doc.FileReference, ThumbSize: typ}
				if err := fetch(ctx, loc, dir, path); err != nil {
					return nil, fmt.Errorf("thumb %s: %w", typ, err)
				}
			}
			out = append(out, thumbJSON{Type: kind, SizeType: typ, W: w, H: h, Size: size, Sizes: sizes})
		}
	}
	return out, nil
}

// inlineThumbs keeps the set thumbs SeedMedia stores inline (path/stripped/cached).
func inlineThumbs(in []tg.PhotoSizeClass) []thumbJSON {
	var out []thumbJSON
	for _, th := range in {
		switch v := th.(type) {
		case *tg.PhotoPathSize:
			out = append(out, thumbJSON{Type: "PhotoPathSize", SizeType: v.Type, Bytes: hex.EncodeToString(v.Bytes)})
		case *tg.PhotoStrippedSize:
			out = append(out, thumbJSON{Type: "PhotoStrippedSize", SizeType: v.Type, Bytes: hex.EncodeToString(v.Bytes)})
		case *tg.PhotoCachedSize:
			out = append(out, thumbJSON{Type: "PhotoCachedSize", SizeType: v.Type, W: v.W, H: v.H, Bytes: hex.EncodeToString(v.Bytes)})
		}
	}
	return out
}

func optionalInt64(v int64, ok bool) *int64 {
	if !ok {
		return nil
	}
	return &v
}

// apiCredentials returns TG_API_ID/TG_API_HASH when set (the public TDesktop id
// is rate-limited with API_ID_PUBLISHED_FLOOD), otherwise the given defaults.
func apiCredentials(defID int, defHash string) (int, string) {
	id, hash := defID, defHash
	if v := strings.TrimSpace(os.Getenv("TG_API_ID")); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			id = n
		}
	}
	if v := strings.TrimSpace(os.Getenv("TG_API_HASH")); v != "" {
		hash = v
	}
	return id, hash
}
