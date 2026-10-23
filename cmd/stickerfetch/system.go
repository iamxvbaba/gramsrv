package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/iamxvbaba/td/telegram/downloader"
	"github.com/iamxvbaba/td/tg"
)

// systemSets are Telegram's built-in sticker sets, stored under the
// directory names internal/app/files/seed.go maps to system keys: gift pack
// pictures, big animated emoji, dice and the rest.
var systemSets = []struct{ spec, dir string }{
	{"premium_gifts", "DefaultSet_PremiumGifts"},
	{"ton_gifts", "DefaultSet_TonGifts"},
	{"animated_emoji", "DefaultSet_AnimatedEmoji"},
	{"animated_emoji_animations", "DefaultSet_AnimatedEmojiAnimations"},
	{"emoji_generic_animations", "DefaultSet_EmojiGenericAnimations"},
	{"emoji_default_statuses", "DefaultSet_EmojiDefaultStatuses"},
	{"emoji_default_topic_icons", "DefaultSet_EmojiDefaultTopicIcons"},
	{"dice:🎲", "DefaultSet_Dice_Normal"},
	{"dice:🎯", "DefaultSet_Dice_Dart"},
	{"dice:🏀", "DefaultSet_Dice_Basketball"},
	{"dice:⚽", "DefaultSet_Dice_Football"},
	{"dice:🎳", "DefaultSet_Dice_Bowling"},
	{"dice:🎰", "DefaultSet_Dice_Casino"},
}

// fetchSystem downloads the system sets, the available reactions and the
// message effects into the layout the server seeds from.
func fetchSystem(ctx context.Context, api *tg.Client, dl *downloader.Downloader, outRoot string) error {
	defaultDir := filepath.Join(outRoot, "telegram_default_stickers_export")
	failed := 0
	for _, set := range systemSets {
		fmt.Printf("[system] %s -> %s\n", set.spec, set.dir)
		if err := fetchSetInto(ctx, api, dl, defaultDir, set.spec, set.dir); err != nil {
			failed++
			fmt.Fprintf(os.Stderr, "skip system %s: %v\n", set.spec, err)
		}
	}
	if err := fetchReactions(ctx, api, dl, outRoot); err != nil {
		failed++
		fmt.Fprintf(os.Stderr, "skip reactions: %v\n", err)
	}
	if err := fetchEffects(ctx, api, dl, outRoot); err != nil {
		failed++
		fmt.Fprintf(os.Stderr, "skip effects: %v\n", err)
	}
	fmt.Printf("[system] done, %d failed\n", failed)
	return nil
}

type reactionJSON struct {
	Reaction          string        `json:"reaction"`
	Title             string        `json:"title"`
	Inactive          bool          `json:"inactive"`
	Premium           bool          `json:"premium"`
	StaticIcon        *documentJSON `json:"static_icon,omitempty"`
	AppearAnimation   *documentJSON `json:"appear_animation,omitempty"`
	SelectAnimation   *documentJSON `json:"select_animation,omitempty"`
	ActivateAnimation *documentJSON `json:"activate_animation,omitempty"`
	EffectAnimation   *documentJSON `json:"effect_animation,omitempty"`
	AroundAnimation   *documentJSON `json:"around_animation,omitempty"`
	CenterIcon        *documentJSON `json:"center_icon,omitempty"`
}

func fileExt(mime string) string {
	switch mime {
	case "video/webm":
		return ".webm"
	case "image/webp":
		return ".webp"
	case "video/mp4":
		return ".mp4"
	}
	return ".tgs"
}

// fetchDocumentFile downloads one document into dir as <id><ext> and
// returns its seed JSON.
func fetchDocumentFile(ctx context.Context, api *tg.Client, dl *downloader.Downloader, dir string, class tg.DocumentClass) (*documentJSON, error) {
	doc, ok := class.(*tg.Document)
	if !ok {
		return nil, nil
	}
	path := filepath.Join(dir, fmt.Sprintf("%d%s", doc.ID, fileExt(doc.MimeType)))
	if !completeExistingFile(path, doc.Size) {
		loc := &tg.InputDocumentFileLocation{ID: doc.ID, AccessHash: doc.AccessHash, FileReference: doc.FileReference}
		if err := downloadToFile(ctx, dl, api, loc, dir, path); err != nil {
			return nil, fmt.Errorf("download %d: %w", doc.ID, err)
		}
	}
	return &documentJSON{
		ID: doc.ID, AccessHash: doc.AccessHash, FileReference: hex.EncodeToString(doc.FileReference),
		Date: time.Unix(int64(doc.Date), 0).UTC().Format(time.RFC3339), MimeType: doc.MimeType,
		Size: doc.Size, DCID: doc.DCID, Attributes: mapAttrs(doc.Attributes),
	}, nil
}

func fetchReactions(ctx context.Context, api *tg.Client, dl *downloader.Downloader, outRoot string) error {
	res, err := withFloodWait(ctx, func() (tg.MessagesAvailableReactionsClass, error) {
		return api.MessagesGetAvailableReactions(ctx, 0)
	})
	if err != nil {
		return fmt.Errorf("getAvailableReactions: %w", err)
	}
	list, ok := res.(*tg.MessagesAvailableReactions)
	if !ok {
		return fmt.Errorf("got %T", res)
	}
	root := filepath.Join(outRoot, "telegram_reactions_export")
	dir := filepath.Join(root, "reactions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	out := make([]reactionJSON, 0, len(list.Reactions))
	for i, r := range list.Reactions {
		item := reactionJSON{Reaction: r.Reaction, Title: r.Title, Inactive: r.Inactive, Premium: r.Premium}
		fields := []struct {
			dst **documentJSON
			doc tg.DocumentClass
		}{
			{&item.StaticIcon, r.StaticIcon}, {&item.AppearAnimation, r.AppearAnimation},
			{&item.SelectAnimation, r.SelectAnimation}, {&item.ActivateAnimation, r.ActivateAnimation},
			{&item.EffectAnimation, r.EffectAnimation},
		}
		if v, ok := r.GetAroundAnimation(); ok {
			fields = append(fields, struct {
				dst **documentJSON
				doc tg.DocumentClass
			}{&item.AroundAnimation, v})
		}
		if v, ok := r.GetCenterIcon(); ok {
			fields = append(fields, struct {
				dst **documentJSON
				doc tg.DocumentClass
			}{&item.CenterIcon, v})
		}
		for _, f := range fields {
			if f.doc == nil {
				continue
			}
			doc, err := fetchDocumentFile(ctx, api, dl, dir, f.doc)
			if err != nil {
				return fmt.Errorf("reaction %s: %w", r.Reaction, err)
			}
			*f.dst = doc
		}
		out = append(out, item)
		fmt.Printf("[reactions] %d/%d %s\n", i+1, len(list.Reactions), r.Reaction)
	}
	raw, err := json.MarshalIndent(map[string]any{"api_call": "messages.getAvailableReactions",
		"result": map[string]any{"reactions": out}}, "", "  ")
	if err != nil {
		return err
	}
	jsonDir := filepath.Join(root, "global_json")
	if err := os.MkdirAll(jsonDir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(jsonDir, "available_reactions_raw.json"), append(raw, '\n'), 0o644)
}
