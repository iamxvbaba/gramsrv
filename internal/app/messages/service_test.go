package messages

import (
	"context"
	"errors"
	"testing"

	"telesrv/internal/app/account"
	"telesrv/internal/app/readmodel"
	"telesrv/internal/domain"
	"telesrv/internal/store"
	"telesrv/internal/store/memory"
)

func TestServiceSendPrivateTextHonorsSendPermissionGate(t *testing.T) {
	ctx := context.Background()
	store := &gateMessageStore{}
	svc := NewService(store, nil, WithSendPermissionChecker(denySendChecker{}))
	if _, err := svc.SendPrivateText(ctx, 1001, domain.SendPrivateTextRequest{
		SenderUserID:    1001,
		RecipientUserID: 1002,
		RandomID:        1,
		Message:         "blocked",
	}); !errors.Is(err, domain.ErrUserFrozen) {
		t.Fatalf("SendPrivateText err=%v, want ErrUserFrozen", err)
	}
	if store.sends != 0 {
		t.Fatalf("store sends=%d, want 0", store.sends)
	}
}

func TestServicePrivateReplayPrecedesCurrentSendPermissionGate(t *testing.T) {
	ctx := context.Background()
	messages := memory.NewMessageStore()
	allowed := NewService(messages, nil)
	req := domain.SendPrivateTextRequest{
		SenderUserID:    1001,
		RecipientUserID: 1002,
		RandomID:        91,
		Message:         "committed before restriction",
		Date:            1_700_000_000,
	}
	first, err := allowed.SendPrivateText(ctx, 1001, req)
	if err != nil {
		t.Fatalf("first SendPrivateText: %v", err)
	}

	denied := NewService(messages, nil, WithSendPermissionChecker(denySendChecker{}))
	req.Date++ // execution time is not part of the immutable send intent.
	replay, err := denied.SendPrivateText(ctx, 1001, req)
	if err != nil {
		t.Fatalf("replay through denied gate: %v", err)
	}
	if !replay.Duplicate || replay.SenderMessage.ID != first.SenderMessage.ID {
		t.Fatalf("replay = %+v, want committed duplicate %d", replay, first.SenderMessage.ID)
	}

	req.Message = "different intent"
	if _, err := denied.SendPrivateText(ctx, 1001, req); !errors.Is(err, domain.ErrMessageRandomIDDuplicate) {
		t.Fatalf("conflicting replay err=%v, want ErrMessageRandomIDDuplicate before send gate", err)
	}
}

func TestServiceForwardPrivateMessagesHonorsSendPermissionGate(t *testing.T) {
	ctx := context.Background()
	store := &gateMessageStore{}
	svc := NewService(store, nil, WithSendPermissionChecker(denySendChecker{}))
	if _, err := svc.ForwardPrivateMessages(ctx, 1001, domain.ForwardPrivateMessagesRequest{
		OwnerUserID: 1001,
		FromPeer:    domain.Peer{Type: domain.PeerTypeUser, ID: 1002},
		ToUserID:    1003,
		MessageIDs:  []int{1},
		RandomIDs:   []int64{2},
	}); !errors.Is(err, domain.ErrUserFrozen) {
		t.Fatalf("ForwardPrivateMessages err=%v, want ErrUserFrozen", err)
	}
	if store.forwards != 0 {
		t.Fatalf("store forwards=%d, want 0", store.forwards)
	}
}

