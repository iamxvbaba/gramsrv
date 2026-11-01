package rpc

import (
	"context"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tlprofile"
)

// Passive stubs for entry points outside group-call scope (to prevent crashes on tap). Unlisted
// phone.* (the conference family / in-call message family / scheduled / RTMP) go through the router
// fallback: 400/500 NOT_IMPLEMENTED + a compatibility-matrix log, without disconnecting the client.
func (r *Router) registerPhoneStubs(d *tlprofile.Dispatcher) {
	registerRPC[
	// Join-identity candidates: see phone_group_call.go for the real implementation (self + the
	// admin's channel identity).
	*tg.PhoneGetGroupCallJoinAsRequest](d, tlprofile.SemanticMethodPhoneGetGroupCallJoinAs, func(ctx context.
		// Persisting the default join-as preference is still a stub
		// (chatFull.groupcall_default_join_as is not backfilled).
		Context, layerRequest *tg.PhoneGetGroupCallJoinAsRequest) (any, error) {
		return r.onPhoneGetGroupCallJoinAs(ctx, layerRequest.
			Peer)
	})
	registerRPC[*tg.PhoneSaveDefaultGroupCallJoinAsRequest](d, tlprofile.SemanticMethodPhoneSaveDefaultGroupCallJoinAs, func(ctx context.Context, req *tg.PhoneSaveDefaultGroupCallJoinAsRequest) (any, error) {
		return true, nil
	})
	registerRPC[

	// Out of recording scope: the client only reads record_start_date (never sent), so acknowledging
	// is enough.
	*tg.PhoneToggleGroupCallRecordRequest](d, tlprofile.SemanticMethodPhoneToggleGroupCallRecord, func(ctx context.Context, req *tg.PhoneToggleGroupCallRecordRequest) (any, error) {
		return tgEmptyUpdates(int(r.clock.Now().Unix())), nil
	})
	registerRPC[

	// RTMP livestream (Live Stream): see phone_group_call_rtmp.go for the real handler.
	*tg.PhoneGetGroupCallStreamChannelsRequest](d, tlprofile.SemanticMethodPhoneGetGroupCallStreamChannels, func(ctx context.Context, layerRequest *tg.PhoneGetGroupCallStreamChannelsRequest) (any, error) {
		return r.onPhoneGetGroupCallStreamChannels(ctx, layerRequest.
			Call)
	})
	registerRPC[*tg.PhoneGetGroupCallStreamRtmpURLRequest](d, tlprofile.SemanticMethodPhoneGetGroupCallStreamRtmpURL, func(ctx context.Context, layerRequest *tg.PhoneGetGroupCallStreamRtmpURLRequest) (any, error) {
		return r.onPhoneGetGroupCallStreamRtmpURL(ctx, layerRequest)
	})
}
