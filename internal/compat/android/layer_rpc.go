package android

import (
	"errors"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/tg"
)

var ErrPrivateLayerRPCInvalid = errors.New("android private layer RPC is invalid")

// AdaptPrivateLayerRPC invokes the provenance-locked static gotdgen overlay
// from the generated unknown-method view. Nested values decode with the exact
// connection profile and the canonical request is re-profiled by gotd core.
func AdaptPrivateLayerRPC(view tg.LayerRPCUnknownMethodView) (tg.LayerOutboundCall, bool, error) {
	outbound, handled, err := view.AdaptClientRPCOverlay(tg.LayerClientRPCOverlayDrkloAndroid)
	if err == nil && !handled {
		outbound, handled, err = view.AdaptClientRPCOverlay(tg.LayerClientRPCOverlayDrkloAndroidTheme)
	}
	if err != nil {
		return tg.LayerOutboundCall{}, handled, errors.Join(ErrPrivateLayerRPCInvalid, err)
	}
	return outbound, handled, nil
}

// UpgradePrivateLayerRPC is retained only for Router.Dispatch's legacy test
// seam. Production admission uses AdaptPrivateLayerRPC above so its decode
// shares the outer generated request budget.
func UpgradePrivateLayerRPC(profile tg.LayerProfile, in *bin.Buffer, limits tg.LayerDecodeLimits) (*bin.Buffer, bool, error) {
	upgraded, handled, err := tg.AdaptClientRPCOverlayWithLimits(profile, tg.LayerClientRPCOverlayDrkloAndroid, in, limits)
	if err == nil && !handled {
		upgraded, handled, err = tg.AdaptClientRPCOverlayWithLimits(profile, tg.LayerClientRPCOverlayDrkloAndroidTheme, in, limits)
	}
	if err != nil {
		return nil, handled, errors.Join(ErrPrivateLayerRPCInvalid, err)
	}
	return upgraded, handled, nil
}