func TestServiceProjectsMessageUsersForViewerContacts(t *testing.T) {
	ctx := context.Background()
	const ownerID int64 = 1001
	const friendID int64 = 1002
	const strangerID int64 = 1003
	contacts := memory.NewContactStore()
	if _, err := contacts.Upsert(ctx, ownerID, domain.ContactInput{
		ContactUserID: friendID,
		Phone:         "15550000002",
		FirstName:     "Remark",
		LastName:      "Friend",
	}); err != nil {
		t.Fatalf("upsert contact: %v", err)
	}
	store := projectionMessageStore{list: domain.MessageList{
		Users: []domain.User{
			{ID: ownerID, Phone: "15550000001", FirstName: "Owner"},
			{ID: friendID, AccessHash: 22, Phone: "15550000002", FirstName: "Public", LastName: "Name"},
			{ID: strangerID, AccessHash: 33, Phone: "15550000003", FirstName: "Stranger"},
		},
	}}
	svc := NewService(store, nil, WithContactStore(contacts), WithPhotoProvider(messageProfilePhotos{
		friendID:   {PhotoID: 9101, DCID: 2, Stripped: []byte{5, 6}},
		strangerID: {PhotoID: 9102, DCID: 4},
	}))

	list, err := svc.GetHistory(ctx, ownerID, domain.MessageFilter{Limit: 10})
	if err != nil {
		t.Fatalf("GetHistory: %v", err)
	}
	friend := findUser(t, list.Users, friendID)
	if !friend.Contact || friend.FirstName != "Remark" || friend.LastName != "Friend" || friend.Phone != "15550000002" {
		t.Fatalf("friend projection = %+v, want contact remark and phone", friend)
	}
	if friend.PhotoID != 9101 || friend.PhotoDCID != 2 || string(friend.PhotoStripped) != string([]byte{5, 6}) {
		t.Fatalf("friend photo = id %d dc %d stripped %v, want 9101/2/[5 6]", friend.PhotoID, friend.PhotoDCID, friend.PhotoStripped)
	}
	stranger := findUser(t, list.Users, strangerID)
	if stranger.Contact || stranger.Phone != "" || stranger.FirstName != "Stranger" {
		t.Fatalf("stranger projection = %+v, want non-contact with hidden phone", stranger)
	}
	if stranger.PhotoID != 9102 || stranger.PhotoDCID != 4 {
		t.Fatalf("stranger photo = id %d dc %d, want 9102/4", stranger.PhotoID, stranger.PhotoDCID)
	}
	self := findUser(t, list.Users, ownerID)
	if self.Phone != "15550000001" {
		t.Fatalf("self phone = %q, want preserved", self.Phone)
	}
}

func TestBusinessAutomationGreetingSendsQuickReplyWithoutLoop(t *testing.T) {
	ctx := context.Background()
	const ownerID int64 = 2001
	const customerID int64 = 2002

	dialogs := memory.NewDialogStore()
	messages := memory.NewMessageStore(dialogs)
	business := memory.NewPasswordStore()
	accountSvc := account.NewService(business, account.WithBusinessAutomation(business))

	ownerShortcutID := saveBusinessQuickReply(t, ctx, accountSvc, ownerID, "hello", "owner hello", 100)
	customerShortcutID := saveBusinessQuickReply(t, ctx, accountSvc, customerID, "hello", "customer hello", 101)
	if _, err := accountSvc.UpdateBusinessGreetingMessage(ctx, ownerID, &domain.BusinessGreetingMessage{
		ShortcutID:     ownerShortcutID,
		Recipients:     businessAutomationAllRecipients(),
		NoActivityDays: 7,
	}); err != nil {
		t.Fatalf("update owner greeting: %v", err)
	}
	if _, err := accountSvc.UpdateBusinessGreetingMessage(ctx, customerID, &domain.BusinessGreetingMessage{
		ShortcutID:     customerShortcutID,
		Recipients:     businessAutomationAllRecipients(),
		NoActivityDays: 7,
	}); err != nil {
		t.Fatalf("update customer greeting: %v", err)
	}

	svc := NewService(messages, dialogs, WithBusinessAutomation(business))
	if _, err := svc.SendPrivateText(ctx, customerID, domain.SendPrivateTextRequest{
		SenderUserID:    customerID,
		RecipientUserID: ownerID,
		RandomID:        10001,
		Message:         "hi",
		Date:            1_700_000_000,
	}); err != nil {
		t.Fatalf("send first message: %v", err)
	}

	if got := countBusinessMessagesByBody(t, ctx, messages, customerID, ownerID, "owner hello"); got != 1 {
		t.Fatalf("customer owner hello count = %d, want 1", got)
	}
	if got := countBusinessMessagesByBody(t, ctx, messages, ownerID, customerID, "owner hello"); got != 1 {
		t.Fatalf("owner outgoing hello count = %d, want 1", got)
	}
	if got := countBusinessMessagesByBody(t, ctx, messages, ownerID, customerID, "customer hello"); got != 0 {
		t.Fatalf("recursive customer hello count = %d, want 0", got)
	}

	if _, err := svc.SendPrivateText(ctx, customerID, domain.SendPrivateTextRequest{
		SenderUserID:    customerID,
		RecipientUserID: ownerID,
		RandomID:        10002,
		Message:         "again",
		Date:            1_700_000_060,
	}); err != nil {
		t.Fatalf("send second message: %v", err)
	}
	if got := countBusinessMessagesByBody(t, ctx, messages, customerID, ownerID, "owner hello"); got != 1 {
		t.Fatalf("owner hello after second incoming = %d, want 1", got)
	}
}

