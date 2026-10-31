package rpc

import (
	"context"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"

	"github.com/iamxvbaba/td/tg"
	"golang.org/x/net/publicsuffix"

	"telesrv/internal/domain"
	"telesrv/internal/links"
)

// urlInTextRe matches a schemed link in raw text (up to the first whitespace or
// angle/quote bracket). Whether it is accepted is decided later by detectURLEntities
// based on http(s) or the current app-link config; an arbitrary foo:// must not be
// promoted to a server-authenticated clickable entity.
var urlInTextRe = regexp.MustCompile(`(?i)[a-z][a-z0-9+.-]*://[^\s<>"'）】]+`)

// bareURLInTextRe matches the bare-domain URLs that official clients recognize locally
// (e.g. github.com/@alice). The leading boundary excludes emails, the interior of an
// already-schemed URL and the middle of a domain; after a candidate match, the TLD is
// still validated by publicsuffix, so ordinary dotted text is not mistakenly promoted
// to a link.
var bareURLInTextRe = regexp.MustCompile(`(?i)(^|[^a-z0-9_@./:+-])((?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.)+(?:[a-z]{2,63}|xn--[a-z0-9-]{2,59})(?::[0-9]{1,5})?(?:[/?#][^\s<>"'）】]*)?)`)

// urlTrailingPunct is sentence-ending punctuation that does not belong to a URL ('/' is
// a valid path ending and is kept).
const urlTrailingPunct = ".,;:!?)]}'\"。，、！？"

type byteSpan struct {
	start  int
	end    int
	scheme string
}

func rawURLByteSpans(message string) []byteSpan {
	if !strings.Contains(message, "://") && !strings.Contains(message, ".") {
		return nil
	}
	var out []byteSpan
	if strings.Contains(message, "://") {
		for _, loc := range urlInTextRe.FindAllStringIndex(message, -1) {
			raw := strings.TrimRight(message[loc[0]:loc[1]], urlTrailingPunct)
			if raw == "" {
				continue
			}
			schemeEnd := strings.Index(raw, "://")
			if schemeEnd <= 0 {
				continue
			}
			out = append(out, byteSpan{
				start:  loc[0],
				end:    loc[0] + len(raw),
				scheme: strings.ToLower(raw[:schemeEnd]),
			})
		}
	}
	if strings.Contains(message, ".") {
		for _, loc := range bareURLInTextRe.FindAllStringSubmatchIndex(message, -1) {
			if len(loc) < 6 || loc[4] < 0 || loc[5] <= loc[4] {
				continue
			}
			start, end := loc[4], loc[5]
			raw := strings.TrimRight(message[start:end], urlTrailingPunct)
			end = start + len(raw)
			if raw == "" || overlapsByteSpan(out, start, end) || !bareURLCandidateValid(raw) {
				continue
			}
			out = append(out, byteSpan{start: start, end: end})
		}
	}
	if len(out) == 0 {
		return nil
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].start == out[j].start {
			return out[i].end < out[j].end
		}
		return out[i].start < out[j].start
	})
	return out
}

func overlapsByteSpan(spans []byteSpan, start, end int) bool {
	for _, span := range spans {
		if start < span.end && span.start < end {
			return true
		}
	}
	return false
}

// detectURLEntities scans the message text server-side to produce url highlight entities
// (MessageEntityURL). A bare-domain URL (github.com/@alice) is accepted following
// official client behavior; for a schemed URL, besides http(s) only the app-link
// scheme/host allowed by the current Router config is accepted. Offset/length are in
// UTF-16 code units (Telegram's entity convention). A custom scheme only participates
// in the entity and does not change the http(s) boundary of the web preview.
func detectURLEntities(message string, appLinks links.AppLinkBuilder) []tg.MessageEntityClass {
	spans := rawURLByteSpans(message)
	if len(spans) == 0 {
		return nil
	}
	out := make([]tg.MessageEntityClass, 0, len(spans))
	for _, span := range spans {
		raw := message[span.start:span.end]
		if span.scheme != "" && span.scheme != "http" && span.scheme != "https" && !appLinks.AcceptsEntityURL(raw) {
			continue
		}
		out = append(out, &tg.MessageEntityURL{
			Offset: utf16CodeUnitLen(message[:span.start]),
			Length: utf16CodeUnitLen(raw),
		})
	}
	return out
}

