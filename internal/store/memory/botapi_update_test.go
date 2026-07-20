package memory

import (
	"context"
	"testing"
	"time"

	"telesrv/internal/domain"
)

func botAPIMessageRequest(botID int64, kind domain.BotAPIUpdateKind, messageID int) domain.EnqueueBotAPIUpdateRequest {
	return domain.EnqueueBotAPIUpdateRequest{
		BotUserID: botID,
		Kind:      kind,
		Peer:      domain.Peer{Type: domain.PeerTypeUser, ID: 2001},
		MessageID: messageID,
		SourcePts: messageID,
		Date:      1700000000 + messageID,
	}
}

func TestBotAPIPollLeaseCompareOwnerAndExpiry(t *testing.T) {
	ctx := context.Background()
	store := NewBotAPIUpdateStore()
	if acquired, err := store.AcquireBotAPIPollLease(ctx, 1001, "one", 20*time.Millisecond); err != nil || !acquired {
		t.Fatalf("first acquire=%v err=%v", acquired, err)
	}
	if acquired, err := store.AcquireBotAPIPollLease(ctx, 1001, "two", time.Second); err != nil || acquired {
		t.Fatalf("competing acquire=%v err=%v", acquired, err)
	}
	if err := store.ReleaseBotAPIPollLease(ctx, 1001, "stale"); err != nil {
		t.Fatal(err)
	}
	if acquired, _ := store.AcquireBotAPIPollLease(ctx, 1001, "two", time.Second); acquired {
		t.Fatal("stale release removed active owner")
	}
	time.Sleep(25 * time.Millisecond)
	if acquired, err := store.AcquireBotAPIPollLease(ctx, 1001, "two", time.Second); err != nil || !acquired {
		t.Fatalf("expired acquire=%v err=%v", acquired, err)
	}
}

