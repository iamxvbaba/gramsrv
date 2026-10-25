package rpc

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"go.uber.org/zap/zaptest"

	botsapp "telesrv/internal/app/bots"
	appusers "telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

// botWalletGifts implements the optional bot-wallet projection on top of an
// embedded (possibly nil) GiftsService, mirroring how the production stargifts
// service is wired behind store.StarGiftLifecycleStore.
type botWalletGifts struct {
	GiftsService
	balances map[int64]int64
	revenue  map[int64]int64
	series   map[int64][]domain.BotStarsRevenuePoint
	page     domain.StarsTransactionPage
}

func (s *botWalletGifts) BotStarsBalance(_ context.Context, botUserID int64) (int64, error) {
	return s.balances[botUserID], nil
}

func (s *botWalletGifts) BotStarsOverallRevenue(_ context.Context, botUserID int64) (int64, error) {
	return s.revenue[botUserID], nil
}

func (s *botWalletGifts) BotStarsRevenueSeries(_ context.Context, botUserID int64, _, _ int) ([]domain.BotStarsRevenuePoint, error) {
	return s.series[botUserID], nil
}

func (s *botWalletGifts) BotStarsTransactions(_ context.Context, _ int64, _ domain.StarsTransactionQuery) (domain.StarsTransactionPage, error) {
	if s.page.Transactions == nil {
		return domain.StarsTransactionPage{Balance: s.balances[0]}, nil
	}
	return s.page, nil
}