func TestBusinessAutomationAwayHonorsOnlineStateAndCooldown(t *testing.T) {
	ctx := context.Background()
	const ownerID int64 = 2101
	const customerID int64 = 2102

	dialogs := memory.NewDialogStore()
	messages := memory.NewMessageStore(dialogs)
	business := memory.NewPasswordStore()
	accountSvc := account.NewService(business, account.WithBusinessAutomation(business))

	shortcutID := saveBusinessQuickReply(t, ctx, accountSvc, ownerID, "away", "away reply", 200)
	if _, err := accountSvc.UpdateBusinessAwayMessage(ctx, ownerID, &domain.BusinessAwayMessage{
		ShortcutID:  shortcutID,
		Schedule:    domain.BusinessAwaySchedule{Kind: domain.BusinessAwayScheduleAlways},
		Recipients:  businessAutomationAllRecipients(),
		OfflineOnly: true,
	}); err != nil {
		t.Fatalf("update away: %v", err)
	}

	online := businessAutomationOnline{ownerID: true}
	svc := NewService(messages, dialogs, WithBusinessAutomation(business, WithBusinessAutomationOnlineChecker(online)))
	if _, err := svc.SendPrivateText(ctx, customerID, domain.SendPrivateTextRequest{
		SenderUserID:    customerID,
		RecipientUserID: ownerID,
		RandomID:        20001,
		Message:         "online?",
		Date:            1_700_010_000,
	}); err != nil {
		t.Fatalf("send while online: %v", err)
	}
	if got := countBusinessMessagesByBody(t, ctx, messages, customerID, ownerID, "away reply"); got != 0 {
		t.Fatalf("away while online count = %d, want 0", got)
	}

	online[ownerID] = false
	if _, err := svc.SendPrivateText(ctx, customerID, domain.SendPrivateTextRequest{
		SenderUserID:    customerID,
		RecipientUserID: ownerID,
		RandomID:        20002,
		Message:         "offline?",
		Date:            1_700_010_060,
	}); err != nil {
		t.Fatalf("send while offline: %v", err)
	}
	if _, err := svc.SendPrivateText(ctx, customerID, domain.SendPrivateTextRequest{
		SenderUserID:    customerID,
		RecipientUserID: ownerID,
		RandomID:        20003,
		Message:         "still offline?",
		Date:            1_700_010_120,
	}); err != nil {
		t.Fatalf("send during cooldown: %v", err)
	}
	if got := countBusinessMessagesByBody(t, ctx, messages, customerID, ownerID, "away reply"); got != 1 {
		t.Fatalf("away reply count = %d, want 1", got)
	}
}

