package rpc

import (
	"context"
	"testing"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"

	"telesrv/internal/domain"
)

func TestBotsLongtailReadStubsReturnEmptyFacts(t *testing.T) {
	ctx := context.Background()
	f := newInlineBotRPCTestFixture(t)
	userCtx := WithUserID(ctx, f.owner.ID)

	can, err := f.router.onBotsCanSendMessage(userCtx, inputUser(f.bot))
	if err != nil || can {
		t.Fatalf("canSendMessage = %v,%v, want false,nil", can, err)
	}

	popular, err := f.router.onBotsGetPopularAppBots(userCtx, &tg.BotsGetPopularAppBotsRequest{Limit: 20})
	if err != nil {
		t.Fatalf("getPopularAppBots: %v", err)
	}
	if next, ok := popular.GetNextOffset(); ok || next != "" || len(popular.Users) != 0 {
		t.Fatalf("popular app bots = %+v, want empty without next offset", popular)
	}

	info, err := f.router.onBotsGetPreviewInfo(userCtx, &tg.BotsGetPreviewInfoRequest{
		Bot:      inputUser(f.bot),
		LangCode: "en",
	})
	if err != nil {
		t.Fatalf("getPreviewInfo: %v", err)
	}
	if len(info.Media) != 0 || len(info.LangCodes) != 0 {
		t.Fatalf("preview info = %+v, want empty", info)
	}

	medias, err := f.router.onBotsGetPreviewMedias(userCtx, inputUser(f.bot))
	if err != nil {
		t.Fatalf("getPreviewMedias: %v", err)
	}
	if len(medias) != 0 {
		t.Fatalf("preview medias len = %d, want 0", len(medias))
	}

	access, err := f.router.onBotsGetAccessSettings(userCtx, inputUser(f.bot))
	if err != nil {
		t.Fatalf("getAccessSettings: %v", err)
	}
	if access.GetRestricted() {
		t.Fatalf("access settings = %+v, want unrestricted empty", access)
	}

	recs, err := f.router.onBotsGetBotRecommendations(userCtx, inputUser(f.bot))
	if err != nil {
		t.Fatalf("getBotRecommendations: %v", err)
	}
	if got := len(recs.(*tg.UsersUsers).Users); got != 0 {
		t.Fatalf("recommendations len = %d, want 0", got)
	}

	recs, err = f.router.onBotsGetBotRecommendations(userCtx, inputUser(f.owner))
	if err != nil {
		t.Fatalf("getBotRecommendations non-bot target: %v", err)
	}
	if got := len(recs.(*tg.UsersUsers).Users); got != 0 {
		t.Fatalf("recommendations for non-bot target len = %d, want 0", got)
	}
}

func TestBotsWriteAccessAllowSendMessageRoundTrip(t *testing.T) {
	ctx := context.Background()
	f := newInlineBotRPCTestFixture(t)
	userCtx := WithUserID(ctx, f.owner.ID)

	can, err := f.router.onBotsCanSendMessage(userCtx, inputUser(f.bot))
	if err != nil || can {
		t.Fatalf("can before allow = %v,%v, want false,nil", can, err)
	}
	updatesClass, err := f.router.onBotsAllowSendMessage(userCtx, inputUser(f.bot))
	if err != nil {
		t.Fatalf("allowSendMessage: %v", err)
	}
	updates := updatesClass.(*tg.Updates)
	if len(updates.Updates) != 1 {
		t.Fatalf("allow updates len = %d, want one service message", len(updates.Updates))
	}
	newMessage, ok := updates.Updates[0].(*tg.UpdateNewMessage)
	if !ok {
		t.Fatalf("allow update = %T, want UpdateNewMessage", updates.Updates[0])
	}
	service, ok := newMessage.Message.(*tg.MessageService)
	if !ok {
		t.Fatalf("allow message = %T, want MessageService", newMessage.Message)
	}
	action, ok := service.Action.(*tg.MessageActionBotAllowed)
	if !ok || !action.FromRequest {
		t.Fatalf("allow action = %+v (%T), want messageActionBotAllowed from_request", service.Action, service.Action)
	}
	if service.PeerID.(*tg.PeerUser).UserID != f.bot.ID || service.FromID.(*tg.PeerUser).UserID != f.owner.ID {
		t.Fatalf("allow service peer/from = %+v/%+v, want bot/user", service.PeerID, service.FromID)
	}

	can, err = f.router.onBotsCanSendMessage(userCtx, inputUser(f.bot))
	if err != nil || !can {
		t.Fatalf("can after allow = %v,%v, want true,nil", can, err)
	}
	repeat, err := f.router.onBotsAllowSendMessage(userCtx, inputUser(f.bot))
	if err != nil {
		t.Fatalf("repeat allowSendMessage: %v", err)
	}
	if got := len(repeat.(*tg.Updates).Updates); got != 0 {
		t.Fatalf("repeat allow updates len = %d, want empty", got)
	}
	history, err := f.router.deps.Messages.GetHistory(ctx, f.owner.ID, domain.MessageFilter{
		HasPeer: true,
		Peer:    domain.Peer{Type: domain.PeerTypeUser, ID: f.bot.ID},
		Limit:   10,
	})
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	serviceMessages := 0
	for _, msg := range history.Messages {
		if msg.Media != nil && msg.Media.ServiceAction != nil && msg.Media.ServiceAction.Kind == domain.MessageServiceActionBotAllowed {
			serviceMessages++
		}
	}
	if serviceMessages != 1 {
		t.Fatalf("bot allowed service messages = %d, want 1", serviceMessages)
	}
}

