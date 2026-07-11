package rpc

import (
	"context"
	"encoding/binary"
	"net/url"

	"github.com/gotd/td/tg"
	"go.uber.org/zap"

	"telesrv/internal/domain"
	"telesrv/internal/sfu"
)

const (
	maxConferenceChainBlockBytes = 64 * 1024
	maxConferenceChainBlocks     = 100

	conferenceChainBlockConstructor            = 0x639a3db6
	conferenceChainBlockServerConstructor      = 0x639a3db7
	conferenceBroadcastCommitConstructor       = 0xd1512ae7
	conferenceBroadcastCommitServerConstructor = 0xd1512ae8
	conferenceBroadcastRevealConstructor       = 0x83f4f9d8
	conferenceBroadcastRevealServerConstructor = 0x83f4f9d9
)

func (r *Router) onPhoneCreateConferenceCall(ctx context.Context, req *tg.PhoneCreateConferenceCallRequest) (tg.UpdatesClass, error) {
	if req == nil {
		return nil, inputRequestInvalidErr()
	}
	if r.deps.GroupCalls == nil {
		return nil, notImplementedErr()
	}
	userID, err := r.phoneRequireUser(ctx)
	if err != nil {
		return nil, err
	}
	now := int(r.clock.Now().Unix())
	call, err := r.deps.GroupCalls.CreateConference(ctx, userID, int64(req.RandomID), 0, now)
	if err != nil {
		return nil, groupCallErr(err)
	}
	out := r.groupCallUpdateContainer(ctx, userID, domain.Channel{},
		&tg.UpdateGroupCall{Call: tgGroupCall(call, userID, true, r.cfg.PublicBaseURL)}, []int64{userID})
	if !req.Join {
		return out, nil
	}
	params, ok := req.GetParams()
	if !ok {
		return nil, groupCallInvalidErr()
	}
	joinReq := &tg.PhoneJoinGroupCallRequest{
		Muted:        req.Muted,
		VideoStopped: req.VideoStopped,
		Call:         &tg.InputGroupCall{ID: call.ID, AccessHash: call.AccessHash},
		JoinAs:       &tg.InputPeerSelf{},
		Params:       params,
	}
	if pk, ok := req.GetPublicKey(); ok {
		joinReq.SetPublicKey(pk)
	}
	if block, ok := req.GetBlock(); ok {
		joinReq.SetBlock(block)
	}
	joinUpdates, err := r.onPhoneJoinGroupCall(ctx, joinReq)
	if err != nil {
		return nil, err
	}
	appendUpdates(out, joinUpdates)
	return out, nil
}

func (r *Router) onPhoneExportGroupCallInvite(ctx context.Context, req *tg.PhoneExportGroupCallInviteRequest) (*tg.PhoneExportedGroupCallInvite, error) {
	if req == nil {
		return nil, inputRequestInvalidErr()
	}
	scope, err := r.groupCallScopeFrom(ctx, req.Call)
	if err != nil {
		return nil, err
	}
	if !scope.call.Active() {
		return nil, groupCallInvalidErr()
	}
	if scope.call.Conference() {
		link := conferenceExportInviteLink(scope.call, r.cfg.PublicBaseURL)
		if link == "" {
			return nil, groupCallInvalidErr()
		}
		return &tg.PhoneExportedGroupCallInvite{Link: link}, nil
	}
	if scope.call.InviteLink != "" {
		return &tg.PhoneExportedGroupCallInvite{Link: scope.call.InviteLink}, nil
	}
	if scope.channel.Username == "" {
		return nil, publicChannelMissingErr()
	}
	return &tg.PhoneExportedGroupCallInvite{Link: r.publicLink(scope.channel.Username)}, nil
}

func conferenceExportInviteLink(call domain.GroupCall, publicBaseURL ...string) string {
	if link := conferenceCanonicalInviteLink(call.InviteSlug, publicBaseURL...); link != "" {
		return link
	}
	return call.InviteLink
}

