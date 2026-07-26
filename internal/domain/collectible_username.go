package domain

import (
	"errors"
	"sort"
	"strings"
	"time"
)

// Collectible (Fragment-style) usernames.
//
// A peer holds exactly one editable username -- the slot the client owns through
// account.updateUsername / channels.updateUsername -- plus any number of
// collectible usernames up to MaxPeerCollectibleUsernames. Both kinds live in
// the same global registry, so occupancy checks and username resolution have a
// single source of truth.
//
// The editable slot keeps the service-level 5..32 length rule. Collectible names
// are minted by the operator and may be shorter: short names are precisely what
// a collectible market distributes. They still have to be syntactically
// resolvable by clients, so the character rules are identical.
const (
	MinCollectibleUsernameLength = 4
	MaxCollectibleUsernameLength = 32
	// MaxPeerCollectibleUsernames bounds the collectible rows a single peer can
	// hold. The TL usernames vector is rendered in full by clients, so the bound
	// keeps both the projection cost and the rendered list finite.
	MaxPeerCollectibleUsernames = 20
	// MaxUsernameSortOrder matches the registry CHECK and bounds reorder input.
	MaxUsernameSortOrder = 1024
	// MaxCollectibleUsernameURLLength matches the registry CHECK on url.
	MaxCollectibleUsernameURLLength = 512
	// MaxCollectibleUsernameReasonLength matches the provenance CHECK on reason.
	MaxCollectibleUsernameReasonLength = 512
	// MaxCollectibleUsernameActorLength matches the provenance CHECK on actor.
	MaxCollectibleUsernameActorLength = 128
	// MaxCollectibleUsernameCommandKeyLength matches the idempotency CHECK.
	MaxCollectibleUsernameCommandKeyLength = 128
)

var (
	// ErrCollectibleUsernameNotFound is returned when no asset backs the name.
	ErrCollectibleUsernameNotFound = errors.New("collectible username not found")
	// ErrCollectibleUsernameNotOwned rejects operations that need a live owner.
	ErrCollectibleUsernameNotOwned = errors.New("collectible username not owned")
	// ErrCollectibleUsernameBurned rejects any mutation of a burned asset.
	ErrCollectibleUsernameBurned = errors.New("collectible username burned")
	// ErrCollectibleUsernameLimit reports the per-peer collectible bound.
	ErrCollectibleUsernameLimit = errors.New("collectible username limit reached")
	// ErrCollectibleUsernameStateInvalid rejects an impossible asset shape.
	ErrCollectibleUsernameStateInvalid = errors.New("collectible username state invalid")
	// ErrUsernameNotCollectible rejects collectible-only operations on the
	// editable slot, which the client owns and the operator must not move.
	ErrUsernameNotCollectible = errors.New("username not collectible")
	// ErrUsernameNotEditable rejects editable-only operations on a collectible.
	ErrUsernameNotEditable = errors.New("username not editable")
	// ErrUsernameOrderInvalid rejects a reorder that is not a permutation of the
	// peer's current collectible usernames.
	ErrUsernameOrderInvalid = errors.New("username order invalid")
	// ErrCollectibleCurrencyInvalid rejects an unsupported purchase currency.
	ErrCollectibleCurrencyInvalid = errors.New("collectible currency invalid")
)

// Purchase currencies recorded on a collectible asset. XTR is Stars, TON is the
// local (non on-chain) TON ledger, USD is a bookkeeping-only fiat record for
// assets the operator imported rather than sold.
const (
	CollectibleCurrencyStars = "XTR"
	CollectibleCurrencyTON   = "TON"
	CollectibleCurrencyUSD   = "USD"
)

// CollectibleCryptoCurrencyTON is the only crypto currency the projection
// reports, matching the TON ledger the star gift lifecycle already uses.
const CollectibleCryptoCurrencyTON = "TON"

// CollectibleUsernameStatus is the asset lifecycle.
//
// vault  -- minted, held by the operator, attached to no peer.
// owned  -- attached to a peer and present in that peer's username registry.
// burned -- permanently retired; the name is released back to the pool.
type CollectibleUsernameStatus string

