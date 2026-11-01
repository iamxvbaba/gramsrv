// Command starcheck is an end-to-end verification tool for Stars paid reactions
// (development use, not a production component).
//
// It logs in to the local telesrv as a user (development code 12345) and verifies
// the Stars local ledger and paid reactions:
//  1. payments.getStarsStatus -- print the current balance (Phase 1: after the lazy first-read grant it should be >0).
//  2. Auto-find a broadcast channel + its top message from dialogs (or specify with -channel-id/-access-hash/-msg-id).
//  3. messages.sendPaidReaction -- send a paid reaction and print the returned Updates structure
//     (Phase 2 crash constraint: must be *tg.Updates and should contain updateMessageReactions + updateStarsBalance).
//  4. payments.getStarsStatus -- print the balance again to verify -count was charged.
//
// Usage:
//
//	go run ./cmd/starcheck -phone "+8618800000001" -count 50
package main

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"go.uber.org/zap"

	"github.com/gotd/log/logzap"
	"github.com/iamxvbaba/td/exchange"
	"github.com/iamxvbaba/td/mtproxy"
	"github.com/iamxvbaba/td/mtproxy/obfuscator"
	"github.com/iamxvbaba/td/proto/codec"
	"github.com/iamxvbaba/td/telegram"
	"github.com/iamxvbaba/td/telegram/dcs"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/transport"

	"telesrv/internal/mtprotoedge"
)

type obfuscatedResolver struct {
	host string
	port int
}

func (r obfuscatedResolver) dial(ctx context.Context, dc int) (transport.Conn, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(r.host, strconv.Itoa(r.port)))
	if err != nil {
		return nil, err
	}
	obf := obfuscator.Obfuscated2(rand.Reader, conn)
	if err := obf.Handshake(codec.IntermediateClientStart, dc, mtproxy.Secret{}); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("obfuscated2 handshake: %w", err)
	}
	proto := transport.NewProtocol(func() transport.Codec {
		return codec.NoHeader{Codec: codec.Intermediate{}}
	})
	tc, err := proto.Handshake(obf)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("transport handshake: %w", err)
	}
	return tc, nil
}

func (r obfuscatedResolver) Primary(ctx context.Context, dc int, _ dcs.List) (transport.Conn, error) {
	return r.dial(ctx, dc)
}
func (r obfuscatedResolver) MediaOnly(ctx context.Context, dc int, _ dcs.List) (transport.Conn, error) {
	return r.dial(ctx, dc)
}
func (r obfuscatedResolver) CDN(ctx context.Context, dc int, _ dcs.List) (transport.Conn, error) {
	return r.dial(ctx, dc)
}

