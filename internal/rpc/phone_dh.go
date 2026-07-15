package rpc

import (
	"context"

	"github.com/iamxvbaba/td/tg"

	appphone "telesrv/internal/app/phone"
)

// onMessagesGetDhConfig 下发私聊通话（及未来 secret chat）的 DH 参数。
//
// 契约（TDesktop calls_instance.cpp updateDhConfig）：
//   - p/g 必须过 MTP::IsPrimeAndGood——用官方 2048-bit prime + g=3 走白名单快速路径；
//   - random 字节数必须与请求的 random_length 一致；
//   - 客户端缓存命中（version 相同）时返回 dhConfigNotModified，只补新随机数。
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