const (
	CollectibleUsernameStatusVault  CollectibleUsernameStatus = "vault"
	CollectibleUsernameStatusOwned  CollectibleUsernameStatus = "owned"
	CollectibleUsernameStatusBurned CollectibleUsernameStatus = "burned"
)

// Valid reports whether the status is one of the three modelled states.
func (s CollectibleUsernameStatus) Valid() bool {
	switch s {
	case CollectibleUsernameStatusVault, CollectibleUsernameStatusOwned, CollectibleUsernameStatusBurned:
		return true
	default:
		return false
	}
}

// CollectibleUsernameTransferKind is the provenance entry kind.
type CollectibleUsernameTransferKind string

const (
	// CollectibleUsernameKindMint records the asset entering the vault.
	CollectibleUsernameKindMint CollectibleUsernameTransferKind = "mint"
	// CollectibleUsernameKindTransfer records an ownership change, including the
	// first assignment out of the vault.
	CollectibleUsernameKindTransfer CollectibleUsernameTransferKind = "transfer"
	// CollectibleUsernameKindRevoke records an owner losing the asset back to the
	// vault without the name being released.
	CollectibleUsernameKindRevoke CollectibleUsernameTransferKind = "revoke"
	// CollectibleUsernameKindBurn records permanent retirement.
	CollectibleUsernameKindBurn CollectibleUsernameTransferKind = "burn"
)

// Valid reports whether the kind is modelled.
func (k CollectibleUsernameTransferKind) Valid() bool {
	switch k {
	case CollectibleUsernameKindMint, CollectibleUsernameKindTransfer, CollectibleUsernameKindRevoke, CollectibleUsernameKindBurn:
		return true
	default:
		return false
	}
}

// Username is one registry row of a peer's username list. It projects directly
// onto username#b4073647 (editable/active flags plus the display form).
type Username struct {
	Username string
	Active   bool
	Editable bool
	// SortOrder orders collectible rows. The editable row always sorts first
	// regardless of its stored value, matching client expectations that the
	// editable username heads the list.
	SortOrder int
	// CollectibleID is zero for the editable slot and non-zero for an asset.
	CollectibleID int64
}

// Collectible reports whether this row is backed by a collectible asset.
func (u Username) Collectible() bool { return u.CollectibleID != 0 }

