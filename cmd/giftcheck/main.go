// Command giftcheck is an end-to-end verification tool for Star gifts
// (development use, not a production component).
//
// It logs in to the local telesrv as a user and verifies the whole star gift path
// against the live server:
//  1. payments.getStarGifts -- print the gift catalog.
//  2. Find a gift recipient from dialogs (or specify with -to).
//  3. payments.getPaymentForm(inputInvoiceStarGift) -- must return paymentFormStarGift (XTR + non-empty prices).
//  4. payments.sendStarsForm -- must return paymentResult{updates} (containing the gift service message + updateStarsBalance).
//  5. Compare payments.getStarsStatus before and after to verify the charge.
//
// Usage: go run ./cmd/giftcheck -phone "+8618800000001"
package main

import (
	"context"
	"crypto/rand"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"strconv"
	"syscall"

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
	proto := transport.NewProtocol(func() transport.Codec { return codec.NoHeader{Codec: codec.Intermediate{}} })
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
	phone := flag.String("phone", "", "login phone number")
	code := flag.String("code", "12345", "development login code")
	toID := flag.Int64("to", 0, "gift recipient user id (0 = auto-find from dialogs)")
	toHash := flag.Int64("to-hash", 0, "gift recipient user access_hash")
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
	host, portStr, _ := net.SplitHostPort(*addr)
	port, _ := strconv.Atoi(portStr)

	client := telegram.NewClient(*apiID, *apiHash, telegram.Options{
		PublicKeys: []exchange.PublicKey{{RSA: &priv.PublicKey}},
		Resolver:   obfuscatedResolver{host: host, port: port},
		DCList:     dcs.List{Options: []tg.DCOption{{ID: *dcID, IPAddress: host, Port: port, Static: true}}},
		Logger:     logzap.New(logger.Named("client")),
	})
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := client.Run(ctx, func(ctx context.Context) error {
		raw := tg.NewClient(client)
		sent, err := raw.AuthSendCode(ctx, &tg.AuthSendCodeRequest{PhoneNumber: *phone, APIID: *apiID, APIHash: *apiHash, Settings: tg.CodeSettings{}})
		if err != nil {
			return fmt.Errorf("sendCode: %w", err)
		}
		sc, ok := sent.(*tg.AuthSentCode)
		if !ok {
			return fmt.Errorf("sentCode type = %T", sent)
		}
		authz, err := raw.AuthSignIn(ctx, &tg.AuthSignInRequest{PhoneNumber: *phone, PhoneCodeHash: sc.PhoneCodeHash, PhoneCode: *code})
		if err != nil {
			return fmt.Errorf("signIn: %w", err)
		}
		self := authz.(*tg.AuthAuthorization).User.(*tg.User)
		fmt.Printf("==== logged in: id=%d name=%q ====\n", self.ID, self.FirstName)

		// 1. Catalog.
		gifts := giftCatalog(ctx, raw)
		if len(gifts) == 0 {
			return fmt.Errorf("gift catalog is empty (animated_emoji not seeded?)")
		}
		gift := gifts[0]
		fmt.Printf("  catalog has %d gifts; picking the first: id=%d stars=%d title=%q sticker=%T\n", len(gifts), gift.ID, gift.Stars, giftTitle(gift), gift.Sticker)

		balBefore := starsBalance(ctx, raw, "before charge")

		// 2. Gift recipient (when -to specifies an id, resolve its access_hash from dialogs; otherwise take the first user).
		var to *tg.InputPeerUser
		if *toID != 0 && *toHash != 0 {
			to = &tg.InputPeerUser{UserID: *toID, AccessHash: *toHash}
		} else {
			to = findRecipient(ctx, raw, self.ID, *toID)
		}
		if to == nil {
			return fmt.Errorf("no gift recipient found (specify with -to/-to-hash)")
		}
		fmt.Printf("  gift recipient: id=%d\n", to.UserID)

		inv := &tg.InputInvoiceStarGift{Peer: to, GiftID: gift.ID}

		// 3. getPaymentForm → paymentFormStarGift.
		formRes, err := raw.PaymentsGetPaymentForm(ctx, &tg.PaymentsGetPaymentFormRequest{Invoice: inv})
		if err != nil {
			return fmt.Errorf("getPaymentForm: %w", err)
		}
		form, ok := formRes.(*tg.PaymentsPaymentFormStarGift)
		if !ok {
			fmt.Printf("  [FAIL] getPaymentForm returned %T, want *PaymentsPaymentFormStarGift (TDesktop single-branch match)\n", formRes)
			return nil
		}
		fmt.Printf("  getPaymentForm: paymentFormStarGift form_id=%d currency=%s prices=%d\n", form.FormID, form.Invoice.Currency, len(form.Invoice.Prices))
		if form.Invoice.Currency != "XTR" || len(form.Invoice.Prices) == 0 {
			fmt.Printf("  [FAIL] invoice must be XTR + non-empty prices\n")
			return nil
		}

		// 4. sendStarsForm → paymentResult.
		payRes, err := raw.PaymentsSendStarsForm(ctx, &tg.PaymentsSendStarsFormRequest{FormID: form.FormID, Invoice: inv})
		if err != nil {
			return fmt.Errorf("sendStarsForm: %w", err)
		}
		pay, ok := payRes.(*tg.PaymentsPaymentResult)
		if !ok {
			fmt.Printf("  [FAIL] sendStarsForm returned %T, want *PaymentsPaymentResult (DrKLO hard cast)\n", payRes)
			return nil
		}
		inspectGiftUpdates(pay.Updates)

		balAfter := starsBalance(ctx, raw, "after charge")
		fmt.Printf("==== charge: %d -> %d, diff %d (expected -%d) ====\n", balBefore, balAfter, balBefore-balAfter, gift.Stars)
		if balBefore-balAfter == gift.Stars {
			fmt.Println("==== ✅ PASS: star gift charge correct ====")
		} else {
			fmt.Println("==== ❌ FAIL: charge amount mismatch ====")
		}
		return nil
	}); err != nil {
		logger.Fatal("run failed", zap.Error(err))
	}
}