func conferenceCanonicalInviteLink(slug string, publicBaseURL ...string) string {
	if slug == "" {
		return ""
	}
	baseURL := ""
	if len(publicBaseURL) > 0 {
		baseURL = publicBaseURL[0]
	}
	return publicLinkQueryWithBaseURL(baseURL, "call/"+slug, url.Values{"slug": []string{slug}})
}

func (r *Router) onPhoneInviteConferenceCallParticipant(ctx context.Context, req *tg.PhoneInviteConferenceCallParticipantRequest) (tg.UpdatesClass, error) {
	if req == nil {
		return nil, inputRequestInvalidErr()
	}
	if r.deps.GroupCalls == nil || r.deps.Messages == nil {
		return nil, notImplementedErr()
	}
	scope, err := r.groupCallScopeFrom(ctx, req.Call)
	if err != nil {
		return nil, err
	}
	if !scope.call.Conference() || !scope.call.Active() {
		return nil, groupCallInvalidErr()
	}
	target, found, err := r.userFromInput(ctx, scope.userID, req.UserID)
	if err != nil {
		return nil, internalErr()
	}
	if !found || target.ID == 0 || target.Bot || target.ID == scope.userID {
		return nil, userIDInvalidErr()
	}
	if p, found, err := r.deps.GroupCalls.Participant(ctx, scope.call.ID, target.ID); err != nil {
		return nil, internalErr()
	} else if found && !p.Left {
		return nil, tgerr400("USER_ALREADY_PARTICIPANT")
	}
	recipientBlocked, err := r.peerBlocksUser(ctx, scope.userID, target.ID)
	if err != nil {
		return nil, err
	}
	now := int(r.clock.Now().Unix())
	randomID := conferenceInviteRandomID(scope.call.ID, target.ID, now)
	res, err := r.deps.Messages.SendPrivateText(ctx, scope.userID, domain.SendPrivateTextRequest{
		SenderUserID:    scope.userID,
		RecipientUserID: target.ID,
		RandomID:        randomID,
		Media: &domain.MessageMedia{
			Kind: domain.MessageMediaKindService,
			ServiceAction: &domain.MessageServiceAction{
				Kind: domain.MessageServiceActionConferenceCall,
				ConferenceCall: &domain.MessageConferenceCallAction{
					CallID: scope.call.ID,
					Video:  req.Video,
					OtherParticipants: []domain.Peer{
						{Type: domain.PeerTypeUser, ID: scope.userID},
					},
				},
			},
		},
		Date:             now,
		OriginAuthKeyID:  rawAuthKeyIDForOrigin(ctx),
		OriginSessionID:  sessionIDFromCtx(ctx),
		RecipientBlocked: recipientBlocked,
	})
	if err != nil {
		return nil, messageSendErr(err)
	}
	// Message and invite index currently live in separate stores/transactions. Always
	// retry the idempotent invite write, including an exact message replay, so a crash
	// after SendPrivateText commit cannot leave an unrecoverable message-without-index.
	invite, err := r.deps.GroupCalls.CreateConferenceInvite(ctx, domain.GroupCallInvite{
		CallID:        scope.call.ID,
		InviterUserID: scope.userID,
		InviteeUserID: target.ID,
		MessageID:     res.RecipientMessage.ID,
		Status:        domain.GroupCallInvitePending,
		Video:         req.Video,
		CreatedAt:     now,
	})
	if err != nil {
		return nil, groupCallErr(err)
	}
	_ = invite
	if res.Duplicate {
		// The first transaction already created both private boxes and their durable
		// update events. Replaying UpdateNewMessage here would reconstruct the service
		// message from the intentionally minimal immutable receipt and push it to the
		// invitee a second time. Only reconcile the caller's random_id and original pts;
		// The invite write above is an idempotent saga repair only; no message/update
		// side effect is repeated.
		return tgPrivateSendResultUpdates(res, randomID, true, nil, nil), nil
	}
	users := r.tgUsersForIDs(ctx, scope.userID, []int64{scope.userID, target.ID})
	out := tgPrivateMessageUpdates(res.SenderEvent, res.SenderMessage, 0, false, users, nil)
	recipientUsers := r.tgUsersForIDs(ctx, target.ID, []int64{scope.userID, target.ID})
	r.pushUserMessage(ctx, target.ID, "conference invite",
		tgPrivateMessageUpdates(res.RecipientEvent, res.RecipientMessage, 0, false, recipientUsers, nil))
	return out, nil
}

