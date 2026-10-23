package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/iamxvbaba/td/telegram/downloader"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
)

const (
	featuredPageSize = 100
	maxFeaturedPages = 50
	maxFloodWaits    = 200
)

// apiCredentials returns TG_API_ID / TG_API_HASH when both are set, else the
// built-in public pair.
func apiCredentials() (int, string) {
	id, err := strconv.Atoi(strings.TrimSpace(os.Getenv("TG_API_ID")))
	hash := strings.TrimSpace(os.Getenv("TG_API_HASH"))
	if err != nil || id <= 0 || hash == "" {
		return tdesktopAPIID, tdesktopAPIHash
	}
	return id, hash
}

// floodWaitDuration extracts the wait Telegram asked for; the downloader can
// re-wrap the RPC error, so the message text is the fallback.
func floodWaitDuration(err error) (time.Duration, bool) {
	if wait, ok := tgerr.AsFloodWait(err); ok {
		return wait, true
	}
	msg := err.Error()
	i := strings.Index(msg, "FLOOD_WAIT (")
	if i < 0 {
		return 0, false
	}
	rest := msg[i+len("FLOOD_WAIT ("):]
	j := strings.IndexByte(rest, ')')
	if j <= 0 {
		return 0, false
	}
	seconds, convErr := strconv.Atoi(rest[:j])
	if convErr != nil || seconds < 0 {
		return 0, false
	}
	return time.Duration(seconds) * time.Second, true
}

// withFloodWait retries op for as long as Telegram answers FLOOD_WAIT,
// sleeping the requested time plus a margin.
func withFloodWait[T any](ctx context.Context, op func() (T, error)) (T, error) {
	for attempt := 0; ; attempt++ {
		value, err := op()
		if err == nil {
			return value, nil
		}
		wait, ok := floodWaitDuration(err)
		if !ok || attempt >= maxFloodWaits {
			return value, err
		}
		fmt.Printf("[flood wait] %v\n", wait)
		select {
		case <-ctx.Done():
			return value, ctx.Err()
		case <-time.After(wait + 2*time.Second):
		}
	}
}

type featuredSetJSON struct {
	ID        int64  `json:"id"`
	ShortName string `json:"short_name"`
	Title     string `json:"title"`
	Count     int    `json:"count"`
}

// featuredSets lists Telegram's featured sticker sets in Telegram's order:
// the current trending page first, then the archive.
func featuredSets(ctx context.Context, api *tg.Client) ([]featuredSetJSON, error) {
	seen := map[int64]bool{}
	var out []featuredSetJSON
	add := func(res tg.MessagesFeaturedStickersClass) int {
		page, ok := res.(*tg.MessagesFeaturedStickers)
		if !ok {
			return 0
		}
		for _, covered := range page.Sets {
			set := covered.GetSet()
			if seen[set.ID] || set.ShortName == "" {
				continue
			}
			seen[set.ID] = true
			out = append(out, featuredSetJSON{ID: set.ID, ShortName: set.ShortName, Title: set.Title, Count: set.Count})
		}
		return len(page.Sets)
	}
	first, err := withFloodWait(ctx, func() (tg.MessagesFeaturedStickersClass, error) {
		return api.MessagesGetFeaturedStickers(ctx, 0)
	})
	if err != nil {
		return nil, fmt.Errorf("getFeaturedStickers: %w", err)
	}
	add(first)
	for page, offset := 0, 0; page < maxFeaturedPages; page++ {
		res, err := withFloodWait(ctx, func() (tg.MessagesFeaturedStickersClass, error) {
			return api.MessagesGetOldFeaturedStickers(ctx, &tg.MessagesGetOldFeaturedStickersRequest{Offset: offset, Limit: featuredPageSize})
		})
		if tgerr.Is(err, "OFFSET_INVALID") {
			// Telegram's way of saying the archive ends here.
			break
		}
		if err != nil {
			return nil, fmt.Errorf("getOldFeaturedStickers offset %d: %w", offset, err)
		}
		n := add(res)
		if n == 0 {
			break
		}
		offset += n
	}
	return out, nil
}

// featuredEmojiSets lists Telegram's featured custom emoji sets. Unlike
// stickers there is no archive to page through: one call returns them all.
func featuredEmojiSets(ctx context.Context, api *tg.Client) ([]featuredSetJSON, error) {
	res, err := withFloodWait(ctx, func() (tg.MessagesFeaturedStickersClass, error) {
		return api.MessagesGetFeaturedEmojiStickers(ctx, 0)
	})
	if err != nil {
		return nil, fmt.Errorf("getFeaturedEmojiStickers: %w", err)
	}
	page, ok := res.(*tg.MessagesFeaturedStickers)
	if !ok {
		return nil, fmt.Errorf("getFeaturedEmojiStickers: got %T", res)
	}
	seen := map[int64]bool{}
	out := make([]featuredSetJSON, 0, len(page.Sets))
	for _, covered := range page.Sets {
		set := covered.GetSet()
		if seen[set.ID] || set.ShortName == "" {
			continue
		}
		seen[set.ID] = true
		out = append(out, featuredSetJSON{ID: set.ID, ShortName: set.ShortName, Title: set.Title, Count: set.Count})
	}
	return out, nil
}

// fetchFeatured downloads every featured sticker set into outRoot. Sets whose
// set_info.json is already there are skipped, so a rerun resumes.
func fetchFeatured(ctx context.Context, api *tg.Client, dl *downloader.Downloader, outRoot string) error {
	sets, err := featuredSets(ctx, api)
	if err != nil {
		return err
	}
	return fetchSetList(ctx, api, dl, outRoot, "featured", sets)
}

// fetchFeaturedEmoji downloads every featured custom emoji set into outRoot.
func fetchFeaturedEmoji(ctx context.Context, api *tg.Client, dl *downloader.Downloader, outRoot string) error {
	sets, err := featuredEmojiSets(ctx, api)
	if err != nil {
		return err
	}
	return fetchSetList(ctx, api, dl, outRoot, "featured_emoji", sets)
}

func fetchSetList(ctx context.Context, api *tg.Client, dl *downloader.Downloader, outRoot, label string, sets []featuredSetJSON) error {
	if err := os.MkdirAll(outRoot, 0o755); err != nil {
		return err
	}
	list, err := json.MarshalIndent(sets, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(outRoot, label+".json"), append(list, '\n'), 0o644); err != nil {
		return err
	}
	fmt.Printf("[%s] %d sets\n", label, len(sets))
	failed := 0
	for i, set := range sets {
		if _, err := os.Stat(filepath.Join(outRoot, fmt.Sprintf("%s_%d", set.ShortName, set.ID), "set_info.json")); err == nil {
			fmt.Printf("[%s %d/%d] %s already fetched\n", label, i+1, len(sets), set.ShortName)
			continue
		}
		fmt.Printf("[%s %d/%d] %s (%q, %d stickers)\n", label, i+1, len(sets), set.ShortName, set.Title, set.Count)
		if err := fetchSet(ctx, api, dl, outRoot, "short:"+set.ShortName); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			failed++
			fmt.Fprintf(os.Stderr, "skip %s %s: %v\n", label, set.ShortName, err)
		}
	}
	fmt.Printf("[%s] done: %d sets, %d failed\n", label, len(sets), failed)
	return nil
}
