package rpc

import (
	"context"

	"github.com/gotd/td/tg"
)

// registerPhone 注册通话域 RPC。
//
// 归属约定（跨任务协调，群聊 M0 落地时遵守）：本文件是 phone.* 的唯一注册点；
// gotd ServerDispatcher 对同一 RPC 重复 On* 注册是静默 last-wins，群聊 stub
// 清单不得覆盖此处已注册的真实现。messages.getDhConfig 属通话域（DH 参数下发），
// 注册在这里而非 messages_register.go。
func (r *Router) registerPhone(d *tg.ServerDispatcher) {
	d.OnMessagesGetDhConfig(r.onMessagesGetDhConfig)

	d.OnPhoneRequestCall(r.onPhoneRequestCall)
	d.OnPhoneReceivedCall(r.onPhoneReceivedCall)
	d.OnPhoneAcceptCall(r.onPhoneAcceptCall)
	d.OnPhoneConfirmCall(r.onPhoneConfirmCall)
	d.OnPhoneDiscardCall(r.onPhoneDiscardCall)
	d.OnPhoneSendSignalingData(r.onPhoneSendSignalingData)
	d.OnPhoneSetCallRating(r.onPhoneSetCallRating)
	d.OnPhoneSaveCallDebug(r.onPhoneSaveCallDebug)
	d.OnPhoneGetCallConfig(func(ctx context.Context) (*tg.DataJSON, error) {
		// tgcalls 对空配置走默认值；需要精调（audio_max_bitrate 等）时再填键值。
		return &tg.DataJSON{Data: "{}"}, nil
	})

	// 超级群语音聊天（group call）。
	d.OnPhoneCreateGroupCall(r.onPhoneCreateGroupCall)
	d.OnPhoneJoinGroupCall(r.onPhoneJoinGroupCall)
	d.OnPhoneLeaveGroupCall(r.onPhoneLeaveGroupCall)
	d.OnPhoneDiscardGroupCall(r.onPhoneDiscardGroupCall)
	d.OnPhoneGetGroupCall(r.onPhoneGetGroupCall)
	d.OnPhoneGetGroupParticipants(r.onPhoneGetGroupParticipants)
	d.OnPhoneCheckGroupCall(r.onPhoneCheckGroupCall)
	d.OnPhoneExportGroupCallInvite(r.onPhoneExportGroupCallInvite)
	d.OnPhoneEditGroupCallParticipant(r.onPhoneEditGroupCallParticipant)
	d.OnPhoneEditGroupCallTitle(r.onPhoneEditGroupCallTitle)
	d.OnPhoneToggleGroupCallSettings(r.onPhoneToggleGroupCallSettings)
	d.OnPhoneInviteToGroupCall(r.onPhoneInviteToGroupCall)
	// Ad-hoc E2E conference call（P2P 通话升级/拉人路径）。
	d.OnPhoneCreateConferenceCall(r.onPhoneCreateConferenceCall)
	d.OnPhoneInviteConferenceCallParticipant(r.onPhoneInviteConferenceCallParticipant)
	d.OnPhoneDeleteConferenceCallParticipants(r.onPhoneDeleteConferenceCallParticipants)
	d.OnPhoneSendConferenceCallBroadcast(r.onPhoneSendConferenceCallBroadcast)
	d.OnPhoneDeclineConferenceCallInvite(r.onPhoneDeclineConferenceCallInvite)
	d.OnPhoneGetGroupCallChainBlocks(r.onPhoneGetGroupCallChainBlocks)
	// 屏幕共享（M4）：同参与者第二媒体连接。
	d.OnPhoneJoinGroupCallPresentation(r.onPhoneJoinGroupCallPresentation)
	d.OnPhoneLeaveGroupCallPresentation(r.onPhoneLeaveGroupCallPresentation)
	r.registerPhoneStubs(d)
}