func TestBotsLongtailRejectsMissingState(t *testing.T) {
	ctx := context.Background()
	f := newInlineBotRPCTestFixture(t)
	userCtx := WithUserID(ctx, f.owner.ID)
	botCtx := WithUserID(ctx, f.bot.ID)

	if _, err := f.router.onBotsInvokeWebViewCustomMethod(userCtx, &tg.BotsInvokeWebViewCustomMethodRequest{
		Bot:          inputUser(f.bot),
		CustomMethod: "unsupported",
		Params:       tg.DataJSON{Data: "{}"},
	}); !tgerr.Is(err, "METHOD_INVALID") {
		t.Fatalf("invoke custom method err = %v, want METHOD_INVALID", err)
	}
	if _, err := f.router.onBotsAddPreviewMedia(userCtx, &tg.BotsAddPreviewMediaRequest{
		Bot:   inputUser(f.bot),
		Media: &tg.InputMediaEmpty{},
	}); !tgerr.Is(err, "BOT_APP_INVALID") {
		t.Fatalf("add preview media err = %v, want BOT_APP_INVALID", err)
	}
	if ok, err := f.router.onBotsDeletePreviewMedia(userCtx, &tg.BotsDeletePreviewMediaRequest{
		Bot:   inputUser(f.bot),
		Media: []tg.InputMediaClass{&tg.InputMediaEmpty{}},
	}); ok || !tgerr.Is(err, "BOT_APP_INVALID") {
		t.Fatalf("delete preview media = %v,%v, want false,BOT_APP_INVALID", ok, err)
	}
	if _, err := f.router.onBotsGetRequestedWebViewButton(userCtx, &tg.BotsGetRequestedWebViewButtonRequest{
		Bot:         inputUser(f.bot),
		WebappReqID: "req",
	}); !tgerr.Is(err, "BUTTON_DATA_INVALID") {
		t.Fatalf("get requested button err = %v, want BUTTON_DATA_INVALID", err)
	}
	if ok, err := f.router.onBotsCheckDownloadFileParams(userCtx, &tg.BotsCheckDownloadFileParamsRequest{
		Bot:      inputUser(f.bot),
		FileName: "file.txt",
		URL:      "https://127.0.0.1/file.txt",
	}); ok || err != nil {
		t.Fatalf("check download params = %v,%v, want false,nil", ok, err)
	}
	if ok, err := f.router.onBotsUpdateUserEmojiStatus(botCtx, &tg.BotsUpdateUserEmojiStatusRequest{
		UserID:      inputUser(f.owner),
		EmojiStatus: &tg.EmojiStatusEmpty{},
	}); ok || !tgerr.Is(err, "USER_PERMISSION_DENIED") {
		t.Fatalf("update emoji status = %v,%v, want false,USER_PERMISSION_DENIED", ok, err)
	}
	if ok, err := f.router.onBotsToggleUserEmojiStatusPermission(userCtx, &tg.BotsToggleUserEmojiStatusPermissionRequest{
		Bot:     inputUser(f.bot),
		Enabled: true,
	}); !ok || err != nil {
		t.Fatalf("toggle emoji permission = %v,%v, want true,nil", ok, err)
	}
	if ok, err := f.router.onBotsUpdateUserEmojiStatus(botCtx, &tg.BotsUpdateUserEmojiStatusRequest{
		UserID:      inputUser(f.owner),
		EmojiStatus: &tg.EmojiStatusEmpty{},
	}); !ok || err != nil {
		t.Fatalf("update emoji status after permission = %v,%v, want true,nil", ok, err)
	}
	if _, err := f.router.onBotsSendCustomRequest(botCtx, &tg.BotsSendCustomRequestRequest{
		CustomMethod: "unsupported",
		Params:       tg.DataJSON{Data: "{}"},
	}); !tgerr.Is(err, "METHOD_INVALID") {
		t.Fatalf("send custom request err = %v, want METHOD_INVALID", err)
	}
	if ok, err := f.router.onBotsAnswerWebhookJSONQuery(botCtx, &tg.BotsAnswerWebhookJSONQueryRequest{
		QueryID: 1,
		Data:    tg.DataJSON{Data: "{}"},
	}); ok || !tgerr.Is(err, "QUERY_ID_INVALID") {
		t.Fatalf("answer webhook json query = %v,%v, want false,QUERY_ID_INVALID", ok, err)
	}
}

