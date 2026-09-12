package rpc

import (
	"context"
	"errors"
	"strings"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
	"telesrv/internal/seed/freeze"
)

// Third-party bot verification on the protocol edge
// (core.telegram.org/api/bots/verification).
//
// Layer 228 spreads one fact -- "verifier bot B marked peer P with icon I and
// description D" -- over six unrelated constructors, and an official client only
// renders the badge when the exact bit is set:
//
//	user#b1b8cc83        bot_verification_icon:flags2.14?long
//	channel#d49f34c6     bot_verification_icon:flags2.13?long
//	userFull#6cbe645     bot_verification:flags2.12?BotVerification
//	channelFull#a04e8d3a bot_verification:flags2.17?BotVerification
//	chatInvite#5c9d3702  bot_verification:flags.13?BotVerification
//	botInfo#4d8a0299     verifier_settings:flags.9?BotVerifierSettings
//
// Every projection here goes through the generated Set* helpers rather than a raw
// field assignment, because the flags word is what the client reads: a struct field
// set without its bit encodes as an absent field and the badge silently disappears.
//
// Deps.BotVerifications is the single source. Nothing in this file recomputes the
// mark, and nothing derives it from the official verified flag (user flags.17 /
// channel flags.7) -- that is a separate operator-granted mechanism, and mixing the
// two would let a third-party verifier mint platform checkmarks.

// accountFrozenMarkIcon is the custom emoji document the synthetic "frozen"
// third-party mark renders with (user#b1b8cc83 bot_verification_icon:flags2.14,
// userFull#6cbe645 bot_verification:flags2.12). It must name a real custom emoji
// document in the operator catalogue -- clients resolve it through
// messages.getCustomEmojiDocuments, so an id without a concrete document decodes
// to an invisible badge. The icon is bundled straight into the binary by
// SeedFreezeEmoji (package telesrv/internal/seed/freeze, "freeze" custom emoji
// set), so every server carries the resolvable document even without an on-disk
// sticker seed directory. accountFrozenMarkBotID is the reserved verifierbot
// identity used as the mark's issuer; the mark itself is a viewer-scoped
// synthetic projection derived from the freeze state, never a custom_verifications
// row, so it disappears the moment the account is unfrozen.
const (
	accountFrozenMarkIcon        int64 = freeze.DocumentID
	accountFrozenMarkBotID       int64 = domain.VerifierBotUserID
	accountFrozenMarkDescription       = "The account was frozen"
)

// tgBotVerification projects domain.BotVerification onto botVerification#f93cd45c.
//
// The bool reports whether the payload is renderable at all: the icon is a custom
// emoji document id resolved through messages.getCustomEmojiDocuments, so a zero
// icon (or an unattributed mark) would encode a badge the client draws as nothing.
// Such a mark is omitted instead of shipped half-formed.
func tgBotVerification(in domain.BotVerification) (tg.BotVerification, bool) {
	if in.BotID <= 0 || in.Icon <= 0 {
		return tg.BotVerification{}, false
	}
	return tg.BotVerification{
		BotID:       in.BotID,
		Icon:        in.Icon,
		Description: in.Description,
	}, true
}

// tgBotVerifierSettings projects domain.BotVerifierSettings onto
// botVerifierSettings#b0cd6617.
//
// custom_description:flags.0 carries the operator default description (the text the
// verifier applies when it supplies none per peer);
// can_modify_custom_description:flags.1 is the permission that lets the verifier
// override it. A configuration that does not validate is omitted entirely rather
// than encoded with an empty company, which clients render as a blank badge sheet.
func tgBotVerifierSettings(in domain.BotVerifierSettings) (tg.BotVerifierSettings, bool) {
	if err := in.Validate(); err != nil {
		return tg.BotVerifierSettings{}, false
	}
	out := tg.BotVerifierSettings{
		Icon:    in.IconDocumentID,
		Company: strings.TrimSpace(in.CompanyName),
	}
	if in.CanModifyCustomDescription {
		out.SetCanModifyCustomDescription(true)
	}
	if desc := strings.TrimSpace(in.DefaultDescription); desc != "" {
		out.SetCustomDescription(desc)
	}
	return out, true
}

