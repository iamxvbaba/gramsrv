package rpc

import (
	"context"
	"encoding/binary"
	"errors"
	"hash/fnv"
	"strings"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"go.uber.org/zap"

	"telesrv/internal/domain"
)

// Layer 232 share themes: messages.setShareTheme decorates the "share
// profile" card of the current user or of a public channel the caller can
// change info in; userFull/channelFull.share_theme carry it to every viewer.

func shareThemeInvalidErr() error { return tgerr.New(400, "SHARE_THEME_INVALID") }

func (r *Router) onMessagesSetShareTheme(ctx context.Context, req *tg.MessagesSetShareThemeRequest) (bool, error) {
	if req == nil || req.Theme == nil || r.deps.ShareThemes == nil {
		return false, internalErr()
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return false, internalErr()
	}
	if r.userIsBot(ctx, userID) {
		return false, botMethodInvalidErr()
	}
	peer, err := r.shareThemePeer(ctx, userID, req.Peer)
	if err != nil {
		return false, err
	}
	theme, err := r.shareThemeFromInput(ctx, userID, peer, req.Theme)
	if err != nil {
		return false, err
	}
	if err := r.deps.ShareThemes.SetShareTheme(ctx, peer, theme); err != nil {
		if errors.Is(err, domain.ErrShareThemeInvalid) {
			return false, shareThemeInvalidErr()
		}
		r.log.Warn("set share theme failed", zap.Int64("user_id", userID), zap.Error(err))
		return false, internalErr()
	}
	if peer.Type == domain.PeerTypeUser {
		r.invalidateRPCProjectionForUser(peer.ID)
	}
	return true, nil
}

// shareThemePeer resolves the target: the caller itself, or a public channel
// in which the caller may change info (TDesktop ShareThemes::CanSave).
func (r *Router) shareThemePeer(ctx context.Context, userID int64, input tg.InputPeerClass) (domain.Peer, error) {
	switch p := input.(type) {
	case *tg.InputPeerSelf:
		return domain.Peer{Type: domain.PeerTypeUser, ID: userID}, nil
	case *tg.InputPeerUser:
		if p.UserID == userID {
			return domain.Peer{Type: domain.PeerTypeUser, ID: userID}, nil
		}
	case *tg.InputPeerChannel:
		_, view, err := r.channelChangeInfoView(ctx, &tg.InputChannel{ChannelID: p.ChannelID, AccessHash: p.AccessHash})
		if err != nil {
			return domain.Peer{}, err
		}
		if strings.TrimSpace(view.Channel.Username) == "" {
			return domain.Peer{}, tgerr.New(400, "CHANNEL_PRIVATE")
		}
		return domain.Peer{Type: domain.PeerTypeChannel, ID: view.Channel.ID}, nil
	}
	return domain.Peer{}, peerIDInvalidErr()
}

func (r *Router) shareThemeFromInput(ctx context.Context, userID int64, peer domain.Peer, input tg.ShareThemeClass) (*domain.ShareTheme, error) {
	switch t := input.(type) {
	case *tg.ShareThemeDefault:
		return nil, nil
	case *tg.ShareThemeEmojiBackdrop:
		theme := &domain.ShareTheme{Kind: domain.ShareThemeEmojiBackdrop}
		if id, ok := t.GetBackdropID(); ok {
			if id <= 0 {
				return nil, shareThemeInvalidErr()
			}
			known, err := r.shareThemeBackdropExists(ctx, id)
			if err != nil {
				return nil, err
			}
			if !known {
				return nil, shareThemeInvalidErr()
			}
			theme.BackdropID = id
		}
		emojis, _ := t.GetEmojis()
		if len(emojis) > domain.MaxShareThemeEmojis {
			return nil, shareThemeInvalidErr()
		}
		for _, reaction := range emojis {
			switch e := reaction.(type) {
			case *tg.ReactionEmoji:
				theme.Emojis = append(theme.Emojis, domain.ShareThemeEmoji{Emoticon: e.Emoticon})
			case *tg.ReactionCustomEmoji:
				theme.Emojis = append(theme.Emojis, domain.ShareThemeEmoji{DocumentID: e.DocumentID})
			default:
				return nil, shareThemeInvalidErr()
			}
		}
		if theme.BackdropID == 0 && len(theme.Emojis) == 0 {
			return nil, nil
		}
		if err := theme.Validate(); err != nil {
			return nil, shareThemeInvalidErr()
		}
		return theme, nil
	case *tg.InputShareThemeGift:
		giftID, err := r.shareThemeGiftID(ctx, userID, peer, t.Stargift)
		if err != nil {
			return nil, err
		}
		return &domain.ShareTheme{Kind: domain.ShareThemeGift, UniqueGiftID: giftID}, nil
	default:
		// shareThemeGift is output-only: clients pick a gift by reference.
		return nil, shareThemeInvalidErr()
	}
}

// shareThemeGiftID requires a unique gift that the themed peer currently owns.
func (r *Router) shareThemeGiftID(ctx context.Context, userID int64, peer domain.Peer, input tg.InputSavedStarGiftClass) (int64, error) {
	if r.deps.Gifts == nil {
		return 0, starGiftInvalidErr()
	}
	ref, ok, err := r.starGiftRefFromInput(ctx, userID, input)
	if err != nil {
		return 0, err
	}
	if !ok || ref.Owner != peer {
		return 0, starGiftInvalidErr()
	}
	saved, found, err := r.deps.Gifts.GetSaved(ctx, ref)
	if err != nil {
		return 0, internalErr()
	}
	if !found || saved.UniqueGiftID <= 0 || saved.Converted ||
		(saved.LifecycleStatus != "" && saved.LifecycleStatus != domain.StarGiftLifecycleActive) {
		return 0, starGiftInvalidErr()
	}
	unique, found, err := r.deps.Gifts.UniqueByID(ctx, saved.UniqueGiftID)
	if err != nil {
		return 0, internalErr()
	}
	if !found || unique.Owner != peer {
		return 0, starGiftInvalidErr()
	}
	return unique.ID, nil
}