func TestBusinessAutomationReplyProviderCanReplaceTemplate(t *testing.T) {
	ctx := context.Background()
	const ownerID int64 = 2201
	const customerID int64 = 2202

	dialogs := memory.NewDialogStore()
	messages := memory.NewMessageStore(dialogs)
	business := memory.NewPasswordStore()
	accountSvc := account.NewService(business, account.WithBusinessAutomation(business))

	shortcutID := saveBusinessQuickReply(t, ctx, accountSvc, ownerID, "hello", "template reply", 300)
	if _, err := accountSvc.UpdateBusinessGreetingMessage(ctx, ownerID, &domain.BusinessGreetingMessage{
		ShortcutID:     shortcutID,
		Recipients:     businessAutomationAllRecipients(),
		NoActivityDays: 7,
	}); err != nil {
		t.Fatalf("update greeting: %v", err)
	}
	svc := NewService(messages, dialogs, WithBusinessAutomation(business, WithBusinessAutomationReplyProvider(staticBusinessAutomationProvider{message: "ai reply"})))
	if _, err := svc.SendPrivateText(ctx, customerID, domain.SendPrivateTextRequest{
		SenderUserID:    customerID,
		RecipientUserID: ownerID,
		RandomID:        30001,
		Message:         "hi",
		Date:            1_700_020_000,
	}); err != nil {
		t.Fatalf("send first message: %v", err)
	}
	if got := countBusinessMessagesByBody(t, ctx, messages, customerID, ownerID, "ai reply"); got != 1 {
		t.Fatalf("provider reply count = %d, want 1", got)
	}
	if got := countBusinessMessagesByBody(t, ctx, messages, customerID, ownerID, "template reply"); got != 0 {
		t.Fatalf("template reply count = %d, want 0", got)
	}
}

func TestBusinessAutomationEchoProviderEchoesTriggerText(t *testing.T) {
	ctx := context.Background()
	const ownerID int64 = 2301
	const customerID int64 = 2302

	dialogs := memory.NewDialogStore()
	messages := memory.NewMessageStore(dialogs)
	business := memory.NewPasswordStore()
	accountSvc := account.NewService(business, account.WithBusinessAutomation(business))

	shortcutID := saveBusinessQuickReply(t, ctx, accountSvc, ownerID, "hello", "template reply", 400)
	if _, err := accountSvc.UpdateBusinessGreetingMessage(ctx, ownerID, &domain.BusinessGreetingMessage{
		ShortcutID:     shortcutID,
		Recipients:     businessAutomationAllRecipients(),
		NoActivityDays: 7,
	}); err != nil {
		t.Fatalf("update greeting: %v", err)
	}
	svc := NewService(messages, dialogs, WithBusinessAutomation(business, WithBusinessAutomationReplyProvider(NewEchoBusinessAutomationProvider())))
	if _, err := svc.SendPrivateText(ctx, customerID, domain.SendPrivateTextRequest{
		SenderUserID:    customerID,
		RecipientUserID: ownerID,
		RandomID:        40001,
		Message:         "echo this",
		Date:            1_700_030_000,
	}); err != nil {
		t.Fatalf("send first message: %v", err)
	}
	if got := countBusinessMessagesByBody(t, ctx, messages, customerID, ownerID, "echo this"); got != 2 {
		t.Fatalf("echo body count in customer history = %d, want 2 (outgoing + echo reply)", got)
	}
	if got := countBusinessMessagesByBody(t, ctx, messages, customerID, ownerID, "template reply"); got != 0 {
		t.Fatalf("template reply count = %d, want 0", got)
	}
}