// applyBotVerificationIconsToPeerObjects overlays user#b1b8cc83
// bot_verification_icon:flags2.14 and channel#d49f34c6
// bot_verification_icon:flags2.13 onto already-projected peer objects.
//
// It mirrors applyUsernamesToPeerObjects exactly, including why it is an overlay:
// tgUser/tgChannel run inside per-id loops (users.getUsers, dialog lists), so a
// per-object read there would be the N+1 this pass exists to avoid. Running at the
// response boundary keeps every one of the ~90 plain projection call sites
// untouched and still reaches them all.
//
// A nil service or any read error is a silent no-op: the encoded peer then stays
// byte-identical to the pre-feature shape.
func (r *Router) applyBotVerificationIconsToPeerObjects(ctx context.Context, users []tg.UserClass, chats []tg.ChatClass) {
	if r.deps.BotVerifications == nil || len(users)+len(chats) == 0 {
		return
	}
	peers := make([]domain.Peer, 0, len(users)+len(chats))
	seen := make(map[domain.Peer]struct{}, len(users)+len(chats))
	addPeer := func(peer domain.Peer) {
		if peer.ID == 0 {
			return
		}
		if _, ok := seen[peer]; ok {
			return
		}
		seen[peer] = struct{}{}
		peers = append(peers, peer)
	}
	for _, item := range users {
		if u, ok := item.(*tg.User); ok && u != nil && !u.Deleted {
			addPeer(domain.Peer{Type: domain.PeerTypeUser, ID: u.ID})
		}
	}
	for _, item := range chats {
		if ch, ok := item.(*tg.Channel); ok && ch != nil {
			addPeer(domain.Peer{Type: domain.PeerTypeChannel, ID: ch.ID})
		}
	}
	if len(peers) == 0 {
		return
	}
	byPeer := r.botVerificationMap(ctx, peers)
	if len(byPeer) == 0 {
		return
	}
	applyBotVerificationIconsFromMap(users, chats, byPeer)
}

func applyBotVerificationIconsFromMap(users []tg.UserClass, chats []tg.ChatClass, byPeer map[domain.Peer]domain.CustomVerification) {
	for _, item := range users {
		u, ok := item.(*tg.User)
		if !ok || u == nil || u.Deleted {
			continue
		}
		mark, ok := byPeer[domain.Peer{Type: domain.PeerTypeUser, ID: u.ID}]
		if !ok || mark.IconDocumentID <= 0 {
			continue
		}
		u.SetBotVerificationIcon(mark.IconDocumentID)
	}
	for _, item := range chats {
		ch, ok := item.(*tg.Channel)
		if !ok || ch == nil {
			continue
		}
		mark, ok := byPeer[domain.Peer{Type: domain.PeerTypeChannel, ID: ch.ID}]
		if !ok || mark.IconDocumentID <= 0 {
			continue
		}
		ch.SetBotVerificationIcon(mark.IconDocumentID)
	}
}

// botVerificationMap loads the marks for the given peers. One peer goes through
// PeerVerification so a single-object projection does not pay for a batch round
// trip; anything larger goes through PeerVerificationBatch, so a list response
// costs one query regardless of length. Any error yields an empty map, which every
// caller treats as "no badge".
func (r *Router) botVerificationMap(ctx context.Context, peers []domain.Peer) map[domain.Peer]domain.CustomVerification {
	if r.deps.BotVerifications == nil || len(peers) == 0 {
		return nil
	}
	_, verifications := r.peerIdentityMaps(ctx, peers, false, true)
	return verifications
}

