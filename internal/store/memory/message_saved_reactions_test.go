package memory

import (
	"context"
	"testing"

	"telesrv/internal/domain"
)

func TestSavedMessageTagsAssignmentCountsSearchAndDelete(t *testing.T) {
	ctx := context.Background()
	const userID int64 = 1001
	self := domain.Peer{Type: domain.PeerTypeUser, ID: userID}
	peerA := domain.Peer{Type: domain.PeerTypeUser, ID: 2001}
	peerB := domain.Peer{Type: domain.PeerTypeChannel, ID: 3001}
	thumb := domain.MessageReaction{Type: domain.MessageReactionEmoji, Emoticon: "👍"}
	custom := domain.MessageReaction{Type: domain.MessageReactionCustomEmoji, DocumentID: 90001}

	store := NewMessageStore()
	create := func(body string, savedPeer domain.Peer) domain.Message {
		msg, err := store.Create(ctx, domain.Message{
			OwnerUserID: userID,
			Peer:        self,
			From:        self,
			SavedPeer:   savedPeer,
			Date:        1_700_000_000,
			Body:        body,
		})
		if err != nil {
			t.Fatalf("create saved message: %v", err)
		}
		return msg
	}
	first := create("first", peerA)
	second := create("second", peerA)
	third := create("third", peerB)

	set := func(msg domain.Message, reactions ...domain.MessageReaction) {
		t.Helper()
		result, err := store.SetMessageReactions(ctx, domain.SetPrivateMessageReactionsRequest{
			UserID:              userID,
			Peer:                self,
			MessageID:           msg.ID,
			Reactions:           reactions,
			ReactionsPerUserMax: 3,
		})
		if err != nil {
			t.Fatalf("set saved tags for %d: %v", msg.ID, err)
		}
		if len(result.Messages) != 1 || result.Messages[0].Reactions == nil ||
			!result.Messages[0].Reactions.AsTags {
			t.Fatalf("saved tag result = %+v, want one reactions_as_tags message", result)
		}
	}
	set(first, thumb)
	set(second, thumb, custom)
	set(third, custom)
	if got := store.nextPts[userID]; got != 0 {
		t.Fatalf("tag mutations pts = %d, want 0", got)
	}

	if err := store.UpsertSavedReactionTag(ctx, domain.SavedReactionTag{
		UserID: userID, Reaction: thumb, Title: "Fav",
	}); err != nil {
		t.Fatalf("rename saved tag: %v", err)
	}
	global, err := store.ListSavedReactionTags(ctx, domain.SavedReactionTagsRequest{UserID: userID, Limit: 100})
	if err != nil {
		t.Fatalf("list global saved tags: %v", err)
	}
	assertMemorySavedTag(t, global, thumb, 2, "Fav")
	assertMemorySavedTag(t, global, custom, 2, "")

	perPeer, err := store.ListSavedReactionTags(ctx, domain.SavedReactionTagsRequest{
		UserID: userID, SavedPeer: peerA, Limit: 100,
	})
	if err != nil {
		t.Fatalf("list per-peer saved tags: %v", err)
	}
	assertMemorySavedTag(t, perPeer, thumb, 2, "")
	assertMemorySavedTag(t, perPeer, custom, 1, "")

	found, err := store.ListByUser(ctx, userID, domain.MessageFilter{
		HasPeer:        true,
		Peer:           self,
		SavedPeer:      peerA,
		SavedReactions: []domain.MessageReaction{custom},
		Limit:          10,
	})
	if err != nil {
		t.Fatalf("search saved tag: %v", err)
	}
	if len(found.Messages) != 1 || found.Messages[0].ID != second.ID ||
		found.Messages[0].Reactions == nil || !found.Messages[0].Reactions.AsTags {
		t.Fatalf("saved tag search = %+v, want second message", found.Messages)
	}
	foundAny, err := store.ListByUser(ctx, userID, domain.MessageFilter{
		HasPeer:        true,
		Peer:           self,
		SavedReactions: []domain.MessageReaction{thumb, custom},
		Limit:          10,
	})
	if err != nil {
		t.Fatalf("search any saved tag: %v", err)
	}
	if len(foundAny.Messages) != 3 {
		t.Fatalf("saved tag OR search = %+v, want all three messages", foundAny.Messages)
	}

	if _, err := store.DeleteMessages(ctx, domain.DeleteMessagesRequest{
		OwnerUserID: userID,
		IDs:         []int{second.ID},
		Date:        1_700_000_100,
	}); err != nil {
		t.Fatalf("delete tagged message: %v", err)
	}
	global, err = store.ListSavedReactionTags(ctx, domain.SavedReactionTagsRequest{UserID: userID, Limit: 100})
	if err != nil {
		t.Fatalf("list tags after delete: %v", err)
	}
	assertMemorySavedTag(t, global, thumb, 1, "Fav")
	assertMemorySavedTag(t, global, custom, 1, "")

	set(first)
	global, err = store.ListSavedReactionTags(ctx, domain.SavedReactionTagsRequest{UserID: userID, Limit: 100})
	if err != nil {
		t.Fatalf("list tags after clear: %v", err)
	}
	if len(global) != 1 || global[0].Reaction.Key() != custom.Key() {
		t.Fatalf("tags after clear = %+v, want only custom", global)
	}
	if err := store.UpsertSavedReactionTag(ctx, domain.SavedReactionTag{
		UserID: userID, Reaction: thumb, Title: "ghost",
	}); err != domain.ErrReactionInvalid {
		t.Fatalf("rename unassigned tag err = %v, want ErrReactionInvalid", err)
	}
}

func assertMemorySavedTag(t *testing.T, tags []domain.SavedReactionTag, reaction domain.MessageReaction, count int, title string) {
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