func main() {
	addr := flag.String("addr", "127.0.0.1:2398", "telesrv MTProto address")
	dcID := flag.Int("dc", 2, "DC id")
	rsaPath := flag.String("rsa", "data/server_rsa.pem", "server RSA key path")
	apiID := flag.Int("api-id", 1, "api_id")
	apiHash := flag.String("api-hash", "hash", "api_hash")
	phone := flag.String("phone", "", "login phone number, e.g. +8618800000001")
	code := flag.String("code", "12345", "development login code")
	count := flag.Int("count", 50, "number of stars for the paid reaction")
	channelID := flag.Int64("channel-id", 0, "channel id to use (0 = auto-find a broadcast channel from dialogs)")
	accessHash := flag.Int64("access-hash", 0, "channel access_hash to use")
	msgID := flag.Int("msg-id", 0, "message id to use (0 = use the channel's top message)")
	flag.Parse()

	if *phone == "" {
		fmt.Fprintln(os.Stderr, "missing -phone")
		os.Exit(2)
	}

	logger, _ := zap.NewDevelopment()
	defer func() { _ = logger.Sync() }()

	priv, err := mtprotoedge.LoadOrGenerateRSAKey(*rsaPath)
	if err != nil {
		logger.Fatal("failed to load RSA key", zap.Error(err))
	}
	host, portStr, err := net.SplitHostPort(*addr)
	if err != nil {
		logger.Fatal("failed to parse address", zap.Error(err))
	}
	port, _ := strconv.Atoi(portStr)

	client := telegram.NewClient(*apiID, *apiHash, telegram.Options{
		PublicKeys: []exchange.PublicKey{{RSA: &priv.PublicKey}},
		Resolver:   obfuscatedResolver{host: host, port: port},
		DCList: dcs.List{Options: []tg.DCOption{
			{ID: *dcID, IPAddress: host, Port: port, Static: true},
		}},
		Logger: logzap.New(logger.Named("client")),
	})

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := client.Run(ctx, func(ctx context.Context) error {
		raw := tg.NewClient(client)

		// 1. User login (development code).
		sent, err := raw.AuthSendCode(ctx, &tg.AuthSendCodeRequest{
			PhoneNumber: *phone, APIID: *apiID, APIHash: *apiHash,
			Settings: tg.CodeSettings{},
		})
		if err != nil {
			return fmt.Errorf("sendCode: %w", err)
		}
		sentCode, ok := sent.(*tg.AuthSentCode)
		if !ok {
			return fmt.Errorf("sentCode type = %T", sent)
		}
		authz, err := raw.AuthSignIn(ctx, &tg.AuthSignInRequest{
			PhoneNumber: *phone, PhoneCodeHash: sentCode.PhoneCodeHash, PhoneCode: *code,
		})
		if err != nil {
			return fmt.Errorf("signIn: %w", err)
		}
		a, ok := authz.(*tg.AuthAuthorization)
		if !ok {
			return fmt.Errorf("authorization type = %T", authz)
		}
		self, _ := a.User.(*tg.User)
		fmt.Printf("==== login successful: id=%d name=%q phone=%s ====\n", self.ID, self.FirstName, *phone)

		// 2. Balance (before).
		balBefore := printStarsBalance(ctx, raw, "before charge")

		// 3. Resolve the target channel + message.
		var peer *tg.InputPeerChannel
		mid := *msgID
		if *channelID != 0 {
			peer = &tg.InputPeerChannel{ChannelID: *channelID, AccessHash: *accessHash}
		} else {
			ch, topMsg, err := findBroadcastChannel(ctx, raw)
			if err != nil {
				return err
			}
			peer = &tg.InputPeerChannel{ChannelID: ch.ID, AccessHash: ch.AccessHash}
			if mid == 0 {
				mid = topMsg
			}
			fmt.Printf("  target channel: id=%d title=%q access_hash=%d top_msg=%d\n", ch.ID, ch.Title, ch.AccessHash, mid)
		}
		if mid == 0 {
			return fmt.Errorf("no usable message id (channel has no messages? specify with -msg-id)")
		}

		// 4. Send the paid reaction.
		rid, _ := randInt64()
		fmt.Printf(">> sendPaidReaction: channel=%d msg=%d count=%d\n", peer.ChannelID, mid, *count)
		res, err := raw.MessagesSendPaidReaction(ctx, &tg.MessagesSendPaidReactionRequest{
			Peer: peer, MsgID: mid, Count: *count, RandomID: rid,
		})
		if err != nil {
			return fmt.Errorf("sendPaidReaction: %w", err)
		}
		inspectPaidReactionUpdates(res)

		// 5. Balance (after).
		balAfter := printStarsBalance(ctx, raw, "after charge")
		fmt.Printf("==== charge check: %d -> %d, diff %d (expected -%d) ====\n", balBefore, balAfter, balBefore-balAfter, *count)
		if balBefore-balAfter == int64(*count) {
			fmt.Println("==== ✅ PASS: paid reaction charge correct ====")
		} else {
			fmt.Println("==== ❌ FAIL: charge amount mismatch ====")
		}
		return nil
	}); err != nil {
		logger.Fatal("run failed", zap.Error(err))
	}
}

