package domain

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
)

// Stars local ledger domain model (no TL types, mirroring the boost.go style). This implementation is a local ledger,
// not a real payment: the balance is integer Stars (Nanos is always 0 online), and debits are atomic and never go negative.

// StarsBalance is an account's current available Stars balance.
type StarsBalance struct {
	UserID  int64
	Balance int64 // currently spendable Stars, always >= 0
	Granted bool  // whether the starting grant has been applied (idempotency guard for the lazy first-read grant)
}

// StarsPurchaseKind identifies the balance owner affected by a fiat Stars
// checkout. It is persisted with the form so a client cannot reinterpret a
// self top-up as a friend gift (or vice versa) when submitting the form.
type StarsPurchaseKind string

const (
	StarsPurchaseTopup    StarsPurchaseKind = "topup"
	StarsPurchaseGift     StarsPurchaseKind = "gift"
	StarsPurchaseGiveaway StarsPurchaseKind = "giveaway"
	// StarsPurchaseBotInvoice is a settled XTR invoice whose proceeds belong to
	// a bot's own revenue wallet rather than to the payer's personal balance.
	StarsPurchaseBotInvoice StarsPurchaseKind = "bot_invoice"
)

func (k StarsPurchaseKind) Valid() bool {
	return k == StarsPurchaseTopup || k == StarsPurchaseGift || k == StarsPurchaseGiveaway ||
		k == StarsPurchaseBotInvoice
}

// BotStarsCredit is one settled XTR invoice that credits a bot's own revenue
// wallet. InvoiceKey makes the credit idempotent across retried
// payments.sendPaymentForm calls: a replay returns the stored balance instead
// of minting a second credit. Amount must be positive; refunds carry their own
// key and a negative Amount.
type BotStarsCredit struct {
	BotUserID   int64
	PayerUserID int64
	Amount      int64
	Reason      StarsTransactionReason
	InvoiceKey  string
	Date        int
}

// Valid reports whether the credit describes a well-formed wallet mutation.
func (c BotStarsCredit) Valid() bool {
	switch c.Reason {
	case StarsReasonBotInvoice, StarsReasonBotRefund:
	default:
		return false
	}
	return c.BotUserID > 0 && c.PayerUserID > 0 && c.Date > 0 &&
		c.Amount != 0 && c.InvoiceKey != "" && len(c.InvoiceKey) <= 128
}

// BotStarsPayment is the durable receipt of one settled invoice credit. It
// records the post-transaction balance so a duplicate settlement can be
// answered without re-reading the mutable balance row.
type BotStarsPayment struct {
	InvoiceKey   string
	BotUserID    int64
	PayerUserID  int64
	Amount       int64
	BalanceAfter int64
	SettledAt    int
}

// BotStarsRevenuePoint is one UTC day of bot wallet activity. DayStart is the
// Unix second at 00:00 UTC, which the RPC layer multiplies by 1000 because the
// official chart renderer expects millisecond x values.
//
// Amount is the day's inbound sum and Spent the day's outbound sum. Spent is
// deliberately not called "withdrawn": Telegram uses "withdrawal" for a
// Fragment cash-out, which telesrv does not support for a bot wallet at all -
// withdrawal_enabled stays false - so that word would promise a payout that
// does not exist.
type BotStarsRevenuePoint struct {
	DayStart int
	Amount   int64
	Spent    int64
}

// Valid reports whether the point can be rendered on the revenue chart.
func (p BotStarsRevenuePoint) Valid() bool {
	return p.DayStart > 0 && p.Amount >= 0 && p.Spent >= 0
}

// StarsGiveawayPurchase is the complete immutable purpose behind one direct
// fiat Stars giveaway checkout. The launch purchase persists this shape; the
// eventual winner draw is a separate lifecycle transition.
type StarsGiveawayPurchase struct {
	BoostPeer          Peer     `json:"boost_peer"`
	AdditionalPeers    []Peer   `json:"additional_peers,omitempty"`
	CountriesISO2      []string `json:"countries_iso2,omitempty"`
	PrizeDescription   string   `json:"prize_description,omitempty"`
	RandomID           int64    `json:"random_id"`
	UntilDate          int      `json:"until_date"`
	Users              int      `json:"users"`
	PerUserStars       int64    `json:"per_user_stars"`
	YearlyBoosts       int      `json:"yearly_boosts"`
	OnlyNewSubscribers bool     `json:"only_new_subscribers,omitempty"`
	WinnersAreVisible  bool     `json:"winners_are_visible,omitempty"`
}

