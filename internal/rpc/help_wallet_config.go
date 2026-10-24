package rpc

import (
	"context"
	"hash/crc32"
	"strconv"

	"github.com/iamxvbaba/td/tg"
	"telesrv/internal/compat/tdesktop"
)

// walletAppConfig231 applies the wallet settings only to negotiated layer 231.
// Fetch the full base config before checking the layer-specific cache hash.
func (r *Router) walletAppConfig231(ctx context.Context, clientHash int) (any, error) {
	var base *tg.HelpAppConfig
	if r.deps.Help == nil {
		base = tdesktop.AppConfig(0).(*tg.HelpAppConfig)
	} else {
		userID, _ := UserIDFrom(ctx)
		cfg, _, err := r.deps.Help.GetAppConfig(ctx, userID, 0)
		if err != nil {
			return nil, internalErr()
		}
		base = &tg.HelpAppConfig{Hash: cfg.Hash, Config: tgJSONValue(cfg.JSON)}
	}
	obj, ok := base.Config.(*tg.JSONObject)
	if !ok {
		return nil, internalErr()
	}
	// Include the values in the cache namespace so future setting changes
	// invalidate cached configs as well as changes to the base config.
	const settings = "internal-gram-v2:wallet_available=true:wallet_internal_gram=true:wallet_transfer_min_nanos=1.0:wallet_gasless_min_nanos=1.0:wallet_gasless_daily_transfers=0.0"
	hash := int(crc32.ChecksumIEEE([]byte(strconv.Itoa(base.Hash)+":"+settings)) & 0x7fffffff)
	if hash == 0 || hash == base.Hash {
		hash = (base.Hash + 1) & 0x7fffffff
		if hash == 0 {
			hash = 1
		}
	}
	if clientHash != 0 && clientHash == hash {
		return &tg.HelpAppConfigNotModified{}, nil
	}
	values := make([]tg.JSONObjectValue, 0, len(obj.Value)+4)
	for _, value := range obj.Value {
		switch value.Key {
		case "wallet_internal_gram", "wallet_available", "wallet_transfer_min_nanos", "wallet_gasless_min_nanos", "wallet_gasless_daily_transfers":
			continue
		}
		values = append(values, value)
	}
	values = append(values,
		tg.JSONObjectValue{Key: "wallet_internal_gram", Value: &tg.JSONBool{Value: true}},
		tg.JSONObjectValue{Key: "wallet_available", Value: &tg.JSONBool{Value: true}},
		tg.JSONObjectValue{Key: "wallet_transfer_min_nanos", Value: &tg.JSONNumber{Value: 1.0}},
		tg.JSONObjectValue{Key: "wallet_gasless_min_nanos", Value: &tg.JSONNumber{Value: 1.0}},
		tg.JSONObjectValue{Key: "wallet_gasless_daily_transfers", Value: &tg.JSONNumber{Value: 0.0}},
	)
	return &tg.HelpAppConfig{Hash: hash, Config: &tg.JSONObject{Value: values}}, nil
}
