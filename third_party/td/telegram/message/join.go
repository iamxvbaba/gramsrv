package message

import (
	"context"

	"github.com/go-faster/errors"

	"github.com/iamxvbaba/td/telegram/deeplink"
	"github.com/iamxvbaba/td/telegram/message/peer"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
)

// JoinLink joins to private chat using given link or hash.
// Input examples:
//
//	sgq.me/+AAAAAAAAAAAAAAAA
//	https://sgq.me/+AAAAAAAAAAAAAAAA
//	sgq.me/joinchat/AAAAAAAAAAAAAAAA
//	https://sgq.me/joinchat/AAAAAAAAAAAAAAAA
//	sg:join?invite=AAAAAAAAAAAAAAAA
//	sg://join?invite=AAAAAAAAAAAAAAAA
func (s *Sender) JoinLink(ctx context.Context, link string) (tg.UpdatesClass, error) {
	hash := link
	if deeplink.IsDeeplinkLike(link) {
		l, err := deeplink.Expect(link, deeplink.Join)
		if err != nil {
			return nil, err
		}

		hash = l.Args.Get("invite")
	}

	return s.JoinHash(ctx, hash)
}

// JoinHash joins to private chat using given hash.
func (s *Sender) JoinHash(ctx context.Context, hash string) (tg.UpdatesClass, error) {
	return s.importChatInvite(ctx, hash)
}

// Join joins resolved channel.
// NB: if resolved peer is not a channel, error will be returned.
func (b *RequestBuilder) Join(ctx context.Context) (tg.UpdatesClass, error) {
	p, err := b.peer(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "peer")
	}

	input, ok := peer.ToInputChannel(p)
	if !ok {
		return nil, errors.Errorf("unexpected type %T", p)
	}

	return b.sender.joinChannel(ctx, input)
}

func (b *RequestBuilder) leave(ctx context.Context, revoke bool) (tg.UpdatesClass, error) {
	p, err := b.peer(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "peer")
	}

	input, ok := peer.ToInputChannel(p)
	if ok {
		r, err := b.sender.leaveChannel(ctx, input)
		if err != nil {
			return nil, errors.Wrap(err, "leave channel")
		}
		return r, nil
	}

	chat, ok := p.(*tg.InputPeerChat)
	if !ok {
		return &tg.Updates{}, nil
	}

	r, err := b.sender.deleteChatUser(ctx, &tg.MessagesDeleteChatUserRequest{
		RevokeHistory: revoke,
		ChatID:        chat.ChatID,
		UserID:        &tg.InputUserSelf{},
	})
	if err != nil {
		// Happens if chat was deactivated.
		if tgerr.Is(err, tg.ErrPeerIDInvalid) {
			return &tg.Updates{}, nil
		}
		return nil, errors.Wrap(err, "leave chat")
	}
	return r, nil
}

// Leave leaves resolved peer.
//
// NB: if resolved peer is not a channel or chat, or chat is deactivated, empty *tg.Updates will be returned.
func (b *RequestBuilder) Leave(ctx context.Context) (tg.UpdatesClass, error) {
	return b.leave(ctx, false)
}
