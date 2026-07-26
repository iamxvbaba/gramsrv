package rpc

import (
	"context"
	"testing"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest"

	"telesrv/internal/domain"
)

// This file is the load-bearing test of the third-party verification feature.
//
// An official client renders the badge off one specific bit of one specific flags
// word. A projection that sets the neighbouring bit encodes a different, valid
// field -- the response still decodes, no error is raised anywhere, and the badge
// simply never appears. So the assertions below are not "the Go field is populated"
// but "the encoded flags word differs from the unmarked encoding in exactly the bit
// layer 228 assigns":
//
//	user#b1b8cc83        bot_verification_icon:flags2.14?long
//	channel#d49f34c6     bot_verification_icon:flags2.13?long
//	userFull#6cbe645     bot_verification:flags2.12?BotVerification
//	channelFull#a04e8d3a bot_verification:flags2.17?BotVerification
//	chatInvite#5c9d3702  bot_verification:flags.13?BotVerification
//	botInfo#4d8a0299     verifier_settings:flags.9?BotVerifierSettings
//
// Each case encodes the same object twice through the real handler -- once before
// the mark exists and once after -- and XORs the two flags words. That catches an
// off-by-one bit, and also catches a projection that quietly disturbs an unrelated
// flag while adding the badge.

// tlRoundTrip serialises through the wire and decodes back, so every assertion
// reads the encoded form rather than the in-memory struct.
func tlRoundTrip(t *testing.T, in bin.Encoder, out bin.Decoder) {
	t.Helper()
	buf := &bin.Buffer{}
	if err := in.Encode(buf); err != nil {
		t.Fatalf("encode %T: %v", in, err)
	}
	if err := out.Decode(buf); err != nil {
		t.Fatalf("decode %T: %v", out, err)
	}
	if buf.Len() != 0 {
		t.Fatalf("decode %T left %d trailing bytes", out, buf.Len())
	}
}

// assertFlagBitDelta pins that adding the mark flipped exactly one bit of the flags
// word, at the index layer 228 assigns.
func assertFlagBitDelta(t *testing.T, label string, before, after bin.Fields, wantBit int) {
	t.Helper()
	want := uint32(1) << uint(wantBit)
	delta := uint32(before) ^ uint32(after)
	if delta != want {
		t.Fatalf("%s flags delta = %032b, want exactly bit %d (%032b); before %032b after %032b",
			label, delta, wantBit, want, uint32(before), uint32(after))
	}
	if uint32(after)&want == 0 {
		t.Fatalf("%s did not set bit %d: flags = %032b", label, wantBit, uint32(after))
	}
}

// TestBotVerificationConstructorIDs pins the two constructor ids the feature
// serialises. A drift here silently reshapes every payload below.
func TestBotVerificationConstructorIDs(t *testing.T) {
	if tg.BotVerificationTypeID != 0xf93cd45c {
		t.Fatalf("botVerification constructor id = %#x, want 0xf93cd45c", tg.BotVerificationTypeID)
	}
	if tg.BotVerifierSettingsTypeID != 0xb0cd6617 {
		t.Fatalf("botVerifierSettings constructor id = %#x, want 0xb0cd6617", tg.BotVerifierSettingsTypeID)
	}
	// The carriers, so a layer bump that reshuffles them is caught here too.
	if tg.UserTypeID != 0xb1b8cc83 || tg.ChannelTypeID != 0xd49f34c6 {
		t.Fatalf("peer constructor ids = user %#x / channel %#x", tg.UserTypeID, tg.ChannelTypeID)
	}
	if tg.UserFullTypeID != 0x6cbe645 || tg.ChannelFullTypeID != 0xa04e8d3a {
		t.Fatalf("full constructor ids = userFull %#x / channelFull %#x", tg.UserFullTypeID, tg.ChannelFullTypeID)
	}
	if tg.ChatInviteTypeID != 0x5c9d3702 || tg.BotInfoTypeID != 0x4d8a0299 {
		t.Fatalf("constructor ids = chatInvite %#x / botInfo %#x", tg.ChatInviteTypeID, tg.BotInfoTypeID)
	}
	if tg.BotsSetCustomVerificationRequestTypeID != 0x8b89dfbd {
		t.Fatalf("bots.setCustomVerification id = %#x, want 0x8b89dfbd", tg.BotsSetCustomVerificationRequestTypeID)
	}
}

