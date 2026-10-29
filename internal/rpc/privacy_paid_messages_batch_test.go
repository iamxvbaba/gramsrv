package rpc

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"go.uber.org/zap/zaptest"

	appaccount "telesrv/internal/app/account"
	appmessages "telesrv/internal/app/messages"
	appprivacy "telesrv/internal/app/privacy"
	appusers "telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store"
	"telesrv/internal/store/memory"
)

// paidBatchStars is a minimal Stars ledger for the batch paid-message tests.
// The embedded interface covers the v2 StarsService methods these tests never
// call.
type paidBatchStars struct {
	StarsService
	balances map[int64]int64
}

func (s *paidBatchStars) GetBalance(_ context.Context, userID int64) (domain.StarsBalance, error) {
	return domain.StarsBalance{UserID: userID, Balance: s.balances[userID]}, nil
}

func (s *paidBatchStars) Credit(_ context.Context, userID, amount int64, _ domain.StarsTransactionReason, _ domain.Peer, _, _ string) (domain.StarsBalance, error) {
	s.balances[userID] += amount
	return domain.StarsBalance{UserID: userID, Balance: s.balances[userID]}, nil
}

func (s *paidBatchStars) Debit(_ context.Context, userID, amount int64, _ domain.StarsTransactionReason, _ domain.Peer, _, _ string) (domain.StarsBalance, error) {
	if s.balances[userID] < amount {
		return domain.StarsBalance{}, domain.ErrStarsInsufficient
	}
	s.balances[userID] -= amount
	return domain.StarsBalance{UserID: userID, Balance: s.balances[userID]}, nil
}

func (s *paidBatchStars) ListTransactions(context.Context, int64, domain.StarsTransactionQuery) (domain.StarsTransactionPage, error) {
	return domain.StarsTransactionPage{}, nil
}

// paidBatchMessages charges SendPrivateText the way the PostgreSQL store does:
// it refuses an uncovered charge and reports both balances after the debit.
type paidBatchMessages struct {
	*appmessages.Service
	stars         *paidBatchStars
	paid          []int64
	scheduledPaid []int64
	afterSend     func(sent int)
}

// ScheduleMessage records the stored amount; the memory store here has no scheduled table.
func (m *paidBatchMessages) ScheduleMessage(_ context.Context, _ int64, req domain.ScheduleMessageRequest, _ store.DeliveryEffectsBuilder[domain.ScheduledMessage]) (domain.ScheduledMessage, error) {
	m.scheduledPaid = append(m.scheduledPaid, req.PaidStars)
	if m.afterSend != nil {
		m.afterSend(len(m.scheduledPaid))
	}
	return domain.ScheduledMessage{
		OwnerUserID:  req.OwnerUserID,
		ID:           len(m.scheduledPaid),
		Peer:         req.Peer,
		RandomID:     req.RandomID,
		Media:        req.Media,
		ScheduleDate: req.ScheduleDate,
		PaidStars:    req.PaidStars,
		CreatedAt:    req.Date,
		State:        "pending",
	}, nil
}

func (m *paidBatchMessages) SendPrivateText(ctx context.Context, userID int64, req domain.SendPrivateTextRequest) (domain.SendPrivateTextResult, error) {
	if req.PaidStars > 0 && req.PaidStarsBalance == nil {
		// v2: the PostgreSQL store rejects a charge it cannot deliver durably.
		return domain.SendPrivateTextResult{}, errors.New("paid private send requires durable stars balance delivery")
	}
	if req.PaidStars > 0 && m.stars.balances[req.SenderUserID] < req.PaidStars {
		return domain.SendPrivateTextResult{}, domain.ErrStarsInsufficient
	}
	res, err := m.Service.SendPrivateText(ctx, userID, req)
	if err != nil || res.Duplicate {
		return res, err
	}
	if req.PaidStars > 0 {
		m.stars.balances[req.SenderUserID] -= req.PaidStars
		m.stars.balances[req.RecipientUserID] += req.PaidStars * paidMessageChannelCommissionPermilleForTest / 1000
		res.PaidStars = &domain.PaidStarsCharge{
			Amount:           req.PaidStars,
			SenderBalance:    m.stars.balances[req.SenderUserID],
			RecipientBalance: m.stars.balances[req.RecipientUserID],
		}
	}
	m.paid = append(m.paid, req.PaidStars)
	if m.afterSend != nil {
		m.afterSend(len(m.paid))
	}
	return res, nil
}

const paidMessageChannelCommissionPermilleForTest = 850

