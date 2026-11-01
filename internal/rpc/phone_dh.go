package rpc

import (
	"context"

	"github.com/iamxvbaba/td/tg"

	appphone "telesrv/internal/app/phone"
)

// onMessagesGetDhConfig hands out the DH parameters for private calls (and future secret chats).
//
// Contract (TDesktop calls_instance.cpp updateDhConfig):
//   - p/g must pass MTP::IsPrimeAndGood — use the official 2048-bit prime + g=3 to take the
//     whitelist fast path;
//   - the number of random bytes must match the requested random_length;
//   - on a client cache hit (same version) return dhConfigNotModified with only fresh random bytes.
func (r *Router) onMessagesGetDhConfig(ctx context.Context, req *tg.MessagesGetDhConfigRequest) (tg.MessagesDhConfigClass, error) {
	if req == nil {
		return nil, inputRequestInvalidErr()
	}
	if _, ok, err := r.currentUserID(ctx); err != nil {
		return nil, internalErr()
	} else if !ok {
		return nil, authKeyUnregisteredErr()
	}
	random, err := appphone.DHRandom(req.RandomLength)
	if err != nil {
		return nil, internalErr()
	}
	if req.Version == appphone.DHConfigVersion {
		return &tg.MessagesDhConfigNotModified{Random: random}, nil
	}
	return &tg.MessagesDhConfig{
		G:       appphone.DHG,
		P:       appphone.DHPrime(),
		Version: appphone.DHConfigVersion,
		Random:  random,
	}, nil
}
