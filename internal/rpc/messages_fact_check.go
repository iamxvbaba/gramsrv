package rpc

import (
	"context"
	"strings"
	"unicode/utf8"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

// tgFactCheck projects a domain fact check into its TL constructor.
//
// When need_check is set, the protocol carries only the hash: country/text are
// lazily loaded by the client via messages.getFactCheck; once a verdict exists,
// the country and text are included directly.
func tgFactCheck(check *domain.FactCheck) *tg.FactCheck {
	if check == nil || check.Hash == 0 {
		return nil
	}
	out := &tg.FactCheck{Hash: check.Hash}
	if check.NeedCheck {
		out.SetNeedCheck(true)
		return out
	}
	if check.Country != "" {
		out.SetCountry(check.Country)
	}
	out.SetText(tgTextWithEntities(check.Text, check.Entities))
	return out
}

// onMessagesEditFactCheck handles messages.editFactCheck: writes a fact-check
// verdict onto a message.
func (r *Router) onMessagesEditFactCheck(ctx context.Context, req *tg.MessagesEditFactCheckRequest) (tg.UpdatesClass, error) {
	if req == nil {
		return nil, inputRequestInvalidErr()
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil || userID == 0 {
		return nil, internalErr()
	}
	peer, err := r.checkedDomainPeerFromInputPeer(ctx, userID, req.Peer)
	if err != nil {
		return nil, err
	}
	// Clients only expose the fact-check entry point in broadcast channels;
	// private chats and groups have no such concept.
	if peer.Type != domain.PeerTypeChannel {
		return nil, peerIDInvalidErr()
	}
	text := req.Text.Text
	if utf8.RuneCountInString(text) > domain.MaxFactCheckTextLength {
		return nil, messageTooLongErr()
	}
	if strings.TrimSpace(text) == "" {
		return nil, messageEmptyErr()
	}
	entities := domainMessageEntitiesForViewer(userID, req.Text.Entities)
	res, err := r.deps.Channels.EditFactCheck(ctx, userID, domain.EditFactCheckRequest{
		UserID:    userID,
		ChannelID: peer.ID,
		MessageID: req.MsgID,
		Text:      text,
		Entities:  entities,
		Date:      int(r.clock.Now().Unix()),
	})
	if err != nil {
		return nil, factCheckErr(err)
	}
	return r.factCheckUpdates(ctx, userID, res), nil
}

// onMessagesDeleteFactCheck handles messages.deleteFactCheck: removes the
// fact-check tag.
func (r *Router) onMessagesDeleteFactCheck(ctx context.Context, req *tg.MessagesDeleteFactCheckRequest) (tg.UpdatesClass, error) {
	if req == nil {
		return nil, inputRequestInvalidErr()
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil || userID == 0 {
		return nil, internalErr()
	}
	peer, err := r.checkedDomainPeerFromInputPeer(ctx, userID, req.Peer)
	if err != nil {
		return nil, err
	}
	if peer.Type != domain.PeerTypeChannel {
		return nil, peerIDInvalidErr()
	}
	res, err := r.deps.Channels.EditFactCheck(ctx, userID, domain.EditFactCheckRequest{
		UserID:    userID,
		ChannelID: peer.ID,
		MessageID: req.MsgID,
		Text:      "",
		Date:      int(r.clock.Now().Unix()),
	})
	if err != nil {
		return nil, factCheckErr(err)
	}
	return r.factCheckUpdates(ctx, userID, res), nil
}

// onMessagesGetFactCheck handles messages.getFactCheck: batch-reads fact-check
// tags.
func (r *Router) onMessagesGetFactCheck(ctx context.Context, req *tg.MessagesGetFactCheckRequest) ([]tg.FactCheck, error) {
	if req == nil || len(req.MsgID) == 0 {
		return nil, inputRequestInvalidErr()
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil || userID == 0 {
		return nil, internalErr()
	}
	peer, err := r.checkedDomainPeerFromInputPeer(ctx, userID, req.Peer)
	if err != nil {
		return nil, err
	}
	if peer.Type != domain.PeerTypeChannel {
		return nil, peerIDInvalidErr()
	}
	list, err := r.deps.Channels.GetFactChecks(ctx, userID, peer.ID, req.MsgID)
	if err != nil {
		return nil, factCheckErr(err)
	}
	out := make([]tg.FactCheck, len(list.FactChecks))
	for i, check := range list.FactChecks {
		if projected := tgFactCheck(&check); projected != nil {
			out[i] = *projected
		}
	}
	return out, nil
}

// factCheckUpdates projects the result of a fact-check write into tg.Updates.
func (r *Router) factCheckUpdates(ctx context.Context, viewerUserID int64, res domain.EditFactCheckResult) *tg.Updates {
	edit := domain.EditChannelMessageResult{
		Channel: res.Channel, Message: res.Message, Event: res.Event, Recipients: res.Recipients,
	}
	updates := r.channelEditMessageUpdates(ctx, viewerUserID, edit)
	r.enqueueChannelEditMessageFanout(ctx, viewerUserID, edit)
	return updates
}