// TestBotVerificationTLProjectionShapes pins the two payload builders: the icon is a
// custom emoji document id, so a mark without one is omitted rather than encoded as
// a badge the client draws as nothing.
func TestBotVerificationTLProjectionShapes(t *testing.T) {
	value, ok := tgBotVerification(domain.BotVerification{BotID: 42, Icon: 777, Description: "Verified by Acme"})
	if !ok {
		t.Fatal("complete mark was rejected")
	}
	decoded := &tg.BotVerification{}
	tlRoundTrip(t, &value, decoded)
	if decoded.BotID != 42 || decoded.Icon != 777 || decoded.Description != "Verified by Acme" {
		t.Fatalf("botVerification = %+v", decoded)
	}
	for _, bad := range []domain.BotVerification{
		{BotID: 0, Icon: 777},
		{BotID: 42, Icon: 0},
	} {
		if _, ok := tgBotVerification(bad); ok {
			t.Fatalf("unrenderable mark %+v was projected", bad)
		}
	}

	settings, ok := tgBotVerifierSettings(domain.BotVerifierSettings{
		BotID: 42, IconDocumentID: 777, CompanyName: "Acme Trust",
		DefaultDescription: "Verified by Acme Trust", CanModifyCustomDescription: true, Enabled: true,
	})
	if !ok {
		t.Fatal("valid verifier settings were rejected")
	}
	decodedSettings := &tg.BotVerifierSettings{}
	tlRoundTrip(t, &settings, decodedSettings)
	if decodedSettings.Icon != 777 || decodedSettings.Company != "Acme Trust" {
		t.Fatalf("botVerifierSettings = %+v", decodedSettings)
	}
	if !decodedSettings.GetCanModifyCustomDescription() || !decodedSettings.Flags.Has(1) {
		t.Fatalf("can_modify_custom_description not on flags.1: %032b", uint32(decodedSettings.Flags))
	}
	if desc, ok := decodedSettings.GetCustomDescription(); !ok || desc != "Verified by Acme Trust" ||
		!decodedSettings.Flags.Has(0) {
		t.Fatalf("custom_description not on flags.0: %q ok=%v flags %032b", desc, ok, uint32(decodedSettings.Flags))
	}
	// A verifier with no default description leaves flags.0 clear.
	bare, ok := tgBotVerifierSettings(domain.BotVerifierSettings{
		BotID: 42, IconDocumentID: 777, CompanyName: "Acme Trust", Enabled: true,
	})
	if !ok {
		t.Fatal("verifier settings without a default description were rejected")
	}
	decodedBare := &tg.BotVerifierSettings{}
	tlRoundTrip(t, &bare, decodedBare)
	if decodedBare.Flags.Has(0) || decodedBare.Flags.Has(1) {
		t.Fatalf("bare verifier settings set optional flags: %032b", uint32(decodedBare.Flags))
	}
	// A configuration that does not validate is omitted entirely.
	if _, ok := tgBotVerifierSettings(domain.BotVerifierSettings{BotID: 42, IconDocumentID: 777, Enabled: true}); ok {
		t.Fatal("verifier settings without a company were projected")
	}
}