func (r *Router) onPhoneDeclineConferenceCallInvite(ctx context.Context, msgID int) (tg.UpdatesClass, error) {
	if r.deps.GroupCalls == nil {
		return nil, notImplementedErr()
	}
	userID, err := r.phoneRequireUser(ctx)
	if err != nil {
		return nil, err
	}
	call, inv, found, err := r.deps.GroupCalls.GetByInviteMessage(ctx, userID, msgID)
	if err != nil {
		return nil, internalErr()
	}
	if !found || !call.Conference() {
		return nil, msgIDInvalidErr()
	}
	now := int(r.clock.Now().Unix())
	if _, _, err := r.deps.GroupCalls.SetConferenceInviteStatus(ctx, call.ID, userID, msgID, domain.GroupCallInviteDeclined, now); err != nil {
		return nil, internalErr()
	}
	r.pushConferenceGroupCallUpdate(ctx, call)
	return r.groupCallUpdateContainer(ctx, userID, domain.Channel{},
		&tg.UpdateGroupCall{Call: tgGroupCall(call, userID, userID == call.CreatorUserID, r.cfg.PublicBaseURL)}, []int64{inv.InviterUserID, inv.InviteeUserID}), nil
}

func (r *Router) onPhoneDeleteConferenceCallParticipants(ctx context.Context, req *tg.PhoneDeleteConferenceCallParticipantsRequest) (tg.UpdatesClass, error) {
	if req == nil {
		return nil, inputRequestInvalidErr()
	}
	if len(req.IDs) == 0 || len(req.IDs) > 100 {
		return nil, limitInvalidErr()
	}
	if len(req.Block) > maxConferenceChainBlockBytes {
		return nil, limitInvalidErr()
	}
	scope, err := r.groupCallScopeFrom(ctx, req.Call)
	if err != nil {
		return nil, err
	}
	if !scope.call.Conference() {
		return nil, groupCallInvalidErr()
	}
	if req.OnlyLeft == req.Kick {
		return nil, inputRequestInvalidErr()
	}
	now := int(r.clock.Now().Unix())
	if req.OnlyLeft && !scope.canManage() {
		self, found, err := r.deps.GroupCalls.Participant(ctx, scope.call.ID, scope.userID)
		if err != nil {
			return nil, internalErr()
		}
		if !found || self.Left {
			return nil, groupCallForbiddenErr()
		}
	}
	for _, targetID := range req.IDs {
		if targetID <= 0 {
			return nil, userIDInvalidErr()
		}
		if req.Kick && targetID != scope.userID && !scope.canManage() {
			return nil, groupCallForbiddenErr()
		}
	}
	result, err := r.deps.GroupCalls.RemoveConferenceParticipants(ctx, domain.RemoveConferenceCallParticipantsRequest{
		CallID:        scope.call.ID,
		AuthorUserID:  scope.userID,
		TargetUserIDs: req.IDs,
		OnlyLeft:      req.OnlyLeft,
		Kick:          req.Kick,
		Block:         req.Block,
		Now:           now,
	})
	if err != nil {
		return nil, groupCallErr(err)
	}
	if result.ChainBlockAppended {
		block := result.ChainBlock
		r.pushConferenceChainBlocks(ctx, result.Call, block.SubChainID, [][]byte{block.Block}, block.Offset+1)
	}
	if len(result.ParticipantsChanged) > 0 {
		r.pushConferenceGroupCallParticipantsUpdate(ctx, result.Call, result.ParticipantsChanged)
		r.pushConferenceGroupCallUpdate(ctx, result.Call)
	}
	for _, p := range result.ParticipantsChanged {
		if r.deps.SFU != nil {
			_ = r.deps.SFU.Leave(ctx, scope.call.ID, p.UserID, sfu.EndpointMain)
		}
	}
	out := r.groupCallUpdateContainer(ctx, scope.userID, domain.Channel{},
		&tg.UpdateGroupCallParticipants{
			Call:         &tg.InputGroupCall{ID: result.Call.ID, AccessHash: result.Call.AccessHash},
			Participants: tgGroupCallParticipants(result.ParticipantsChanged, scope.userID),
			Version:      result.Call.Version,
		}, req.IDs)
	if result.ChainBlockAppended {
		block := result.ChainBlock
		out.Updates = append(out.Updates, conferenceChainBlocksUpdate(result.Call, block.SubChainID, [][]byte{block.Block}, block.Offset+1))
	}
	return out, nil
}