// botWalletFixture builds a router whose viewer owns one bot and can see its
// wallet through payments.getStarsRevenueStats.
func botWalletFixture(t *testing.T, balance, revenue int64) (*Router, int64, int64) {
	t.Helper()
	ctx := context.Background()
	users := memory.NewUserStore()
	botStore := memory.NewBotStore(users)
	dialogs := memory.NewDialogStore()
	botStore.AttachDeliveryDependencies(dialogs, memory.NewDeliveryOutboxStore())
	messages := memory.NewMessageStore(dialogs)
	bots := botsapp.NewService(users, botStore, messages)

	owner, err := users.Create(ctx, domain.User{AccessHash: 7711, Phone: "15550007711", FirstName: "Bot Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	bot, _, err := bots.CreateBotWithDelivery(ctx, owner.ID, "Wallet Bot", "wallet_bot", rpcTestBotLifecycleEffects)
	if err != nil {
		t.Fatalf("create bot: %v", err)
	}
	gifts := &botWalletGifts{
		balances: map[int64]int64{bot.ID: balance},
		revenue:  map[int64]int64{bot.ID: revenue},
		series:   map[int64][]domain.BotStarsRevenuePoint{},
	}
	r := New(Config{}, Deps{
		Users: appusers.NewService(users),
		Bots:  bots,
		Gifts: gifts,
	}, zaptest.NewLogger(t), clock.System)
	return r, owner.ID, bot.ID
}

func dispatchBotRevenueStats(t *testing.T, r *Router, viewerID, botUserID int64) *tg.PaymentsStarsRevenueStats {
	t.Helper()
	ctx := WithUserID(context.Background(), viewerID)
	peer := &tg.InputPeerUser{UserID: botUserID, AccessHash: 1}
	req := &tg.PaymentsGetStarsRevenueStatsRequest{Peer: peer}
	var buf bin.Buffer
	if err := req.Encode(&buf); err != nil {
		t.Fatalf("encode request: %v", err)
	}
	got, err := r.Dispatch(ctx, [8]byte{}, 0, &buf)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	stats, ok := got.(*tg.PaymentsStarsRevenueStats)
	if !ok {
		t.Fatalf("response type = %T, want *tg.PaymentsStarsRevenueStats", got)
	}
	return stats
}

func TestPaymentsGetStarsRevenueStatsReportsBotWalletForOwner(t *testing.T) {
	r, ownerID, botID := botWalletFixture(t, 750, 900)

	stats := dispatchBotRevenueStats(t, r, ownerID, botID)

	current, ok := stats.Status.CurrentBalance.(*tg.StarsAmount)
	if !ok {
		t.Fatalf("current balance = %T, want *tg.StarsAmount", stats.Status.CurrentBalance)
	}
	if current.Amount != 750 {
		t.Fatalf("current balance = %d, want 750", current.Amount)
	}
	available, ok := stats.Status.AvailableBalance.(*tg.StarsAmount)
	if !ok {
		t.Fatalf("available balance = %T, want *tg.StarsAmount", stats.Status.AvailableBalance)
	}
	if available.Amount != 750 {
		t.Fatalf("available balance = %d, want 750", available.Amount)
	}
	overall, ok := stats.Status.OverallRevenue.(*tg.StarsAmount)
	if !ok {
		t.Fatalf("overall revenue = %T, want *tg.StarsAmount", stats.Status.OverallRevenue)
	}
	if overall.Amount != 900 {
		t.Fatalf("overall revenue = %d, want 900", overall.Amount)
	}
	// Bot wallet funds are spent through the bot's own invoices, never
	// withdrawn to a personal ledger.
	if stats.Status.WithdrawalEnabled {
		t.Fatal("withdrawal enabled for a bot wallet, want disabled")
	}
}

// A zero wallet must read as zero, not as an error and not as a stub that hides
// the difference between "no revenue" and "not installed".
func TestPaymentsGetStarsRevenueStatsReportsZeroBotWallet(t *testing.T) {
	r, ownerID, botID := botWalletFixture(t, 0, 0)

	stats := dispatchBotRevenueStats(t, r, ownerID, botID)

	current, ok := stats.Status.CurrentBalance.(*tg.StarsAmount)
	if !ok {
		t.Fatalf("current balance = %T, want *tg.StarsAmount", stats.Status.CurrentBalance)
	}
	if current.Amount != 0 {
		t.Fatalf("current balance = %d, want 0", current.Amount)
	}
}

// Reading a peer's wallet is operator business data: a non-owner must be
// refused rather than handed a zeroed stub they could mistake for "no revenue".
func TestPaymentsGetStarsRevenueStatsDeniesNonOwnerBotWallet(t *testing.T) {
	r, _, botID := botWalletFixture(t, 750, 900)

	// A session id that does not own the bot: ownership is resolved against the
	// bots service, so the viewer does not need to exist as a stored user.
	const strangerID int64 = 1000000009
	strangerCtx := WithUserID(context.Background(), strangerID)
	var buf bin.Buffer
	req := &tg.PaymentsGetStarsRevenueStatsRequest{Peer: &tg.InputPeerUser{UserID: botID, AccessHash: 1}}
	if err := req.Encode(&buf); err != nil {
		t.Fatalf("encode request: %v", err)
	}
	if _, err := r.Dispatch(strangerCtx, [8]byte{}, 0, &buf); err == nil {
		t.Fatal("dispatch by a non-owner returned no error, want USER_PERMISSION_DENIED")
	} else if !tgerr.Is(err, "USER_PERMISSION_DENIED") {
		t.Fatalf("non-owner err = %v, want USER_PERMISSION_DENIED", err)
	}
}

// A plain user has no revenue bucket separate from their spendable Stars
// balance, so the personal path must keep the bounded compatibility response
// instead of starting to invent revenue.
func TestPaymentsGetStarsRevenueStatsKeepsStubForPlainUser(t *testing.T) {
	ctx := context.Background()
	users := memory.NewUserStore()
	plain, err := users.Create(ctx, domain.User{AccessHash: 9911, Phone: "15550009911", FirstName: "Plain User"})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	r := New(Config{}, Deps{
		Users: appusers.NewService(users),
		Gifts: &botWalletGifts{balances: map[int64]int64{plain.ID: 4242}, revenue: map[int64]int64{plain.ID: 4242}},
	}, zaptest.NewLogger(t), clock.System)

	stats := dispatchBotRevenueStats(t, r, plain.ID, plain.ID)

	current, ok := stats.Status.CurrentBalance.(*tg.StarsAmount)
	if !ok {
		t.Fatalf("current balance = %T, want *tg.StarsAmount", stats.Status.CurrentBalance)
	}
	if current.Amount != 0 {
		t.Fatalf("plain user current balance = %d, want 0", current.Amount)
	}
}

// TDesktop opens the bot earn screen through payments.getStarsStatus: it shows
// the wallet balance in the bot profile and hides the whole balance section
// while that balance is zero. So this method has to serve the bot peer or the
// section stays hidden and the revenue chart is never requested at all.
func TestPaymentsGetStarsStatusServesBotWalletBalance(t *testing.T) {
	r, ownerID, botID := botWalletFixture(t, 750, 900)

	status := dispatchBotWalletStatus(t, r, ownerID, botID, false)

	balance, ok := status.Balance.(*tg.StarsAmount)
	if !ok {
		t.Fatalf("balance = %T, want *tg.StarsAmount", status.Balance)
	}
	if balance.Amount != 750 {
		t.Fatalf("balance = %d, want 750", balance.Amount)
	}
	// The client renders the balance next to the bot it belongs to.
	if len(status.Users) != 1 {
		t.Fatalf("users len = %d, want 1", len(status.Users))
	}
	projected, ok := status.Users[0].(*tg.User)
	if !ok {
		t.Fatalf("users[0] = %T, want *tg.User", status.Users[0])
	}
	if projected.ID != botID {
		t.Fatalf("users[0] = %d, want the bot %d", projected.ID, botID)
	}
}

func TestPaymentsGetStarsStatusReturnsEmptyTONForBotWallet(t *testing.T) {
	r, ownerID, botID := botWalletFixture(t, 750, 900)

	status := dispatchBotWalletStatus(t, r, ownerID, botID, true)

	if _, ok := status.Balance.(*tg.StarsTonAmount); !ok {
		t.Fatalf("balance = %T, want *tg.StarsTonAmount", status.Balance)
	}
}

func TestPaymentsGetStarsStatusDeniesNonOwnerBotWallet(t *testing.T) {
	r, _, botID := botWalletFixture(t, 750, 900)
	const strangerID int64 = 1000000009

	ctx := WithUserID(context.Background(), strangerID)
	var buf bin.Buffer
	req := &tg.PaymentsGetStarsStatusRequest{Peer: &tg.InputPeerUser{UserID: botID, AccessHash: 1}}
	if err := req.Encode(&buf); err != nil {
		t.Fatalf("encode request: %v", err)
	}
	if _, err := r.Dispatch(ctx, [8]byte{}, 0, &buf); err == nil {
		t.Fatal("dispatch by a non-owner returned no error, want USER_PERMISSION_DENIED")
	} else if !tgerr.Is(err, "USER_PERMISSION_DENIED") {
		t.Fatalf("non-owner err = %v, want USER_PERMISSION_DENIED", err)
	}
}

func dispatchBotWalletStatus(t *testing.T, r *Router, viewerID, botUserID int64, ton bool) *tg.PaymentsStarsStatus {
	t.Helper()
	ctx := WithUserID(context.Background(), viewerID)
	req := &tg.PaymentsGetStarsStatusRequest{Peer: &tg.InputPeerUser{UserID: botUserID, AccessHash: 1}}
	req.SetTon(ton)
	var buf bin.Buffer
	if err := req.Encode(&buf); err != nil {
		t.Fatalf("encode request: %v", err)
	}
	got, err := r.Dispatch(ctx, [8]byte{}, 0, &buf)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	status, ok := got.(*tg.PaymentsStarsStatus)
	if !ok {
		t.Fatalf("response type = %T, want *tg.PaymentsStarsStatus", got)
	}
	return status
}

// A bot that was never paid has no series to draw, and the official renderer
// logs "Empty columns list" for a column array without an x column, so the
// response must stay a statsGraphError rather than an empty chart.
func TestPaymentsGetStarsRevenueStatsReturnsGraphErrorWithoutSeries(t *testing.T) {
	r, ownerID, botID := botWalletFixture(t, 0, 0)

	stats := dispatchBotRevenueStats(t, r, ownerID, botID)

	if _, ok := stats.RevenueGraph.(*tg.StatsGraphError); !ok {
		t.Fatalf("revenue graph = %T, want *tg.StatsGraphError", stats.RevenueGraph)
	}
}

// Layer 228 has no line/bar StatsGraph constructor: statsGraph with an inline
// DataJSON payload is the only data-carrying form, and the client parses
// {"columns":[["x",<ms>,...],["revenue",<v>,...]],"yTickFormatter":"XTR"}
// straight out of the response.
func TestPaymentsGetStarsRevenueStatsRendersInlineJSONChart(t *testing.T) {
	r, ownerID, botID := botWalletFixture(t, 750, 900)
	// The fixture bot is created now, so its creation day is today and the
	// window is a single day.
	today := (int(time.Now().Unix()) / 86400) * 86400
	r.deps.Gifts.(*botWalletGifts).series[botID] = []domain.BotStarsRevenuePoint{
		{DayStart: today, Amount: 500, Spent: 100},
	}

	stats := dispatchBotRevenueStats(t, r, ownerID, botID)

	graph, ok := stats.RevenueGraph.(*tg.StatsGraph)
	if !ok {
		t.Fatalf("revenue graph = %T, want *tg.StatsGraph", stats.RevenueGraph)
	}
	var payload struct {
		Columns [][]any `json:"columns"`
		YFormat string  `json:"yTickFormatter"`
		Colors  struct {
			Revenue string `json:"revenue"`
		} `json:"colors"`
		Names struct {
			Revenue string `json:"revenue"`
		} `json:"names"`
	}
	if err := json.Unmarshal([]byte(graph.JSON.Data), &payload); err != nil {
		t.Fatalf("unmarshal chart json: %v", err)
	}
	if payload.YFormat != "XTR" {
		t.Fatalf("yTickFormatter = %q, want XTR", payload.YFormat)
	}
	// TDesktop paints a line from the colorKey prefix through
	// FillLineColorsByKey, and falls back to a black bar without it. GOLDEN is
	// the theme key the client itself uses for Stars.
	if payload.Colors.Revenue != "GOLDEN#eba52d" {
		t.Fatalf("revenue color = %q, want GOLDEN#eba52d", payload.Colors.Revenue)
	}
	// The point-details popup renders line.name and the client does not localize
	// a server-supplied string, so an empty name shows a blank label.
	if payload.Names.Revenue != "Revenue" {
		t.Fatalf("revenue name = %q, want Revenue", payload.Names.Revenue)
	}
	// Exactly one value column. A second line is what makes the client draw its
	// legend buttons, which read as a glitch under the chart, so a wallet with
	// a debit on its only day must still ship a single line.
	if len(payload.Columns) != 2 {
		t.Fatalf("columns len = %d, want 2 (x + revenue)", len(payload.Columns))
	}
	if id, ok := payload.Columns[0][0].(string); !ok || id != "x" {
		t.Fatalf("first column id = %v, want x", payload.Columns[0][0])
	}
	// Equal-length columns are mandatory; the stack renderer truncates otherwise.
	for i, column := range payload.Columns {
		if len(column) != len(payload.Columns[0]) {
			t.Fatalf("column %d len = %d, want %d", i, len(column), len(payload.Columns[0]))
		}
	}
	// A bot created today is padded to the three points the stack renderer
	// requires: it reads xPercentage[1] unguarded, so a one- or two-point
	// window is an out-of-bounds read or a zero-width bar.
	if len(payload.Columns[0]) != 4 {
		t.Fatalf("x column len = %d, want 4 (id + 3 days)", len(payload.Columns[0]))
	}
	if got := int64(payload.Columns[0][3].(float64)); got != int64(today)*1000 {
		t.Fatalf("last x = %d ms, want today %d", got, int64(today)*1000)
	}
	if got := int64(payload.Columns[1][3].(float64)); got != 500 {
		t.Fatalf("revenue on the last day = %d, want 500", got)
	}
}

// A bot that is older than the cap is still plotted against a bounded window,
// and a bot created in the future must not produce a window that ends before it
// starts.
func TestBotStarsRevenueGraphAnchorsWindowOnBotCreation(t *testing.T) {
	const bucketSeconds = 86400
	now := int(time.Now().Unix())
	today := now / bucketSeconds * bucketSeconds
	r := New(Config{}, Deps{}, zaptest.NewLogger(t), clock.System)

	for _, tc := range []struct {
		name      string
		createdAt time.Time
		wantDays  int
	}{
		{"created three days ago", time.Unix(int64(today-3*bucketSeconds), 0).UTC(), 4},
		// A young bot is padded to the three points the stack renderer needs,
		// so the window reaches back past the creation day instead of shipping
		// a one- or two-point window the client cannot draw.
		{"created today", time.Unix(int64(today), 0).UTC(), 3},
		{"created yesterday", time.Unix(int64(today-bucketSeconds), 0).UTC(), 3},
		{"older than the cap", time.Unix(int64(today-2*365*bucketSeconds), 0).UTC(), 366},
		{"created in the future", time.Unix(int64(today+10*bucketSeconds), 0).UTC(), 3},
		{"unknown creation date", time.Time{}, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ledger := &botWalletGifts{series: map[int64][]domain.BotStarsRevenuePoint{
				7: {{DayStart: today, Amount: 10}},
			}}
			graph, err := r.botStarsRevenueGraph(context.Background(), ledger, 7, tc.createdAt, now)
			if err != nil {
				t.Fatalf("revenue graph: %v", err)
			}
			got, ok := graph.(*tg.StatsGraph)
			if !ok {
				t.Fatalf("graph = %T, want *tg.StatsGraph", graph)
			}
			var payload struct {
				Columns [][]any `json:"columns"`
			}
			if err := json.Unmarshal([]byte(got.JSON.Data), &payload); err != nil {
				t.Fatalf("unmarshal chart json: %v", err)
			}
			// One entry is the column id, the rest are days.
			if days := len(payload.Columns[0]) - 1; days != tc.wantDays {
				t.Fatalf("chart days = %d, want %d", days, tc.wantDays)
			}
		})
	}
}

// An earning-only wallet must also ship a single column, because the legend
// buttons the client draws for a second line are the artifact being removed.
func TestPaymentsGetStarsRevenueStatsShipsSingleColumnForEarningOnlyWallet(t *testing.T) {
	r, ownerID, botID := botWalletFixture(t, 300, 300)
	day := (int(time.Now().Unix()) / 86400) * 86400
	r.deps.Gifts.(*botWalletGifts).series[botID] = []domain.BotStarsRevenuePoint{
		{DayStart: day, Amount: 300},
	}

	stats := dispatchBotRevenueStats(t, r, ownerID, botID)

	graph, ok := stats.RevenueGraph.(*tg.StatsGraph)
	if !ok {
		t.Fatalf("revenue graph = %T, want *tg.StatsGraph", stats.RevenueGraph)
	}
	var payload struct {
		Columns [][]any `json:"columns"`
	}
	if err := json.Unmarshal([]byte(graph.JSON.Data), &payload); err != nil {
		t.Fatalf("unmarshal chart json: %v", err)
	}
	if len(payload.Columns) != 2 {
		t.Fatalf("columns len = %d, want 2 (x + revenue)", len(payload.Columns))
	}
}

// payments.getStarsTransactions accepts any InputPeer, and the official Stars
// screen pages a bot's history through it, so the bot wallet has to answer in
// the same starsStatus envelope the channel path uses.
func TestPaymentsGetStarsTransactionsServesBotWalletHistory(t *testing.T) {
	r, ownerID, botID := botWalletFixture(t, 750, 900)
	gifts := r.deps.Gifts.(*botWalletGifts)
	gifts.page = domain.StarsTransactionPage{
		Balance: 750,
		Transactions: []domain.StarsTransaction{{
			ID: 7, UserID: botID, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: 4242},
			Amount: 500, Date: 1700006400, Reason: domain.StarsReasonBotInvoice,
		}},
		NextOffset: domain.EncodeStarsCursor(7),
	}

	status := dispatchBotWalletTransactions(t, r, ownerID, botID, false, false)

	balance, ok := status.Balance.(*tg.StarsAmount)
	if !ok {
		t.Fatalf("balance = %T, want *tg.StarsAmount", status.Balance)
	}
	if balance.Amount != 750 {
		t.Fatalf("balance = %d, want 750", balance.Amount)
	}
	if len(status.History) != 1 {
		t.Fatalf("history len = %d, want 1", len(status.History))
	}
	amount, ok := status.History[0].Amount.(*tg.StarsAmount)
	if !ok {
		t.Fatalf("history amount = %T, want *tg.StarsAmount", status.History[0].Amount)
	}
	if amount.Amount != 500 {
		t.Fatalf("history amount = %d, want 500", amount.Amount)
	}
	// The client stops paging when next_offset is absent, and this page is not
	// the last one, so it must be present.
	if status.NextOffset == "" {
		t.Fatal("next_offset missing on a non-terminal page")
	}
}

