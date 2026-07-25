package postgres

import (
	"context"
	"testing"
	"time"

	"telesrv/internal/domain"
)

func TestSavedMessageTagsPostgresAssignmentCountsSearchAndDelete(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)
	users := NewUserStore(pool)
	user, err := users.Create(ctx, domain.User{
		AccessHash: 1,
		Phone:      "+1777" + suffix + "01",
		FirstName:  "SavedTags",
	})
	if err != nil {
		t.Fatalf("create saved-tag user: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM saved_message_reaction_tags WHERE user_id = $1", user.ID)
		_, _ = pool.Exec(ctx, "DELETE FROM user_saved_reaction_tags WHERE user_id = $1", user.ID)
		_, _ = pool.Exec(ctx, "DELETE FROM message_boxes WHERE owner_user_id = $1", user.ID)
		_, _ = pool.Exec(ctx, "DELETE FROM private_messages WHERE sender_user_id = $1 OR recipient_user_id = $1", user.ID)
		_, _ = pool.Exec(ctx, "DELETE FROM user_update_events WHERE user_id = $1", user.ID)
		_, _ = pool.Exec(ctx, "DELETE FROM dialogs WHERE user_id = $1", user.ID)
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = $1", user.ID)
	})

	messages := NewMessageStore(pool)
	self := domain.Peer{Type: domain.PeerTypeUser, ID: user.ID}
	peerA := domain.Peer{Type: domain.PeerTypeUser, ID: user.ID}
	peerB := domain.Peer{Type: domain.PeerTypeChannel, ID: 90001}
	create := func(body string, savedPeer domain.Peer) domain.Message {
		msg, err := messages.Create(ctx, domain.Message{
			OwnerUserID: user.ID,
			Peer:        self,
			From:        self,
			Date:        int(time.Now().Unix()),
			Body:        body,
		})
		if err != nil {
			t.Fatalf("create saved message: %v", err)
		}
		if _, err := pool.Exec(ctx, `
UPDATE message_boxes
SET saved_peer_type = $3, saved_peer_id = $4
WHERE owner_user_id = $1 AND box_id = $2`,
			user.ID, msg.ID, string(savedPeer.Type), savedPeer.ID); err != nil {
			t.Fatalf("set saved peer: %v", err)
		}
		msg.SavedPeer = savedPeer
		return msg
	}
	first := create("first", peerA)
	second := create("second", peerA)
	third := create("third", peerB)
	thumb := domain.MessageReaction{Type: domain.MessageReactionEmoji, Emoticon: "👍"}
	custom := domain.MessageReaction{Type: domain.MessageReactionCustomEmoji, DocumentID: 70001}

	set := func(msg domain.Message, reactions ...domain.MessageReaction) {
		t.Helper()
		result, err := messages.SetMessageReactions(ctx, domain.SetPrivateMessageReactionsRequest{
			UserID:              user.ID,
			Peer:                self,
			MessageID:           msg.ID,
			Reactions:           reactions,
			ReactionsPerUserMax: 3,
		})
		if err != nil {
			t.Fatalf("set saved tags on %d: %v", msg.ID, err)
		}
		if len(result.Messages) != 1 || result.Messages[0].Reactions == nil ||
			!result.Messages[0].Reactions.AsTags {
			t.Fatalf("set saved tags result = %+v", result)
		}
	}
	set(first, thumb)
	set(second, thumb, custom)
	set(third, custom)
	if err := messages.UpsertSavedReactionTag(ctx, domain.SavedReactionTag{
		UserID: user.ID, Reaction: custom, Title: "Custom",
	}); err != nil {
		t.Fatalf("rename custom saved tag: %v", err)
	}

	global, err := messages.ListSavedReactionTags(ctx, domain.SavedReactionTagsRequest{
		UserID: user.ID, Limit: 100,
	})
	if err != nil {
		t.Fatalf("list global saved tags: %v", err)
	}
	assertPostgresSavedTag(t, global, thumb, 2, "")
	assertPostgresSavedTag(t, global, custom, 2, "Custom")

	perPeer, err := messages.ListSavedReactionTags(ctx, domain.SavedReactionTagsRequest{
		UserID: user.ID, SavedPeer: peerA, Limit: 100,
	})
	if err != nil {
		t.Fatalf("list per-peer saved tags: %v", err)
	}
	assertPostgresSavedTag(t, perPeer, thumb, 2, "")
	assertPostgresSavedTag(t, perPeer, custom, 1, "")

	search, err := messages.ListByUser(ctx, user.ID, domain.MessageFilter{
		HasPeer:        true,
		Peer:           self,
		SavedPeer:      peerA,
		SavedReactions: []domain.MessageReaction{custom},
		NeedTotalCount: true,
		Limit:          10,
	})
	if err != nil {
		t.Fatalf("search saved tag: %v", err)
	}
	if len(search.Messages) != 1 || search.Messages[0].ID != second.ID ||
		search.Messages[0].Reactions == nil || !search.Messages[0].Reactions.AsTags {
		t.Fatalf("saved tag search = %+v, want second message", search.Messages)
	}
	searchAny, err := messages.ListByUser(ctx, user.ID, domain.MessageFilter{
		HasPeer:        true,
		Peer:           self,
		SavedReactions: []domain.MessageReaction{thumb, custom},
		NeedTotalCount: true,
		Limit:          10,
	})
	if err != nil {
		t.Fatalf("search any saved tag: %v", err)
	}
	if len(searchAny.Messages) != 3 || searchAny.Count != 3 {
		t.Fatalf("saved tag OR search = count %d messages %+v, want all three", searchAny.Count, searchAny.Messages)
	}

	var reactionEvents int
	if err := pool.QueryRow(ctx, `
SELECT COUNT(*)::int
FROM user_update_events
WHERE user_id = $1 AND event_type = 'message_reactions'`, user.ID).Scan(&reactionEvents); err != nil {
		t.Fatalf("count reaction events: %v", err)
	}
	if reactionEvents != 0 {
		t.Fatalf("reaction durable events = %d, want 0", reactionEvents)
	}

	if _, err := messages.DeleteMessages(ctx, domain.DeleteMessagesRequest{
		OwnerUserID: user.ID,
		IDs:         []int{second.ID},
		Date:        int(time.Now().Unix()),
	}); err != nil {
		t.Fatalf("delete tagged saved message: %v", err)
	}
	global, err = messages.ListSavedReactionTags(ctx, domain.SavedReactionTagsRequest{
		UserID: user.ID, Limit: 100,
	})
	if err != nil {
		t.Fatalf("list tags after delete: %v", err)
	}
	assertPostgresSavedTag(t, global, thumb, 1, "")
	assertPostgresSavedTag(t, global, custom, 1, "Custom")
}

func assertPostgresSavedTag(t *testing.T, tags []domain.SavedReactionTag, reaction domain.MessageReaction, count int, title string) {
	t.Helper()
	for _, tag := range tags {
		if tag.Reaction.Key() == reaction.Key() {
			if tag.Count != count || tag.Title != title {
				t.Fatalf("tag %s = %+v, want count=%d title=%q", reaction.Key(), tag, count, title)
			}
			return
		}
	}
	t.Fatalf("tag %s not found in %+v", reaction.Key(), tags)
}