func TestBotsLongtailCommercialAndSettingsStubs(t *testing.T) {
	ctx := context.Background()
	f := newInlineBotRPCTestFixture(t)
	userCtx := WithUserID(ctx, f.owner.ID)
	botCtx := WithUserID(ctx, f.bot.ID)

	if ok, err := f.router.onBotsSetBotBroadcastDefaultAdminRights(botCtx, tg.ChatAdminRights{}); ok || !tgerr.Is(err, "RIGHTS_NOT_MODIFIED") {
		t.Fatalf("broadcast default rights = %v,%v, want false,RIGHTS_NOT_MODIFIED", ok, err)
	}
	if ok, err := f.router.onBotsSetBotGroupDefaultAdminRights(botCtx, tg.ChatAdminRights{}); ok || !tgerr.Is(err, "RIGHTS_NOT_MODIFIED") {
		t.Fatalf("group default rights = %v,%v, want false,RIGHTS_NOT_MODIFIED", ok, err)
	}
	if ok, err := f.router.onBotsReorderUsernames(userCtx, &tg.BotsReorderUsernamesRequest{
		Bot:   inputUser(f.bot),
		Order: []string{f.bot.Username},
	}); ok || !tgerr.Is(err, "USERNAME_NOT_MODIFIED") {
		t.Fatalf("reorder usernames = %v,%v, want false,USERNAME_NOT_MODIFIED", ok, err)
	}
	if ok, err := f.router.onBotsToggleUsername(userCtx, &tg.BotsToggleUsernameRequest{
		Bot:      inputUser(f.bot),
		Username: f.bot.Username,
		Active:   true,
	}); ok || !tgerr.Is(err, "USERNAME_NOT_MODIFIED") {
		t.Fatalf("toggle username = %v,%v, want false,USERNAME_NOT_MODIFIED", ok, err)
	}
	if _, err := f.router.onBotsUpdateStarRefProgram(userCtx, &tg.BotsUpdateStarRefProgramRequest{
		Bot:                inputUser(f.bot),
		CommissionPermille: 100,
	}); !tgerr.Is(err, "BOT_INVALID") {
		t.Fatalf("update star ref program err = %v, want BOT_INVALID", err)
	}
	if ok, err := f.router.onBotsSetCustomVerification(userCtx, func() *tg.BotsSetCustomVerificationRequest {
		req := &tg.BotsSetCustomVerificationRequest{Peer: inputPeerUser(f.peer)}
		req.SetBot(inputUser(f.bot))
		req.SetEnabled(true)
		return req
	}()); ok || !tgerr.Is(err, "BOT_VERIFIER_FORBIDDEN") {
		t.Fatalf("set custom verification = %v,%v, want false,BOT_VERIFIER_FORBIDDEN", ok, err)
	}
	if ok, err := f.router.onBotsEditAccessSettings(userCtx, &tg.BotsEditAccessSettingsRequest{
		Bot: inputUser(f.bot),
	}); ok || !tgerr.Is(err, "BOT_INVALID") {
		t.Fatalf("edit access settings = %v,%v, want false,BOT_INVALID", ok, err)
	}
	if _, err := f.router.onBotsRequestWebViewButton(botCtx, &tg.BotsRequestWebViewButtonRequest{
		UserID: inputUser(f.owner),
		Button: &tg.KeyboardButtonSimpleWebView{
			Text: "Open",
			URL:  "https://example.com/app",
		},
	}); !tgerr.Is(err, "BUTTON_DATA_INVALID") {
		t.Fatalf("request webview button err = %v, want BUTTON_DATA_INVALID", err)
	}
}

