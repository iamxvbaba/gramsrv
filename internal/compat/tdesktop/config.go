package tdesktop

import (
	"time"

	"github.com/gotd/td/tg"
)

// BuildConfig 构造 help.getConfig 返回的 tg.Config，含自建 DC 的 DCOptions。
//
// 字段值取 Telegram 常见默认；TDesktop 联调阶段按客户端实际需要微调
// （记录于 docs/compatibility-matrix.md）。
func BuildConfig(dc int, ip string, port int, now time.Time) *tg.Config {
	return &tg.Config{
		Date:     int(now.Unix()),
		Expires:  int(now.Add(time.Hour).Unix()),
		TestMode: false,
		ThisDC:   dc,
		DCOptions: []tg.DCOption{{
			ID:                dc,
			IPAddress:         ip,
			Port:              port,
			TCPObfuscatedOnly: true,
			Static:            true,
			ThisPortOnly:      true,
		}},
		ChatSizeMax:          200,
		MegagroupSizeMax:     200000,
		ForwardedCountMax:    100,
		OnlineUpdatePeriodMs: 120000,
		OfflineBlurTimeoutMs: 5000,
		OfflineIdleTimeoutMs: 30000,
		OnlineCloudTimeoutMs: 300000,
		NotifyCloudDelayMs:   30000,
		NotifyDefaultDelayMs: 1500,
		PushChatPeriodMs:     60000,
		PushChatLimit:        2,
		EditTimeLimit:        172800,
		// 官方现行 revoke 三元组：无时限 + 允许撤回对方发来的私聊消息。
		// TDesktop 的私聊 "Also delete for X" 复选框要求
		// revoke_pm_inbox=true 且 revoke_pm_time_limit=0x7FFFFFFF，
		// 否则双向删除 UI 永不出现。
		RevokeTimeLimit:      2147483647,
		RevokePmTimeLimit:    2147483647,
		RevokePmInbox:        true,
		RatingEDecay:         2419200,
		StickersRecentLimit:  200,
		CallReceiveTimeoutMs: 20000,
		CallRingTimeoutMs:    90000,
		CallConnectTimeoutMs: 30000,
		CallPacketTimeoutMs:  10000,
		MeURLPrefix:          "https://telesrv.net/",
		CaptionLengthMax:     1024,
		MessageLengthMax:     4096,
		WebfileDCID:          dc,
	}
}

// NearestDC 构造 help.getNearestDc 返回值。
func NearestDC(dc int) *tg.NearestDC {
	return &tg.NearestDC{
		// 默认国家=中国：DrKLO/TDesktop 登录页据此预选区号(+86)。
		Country:   "CN",
		ThisDC:    dc,
		NearestDC: dc,
	}
}