func TestConnectedBusinessBotEchoHonorsPauseAndDisable(t *testing.T) {
	ctx := context.Background()
	const ownerID int64 = 2401
	const customerID int64 = 2402
	const botID int64 = 2403

	dialogs := memory.NewDialogStore()
	messages := memory.NewMessageStore(dialogs)
	business := memory.NewPasswordStore()
	accountSvc := account.NewService(business, account.WithBusinessAutomation(business))
	if _, err := accountSvc.SaveConnectedBusinessBot(ctx, ownerID, domain.ConnectedBusinessBot{
		BotUserID:  botID,
		Recipients: domain.BusinessBotRecipients{ExcludeSelected: true},
		Rights:     domain.BusinessBotRights{Reply: true},
	}); err != nil {
		t.Fatalf("save connected bot: %v", err)
	}

	svc := NewService(messages, dialogs, WithBusinessAutomation(business, WithBusinessAutomationReplyProvider(NewEchoBusinessAutomationProvider())))
	if _, err := svc.SendPrivateText(ctx, customerID, domain.SendPrivateTextRequest{
		SenderUserID:    customerID,
		RecipientUserID: ownerID,
		RandomID:        50001,
		Message:         "connected echo",
		Date:            1_700_050_000,
	}); err != nil {
		t.Fatalf("send connected echo: %v", err)
	}
	if got := countBusinessMessagesByBody(t, ctx, messages, customerID, ownerID, "connected echo"); got != 2 {
		t.Fatalf("connected echo customer count = %d, want 2 (original + echo)", got)
	}
	assertMessageViaBot(t, ctx, messages, customerID, ownerID, "connected echo", botID)

	if _, err := accountSvc.SetConnectedBusinessBotPaused(ctx, ownerID, customerID, true); err != nil {
		t.Fatalf("pause connected bot: %v", err)
	}
	if _, err := svc.SendPrivateText(ctx, customerID, domain.SendPrivateTextRequest{
		SenderUserID:    customerID,
		RecipientUserID: ownerID,
		RandomID:        50002,
		Message:         "paused echo",
		Date:            1_700_050_060,
	}); err != nil {
		t.Fatalf("send paused echo: %v", err)
	}
	if got := countBusinessMessagesByBody(t, ctx, messages, customerID, ownerID, "paused echo"); got != 1 {
		t.Fatalf("paused echo customer count = %d, want 1", got)
	}

	if _, err := accountSvc.SetConnectedBusinessBotPaused(ctx, ownerID, customerID, false); err != nil {
		t.Fatalf("unpause connected bot: %v", err)
	}
	if _, err := accountSvc.DisableConnectedBusinessBotForPeer(ctx, ownerID, customerID); err != nil {
		t.Fatalf("disable connected bot peer: %v", err)
	}
	if _, err := svc.SendPrivateText(ctx, customerID, domain.SendPrivateTextRequest{
		SenderUserID:    customerID,
		RecipientUserID: ownerID,
		RandomID:        50003,
		Message:         "disabled echo",
		Date:            1_700_050_120,
	}); err != nil {
		t.Fatalf("send disabled echo: %v", err)
	}
	if got := countBusinessMessagesByBody(t, ctx, messages, customerID, ownerID, "disabled echo"); got != 1 {
		t.Fatalf("disabled echo customer count = %d, want 1", got)
	}
}

func TestEchoBusinessAutomationProviderSkipsEmptyText(t *testing.T) {
	msgs, err := NewEchoBusinessAutomationProvider().BusinessAutomationReplies(context.Background(), BusinessAutomationReplyInput{
		TriggerMessage: domain.Message{Body: " \t\n"},
		Now:            1_700_040_000,
	})
	if err != nil {
		t.Fatalf("BusinessAutomationReplies: %v", err)
	}
	if len(msgs) != 0 {
		t.Fatalf("messages = %+v, want none", msgs)
	}
}

func TestAIBusinessAutomationProviderUsesGenerator(t *testing.T) {
	generator := &fakeBusinessAITextGenerator{text: "Thanks, we will check this."}
	msgs, err := NewAIBusinessAutomationProvider(generator).BusinessAutomationReplies(context.Background(), BusinessAutomationReplyInput{
		Kind:        domain.BusinessAutomationGreeting,
		OwnerUserID: 1001,
		TriggerMessage: domain.Message{
			Body: "hello, are you open?",
			Entities: []domain.MessageEntity{{
				Type:   domain.MessageEntityBold,
				Offset: 0,
				Length: 5,
			}},
		},
		Templates: []domain.QuickReplyMessage{{Message: "Hi, thanks for contacting us."}},
		Now:       1_700_060_000,
	})
	if err != nil {
		t.Fatalf("BusinessAutomationReplies: %v", err)
	}
	if len(msgs) != 1 || msgs[0].Message != generator.text {
		t.Fatalf("messages = %+v, want generator reply", msgs)
	}
	if generator.seen.UserID != 1001 || generator.seen.Text.Text != "hello, are you open?" {
		t.Fatalf("generator request = %#v", generator.seen)
	}
	if generator.seen.Instruction == "" {
		t.Fatal("generator instruction is empty")
	}
}

func findUser(t *testing.T, users []domain.User, id int64) domain.User {
	t.Helper()
	for _, user := range users {
		if user.ID == id {
			return user
		}
	}
	t.Fatalf("user %d not found in %+v", id, users)
	return domain.User{}
}