// getUsersUser runs users.getUsers and returns the wire form of one projected user.
func getUsersUser(t *testing.T, r *Router, viewerUserID int64, target domain.User) *tg.User {
	t.Helper()
	out, err := r.onUsersGetUsers(WithUserID(context.Background(), viewerUserID),
		[]tg.InputUserClass{&tg.InputUser{UserID: target.ID, AccessHash: target.AccessHash}})
	if err != nil {
		t.Fatalf("get users: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("get users returned %d users, want 1", len(out))
	}
	decoded := &tg.User{}
	tlRoundTrip(t, out[0].(*tg.User), decoded)
	return decoded
}

// getFullUserProjection runs users.getFullUser and returns the wire forms of the
// userFull and (when present) its bot_info.
func getFullUserProjection(t *testing.T, r *Router, viewerUserID int64, target domain.User) (*tg.UserFull, *tg.BotInfo) {
	t.Helper()
	res, err := r.onUsersGetFullUser(WithUserID(context.Background(), viewerUserID),
		&tg.InputUser{UserID: target.ID, AccessHash: target.AccessHash})
	if err != nil {
		t.Fatalf("get full user: %v", err)
	}
	full := res.FullUser
	decoded := &tg.UserFull{}
	tlRoundTrip(t, &full, decoded)
	info, ok := decoded.GetBotInfo()
	if !ok {
		return decoded, nil
	}
	return decoded, &info
}

// TestUserAndUserFullCarryBotVerificationOnLayer228Bits is the user half: the icon
// on user#b1b8cc83 flags2.14 and the full payload on userFull#6cbe645 flags2.12.
func TestUserAndUserFullCarryBotVerificationOnLayer228Bits(t *testing.T) {
	f := newBotVerificationFixture(t, newFakeBotVerifications())
	f.enableVerifier(f.bot.ID, 8800001, true)
	ownerCtx := WithUserID(context.Background(), f.owner.ID)

	plainUser := getUsersUser(t, f.router, f.owner.ID, f.target)
	if _, ok := plainUser.GetBotVerificationIcon(); ok {
		t.Fatalf("unmarked user carries an icon: %+v", plainUser)
	}
	plainFull, _ := getFullUserProjection(t, f.router, f.owner.ID, f.target)
	if _, ok := plainFull.GetBotVerification(); ok {
		t.Fatalf("unmarked userFull carries a mark: %+v", plainFull)
	}

	if ok, err := f.router.onBotsSetCustomVerification(ownerCtx,
		setCustomVerificationRequest(inputPeerUser(f.target), inputUser(f.bot), true, "Official reseller")); err != nil || !ok {
		t.Fatalf("grant = %v,%v, want true,nil", ok, err)
	}

	markedUser := getUsersUser(t, f.router, f.owner.ID, f.target)
	icon, ok := markedUser.GetBotVerificationIcon()
	if !ok || icon != 8800001 {
		t.Fatalf("user bot_verification_icon = %d, ok=%v, want 8800001", icon, ok)
	}
	assertFlagBitDelta(t, "user", plainUser.Flags2, markedUser.Flags2, 14)
	if uint32(plainUser.Flags) != uint32(markedUser.Flags) {
		t.Fatalf("user flags word changed: before %032b after %032b", uint32(plainUser.Flags), uint32(markedUser.Flags))
	}
	// The operator-granted checkmark is a different mechanism and must stay clear.
	if markedUser.Verified || markedUser.Flags.Has(17) {
		t.Fatalf("third-party mark leaked into official verified:flags.17: %+v", markedUser)
	}

	markedFull, _ := getFullUserProjection(t, f.router, f.owner.ID, f.target)
	mark, ok := markedFull.GetBotVerification()
	if !ok {
		t.Fatalf("userFull bot_verification unset: %+v", markedFull)
	}
	if mark.BotID != f.bot.ID || mark.Icon != 8800001 || mark.Description != "Official reseller" {
		t.Fatalf("userFull bot_verification = %+v, want verifier %d icon 8800001", mark, f.bot.ID)
	}
	assertFlagBitDelta(t, "userFull", plainFull.Flags2, markedFull.Flags2, 12)
	if uint32(plainFull.Flags) != uint32(markedFull.Flags) {
		t.Fatalf("userFull flags word changed: before %032b after %032b", uint32(plainFull.Flags), uint32(markedFull.Flags))
	}

	// Revoking clears the bit again on both surfaces: the payload is an overlay, so
	// it must not survive inside the userFull projection cache.
	if ok, err := f.router.onBotsSetCustomVerification(ownerCtx,
		setCustomVerificationRequest(inputPeerUser(f.target), inputUser(f.bot), false, "")); err != nil || !ok {
		t.Fatalf("revoke = %v,%v, want true,nil", ok, err)
	}
	if revokedUser := getUsersUser(t, f.router, f.owner.ID, f.target); revokedUser.Flags2.Has(14) {
		t.Fatalf("revoked user still carries flags2.14: %032b", uint32(revokedUser.Flags2))
	}
	revokedFull, _ := getFullUserProjection(t, f.router, f.owner.ID, f.target)
	if revokedFull.Flags2.Has(12) {
		t.Fatalf("revoked userFull still carries flags2.12: %032b", uint32(revokedFull.Flags2))
	}
}

// TestBotInfoCarriesVerifierSettingsOnFlags9 pins botInfo#4d8a0299
// verifier_settings:flags.9 inside the verifier bot's own userFull, and the operator
// kill switch: a disabled verifier stops advertising itself.
func TestBotInfoCarriesVerifierSettingsOnFlags9(t *testing.T) {
	f := newBotVerificationFixture(t, newFakeBotVerifications())

	_, plainInfo := getFullUserProjection(t, f.router, f.owner.ID, f.bot)
	if plainInfo == nil {
		t.Fatal("bot userFull carries no bot_info at all")
	}
	if _, ok := plainInfo.GetVerifierSettings(); ok {
		t.Fatalf("ordinary bot advertises verifier settings: %+v", plainInfo)
	}

	f.enableVerifier(f.bot.ID, 8800002, true)
	f.router.invalidateRPCProjectionForUser(f.bot.ID)
	_, markedInfo := getFullUserProjection(t, f.router, f.owner.ID, f.bot)
	if markedInfo == nil {
		t.Fatal("verifier bot userFull carries no bot_info")
	}
	settings, ok := markedInfo.GetVerifierSettings()
	if !ok {
		t.Fatalf("verifier bot bot_info has no verifier_settings: %+v", markedInfo)
	}
	if settings.Icon != 8800002 || settings.Company != "Acme Trust" || !settings.GetCanModifyCustomDescription() {
		t.Fatalf("verifier_settings = %+v", settings)
	}
	assertFlagBitDelta(t, "botInfo", plainInfo.Flags, markedInfo.Flags, 9)

	// Operator kill switch: the row stays, the advertisement stops.
	disabled := f.verify.settings[f.bot.ID]
	disabled.Enabled = false
	f.verify.settings[f.bot.ID] = disabled
	f.router.invalidateRPCProjectionForUser(f.bot.ID)
	_, offInfo := getFullUserProjection(t, f.router, f.owner.ID, f.bot)
	if offInfo == nil || offInfo.Flags.Has(9) {
		t.Fatalf("disabled verifier still advertises verifier_settings: %+v", offInfo)
	}
}

// botVerificationGroup creates a megagroup owned by the fixture owner and returns its
// projected channel object.
func (f botVerificationFixture) botVerificationGroup(t *testing.T, title string) *tg.Channel {
	t.Helper()
	created, err := f.router.onMessagesCreateChat(WithUserID(context.Background(), f.owner.ID),
		&tg.MessagesCreateChatRequest{
			Users: []tg.InputUserClass{inputUser(f.stranger)},
			Title: title,
		})
	if err != nil {
		t.Fatalf("create chat: %v", err)
	}
	return created.Updates.(*tg.Updates).Chats[0].(*tg.Channel)
}

func getChannelsChannel(t *testing.T, r *Router, viewerUserID int64, channel *tg.Channel) *tg.Channel {
	t.Helper()
	res, err := r.onChannelsGetChannels(WithUserID(context.Background(), viewerUserID),
		[]tg.InputChannelClass{&tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash}})
	if err != nil {
		t.Fatalf("get channels: %v", err)
	}
	chats := res.(*tg.MessagesChats).Chats
	if len(chats) != 1 {
		t.Fatalf("get channels returned %d chats, want 1", len(chats))
	}
	decoded := &tg.Channel{}
	tlRoundTrip(t, chats[0].(*tg.Channel), decoded)
	return decoded
}