// StarsPurchaseForm binds one short-lived fiat Stars checkout to its
// authenticated buyer, purpose and exact server-advertised package. Recipient
// is zero for a self top-up and mandatory for a friend gift.
type StarsPurchaseForm struct {
	FormID           int64
	Kind             StarsPurchaseKind
	BuyerUserID      int64
	RecipientUserID  int64
	SpendPurposePeer Peer
	Giveaway         *StarsGiveawayPurchase
	Stars            int64
	Currency         string
	Amount           int64
	IssuedAt         int
	ExpiresAt        int
}

// StarsPurchaseRequest is the immutable settlement command carried by
// inputInvoiceStars. Android and TDesktop sendPaymentForm both resolve to this
// command after the ordinary fiat checkout has produced provider credentials.
type StarsPurchaseRequest struct {
	StarsPurchaseForm
	Date            int
	OriginAuthKeyID [8]byte
	OriginSessionID int64
}

// StarsPurchaseResult is the atomically committed credit and, for a friend
// gift, bilateral service-message receipt. Duplicate means an exact form replay.
type StarsPurchaseResult struct {
	Balance       StarsBalance
	Send          SendPrivateTextResult
	ChannelSend   SendChannelMessageResult
	TransactionID string
	Duplicate     bool
}

// StarsGiveawayInfo is the viewer-specific state of one durable launch card.
// Winner selection/results are intentionally outside the purchase aggregate.
type StarsGiveawayInfo struct {
	StartDate             int
	Participating         bool
	PreparingResults      bool
	JoinedTooEarlyDate    int
	AdminDisallowedChatID int64
	DisallowedCountry     string
}

// StarsTransactionReason marks the semantics of a ledger entry (projected to tg.StarsTransaction's flags/title).
type StarsTransactionReason string

const StarsReasonTransfer StarsTransactionReason = "transfer"

const (
	StarsReasonGrant           StarsTransactionReason = "grant"        // automatic starting-balance grant
	StarsReasonTopup           StarsTransactionReason = "topup"        // top-up (minted locally)
	StarsReasonReaction        StarsTransactionReason = "reaction"     // paid reaction spend
	StarsReasonGift            StarsTransactionReason = "gift"         // star gift spend/receive
	StarsReasonGiftUpgrade     StarsTransactionReason = "gift_upgrade" // upgrade a regular gift into a unique gift
	StarsReasonGiftTransfer    StarsTransactionReason = "gift_transfer"
	StarsReasonGiftResale      StarsTransactionReason = "gift_resale"
	StarsReasonGiftOffer       StarsTransactionReason = "gift_offer"
	StarsReasonGiftAuction     StarsTransactionReason = "gift_auction"
	StarsReasonGiftPrepaid     StarsTransactionReason = "gift_prepaid_upgrade"
	StarsReasonGiftDrop        StarsTransactionReason = "gift_drop_original_details"
	StarsReasonPaidMedia       StarsTransactionReason = "paid_media"   // paid media unlock
	StarsReasonPaidMessage     StarsTransactionReason = "paid_message" // channel Direct Message spend
	StarsReasonSuggestedPost   StarsTransactionReason = "suggested_post"
	StarsReasonPremium         StarsTransactionReason = "premium"
	StarsReasonWithdrawal      StarsTransactionReason = "withdrawal"
	StarsReasonAdjust          StarsTransactionReason = "adjust"           // fallback/manual adjustment
	StarsReasonDonationDeposit StarsTransactionReason = "donation_deposit" // crypto deposit credited after on-chain confirmation
	StarsReasonBotInvoice      StarsTransactionReason = "bot_invoice"      // settle an XTR invoice, crediting the bot wallet
	StarsReasonBotSpend        StarsTransactionReason = "bot_spend"        // bot wallet spend
	StarsReasonBotRefund       StarsTransactionReason = "bot_refund"       // invoice refund, debiting the bot wallet
)

