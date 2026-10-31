package memory

import (
	"testing"

	"telesrv/internal/domain"
)

// TestChannelUnreadCountClampedToMax locks P1-v: the unread COUNT is clamped to
// MaxDialogUnreadCount (min(actual, cap)), the same semantics as postgres's LIMIT subquery.
// Feeding s.messages directly avoids building the chain with 1000+ sends.
func TestChannelUnreadCountClampedToMax(t *testing.T) {
	const (
		channelID = int64(7000000001)
		viewer    = int64(5001)
		sender    = int64(5002)
	)
	s := NewChannelStore()

	over := domain.MaxDialogUnreadCount + 25
	msgs := make([]domain.ChannelMessage, 0, over)
	for i := 1; i <= over; i++ {
		msgs = append(msgs, domain.ChannelMessage{ChannelID: channelID, ID: i, SenderUserID: sender, ReplyTo: &domain.MessageReply{TopMessageID: 100}})
	}
	s.mu.Lock()
	s.messages[channelID] = msgs
	s.mu.Unlock()

	// All unread (readMaxID=0, topID covers everything) -> must clamp to cap, not over.
	if got := s.channelUnreadCountLocked(viewer, channelID, 0, over); got != domain.MaxDialogUnreadCount {
		t.Fatalf("channelUnreadCountLocked = %d, want clamped %d", got, domain.MaxDialogUnreadCount)
	}
	// Thread unread is clamped to cap as well (all messages hang off the root=100 thread).
	if got := s.channelThreadUnreadCountLocked(viewer, channelID, 100, 0); got != domain.MaxDialogUnreadCount {
		t.Fatalf("channelThreadUnreadCountLocked = %d, want clamped %d", got, domain.MaxDialogUnreadCount)
	}

	// Below the limit the exact value is returned (not clamped while within cap).
	const fewChannel = int64(7000000002)
	few := make([]domain.ChannelMessage, 0, 3)
	for i := 1; i <= 3; i++ {
		few = append(few, domain.ChannelMessage{ChannelID: fewChannel, ID: i, SenderUserID: sender})
	}
	s.mu.Lock()
	s.messages[fewChannel] = few
	s.mu.Unlock()
	if got := s.channelUnreadCountLocked(viewer, fewChannel, 0, 3); got != 3 {
		t.Fatalf("channelUnreadCountLocked(few) = %d, want exact 3 (below cap unaffected)", got)
	}
}