func getFullChannelProjection(t *testing.T, r *Router, viewerUserID int64, channel *tg.Channel) *tg.ChannelFull {
	t.Helper()
	res, err := r.onChannelsGetFullChannel(WithUserID(context.Background(), viewerUserID),
		&tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash})
	if err != nil {
		t.Fatalf("get full channel: %v", err)
	}
	decoded := &tg.ChannelFull{}
	tlRoundTrip(t, res.FullChat.(*tg.ChannelFull), decoded)
	return decoded
}

// TestChannelAndChannelFullCarryBotVerificationOnLayer228Bits is the channel half:
// channel#d49f34c6 flags2.13 and channelFull#a04e8d3a flags2.17. Note the two bits
// differ from the user ones, which is exactly the mistake this test exists to catch.
func TestChannelAndChannelFullCarryBotVerificationOnLayer228Bits(t *testing.T) {
	f := newBotVerificationFixture(t, newFakeBotVerifications())
	f.enableVerifier(f.bot.ID, 8800003, true)
	ownerCtx := WithUserID(context.Background(), f.owner.ID)
	group := f.botVerificationGroup(t, "Verified Group")

	plainChannel := getChannelsChannel(t, f.router, f.owner.ID, group)
	if _, ok := plainChannel.GetBotVerificationIcon(); ok {
		t.Fatalf("unmarked channel carries an icon: %+v", plainChannel)
	}
	plainFull := getFullChannelProjection(t, f.router, f.owner.ID, group)
	if _, ok := plainFull.GetBotVerification(); ok {
		t.Fatalf("unmarked channelFull carries a mark: %+v", plainFull)
	}

	if ok, err := f.router.onBotsSetCustomVerification(ownerCtx, setCustomVerificationRequest(
		&tg.InputPeerChannel{ChannelID: group.ID, AccessHash: group.AccessHash},
		inputUser(f.bot), true, "Community partner")); err != nil || !ok {
		t.Fatalf("grant on channel = %v,%v, want true,nil", ok, err)
	}

	markedChannel := getChannelsChannel(t, f.router, f.owner.ID, group)
	icon, ok := markedChannel.GetBotVerificationIcon()
	if !ok || icon != 8800003 {
		t.Fatalf("channel bot_verification_icon = %d, ok=%v, want 8800003", icon, ok)
	}
	assertFlagBitDelta(t, "channel", plainChannel.Flags2, markedChannel.Flags2, 13)
	if uint32(plainChannel.Flags) != uint32(markedChannel.Flags) {
		t.Fatalf("channel flags word changed: before %032b after %032b",
			uint32(plainChannel.Flags), uint32(markedChannel.Flags))
	}
	if markedChannel.Verified || markedChannel.Flags.Has(7) {
		t.Fatalf("third-party mark leaked into official verified:flags.7: %+v", markedChannel)
	}

	markedFull := getFullChannelProjection(t, f.router, f.owner.ID, group)
	mark, ok := markedFull.GetBotVerification()
	if !ok {
		t.Fatalf("channelFull bot_verification unset: %+v", markedFull)
	}
	if mark.BotID != f.bot.ID || mark.Icon != 8800003 || mark.Description != "Community partner" {
		t.Fatalf("channelFull bot_verification = %+v, want verifier %d icon 8800003", mark, f.bot.ID)
	}
	assertFlagBitDelta(t, "channelFull", plainFull.Flags2, markedFull.Flags2, 17)
	if uint32(plainFull.Flags) != uint32(markedFull.Flags) {
		t.Fatalf("channelFull flags word changed: before %032b after %032b",
			uint32(plainFull.Flags), uint32(markedFull.Flags))
	}

	if ok, err := f.router.onBotsSetCustomVerification(ownerCtx, setCustomVerificationRequest(
		&tg.InputPeerChannel{ChannelID: group.ID, AccessHash: group.AccessHash},
		inputUser(f.bot), false, "")); err != nil || !ok {
		t.Fatalf("revoke on channel = %v,%v, want true,nil", ok, err)
	}
	if revoked := getChannelsChannel(t, f.router, f.owner.ID, group); revoked.Flags2.Has(13) {
		t.Fatalf("revoked channel still carries flags2.13: %032b", uint32(revoked.Flags2))
	}
	if revokedFull := getFullChannelProjection(t, f.router, f.owner.ID, group); revokedFull.Flags2.Has(17) {
		t.Fatalf("revoked channelFull still carries flags2.17: %032b", uint32(revokedFull.Flags2))
	}
}

