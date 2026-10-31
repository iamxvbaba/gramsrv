package tdesktop

import (
	"net/netip"
	"time"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/links"
)

// BuildConfig constructs the tg.Config returned by help.getConfig, including the
// DCOptions of the self-hosted DC.
//
// Field values follow common Telegram defaults; during TDesktop bring-up they are tuned
// to what the client actually needs (recorded in docs/compatibility-matrix.md).
func BuildConfig(dc int, ip string, port int, now time.Time, publicBaseURL, updateBaseURL string) *tg.Config {
	// TELESRV_ADVERTISE_IP is validated during config loading. Parse again here
	// only to derive the wire ipv6 flag and to render IPv4-mapped addresses in
	// their canonical form. Keeping the advertised route in help.getConfig is a
	// protocol invariant: clients replace or persist this list for reconnects.
	addr, err := netip.ParseAddr(ip)
	if err == nil {
		addr = addr.Unmap()
		ip = addr.String()
	}
	meURLPrefix := links.NormalizeBaseURL(publicBaseURL) + "/"
	config := &tg.Config{
		Date:     int(now.Unix()),
		Expires:  int(now.Add(time.Hour).Unix()),
		TestMode: false,
		ThisDC:   dc,
		DCOptions: []tg.DCOption{{
			Ipv6:      addr.Is6(),
			ID:        dc,
			IPAddress: ip,
			Port:      port,
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
		// The current official revoke triad: no time limit + allow revoking private
		// messages sent by the other party. TDesktop's private-chat "Also delete for X"
		// checkbox requires revoke_pm_inbox=true and revoke_pm_time_limit=0x7FFFFFFF,
		// otherwise the two-way delete UI never appears.
		RevokeTimeLimit:      2147483647,
		RevokePmTimeLimit:    2147483647,
		RevokePmInbox:        true,
		RatingEDecay:         2419200,
		StickersRecentLimit:  200,
		CallReceiveTimeoutMs: 20000,
		CallRingTimeoutMs:    90000,
		CallConnectTimeoutMs: 30000,
		CallPacketTimeoutMs:  10000,
		MeURLPrefix:          meURLPrefix,
		CaptionLengthMax:     1024,
		MessageLengthMax:     4096,
		WebfileDCID:          dc,
	}
	config.SetGifSearchUsername("gif")
	config.SetReactionsDefault(&tg.ReactionEmoji{Emoticon: DefaultReactionEmoticon})
	if updateBaseURL != "" {
		config.SetAutoupdateURLPrefix(links.NormalizeBaseURL(updateBaseURL))
	}
	return config
}