func bareURLCandidateValid(raw string) bool {
	if raw == "" || strings.Contains(raw, "://") || strings.ContainsAny(raw, " \t\r\n<>\"'") {
		return false
	}
	hostPort := raw
	if cut := strings.IndexAny(hostPort, "/?#"); cut >= 0 {
		hostPort = hostPort[:cut]
	}
	if hostPort == "" || strings.Contains(hostPort, "@") {
		return false
	}
	host := hostPort
	if h, port, ok := splitBareHostPort(hostPort); ok {
		host = h
		n, err := strconv.Atoi(port)
		if err != nil || n <= 0 || n > 65535 {
			return false
		}
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if !bareDomainHostValid(host) {
		return false
	}
	return bareDomainTLDValid(host)
}

func splitBareHostPort(hostPort string) (string, string, bool) {
	idx := strings.LastIndexByte(hostPort, ':')
	if idx < 0 {
		return hostPort, "", false
	}
	return hostPort[:idx], hostPort[idx+1:], true
}

func bareDomainHostValid(host string) bool {
	if host == "" || strings.Contains(host, ":") || strings.HasPrefix(host, ".") || strings.HasSuffix(host, ".") {
		return false
	}
	labels := strings.Split(host, ".")
	if len(labels) < 2 {
		return false
	}
	for _, label := range labels {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for i := 0; i < len(label); i++ {
			c := label[i]
			if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' {
				continue
			}
			return false
		}
	}
	return true
}

func bareDomainTLDValid(host string) bool {
	tld := host[strings.LastIndexByte(host, '.')+1:]
	suffix, icann := publicsuffix.PublicSuffix("x." + tld)
	return icann && suffix == tld
}

// firstPreviewableURL extracts the first previewable link from the message text +
// entities, for the link preview; a bare domain is normalized with https://:
//   - MessageEntityTextURL: the URL is directly in the entity (markdown-style
//     [text](url)).
//   - MessageEntityURL: the URL is a substring of the text at [offset,offset+length)
//     (Telegram entity offsets are counted in UTF-16 code units, so it must be sliced
//     by UTF-16, not by rune/byte).
//   - Fallback: when there is no URL in the entities, scan the raw text. DrKLO sends
//     with url entities, but clients such as TDesktop do not and rely on the server
//     detecting links in the raw text (consistent with official server behavior), so a
//     text scan fallback is required.
//
// Takes the first valid link in appearance order (consistent with the official "preview
// the first link"). Returns ok=false when there is no previewable link.
func firstPreviewableURL(message string, entities []tg.MessageEntityClass) (string, bool) {
	var units []uint16 // lazy: encode the whole text only when a MessageEntityURL is hit (needs UTF-16 offset slicing).
	for _, entity := range entities {
		var candidate string
		switch e := entity.(type) {
		case *tg.MessageEntityTextURL:
			candidate = e.URL // URL is inline, no text slicing needed.
		case *tg.MessageEntityURL:
			if units == nil {
				units = utf16.Encode([]rune(message))
			}
			candidate = sliceUTF16(units, e.Offset, e.Length)
		default:
			continue
		}
		if normalized, ok := normalizePreviewURLCandidate(candidate); ok {
			return normalized, true
		}
	}
	// Fallback scan of the raw text: the vast majority of messages have no link, so when
	// there is no URL trigger character the regex and allocations are skipped outright.
	// firstURLInText further restricts to http(s)/bare domain; a custom app-link never
	// enters the web preview.
	if !strings.Contains(message, "://") && !strings.Contains(message, ".") {
		return "", false
	}
	if raw, ok := firstURLInText(message); ok {
		return raw, true
	}
	return "", false
}