func giftCatalog(ctx context.Context, raw *tg.Client) []*tg.StarGift {
	res, err := raw.PaymentsGetStarGifts(ctx, 0)
	if err != nil {
		fmt.Printf("  getStarGifts failed: %v\n", err)
		return nil
	}
	full, ok := res.(*tg.PaymentsStarGifts)
	if !ok {
		fmt.Printf("  getStarGifts returned %T\n", res)
		return nil
	}
	out := make([]*tg.StarGift, 0, len(full.Gifts))
	for _, g := range full.Gifts {
		if sg, ok := g.(*tg.StarGift); ok {
			out = append(out, sg)
		}
	}
	return out
}

func giftTitle(g *tg.StarGift) string {
	t, _ := g.GetTitle()
	return t
}

func starsBalance(ctx context.Context, raw *tg.Client, label string) int64 {
	status, err := raw.PaymentsGetStarsStatus(ctx, &tg.PaymentsGetStarsStatusRequest{Peer: &tg.InputPeerSelf{}})
	if err != nil {
		fmt.Printf("  [%s] getStarsStatus failed: %v\n", label, err)
		return -1
	}
	var bal int64
	if amt, ok := status.Balance.(*tg.StarsAmount); ok {
		bal = amt.Amount
	}
	fmt.Printf("  [%s] balance=%d stars\n", label, bal)
	return bal
}

func findRecipient(ctx context.Context, raw *tg.Client, selfID, targetID int64) *tg.InputPeerUser {
	dlgs, err := raw.MessagesGetDialogs(ctx, &tg.MessagesGetDialogsRequest{OffsetPeer: &tg.InputPeerEmpty{}, Limit: 100})
	if err != nil {
		return nil
	}
	var users []tg.UserClass
	switch v := dlgs.(type) {
	case *tg.MessagesDialogs:
		users = v.Users
	case *tg.MessagesDialogsSlice:
		users = v.Users
	default:
		return nil
	}
	for _, uc := range users {
		u, ok := uc.(*tg.User)
		if !ok || u.ID == selfID || u.Bot || u.Self {
			continue
		}
		if targetID != 0 {
			if u.ID == targetID {
				return &tg.InputPeerUser{UserID: u.ID, AccessHash: u.AccessHash}
			}
			continue
		}
		return &tg.InputPeerUser{UserID: u.ID, AccessHash: u.AccessHash}
	}
	return nil
}

func inspectGiftUpdates(res tg.UpdatesClass) {
	var ups []tg.UpdateClass
	switch v := res.(type) {
	case *tg.Updates:
		ups = v.Updates
	case *tg.UpdatesCombined:
		ups = v.Updates
	case *tg.UpdateShort:
		ups = []tg.UpdateClass{v.Update}
	default:
		fmt.Printf("  [warning] paymentResult.updates is not an Updates subtype: %T\n", res)
		return
	}
	hasGiftMsg, hasBalance := false, false
	for _, up := range ups {
		switch u := up.(type) {
		case *tg.UpdateNewMessage:
			if svc, ok := u.Message.(*tg.MessageService); ok {
				if a, ok := svc.Action.(*tg.MessageActionStarGift); ok {
					hasGiftMsg = true
					gid := int64(0)
					if sg, ok := a.Gift.(*tg.StarGift); ok {
						gid = sg.ID
					}
					fmt.Printf("    messageActionStarGift: msg_id=%d gift_id=%d\n", u.Message.(*tg.MessageService).ID, gid)
				}
			}
		case *tg.UpdateStarsBalance:
			hasBalance = true
			if amt, ok := u.Balance.(*tg.StarsAmount); ok {
				fmt.Printf("    updateStarsBalance: %d\n", amt.Amount)
			}
		}
	}
	fmt.Printf("  paymentResult: hasGiftServiceMessage=%v hasUpdateStarsBalance=%v\n", hasGiftMsg, hasBalance)
}