func printStarsBalance(ctx context.Context, raw *tg.Client, label string) int64 {
	status, err := raw.PaymentsGetStarsStatus(ctx, &tg.PaymentsGetStarsStatusRequest{Peer: &tg.InputPeerSelf{}})
	if err != nil {
		fmt.Printf("  [%s] getStarsStatus failed: %v\n", label, err)
		return -1
	}
	amount, _ := status.Balance.(*tg.StarsAmount)
	var bal int64
	if amount != nil {
		bal = amount.Amount
	}
	fmt.Printf("  [%s] balance=%d stars (balance type=%T, chats=%d users=%d)\n", label, bal, status.Balance, len(status.Chats), len(status.Users))
	return bal
}

// findBroadcastChannel finds the first broadcast channel in dialogs + its top message id.
func findBroadcastChannel(ctx context.Context, raw *tg.Client) (*tg.Channel, int, error) {
	dlgs, err := raw.MessagesGetDialogs(ctx, &tg.MessagesGetDialogsRequest{
		OffsetPeer: &tg.InputPeerEmpty{}, Limit: 100,
	})
	if err != nil {
		return nil, 0, fmt.Errorf("getDialogs: %w", err)
	}
	var chats []tg.ChatClass
	var dialogs []tg.DialogClass
	switch v := dlgs.(type) {
	case *tg.MessagesDialogs:
		chats, dialogs = v.Chats, v.Dialogs
	case *tg.MessagesDialogsSlice:
		chats, dialogs = v.Chats, v.Dialogs
	default:
		return nil, 0, fmt.Errorf("dialogs type = %T", dlgs)
	}
	topByChannel := make(map[int64]int)
	for _, d := range dialogs {
		dd, ok := d.(*tg.Dialog)
		if !ok {
			continue
		}
		if pc, ok := dd.Peer.(*tg.PeerChannel); ok {
			topByChannel[pc.ChannelID] = dd.TopMessage
		}
	}
	for _, c := range chats {
		ch, ok := c.(*tg.Channel)
		if !ok || !ch.Broadcast || ch.Megagroup {
			continue
		}
		return ch, topByChannel[ch.ID], nil
	}
	return nil, 0, fmt.Errorf("no broadcast channel in dialogs")
}

// inspectPaidReactionUpdates checks whether the returned Updates is valid and contains updateMessageReactions/updateStarsBalance.
func inspectPaidReactionUpdates(res tg.UpdatesClass) {
	fmt.Printf("  return type=%T\n", res)
	var ups []tg.UpdateClass
	switch v := res.(type) {
	case *tg.Updates:
		ups = v.Updates
	case *tg.UpdatesCombined:
		ups = v.Updates
	case *tg.UpdateShort:
		ups = []tg.UpdateClass{v.Update}
	default:
		fmt.Printf("  [warning] returned a non-Updates subtype (DrKLO will crash with ClassCastException)\n")
		return
	}
	hasReactions, hasBalance := false, false
	for _, up := range ups {
		switch u := up.(type) {
		case *tg.UpdateMessageReactions:
			hasReactions = true
			paid := 0
			for _, rc := range u.Reactions.Results {
				if _, ok := rc.Reaction.(*tg.ReactionPaid); ok {
					paid = rc.Count
				}
			}
			reactors, _ := u.Reactions.GetTopReactors()
			fmt.Printf("    updateMessageReactions: msg=%d paid_count=%d top_reactors=%d\n", u.MsgID, paid, len(reactors))
		case *tg.UpdateStarsBalance:
			hasBalance = true
			amount, _ := u.Balance.(*tg.StarsAmount)
			var b int64
			if amount != nil {
				b = amount.Amount
			}
			fmt.Printf("    updateStarsBalance: balance=%d\n", b)
		}
	}
	fmt.Printf("  valid Updates=true, has updateMessageReactions=%v, has updateStarsBalance=%v\n", hasReactions, hasBalance)
}

func randInt64() (int64, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return time.Now().UnixNano(), err
	}
	v := int64(binary.LittleEndian.Uint64(b[:]))
	if v == 0 {
		v = 1
	}
	return v, nil
}