func (r *Router) onPhoneSendConferenceCallBroadcast(ctx context.Context, req *tg.PhoneSendConferenceCallBroadcastRequest) (tg.UpdatesClass, error) {
	if req == nil {
		return nil, inputRequestInvalidErr()
	}
	if len(req.Block) == 0 || len(req.Block) > maxConferenceChainBlockBytes {
		return nil, limitInvalidErr()
	}
	scope, err := r.groupCallScopeFrom(ctx, req.Call)
	if err != nil {
		return nil, err
	}
	if !scope.call.Conference() {
		return nil, groupCallInvalidErr()
	}
	if err := r.requireActiveConferenceParticipant(ctx, scope.call.ID, scope.userID); err != nil {
		return nil, err
	}
	now := int(r.clock.Now().Unix())
	block, err := r.deps.GroupCalls.AppendChainBlock(ctx, domain.GroupCallChainBlock{
		CallID:       scope.call.ID,
		SubChainID:   1,
		Offset:       -1,
		AuthorUserID: scope.userID,
		Block:        req.Block,
		CreatedAt:    now,
	})
	if err != nil {
		return nil, groupCallErr(err)
	}
	nextOffset := block.Offset + 1
	r.pushConferenceChainBlocks(ctx, scope.call, block.SubChainID, [][]byte{block.Block}, nextOffset)
	return r.conferenceChainBlocksUpdates(ctx, scope.userID, scope.call, block.SubChainID, [][]byte{block.Block}, nextOffset), nil
}

func (r *Router) requireActiveConferenceParticipant(ctx context.Context, callID, userID int64) error {
	if r.deps.GroupCalls == nil {
		return notImplementedErr()
	}
	p, found, err := r.deps.GroupCalls.Participant(ctx, callID, userID)
	if err != nil {
		return internalErr()
	}
	if !found || p.Left {
		return groupCallJoinMissingErr()
	}
	return nil
}

func (r *Router) onPhoneGetGroupCallChainBlocks(ctx context.Context, req *tg.PhoneGetGroupCallChainBlocksRequest) (tg.UpdatesClass, error) {
	if req == nil {
		return nil, inputRequestInvalidErr()
	}
	if req.Offset < domain.GroupCallChainBlockLatestOffset {
		return nil, inputRequestInvalidErr()
	}
	limit := req.Limit
	if limit <= 0 || limit > maxConferenceChainBlocks {
		limit = maxConferenceChainBlocks
	}
	scope, err := r.groupCallScopeFrom(ctx, req.Call)
	if err != nil {
		return nil, err
	}
	if !scope.call.Conference() {
		return nil, groupCallInvalidErr()
	}
	page, err := r.deps.GroupCalls.ChainBlocks(ctx, scope.call.ID, req.SubChainID, req.Offset, limit)
	if err != nil {
		return nil, groupCallErr(err)
	}
	blocks := make([][]byte, 0, len(page.Blocks))
	for _, row := range page.Blocks {
		blocks = append(blocks, row.Block)
	}
	return r.conferenceChainBlocksUpdates(ctx, scope.userID, scope.call, req.SubChainID, blocks, page.NextOffset), nil
}