// TestChannelFullBotInfoCarriesVerifierSettingsInOneBatch covers the batched botInfo
// path (channelFull.bot_info): the verifier settings for the whole bot list must cost
// one query, not one per bot.
func TestChannelFullBotInfoCarriesVerifierSettingsInOneBatch(t *testing.T) {
	ctx := context.Background()
	f := newBotVerificationFixture(t, newFakeBotVerifications())
	f.enableVerifier(f.bot.ID, 8800004, true)
	group := f.botVerificationGroup(t, "Bot Group")
	if _, err := f.bots.SetJoinGroups(ctx, f.bot.ID, true); err != nil {
		t.Fatalf("enable join groups: %v", err)
	}
	if _, err := f.router.onChannelsInviteToChannel(WithUserID(ctx, f.owner.ID), &tg.ChannelsInviteToChannelRequest{
		Channel: &tg.InputChannel{ChannelID: group.ID, AccessHash: group.AccessHash},
		Users:   []tg.InputUserClass{inputUser(f.bot)},
	}); err != nil {
		t.Fatalf("invite verifier bot: %v", err)
	}

	f.verify.settingsBatchCalls = 0
	f.verify.settingsCalls = 0
	full := getFullChannelProjection(t, f.router, f.owner.ID, group)
	if len(full.BotInfo) != 1 || full.BotInfo[0].UserID != f.bot.ID {
		t.Fatalf("channelFull bot_info = %+v, want the verifier bot", full.BotInfo)
	}
	settings, ok := full.BotInfo[0].GetVerifierSettings()
	if !ok || !full.BotInfo[0].Flags.Has(9) {
		t.Fatalf("channelFull bot_info verifier_settings unset: %+v", full.BotInfo[0])
	}
	if settings.Icon != 8800004 || settings.Company != "Acme Trust" {
		t.Fatalf("channelFull verifier_settings = %+v", settings)
	}
	if f.verify.settingsBatchCalls != 1 || f.verify.settingsCalls != 0 {
		t.Fatalf("verifier settings reads = batch %d / single %d, want batch 1 / single 0",
			f.verify.settingsBatchCalls, f.verify.settingsCalls)
	}
}

