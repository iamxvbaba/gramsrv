package rpc

import (
	"context"
	"fmt"
	"hash/fnv"
	"net/url"
	"strings"
	"time"

	"telesrv/internal/domain"
)

const aiComposeToneWebPageType = "telegram_aicomposetone"

func (r *Router) resolveAIComposeStyleWebPage(ctx context.Context, rawURL string) (domain.MessageWebPage, bool) {
	link, ok := parseAIComposeStyleLink(rawURL, r.publicLinkHost())
	if !ok || r.deps.AICompose == nil {
		return domain.MessageWebPage{}, false
	}
	userID, ok, err := r.currentUserID(ctx)
	if err != nil || !ok {
		return domain.MessageWebPage{}, false
	}
	tones, err := r.deps.AICompose.GetTone(ctx, userID, domain.AIComposeToneRef{
		Kind: domain.AIComposeToneRefSlug,
		Slug: link.slug,
	})
	if err != nil || len(tones.Tones) == 0 {
		return domain.MessageWebPage{}, false
	}
	tone := tones.Tones[0]
	now := time.Now()
	if r.clock != nil {
		now = r.clock.Now()
	}
	page := domain.MessageWebPage{
		State:              domain.MessageWebPageStateDone,
		ID:                 domain.WebPageURLHash(link.normalized),
		URL:                link.normalized,
		DisplayURL:         link.display,
		Hash:               aiComposeToneWebPageHash(tone),
		Date:               int(now.Unix()),
		Type:               aiComposeToneWebPageType,
		SiteName:           "Telegram",
		Title:              tone.Title,
		Description:        tone.Prompt,
		ComposeToneEmojiID: tone.EmojiID,
	}
	if page.Title == "" {
		page.Title = tone.Slug
	}
	if page.Description == "" {
		page.Description = "AI compose style"
	}
	return page, true
}

type aiComposeStyleLink struct {
	normalized string
	display    string
	slug       string
}

func parseAIComposeStyleLink(raw, publicHost string) (aiComposeStyleLink, bool) {
	normalized, ok := domain.NormalizeWebPageURL(raw)
	if !ok {
		return aiComposeStyleLink{}, false
	}
	u, err := url.Parse(normalized)
	if err != nil {
		return aiComposeStyleLink{}, false
	}
	host := strings.ToLower(u.Hostname())
	if !aiComposeStyleHostAllowed(host, publicHost) {
		return aiComposeStyleLink{}, false
	}
	parts := strings.Split(strings.Trim(strings.ToLower(u.EscapedPath()), "/"), "/")
	var slug string
	switch {
	case len(parts) == 1 && parts[0] == "addstyle":
		slug = u.Query().Get("slug")
	case len(parts) == 2 && parts[0] == "addstyle":
		if decoded, err := url.PathUnescape(parts[1]); err == nil {
			slug = decoded
		}
	}
	slug = strings.ToLower(strings.TrimSpace(slug))
	if slug == "" {
		return aiComposeStyleLink{}, false
	}
	display := host
	if path := strings.Trim(u.EscapedPath(), "/"); path != "" {
		display += "/" + path
	}
	return aiComposeStyleLink{normalized: normalized, display: display, slug: slug}, true
}

func aiComposeStyleHostAllowed(host, publicHost string) bool {
	if publicHost != "" && strings.EqualFold(host, publicHost) {
		return true
	}
	switch host {
	case "t.me", "telegram.me", "telesrv.net", "localhost", "127.0.0.1":
		return true
	default:
		return false
	}
}

func aiComposeToneWebPageHash(tone domain.AIComposeTone) int {
	h := fnv.New32a()
	_, _ = fmt.Fprintf(h, "%s|%s|%d|%s|%d", tone.Slug, tone.Title, tone.EmojiID, tone.Prompt, tone.UpdatedAt)
	return int(h.Sum32() & 0x7fffffff)
}