func (r *Router) loadBotVerificationMap(ctx context.Context, peers []domain.Peer) (map[domain.Peer]domain.CustomVerification, error) {
	if len(peers) == 1 {
		mark, err := r.deps.BotVerifications.PeerVerification(ctx, peers[0])
		if err != nil {
			if errors.Is(err, domain.ErrCustomVerificationNotFound) {
				return map[domain.Peer]domain.CustomVerification{}, nil
			}
			return nil, err
		}
		if mark.IconDocumentID <= 0 {
			return map[domain.Peer]domain.CustomVerification{}, nil
		}
		return map[domain.Peer]domain.CustomVerification{peers[0]: mark}, nil
	}
	byPeer, err := r.deps.BotVerifications.PeerVerificationBatch(ctx, peers)
	if err != nil {
		return nil, err
	}
	return byPeer, nil
}

// peerBotVerificationIcon resolves just the icon for one peer, for the update
// fan-outs. They build one peer object per recipient from the same peer-wide fact,
// so they resolve it once with this and then stamp it with the helpers below rather
// than reading inside their per-recipient builder. Zero means "no mark", which
// leaves the flag unset.
func (r *Router) peerBotVerificationIcon(ctx context.Context, peer domain.Peer) int64 {
	if r.deps.BotVerifications == nil || peer.ID <= 0 {
		return 0
	}
	mark, err := r.deps.BotVerifications.PeerVerification(ctx, peer)
	if err != nil {
		return 0
	}
	return mark.IconDocumentID
}

// applyBotVerificationIconToUsers stamps an already-resolved icon onto the matching
// user object of one push (user#b1b8cc83 bot_verification_icon:flags2.14).
func applyBotVerificationIconToUsers(users []tg.UserClass, userID, icon int64) {
	if icon <= 0 || userID <= 0 {
		return
	}
	for _, item := range users {
		if u, ok := item.(*tg.User); ok && u != nil && !u.Deleted && u.ID == userID {
			u.SetBotVerificationIcon(icon)
		}
	}
}

// applyBotVerificationIconToChannelChats is the channel counterpart
// (channel#d49f34c6 bot_verification_icon:flags2.13).
func applyBotVerificationIconToChannelChats(chats []tg.ChatClass, channelID, icon int64) {
	if icon <= 0 || channelID <= 0 {
		return
	}
	for _, item := range chats {
		if ch, ok := item.(*tg.Channel); ok && ch != nil && ch.ID == channelID {
			ch.SetBotVerificationIcon(icon)
		}
	}
}

// peerBotVerification resolves the single-peer projection payload.
func (r *Router) peerBotVerification(ctx context.Context, peer domain.Peer) (tg.BotVerification, bool) {
	if r.deps.BotVerifications == nil || peer.ID <= 0 {
		return tg.BotVerification{}, false
	}
	mark, err := r.deps.BotVerifications.PeerVerification(ctx, peer)
	if err != nil {
		// Includes domain.ErrCustomVerificationNotFound: an unmarked peer simply has
		// no badge to show.
		return tg.BotVerification{}, false
	}
	return tgBotVerification(mark.Projection())
}

// applyBotVerificationToUserFull sets userFull#6cbe645
// bot_verification:flags2.12.
//
// It runs as a post-cache overlay on both users.getFullUser paths (cache hit and
// fresh build) and clears the bit first, so a revoked mark disappears on the next
// response instead of surviving inside the per-(viewer,target) projection cache
// TTL. The payload is viewer-independent by construction: a badge granted by a
// verifier is a property of the peer, not of who is looking.
func (r *Router) applyBotVerificationToUserFull(ctx context.Context, userID int64, full *tg.UserFull) {
	if r.deps.BotVerifications == nil || full == nil || userID <= 0 {
		return
	}
	full.Flags2.Unset(12)
	full.BotVerification = tg.BotVerification{}
	if value, ok := r.peerBotVerification(ctx, domain.Peer{Type: domain.PeerTypeUser, ID: userID}); ok {
		full.SetBotVerification(value)
	}
}