func TestCountPrivateMediaCategoriesCachesByReadModelHash(t *testing.T) {
	ctx := context.Background()
	const ownerID int64 = 1001
	const peerID int64 = 1002
	key := store.ReadModelKey{Model: readmodel.ModelPrivateMediaCounts, OwnerUserID: ownerID, PeerType: domain.PeerTypeUser, PeerID: peerID}
	versions := &fakeMessageReadModelVersions{hashes: map[store.ReadModelKey]int64{key: 101}}
	counting := &countingPrivateMediaStore{counts: domain.MediaCategoryCounts{
		domain.MediaCategoryPhoto: 3,
		domain.MediaCategoryVideo: 2,
	}}
	svc := NewService(counting, nil, WithReadModelVersions(versions))

	first, err := svc.CountPrivateMediaCategories(ctx, ownerID, peerID)
	if err != nil {
		t.Fatalf("CountPrivateMediaCategories first: %v", err)
	}
	first[domain.MediaCategoryPhoto] = 99
	second, err := svc.CountPrivateMediaCategories(ctx, ownerID, peerID)
	if err != nil {
		t.Fatalf("CountPrivateMediaCategories second: %v", err)
	}
	if counting.countPrivateMediaCalls != 1 {
		t.Fatalf("count calls = %d, want 1", counting.countPrivateMediaCalls)
	}
	if got := second[domain.MediaCategoryPhoto]; got != 3 {
		t.Fatalf("cached photo count = %d, want 3", got)
	}

	svc.InvalidatePrivateMediaCountReadModel(ownerID, peerID)
	if _, err := svc.CountPrivateMediaCategories(ctx, ownerID, peerID); err != nil {
		t.Fatalf("CountPrivateMediaCategories after explicit invalidation: %v", err)
	}
	if counting.countPrivateMediaCalls != 2 {
		t.Fatalf("count calls after explicit invalidation = %d, want 2", counting.countPrivateMediaCalls)
	}

	versions.hashes[key] = 202
	counting.counts[domain.MediaCategoryPhoto] = 4
	third, err := svc.CountPrivateMediaCategories(ctx, ownerID, peerID)
	if err != nil {
		t.Fatalf("CountPrivateMediaCategories third: %v", err)
	}
	if counting.countPrivateMediaCalls != 3 {
		t.Fatalf("count calls after hash change = %d, want 3", counting.countPrivateMediaCalls)
	}
	if got := third[domain.MediaCategoryPhoto]; got != 4 {
		t.Fatalf("reloaded photo count = %d, want 4", got)
	}
}

type projectionMessageStore struct {
	list domain.MessageList
}

type denySendChecker struct{}

func (denySendChecker) CanSendMessages(context.Context, int64) error {
	return domain.ErrUserFrozen
}

type gateMessageStore struct {
	projectionMessageStore
	sends    int
	forwards int
}

func (s *gateMessageStore) SendPrivateText(context.Context, domain.SendPrivateTextRequest) (domain.SendPrivateTextResult, error) {
	s.sends++
	return domain.SendPrivateTextResult{}, nil
}

func (s *gateMessageStore) ForwardPrivateMessages(context.Context, domain.ForwardPrivateMessagesRequest) (domain.ForwardPrivateMessagesResult, error) {
	s.forwards++
	return domain.ForwardPrivateMessagesResult{}, nil
}

type countingPrivateMediaStore struct {
	projectionMessageStore
	counts                 domain.MediaCategoryCounts
	countPrivateMediaCalls int
}

func (s *countingPrivateMediaStore) CountPrivateMediaCategories(context.Context, int64, int64) (domain.MediaCategoryCounts, error) {
	s.countPrivateMediaCalls++
	out := make(domain.MediaCategoryCounts, len(s.counts))
	for category, count := range s.counts {
		out[category] = count
	}
	return out, nil
}

type fakeMessageReadModelVersions struct {
	hashes map[store.ReadModelKey]int64
}

func (f *fakeMessageReadModelVersions) ReadModelHash(_ context.Context, model string, ownerUserID int64, peerType domain.PeerType, peerID int64) (int64, bool, error) {
	hash := f.hashes[store.ReadModelKey{Model: model, OwnerUserID: ownerUserID, PeerType: peerType, PeerID: peerID}]
	return hash, hash != 0, nil
}