func newPaidBatchRouter(t *testing.T) (*Router, *appaccount.Service, *paidBatchMessages, *paidBatchStars, domain.User, domain.User) {
	t.Helper()
	ctx := context.Background()
	usersStore := memory.NewUserStore()
	alice, err := usersStore.Create(ctx, domain.User{AccessHash: 41, Phone: "15550008401", FirstName: "Payer"})
	if err != nil {
		t.Fatalf("create payer: %v", err)
	}
	bob, err := usersStore.Create(ctx, domain.User{AccessHash: 42, Phone: "15550008402", FirstName: "Payee"})
	if err != nil {
		t.Fatalf("create payee: %v", err)
	}
	users := appusers.NewService(usersStore)
	contacts := memory.NewContactStore()
	privacy := appprivacy.NewService(memory.NewPrivacyStore(), contacts).ConfigureReadModels(users, nil)
	settingsStore := memory.NewPasswordStore()
	account := appaccount.NewService(settingsStore, appaccount.WithAccountSettings(settingsStore))
	dialogStore := memory.NewDialogStore()
	stars := &paidBatchStars{balances: map[int64]int64{}}
	messages := &paidBatchMessages{Service: appmessages.NewService(memory.NewMessageStore(dialogStore), dialogStore), stars: stars}
	router := New(Config{}, Deps{
		Account:        account,
		Privacy:        privacy,
		Users:          users,
		Messages:       messages,
		Stars:          stars,
		DeliveryOutbox: memory.NewDeliveryOutboxStore(),
	}, zaptest.NewLogger(t), clock.System)
	return router, account, messages, stars, alice, bob
}

func setPaidBatchPrice(t *testing.T, r *Router, account *appaccount.Service, userID, stars int64) {
	t.Helper()
	if _, err := account.SetGlobalPrivacy(context.Background(), userID, domain.GlobalPrivacy{NoncontactPeersPaidStars: stars}); err != nil {
		t.Fatalf("set paid requirement %d: %v", stars, err)
	}
	r.accountSettings.Delete(userID)
}

func paidBatchAlbum(randomIDs ...int64) []tg.InputSingleMedia {
	items := make([]tg.InputSingleMedia, 0, len(randomIDs))
	for i, randomID := range randomIDs {
		items = append(items, tg.InputSingleMedia{
			Media:    &tg.InputMediaGeoPoint{GeoPoint: &tg.InputGeoPoint{Lat: 10 + float64(i), Long: 20}},
			RandomID: randomID,
		})
	}
	return items
}

func lastStarsBalanceUpdate(t *testing.T, updates tg.UpdatesClass) (int64, bool) {
	t.Helper()
	upd, ok := updates.(*tg.Updates)
	if !ok {
		t.Fatalf("updates = %T, want *tg.Updates", updates)
	}
	balance, found := int64(0), false
	for _, u := range upd.Updates {
		if sb, ok := u.(*tg.UpdateStarsBalance); ok {
			amount, ok := sb.Balance.(*tg.StarsAmount)
			if !ok {
				t.Fatalf("stars balance = %T, want *tg.StarsAmount", sb.Balance)
			}
			balance, found = amount.Amount, true
		}
	}
	return balance, found
}

func TestSendMultiMediaPaidAlbumChecksBalanceBeforeFirstItem(t *testing.T) {
	r, account, messages, stars, alice, bob := newPaidBatchRouter(t)
	setPaidBatchPrice(t, r, account, bob.ID, 7)
	stars.balances[alice.ID] = 10
	_, err := r.onMessagesSendMultiMedia(WithUserID(context.Background(), alice.ID), &tg.MessagesSendMultiMediaRequest{
		Peer:           &tg.InputPeerUser{UserID: bob.ID, AccessHash: bob.AccessHash},
		MultiMedia:     paidBatchAlbum(94001, 94002),
		AllowPaidStars: 14,
	})
	if !tgerr.Is(err, "BALANCE_TOO_LOW") {
		t.Fatalf("album err = %v, want BALANCE_TOO_LOW", err)
	}
	if len(messages.paid) != 0 || stars.balances[alice.ID] != 10 {
		t.Fatalf("album sent %v and left balance %d, want nothing sent and 10", messages.paid, stars.balances[alice.ID])
	}
}

func TestSendMultiMediaPaidAlbumChargesPriceCheckedUpFront(t *testing.T) {
	r, account, messages, stars, alice, bob := newPaidBatchRouter(t)
	setPaidBatchPrice(t, r, account, bob.ID, 7)
	stars.balances[alice.ID] = 100
	messages.afterSend = func(sent int) {
		if sent == 1 {
			setPaidBatchPrice(t, r, account, bob.ID, 12)
		}
	}
	updates, err := r.onMessagesSendMultiMedia(WithUserID(context.Background(), alice.ID), &tg.MessagesSendMultiMediaRequest{
		Peer:           &tg.InputPeerUser{UserID: bob.ID, AccessHash: bob.AccessHash},
		MultiMedia:     paidBatchAlbum(94101, 94102),
		AllowPaidStars: 14,
	})
	if err != nil {
		t.Fatalf("paid album: %v", err)
	}
	if len(messages.paid) != 2 || messages.paid[0] != 7 || messages.paid[1] != 7 {
		t.Fatalf("album charges = %v, want [7 7] after a mid-album price raise", messages.paid)
	}
	if balance, ok := lastStarsBalanceUpdate(t, updates); !ok || balance != 86 {
		t.Fatalf("album stars balance update = %d present=%v, want 86", balance, ok)
	}
}

