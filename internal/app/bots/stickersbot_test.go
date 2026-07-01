package bots

import (
	"context"
	"strings"
	"testing"

	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

func sendTextToStickers(t *testing.T, svc *Service, messages *memory.MessageStore, owner domain.User, text string) string {
	t.Helper()
	return sendMessageToStickers(t, svc, messages, owner, domain.Message{Body: text})
}

func sendDocumentToStickers(t *testing.T, svc *Service, messages *memory.MessageStore, owner domain.User, doc domain.Document) string {
	t.Helper()
	return sendMessageToStickers(t, svc, messages, owner, domain.Message{
		Media: &domain.MessageMedia{
			Kind:     domain.MessageMediaKindDocument,
			Document: &doc,
		},
	})
}

func sendMessageToStickers(t *testing.T, svc *Service, messages *memory.MessageStore, owner domain.User, msg domain.Message) string {
	t.Helper()
	msg.From = domain.Peer{Type: domain.PeerTypeUser, ID: owner.ID}
	msg.Peer = domain.Peer{Type: domain.PeerTypeUser, ID: domain.StickersBotUserID}
	svc.respondAsStickers(owner.ID, msg)
	list, err := messages.ListByUser(context.Background(), owner.ID, domain.MessageFilter{
		HasPeer: true,
		Peer:    domain.Peer{Type: domain.PeerTypeUser, ID: domain.StickersBotUserID},
		Limit:   100,
	})
	if err != nil {
		t.Fatalf("list stickers history: %v", err)
	}
	var latest domain.Message
	for _, msg := range list.Messages {
		if msg.From.ID == domain.StickersBotUserID && msg.ID > latest.ID {
			latest = msg
		}
	}
	if latest.ID == 0 {
		t.Fatalf("no Stickers reply after message %+v", msg)
	}
	return latest.Body
}

func newStickersBotTestService(t *testing.T) (*Service, *memory.UserStore, *memory.BotStore, *memory.MessageStore, *stickersBotFakeCreator, *stickersBotFakeInstaller) {
	t.Helper()
	users := memory.NewUserStore()
	bots := memory.NewBotStore(users)
	dialogs := memory.NewDialogStore()
	messages := memory.NewMessageStore(dialogs)
	creator := &stickersBotFakeCreator{}
	installer := &stickersBotFakeInstaller{}
	svc := NewService(users, bots, messages, WithStickerSetCreator(creator), WithUserStickerSets(installer))
	return svc, users, bots, messages, creator, installer
}

func stickerBotTestDocument(id, accessHash int64, attr domain.DocumentAttributeKind) domain.Document {
	return domain.Document{
		ID:         id,
		AccessHash: accessHash,
		DCID:       2,
		MimeType:   "image/webp",
		Attributes: []domain.DocumentAttribute{{Kind: attr}},
	}
}

func stickerBotUploadDocument(id, accessHash int64, mimeType, fileName string) domain.Document {
	return domain.Document{
		ID:         id,
		AccessHash: accessHash,
		DCID:       2,
		MimeType:   mimeType,
		Size:       4096,
		Attributes: []domain.DocumentAttribute{{Kind: domain.DocAttrFilename, FileName: fileName}},
	}
}

func stickerBotSetDocument(id, accessHash, setID, setAccessHash int64) domain.Document {
	return domain.Document{
		ID:         id,
		AccessHash: accessHash,
		DCID:       2,
		MimeType:   "image/webp",
		Attributes: []domain.DocumentAttribute{{
			Kind:                 domain.DocAttrSticker,
			StickerSetID:         setID,
			StickerSetAccessHash: setAccessHash,
		}},
	}
}

func stickerBotSetCustomEmojiDocument(id, accessHash, setID, setAccessHash int64) domain.Document {
	return domain.Document{
		ID:         id,
		AccessHash: accessHash,
		DCID:       2,
		MimeType:   "application/x-tgsticker",
		Attributes: []domain.DocumentAttribute{{
			Kind:                 domain.DocAttrCustomEmoji,
			StickerSetID:         setID,
			StickerSetAccessHash: setAccessHash,
		}},
	}
}

func TestStickersBotSystemSeedStartAndCancel(t *testing.T) {
	svc, users, bots, messages, _, _ := newStickersBotTestService(t)
	owner := newOwner(t, users, "+3000")
	ctx := context.Background()

	if !svc.HandlesBot(domain.BotFatherUserID) || !svc.HandlesBot(domain.StickersBotUserID) {
		t.Fatal("service should handle BotFather and Stickers")
	}
	u, found, err := users.ByUsername(ctx, "Stickers")
	if err != nil || !found {
		t.Fatalf("@Stickers user not seeded: found=%v err=%v", found, err)
	}
	if u.ID != domain.StickersBotUserID || !u.Bot || u.BotInfoVersion < 1 {
		t.Fatalf("@Stickers user = %+v, want seeded bot", u)
	}
	profile, found, err := bots.GetBot(ctx, domain.StickersBotUserID)
	if err != nil || !found {
		t.Fatalf("@Stickers profile not seeded: found=%v err=%v", found, err)
	}
	if !botCommandExists(profile.Commands, "newpack") || !botCommandExists(profile.Commands, "newemoji") ||
		!botCommandExists(profile.Commands, "publish") || !botCommandExists(profile.Commands, "addsticker") ||
		!botCommandExists(profile.Commands, "delsticker") {
		t.Fatalf("@Stickers commands = %+v, want newpack/newemoji/publish/addsticker/delsticker", profile.Commands)
	}

	if reply := sendTextToStickers(t, svc, messages, owner, "/start"); !strings.Contains(reply, "/newpack") || !strings.Contains(reply, "/newemoji") || !strings.Contains(reply, "/addsticker") {
		t.Fatalf("/start reply = %q, want help text", reply)
	}
	sendTextToStickers(t, svc, messages, owner, "/newpack")
	if reply := sendTextToStickers(t, svc, messages, owner, "/cancel"); !strings.Contains(reply, "Cancelled") {
		t.Fatalf("/cancel reply = %q, want cancelled", reply)
	}
	if _, found, _ := bots.GetBotChatState(ctx, domain.StickersBotUserID, owner.ID); found {
		t.Fatal("stickers bot state still present after /cancel")
	}
}

func TestStickersBotNewPackNoMaterialAndInvalidEmoji(t *testing.T) {
	svc, users, _, messages, creator, _ := newStickersBotTestService(t)
	owner := newOwner(t, users, "+3001")

	if reply := sendTextToStickers(t, svc, messages, owner, "/newpack"); !strings.Contains(reply, "sticker pack") {
		t.Fatalf("/newpack reply = %q, want sticker pack prompt", reply)
	}
	if reply := sendTextToStickers(t, svc, messages, owner, "My Pack"); !strings.Contains(reply, "Lottie JSON") {
		t.Fatalf("title reply = %q, want document prompt", reply)
	}
	if reply := sendTextToStickers(t, svc, messages, owner, "/publish"); !strings.Contains(reply, "Add at least one") {
		t.Fatalf("empty publish reply = %q, want no material notice", reply)
	}
	if reply := sendTextToStickers(t, svc, messages, owner, "not a document"); !strings.Contains(reply, "WebM/MP4 must include video metadata") {
		t.Fatalf("text in document step reply = %q, want material prompt", reply)
	}
	if reply := sendDocumentToStickers(t, svc, messages, owner, stickerBotTestDocument(101, 1101, domain.DocAttrSticker)); !strings.Contains(reply, "emoji") {
		t.Fatalf("document reply = %q, want emoji prompt", reply)
	}
	if reply := sendTextToStickers(t, svc, messages, owner, "notemoji"); !strings.Contains(reply, "valid emoji") {
		t.Fatalf("invalid emoji reply = %q, want emoji validation", reply)
	}
	if len(creator.created) != 0 {
		t.Fatalf("creator called before valid publish: %+v", creator.created)
	}
}

func TestStickersBotPublishStickerPack(t *testing.T) {
	svc, users, bots, messages, creator, installer := newStickersBotTestService(t)
	hooks := &stickersBotHookRecorder{}
	svc.SetRouterHooks(hooks)
	owner := newOwner(t, users, "+3002")

	sendTextToStickers(t, svc, messages, owner, "/newpack")
	sendTextToStickers(t, svc, messages, owner, "Fresh Pack")
	sendDocumentToStickers(t, svc, messages, owner, stickerBotTestDocument(201, 2201, domain.DocAttrSticker))
	sendTextToStickers(t, svc, messages, owner, "🙂")
	sendTextToStickers(t, svc, messages, owner, "/publish")
	reply := sendTextToStickers(t, svc, messages, owner, "fresh_pack")

	if !strings.Contains(reply, "https://telesrv.net/addstickers/fresh_pack") {
		t.Fatalf("publish reply = %q, want addstickers link", reply)
	}
	if len(creator.created) != 1 {
		t.Fatalf("created requests = %d, want 1", len(creator.created))
	}
	req := creator.created[0]
	if req.CreatorUserID != owner.ID || req.Title != "Fresh Pack" || req.ShortName != "fresh_pack" || req.Kind != domain.StickerSetKindStickers {
		t.Fatalf("create request = %+v, want owner/title/short/kind", req)
	}
	if len(req.Items) != 1 || req.Items[0].DocumentID != 201 || req.Items[0].DocumentAccessHash != 2201 || req.Items[0].Emoji != "🙂" {
		t.Fatalf("create items = %+v, want doc+emoji", req.Items)
	}
	if len(installer.installs) != 1 || installer.installs[0].userID != owner.ID || installer.installs[0].setID != creator.sets[0].ID {
		t.Fatalf("installs = %+v, want creator install", installer.installs)
	}
	if hooks.userID != owner.ID || hooks.kind != domain.StickerSetKindStickers {
		t.Fatalf("sticker update hook = user %d kind %q, want creator stickers", hooks.userID, hooks.kind)
	}
	if _, found, _ := bots.GetBotChatState(context.Background(), domain.StickersBotUserID, owner.ID); found {
		t.Fatal("stickers bot state still present after publish")
	}
}

func TestStickersBotPublishUploadedTGS(t *testing.T) {
	svc, users, _, messages, creator, installer := newStickersBotTestService(t)
	owner := newOwner(t, users, "+3004")

	sendTextToStickers(t, svc, messages, owner, "/newemoji")
	sendTextToStickers(t, svc, messages, owner, "Local Emoji")
	reply := sendDocumentToStickers(t, svc, messages, owner, stickerBotUploadDocument(401, 4401, "application/octet-stream", "wave.tgs"))
	if !strings.Contains(reply, "emoji") {
		t.Fatalf("uploaded tgs reply = %q, want emoji prompt", reply)
	}
	sendTextToStickers(t, svc, messages, owner, "👋")
	sendTextToStickers(t, svc, messages, owner, "/publish")
	reply = sendTextToStickers(t, svc, messages, owner, "local_emoji")

	if !strings.Contains(reply, "https://telesrv.net/addemoji/local_emoji") {
		t.Fatalf("publish uploaded tgs reply = %q, want addemoji link", reply)
	}
	if len(creator.created) != 1 {
		t.Fatalf("created requests = %d, want 1", len(creator.created))
	}
	req := creator.created[0]
	if req.Kind != domain.StickerSetKindEmoji || len(req.Items) != 1 || req.Items[0].DocumentID != 401 || req.Items[0].DocumentAccessHash != 4401 {
		t.Fatalf("create request = %+v, want uploaded tgs item in emoji pack", req)
	}
	if len(installer.installs) != 1 || installer.installs[0].kind != domain.StickerSetKindEmoji {
		t.Fatalf("installs = %+v, want emoji install", installer.installs)
	}
}

func TestStickersBotPublishUploadedLottieJSON(t *testing.T) {
	svc, users, _, messages, creator, installer := newStickersBotTestService(t)
	owner := newOwner(t, users, "+3005")

	sendTextToStickers(t, svc, messages, owner, "/newpack")
	sendTextToStickers(t, svc, messages, owner, "Lottie Pack")
	reply := sendDocumentToStickers(t, svc, messages, owner, stickerBotUploadDocument(402, 4402, "application/json", "wave.json"))
	if !strings.Contains(reply, "emoji") {
		t.Fatalf("uploaded lottie reply = %q, want emoji prompt", reply)
	}
	sendTextToStickers(t, svc, messages, owner, "👋")
	sendTextToStickers(t, svc, messages, owner, "/publish")
	reply = sendTextToStickers(t, svc, messages, owner, "lottie_pack")

	if !strings.Contains(reply, "https://telesrv.net/addstickers/lottie_pack") {
		t.Fatalf("publish uploaded lottie reply = %q, want addstickers link", reply)
	}
	if len(creator.created) != 1 {
		t.Fatalf("created requests = %d, want 1", len(creator.created))
	}
	req := creator.created[0]
	if req.Kind != domain.StickerSetKindStickers || len(req.Items) != 1 || req.Items[0].DocumentID != 402 || req.Items[0].DocumentAccessHash != 4402 {
		t.Fatalf("create request = %+v, want uploaded lottie item in sticker pack", req)
	}
	if len(installer.installs) != 1 || installer.installs[0].kind != domain.StickerSetKindStickers {
		t.Fatalf("installs = %+v, want sticker install", installer.installs)
	}
}

func TestStickersBotPublishCustomEmojiPack(t *testing.T) {
	svc, users, _, messages, creator, installer := newStickersBotTestService(t)
	owner := newOwner(t, users, "+3003")

	sendTextToStickers(t, svc, messages, owner, "/newemoji")
	sendTextToStickers(t, svc, messages, owner, "Emoji Pack")
	sendDocumentToStickers(t, svc, messages, owner, stickerBotTestDocument(301, 3301, domain.DocAttrCustomEmoji))
	sendTextToStickers(t, svc, messages, owner, "🔥")
	sendTextToStickers(t, svc, messages, owner, "/publish")
	reply := sendTextToStickers(t, svc, messages, owner, "emoji_pack")

	if !strings.Contains(reply, "https://telesrv.net/addemoji/emoji_pack") {
		t.Fatalf("publish emoji reply = %q, want addemoji link", reply)
	}
	if len(creator.created) != 1 || creator.created[0].Kind != domain.StickerSetKindEmoji {
		t.Fatalf("created emoji requests = %+v, want kind emoji", creator.created)
	}
	if len(installer.installs) != 1 || installer.installs[0].kind != domain.StickerSetKindEmoji {
		t.Fatalf("emoji installs = %+v, want emoji kind install", installer.installs)
	}
}

func TestStickersBotAddStickerToExistingPack(t *testing.T) {
	svc, users, bots, messages, manager, _ := newStickersBotTestService(t)
	hooks := &stickersBotHookRecorder{}
	svc.SetRouterHooks(hooks)
	owner := newOwner(t, users, "+3006")
	manager.sets = append(manager.sets, domain.StickerSet{
		ID:            7100,
		AccessHash:    8100,
		ShortName:     "fresh_pack",
		Title:         "Fresh Pack",
		Kind:          domain.StickerSetKindStickers,
		Creator:       true,
		CreatorUserID: owner.ID,
		Count:         1,
		DocumentIDs:   []int64{501},
	})

	if reply := sendTextToStickers(t, svc, messages, owner, "/addsticker"); !strings.Contains(reply, "short name") {
		t.Fatalf("/addsticker reply = %q, want short name prompt", reply)
	}
	if reply := sendTextToStickers(t, svc, messages, owner, "https://telesrv.net/addstickers/fresh_pack"); !strings.Contains(reply, "Selected Fresh Pack") {
		t.Fatalf("select pack reply = %q, want selected pack", reply)
	}
	if reply := sendDocumentToStickers(t, svc, messages, owner, stickerBotUploadDocument(502, 5502, "application/json", "new.json")); !strings.Contains(reply, "emoji") {
		t.Fatalf("add document reply = %q, want emoji prompt", reply)
	}
	reply := sendTextToStickers(t, svc, messages, owner, "😄")
	if !strings.Contains(reply, "Done. Added to Fresh Pack") || !strings.Contains(reply, "https://telesrv.net/addstickers/fresh_pack") {
		t.Fatalf("add final reply = %q, want done link", reply)
	}
	if len(manager.adds) != 1 {
		t.Fatalf("adds = %+v, want one add", manager.adds)
	}
	add := manager.adds[0]
	if add.userID != owner.ID || add.ref.ID != 7100 || add.item.DocumentID != 502 || add.item.DocumentAccessHash != 5502 || add.item.Emoji != "😄" {
		t.Fatalf("add call = %+v, want owner/set/doc/emoji", add)
	}
	if hooks.userID != owner.ID || hooks.kind != domain.StickerSetKindStickers {
		t.Fatalf("hook = user %d kind %q, want owner stickers", hooks.userID, hooks.kind)
	}
	if _, found, _ := bots.GetBotChatState(context.Background(), domain.StickersBotUserID, owner.ID); found {
		t.Fatal("stickers bot state still present after add")
	}
}

func TestStickersBotAddStickerDuplicateStaysInFlow(t *testing.T) {
	svc, users, bots, messages, manager, _ := newStickersBotTestService(t)
	owner := newOwner(t, users, "+3008")
	manager.sets = append(manager.sets, domain.StickerSet{
		ID:            7300,
		AccessHash:    8300,
		ShortName:     "fresh_pack",
		Title:         "Fresh Pack",
		Kind:          domain.StickerSetKindStickers,
		CreatorUserID: owner.ID,
		Count:         1,
		DocumentIDs:   []int64{501},
	})

	sendTextToStickers(t, svc, messages, owner, "/addsticker")
	sendTextToStickers(t, svc, messages, owner, "fresh_pack")
	sendDocumentToStickers(t, svc, messages, owner, stickerBotSetDocument(501, 5501, 7300, 8300))
	reply := sendTextToStickers(t, svc, messages, owner, "😄")

	if !strings.Contains(reply, "already in this pack") {
		t.Fatalf("duplicate add reply = %q, want already-in-pack notice", reply)
	}
	if len(manager.adds) != 0 {
		t.Fatalf("adds = %+v, want no manager add for duplicate", manager.adds)
	}
	state, found, _ := bots.GetBotChatState(context.Background(), domain.StickersBotUserID, owner.ID)
	if !found || state.Step != stickersBotStepDocument {
		t.Fatalf("state after duplicate = %+v found=%v, want document step", state, found)
	}
}

func TestStickersBotDeleteStickerFromExistingPack(t *testing.T) {
	svc, users, bots, messages, manager, _ := newStickersBotTestService(t)
	hooks := &stickersBotHookRecorder{}
	svc.SetRouterHooks(hooks)
	owner := newOwner(t, users, "+3007")
	manager.sets = append(manager.sets, domain.StickerSet{
		ID:            7200,
		AccessHash:    8200,
		ShortName:     "old_pack",
		Title:         "Old Pack",
		Kind:          domain.StickerSetKindStickers,
		Creator:       true,
		CreatorUserID: owner.ID,
		Count:         2,
		DocumentIDs:   []int64{601, 602},
	})

	sendTextToStickers(t, svc, messages, owner, "/delsticker")
	if reply := sendTextToStickers(t, svc, messages, owner, "old_pack"); !strings.Contains(reply, "Selected Old Pack") {
		t.Fatalf("select delete pack reply = %q, want selected pack", reply)
	}
	doc := stickerBotSetDocument(601, 6601, 7200, 8200)
	reply := sendDocumentToStickers(t, svc, messages, owner, doc)
	if !strings.Contains(reply, "Done. Removed from Old Pack") || !strings.Contains(reply, "https://telesrv.net/addstickers/old_pack") {
		t.Fatalf("delete final reply = %q, want done link", reply)
	}
	if len(manager.removes) != 1 || manager.removes[0].documentID != 601 || manager.removes[0].accessHash != 6601 {
		t.Fatalf("removes = %+v, want document 601", manager.removes)
	}
	if hooks.userID != owner.ID || hooks.kind != domain.StickerSetKindStickers {
		t.Fatalf("hook = user %d kind %q, want owner stickers", hooks.userID, hooks.kind)
	}
	if _, found, _ := bots.GetBotChatState(context.Background(), domain.StickersBotUserID, owner.ID); found {
		t.Fatal("stickers bot state still present after delete")
	}
}

func TestStickersBotDeleteCustomEmojiEntityFromExistingPack(t *testing.T) {
	svc, users, bots, messages, manager, _ := newStickersBotTestService(t)
	owner := newOwner(t, users, "+3009")
	manager.sets = append(manager.sets, domain.StickerSet{
		ID:            7400,
		AccessHash:    8400,
		ShortName:     "emoji_pack",
		Title:         "Emoji Pack",
		Kind:          domain.StickerSetKindEmoji,
		Emojis:        true,
		CreatorUserID: owner.ID,
		Count:         2,
		DocumentIDs:   []int64{701, 702},
	})
	manager.docs = map[int64]domain.Document{
		701: stickerBotSetCustomEmojiDocument(701, 7701, 7400, 8400),
	}

	sendTextToStickers(t, svc, messages, owner, "/delsticker")
	sendTextToStickers(t, svc, messages, owner, "emoji_pack")
	reply := sendMessageToStickers(t, svc, messages, owner, domain.Message{
		Body: "🔥",
		Entities: []domain.MessageEntity{{
			Type:       domain.MessageEntityCustomEmoji,
			Offset:     0,
			Length:     2,
			DocumentID: 701,
		}},
	})

	if !strings.Contains(reply, "Done. Removed from Emoji Pack") || !strings.Contains(reply, "https://telesrv.net/addemoji/emoji_pack") {
		t.Fatalf("delete custom emoji reply = %q, want done emoji link", reply)
	}
	if len(manager.removes) != 1 || manager.removes[0].documentID != 701 || manager.removes[0].accessHash != 7701 {
		t.Fatalf("removes = %+v, want custom emoji document 701", manager.removes)
	}
	if _, found, _ := bots.GetBotChatState(context.Background(), domain.StickersBotUserID, owner.ID); found {
		t.Fatal("stickers bot state still present after custom emoji delete")
	}
}

func botCommandExists(commands []domain.BotCommand, want string) bool {
	for _, c := range commands {
		if c.Command == want {
			return true
		}
	}
	return false
}

type stickersBotFakeCreator struct {
	created []domain.CreateStickerSetRequest
	sets    []domain.StickerSet
	docs    map[int64]domain.Document
	adds    []stickersBotAdd
	removes []stickersBotRemove
}

type stickersBotAdd struct {
	userID int64
	ref    domain.StickerSetRef
	item   domain.StickerSetItemInput
}

type stickersBotRemove struct {
	userID     int64
	documentID int64
	accessHash int64
}

func (f *stickersBotFakeCreator) CreateStickerSet(_ context.Context, req domain.CreateStickerSetRequest) (domain.StickerSet, []domain.Document, error) {
	f.created = append(f.created, req)
	docIDs := make([]int64, 0, len(req.Items))
	for _, item := range req.Items {
		docIDs = append(docIDs, item.DocumentID)
	}
	kind := req.Kind
	if kind == "" {
		kind = domain.StickerSetKindStickers
	}
	set := domain.StickerSet{
		ID:            7000 + int64(len(f.sets)),
		AccessHash:    8000 + int64(len(f.sets)),
		ShortName:     strings.ToLower(strings.TrimSpace(req.ShortName)),
		Title:         req.Title,
		Kind:          kind,
		Emojis:        kind == domain.StickerSetKindEmoji,
		Creator:       true,
		CreatorUserID: req.CreatorUserID,
		Count:         len(docIDs),
		DocumentIDs:   docIDs,
	}
	f.sets = append(f.sets, set)
	return set, nil, nil
}

func (f *stickersBotFakeCreator) ResolveStickerSet(_ context.Context, ref domain.StickerSetRef) (domain.StickerSet, []domain.Document, bool, error) {
	idx := f.indexSet(ref)
	if idx < 0 {
		return domain.StickerSet{}, nil, false, nil
	}
	return f.sets[idx], nil, true, nil
}

func (f *stickersBotFakeCreator) ListCreatedStickerSets(_ context.Context, userID int64, _ int64, limit int) ([]domain.StickerSet, int, error) {
	var out []domain.StickerSet
	for _, set := range f.sets {
		if set.CreatorUserID == userID {
			out = append(out, set)
		}
	}
	total := len(out)
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, total, nil
}

func (f *stickersBotFakeCreator) GetDocuments(_ context.Context, ids []int64) ([]domain.Document, error) {
	out := make([]domain.Document, 0, len(ids))
	for _, id := range ids {
		if doc, ok := f.docs[id]; ok {
			out = append(out, doc)
		}
	}
	return out, nil
}

func (f *stickersBotFakeCreator) AddStickerToSet(_ context.Context, actorUserID int64, ref domain.StickerSetRef, item domain.StickerSetItemInput) (domain.StickerSet, []domain.Document, error) {
	f.adds = append(f.adds, stickersBotAdd{userID: actorUserID, ref: ref, item: item})
	idx := f.indexSet(ref)
	if idx < 0 || f.sets[idx].CreatorUserID != actorUserID {
		return domain.StickerSet{}, nil, domain.ErrStickerSetInvalid
	}
	set := f.sets[idx]
	if !stickersBotTestContainsInt64(set.DocumentIDs, item.DocumentID) {
		set.DocumentIDs = append(set.DocumentIDs, item.DocumentID)
		set.Count = len(set.DocumentIDs)
	}
	f.sets[idx] = set
	return set, nil, nil
}

func (f *stickersBotFakeCreator) RemoveStickerFromSet(_ context.Context, actorUserID int64, documentID int64, accessHash int64) (domain.StickerSet, []domain.Document, error) {
	f.removes = append(f.removes, stickersBotRemove{userID: actorUserID, documentID: documentID, accessHash: accessHash})
	for i, set := range f.sets {
		if set.CreatorUserID != actorUserID {
			continue
		}
		for idx, id := range set.DocumentIDs {
			if id != documentID {
				continue
			}
			set.DocumentIDs = append(append([]int64(nil), set.DocumentIDs[:idx]...), set.DocumentIDs[idx+1:]...)
			set.Count = len(set.DocumentIDs)
			f.sets[i] = set
			return set, nil, nil
		}
	}
	return domain.StickerSet{}, nil, domain.ErrStickerSetFileInvalid
}

func (f *stickersBotFakeCreator) indexSet(ref domain.StickerSetRef) int {
	for i, set := range f.sets {
		switch ref.Kind {
		case domain.StickerSetRefByID:
			if set.ID == ref.ID && (ref.AccessHash == 0 || set.AccessHash == ref.AccessHash) {
				return i
			}
		case domain.StickerSetRefByShortName:
			if strings.EqualFold(set.ShortName, ref.ShortName) {
				return i
			}
		}
	}
	return -1
}

func stickersBotTestContainsInt64(values []int64, want int64) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

type stickersBotFakeInstaller struct {
	installs []stickersBotInstall
}

type stickersBotInstall struct {
	userID int64
	setID  int64
	kind   domain.StickerSetKind
}

func (f *stickersBotFakeInstaller) InstallUserStickerSet(_ context.Context, userID int64, setID int64, kind domain.StickerSetKind, _ bool, _ int) error {
	f.installs = append(f.installs, stickersBotInstall{userID: userID, setID: setID, kind: kind})
	return nil
}

type stickersBotHookRecorder struct {
	userID int64
	kind   domain.StickerSetKind
}

func (h *stickersBotHookRecorder) RevokeBotSessions(context.Context, int64) error {
	return nil
}

func (h *stickersBotHookRecorder) PushBotCommandsChanged(context.Context, int64, []domain.BotCommand) {
}

func (h *stickersBotHookRecorder) PushStickerSetsChanged(_ context.Context, userID int64, kind domain.StickerSetKind) {
	h.userID = userID
	h.kind = kind
}