func TestBotAPIWebhookLeaseWakeAndAtomicDrop(t *testing.T) {
	ctx := context.Background()
	store := NewBotAPIUpdateStore()
	if _, created, err := store.EnqueueBotAPIUpdate(ctx, botAPIMessageRequest(1001, domain.BotAPIUpdateMessage, 1)); err != nil || !created {
		t.Fatalf("enqueue initial created=%v err=%v", created, err)
	}
	config := domain.BotAPIWebhook{BotUserID: 1001, URL: "https://example.test/hook", MaxConnections: 8}
	if err := store.SetBotAPIWebhook(ctx, config, true); err != nil {
		t.Fatal(err)
	}
	if count, _ := store.PendingBotAPIUpdateCount(ctx, 1001); count != 0 {
		t.Fatalf("pending after atomic drop=%d", count)
	}
	if acquired, err := store.AcquireBotAPIWebhookLease(ctx, 1001, "worker-1", time.Second); err != nil || !acquired {
		t.Fatalf("lease acquire=%v err=%v", acquired, err)
	}
	if acquired, _ := store.AcquireBotAPIWebhookLease(ctx, 1001, "worker-2", time.Second); acquired {
		t.Fatal("second webhook worker acquired active lease")
	}
	if err := store.RecordBotAPIWebhookSuccess(ctx, 1001, "worker-1", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if due, err := store.ListDueBotAPIWebhooks(ctx, 10); err != nil || len(due) != 0 {
		t.Fatalf("idle due=%#v err=%v", due, err)
	}
	if _, created, err := store.EnqueueBotAPIUpdate(ctx, botAPIMessageRequest(1001, domain.BotAPIUpdateMessage, 2)); err != nil || !created {
		t.Fatalf("enqueue wake created=%v err=%v", created, err)
	}
	if due, err := store.ListDueBotAPIWebhooks(ctx, 10); err != nil || len(due) != 1 || due[0].BotUserID != 1001 {
		t.Fatalf("woken due=%#v err=%v", due, err)
	}
}

func TestBotAPIWebhookAllowedUpdatesOmissionPreservesPolicy(t *testing.T) {
	ctx := context.Background()
	store := NewBotAPIUpdateStore()
	if err := store.SetBotAPIAllowedUpdates(ctx, 1001, []domain.BotAPIUpdateKind{domain.BotAPIUpdateCallbackQuery}); err != nil {
		t.Fatal(err)
	}
	config := domain.BotAPIWebhook{BotUserID: 1001, URL: "https://example.test/one", MaxConnections: 8}
	if err := store.SetBotAPIWebhook(ctx, config, false); err != nil {
		t.Fatal(err)
	}
	stored, found, err := store.BotAPIWebhook(ctx, 1001)
	if err != nil || !found || len(stored.AllowedUpdates) != 1 || stored.AllowedUpdates[0] != domain.BotAPIUpdateCallbackQuery {
		t.Fatalf("preserved webhook=%#v found=%v err=%v", stored, found, err)
	}
	if row, created, err := store.EnqueueBotAPIUpdate(ctx, botAPIMessageRequest(1001, domain.BotAPIUpdateMessage, 1)); err != nil || created || row.ID != 0 {
		t.Fatalf("message bypassed preserved policy: row=%#v created=%v err=%v", row, created, err)
	}
	config.URL = "https://example.test/two"
	config.AllowedUpdatesSet = true // Explicit empty resets to the default/all policy.
	if err := store.SetBotAPIWebhook(ctx, config, false); err != nil {
		t.Fatal(err)
	}
	stored, found, err = store.BotAPIWebhook(ctx, 1001)
	if err != nil || !found || stored.AllowedUpdates != nil {
		t.Fatalf("explicit empty webhook=%#v found=%v err=%v", stored, found, err)
	}
	if _, created, err := store.EnqueueBotAPIUpdate(ctx, botAPIMessageRequest(1001, domain.BotAPIUpdateMessage, 2)); err != nil || !created {
		t.Fatalf("message after explicit reset created=%v err=%v", created, err)
	}
}

func TestBotAPIUpdateCursorClampDropAndTail(t *testing.T) {
	ctx := context.Background()
	store := NewBotAPIUpdateStore()
	for id := 1; id <= 5; id++ {
		if _, created, err := store.EnqueueBotAPIUpdate(ctx, botAPIMessageRequest(1001, domain.BotAPIUpdateMessage, id)); err != nil || !created {
			t.Fatalf("enqueue %d: created=%v err=%v", id, created, err)
		}
	}
	tail, err := store.ListTailBotAPIUpdates(ctx, 1001, 2, 100)
	if err != nil || len(tail) != 2 || tail[0].MessageID != 4 || tail[1].MessageID != 5 {
		t.Fatalf("tail = %#v err=%v", tail, err)
	}
	if err := store.ConfirmBotAPIUpdates(ctx, 1001, 1<<60); err != nil {
		t.Fatalf("confirm huge offset: %v", err)
	}
	confirmed, found, err := store.ConfirmedBotAPIUpdateID(ctx, 1001)
	if err != nil || !found || confirmed != 5 {
		t.Fatalf("confirmed = %d found=%v err=%v, want 5", confirmed, found, err)
	}
	row, created, err := store.EnqueueBotAPIUpdate(ctx, botAPIMessageRequest(1001, domain.BotAPIUpdateMessage, 6))
	if err != nil || !created {
		t.Fatalf("enqueue after huge offset: row=%#v created=%v err=%v", row, created, err)
	}
	if err := store.ConfirmBotAPIUpdates(ctx, 1001, 1<<60); err != nil {
		t.Fatalf("repeat foreign offset: %v", err)
	}
	if confirmed, _, _ := store.ConfirmedBotAPIUpdateID(ctx, 1001); confirmed != 5 {
		t.Fatalf("repeat foreign offset advanced cursor to %d, want 5", confirmed)
	}
	pending, err := store.ListBotAPIUpdates(ctx, 1001, confirmed+1, 100)
	if err != nil || len(pending) != 1 || pending[0].MessageID != 6 {
		t.Fatalf("pending after huge offset = %#v err=%v", pending, err)
	}
	if err := store.DropPendingBotAPIUpdates(ctx, 1001); err != nil {
		t.Fatalf("drop pending: %v", err)
	}
	count, err := store.PendingBotAPIUpdateCount(ctx, 1001)
	if err != nil || count != 0 {
		t.Fatalf("pending count = %d err=%v", count, err)
	}
}

func TestBotAPIAllowedUpdatesOnlyAffectsFutureEnqueue(t *testing.T) {
	ctx := context.Background()
	store := NewBotAPIUpdateStore()
	first, created, err := store.EnqueueBotAPIUpdate(ctx, botAPIMessageRequest(1001, domain.BotAPIUpdateMessage, 1))
	if err != nil || !created {
		t.Fatalf("enqueue pre-policy: %#v created=%v err=%v", first, created, err)
	}
	if err := store.SetBotAPIAllowedUpdates(ctx, 1001, []domain.BotAPIUpdateKind{domain.BotAPIUpdateEditedMessage}); err != nil {
		t.Fatalf("set policy: %v", err)
	}
	if row, created, err := store.EnqueueBotAPIUpdate(ctx, botAPIMessageRequest(1001, domain.BotAPIUpdateMessage, 2)); err != nil || created || row.ID != 0 {
		t.Fatalf("filtered message = %#v created=%v err=%v", row, created, err)
	}
	if _, created, err := store.EnqueueBotAPIUpdate(ctx, botAPIMessageRequest(1001, domain.BotAPIUpdateEditedMessage, 3)); err != nil || !created {
		t.Fatalf("allowed edit created=%v err=%v", created, err)
	}
	rows, err := store.ListBotAPIUpdates(ctx, 1001, 1, 100)
	if err != nil || len(rows) != 2 || rows[0].ID != first.ID || rows[1].Kind != domain.BotAPIUpdateEditedMessage {
		t.Fatalf("rows = %#v err=%v", rows, err)
	}
}

func TestBotAPIInlineCallbackRoundTrip(t *testing.T) {
	ctx := context.Background()
	store := NewBotAPIUpdateStore()
	callback := &domain.BotCallbackQuery{
		ID: 77, BotUserID: 1001, UserID: 2001, ChatInstance: 99, Data: []byte("tap"),
		InlineMessage: &domain.BotInlineMessageID{DCID: 2, OwnerID: 2001, ID: 15, AccessHash: 1234},
	}
	row, created, err := store.EnqueueBotAPIUpdate(ctx, domain.EnqueueBotAPIUpdateRequest{
		BotUserID: 1001, Kind: domain.BotAPIUpdateCallbackQuery, Date: int(time.Now().Unix()), Callback: callback,
	})
	if err != nil || !created || row.MessageID != 0 || row.Peer != (domain.Peer{}) || row.Callback == nil ||
		row.Callback.InlineMessage == nil || *row.Callback.InlineMessage != *callback.InlineMessage {
		t.Fatalf("inline callback row=%#v created=%v err=%v", row, created, err)
	}
	callback.Data[0] = 'X'
	callback.InlineMessage.ID = 99
	rows, err := store.ListBotAPIUpdates(ctx, 1001, 1, 100)
	if err != nil || len(rows) != 1 || string(rows[0].Callback.Data) != "tap" || rows[0].Callback.InlineMessage.ID != 15 {
		t.Fatalf("inline callback rows=%#v err=%v", rows, err)
	}
}

func TestBotAPIEphemeralMessageVersionsAndCallbackRoundTrip(t *testing.T) {
	ctx := context.Background()
	store := NewBotAPIUpdateStore()
	now := time.Now()
	peer := domain.Peer{Type: domain.PeerTypeChannel, ID: 3001}
	incoming := domain.EphemeralMessage{
		ID: 71, Peer: peer, SenderUserID: 2001, ReceiverUserID: 1001,
		Date: int(now.Unix()), RandomID: 1, Content: domain.EphemeralContent{Message: "/private"},
		Version: 1, CreatedAt: now, ExpiresAt: now.Add(domain.EphemeralMessageRetention),
	}
	request := domain.EnqueueBotAPIUpdateRequest{
		BotUserID: 1001, Kind: domain.BotAPIUpdateMessage, Peer: peer,
		MessageID: incoming.ID, Date: incoming.Date,
		Ephemeral: domain.NewBotAPIEphemeralPayload(incoming),
	}
	first, created, err := store.EnqueueBotAPIUpdate(ctx, request)
	if err != nil || !created || first.SourcePts != 0 || first.Ephemeral == nil {
		t.Fatalf("first=%+v created=%v err=%v", first, created, err)
	}
	if replay, created, err := store.EnqueueBotAPIUpdate(ctx, request); err != nil || created || replay.ID != first.ID {
		t.Fatalf("replay=%+v created=%v err=%v", replay, created, err)
	}
	incoming.Version = 2
	incoming.EditDate = incoming.Date + 1
	incoming.Content.Message = "edited"
	request.Kind = domain.BotAPIUpdateEditedMessage
	request.Ephemeral = domain.NewBotAPIEphemeralPayload(incoming)
	edited, created, err := store.EnqueueBotAPIUpdate(ctx, request)
	if err != nil || !created || edited.ID <= first.ID {
		t.Fatalf("edited=%+v created=%v err=%v", edited, created, err)
	}

	outgoing := incoming
	outgoing.ID, outgoing.SenderUserID, outgoing.ReceiverUserID = 72, 1001, 2001
	outgoing.Version, outgoing.Content.Message = 1, "button"
	callback := &domain.BotCallbackQuery{
		ID: 9001, BotUserID: 1001, UserID: 2001, Peer: peer,
		MessageID: outgoing.ID, ChatInstance: 901, Data: []byte("tap"),
	}
	callbackRow, created, err := store.EnqueueBotAPIUpdate(ctx, domain.EnqueueBotAPIUpdateRequest{
		BotUserID: 1001, Kind: domain.BotAPIUpdateCallbackQuery, Peer: peer,
		MessageID: outgoing.ID, Date: outgoing.Date, Callback: callback,
		Ephemeral: domain.NewBotAPIEphemeralPayload(outgoing),
	})
	if err != nil || !created || callbackRow.Callback == nil || callbackRow.Ephemeral == nil {
		t.Fatalf("callback=%+v created=%v err=%v", callbackRow, created, err)
	}
	rows, err := store.ListBotAPIUpdates(ctx, 1001, first.ID, 100)
	if err != nil || len(rows) != 3 || rows[0].Ephemeral.Message.Content.Message != "/private" ||
		rows[1].Ephemeral.Message.Content.Message != "edited" || string(rows[2].Callback.Data) != "tap" {
		t.Fatalf("rows=%+v err=%v", rows, err)
	}
}
