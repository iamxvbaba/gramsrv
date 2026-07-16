package rpc

import (
	"context"
	"testing"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"go.uber.org/zap/zaptest"

	appchannels "telesrv/internal/app/channels"
	appmessages "telesrv/internal/app/messages"
	appstargifts "telesrv/internal/app/stargifts"
	appstars "telesrv/internal/app/stars"
	appusers "telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

func starGiftTestRouter(t *testing.T) (*Router, domain.User, domain.User, domain.StarGift) {
	t.Helper()
	ctx := context.Background()
	users := memory.NewUserStore()
	dialogs := memory.NewDialogStore()
	msgStore := memory.NewMessageStore(dialogs)
	channelStore := memory.NewChannelStore()
	sender, err := users.Create(ctx, domain.User{AccessHash: 7101, Phone: "15550007101", FirstName: "Sender"})
	if err != nil {
		t.Fatalf("create sender: %v", err)
	}
	recipient, err := users.Create(ctx, domain.User{AccessHash: 7102, Phone: "15550007102", FirstName: "Recipient"})
	if err != nil {
		t.Fatalf("create recipient: %v", err)
	}
	gift := domain.StarGift{
		ID: 8001, RevisionID: 9001, Stars: 50, ConvertStars: 50, Title: "Cake",
		Sticker: domain.Document{ID: 700, AccessHash: 7, DCID: 2, MimeType: "application/x-tgsticker", Attributes: []domain.DocumentAttribute{{Kind: domain.DocAttrSticker}}},
	}
	giftStore := memory.NewStarGiftStore()
	giftStore.SeedCatalog([]domain.StarGift{gift})
	gifts := appstargifts.NewService(giftStore, nil, 2)
	r := New(Config{DC: 2, IP: "127.0.0.1", Port: 2398}, Deps{
		Users:    appusers.NewService(users),
		Messages: appmessages.NewService(msgStore, dialogs),
		Channels: appchannels.NewService(channelStore),
		Stars:    appstars.NewService(memory.NewStarsStore(), appstars.WithStartingGrant(1000)),
		Gifts:    gifts,
	}, zaptest.NewLogger(t), clock.System)
	return r, sender, recipient, gift
}

type uniqueGiftRPCService struct {
	GiftsService
	unique domain.UniqueStarGift
}

func (s *uniqueGiftRPCService) UniqueBySlug(_ context.Context, slug string) (domain.UniqueStarGift, bool, error) {
	return s.unique, slug == s.unique.Slug, nil
}

func collectibleRPCAttribute(kind domain.StarGiftCollectibleAttributeKind, id int64, name string) domain.StarGiftCollectibleAttribute {
	attribute := domain.StarGiftCollectibleAttribute{Kind: kind, Name: name, RarityPermille: 1000}
	if kind == domain.StarGiftCollectibleBackdrop {
		attribute.BackdropID = int(id)
		attribute.CenterColor = 0x112233
		attribute.EdgeColor = 0x223344
		attribute.PatternColor = 0x334455
		attribute.TextColor = 0xffffff
		return attribute
	}
	attribute.Document = &domain.Document{
		ID: id, AccessHash: id + 1, FileReference: []byte("collectible-rpc"), Date: 1700000000,
		MimeType: "application/x-tgsticker", Size: 3, DCID: 2,
		Attributes: []domain.DocumentAttribute{{Kind: domain.DocAttrSticker}, {Kind: domain.DocAttrFilename, FileName: "gift.tgs"}},
	}
	attribute.Animation = &domain.StarGiftAnimation{
		SourceName: "gift.tgs", SourceFormat: domain.StarGiftAnimationTGS,
		JSON: []byte(`{"v":"5.7"}`), TGS: []byte("tgs"), SHA256: make([]byte, 32), Width: 512, Height: 512,
	}
	attribute.Blob = &domain.FileBlob{LocationKey: "doc:test", Backend: domain.MediaBackendLocalFS, ObjectKey: "test", Size: 3}
	return attribute
}

func TestSavedStarGiftProjectionCombinesHistoricalCatalogWithCurrentCollectibleAvailability(t *testing.T) {
	historical := domain.StarGift{
		ID: 8001, RevisionID: 9001, Stars: 50, ConvertStars: 25, Title: "Historical Cake",
		Sticker: domain.Document{ID: 700, AccessHash: 7, DCID: 2, MimeType: "application/x-tgsticker"},
	}
	saved := domain.SavedStarGift{GiftID: historical.ID, RevisionID: historical.RevisionID, MsgID: 44, Date: 100, ConvertStars: historical.ConvertStars}
	availability := map[int64]domain.StarGiftCollectibleAvailability{
		historical.ID: {UpgradeStars: 75, SupplyTotal: 500, Issued: 12},
	}

	projected := tgSavedStarGifts([]domain.SavedStarGift{saved}, map[int64]domain.StarGift{historical.RevisionID: historical}, availability)
	if len(projected) != 1 || !projected[0].CanUpgrade {
		t.Fatalf("saved gift = %#v, want current pool to make historical gift upgradable", projected)
	}
	gift, ok := projected[0].Gift.(*tg.StarGift)
	if !ok {
		t.Fatalf("saved gift inner = %T, want *tg.StarGift", projected[0].Gift)
	}
	if gift.Title != historical.Title || gift.Stars != historical.Stars || gift.ConvertStars != historical.ConvertStars {
		t.Fatalf("historical snapshot changed: %#v", gift)
	}
	if upgradeStars, ok := gift.GetUpgradeStars(); !ok || upgradeStars != 75 {
		t.Fatalf("upgrade_stars = %d ok=%v, want current price 75", upgradeStars, ok)
	}
	for _, profile := range []tg.LayerProfile{tg.LayerProfile227, tg.LayerProfile228} {
		wire := &tg.PaymentsSavedStarGifts{Count: 1, Gifts: projected, Chats: []tg.ChatClass{}, Users: []tg.UserClass{}}
		encoded := &bin.Buffer{}
		if err := tg.EncodeLayer(profile, tg.LayerConstructorPaymentsSavedStarGiftsType(), wire, encoded); err != nil {
			t.Fatalf("encode Layer %d saved gift: %v", profile, err)
		}
		decoded, err := tg.DecodeLayer(profile, tg.LayerConstructorPaymentsSavedStarGiftsType(), &bin.Buffer{Buf: encoded.Buf})
		if err != nil {
			t.Fatalf("decode Layer %d saved gift: %v", profile, err)
		}
		inner, ok := decoded.Gifts[0].Gift.(*tg.StarGift)
		if !ok || !decoded.Gifts[0].CanUpgrade || inner.UpgradeStars != 75 {
			t.Fatalf("Layer %d projection lost upgrade flags: %#v", profile, decoded.Gifts[0])
		}
	}

	availability[historical.ID] = domain.StarGiftCollectibleAvailability{UpgradeStars: 75, SupplyTotal: 500, Issued: 500}
	soldOut := tgSavedStarGifts([]domain.SavedStarGift{saved}, map[int64]domain.StarGift{historical.RevisionID: historical}, availability)[0]
	if soldOut.CanUpgrade {
		t.Fatal("sold-out collectible pool must not advertise upgrade")
	}
	if gift, ok := soldOut.Gift.(*tg.StarGift); !ok {
		t.Fatalf("sold-out inner = %T, want *tg.StarGift", soldOut.Gift)
	} else if _, ok := gift.GetUpgradeStars(); ok {
		t.Fatal("sold-out catalog projection must not expose upgrade_stars")
	}
	saved.PrepaidUpgradeStars = 75
	soldOutPrepaid := tgSavedStarGifts([]domain.SavedStarGift{saved}, map[int64]domain.StarGift{historical.RevisionID: historical}, availability)[0]
	if soldOutPrepaid.CanUpgrade {
		t.Fatal("sold-out prepaid gift must not advertise an upgrade the aggregate will reject")
	}
	if _, ok := soldOutPrepaid.GetUpgradeStars(); ok {
		t.Fatal("sold-out prepaid gift must not expose stale prepaid upgrade_stars")
	}
}

func TestStarGiftCollectiblePreviewUpgradeFormUniqueAndServiceProjection(t *testing.T) {
	r, sender, owner, gift := starGiftTestRouter(t)
	ctx := context.Background()
	ownerCtx := WithUserID(ctx, owner.ID)
	giftService, ok := r.deps.Gifts.(*appstargifts.Service)
	if !ok {
		t.Fatalf("gift service = %T", r.deps.Gifts)
	}
	model := collectibleRPCAttribute(domain.StarGiftCollectibleModel, 8101, "Aurora")
	pattern := collectibleRPCAttribute(domain.StarGiftCollectiblePattern, 8102, "Orbit")
	backdrop := collectibleRPCAttribute(domain.StarGiftCollectibleBackdrop, 1, "Midnight")
	if _, err := giftService.PublishCollectibleRevision(ctx, domain.StarGiftCollectibleWrite{
		GiftID: gift.ID, UpgradeStars: 75, SupplyTotal: 500, SlugPrefix: "cake",
		Models: []domain.StarGiftCollectibleAttribute{model}, Patterns: []domain.StarGiftCollectibleAttribute{pattern},
		Backdrops: []domain.StarGiftCollectibleAttribute{backdrop}, Actor: "test", CommandID: "collectible-rpc",
	}); err != nil {
		t.Fatalf("publish collectible pool: %v", err)
	}
	if _, err := r.deps.Gifts.RecordSavedGift(ctx, domain.SavedStarGift{
		Owner: domain.Peer{Type: domain.PeerTypeUser, ID: owner.ID}, FromUserID: sender.ID,
		GiftID: gift.ID, RevisionID: gift.RevisionID, MsgID: 444, Date: 1700000000, ConvertStars: gift.ConvertStars,
	}); err != nil {
		t.Fatalf("record upgrade target: %v", err)
	}

	preview, err := r.onPaymentsGetStarGiftUpgradePreview(ownerCtx, gift.ID)
	if err != nil || len(preview.SampleAttributes) != 3 {
		t.Fatalf("upgrade preview = %#v err %v", preview, err)
	}
	invoice := &tg.InputInvoiceStarGiftUpgrade{Stargift: &tg.InputSavedStarGiftUser{MsgID: 444}}
	formClass, err := r.onPaymentsGetPaymentForm(ownerCtx, &tg.PaymentsGetPaymentFormRequest{Invoice: invoice})
	if err != nil {
		t.Fatalf("get upgrade payment form: %v", err)
	}
	form, ok := formClass.(*tg.PaymentsPaymentFormStarGift)
	if !ok || form.FormID == 0 || form.Invoice.Currency != "XTR" || len(form.Invoice.Prices) != 1 || form.Invoice.Prices[0].Amount != 75 {
		t.Fatalf("upgrade payment form = %T %#v", formClass, formClass)
	}

	unique := domain.UniqueStarGift{
		ID: 9200000000000001, GiftID: gift.ID, Title: gift.Title, Slug: "cake-1", Num: 1,
		Owner: domain.Peer{Type: domain.PeerTypeUser, ID: owner.ID},
		Model: model, Pattern: pattern, Backdrop: backdrop,
		AvailabilityIssued: 1, AvailabilityTotal: 500, KeepOriginalDetails: true,
		OriginalFromUserID: sender.ID, OriginalOwner: domain.Peer{Type: domain.PeerTypeUser, ID: owner.ID},
		OriginalDate: 1700000000, OriginalMessage: "hello",
	}
	r.deps.Gifts = &uniqueGiftRPCService{GiftsService: r.deps.Gifts, unique: unique}
	uniqueResponse, err := r.onPaymentsGetUniqueStarGift(WithUserID(ctx, sender.ID), unique.Slug)
	if err != nil {
		t.Fatalf("get unique gift = %#v err %v", uniqueResponse, err)
	}
	uniqueGift, ok := uniqueResponse.Gift.(*tg.StarGiftUnique)
	if !ok || uniqueGift.Slug != unique.Slug || len(uniqueGift.Attributes) != 4 || len(uniqueResponse.Users) != 2 {
		t.Fatalf("get unique gift = %#v", uniqueResponse)
	}

	message := domain.Message{Media: &domain.MessageMedia{Kind: domain.MessageMediaKindService, ServiceAction: &domain.MessageServiceAction{
		Kind: domain.MessageServiceActionStarGiftUnique,
		StarGiftUnique: &domain.MessageStarGiftUniqueAction{
			Gift: unique, FromUserID: sender.ID, Peer: unique.Owner, Upgrade: true, Saved: true,
		},
	}}}
	action, ok := tgMessageServiceAction(message).(*tg.MessageActionStarGiftUnique)
	if !ok {
		t.Fatalf("unique service action type = %T", tgMessageServiceAction(message))
	}
	projectedGift, giftOK := action.Gift.(*tg.StarGiftUnique)
	if !giftOK || !action.Upgrade || !action.Saved || projectedGift.ID != unique.ID || projectedGift.Slug != unique.Slug {
		t.Fatalf("unique service action = %#v", tgMessageServiceAction(message))
	}
	if peer, ok := action.GetPeer(); !ok {
		t.Fatal("unique service action missing owner peer")
	} else if user, ok := peer.(*tg.PeerUser); !ok || user.UserID != owner.ID {
		t.Fatalf("unique service action peer = %#v", peer)
	}
	for _, profile := range []tg.LayerProfile{tg.LayerProfile227, tg.LayerProfile228} {
		responseWire := &bin.Buffer{}
		if err := tg.EncodeLayer(profile, tg.LayerConstructorPaymentsUniqueStarGiftType(), uniqueResponse, responseWire); err != nil {
			t.Fatalf("encode Layer %d unique response: %v", profile, err)
		}
		decodedResponse, err := tg.DecodeLayer(profile, tg.LayerConstructorPaymentsUniqueStarGiftType(), &bin.Buffer{Buf: responseWire.Buf})
		if err != nil {
			t.Fatalf("decode Layer %d unique response: %v", profile, err)
		}
		decodedGift, ok := decodedResponse.Gift.(*tg.StarGiftUnique)
		if !ok || decodedGift.Slug != unique.Slug || len(decodedGift.Attributes) != 4 {
			t.Fatalf("Layer %d unique response lost fields: %#v", profile, decodedResponse.Gift)
		}

		actionWire := &bin.Buffer{}
		if err := tg.EncodeLayer(profile, tg.LayerConstructorMessageActionStarGiftUniqueType(), action, actionWire); err != nil {
			t.Fatalf("encode Layer %d unique action: %v", profile, err)
		}
		decodedAction, err := tg.DecodeLayer(profile, tg.LayerConstructorMessageActionStarGiftUniqueType(), &bin.Buffer{Buf: actionWire.Buf})
		if err != nil {
			t.Fatalf("decode Layer %d unique action: %v", profile, err)
		}
		if decodedActionGift, ok := decodedAction.Gift.(*tg.StarGiftUnique); !ok || !decodedAction.Upgrade || decodedActionGift.Slug != unique.Slug {
			t.Fatalf("Layer %d unique action lost fields: %#v", profile, decodedAction)
		}
	}
}

func TestStarGiftCollectionsCRUDFilterOrderAndPin(t *testing.T) {
	r, _, owner, gift := starGiftTestRouter(t)
	ctx := WithUserID(context.Background(), owner.ID)
	ownerPeer := domain.Peer{Type: domain.PeerTypeUser, ID: owner.ID}
	for _, msgID := range []int{101, 102} {
		if _, err := r.deps.Gifts.RecordSavedGift(context.Background(), domain.SavedStarGift{
			Owner: ownerPeer, GiftID: gift.ID, RevisionID: gift.RevisionID,
			MsgID: msgID, Date: 1700000000 + msgID, ConvertStars: gift.ConvertStars,
		}); err != nil {
			t.Fatalf("record saved gift %d: %v", msgID, err)
		}
	}
	ref101 := tg.InputSavedStarGiftClass(&tg.InputSavedStarGiftUser{MsgID: 101})
	ref102 := tg.InputSavedStarGiftClass(&tg.InputSavedStarGiftUser{MsgID: 102})

	first, err := r.onPaymentsCreateStarGiftCollection(ctx, &tg.PaymentsCreateStarGiftCollectionRequest{
		Peer: &tg.InputPeerSelf{}, Title: " Favorites ", Stargift: []tg.InputSavedStarGiftClass{ref101},
	})
	if err != nil {
		t.Fatalf("create first collection: %v", err)
	}
	second, err := r.onPaymentsCreateStarGiftCollection(ctx, &tg.PaymentsCreateStarGiftCollectionRequest{
		Peer: &tg.InputPeerSelf{}, Title: "Archive", Stargift: []tg.InputSavedStarGiftClass{ref102},
	})
	if err != nil {
		t.Fatalf("create second collection: %v", err)
	}
	if first.Title != "Favorites" || first.GiftsCount != 1 || second.GiftsCount != 1 {
		t.Fatalf("created collections = %#v / %#v", first, second)
	}

	listedClass, err := r.onPaymentsGetStarGiftCollections(ctx, &tg.PaymentsGetStarGiftCollectionsRequest{Peer: &tg.InputPeerSelf{}})
	if err != nil {
		t.Fatalf("list collections: %v", err)
	}
	listed, ok := listedClass.(*tg.PaymentsStarGiftCollections)
	if !ok || len(listed.Collections) != 2 {
		t.Fatalf("list collections = %T %#v, want two", listedClass, listedClass)
	}
	domainCollections, err := r.deps.Gifts.ListCollections(context.Background(), ownerPeer)
	if err != nil {
		t.Fatalf("list domain collections: %v", err)
	}
	if notModified, err := r.onPaymentsGetStarGiftCollections(ctx, &tg.PaymentsGetStarGiftCollectionsRequest{
		Peer: &tg.InputPeerSelf{}, Hash: domain.StarGiftCollectionsHash(domainCollections),
	}); err != nil {
		t.Fatalf("hash list collections: %v", err)
	} else if _, ok := notModified.(*tg.PaymentsStarGiftCollectionsNotModified); !ok {
		t.Fatalf("hash response = %T, want not modified", notModified)
	}

	update := &tg.PaymentsUpdateStarGiftCollectionRequest{Peer: &tg.InputPeerSelf{}, CollectionID: first.CollectionID}
	update.SetTitle("Best")
	update.SetAddStargift([]tg.InputSavedStarGiftClass{ref102})
	update.SetOrder([]tg.InputSavedStarGiftClass{ref102, ref101})
	updated, err := r.onPaymentsUpdateStarGiftCollection(ctx, update)
	if err != nil {
		t.Fatalf("update collection: %v", err)
	}
	if updated.Title != "Best" || updated.GiftsCount != 2 {
		t.Fatalf("updated collection = %#v", updated)
	}

	filteredReq := &tg.PaymentsGetSavedStarGiftsRequest{Peer: &tg.InputPeerSelf{}, Limit: 10}
	filteredReq.SetCollectionID(first.CollectionID)
	filtered, err := r.onPaymentsGetSavedStarGifts(ctx, filteredReq)
	if err != nil {
		t.Fatalf("filter saved gifts by collection: %v", err)
	}
	if filtered.Count != 2 || len(filtered.Gifts) != 2 {
		t.Fatalf("filtered gifts count=%d len=%d, want 2/2", filtered.Count, len(filtered.Gifts))
	}
	for _, saved := range filtered.Gifts {
		ids, ok := saved.GetCollectionID()
		if !ok || len(ids) == 0 || ids[0] != first.CollectionID {
			t.Fatalf("saved gift collection projection = %#v", saved)
		}
	}

	if ok, err := r.onPaymentsToggleStarGiftsPinnedToTop(ctx, &tg.PaymentsToggleStarGiftsPinnedToTopRequest{
		Peer: &tg.InputPeerSelf{}, Stargift: []tg.InputSavedStarGiftClass{ref101, ref102},
	}); err != nil || !ok {
		t.Fatalf("pin gifts = %v err %v", ok, err)
	}
	pinned, err := r.onPaymentsGetSavedStarGifts(ctx, &tg.PaymentsGetSavedStarGiftsRequest{Peer: &tg.InputPeerSelf{}, Limit: 10})
	if err != nil {
		t.Fatalf("list pinned gifts: %v", err)
	}
	if len(pinned.Gifts) != 2 || !pinned.Gifts[0].PinnedToTop || !pinned.Gifts[1].PinnedToTop {
		t.Fatalf("pinned projection = %#v", pinned.Gifts)
	}

	if ok, err := r.onPaymentsReorderStarGiftCollections(ctx, &tg.PaymentsReorderStarGiftCollectionsRequest{
		Peer: &tg.InputPeerSelf{}, Order: []int{second.CollectionID, first.CollectionID},
	}); err != nil || !ok {
		t.Fatalf("reorder collections = %v err %v", ok, err)
	}
	reorderedClass, err := r.onPaymentsGetStarGiftCollections(ctx, &tg.PaymentsGetStarGiftCollectionsRequest{Peer: &tg.InputPeerSelf{}})
	if err != nil {
		t.Fatalf("list reordered collections: %v", err)
	}
	reordered := reorderedClass.(*tg.PaymentsStarGiftCollections)
	if reordered.Collections[0].CollectionID != second.CollectionID {
		t.Fatalf("reordered collections = %#v", reordered.Collections)
	}

	if ok, err := r.onPaymentsDeleteStarGiftCollection(ctx, &tg.PaymentsDeleteStarGiftCollectionRequest{
		Peer: &tg.InputPeerSelf{}, CollectionID: first.CollectionID,
	}); err != nil || !ok {
		t.Fatalf("delete collection = %v err %v", ok, err)
	}
	afterDelete, err := r.onPaymentsGetSavedStarGifts(ctx, &tg.PaymentsGetSavedStarGiftsRequest{Peer: &tg.InputPeerSelf{}, Limit: 10})
	if err != nil {
		t.Fatalf("list gifts after collection delete: %v", err)
	}
	for _, saved := range afterDelete.Gifts {
		if ids, ok := saved.GetCollectionID(); ok {
			for _, id := range ids {
				if id == first.CollectionID {
					t.Fatalf("deleted collection %d leaked in saved gift %#v", id, saved)
				}
			}
		}
	}
}

// 完整 star gift saga：catalog → getPaymentForm(paymentFormStarGift) → sendStarsForm(扣费+服务消息
// +paymentResult) → 收礼人 getSavedStarGifts → save/convert。
func TestStarGiftSaga(t *testing.T) {
	r, sender, recipient, gift := starGiftTestRouter(t)
	ctx := context.Background()
	senderCtx := WithUserID(ctx, sender.ID)
	recipientCtx := WithUserID(ctx, recipient.ID)

	// 1. 目录。
	catRes, err := r.onPaymentsGetStarGifts(senderCtx, 0)
	if err != nil {
		t.Fatalf("getStarGifts: %v", err)
	}
	cat, ok := catRes.(*tg.PaymentsStarGifts)
	if !ok || len(cat.Gifts) != 1 {
		t.Fatalf("catalog = %T %+v, want 1 gift", catRes, catRes)
	}
	if g, ok := cat.Gifts[0].(*tg.StarGift); !ok || g.ID != gift.ID || g.Stars != 50 {
		t.Fatalf("catalog gift = %#v, want id %d stars 50", cat.Gifts[0], gift.ID)
	}
	// 即便 hash 命中也始终回完整目录（DrKLO force-stop 后保留 hash 但丢礼物缓存，
	// 返 NotModified 会让送礼选择器永远空）。
	if again, err := r.onPaymentsGetStarGifts(senderCtx, cat.Hash); err != nil {
		t.Fatalf("getStarGifts hash: %v", err)
	} else if full, ok := again.(*tg.PaymentsStarGifts); !ok || len(full.Gifts) != 1 {
		t.Fatalf("hash match = %T, want full catalog (不返 NotModified)", again)
	}

	inv := &tg.InputInvoiceStarGift{
		Peer:   &tg.InputPeerUser{UserID: recipient.ID, AccessHash: recipient.AccessHash},
		GiftID: gift.ID,
	}

	// 2. getPaymentForm → paymentFormStarGift（XTR + 非空 prices）。
	formRes, err := r.onPaymentsGetPaymentForm(senderCtx, &tg.PaymentsGetPaymentFormRequest{Invoice: inv})
	if err != nil {
		t.Fatalf("getPaymentForm: %v", err)
	}
	form, ok := formRes.(*tg.PaymentsPaymentFormStarGift)
	if !ok {
		t.Fatalf("form = %T, want *tg.PaymentsPaymentFormStarGift (TDesktop 单分支 match)", formRes)
	}
	if form.Invoice.Currency != "XTR" || len(form.Invoice.Prices) != 1 || form.Invoice.Prices[0].Amount != 50 {
		t.Fatalf("form invoice = %+v, want XTR + 1 price 50", form.Invoice)
	}

	// 3. sendStarsForm → paymentResult（扣费 + 服务消息 + updateStarsBalance）。
	payRes, err := r.onPaymentsSendStarsForm(senderCtx, &tg.PaymentsSendStarsFormRequest{FormID: form.FormID, Invoice: inv})
	if err != nil {
		t.Fatalf("sendStarsForm: %v", err)
	}
	pay, ok := payRes.(*tg.PaymentsPaymentResult)
	if !ok {
		t.Fatalf("pay result = %T, want *tg.PaymentsPaymentResult (DrKLO 强转)", payRes)
	}
	updates, ok := pay.Updates.(*tg.Updates)
	if !ok {
		t.Fatalf("pay updates = %T, want *tg.Updates", pay.Updates)
	}
	hasBalance, hasGiftMsg := false, false
	for _, up := range updates.Updates {
		switch u := up.(type) {
		case *tg.UpdateStarsBalance:
			hasBalance = true
			if amt, ok := u.Balance.(*tg.StarsAmount); !ok || amt.Amount != 950 {
				t.Fatalf("updateStarsBalance = %#v, want 950", u.Balance)
			}
		case *tg.UpdateNewMessage:
			if svc, ok := u.Message.(*tg.MessageService); ok {
				if _, ok := svc.Action.(*tg.MessageActionStarGift); ok {
					hasGiftMsg = true
				}
			}
		}
	}
	if !hasBalance || !hasGiftMsg {
		t.Fatalf("pay updates: balance=%v giftMsg=%v, want both (崩溃约束:必返合法 Updates)", hasBalance, hasGiftMsg)
	}
	// 送礼人余额扣 50。
	if bal, _ := r.deps.Stars.GetBalance(ctx, sender.ID); bal.Balance != 950 {
		t.Fatalf("sender balance = %d, want 950", bal.Balance)
	}

	// 4. 收礼人 getSavedStarGifts。
	savedRes, err := r.onPaymentsGetSavedStarGifts(recipientCtx, &tg.PaymentsGetSavedStarGiftsRequest{Peer: &tg.InputPeerSelf{}})
	if err != nil {
		t.Fatalf("getSavedStarGifts: %v", err)
	}
	if savedRes.Count != 1 || len(savedRes.Gifts) != 1 {
		t.Fatalf("saved gifts = count %d len %d, want 1/1", savedRes.Count, len(savedRes.Gifts))
	}
	saved := savedRes.Gifts[0]
	msgID, ok := saved.GetMsgID()
	if !ok || msgID <= 0 {
		t.Fatalf("saved gift msg_id = %d ok %v, want positive", msgID, ok)
	}
	if g, ok := saved.Gift.(*tg.StarGift); !ok || g.ID != gift.ID {
		t.Fatalf("saved gift inner = %#v, want gift %d", saved.Gift, gift.ID)
	}
	if from, ok := saved.GetFromID(); !ok {
		t.Fatalf("saved gift from = %v, want sender peer", from)
	}

	// 4b. 收礼人 userFull 必须带 stargifts_count（否则客户端资料页 Gifts 区段不出现）。
	fullRes, err := r.onUsersGetFullUser(senderCtx, &tg.InputUser{UserID: recipient.ID, AccessHash: recipient.AccessHash})
	if err != nil {
		t.Fatalf("getFullUser: %v", err)
	}
	if cnt, ok := fullRes.FullUser.GetStargiftsCount(); !ok || cnt != 1 {
		t.Fatalf("recipient userFull stargifts_count = %d ok %v, want 1 (资料页 Gifts 门控)", cnt, ok)
	}

	// 5. saveStarGift（隐藏）。
	if ok, err := r.onPaymentsSaveStarGift(recipientCtx, &tg.PaymentsSaveStarGiftRequest{
		Unsave: true, Stargift: &tg.InputSavedStarGiftUser{MsgID: msgID},
	}); err != nil || !ok {
		t.Fatalf("saveStarGift hide = %v err %v", ok, err)
	}

	// 6. convertStarGift（转回 Stars，收礼人 +50）。
	recipBefore, _ := r.deps.Stars.GetBalance(ctx, recipient.ID)
	if ok, err := r.onPaymentsConvertStarGift(recipientCtx, &tg.InputSavedStarGiftUser{MsgID: msgID}); err != nil || !ok {
		t.Fatalf("convertStarGift = %v err %v", ok, err)
	}
	recipAfter, _ := r.deps.Stars.GetBalance(ctx, recipient.ID)
	if recipAfter.Balance != recipBefore.Balance+50 {
		t.Fatalf("recipient balance %d -> %d, want +50", recipBefore.Balance, recipAfter.Balance)
	}
	// 转换后从列表消失。
	afterRes, _ := r.onPaymentsGetSavedStarGifts(recipientCtx, &tg.PaymentsGetSavedStarGiftsRequest{Peer: &tg.InputPeerSelf{}})
	if afterRes.Count != 0 {
		t.Fatalf("saved gifts after convert = %d, want 0", afterRes.Count)
	}
	// 重复转换被拒。
	if _, err := r.onPaymentsConvertStarGift(recipientCtx, &tg.InputSavedStarGiftUser{MsgID: msgID}); err == nil {
		t.Fatalf("double convert should error")
	}
}

// 频道 star gift saga：channel peer 能付款发送，但不生成频道历史消息；
// saved gift 用 inputSavedStarGiftChat.saved_id 定位，Recent Actions 用 admin log 快照承载。
func TestStarGiftChannelSaga(t *testing.T) {
	r, sender, owner, gift := starGiftTestRouter(t)
	ctx := context.Background()
	senderCtx := WithUserID(ctx, sender.ID)
	ownerCtx := WithUserID(ctx, owner.ID)

	created, err := r.deps.Channels.CreateChannel(ctx, owner.ID, domain.CreateChannelRequest{
		CreatorUserID: owner.ID,
		Title:         "Gift Channel",
		Broadcast:     true,
		MemberUserIDs: []int64{sender.ID},
		Date:          1700001000,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	channel := created.Channel
	channelPeer := &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash}
	channelInput := &tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash}
	giftService := r.deps.Gifts.(*appstargifts.Service)
	if _, err := giftService.PublishCollectibleRevision(ctx, domain.StarGiftCollectibleWrite{
		GiftID: gift.ID, UpgradeStars: 75, SupplyTotal: 10, SlugPrefix: "channel-cake",
		Models:    []domain.StarGiftCollectibleAttribute{collectibleRPCAttribute(domain.StarGiftCollectibleModel, 8201, "Aurora")},
		Patterns:  []domain.StarGiftCollectibleAttribute{collectibleRPCAttribute(domain.StarGiftCollectiblePattern, 8202, "Orbit")},
		Backdrops: []domain.StarGiftCollectibleAttribute{collectibleRPCAttribute(domain.StarGiftCollectibleBackdrop, 2, "Midnight")},
		Actor:     "test", CommandID: "channel-collectible-rpc",
	}); err != nil {
		t.Fatalf("publish channel collectible pool: %v", err)
	}
	if _, err := r.onPaymentsGetPaymentForm(senderCtx, &tg.PaymentsGetPaymentFormRequest{Invoice: &tg.InputInvoiceStarGift{
		Peer: channelPeer, GiftID: gift.ID, IncludeUpgrade: true,
	}}); err == nil {
		t.Fatal("channel include_upgrade must be rejected while channel upgrade is blocked")
	}
	inv := &tg.InputInvoiceStarGift{
		Peer:   channelPeer,
		GiftID: gift.ID,
	}

	formRes, err := r.onPaymentsGetPaymentForm(senderCtx, &tg.PaymentsGetPaymentFormRequest{Invoice: inv})
	if err != nil {
		t.Fatalf("getPaymentForm(channel): %v", err)
	}
	form, ok := formRes.(*tg.PaymentsPaymentFormStarGift)
	if !ok || form.Invoice.Currency != "XTR" || len(form.Invoice.Prices) != 1 {
		t.Fatalf("form(channel) = %T %+v, want star gift XTR form", formRes, formRes)
	}

	payRes, err := r.onPaymentsSendStarsForm(senderCtx, &tg.PaymentsSendStarsFormRequest{FormID: form.FormID, Invoice: inv})
	if err != nil {
		t.Fatalf("sendStarsForm(channel): %v", err)
	}
	pay, ok := payRes.(*tg.PaymentsPaymentResult)
	if !ok {
		t.Fatalf("pay result = %T, want *tg.PaymentsPaymentResult", payRes)
	}
	updates, ok := pay.Updates.(*tg.Updates)
	if !ok {
		t.Fatalf("pay updates = %T, want *tg.Updates", pay.Updates)
	}
	var (
		hasBalance bool
	)
	for _, up := range updates.Updates {
		switch u := up.(type) {
		case *tg.UpdateStarsBalance:
			hasBalance = true
			if amt, ok := u.Balance.(*tg.StarsAmount); !ok || amt.Amount != 950 {
				t.Fatalf("channel updateStarsBalance = %#v, want 950", u.Balance)
			}
		case *tg.UpdateNewChannelMessage:
			t.Fatalf("channel gift must not be pushed as UpdateNewChannelMessage: %#v", u.Message)
		}
	}
	if !hasBalance {
		t.Fatalf("channel pay updates: balance=%v, want updateStarsBalance", hasBalance)
	}

	savedRes, err := r.onPaymentsGetSavedStarGifts(senderCtx, &tg.PaymentsGetSavedStarGiftsRequest{Peer: channelPeer})
	if err != nil {
		t.Fatalf("getSavedStarGifts(channel): %v", err)
	}
	if savedRes.Count != 1 || len(savedRes.Gifts) != 1 {
		t.Fatalf("channel saved gifts = count %d len %d, want 1/1", savedRes.Count, len(savedRes.Gifts))
	}
	if savedRes.Gifts[0].CanUpgrade {
		t.Fatal("channel saved gift must not advertise upgrade while channel aggregate is blocked")
	}
	savedID, ok := savedRes.Gifts[0].GetSavedID()
	if !ok || savedID <= 0 {
		t.Fatalf("saved gift saved_id = %d ok %v, want positive", savedID, ok)
	}
	if _, ok := savedRes.Gifts[0].GetMsgID(); ok {
		t.Fatalf("channel saved gift should not expose inputSavedStarGiftUser.msg_id")
	}
	nextHistoryID := channel.TopMessageID + 1
	history, err := r.onChannelsGetMessages(ownerCtx, &tg.ChannelsGetMessagesRequest{
		Channel: channelInput,
		ID:      []tg.InputMessageClass{&tg.InputMessageID{ID: nextHistoryID}},
	})
	if err != nil {
		t.Fatalf("get channel message after gift payment: %v", err)
	}
	gotMessages := history.(*tg.MessagesMessages).Messages
	if len(gotMessages) != 1 {
		t.Fatalf("channel getMessages len = %d, want 1 messageEmpty", len(gotMessages))
	}
	if _, ok := gotMessages[0].(*tg.MessageEmpty); !ok {
		t.Fatalf("channel gift leaked into message history as %T", gotMessages[0])
	}

	sendFilter := tg.ChannelAdminLogEventsFilter{}
	sendFilter.SetSend(true)
	adminReq := &tg.ChannelsGetAdminLogRequest{Channel: channelInput, Limit: 10}
	adminReq.SetEventsFilter(sendFilter)
	adminLog, err := r.onChannelsGetAdminLog(ownerCtx, adminReq)
	if err != nil {
		t.Fatalf("getAdminLog(channel gift): %v", err)
	}
	foundAdminGift := false
	for _, event := range adminLog.Events {
		send, ok := event.Action.(*tg.ChannelAdminLogEventActionSendMessage)
		if !ok {
			continue
		}
		svc, ok := send.Message.(*tg.MessageService)
		if !ok {
			continue
		}
		action, ok := svc.Action.(*tg.MessageActionStarGift)
		if !ok {
			continue
		}
		if got, ok := action.GetSavedID(); !ok || got != savedID {
			t.Fatalf("admin log star gift saved_id = %d ok %v, want %d", got, ok, savedID)
		}
		if peer, ok := action.GetPeer(); !ok {
			t.Fatalf("admin log star gift peer missing")
		} else if ch, ok := peer.(*tg.PeerChannel); !ok || ch.ChannelID != channel.ID {
			t.Fatalf("admin log star gift peer = %#v, want channel %d", peer, channel.ID)
		}
		foundAdminGift = true
	}
	if !foundAdminGift {
		t.Fatalf("admin log did not include star gift send_message action")
	}

	oneRes, err := r.onPaymentsGetSavedStarGift(senderCtx, []tg.InputSavedStarGiftClass{
		&tg.InputSavedStarGiftChat{Peer: channelPeer, SavedID: savedID},
	})
	if err != nil || oneRes.Count != 1 || len(oneRes.Gifts) != 1 {
		t.Fatalf("getSavedStarGift(channel) count=%d len=%d err=%v, want 1/1", oneRes.Count, len(oneRes.Gifts), err)
	}

	fullRes, err := r.onChannelsGetFullChannel(ownerCtx, channelInput)
	if err != nil {
		t.Fatalf("getFullChannel: %v", err)
	}
	full, ok := fullRes.FullChat.(*tg.ChannelFull)
	if !ok {
		t.Fatalf("full chat = %T, want *tg.ChannelFull", fullRes.FullChat)
	}
	if cnt, ok := full.GetStargiftsCount(); !ok || cnt != 1 {
		t.Fatalf("channelFull stargifts_count = %d ok %v, want 1", cnt, ok)
	}
	if !full.GetStargiftsAvailable() {
		t.Fatalf("channelFull stargifts_available = false, want true for broadcast channel gift entry")
	}

	if ok, err := r.onPaymentsSaveStarGift(ownerCtx, &tg.PaymentsSaveStarGiftRequest{
		Unsave:   true,
		Stargift: &tg.InputSavedStarGiftChat{Peer: channelPeer, SavedID: savedID},
	}); err != nil || !ok {
		t.Fatalf("saveStarGift(channel hide) = %v err %v", ok, err)
	}
	hiddenRes, err := r.onPaymentsGetSavedStarGifts(senderCtx, &tg.PaymentsGetSavedStarGiftsRequest{Peer: channelPeer, ExcludeUnsaved: true})
	if err != nil || hiddenRes.Count != 0 || len(hiddenRes.Gifts) != 0 {
		t.Fatalf("channel excludeUnsaved after hide = count %d len %d err %v, want 0/0", hiddenRes.Count, len(hiddenRes.Gifts), err)
	}
}

// 余额不足 → sendStarsForm 返回 BALANCE_TOO_LOW（不发礼、不扣费）。
func TestStarGiftInsufficientBalance(t *testing.T) {
	ctx := context.Background()
	users := memory.NewUserStore()
	dialogs := memory.NewDialogStore()
	msgStore := memory.NewMessageStore(dialogs)
	sender, _ := users.Create(ctx, domain.User{AccessHash: 7201, Phone: "15550007201", FirstName: "Poor"})
	recipient, _ := users.Create(ctx, domain.User{AccessHash: 7202, Phone: "15550007202", FirstName: "Rich"})
	gift := domain.StarGift{ID: 8002, RevisionID: 9002, Stars: 5000, ConvertStars: 5000, Title: "Expensive",
		Sticker: domain.Document{ID: 701, AccessHash: 7, DCID: 2, MimeType: "application/x-tgsticker", Attributes: []domain.DocumentAttribute{{Kind: domain.DocAttrSticker}}}}
	giftStore := memory.NewStarGiftStore()
	giftStore.SeedCatalog([]domain.StarGift{gift})
	r := New(Config{DC: 2, IP: "127.0.0.1", Port: 2398}, Deps{
		Users:    appusers.NewService(users),
		Messages: appmessages.NewService(msgStore, dialogs),
		Stars:    appstars.NewService(memory.NewStarsStore(), appstars.WithStartingGrant(1000)), // < 5000
		Gifts:    appstargifts.NewService(giftStore, nil, 2),
	}, zaptest.NewLogger(t), clock.System)
	senderCtx := WithUserID(ctx, sender.ID)
	inv := &tg.InputInvoiceStarGift{Peer: &tg.InputPeerUser{UserID: recipient.ID, AccessHash: recipient.AccessHash}, GiftID: gift.ID}
	peer := domain.Peer{Type: domain.PeerTypeUser, ID: recipient.ID}
	if _, err := r.onPaymentsSendStarsForm(senderCtx, &tg.PaymentsSendStarsFormRequest{FormID: starGiftFormID(sender.ID, peer, gift), Invoice: inv}); err == nil {
		t.Fatalf("over-budget gift should error BALANCE_TOO_LOW")
	}
	// 余额未变。
	if bal, _ := r.deps.Stars.GetBalance(ctx, sender.ID); bal.Balance != 1000 {
		t.Fatalf("sender balance = %d, want 1000 unchanged", bal.Balance)
	}
}

func TestStarGiftFormBindsCatalogRevisionAndPrice(t *testing.T) {
	r, sender, recipient, gift := starGiftTestRouter(t)
	ctx := WithUserID(context.Background(), sender.ID)
	peer := domain.Peer{Type: domain.PeerTypeUser, ID: recipient.ID}
	base := starGiftFormID(sender.ID, peer, gift)
	changedRevision := gift
	changedRevision.RevisionID++
	changedPrice := gift
	changedPrice.Stars++
	changedPeer := domain.Peer{Type: domain.PeerTypeUser, ID: recipient.ID + 1}
	if base == starGiftFormID(sender.ID, peer, changedRevision) || base == starGiftFormID(sender.ID, peer, changedPrice) || base == starGiftFormID(sender.ID, changedPeer, gift) {
		t.Fatal("star gift form id must bind revision, price and recipient")
	}
	inv := &tg.InputInvoiceStarGift{Peer: &tg.InputPeerUser{UserID: recipient.ID, AccessHash: recipient.AccessHash}, GiftID: gift.ID}
	if _, err := r.onPaymentsSendStarsForm(ctx, &tg.PaymentsSendStarsFormRequest{FormID: base + 1, Invoice: inv}); !tgerr.Is(err, "STARS_FORM_AMOUNT_MISMATCH") {
		t.Fatalf("bad form err=%v", err)
	}
}

func TestStarsTopupInvoiceFallbackCreditsBalance(t *testing.T) {
	r, sender, _, _ := starGiftTestRouter(t)
	ctx := context.Background()
	senderCtx := WithUserID(ctx, sender.ID)
	opt := devStarsTopupOptions()[1]
	inv := &tg.InputInvoiceStars{Purpose: &tg.InputStorePaymentStarsTopup{
		Stars:    opt.Stars,
		Currency: opt.Currency,
		Amount:   opt.Amount,
	}}

	formRes, err := r.onPaymentsGetPaymentForm(senderCtx, &tg.PaymentsGetPaymentFormRequest{Invoice: inv})
	if err != nil {
		t.Fatalf("getPaymentForm topup: %v", err)
	}
	form, ok := formRes.(*tg.PaymentsPaymentFormStars)
	if !ok {
		t.Fatalf("form = %T, want *tg.PaymentsPaymentFormStars", formRes)
	}
	if form.FormID != starsTopupFormID(sender.ID, opt.Stars, opt.Currency, opt.Amount) {
		t.Fatalf("form id = %d, want deterministic topup id", form.FormID)
	}
	if form.BotID != domain.OfficialSystemUserID || len(form.Users) != 1 {
		t.Fatalf("form bot/users = %d/%d, want official system user", form.BotID, len(form.Users))
	}
	if form.Invoice.Currency != "XTR" || len(form.Invoice.Prices) != 1 || form.Invoice.Prices[0].Amount != opt.Stars {
		t.Fatalf("form invoice = %+v, want XTR + 1 price %d", form.Invoice, opt.Stars)
	}

	if _, err := r.onPaymentsSendStarsForm(senderCtx, &tg.PaymentsSendStarsFormRequest{FormID: form.FormID + 1, Invoice: inv}); !tgerr.Is(err, "STARS_FORM_AMOUNT_MISMATCH") {
		t.Fatalf("sendStarsForm bad form err = %v, want STARS_FORM_AMOUNT_MISMATCH", err)
	}
	if bal, _ := r.deps.Stars.GetBalance(ctx, sender.ID); bal.Balance != 1000 {
		t.Fatalf("balance after bad form = %d, want 1000 unchanged", bal.Balance)
	}

	payRes, err := r.onPaymentsSendStarsForm(senderCtx, &tg.PaymentsSendStarsFormRequest{FormID: form.FormID, Invoice: inv})
	if err != nil {
		t.Fatalf("sendStarsForm topup: %v", err)
	}
	pay, ok := payRes.(*tg.PaymentsPaymentResult)
	if !ok {
		t.Fatalf("pay result = %T, want *tg.PaymentsPaymentResult", payRes)
	}
	updates, ok := pay.Updates.(*tg.Updates)
	if !ok {
		t.Fatalf("pay updates = %T, want *tg.Updates", pay.Updates)
	}
	foundBalance := false
	for _, up := range updates.Updates {
		if balance, ok := up.(*tg.UpdateStarsBalance); ok {
			foundBalance = true
			if amt, ok := balance.Balance.(*tg.StarsAmount); !ok || amt.Amount != 3500 {
				t.Fatalf("updateStarsBalance = %#v, want 3500", balance.Balance)
			}
		}
	}
	if !foundBalance {
		t.Fatalf("payment updates missing updateStarsBalance: %#v", updates.Updates)
	}
	if bal, _ := r.deps.Stars.GetBalance(ctx, sender.ID); bal.Balance != 3500 {
		t.Fatalf("balance after topup = %d, want 3500", bal.Balance)
	}
	page, err := r.deps.Stars.ListTransactions(ctx, sender.ID, "", 10)
	if err != nil {
		t.Fatalf("list transactions: %v", err)
	}
	hasTopup := false
	for _, tx := range page.Transactions {
		if tx.Reason == domain.StarsReasonTopup && tx.Amount == opt.Stars {
			hasTopup = true
		}
	}
	if !hasTopup {
		t.Fatalf("transactions missing topup %d: %+v", opt.Stars, page.Transactions)
	}
}

func TestStarsTopupRejectsUnlistedAmount(t *testing.T) {
	r, sender, _, _ := starGiftTestRouter(t)
	ctx := WithUserID(context.Background(), sender.ID)
	inv := &tg.InputInvoiceStars{Purpose: &tg.InputStorePaymentStarsTopup{
		Stars:    2501,
		Currency: "USD",
		Amount:   199,
	}}
	_, err := r.onPaymentsGetPaymentForm(ctx, &tg.PaymentsGetPaymentFormRequest{Invoice: inv})
	if !tgerr.Is(err, "STARS_FORM_AMOUNT_MISMATCH") {
		t.Fatalf("getPaymentForm unlisted err = %v, want STARS_FORM_AMOUNT_MISMATCH", err)
	}
}
