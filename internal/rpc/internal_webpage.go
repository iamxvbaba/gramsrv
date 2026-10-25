package rpc

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"telesrv/internal/branding"
	"telesrv/internal/domain"
)

// Link previews of the server's own public links (https://core.telegram.org/constructor/webPage):
// profiles of users, bots, channels and groups, invite links, collectible
// gifts, gift collections and gift auctions. They are built from the
// server's own data, never fetched over HTTP.
//
// A preview is stored in the message and shown to every recipient, so it
// carries only what the public link page shows to an anonymous visitor: the
// base profile (not the sender's contact name or personal photo), About and
// photo only where privacy allows everybody, and no hidden gifts.

const (
	webPageTypeUser       = "telegram_user"
	webPageTypeBot        = "telegram_bot"
	webPageTypeChannel    = "telegram_channel"
	webPageTypeMegagroup  = "telegram_megagroup"
	webPageTypeNFT        = "telegram_nft"
	webPageTypeCollection = "telegram_collection"
	webPageTypeAuction    = "telegram_auction"
	webPageRequestSuffix  = "_request"
)

var internalLinkUsernameRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{3,31}$`)

type internalLink struct {
	normalized string
	display    string
	kind       string // profile, invite, nft, auction, collection
	username   string
	slug       string
	collection int
}

// resolveInternalWebPage builds the preview of one of the server's own links.
func (r *Router) resolveInternalWebPage(ctx context.Context, rawURL string) (domain.MessageWebPage, bool) {
	if page, ok := r.resolveAIComposeStyleWebPage(ctx, rawURL); ok {
		return page, true
	}
	link, ok := parseInternalLink(rawURL, r.publicLinkHost())
	if !ok {
		return domain.MessageWebPage{}, false
	}
	viewer, ok, err := r.currentUserID(ctx)
	if err != nil || !ok || viewer == 0 {
		return domain.MessageWebPage{}, false
	}
	now := time.Now()
	if r.clock != nil {
		now = r.clock.Now()
	}
	page := domain.MessageWebPage{
		State:      domain.MessageWebPageStateDone,
		ID:         domain.WebPageURLHash(link.normalized),
		URL:        link.normalized,
		DisplayURL: link.display,
		Date:       int(now.Unix()),
		SiteName:   branding.ProductName(),
	}
	switch link.kind {
	case "profile":
		ok = r.fillProfileWebPage(ctx, viewer, link.username, &page)
	case "invite":
		ok = r.fillInviteWebPage(ctx, viewer, link.slug, int(now.Unix()), &page)
	case "nft":
		// v2 keeps resolveUniqueGiftWebPage (gramsrv PR #74) for /nft/<slug>:
		// it adds the rendered preview image. See D12 in docs/main-v2-sync.md.
		return domain.MessageWebPage{}, false
	case "auction":
		ok = r.fillAuctionWebPage(ctx, viewer, link.slug, int(now.Unix()), &page)
	case "collection":
		ok = r.fillCollectionWebPage(ctx, viewer, link.username, link.collection, &page)
	default:
		ok = false
	}
	if !ok {
		return domain.MessageWebPage{}, false
	}
	page.Hash = internalWebPageHash(page)
	return page, true
}

func parseInternalLink(raw, publicHost string) (internalLink, bool) {
	raw = strings.TrimSpace(raw)
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	normalized, ok := domain.NormalizeWebPageURL(raw)
	if !ok {
		return internalLink{}, false
	}
	u, err := url.Parse(normalized)
	if err != nil {
		return internalLink{}, false
	}
	host := strings.ToLower(u.Hostname())
	// Only the configured public link host belongs to this server. t.me and
	// the other AI compose hosts name real Telegram accounts; answering them
	// from local data would show a different account under that name.
	if publicHost == "" || !strings.EqualFold(host, publicHost) {
		return internalLink{}, false
	}
	path := strings.Trim(u.Path, "/")
	if path == "" {
		return internalLink{}, false
	}
	link := internalLink{normalized: normalized, display: host + "/" + path}
	parts := strings.Split(path, "/")
	switch {
	case len(parts) == 1 && strings.HasPrefix(parts[0], "+") && len(parts[0]) > 1:
		if _, err := strconv.ParseUint(parts[0][1:], 10, 64); err == nil {
			return internalLink{}, false // +phone links are not invites
		}
		link.kind, link.slug = "invite", parts[0][1:]
	case len(parts) == 2 && strings.EqualFold(parts[0], "joinchat") && parts[1] != "":
		link.kind, link.slug = "invite", parts[1]
	case len(parts) == 2 && strings.EqualFold(parts[0], "nft") && parts[1] != "":
		link.kind, link.slug = "nft", parts[1]
	case len(parts) == 2 && strings.EqualFold(parts[0], "auction") && parts[1] != "":
		link.kind, link.slug = "auction", parts[1]
	case len(parts) == 3 && parts[1] == "c" && internalLinkUsernameRe.MatchString(parts[0]):
		id, err := strconv.Atoi(parts[2])
		if err != nil || id <= 0 {
			return internalLink{}, false
		}
		link.kind, link.username, link.collection = "collection", parts[0], id
	case len(parts) == 1 && internalLinkUsernameRe.MatchString(parts[0]):
		if c := u.Query().Get("collection"); c != "" {
			id, err := strconv.Atoi(c)
			if err != nil || id <= 0 {
				return internalLink{}, false
			}
			link.kind, link.username, link.collection = "collection", parts[0], id
		} else {
			link.kind, link.username = "profile", parts[0]
		}
	default:
		return internalLink{}, false
	}
	return link, true
}

func (r *Router) fillProfileWebPage(ctx context.Context, viewer int64, username string, page *domain.MessageWebPage) bool {
	u, found, err := r.publicUserByUsername(ctx, viewer, username)
	if err != nil {
		return false
	}
	if found {
		showAbout, showPhoto := u.Bot, u.Bot
		if !u.Bot {
			if showAbout, err = r.publiclyVisible(ctx, u.ID, domain.PrivacyKeyAbout); err != nil {
				return false
			}
			if showPhoto, err = r.publiclyVisible(ctx, u.ID, domain.PrivacyKeyProfilePhoto); err != nil {
				return false
			}
		}
		page.Type = webPageTypeUser
		if u.Bot {
			page.Type = webPageTypeBot
		}
		page.Title = strings.TrimSpace(u.FirstName + " " + u.LastName)
		if showAbout {
			page.Description = u.About
		}
		if showPhoto && u.PhotoID != 0 && r.deps.Files != nil {
			if photo, found, err := r.deps.Files.GetPhoto(ctx, u.PhotoID); err == nil && found {
				page.Photo = &photo
			}
		}
		return page.Title != ""
	}
	if r.deps.Channels == nil {
		return false
	}
	ch, found, err := r.deps.Channels.ResolvePublicUsername(ctx, viewer, username)
	if err != nil || !found || ch.Deleted {
		return false
	}
	r.fillChannelWebPage(ctx, ch, false, page)
	return true
}

func (r *Router) fillInviteWebPage(ctx context.Context, viewer int64, hash string, now int, page *domain.MessageWebPage) bool {
	if r.deps.Channels == nil {
		return false
	}
	res, err := r.deps.Channels.CheckInvite(ctx, viewer, hash, now)
	if err != nil || res.Channel.ID == 0 || res.Channel.Deleted {
		return false
	}
	// The type follows the invite, not whether the sender is already a member:
	// every recipient sees the same stored preview.
	r.fillChannelWebPage(ctx, res.Channel, res.Invite.RequestNeeded, page)
	return true
}

func (r *Router) fillChannelWebPage(ctx context.Context, ch domain.Channel, request bool, page *domain.MessageWebPage) {
	page.Type = webPageTypeChannel
	if ch.Megagroup {
		page.Type = webPageTypeMegagroup
	}
	if request {
		page.Type += webPageRequestSuffix
	}
	page.Title = ch.Title
	page.Description = ch.About
	if ch.PhotoID != 0 && r.deps.Files != nil {
		if photo, found, err := r.deps.Files.GetPhoto(ctx, ch.PhotoID); err == nil && found {
			page.Photo = &photo
		}
	}
}

func (r *Router) fillAuctionWebPage(ctx context.Context, viewer int64, slug string, now int, page *domain.MessageWebPage) bool {
	if r.deps.Gifts == nil {
		return false
	}
	auction, err := r.deps.Gifts.AuctionState(ctx, viewer, 0, slug, now)
	if err != nil || auction.Gift.ID == 0 {
		return false
	}
	gift := auction.Gift
	page.Type = webPageTypeAuction
	page.Title = gift.Title
	page.AuctionGift = &gift
	page.AuctionEndDate = auction.EndDate
	return true
}

// fillCollectionWebPage previews a gift collection with the stickers of its
// first gifts as icons.
func (r *Router) fillCollectionWebPage(ctx context.Context, viewer int64, username string, collectionID int, page *domain.MessageWebPage) bool {
	if r.deps.Gifts == nil {
		return false
	}
	owner := domain.Peer{}
	ownerName := ""
	u, found, err := r.publicUserByUsername(ctx, viewer, username)
	if err != nil {
		return false
	}
	if found {
		owner = domain.Peer{Type: domain.PeerTypeUser, ID: u.ID}
		ownerName = strings.TrimSpace(u.FirstName + " " + u.LastName)
	}
	if owner.ID == 0 && r.deps.Channels != nil {
		if ch, found, err := r.deps.Channels.ResolvePublicUsername(ctx, viewer, username); err == nil && found && !ch.Deleted {
			owner = domain.Peer{Type: domain.PeerTypeChannel, ID: ch.ID}
			ownerName = ch.Title
		}
	}
	if owner.ID == 0 {
		return false
	}
	collections, err := r.deps.Gifts.ListCollections(ctx, owner)
	if err != nil {
		return false
	}
	var collection *domain.StarGiftCollection
	for i := range collections {
		if collections[i].CollectionID == collectionID {
			collection = &collections[i]
			break
		}
	}
	if collection == nil {
		return false
	}
	page.Type = webPageTypeCollection
	page.Title = collection.Title
	page.Description = ownerName
	// Hidden gifts never leave the owner's view (payments.getSavedStarGifts
	// forces exclude_unsaved for everyone else), and this preview is public.
	gifts, err := r.deps.Gifts.ListSavedFiltered(ctx, domain.SavedStarGiftFilter{Owner: owner, CollectionID: collectionID, ExcludeUnsaved: true, Limit: 3})
	if err == nil {
		for _, saved := range gifts.Gifts {
			if doc, ok := r.savedGiftIconDocument(ctx, saved); ok {
				page.CollectionIcons = append(page.CollectionIcons, doc)
			}
		}
	}
	return true
}

// publicUserByUsername resolves a username the way the public link page does.
// ResolveUsername keeps the frozen/deleted tombstone rules, but its result is
// projected for the viewer (contact name, personal photo); the preview uses
// the viewer-independent base user instead.
func (r *Router) publicUserByUsername(ctx context.Context, viewer int64, username string) (domain.User, bool, error) {
	svc, ok := r.deps.Users.(UserIdentityService)
	if !ok {
		return domain.User{}, false, nil
	}
	projected, found, err := svc.ResolveUsername(ctx, viewer, username)
	if errors.Is(err, domain.ErrUsernameInvalid) {
		// Not a username this server can hold, so not a user link.
		return domain.User{}, false, nil
	}
	if err != nil {
		return domain.User{}, false, err
	}
	if !found || projected.Deleted {
		return domain.User{}, false, nil
	}
	base, ok := r.deps.Users.(privateSendBaseUserResolver)
	if !ok {
		return domain.User{}, false, fmt.Errorf("base user resolver unavailable")
	}
	users, err := base.BaseUsersByIDs(ctx, []int64{projected.ID})
	if err != nil {
		return domain.User{}, false, err
	}
	if len(users) != 1 || users[0].ID != projected.ID || users[0].Deleted {
		return domain.User{}, false, nil
	}
	return users[0], true, nil
}

// publiclyVisible evaluates one privacy key for an anonymous viewer, the
// audience of a stored preview. Without an evaluator nothing private is shown.
func (r *Router) publiclyVisible(ctx context.Context, ownerUserID int64, key domain.PrivacyKey) (bool, error) {
	evaluator, ok := r.deps.Privacy.(interface {
		CanSeeAnonymous(ctx context.Context, ownerUserID int64, key domain.PrivacyKey) (bool, error)
	})
	if !ok {
		return false, nil
	}
	return evaluator.CanSeeAnonymous(ctx, ownerUserID, key)
}

func (r *Router) savedGiftIconDocument(ctx context.Context, saved domain.SavedStarGift) (domain.Document, bool) {
	if saved.Unique != nil && saved.Unique.Model.Document != nil {
		return *saved.Unique.Model.Document, true
	}
	if saved.GiftID == 0 {
		return domain.Document{}, false
	}
	gift, found, err := r.deps.Gifts.GiftByID(ctx, saved.GiftID)
	if err != nil || !found || gift.Sticker.ID == 0 {
		return domain.Document{}, false
	}
	return gift.Sticker, true
}

func internalWebPageHash(page domain.MessageWebPage) int {
	h := fnv.New32a()
	_, _ = fmt.Fprintf(h, "%s|%s|%s|%s", page.Type, page.URL, page.Title, page.Description)
	if page.Photo != nil {
		_, _ = fmt.Fprintf(h, "|p%d", page.Photo.ID)
	}
	if page.UniqueGift != nil {
		_, _ = fmt.Fprintf(h, "|u%d|%d", page.UniqueGift.ID, page.UniqueGift.Owner.ID)
	}
	for _, d := range page.CollectionIcons {
		_, _ = fmt.Fprintf(h, "|i%d", d.ID)
	}
	if page.AuctionGift != nil {
		_, _ = fmt.Fprintf(h, "|a%d|%d", page.AuctionGift.ID, page.AuctionEndDate)
	}
	return int(h.Sum32() & 0x7fffffff)
}