func (r *Router) shareThemeBackdropExists(ctx context.Context, backdropID int) (bool, error) {
	backdrops, err := r.deps.ShareThemes.ShareThemeBackdrops(ctx)
	if err != nil {
		return false, internalErr()
	}
	for _, b := range backdrops {
		if b.BackdropID == backdropID {
			return true, nil
		}
	}
	return false, nil
}

func (r *Router) onMessagesGetShareThemeBackdrops(ctx context.Context, req *tg.MessagesGetShareThemeBackdropsRequest) (tg.ShareThemeBackdropsClass, error) {
	if req == nil {
		return nil, inputRequestInvalidErr()
	}
	if _, _, err := r.currentUserID(ctx); err != nil {
		return nil, internalErr()
	}
	if r.deps.ShareThemes == nil {
		return &tg.ShareThemeBackdrops{Hash: 0, Backdrops: []tg.ShareThemeBackdropOption{}}, nil
	}
	backdrops, err := r.deps.ShareThemes.ShareThemeBackdrops(ctx)
	if err != nil {
		return nil, internalErr()
	}
	hash := shareThemeBackdropsHash(backdrops)
	if req.Hash != 0 && req.Hash == hash {
		return &tg.ShareThemeBackdropsNotModified{}, nil
	}
	out := make([]tg.ShareThemeBackdropOption, 0, len(backdrops))
	for _, b := range backdrops {
		out = append(out, tg.ShareThemeBackdropOption{
			BackdropID: b.BackdropID, Name: craftedAttributeDisplayName(b.Name),
			CenterColor: b.CenterColor, EdgeColor: b.EdgeColor,
			PatternColor: b.PatternColor, TextColor: b.TextColor,
		})
	}
	return &tg.ShareThemeBackdrops{Hash: hash, Backdrops: out}, nil
}

func shareThemeBackdropsHash(backdrops []domain.ShareThemeBackdrop) int {
	if len(backdrops) == 0 {
		return 0
	}
	h := fnv.New32a()
	var buf [24]byte
	for _, b := range backdrops {
		binary.LittleEndian.PutUint32(buf[0:], uint32(b.BackdropID))
		binary.LittleEndian.PutUint32(buf[4:], uint32(b.CenterColor))
		binary.LittleEndian.PutUint32(buf[8:], uint32(b.EdgeColor))
		binary.LittleEndian.PutUint32(buf[12:], uint32(b.PatternColor))
		binary.LittleEndian.PutUint32(buf[16:], uint32(b.TextColor))
		binary.LittleEndian.PutUint32(buf[20:], uint32(len(b.Name)))
		_, _ = h.Write(buf[:])
		_, _ = h.Write([]byte(b.Name))
	}
	hash := int(int32(h.Sum32()))
	if hash == 0 {
		hash = 1
	}
	return hash
}

// tgShareTheme projects the stored theme of peer, or nil for the default. A
// gift theme disappears once the peer no longer owns the gift.
func (r *Router) tgShareTheme(ctx context.Context, peer domain.Peer) tg.ShareThemeClass {
	if r.deps.ShareThemes == nil {
		return nil
	}
	theme, found, err := r.deps.ShareThemes.GetShareTheme(ctx, peer)
	if err != nil {
		r.log.Debug("load share theme failed", zap.String("peer_type", string(peer.Type)), zap.Int64("peer_id", peer.ID), zap.Error(err))
		return nil
	}
	if !found {
		return nil
	}
	switch theme.Kind {
	case domain.ShareThemeEmojiBackdrop:
		out := &tg.ShareThemeEmojiBackdrop{}
		if theme.BackdropID > 0 {
			out.SetBackdropID(theme.BackdropID)
		}
		if len(theme.Emojis) > 0 {
			reactions := make([]tg.ReactionClass, 0, len(theme.Emojis))
			for _, e := range theme.Emojis {
				if e.DocumentID > 0 {
					reactions = append(reactions, &tg.ReactionCustomEmoji{DocumentID: e.DocumentID})
				} else {
					reactions = append(reactions, &tg.ReactionEmoji{Emoticon: e.Emoticon})
				}
			}
			out.SetEmojis(reactions)
		}
		return out
	case domain.ShareThemeGift:
		if r.deps.Gifts == nil {
			return nil
		}
		unique, found, err := r.deps.Gifts.UniqueByID(ctx, theme.UniqueGiftID)
		if err != nil || !found || unique.Owner != peer {
			return nil
		}
		return &tg.ShareThemeGift{Stargift: tgUniqueStarGift(unique)}
	}
	return nil
}

func (r *Router) applyShareThemeToUserFull(ctx context.Context, userID int64, full *tg.UserFull) {
	if full == nil {
		return
	}
	if theme := r.tgShareTheme(ctx, domain.Peer{Type: domain.PeerTypeUser, ID: userID}); theme != nil {
		full.SetShareTheme(theme)
	}
}

func (r *Router) applyShareThemeToChannelFull(ctx context.Context, channelID int64, full *tg.ChannelFull) {
	if full == nil {
		return
	}
	if theme := r.tgShareTheme(ctx, domain.Peer{Type: domain.PeerTypeChannel, ID: channelID}); theme != nil {
		full.SetShareTheme(theme)
	}
}