func TestSendMultiMediaScheduledPaidAlbumStoresPriceCheckedUpFront(t *testing.T) {
	r, account, messages, stars, alice, bob := newPaidBatchRouter(t)
	setPaidBatchPrice(t, r, account, bob.ID, 7)
	stars.balances[alice.ID] = 0
	messages.afterSend = func(scheduled int) {
		if scheduled == 1 {
			setPaidBatchPrice(t, r, account, bob.ID, 12)
		}
	}
	_, err := r.onMessagesSendMultiMedia(WithUserID(context.Background(), alice.ID), &tg.MessagesSendMultiMediaRequest{
		Peer:           &tg.InputPeerUser{UserID: bob.ID, AccessHash: bob.AccessHash},
		MultiMedia:     paidBatchAlbum(94151, 94152),
		AllowPaidStars: 14,
		ScheduleDate:   int(time.Now().Add(time.Hour).Unix()),
	})
	if err != nil {
		t.Fatalf("scheduled paid album: %v", err)
	}
	if len(messages.paid) != 0 {
		t.Fatalf("scheduled album sent %v, want nothing sent before delivery", messages.paid)
	}
	if len(messages.scheduledPaid) != 2 || messages.scheduledPaid[0] != 7 || messages.scheduledPaid[1] != 7 {
		t.Fatalf("scheduled album stored %v, want [7 7] without a balance check", messages.scheduledPaid)
	}
}

func sendPaidBatchSources(t *testing.T, messages *paidBatchMessages, alice, bob domain.User) []int {
	t.Helper()
	ids := make([]int, 0, 2)
	for i, body := range []string{"first source", "second source"} {
		res, err := messages.Service.SendPrivateText(context.Background(), alice.ID, domain.SendPrivateTextRequest{
			SenderUserID:    alice.ID,
			RecipientUserID: bob.ID,
			RandomID:        94200 + int64(i),
			Message:         body,
			Date:            1700002000 + i,
		})
		if err != nil {
			t.Fatalf("send source %d: %v", i, err)
		}
		ids = append(ids, res.SenderMessage.ID)
	}
	return ids
}

func TestForwardMessagesPaidRecipientChecksBalanceBeforeFirstMessage(t *testing.T) {
	r, account, messages, stars, alice, bob := newPaidBatchRouter(t)
	ids := sendPaidBatchSources(t, messages, alice, bob)
	setPaidBatchPrice(t, r, account, bob.ID, 7)
	stars.balances[alice.ID] = 10
	peer := &tg.InputPeerUser{UserID: bob.ID, AccessHash: bob.AccessHash}
	_, err := r.onMessagesForwardMessages(WithUserID(context.Background(), alice.ID), &tg.MessagesForwardMessagesRequest{
		FromPeer:       peer,
		ToPeer:         peer,
		ID:             ids,
		RandomID:       []int64{94301, 94302},
		AllowPaidStars: 14,
	})
	if !tgerr.Is(err, "BALANCE_TOO_LOW") {
		t.Fatalf("forward err = %v, want BALANCE_TOO_LOW", err)
	}
	if len(messages.paid) != 0 || stars.balances[alice.ID] != 10 {
		t.Fatalf("forward sent %v and left balance %d, want nothing sent and 10", messages.paid, stars.balances[alice.ID])
	}
}

func TestForwardMessagesPaidRecipientReturnsStarsBalance(t *testing.T) {
	r, account, messages, stars, alice, bob := newPaidBatchRouter(t)
	ids := sendPaidBatchSources(t, messages, alice, bob)
	setPaidBatchPrice(t, r, account, bob.ID, 7)
	stars.balances[alice.ID] = 100
	peer := &tg.InputPeerUser{UserID: bob.ID, AccessHash: bob.AccessHash}
	updates, err := r.onMessagesForwardMessages(WithUserID(context.Background(), alice.ID), &tg.MessagesForwardMessagesRequest{
		FromPeer:       peer,
		ToPeer:         peer,
		ID:             ids,
		RandomID:       []int64{94401, 94402},
		AllowPaidStars: 14,
	})
	if err != nil {
		t.Fatalf("paid forward: %v", err)
	}
	if len(messages.paid) != 2 || messages.paid[0] != 7 || messages.paid[1] != 7 {
		t.Fatalf("forward charges = %v, want [7 7]", messages.paid)
	}
	if balance, ok := lastStarsBalanceUpdate(t, updates); !ok || balance != 86 {
		t.Fatalf("forward stars balance update = %d present=%v, want 86", balance, ok)
	}
}

func TestMessageForwardErrMapsInsufficientStars(t *testing.T) {
	if err := messageForwardErr(domain.ErrStarsInsufficient); !tgerr.Is(err, "BALANCE_TOO_LOW") {
		t.Fatalf("messageForwardErr(ErrStarsInsufficient) = %v, want BALANCE_TOO_LOW", err)
	}
}