// StarsTransaction is one ledger entry. amount is signed: credit > 0 (including refund/receive), debit < 0.
type StarsTransaction struct {
	ID              int64 // monotonic ledger id (keyset cursor)
	UserID          int64 // ledger owner
	Peer            Peer  // counterparty (a zero Peer when there is none, e.g. grant/topup)
	Amount          int64 // signed amount
	Date            int   // Unix seconds
	Reason          StarsTransactionReason
	Title           string // optional, projected to tg.StarsTransaction.Title
	Description     string // optional, projected to tg.StarsTransaction.Description
	PaymentID       int64
	RecipientUserID int64
	PremiumMonths   int
}

// IsCredit reports whether the entry is a credit, projected to tg.StarsTransaction.Refund.
func (t StarsTransaction) IsCredit() bool { return t.Amount > 0 }

// StarsTransactionDirection scopes one payments.getStarsTransactions view.
// The zero value intentionally means the combined inbound/outbound history.
type StarsTransactionDirection uint8

const (
	StarsTransactionDirectionAll StarsTransactionDirection = iota
	StarsTransactionDirectionIncoming
	StarsTransactionDirectionOutgoing
)

func (d StarsTransactionDirection) Valid() bool {
	return d <= StarsTransactionDirectionOutgoing
}

func (d StarsTransactionDirection) IncludesAmount(amount int64) bool {
	switch d {
	case StarsTransactionDirectionAll:
		return true
	case StarsTransactionDirectionIncoming:
		return amount > 0
	case StarsTransactionDirectionOutgoing:
		return amount < 0
	default:
		return false
	}
}

// StarsTransactionQuery keeps direction, ordering and the opaque keyset cursor
// together so filtering is applied before LIMIT in every ledger backend.
type StarsTransactionQuery struct {
	Offset    string
	Limit     int
	Direction StarsTransactionDirection
	Ascending bool
}

// NormalizeStarsTransactionQuery preserves the existing bounded limit/offset
// behavior while rejecting impossible internal direction values.
func NormalizeStarsTransactionQuery(query StarsTransactionQuery) (StarsTransactionQuery, error) {
	if !query.Direction.Valid() {
		return StarsTransactionQuery{}, ErrStarsTransactionQueryInvalid
	}
	if len(query.Offset) > MaxStarsTransactionsOffsetBytes {
		query.Offset = ""
	}
	if query.Limit <= 0 || query.Limit > MaxStarsTransactionsLimit {
		query.Limit = MaxStarsTransactionsLimit
	}
	return query, nil
}

// StarsTransactionPage is one page of ledger entries + current balance + pagination cursor + the counterparty user enrichment set.
type StarsTransactionPage struct {
	Balance      int64
	Transactions []StarsTransaction
	NextOffset   string // empty means no more pages (DrKLO stops paging on it; do not give a non-empty value on the last page)
	Users        []User // counterparty users mentioned in History, for tg Users enrichment
}

// TonUSDRate is the fixed local-ledger USD reference rate, not a live market quote.
const TonUSDRate = 1.3448279670684584

// TonTransaction is an entry in telesrv's internal nanoton ledger. It models
// the Telegram TON-denominated gift UI without contacting a wallet, Fragment,
// a TON node, or any blockchain service.
type TonTransaction struct {
	Comment     string
	ID          int64
	UserID      int64
	Peer        Peer
	GiftID      int64
	Amount      int64 // signed nanoton amount
	Date        int
	Reason      StarsTransactionReason
	Title       string
	Description string
}

type TonTransactionPage struct {
	Balance      int64
	Transactions []TonTransaction
	NextOffset   string
	Users        []User
}

// Stars ledger boundary constants.
const (
	// DefaultStarsStartingGrant is the starting Stars balance of the lazy first-read grant (for local testing).
	DefaultStarsStartingGrant = 1000
	// MaxStarsTransactionsLimit is the per-page limit of getStarsTransactions.
	MaxStarsTransactionsLimit = 100
	// MaxStarsTransactionsOffsetBytes is the keyset cursor string length limit.
	MaxStarsTransactionsOffsetBytes = 64
)

