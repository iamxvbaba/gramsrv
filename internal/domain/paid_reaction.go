package domain

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
)

// Channel post paid reaction (messages.sendPaidReaction): a user spends Stars to "like" a channel message,
// with the star count accumulated over (channel,message,user); the message shows the ReactionPaid total stars + a top reactors leaderboard.
// It debits the payer, credits the channel and accumulates in the same store transaction as the Stars ledger (stars.go).

const (
	// MaxPaidReactionStarsPerRequest is the star limit for a single sendPaidReaction (aligned with the official stars_paid_reaction_amount_max).
	MaxPaidReactionStarsPerRequest = 10000
	// MaxPaidReactionTopReactors is the number of entries shown in the top reactors leaderboard.
	MaxPaidReactionTopReactors = 3
	// Paid reaction random_id embeds unix time in its high 32 bits. The future
	// allowance covers ordinary client/server clock skew; the past window keeps
	// successful command receipts bounded while allowing transport retries.
	PaidReactionRandomIDMaxFutureSeconds = 5 * 60
	PaidReactionRandomIDMaxAgeSeconds    = 24 * 60 * 60
	PaidReactionReceiptRetentionSeconds  = 2 * PaidReactionRandomIDMaxAgeSeconds
)

var (
	ErrPaidReactionRandomIDExpired   = errors.New("paid reaction: random id expired")
	ErrPaidReactionCutoverAmbiguous  = errors.New("paid reaction: cutover random id is ambiguous")
	ErrPaidReactionSendAsPeerInvalid = errors.New("paid reaction: send-as peer invalid")
)

// PaidReactionPrivacyAccountDefault is a sendPaidReaction command intent, not
// an account setting value. A missing wire `private` flag means "resolve the
// saved account default", while an explicit paidReactionPrivacyDefault means
// "show the payer". They must remain distinct in the random_id fingerprint.
const PaidReactionPrivacyAccountDefault PaidReactionPrivacyKind = "account_default"

// PaidReactionRandomIDExpired validates the wire-required `(time()<<32)|rand`
// shape against the authoritative server time.
func PaidReactionRandomIDExpired(randomID int64, now int) bool {
	if randomID == 0 || now <= 0 {
		return true
	}
	timestamp := int64(PaidReactionRandomIDTimestamp(randomID))
	return timestamp <= 0 || timestamp < int64(now-PaidReactionRandomIDMaxAgeSeconds) ||
		timestamp > int64(now+PaidReactionRandomIDMaxFutureSeconds)
}

func PaidReactionRandomIDTimestamp(randomID int64) int {
	return int(uint64(randomID) >> 32)
}

// PaidReactor is the paid reaction stars one reactor has cumulatively put into a message.
type PaidReactor struct {
	UserID int64
	// Peer is the leaderboard identity. UserID remains the payer identity used
	// for My and idempotent aggregation; Peer may be an owned broadcast channel.
	Peer      Peer
	Stars     int64
	Anonymous bool
	My        bool // whether this is the current viewer (set per perspective when projecting)
}

func (r PaidReactor) DisplayPeer() Peer {
	if r.Peer.ID > 0 {
		return r.Peer
	}
	return Peer{Type: PeerTypeUser, ID: r.UserID}
}

// ChannelMessagePaidReactions is the paid reaction aggregate of one channel message (carried on the message / in reaction updates).
type ChannelMessagePaidReactions struct {
	TotalStars  int64         // sum of stars put in by all reactors
	MyStars     int64         // stars put in by the current viewer (0 = none)
	MyAnonymous bool          // whether the current viewer contributed anonymously
	TopReactors []PaidReactor // by Stars DESC, including the current viewer
}

// SendChannelPaidReactionRequest adds paid reaction stars to a channel message.
type SendChannelPaidReactionRequest struct {
	UserID    int64
	ChannelID int64
	MessageID int
	Stars     int64
	RandomID  int64
	// Privacy is the immutable client intent used by random_id idempotency.
	// Anonymous is the resolved value applied by the first execution; for
	// default privacy it may differ after an account setting change, without
	// changing the original command fingerprint.
	Privacy PaidReactionPrivacy
	// DisplayPeer is the first execution's server-resolved leaderboard identity.
	// It is excluded from Fingerprint: account-default resolution is mutable,
	// while Privacy records the immutable client intent. Empty means payer user.
	DisplayPeer Peer
	Anonymous   bool // privacy: whether to contribute anonymously
	Date        int
}

// Fingerprint binds random_id to the immutable client-controlled paid
// reaction intent. Server-derived Date and the resolved default privacy value
// are deliberately excluded, so a transport retry remains exact.
func (r SendChannelPaidReactionRequest) Fingerprint() [sha256.Size]byte {
	buf := make([]byte, 0, 80)
	buf = append(buf, 1) // encoding version
	buf = binary.BigEndian.AppendUint64(buf, uint64(r.UserID))
	buf = binary.BigEndian.AppendUint64(buf, uint64(r.ChannelID))
	buf = binary.BigEndian.AppendUint32(buf, uint32(r.MessageID))
	buf = binary.BigEndian.AppendUint64(buf, uint64(r.Stars))
	kind := r.Privacy.Kind
	if kind == "" {
		kind = PaidReactionPrivacyDefault
	}
	buf = binary.BigEndian.AppendUint32(buf, uint32(len(kind)))
	buf = append(buf, string(kind)...)
	if kind == PaidReactionPrivacyPeer && r.Privacy.Peer != nil {
		peerType := string(r.Privacy.Peer.Type)
		buf = binary.BigEndian.AppendUint32(buf, uint32(len(peerType)))
		buf = append(buf, peerType...)
		buf = binary.BigEndian.AppendUint64(buf, uint64(r.Privacy.Peer.ID))
	}
	return sha256.Sum256(buf)
}

// ChannelMessagePaidReactionResult is the result after adding stars, for rpc projection and fan-out.
type ChannelMessagePaidReactionResult struct {
	Channel        Channel
	Message        ChannelMessage
	Paid           ChannelMessagePaidReactions
	PayerBalance   StarsBalance
	ChannelBalance int64
	// DisplayChannels snapshots channel identities referenced by Paid reactors,
	// so an exact receipt replay can include companion chats without rechecking
	// current send-as ownership or channel membership.
	DisplayChannels []Channel
	Duplicate       bool
	Recipients      []int64
}