// The bot wallet is XTR-only: returning the XTR history for a ton=true request
// would credit the wrong ledger.
func TestPaymentsGetStarsTransactionsReturnsEmptyTONForBotWallet(t *testing.T) {
	r, ownerID, botID := botWalletFixture(t, 750, 900)
	r.deps.Gifts.(*botWalletGifts).page = domain.StarsTransactionPage{
		Balance:      750,
		Transactions: []domain.StarsTransaction{{ID: 7, Amount: 500, Reason: domain.StarsReasonBotInvoice}},
	}

	status := dispatchBotWalletTransactions(t, r, ownerID, botID, true, false)

	if _, ok := status.Balance.(*tg.StarsTonAmount); !ok {
		t.Fatalf("balance = %T, want *tg.StarsTonAmount", status.Balance)
	}
	if len(status.History) != 0 {
		t.Fatalf("history len = %d, want 0 for a ton request", len(status.History))
	}
}

// Reading somebody else's wallet history is the same leak as reading the
// balance, so it must fail the same way.
func TestPaymentsGetStarsTransactionsDeniesNonOwnerBotWallet(t *testing.T) {
	r, _, botID := botWalletFixture(t, 750, 900)
	const strangerID int64 = 1000000009

	ctx := WithUserID(context.Background(), strangerID)
	var buf bin.Buffer
	req := &tg.PaymentsGetStarsTransactionsRequest{Peer: &tg.InputPeerUser{UserID: botID, AccessHash: 1}, Limit: 10}
	if err := req.Encode(&buf); err != nil {
		t.Fatalf("encode request: %v", err)
	}
	if _, err := r.Dispatch(ctx, [8]byte{}, 0, &buf); err == nil {
		t.Fatal("dispatch by a non-owner returned no error, want USER_PERMISSION_DENIED")
	} else if !tgerr.Is(err, "USER_PERMISSION_DENIED") {
		t.Fatalf("non-owner err = %v, want USER_PERMISSION_DENIED", err)
	}
}

func dispatchBotWalletTransactions(t *testing.T, r *Router, viewerID, botUserID int64, ton, inbound bool) *tg.PaymentsStarsStatus {
	t.Helper()
	ctx := WithUserID(context.Background(), viewerID)
	req := &tg.PaymentsGetStarsTransactionsRequest{
		Peer:  &tg.InputPeerUser{UserID: botUserID, AccessHash: 1},
		Limit: 10,
	}
	req.SetTon(ton)
	req.SetInbound(inbound)
	var buf bin.Buffer
	if err := req.Encode(&buf); err != nil {
		t.Fatalf("encode request: %v", err)
	}
	got, err := r.Dispatch(ctx, [8]byte{}, 0, &buf)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	status, ok := got.(*tg.PaymentsStarsStatus)
	if !ok {
		t.Fatalf("response type = %T, want *tg.PaymentsStarsStatus", got)
	}
	return status
}
