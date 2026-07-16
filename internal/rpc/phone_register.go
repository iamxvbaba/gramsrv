package rpc

import (
	"context"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tlprofile"
)

// registerPhone 注册通话域 RPC。
//
// 归属约定（跨任务协调，群聊 M0 落地时遵守）：本文件是 phone.* 的唯一注册点；
// tlprofile.Dispatcher 对同一 semantic method 重复注册会失败，群聊 stub
// 清单不得覆盖此处已注册的真实现。messages.getDhConfig 属通话域（DH 参数下发），
// 注册在这里而非 messages_register.go。
func (r *Router) registerPhone(d *tlprofile.Dispatcher) {
	registerRPC[*tg.MessagesGetDhConfigRequest](d, tlprofile.SemanticMethodMessagesGetDhConfig, func(ctx context.Context, layerRequest *tg.MessagesGetDhConfigRequest) (any, error) {
		return r.onMessagesGetDhConfig(ctx, layerRequest)
	})
	registerRPC[*tg.PhoneRequestCallRequest](d, tlprofile.SemanticMethodPhoneRequestCall, func(ctx context.Context, layerRequest *tg.PhoneRequestCallRequest) (any, error) {
		return r.onPhoneRequestCall(ctx, layerRequest)
	})
	registerRPC[*tg.PhoneReceivedCallRequest](d, tlprofile.SemanticMethodPhoneReceivedCall, func(ctx context.Context, layerRequest *tg.PhoneReceivedCallRequest) (any, error) {
		return r.onPhoneReceivedCall(ctx, layerRequest.
			Peer)
	})
	registerRPC[*tg.PhoneAcceptCallRequest](d, tlprofile.SemanticMethodPhoneAcceptCall, func(ctx context.Context, layerRequest *tg.PhoneAcceptCallRequest) (any, error) {
		return r.onPhoneAcceptCall(ctx, layerRequest)
	})
	registerRPC[*tg.PhoneConfirmCallRequest](d, tlprofile.SemanticMethodPhoneConfirmCall, func(ctx context.Context, layerRequest *tg.PhoneConfirmCallRequest) (any, error) {
		return r.onPhoneConfirmCall(ctx, layerRequest)
	})
	registerRPC[*tg.PhoneDiscardCallRequest](d, tlprofile.SemanticMethodPhoneDiscardCall, func(ctx context.Context, layerRequest *tg.PhoneDiscardCallRequest) (any, error) {
		return r.onPhoneDiscardCall(ctx, layerRequest)
	})
	registerRPC[*tg.PhoneSendSignalingDataRequest](d, tlprofile.SemanticMethodPhoneSendSignalingData, func(ctx context.Context, layerRequest *tg.PhoneSendSignalingDataRequest) (any, error) {
		return r.onPhoneSendSignalingData(ctx, layerRequest)
	})
	registerRPC[*tg.PhoneSetCallRatingRequest](d, tlprofile.SemanticMethodPhoneSetCallRating, func(ctx context.Context, layerRequest *tg.PhoneSetCallRatingRequest) (any, error) {
		return r.onPhoneSetCallRating(ctx, layerRequest)
	})
	registerRPC[*tg.PhoneSaveCallDebugRequest](d, tlprofile.SemanticMethodPhoneSaveCallDebug, func(ctx context.Context, layerRequest *tg.PhoneSaveCallDebugRequest) (any, error) {
		return r.onPhoneSaveCallDebug(ctx, layerRequest)
	})
	registerRPC[*tg.PhoneGetCallConfigRequest](d, tlprofile.SemanticMethodPhoneGetCallConfig, func(ctx context.Context, layerRequest *
	// tgcalls 对空配置走默认值；需要精调（audio_max_bitrate 等）时再填键值。
	tg.PhoneGetCallConfigRequest) (any, error) {

		return &tg.DataJSON{Data: "{}"}, nil
	})
	registerRPC[

	// 超级群语音聊天（group call）。
	*tg.PhoneCreateGroupCallRequest](d, tlprofile.SemanticMethodPhoneCreateGroupCall, func(ctx context.Context, layerRequest *tg.PhoneCreateGroupCallRequest) (any, error) {
		return r.onPhoneCreateGroupCall(ctx, layerRequest)
	})
	registerRPC[*tg.PhoneJoinGroupCallRequest](d, tlprofile.SemanticMethodPhoneJoinGroupCall, func(ctx context.Context, layerRequest *tg.PhoneJoinGroupCallRequest) (any, error) {
		return r.onPhoneJoinGroupCall(ctx, layerRequest)
	})
	registerRPC[*tg.PhoneLeaveGroupCallRequest](d, tlprofile.SemanticMethodPhoneLeaveGroupCall, func(ctx context.Context, layerRequest *tg.PhoneLeaveGroupCallRequest) (any, error) {
		return r.onPhoneLeaveGroupCall(ctx, layerRequest)
	})
	registerRPC[*tg.PhoneDiscardGroupCallRequest](d, tlprofile.SemanticMethodPhoneDiscardGroupCall, func(ctx context.Context, layerRequest *tg.PhoneDiscardGroupCallRequest) (any, error) {
		return r.onPhoneDiscardGroupCall(ctx, layerRequest.
			Call)
	})
	registerRPC[*tg.PhoneGetGroupCallRequest](d, tlprofile.SemanticMethodPhoneGetGroupCall, func(ctx context.Context, layerRequest *tg.PhoneGetGroupCallRequest) (any, error) {
		return r.onPhoneGetGroupCall(ctx, layerRequest)
	})
	registerRPC[*tg.PhoneGetGroupParticipantsRequest](d, tlprofile.SemanticMethodPhoneGetGroupParticipants, func(ctx context.Context, layerRequest *tg.PhoneGetGroupParticipantsRequest) (any, error) {
		return r.onPhoneGetGroupParticipants(ctx, layerRequest)
	})
	registerRPC[*tg.PhoneCheckGroupCallRequest](d, tlprofile.SemanticMethodPhoneCheckGroupCall, func(ctx context.Context, layerRequest *tg.PhoneCheckGroupCallRequest) (any, error) {
		return r.onPhoneCheckGroupCall(ctx, layerRequest)
	})
	registerRPC[*tg.PhoneExportGroupCallInviteRequest](d, tlprofile.SemanticMethodPhoneExportGroupCallInvite, func(ctx context.Context, layerRequest *tg.PhoneExportGroupCallInviteRequest) (any, error) {
		return r.onPhoneExportGroupCallInvite(ctx, layerRequest)
	})
	registerRPC[*tg.PhoneEditGroupCallParticipantRequest](d, tlprofile.SemanticMethodPhoneEditGroupCallParticipant, func(ctx context.Context, layerRequest *tg.PhoneEditGroupCallParticipantRequest) (any, error) {
		return r.onPhoneEditGroupCallParticipant(ctx, layerRequest)
	})
	registerRPC[*tg.PhoneEditGroupCallTitleRequest](d, tlprofile.SemanticMethodPhoneEditGroupCallTitle, func(ctx context.Context, layerRequest *tg.PhoneEditGroupCallTitleRequest) (any, error) {
		return r.onPhoneEditGroupCallTitle(ctx, layerRequest)
	})
	registerRPC[*tg.PhoneToggleGroupCallSettingsRequest](d, tlprofile.SemanticMethodPhoneToggleGroupCallSettings, func(ctx context.Context, layerRequest *tg.PhoneToggleGroupCallSettingsRequest) (

		// 定时通话（scheduled video chat）。
		any, error) {
		return r.onPhoneToggleGroupCallSettings(ctx, layerRequest)
	})
	registerRPC[*tg.PhoneInviteToGroupCallRequest](d, tlprofile.SemanticMethodPhoneInviteToGroupCall, func(ctx context.Context, layerRequest *tg.PhoneInviteToGroupCallRequest) (any, error) {
		return r.onPhoneInviteToGroupCall(ctx, layerRequest)
	})
	registerRPC[*tg.PhoneStartScheduledGroupCallRequest](d, tlprofile.SemanticMethodPhoneStartScheduledGroupCall, func(ctx context.Context, layerRequest *tg.PhoneStartScheduledGroupCallRequest) (any, error) {
		return r.onPhoneStartScheduledGroupCall(ctx, layerRequest.
			Call)
	})
	registerRPC[*tg.PhoneToggleGroupCallStartSubscriptionRequest](d, tlprofile.SemanticMethodPhoneToggleGroupCallStartSubscription, func(ctx context.Context,
		// Ad-hoc E2E conference call（P2P 通话升级/拉人路径）。
		layerRequest *tg.PhoneToggleGroupCallStartSubscriptionRequest) (any, error) {
		return r.onPhoneToggleGroupCallStartSubscription(ctx, layerRequest)
	})
	registerRPC[*tg.PhoneCreateConferenceCallRequest](d, tlprofile.SemanticMethodPhoneCreateConferenceCall, func(ctx context.Context, layerRequest *tg.PhoneCreateConferenceCallRequest) (any, error) {
		return r.onPhoneCreateConferenceCall(ctx, layerRequest)
	})
	registerRPC[*tg.PhoneInviteConferenceCallParticipantRequest](d, tlprofile.SemanticMethodPhoneInviteConferenceCallParticipant, func(ctx context.Context, layerRequest *tg.PhoneInviteConferenceCallParticipantRequest) (any, error) {
		return r.onPhoneInviteConferenceCallParticipant(ctx, layerRequest)
	})
	registerRPC[*tg.PhoneDeleteConferenceCallParticipantsRequest](d, tlprofile.SemanticMethodPhoneDeleteConferenceCallParticipants, func(ctx context.Context, layerRequest *tg.PhoneDeleteConferenceCallParticipantsRequest) (any, error) {
		return r.onPhoneDeleteConferenceCallParticipants(ctx, layerRequest)
	})
	registerRPC[*tg.PhoneSendConferenceCallBroadcastRequest](d, tlprofile.SemanticMethodPhoneSendConferenceCallBroadcast, func(ctx context.Context, layerRequest *tg.PhoneSendConferenceCallBroadcastRequest) (any, error) {
		return r.onPhoneSendConferenceCallBroadcast(ctx, layerRequest)
	})
	registerRPC[*tg.PhoneDeclineConferenceCallInviteRequest](d, tlprofile.SemanticMethodPhoneDeclineConferenceCallInvite, func(ctx context.Context, layerRequest *tg.PhoneDeclineConferenceCallInviteRequest) (any, error) {

		// 屏幕共享（M4）：同参与者第二媒体连接。
		return r.onPhoneDeclineConferenceCallInvite(ctx, layerRequest.
			MsgID)
	})
	registerRPC[*tg.PhoneGetGroupCallChainBlocksRequest](d, tlprofile.SemanticMethodPhoneGetGroupCallChainBlocks, func(ctx context.Context, layerRequest *tg.PhoneGetGroupCallChainBlocksRequest) (any, error) {
		return r.onPhoneGetGroupCallChainBlocks(ctx, layerRequest)
	})
	registerRPC[*tg.PhoneJoinGroupCallPresentationRequest](d, tlprofile.SemanticMethodPhoneJoinGroupCallPresentation, func(ctx context.Context, layerRequest *tg.PhoneJoinGroupCallPresentationRequest) (any, error) {
		return r.onPhoneJoinGroupCallPresentation(ctx, layerRequest)
	})
	registerRPC[*tg.PhoneLeaveGroupCallPresentationRequest](d, tlprofile.SemanticMethodPhoneLeaveGroupCallPresentation, func(ctx context.Context, layerRequest *tg.PhoneLeaveGroupCallPresentationRequest) (any, error) {
		return r.onPhoneLeaveGroupCallPresentation(ctx, layerRequest.
			Call)
	})

	r.registerPhoneStubs(d)
}