// applyFrozenUserFullMark sets userFull#6cbe645 bot_verification:flags2.12 to the
// synthetic "frozen" mark. It is only applied to a peer tombstone projected from
// an account freeze (see applyAccountFreezeProjection): the mark carries the
// operator catalogue icon and the "The account was frozen" description next to
// the deleted-account presentation.
func applyFrozenUserFullMark(full *tg.UserFull, u domain.User) {
	if full == nil || !u.FrozenForViewer {
		return
	}
	full.Flags2.Unset(12)
	full.BotVerification = tg.BotVerification{}
	if value, ok := tgBotVerification(domain.BotVerification{
		BotID:       accountFrozenMarkBotID,
		Icon:        accountFrozenMarkIcon,
		Description: accountFrozenMarkDescription,
	}); ok {
		full.SetBotVerification(value)
	}
}

// applyBotVerificationToChannelFull sets channelFull#a04e8d3a
// bot_verification:flags2.17, on the same post-cache overlay contract as
// applyBotVerificationToUserFull.
func (r *Router) applyBotVerificationToChannelFull(ctx context.Context, channelID int64, full *tg.ChannelFull) {
	if r.deps.BotVerifications == nil || full == nil || channelID <= 0 {
		return
	}
	full.Flags2.Unset(17)
	full.BotVerification = tg.BotVerification{}
	if value, ok := r.peerBotVerification(ctx, domain.Peer{Type: domain.PeerTypeChannel, ID: channelID}); ok {
		full.SetBotVerification(value)
	}
}

// applyBotVerificationToChatInvite sets chatInvite#5c9d3702
// bot_verification:flags.13.
//
// A non-member sees only this preview, so the badge has to be visible here for the
// same reason verified/scam/fake are (applyChatInviteModerationFlags): a mark that
// appears only after joining is exactly backwards.
func (r *Router) applyBotVerificationToChatInvite(ctx context.Context, invite *tg.ChatInvite, channelID int64) {
	if r.deps.BotVerifications == nil || invite == nil || channelID <= 0 {
		return
	}
	if value, ok := r.peerBotVerification(ctx, domain.Peer{Type: domain.PeerTypeChannel, ID: channelID}); ok {
		invite.SetBotVerification(value)
	}
}

// applyVerifierSettingsToBotInfo sets botInfo#4d8a0299
// verifier_settings:flags.9 -- the block a client shows inside a verifier bot's
// own profile, and what tells it the bot may verify others.
//
// The operator kill switch is honoured here: a disabled verifier keeps its row and
// the marks it already granted, but stops advertising itself as a verifier, so the
// client stops offering the verification UI.
func applyVerifierSettingsToBotInfo(info *tg.BotInfo, botUserID int64, settings domain.BotVerifierSettings) {
	if info == nil || botUserID <= 0 || !settings.Enabled || settings.BotID != botUserID {
		return
	}
	if value, ok := tgBotVerifierSettings(settings); ok {
		info.SetVerifierSettings(value)
	}
}

// applyVerifierSettingsToOneBotInfo is the single-bot path (userFull.bot_info).
func (r *Router) applyVerifierSettingsToOneBotInfo(ctx context.Context, info *tg.BotInfo, botUserID int64) {
	if r.deps.BotVerifications == nil || info == nil || botUserID <= 0 {
		return
	}
	settings, err := r.deps.BotVerifications.VerifierSettings(ctx, botUserID)
	if err != nil {
		// Includes domain.ErrVerifierNotFound: an ordinary bot is not a verifier.
		return
	}
	applyVerifierSettingsToBotInfo(info, botUserID, settings)
}

// verifierSettingsBatch resolves verifier status for several bots at once, next to
// the profile batch resolver tgBotInfos already uses, so channelFull.bot_info costs
// one settings query for the whole bot list.
func (r *Router) verifierSettingsBatch(ctx context.Context, botUserIDs []int64) map[int64]domain.BotVerifierSettings {
	if r.deps.BotVerifications == nil || len(botUserIDs) == 0 {
		return nil
	}
	settings, err := r.deps.BotVerifications.VerifierSettingsBatch(ctx, botUserIDs)
	if err != nil {
		return nil
	}
	return settings
}