// TestChatInviteCarriesBotVerificationOnFlags13 pins chatInvite#5c9d3702
// bot_verification:flags.13 on the preview a non-member sees.
func TestChatInviteCarriesBotVerificationOnFlags13(t *testing.T) {
	const channelID = int64(4242)
	channel := domain.Channel{
		ID: channelID, AccessHash: 42, Title: "Partner", Username: "partner",
		Broadcast: true, ParticipantsCount: 11,
	}
	verify := newFakeBotVerifications()
	r := New(Config{}, Deps{
		Channels: &inviteBadgeChannels{result: domain.CheckChannelInviteResult{
			Channel: channel,
			Invite:  domain.ChannelInvite{Hash: "hash", ChannelID: channelID},
		}},
		BotVerifications: verify,
	}, zap.NewNop(), clock.System)
	ctx := WithUserID(context.Background(), 1001)

	preview := func() *tg.ChatInvite {
		t.Helper()
		res, err := r.onMessagesCheckChatInvite(ctx, "hash")
		if err != nil {
			t.Fatalf("check chat invite: %v", err)
		}
		decoded := &tg.ChatInvite{}
		tlRoundTrip(t, res.(*tg.ChatInvite), decoded)
		return decoded
	}

	plain := preview()
	if _, ok := plain.GetBotVerification(); ok {
		t.Fatalf("unmarked invite carries a mark: %+v", plain)
	}

	verify.marks[domain.Peer{Type: domain.PeerTypeChannel, ID: channelID}] = domain.CustomVerification{
		VerifierBotID: 777000123, Peer: domain.Peer{Type: domain.PeerTypeChannel, ID: channelID},
		IconDocumentID: 8800005, Description: "Verified by Acme Trust",
	}
	marked := preview()
	mark, ok := marked.GetBotVerification()
	if !ok {
		t.Fatalf("invite bot_verification unset: %+v", marked)
	}
	if mark.BotID != 777000123 || mark.Icon != 8800005 || mark.Description != "Verified by Acme Trust" {
		t.Fatalf("invite bot_verification = %+v", mark)
	}
	assertFlagBitDelta(t, "chatInvite", plain.Flags, marked.Flags, 13)
	// The official badge and the moderation warnings are untouched.
	if marked.GetVerified() || marked.GetScam() || marked.GetFake() {
		t.Fatalf("third-party mark leaked into the moderation flags: %+v", marked)
	}
	if !marked.Channel || !marked.Broadcast || !marked.Public ||
		marked.Title != "Partner" || marked.ParticipantsCount != 11 {
		t.Fatalf("invite lost unrelated fields: %+v", marked)
	}
}