func normalizePreviewURLCandidate(candidate string) (string, bool) {
	candidate = strings.TrimRight(strings.TrimSpace(candidate), urlTrailingPunct)
	if normalized, ok := domain.NormalizeWebPageURL(candidate); ok {
		return normalized, true
	}
	if !bareURLCandidateValid(candidate) {
		return "", false
	}
	return domain.NormalizeWebPageURL("https://" + candidate)
}

// firstURLInText scans the raw text for the first previewable http(s)/bare-domain link,
// stripping trailing punctuation.
func firstURLInText(message string) (string, bool) {
	for _, span := range rawURLByteSpans(message) {
		raw := message[span.start:span.end]
		if normalized, ok := normalizePreviewURLCandidate(raw); ok {
			return normalized, true
		}
	}
	return "", false
}

// sliceUTF16 takes a substring from an encoded sequence by UTF-16 code-unit
// offset/length; out of range returns an empty string.
func sliceUTF16(units []uint16, offset, length int) string {
	if offset < 0 || length <= 0 || offset > len(units) || length > len(units)-offset {
		return ""
	}
	return strings.TrimSpace(string(utf16.Decode(units[offset : offset+length])))
}

// webPagePendingOrCachedMedia constructs the link-preview media for a send:
//   - If the URL is already resolved in cache (typically the client's
//     getWebPagePreview on input already triggered resolution), attach a done card
//     directly - the send echo then carries the card without relying on an async
//     updateWebPage swap (official behavior; TDesktop does not apply that swap to
//     messages it sent, so the echo must carry done directly).
//   - Otherwise attach a pending placeholder, swapped in by the async resolver via
//     updateWebPage once resolved.
//
// Returns nil when previews are disabled or the URL cannot be normalized (the send
// degrades to no preview, without an error).
func (r *Router) webPagePendingOrCachedMedia(ctx context.Context, rawURL string, invertMedia, forceLarge, forceSmall bool) *domain.MessageMedia {
	if page, ok := r.resolveInternalWebPage(ctx, rawURL); ok {
		page.ForceLargeMedia = forceLarge
		page.ForceSmallMedia = forceSmall
		return &domain.MessageMedia{Kind: domain.MessageMediaKindWebPage, InvertMedia: invertMedia, WebPage: &page}
	}
	if r.deps.Files == nil || !r.deps.Files.WebPagePreviewEnabled() {
		return nil
	}
	normalized, ok := normalizePreviewURLCandidate(rawURL)
	if !ok {
		return nil
	}
	if page, found := r.deps.Files.LookupWebPage(ctx, normalized); found {
		if page.State == domain.MessageWebPageStateEmpty {
			return nil
		}
		if page.State == domain.MessageWebPageStateDone {
			page.ForceLargeMedia = forceLarge
			page.ForceSmallMedia = forceSmall
			return &domain.MessageMedia{Kind: domain.MessageMediaKindWebPage, InvertMedia: invertMedia, WebPage: &page}
		}
	}
	return &domain.MessageMedia{
		Kind:        domain.MessageMediaKindWebPage,
		InvertMedia: invertMedia,
		WebPage: &domain.MessageWebPage{
			State: domain.MessageWebPageStatePending,
			ID:    domain.WebPageURLHash(normalized),
			URL:   normalized,
			// date is the absolute deadline for the client to re-fetch the pending
			// message. TDesktop/DrKLO immediately re-fetch when date<=now; if the
			// resolver is still running at that point, TDesktop marks the placeholder as
			// sticky failed, and a later done update will not re-display the card either.
			Date:            int(r.clock.Now().Add(webPagePendingLifetime).Unix()),
			ForceLargeMedia: forceLarge,
			ForceSmallMedia: forceSmall,
		},
	}
}

// webPageMediaFromText constructs the link-preview media for a text send: no_webpage
// suppresses it; otherwise it takes the first previewable URL.
func (r *Router) webPageMediaFromText(ctx context.Context, message string, entities []tg.MessageEntityClass, noWebpage, invertMedia bool) *domain.MessageMedia {
	if noWebpage {
		return nil
	}
	rawURL, ok := firstPreviewableURL(message, entities)
	if !ok {
		return nil
	}
	return r.webPagePendingOrCachedMedia(ctx, rawURL, invertMedia, false, false)
}