func (f *fakeMessageReadModelVersions) ReadModelHashes(_ context.Context, keys []store.ReadModelKey) (map[store.ReadModelKey]int64, error) {
	out := make(map[store.ReadModelKey]int64, len(keys))
	for _, key := range keys {
		if hash := f.hashes[key]; hash != 0 {
			out[key] = hash
		}
	}
	return out, nil
}

type messageProfilePhotos map[int64]domain.ProfilePhotoRef

type businessAutomationOnline map[int64]bool

func (o businessAutomationOnline) IsUserOnline(userID int64) bool {
	return o[userID]
}

type staticBusinessAutomationProvider struct {
	message string
}

func (p staticBusinessAutomationProvider) BusinessAutomationReplies(context.Context, BusinessAutomationReplyInput) ([]domain.QuickReplyMessage, error) {
	return []domain.QuickReplyMessage{{ID: 1, Message: p.message}}, nil
}

type fakeBusinessAITextGenerator struct {
	text string
	seen domain.AITextGenerationRequest
}

func (g *fakeBusinessAITextGenerator) GenerateText(_ context.Context, req domain.AITextGenerationRequest) (domain.AIComposeText, error) {
	g.seen = req
	return domain.AIComposeText{Text: g.text}, nil
}

func businessAutomationAllRecipients() domain.BusinessRecipients {
	return domain.BusinessRecipients{
		ExistingChats: true,
		NewChats:      true,
		Contacts:      true,
		NonContacts:   true,
	}
}

func saveBusinessQuickReply(t *testing.T, ctx context.Context, svc *account.Service, ownerID int64, shortcut, message string, randomID int64) int {
	t.Helper()
	mutation, err := svc.SaveQuickReplyText(ctx, ownerID, shortcut, domain.QuickReplyMessage{
		RandomID: randomID,
		Date:     1_700_000_000,
		Message:  message,
	})
	if err != nil {
		t.Fatalf("save quick reply %s: %v", shortcut, err)
	}
	return mutation.ShortcutID
}

func countBusinessMessagesByBody(t *testing.T, ctx context.Context, messages *memory.MessageStore, ownerID, peerID int64, body string) int {
	t.Helper()
	list, err := messages.ListByUser(ctx, ownerID, domain.MessageFilter{
		HasPeer: true,
		Peer:    domain.Peer{Type: domain.PeerTypeUser, ID: peerID},
		Limit:   50,
	})
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	count := 0
	for _, msg := range list.Messages {
		if msg.Body == body {
			count++
		}
	}
	return count
}

func assertMessageViaBot(t *testing.T, ctx context.Context, messages *memory.MessageStore, ownerID, peerID int64, body string, botID int64) {
	t.Helper()
	list, err := messages.ListByUser(ctx, ownerID, domain.MessageFilter{
		HasPeer: true,
		Peer:    domain.Peer{Type: domain.PeerTypeUser, ID: peerID},
		Limit:   50,
	})
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	for _, msg := range list.Messages {
		if msg.Body == body && msg.ViaBotID == botID {
			return
		}
	}
	t.Fatalf("message body %q via bot %d not found in %+v", body, botID, list.Messages)
}

func (p messageProfilePhotos) CurrentProfilePhotos(_ context.Context, _ domain.PeerType, ids []int64) (map[int64]domain.ProfilePhotoRef, error) {
	out := make(map[int64]domain.ProfilePhotoRef, len(ids))
	for _, id := range ids {
		if ref, ok := p[id]; ok {
			out[id] = ref
		}
	}
	return out, nil
}

func (s projectionMessageStore) Create(context.Context, domain.Message) (domain.Message, error) {
	return domain.Message{}, nil
}

func (s projectionMessageStore) SendPrivateText(context.Context, domain.SendPrivateTextRequest) (domain.SendPrivateTextResult, error) {
	return domain.SendPrivateTextResult{}, nil
}

func (s projectionMessageStore) ForwardPrivateMessages(context.Context, domain.ForwardPrivateMessagesRequest) (domain.ForwardPrivateMessagesResult, error) {
	return domain.ForwardPrivateMessagesResult{}, nil
}

func (s projectionMessageStore) ReadHistory(context.Context, domain.ReadHistoryRequest) (domain.ReadHistoryResult, error) {
	return domain.ReadHistoryResult{}, nil
}