// TestBotVerificationDegradesWithoutService is the whole-feature degradation test:
// with no verification service wired, not one of the six flags may be set, and every
// response must stay exactly what it was before the feature existed.
func TestBotVerificationDegradesWithoutService(t *testing.T) {
	ctx := context.Background()
	f := newBotVerificationFixture(t, nil)
	group := f.botVerificationGroup(t, "Plain Group")
	if _, err := f.bots.SetJoinGroups(ctx, f.bot.ID, true); err != nil {
		t.Fatalf("enable join groups: %v", err)
	}
	if _, err := f.router.onChannelsInviteToChannel(WithUserID(ctx, f.owner.ID), &tg.ChannelsInviteToChannelRequest{
		Channel: &tg.InputChannel{ChannelID: group.ID, AccessHash: group.AccessHash},
		Users:   []tg.InputUserClass{inputUser(f.bot)},
	}); err != nil {
		t.Fatalf("invite bot: %v", err)
	}

	if user := getUsersUser(t, f.router, f.owner.ID, f.target); user.Flags2.Has(14) {
		t.Fatalf("user set flags2.14 without a service: %032b", uint32(user.Flags2))
	}
	userFull, botInfo := getFullUserProjection(t, f.router, f.owner.ID, f.bot)
	if userFull.Flags2.Has(12) {
		t.Fatalf("userFull set flags2.12 without a service: %032b", uint32(userFull.Flags2))
	}
	if botInfo == nil {
		t.Fatal("bot userFull carries no bot_info")
	}
	if botInfo.Flags.Has(9) {
		t.Fatalf("botInfo set flags.9 without a service: %032b", uint32(botInfo.Flags))
	}
	if channel := getChannelsChannel(t, f.router, f.owner.ID, group); channel.Flags2.Has(13) {
		t.Fatalf("channel set flags2.13 without a service: %032b", uint32(channel.Flags2))
	}
	full := getFullChannelProjection(t, f.router, f.owner.ID, group)
	if full.Flags2.Has(17) {
		t.Fatalf("channelFull set flags2.17 without a service: %032b", uint32(full.Flags2))
	}
	if len(full.BotInfo) != 1 || full.BotInfo[0].Flags.Has(9) {
		t.Fatalf("channelFull bot_info set flags.9 without a service: %+v", full.BotInfo)
	}

	invite := New(Config{}, Deps{Channels: &inviteBadgeChannels{result: domain.CheckChannelInviteResult{
		Channel: domain.Channel{ID: 4343, AccessHash: 43, Title: "Plain", Broadcast: true},
		Invite:  domain.ChannelInvite{Hash: "hash", ChannelID: 4343},
	}}}, zaptest.NewLogger(t), clock.System)
	res, err := invite.onMessagesCheckChatInvite(WithUserID(ctx, 1001), "hash")
	if err != nil {
		t.Fatalf("check chat invite: %v", err)
	}
	decodedInvite := &tg.ChatInvite{}
	tlRoundTrip(t, res.(*tg.ChatInvite), decodedInvite)
	if decodedInvite.Flags.Has(13) {
		t.Fatalf("chatInvite set flags.13 without a service: %032b", uint32(decodedInvite.Flags))
	}
}