func TestBotsLongtailExplicitStubsAreRegistered(t *testing.T) {
	ctx := context.Background()
	f := newInlineBotRPCTestFixture(t)
	userCtx := WithUserID(ctx, f.owner.ID)
	botCtx := WithUserID(ctx, f.bot.ID)

	userRequests := []struct {
		name string
		req  bin.Encoder
		want string
	}{
		{name: "invokeWebViewCustomMethod", req: &tg.BotsInvokeWebViewCustomMethodRequest{Bot: inputUser(f.bot), CustomMethod: "x", Params: tg.DataJSON{Data: "{}"}}, want: "METHOD_INVALID"},
		{name: "addPreviewMedia", req: &tg.BotsAddPreviewMediaRequest{Bot: inputUser(f.bot), Media: &tg.InputMediaEmpty{}}, want: "BOT_APP_INVALID"},
		{name: "getRequestedWebViewButton", req: &tg.BotsGetRequestedWebViewButtonRequest{Bot: inputUser(f.bot), WebappReqID: "req"}, want: "BUTTON_DATA_INVALID"},
		{name: "reorderUsernames", req: &tg.BotsReorderUsernamesRequest{Bot: inputUser(f.bot), Order: []string{f.bot.Username}}, want: "USERNAME_NOT_MODIFIED"},
		{name: "toggleUsername", req: &tg.BotsToggleUsernameRequest{Bot: inputUser(f.bot), Username: f.bot.Username, Active: true}, want: "USERNAME_NOT_MODIFIED"},
		{name: "editAccessSettings", req: &tg.BotsEditAccessSettingsRequest{Bot: inputUser(f.bot)}, want: "BOT_INVALID"},
	}
	for _, tt := range userRequests {
		t.Run(tt.name, func(t *testing.T) {
			var in bin.Buffer
			if err := tt.req.Encode(&in); err != nil {
				t.Fatalf("encode request: %v", err)
			}
			if _, err := f.router.Dispatch(userCtx, [8]byte{}, 0, &in); !tgerr.Is(err, tt.want) {
				t.Fatalf("dispatch err = %v, want %s", err, tt.want)
			}
		})
	}

	botRequests := []struct {
		name string
		req  bin.Encoder
		want string
	}{
		{name: "sendCustomRequest", req: &tg.BotsSendCustomRequestRequest{CustomMethod: "x", Params: tg.DataJSON{Data: "{}"}}, want: "METHOD_INVALID"},
		{name: "answerWebhookJSONQuery", req: &tg.BotsAnswerWebhookJSONQueryRequest{QueryID: 1, Data: tg.DataJSON{Data: "{}"}}, want: "QUERY_ID_INVALID"},
		{name: "setBotBroadcastDefaultAdminRights", req: &tg.BotsSetBotBroadcastDefaultAdminRightsRequest{AdminRights: tg.ChatAdminRights{}}, want: "RIGHTS_NOT_MODIFIED"},
		{name: "setBotGroupDefaultAdminRights", req: &tg.BotsSetBotGroupDefaultAdminRightsRequest{AdminRights: tg.ChatAdminRights{}}, want: "RIGHTS_NOT_MODIFIED"},
		{name: "updateUserEmojiStatus", req: &tg.BotsUpdateUserEmojiStatusRequest{UserID: inputUser(f.owner), EmojiStatus: &tg.EmojiStatusEmpty{}}, want: "USER_PERMISSION_DENIED"},
	}
	for _, tt := range botRequests {
		t.Run(tt.name, func(t *testing.T) {
			var in bin.Buffer
			if err := tt.req.Encode(&in); err != nil {
				t.Fatalf("encode request: %v", err)
			}
			if _, err := f.router.Dispatch(botCtx, [8]byte{}, 0, &in); !tgerr.Is(err, tt.want) {
				t.Fatalf("dispatch err = %v, want %s", err, tt.want)
			}
		})
	}
}