// CollectibleUsername is the asset behind a collectible username: who holds it,
// what was paid, and where the purchase can be verified.
type CollectibleUsername struct {
	ID       int64
	Username string
	Status   CollectibleUsernameStatus
	// Owner is the zero Peer unless Status is owned.
	Owner Peer
	// PurchaseDate, Currency, Amount, CryptoCurrency, CryptoAmount and URL are
	// the fragment.collectibleInfo payload.
	PurchaseDate   time.Time
	Currency       string
	Amount         int64
	CryptoCurrency string
	CryptoAmount   int64
	URL            string
	// OriginalOwner is the first holder and survives transfers and burns.
	OriginalOwner Peer
	TransferCount int
	Version       int64
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// CollectibleInfo is the fragment.collectibleInfo projection. PurchaseDate is a
// unix timestamp because the TL field is an int date.
type CollectibleInfo struct {
	PurchaseDate   int
	Currency       string
	Amount         int64
	CryptoCurrency string
	CryptoAmount   int64
	URL            string
}

// Info returns the TL-shaped purchase record.
func (c CollectibleUsername) Info() CollectibleInfo {
	date := 0
	if !c.PurchaseDate.IsZero() {
		date = int(c.PurchaseDate.Unix())
	}
	return CollectibleInfo{
		PurchaseDate:   date,
		Currency:       c.Currency,
		Amount:         c.Amount,
		CryptoCurrency: c.CryptoCurrency,
		CryptoAmount:   c.CryptoAmount,
		URL:            c.URL,
	}
}

// Owned reports whether the asset is currently attached to a peer.
func (c CollectibleUsername) Owned() bool {
	return c.Status == CollectibleUsernameStatusOwned && c.Owner.Type != "" && c.Owner.ID > 0
}

// Validate enforces the invariants the registry CHECK constraints encode, so an
// in-memory store and PostgreSQL reject the same shapes.
func (c CollectibleUsername) Validate() error {
	if !ValidCollectibleUsername(c.Username) {
		return ErrUsernameInvalid
	}
	if !c.Status.Valid() {
		return ErrCollectibleUsernameStateInvalid
	}
	switch c.Status {
	case CollectibleUsernameStatusOwned:
		if !validCollectibleOwner(c.Owner) {
			return ErrCollectibleUsernameStateInvalid
		}
	default:
		if c.Owner.Type != "" || c.Owner.ID != 0 {
			return ErrCollectibleUsernameStateInvalid
		}
	}
	if err := ValidateCollectibleAmounts(c.Currency, c.Amount, c.CryptoCurrency, c.CryptoAmount); err != nil {
		return err
	}
	if len(c.URL) > MaxCollectibleUsernameURLLength {
		return ErrCollectibleUsernameStateInvalid
	}
	if c.TransferCount < 0 {
		return ErrCollectibleUsernameStateInvalid
	}
	if c.OriginalOwner.Type != "" && !validCollectibleOwner(c.OriginalOwner) {
		return ErrCollectibleUsernameStateInvalid
	}
	return nil
}

func validCollectibleOwner(peer Peer) bool {
	switch peer.Type {
	case PeerTypeUser, PeerTypeChannel:
		return peer.ID > 0
	default:
		return false
	}
}

// ValidateCollectibleAmounts enforces the currency/amount pairing shared by the
// registry CHECK constraints: a crypto currency requires a positive amount and
// an empty one forbids it.
func ValidateCollectibleAmounts(currency string, amount int64, cryptoCurrency string, cryptoAmount int64) error {
	switch currency {
	case CollectibleCurrencyStars, CollectibleCurrencyTON, CollectibleCurrencyUSD:
	default:
		return ErrCollectibleCurrencyInvalid
	}
	if amount < 0 {
		return ErrCollectibleCurrencyInvalid
	}
	switch cryptoCurrency {
	case "":
		if cryptoAmount != 0 {
			return ErrCollectibleCurrencyInvalid
		}
	case CollectibleCryptoCurrencyTON:
		if cryptoAmount <= 0 {
			return ErrCollectibleCurrencyInvalid
		}
	default:
		return ErrCollectibleCurrencyInvalid
	}
	return nil
}

// CollectibleUsernameTransfer is one provenance row.
type CollectibleUsernameTransfer struct {
	ID            int64
	CollectibleID int64
	Kind          CollectibleUsernameTransferKind
	From          Peer
	To            Peer
	Currency      string
	Amount        int64
	Actor         string
	Reason        string
	CommandKey    string
	CreatedAt     time.Time
}

// NormalizeUsername trims whitespace and a leading '@'. It is the shared entry
// normalisation for every username surface: RPC, admin API and store.
func NormalizeUsername(username string) string {
	username = strings.TrimSpace(username)
	username = strings.TrimPrefix(username, "@")
	return strings.TrimSpace(username)
}

// ValidCollectibleUsername reports whether the name is syntactically usable as a
// collectible username. Character rules match the editable slot exactly; only
// the minimum length differs.
func ValidCollectibleUsername(username string) bool {
	return validUsernameChars(username, MinCollectibleUsernameLength, MaxCollectibleUsernameLength)
}

func validUsernameChars(username string, minLen, maxLen int) bool {
	if len(username) < minLen || len(username) > maxLen {
		return false
	}
	for i := 0; i < len(username); i++ {
		c := username[i]
		switch {
		case c >= 'a' && c <= 'z':
		case c >= 'A' && c <= 'Z':
		case c >= '0' && c <= '9':
			if i == 0 {
				return false
			}
		case c == '_':
			if i == 0 {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// SortUsernames returns the projection order clients expect: the editable slot
// first, then collectibles by stored order, with the name as a stable tiebreak.
// The input is not mutated.
func SortUsernames(list []Username) []Username {
	out := append([]Username(nil), list...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Editable != out[j].Editable {
			return out[i].Editable
		}
		if out[i].SortOrder != out[j].SortOrder {
			return out[i].SortOrder < out[j].SortOrder
		}
		return strings.ToLower(out[i].Username) < strings.ToLower(out[j].Username)
	})
	return out
}

// ActiveUsername returns the name clients treat as the peer's primary username:
// the active editable slot when present, otherwise the first active collectible.
// It is what the legacy scalar users.username / channels.username column mirrors.
func ActiveUsername(list []Username) string {
	for _, item := range SortUsernames(list) {
		if item.Active && item.Username != "" {
			return item.Username
		}
	}
	return ""
}

// ValidateUsernameReorder checks that order is a permutation of the peer's
// current collectible usernames. Reordering never touches the editable slot,
// which always projects first.
func ValidateUsernameReorder(current []Username, order []string) error {
	if len(order) > MaxPeerCollectibleUsernames {
		return ErrUsernameOrderInvalid
	}
	collectibles := make(map[string]struct{}, len(current))
	for _, item := range current {
		if item.Collectible() {
			collectibles[strings.ToLower(item.Username)] = struct{}{}
		}
	}
	seen := make(map[string]struct{}, len(order))
	for _, name := range order {
		key := strings.ToLower(NormalizeUsername(name))
		if key == "" {
			return ErrUsernameOrderInvalid
		}
		if _, ok := collectibles[key]; !ok {
			return ErrUsernameOrderInvalid
		}
		if _, dup := seen[key]; dup {
			return ErrUsernameOrderInvalid
		}
		seen[key] = struct{}{}
	}
	if len(seen) != len(collectibles) {
		return ErrUsernameOrderInvalid
	}
	return nil
}

// ApplyUsernameReorder returns the list with collectible sort orders rewritten
// to follow order. The editable row is preserved untouched.
func ApplyUsernameReorder(current []Username, order []string) ([]Username, error) {
	if err := ValidateUsernameReorder(current, order); err != nil {
		return nil, err
	}
	position := make(map[string]int, len(order))
	for i, name := range order {
		position[strings.ToLower(NormalizeUsername(name))] = i
	}
	out := append([]Username(nil), current...)
	for i := range out {
		if !out[i].Collectible() {
			continue
		}
		out[i].SortOrder = position[strings.ToLower(out[i].Username)]
	}
	return SortUsernames(out), nil
}

// ValidateUsernameToggle rejects deactivating the editable slot through the
// collectible toggle path and enforces that at least one active username remains
// when the peer has any. Telegram clients rely on a peer that owns usernames
// keeping a resolvable one.
func ValidateUsernameToggle(current []Username, username string, active bool) error {
	key := strings.ToLower(NormalizeUsername(username))
	if key == "" {
		return ErrUsernameInvalid
	}
	var target *Username
	for i := range current {
		if strings.ToLower(current[i].Username) == key {
			target = &current[i]
			break
		}
	}
	if target == nil {
		return ErrUsernameNotOccupied
	}
	if !target.Collectible() {
		return ErrUsernameNotCollectible
	}
	if active {
		return nil
	}
	remaining := 0
	for _, item := range current {
		if strings.ToLower(item.Username) == key {
			continue
		}
		if item.Active {
			remaining++
		}
	}
	if remaining == 0 {
		return ErrUsernameOrderInvalid
	}
	return nil
}

// MintCollectibleUsernameRequest describes an operator minting a new asset,
// optionally assigning it to a holder in the same command.
type MintCollectibleUsernameRequest struct {
	Username string
	// Owner is optional: the zero value mints into the vault.
	Owner          Peer
	PurchaseDate   time.Time
	Currency       string
	Amount         int64
	CryptoCurrency string
	CryptoAmount   int64
	URL            string
	Actor          string
	Reason         string
	CommandKey     string
}

// Validate normalises nothing and only checks; callers normalise first.
func (r MintCollectibleUsernameRequest) Validate() error {
	if !ValidCollectibleUsername(r.Username) {
		return ErrUsernameInvalid
	}
	if r.Owner.Type != "" && !validCollectibleOwner(r.Owner) {
		return ErrCollectibleUsernameStateInvalid
	}
	if err := ValidateCollectibleAmounts(r.Currency, r.Amount, r.CryptoCurrency, r.CryptoAmount); err != nil {
		return err
	}
	if len(r.URL) > MaxCollectibleUsernameURLLength {
		return ErrCollectibleUsernameStateInvalid
	}
	if len(r.Reason) > MaxCollectibleUsernameReasonLength {
		return ErrCollectibleUsernameStateInvalid
	}
	if len(r.Actor) > MaxCollectibleUsernameActorLength {
		return ErrCollectibleUsernameStateInvalid
	}
	if len(r.CommandKey) > MaxCollectibleUsernameCommandKeyLength {
		return ErrCollectibleUsernameStateInvalid
	}
	return nil
}

// TransferCollectibleUsernameRequest moves an asset between peers, or out of the
// vault when the asset is unowned.
type TransferCollectibleUsernameRequest struct {
	Username   string
	To         Peer
	Actor      string
	Reason     string
	CommandKey string
}

// Validate checks the request shape.
func (r TransferCollectibleUsernameRequest) Validate() error {
	if !ValidCollectibleUsername(r.Username) {
		return ErrUsernameInvalid
	}
	if !validCollectibleOwner(r.To) {
		return ErrCollectibleUsernameStateInvalid
	}
	if len(r.Reason) > MaxCollectibleUsernameReasonLength {
		return ErrCollectibleUsernameStateInvalid
	}
	if len(r.Actor) > MaxCollectibleUsernameActorLength {
		return ErrCollectibleUsernameStateInvalid
	}
	if len(r.CommandKey) > MaxCollectibleUsernameCommandKeyLength {
		return ErrCollectibleUsernameStateInvalid
	}
	return nil
}

// RevokeCollectibleUsernameRequest returns an asset to the vault, or burns it.
type RevokeCollectibleUsernameRequest struct {
	Username string
	// Burn retires the asset permanently instead of returning it to the vault.
	Burn       bool
	Actor      string
	Reason     string
	CommandKey string
}

// Validate checks the request shape.
func (r RevokeCollectibleUsernameRequest) Validate() error {
	if !ValidCollectibleUsername(r.Username) {
		return ErrUsernameInvalid
	}
	if len(r.Reason) > MaxCollectibleUsernameReasonLength {
		return ErrCollectibleUsernameStateInvalid
	}
	if len(r.Actor) > MaxCollectibleUsernameActorLength {
		return ErrCollectibleUsernameStateInvalid
	}
	if len(r.CommandKey) > MaxCollectibleUsernameCommandKeyLength {
		return ErrCollectibleUsernameStateInvalid
	}
	return nil
}

// DeleteCollectibleUsernameRequest removes an asset outright, releasing its name
// and discarding its provenance. Revoke+Burn retires an asset but keeps the
// history; this is the escape hatch for a name that was issued by mistake.
type DeleteCollectibleUsernameRequest struct {
	Username   string
	Actor      string
	Reason     string
	CommandKey string
}

// Validate checks the request shape.
func (r DeleteCollectibleUsernameRequest) Validate() error {
	if !ValidCollectibleUsername(r.Username) {
		return ErrUsernameInvalid
	}
	if len(r.Reason) > MaxCollectibleUsernameReasonLength {
		return ErrCollectibleUsernameStateInvalid
	}
	if len(r.Actor) > MaxCollectibleUsernameActorLength {
		return ErrCollectibleUsernameStateInvalid
	}
	if len(r.CommandKey) > MaxCollectibleUsernameCommandKeyLength {
		return ErrCollectibleUsernameStateInvalid
	}
	return nil
}

// CollectibleUsernameFilter bounds an admin listing query.
type CollectibleUsernameFilter struct {
	Status   CollectibleUsernameStatus
	Owner    Peer
	Query    string
	BeforeID int64
	Limit    int
}
