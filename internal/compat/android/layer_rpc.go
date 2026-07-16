package android

import (
	"errors"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/tlprofile"
)

var ErrPrivateLayerRPCInvalid = errors.New("android private layer RPC is invalid")

// AdaptPrivateLayerRPC invokes the provenance-locked static gotdgen overlay
// from the generated unknown-method view. Nested values decode with the exact
// connection profile and the canonical request is re-profiled by gotd core.
func AdaptPrivateLayerRPC(view tlprofile.UnknownMethodView) (tlprofile.OutboundCall, bool, error) {
	outbound, handled, err := view.AdaptClientRPCOverlay(tlprofile.ClientRPCOverlayDrkloAndroid)
	if err == nil && !handled {
		outbound, handled, err = view.AdaptClientRPCOverlay(tlprofile.ClientRPCOverlayDrkloAndroidTheme)
	}
	if err != nil {
		return tlprofile.OutboundCall{}, handled, errors.Join(ErrPrivateLayerRPCInvalid, err)
	}
	return outbound, handled, nil
}

// UpgradePrivateLayerRPC is retained only for Router.Dispatch's legacy test
// seam. Production admission uses AdaptPrivateLayerRPC above so its decode
// shares the outer generated request budget.
func UpgradePrivateLayerRPC(profile tlprofile.Profile, in *bin.Buffer, limits tlprofile.Limits) (*bin.Buffer, bool, error) {
	upgraded, handled, err := tlprofile.AdaptClientRPCOverlayWithLimits(profile, tlprofile.ClientRPCOverlayDrkloAndroid, in, limits)
	if err == nil && !handled {
		upgraded, handled, err = tlprofile.AdaptClientRPCOverlayWithLimits(profile, tlprofile.ClientRPCOverlayDrkloAndroidTheme, in, limits)
	}
	if err != nil {
		return nil, handled, errors.Join(ErrPrivateLayerRPCInvalid, err)
	}
	return upgraded, handled, nil
}