// TestBotVerificationDegradesWhenServiceFails is the other half of the degradation
// contract: a failing read model must be indistinguishable from an unmarked peer, so
// a storage blip cannot turn a peer response into an error.
func TestBotVerificationDegradesWhenServiceFails(t *testing.T) {
	verify := newFakeBotVerifications()
	verify.err = context.DeadlineExceeded
	f := newBotVerificationFixture(t, verify)

	if user := getUsersUser(t, f.router, f.owner.ID, f.target); user.Flags2.Has(14) {
		t.Fatalf("failing service still set flags2.14: %032b", uint32(user.Flags2))
	}
	full, botInfo := getFullUserProjection(t, f.router, f.owner.ID, f.bot)
	if full.Flags2.Has(12) {
		t.Fatalf("failing service still set flags2.12: %032b", uint32(full.Flags2))
	}
	if botInfo == nil || botInfo.Flags.Has(9) {
		t.Fatalf("failing service still set botInfo flags.9: %+v", botInfo)
	}
}

// TestUsersGetUsersResolvesBotVerificationInOneBatch pins the absence of an N+1: the
// icon overlay runs once per response over the whole user set, never once per user.
func TestUsersGetUsersResolvesBotVerificationInOneBatch(t *testing.T) {
	verify := newFakeBotVerifications()
	f := newBotVerificationFixture(t, verify)
	verify.marks[domain.Peer{Type: domain.PeerTypeUser, ID: f.target.ID}] = domain.CustomVerification{
		VerifierBotID: f.bot.ID, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: f.target.ID},
		IconDocumentID: 8800006, Description: "Verified by Acme Trust",
	}
	verify.marks[domain.Peer{Type: domain.PeerTypeUser, ID: f.stranger.ID}] = domain.CustomVerification{
		VerifierBotID: f.bot.ID, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: f.stranger.ID},
		IconDocumentID: 8800007, Description: "Verified by Acme Trust",
	}
	verify.peerCalls = 0
	verify.batchCalls = 0

	out, err := f.router.onUsersGetUsers(WithUserID(context.Background(), f.owner.ID), []tg.InputUserClass{
		&tg.InputUserSelf{},
		&tg.InputUser{UserID: f.target.ID, AccessHash: f.target.AccessHash},
		&tg.InputUser{UserID: f.stranger.ID, AccessHash: f.stranger.AccessHash},
	})
	if err != nil {
		t.Fatalf("get users: %v", err)
	}
	if len(out) != 3 {
		t.Fatalf("users = %d, want 3", len(out))
	}
	icons := map[int64]int64{}
	for _, item := range out {
		decoded := &tg.User{}
		tlRoundTrip(t, item.(*tg.User), decoded)
		if icon, ok := decoded.GetBotVerificationIcon(); ok {
			if !decoded.Flags2.Has(14) {
				t.Fatalf("icon set without flags2.14 on user %d: %032b", decoded.ID, uint32(decoded.Flags2))
			}
			icons[decoded.ID] = icon
		}
	}
	if icons[f.target.ID] != 8800006 || icons[f.stranger.ID] != 8800007 {
		t.Fatalf("projected icons = %v", icons)
	}
	if _, marked := icons[f.owner.ID]; marked {
		t.Fatalf("unmarked self got an icon: %v", icons)
	}
	// Three users, one batch read: no N+1.
	if verify.batchCalls != 1 || verify.peerCalls != 0 {
		t.Fatalf("verification reads = batch %d / peer %d, want batch 1 / peer 0",
			verify.batchCalls, verify.peerCalls)
	}
}