func (r *Router) pushConferenceChainBlocks(ctx context.Context, call domain.GroupCall, subChainID int, blocks [][]byte, nextOffset int) {
	recipients := r.conferenceCallRecipients(ctx, call.ID)
	for _, viewerID := range recipients {
		r.pushUserMessage(ctx, viewerID, "conference chain blocks",
			r.conferenceChainBlocksUpdates(ctx, viewerID, call, subChainID, blocks, nextOffset))
	}
}

func (r *Router) conferenceChainBlocksUpdates(ctx context.Context, viewerID int64, call domain.GroupCall, subChainID int, blocks [][]byte, nextOffset int) *tg.Updates {
	return &tg.Updates{
		Updates: []tg.UpdateClass{conferenceChainBlocksUpdate(call, subChainID, blocks, nextOffset)},
		Users:   r.tgUsersForIDs(ctx, viewerID, []int64{viewerID}),
		Date:    int(r.clock.Now().Unix()),
		Seq:     0,
	}
}

func conferenceChainBlocksUpdate(call domain.GroupCall, subChainID int, blocks [][]byte, nextOffset int) *tg.UpdateGroupCallChainBlocks {
	return &tg.UpdateGroupCallChainBlocks{
		Call:       &tg.InputGroupCall{ID: call.ID, AccessHash: call.AccessHash},
		SubChainID: subChainID,
		Blocks:     conferenceServerBlocks(subChainID, blocks),
		NextOffset: nextOffset,
	}
}

func conferenceServerBlocks(subChainID int, blocks [][]byte) [][]byte {
	out := make([][]byte, 0, len(blocks))
	for _, block := range blocks {
		out = append(out, conferenceServerBlock(subChainID, block))
	}
	return out
}

func conferenceServerBlock(subChainID int, block []byte) []byte {
	out := append([]byte(nil), block...)
	if len(out) < 4 {
		return out
	}
	constructor := binary.LittleEndian.Uint32(out[:4])
	switch subChainID {
	case 0:
		if constructor == conferenceChainBlockConstructor {
			binary.LittleEndian.PutUint32(out[:4], conferenceChainBlockServerConstructor)
		}
	case 1:
		switch constructor {
		case conferenceBroadcastCommitConstructor:
			binary.LittleEndian.PutUint32(out[:4], conferenceBroadcastCommitServerConstructor)
		case conferenceBroadcastRevealConstructor:
			binary.LittleEndian.PutUint32(out[:4], conferenceBroadcastRevealServerConstructor)
		}
	}
	return out
}

func appendUpdates(dst *tg.Updates, src tg.UpdatesClass) {
	if dst == nil || src == nil {
		return
	}
	switch v := src.(type) {
	case *tg.Updates:
		dst.Updates = append(dst.Updates, v.Updates...)
		dst.Users = append(dst.Users, v.Users...)
		dst.Chats = append(dst.Chats, v.Chats...)
	case *tg.UpdatesCombined:
		dst.Updates = append(dst.Updates, v.Updates...)
		dst.Users = append(dst.Users, v.Users...)
		dst.Chats = append(dst.Chats, v.Chats...)
	}
}

func conferenceInviteRandomID(callID, targetID int64, date int) int64 {
	id := int64(0x636f6e6663616c) // "confcal"
	id ^= callID << 11
	id ^= targetID << 3
	id ^= int64(date) << 29
	if id == 0 {
		return 0x636f6e66
	}
	return id
}

func authKeyIDFromCtx(ctx context.Context) [8]byte {
	if authKeyID, ok := RawAuthKeyIDFrom(ctx); ok {
		return authKeyID
	}
	return [8]byte{}
}

func sessionIDFromCtx(ctx context.Context) int64 {
	if sessionID, ok := SessionIDFrom(ctx); ok {
		return sessionID
	}
	return 0
}

func (r *Router) logConferenceMessageFailure(callID int64, err error) {
	if err != nil {
		r.log.Warn("conference message", zap.Int64("call_id", callID), zap.Error(err))
	}
}