func (s projectionMessageStore) ReadMessageContents(context.Context, domain.ReadMessageContentsRequest) (domain.ReadMessageContentsResult, error) {
	return domain.ReadMessageContentsResult{}, nil
}

func (s projectionMessageStore) GetOutboxReadDate(context.Context, domain.OutboxReadDateRequest) (int, error) {
	return 0, nil
}

func (s projectionMessageStore) SetMessageReactions(context.Context, domain.SetPrivateMessageReactionsRequest) (domain.PrivateMessageReactionsResult, error) {
	return domain.PrivateMessageReactionsResult{}, nil
}

func (s projectionMessageStore) GetMessageReactions(context.Context, domain.PrivateMessageReactionsRequest) (domain.PrivateMessageReactionsResult, error) {
	return domain.PrivateMessageReactionsResult{}, nil
}

func (s projectionMessageStore) VoteMessagePoll(context.Context, domain.VotePrivateMessagePollRequest) (domain.PrivateMessagePollResult, error) {
	return domain.PrivateMessagePollResult{}, nil
}

func (s projectionMessageStore) CloseMessagePoll(context.Context, domain.ClosePrivateMessagePollRequest) (domain.PrivateMessagePollResult, error) {
	return domain.PrivateMessagePollResult{}, nil
}

func (s projectionMessageStore) EditMessage(context.Context, domain.EditMessageRequest) (domain.EditMessageResult, error) {
	return domain.EditMessageResult{}, nil
}

func (s projectionMessageStore) PinPrivateMessage(context.Context, domain.PinPrivateMessageRequest) (domain.PinPrivateMessageResult, error) {
	return domain.PinPrivateMessageResult{}, nil
}

func (s projectionMessageStore) UnpinAllPrivateMessages(context.Context, domain.UnpinAllPrivateMessagesRequest) (domain.PinPrivateMessageResult, error) {
	return domain.PinPrivateMessageResult{}, nil
}

func (s projectionMessageStore) DeleteMessages(context.Context, domain.DeleteMessagesRequest) (domain.DeleteMessagesResult, error) {
	return domain.DeleteMessagesResult{}, nil
}

func (s projectionMessageStore) ListSavedDialogs(context.Context, int64, domain.SavedDialogsFilter) (domain.SavedDialogList, error) {
	return domain.SavedDialogList{}, nil
}

func (s projectionMessageStore) ListPinnedSavedDialogs(context.Context, int64) (domain.SavedDialogList, error) {
	return domain.SavedDialogList{}, nil
}

func (s projectionMessageStore) ListSavedDialogsByPeers(context.Context, int64, []domain.Peer) (domain.SavedDialogList, error) {
	return domain.SavedDialogList{}, nil
}

func (s projectionMessageStore) ToggleSavedDialogPin(context.Context, int64, domain.Peer, bool) (bool, error) {
	return false, nil
}

func (s projectionMessageStore) ReorderPinnedSavedDialogs(context.Context, int64, []domain.Peer, bool) error {
	return nil
}

func (s projectionMessageStore) DeleteSavedHistory(context.Context, domain.DeleteSavedHistoryRequest) (domain.DeleteSavedHistoryResult, error) {
	return domain.DeleteSavedHistoryResult{}, nil
}

func (s projectionMessageStore) DeleteHistory(context.Context, domain.DeleteHistoryRequest) (domain.DeleteMessagesResult, error) {
	return domain.DeleteMessagesResult{}, nil
}

func (s projectionMessageStore) GetByIDs(context.Context, int64, []int) (domain.MessageList, error) {
	return s.list, nil
}

func (s projectionMessageStore) ListByUser(context.Context, int64, domain.MessageFilter) (domain.MessageList, error) {
	return s.list, nil
}

func (s projectionMessageStore) SearchPrivateMedia(context.Context, int64, int64, domain.MediaSearchRequest) (domain.MessageList, error) {
	return domain.MessageList{}, nil
}

func (s projectionMessageStore) CountPrivateMediaCategories(context.Context, int64, int64) (domain.MediaCategoryCounts, error) {
	return domain.MediaCategoryCounts{}, nil
}