// Stars ledger sentinel errors (the rpc layer matches via errors.Is and maps to tgerr, mirroring ErrPremiumRequired).
var (
	// ErrStarsInsufficient means the balance is insufficient to complete the debit (maps to BALANCE_TOO_LOW).
	ErrStarsInsufficient = errors.New("stars: insufficient balance")
	// ErrStarsInvalidAmount means the amount is invalid (<=0).
	ErrStarsInvalidAmount = errors.New("stars: invalid amount")
	// ErrStarsTransactionQueryInvalid means an impossible transaction direction was constructed internally.
	ErrStarsTransactionQueryInvalid = errors.New("stars: invalid transaction query")
	// ErrStarsPurchaseFormInvalid covers a missing/cross-account/mutated form.
	ErrStarsPurchaseFormInvalid = errors.New("stars: purchase form invalid")
	// ErrStarsPurchaseFormExpired is returned before any settlement write.
	ErrStarsPurchaseFormExpired = errors.New("stars: purchase form expired")
	// ErrStarsGiftUnavailable covers a recipient that cannot receive the gift.
	ErrStarsGiftUnavailable = errors.New("stars: gift unavailable")
	// ErrStarsAirdropUnsupported means the underlying store does not support bulk credit (e.g. the in-memory backend).
	ErrStarsAirdropUnsupported = errors.New("stars: airdrop unsupported")
)

// StarsPaymentRequiredError reports the minimum paid-message authorization the
// sender must include in allow_paid_stars. The authorization is a ceiling; the
// ledger debits only the channel's current configured price.
type StarsPaymentRequiredError struct {
	Stars int64
}

func (e *StarsPaymentRequiredError) Error() string {
	return fmt.Sprintf("stars: allow payment required: %d", e.Stars)
}

// EncodeStarsCursor encodes the keyset cursor (the last entry id) into a client-opaque string.
func EncodeStarsCursor(id int64) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(id, 10)))
}

// DecodeStarsCursor reverses EncodeStarsCursor; it returns ok=false when it cannot parse (including an empty string),
// and the caller should start from the first page (the client only echoes back cursors we gave it; malformed is only a fallback).
func DecodeStarsCursor(s string) (int64, bool) {
	if s == "" {
		return 0, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return 0, false
	}
	id, err := strconv.ParseInt(string(raw), 10, 64)
	if err != nil || id <= 0 {
		return 0, false
	}
	return id, true
}

var ErrWalletTransferStartingGrant = errors.New("wallet transfers require a zero starting grant")
var ErrWalletTransferRecipientBlocked = errors.New("wallet transfer recipient blocked sender")

// WalletGramTransferRequest describes one gram transfer between two accounts.
//
// The request is the whole movement: who sends, who receives, how much, and
// what the note says. The address is the recipient's wallet address, which
// the caller knows from the wallet and the ledger does not.
type WalletGramTransferRequest struct {
	// AuthorizationHash identifies the one-use SRP grant; no secret is persisted.
	AuthorizationHash [32]byte
	// FromUserID is the account the money leaves.
	FromUserID int64
	// ToUserID is the account the money arrives at.
	ToUserID int64
	// AmountNanoton is the amount, in nanoton.
	AmountNanoton int64
	// Reason is the ledger reason of the movement.
	Reason StarsTransactionReason
	// PeerAddress is the recipient's wallet address, shown in the service
	// message. It is empty when the caller does not know it.
	PeerAddress string
	// Comment is the note the sender wrote on the transfer.
	Comment string
	// Date is when the movement happened.
	Date int
	// Title describes the movement in the ledger.
	Title string
	// CommandKey makes the movement idempotent. An empty key means no
	// idempotency.
	CommandKey string
	// OriginAuthKeyID and OriginSessionID carry the origin of the request, so
	// a server-generated service message is delivered like the client's own.
	OriginAuthKeyID [8]byte
	OriginSessionID int64
	// OriginUserID is the authenticated initiator when the message is
	// authored by another user, as an operator transfer is.
	OriginUserID int64
}

// WalletGramTransferResult is the result of one gram transfer.
type WalletGramTransferResult struct {
	Date      int
	Duplicate bool
	// Balance is the receiver's balance after the transfer.
	Balance int64
	// TransactionID is the sender's ledger row. The service message and the
	// history entry are the same movement, and the client correlates them by
	// this id.
	TransactionID int64
	// Send is the service message that told the chat about the transfer.
	Send SendPrivateTextResult
}
