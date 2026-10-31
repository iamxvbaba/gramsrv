package postgres

import (
	"context"
	"testing"

	"telesrv/internal/domain"
)

// TestChannelMediaIndexSearch verifies the channel shared media index end to end (migration 0118):
// sending various media messages → the write path builds the index → SearchChannelMedia returns
// the right messages by tab category. Gated on TELESRV_TEST_POSTGRES_DSN.
func TestChannelMediaIndexSearch(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)

	users := NewUserStore(pool)
	owner, err := users.Create(ctx, domain.User{AccessHash: 771, Phone: "+1890" + suffix + "01", FirstName: "MediaOwner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	channels := NewChannelStore(pool)
	var channelID int64
	t.Cleanup(func() {
		if channelID != 0 {
			_, _ = pool.Exec(ctx, "DELETE FROM channels WHERE id = $1", channelID)
		}
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = $1", owner.ID)
	})
	created, err := channels.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: owner.ID, Title: "Media " + suffix, Megagroup: true, Date: 1700002000,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	channelID = created.Channel.ID

	send := func(rnd int64, msg string, media *domain.MessageMedia, ents []domain.MessageEntity) int {
		res, err := channels.SendChannelMessage(ctx, domain.SendChannelMessageRequest{
			UserID: owner.ID, ChannelID: channelID, RandomID: rnd, Message: msg, Media: media, Entities: ents, Date: 1700002000 + int(rnd),
		})
		if err != nil {
			t.Fatalf("send channel message %d: %v", rnd, err)
		}
		return res.Message.ID
	}
	docMedia := func(attrs ...domain.DocumentAttribute) *domain.MessageMedia {
		return &domain.MessageMedia{Kind: domain.MessageMediaKindDocument, Document: &domain.Document{ID: 1, AccessHash: 1, Attributes: attrs}}
	}

	photoID := send(1, "pic", &domain.MessageMedia{Kind: domain.MessageMediaKindPhoto, Photo: &domain.Photo{ID: 11, AccessHash: 1}}, nil)
	fileID := send(2, "doc", docMedia(domain.DocumentAttribute{Kind: domain.DocAttrFilename, FileName: "a.pdf"}), nil)
	videoID := send(3, "vid", docMedia(domain.DocumentAttribute{Kind: domain.DocAttrVideo, W: 1, H: 1}), nil)
	urlID := send(4, "see https://x.test", nil, []domain.MessageEntity{{Type: domain.MessageEntityURL, Offset: 4, Length: 14}})
	musicID := send(6, "song", docMedia(domain.DocumentAttribute{Kind: domain.DocAttrAudio, Title: "song"}), nil)
	_ = send(5, "plain text only", nil, nil) // does not go into any tab

	search := func(cats ...domain.MediaCategory) domain.ChannelHistory {
		h, err := channels.SearchChannelMedia(ctx, owner.ID, channelID, domain.MediaSearchRequest{Categories: cats, Limit: 50})
		if err != nil {
			t.Fatalf("search media %v: %v", cats, err)
		}
		return h
	}
	wantIDs := func(name string, h domain.ChannelHistory, ids ...int) {
		if h.Count != len(ids) {
			t.Fatalf("%s: count = %d, want %d", name, h.Count, len(ids))
		}
		if len(h.Messages) != len(ids) {
			t.Fatalf("%s: got %d messages, want %d", name, len(h.Messages), len(ids))
		}
		for i, id := range ids { // newest-first
			if h.Messages[i].ID != id {
				t.Fatalf("%s: message[%d].ID = %d, want %d (order %v)", name, i, h.Messages[i].ID, id, ids)
			}
		}
	}
	wantCount := func(name string, counts domain.MediaCategoryCounts, category domain.MediaCategory, want int) {
		if got := counts[category]; got != want {
			t.Fatalf("%s: count[%d] = %d, want %d", name, category, got, want)
		}
	}

	wantIDs("photos", search(domain.MediaCategoryPhoto), photoID)
	wantIDs("files", search(domain.MediaCategoryFile), fileID)
	wantIDs("video", search(domain.MediaCategoryVideo), videoID)
	wantIDs("url", search(domain.MediaCategoryURL), urlID)
	wantIDs("music", search(domain.MediaCategoryMusic), musicID)
	wantIDs("photoVideo", search(domain.MediaCategoryPhoto, domain.MediaCategoryVideo), videoID, photoID) // newest-first
	wantIDs("voice empty", search(domain.MediaCategoryVoice))
	combined, err := channels.SearchChannelMedia(ctx, owner.ID, channelID, domain.MediaSearchRequest{
		Categories:   []domain.MediaCategory{domain.MediaCategoryVideo},
		Query:        "VID",
		SenderUserID: owner.ID,
		MinDate:      1700002002,
		MaxDate:      1700002004,
		Limit:        50,
	})
	if err != nil {
		t.Fatalf("combined channel media search: %v", err)
	}
	wantIDs("combined channel media", combined, videoID)
	countOnly, err := channels.SearchChannelMedia(ctx, owner.ID, channelID, domain.MediaSearchRequest{
		Categories: []domain.MediaCategory{domain.MediaCategoryPhoto},
		Limit:      0,
	})
	if err != nil {
		t.Fatalf("search count-only media: %v", err)
	}
	if countOnly.Count != 1 || len(countOnly.Messages) != 0 {
		t.Fatalf("count-only media = count %d messages %d, want count 1 and no messages", countOnly.Count, len(countOnly.Messages))
	}
	counts, err := channels.CountChannelMediaCategories(ctx, owner.ID, channelID)
	if err != nil {
		t.Fatalf("count channel media categories: %v", err)
	}
	wantCount("initial", counts, domain.MediaCategoryPhoto, 1)
	wantCount("initial", counts, domain.MediaCategoryFile, 1)
	wantCount("initial", counts, domain.MediaCategoryVideo, 1)
	wantCount("initial", counts, domain.MediaCategoryMusic, 1)
	wantCount("initial", counts, domain.MediaCategoryURL, 1)
	wantCount("initial", counts, domain.MediaCategoryVoice, 0)

	// An edit changes the video message into voice → the index category should migrate accordingly
	// (the video tab no longer contains it, voice does).
	if _, err := channels.EditChannelMessage(ctx, domain.EditChannelMessageRequest{
		UserID: owner.ID, ChannelID: channelID, ID: videoID, Message: "now voice", EditDate: 1700002999,
		Media: docMedia(domain.DocumentAttribute{Kind: domain.DocAttrAudio, Voice: true}),
	}); err != nil {
		t.Fatalf("edit channel message media: %v", err)
	}
	wantIDs("video after edit empty", search(domain.MediaCategoryVideo))
	wantIDs("voice after edit", search(domain.MediaCategoryVoice), videoID)
	counts, err = channels.CountChannelMediaCategories(ctx, owner.ID, channelID)
	if err != nil {
		t.Fatalf("count channel media categories after edit: %v", err)
	}
	wantCount("after edit", counts, domain.MediaCategoryVideo, 0)
	wantCount("after edit", counts, domain.MediaCategoryVoice, 1)
	wantCount("after edit", counts, domain.MediaCategoryMusic, 1)

	if _, err := channels.DeleteChannelMessages(ctx, domain.DeleteChannelMessagesRequest{
		UserID: owner.ID, ChannelID: channelID, IDs: []int{videoID}, Date: 1700003001,
	}); err != nil {
		t.Fatalf("delete channel media message: %v", err)
	}
	counts, err = channels.CountChannelMediaCategories(ctx, owner.ID, channelID)
	if err != nil {
		t.Fatalf("count channel media categories after delete: %v", err)
	}
	wantCount("after delete", counts, domain.MediaCategoryVideo, 0)
	wantCount("after delete", counts, domain.MediaCategoryVoice, 0)
	wantCount("after delete", counts, domain.MediaCategoryMusic, 1)
}

func TestPrivateMediaCategoryCountsMaterialized(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)

	users := NewUserStore(pool)
	alice, err := users.Create(ctx, domain.User{AccessHash: 772, Phone: "+1890" + suffix + "11", FirstName: "MediaAlice"})
	if err != nil {
		t.Fatalf("create alice: %v", err)
	}
	bob, err := users.Create(ctx, domain.User{AccessHash: 773, Phone: "+1890" + suffix + "12", FirstName: "MediaBob"})
	if err != nil {
		t.Fatalf("create bob: %v", err)
	}
	ids := []int64{alice.ID, bob.ID}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM dispatch_outbox WHERE target_user_id = ANY($1::bigint[])", ids)
		_, _ = pool.Exec(ctx, "DELETE FROM user_update_events WHERE user_id = ANY($1::bigint[])", ids)
		_, _ = pool.Exec(ctx, "DELETE FROM message_boxes WHERE owner_user_id = ANY($1::bigint[])", ids)
		_, _ = pool.Exec(ctx, "DELETE FROM private_messages WHERE sender_user_id = ANY($1::bigint[])", ids)
		_, _ = pool.Exec(ctx, "DELETE FROM dialogs WHERE user_id = ANY($1::bigint[])", ids)
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = ANY($1::bigint[])", ids)
	})

	messages := NewMessageStore(pool, WithMessageAllocators(&perUserCounterAllocator{}))
	docMedia := func(attrs ...domain.DocumentAttribute) *domain.MessageMedia {
		return &domain.MessageMedia{Kind: domain.MessageMediaKindDocument, Document: &domain.Document{ID: 2, AccessHash: 2, Attributes: attrs}}
	}
	sent, err := messages.SendPrivateText(ctx, domain.SendPrivateTextRequest{
		SenderUserID: alice.ID, RecipientUserID: bob.ID, RandomID: 991,
		Message: "doc", Media: docMedia(domain.DocumentAttribute{Kind: domain.DocAttrFilename, FileName: "a.pdf"}), Date: 1700003100,
	})
	if err != nil {
		t.Fatalf("send private media: %v", err)
	}
	combined, err := messages.SearchPrivateMedia(ctx, bob.ID, alice.ID, domain.MediaSearchRequest{
		Categories:   []domain.MediaCategory{domain.MediaCategoryFile},
		Query:        "DOC",
		SenderUserID: alice.ID,
		MinDate:      1700003000,
		MaxDate:      1700003200,
		Limit:        10,
	})
	if err != nil {
		t.Fatalf("combined private media search: %v", err)
	}
	if combined.Count != 1 || len(combined.Messages) != 1 || combined.Messages[0].ID != sent.RecipientMessage.ID {
		t.Fatalf("combined private media = count %d messages %+v", combined.Count, combined.Messages)
	}
	wantCount := func(name string, ownerID, peerID int64, category domain.MediaCategory, want int) {
		counts, err := messages.CountPrivateMediaCategories(ctx, ownerID, peerID)
		if err != nil {
			t.Fatalf("%s count private media categories: %v", name, err)
		}
		if got := counts[category]; got != want {
			t.Fatalf("%s count[%d] = %d, want %d", name, category, got, want)
		}
	}
	wantCount("alice initial", alice.ID, bob.ID, domain.MediaCategoryFile, 1)
	wantCount("bob initial", bob.ID, alice.ID, domain.MediaCategoryFile, 1)

	if _, err := messages.EditMessage(ctx, domain.EditMessageRequest{
		OwnerUserID: alice.ID,
		Peer:        domain.Peer{Type: domain.PeerTypeUser, ID: bob.ID},
		ID:          sent.SenderMessage.ID,
		Message:     "voice",
		Media:       docMedia(domain.DocumentAttribute{Kind: domain.DocAttrAudio, Voice: true}),
		EditDate:    1700003101,
	}); err != nil {
		t.Fatalf("edit private media: %v", err)
	}
	wantCount("alice file after edit", alice.ID, bob.ID, domain.MediaCategoryFile, 0)
	wantCount("alice voice after edit", alice.ID, bob.ID, domain.MediaCategoryVoice, 1)
	wantCount("bob file after edit", bob.ID, alice.ID, domain.MediaCategoryFile, 0)
	wantCount("bob voice after edit", bob.ID, alice.ID, domain.MediaCategoryVoice, 1)

	if _, err := messages.DeleteMessages(ctx, domain.DeleteMessagesRequest{
		OwnerUserID: alice.ID, IDs: []int{sent.SenderMessage.ID}, Revoke: true, Date: 1700003102,
	}); err != nil {
		t.Fatalf("delete private media: %v", err)
	}
	wantCount("alice voice after delete", alice.ID, bob.ID, domain.MediaCategoryVoice, 0)
	wantCount("bob voice after delete", bob.ID, alice.ID, domain.MediaCategoryVoice, 0)
}
