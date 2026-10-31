// Command botdemo is a reference example of a bot that proactively sends
// messages to a group or user on a timer (for development/teaching).
//
// It demonstrates how an external bot program can:
//  1. Log in to telesrv through auth.importBotAuthorization with a BotFather token;
//  2. Resolve a target peer (supergroup channel or user);
//  3. In a timed loop, proactively send three kinds of content at a set interval:
//     - plain text (messages.sendMessage)
//     - photo      (upload a file + messages.sendMedia(inputMediaUploadedPhoto))
//     - photo+caption (same, but with a caption)
//
// A bot sending a message in a group goes through messages.sendMessage/sendMedia
// just like any ordinary member, with no need to be triggered by "@/command" -- the
// timer here is the trigger for "proactive send"; swapping in a business event/cron
// is the same set of calls. Privacy mode (bot_chat_history) only affects which
// messages the bot can receive; it does not restrict what it can send.
//
// Usage example (send text/photo/photo+caption in rotation to a supergroup, one per 15s):
//
//	go run ./cmd/bots/botdemo \
//	    -token "<bot_id>:<secret>" \
//	    -chat <channel_id> -chat-hash <access_hash> \
//	    -interval 15s -mode rotate
//
// Other common parameters:
//
//	-to <user_id> -to-hash <hash>   # instead, send private messages to a user (the user must have interacted with the bot first)
//	-mode text|photo|caption|rotate # text only / photo only / photo+caption only / all three in rotation (default rotate)
//	-image path/to/pic.jpg          # specify an image file; if omitted the program generates a PNG automatically
//	-text "..."  -caption "..."     # custom text and caption (the program appends a sequence number+time to make each one different)
//	-count 5                        # exit after sending 5 (default 0 = keep sending until Ctrl+C)
//
// Connecting to telesrv uses standard no-secret MTProto TCP obfuscation
// (obfuscated2), same as the production server.
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/png"
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
	"github.com/iamxvbaba/td/telegram/uploader"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/transport"

	"telesrv/internal/mtprotoedge"
)

// obfuscatedResolver connects to telesrv with standard no-secret MTProto TCP
// obfuscation (obfuscated2), matching the obfuscated2 path the production server
// auto-detects. gotd's built-in dcs.Plain TCPObfuscatedOnly path goes through
// MTProxy (which forces a secret), which does not apply here, hence the custom
// Resolver.
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

type config struct {
	chatID, chatHash int64
	toID, toHash     int64
	interval         time.Duration
	count            int
	mode             string
	text, caption    string
	imagePath        string
}

func main() {
	addr := flag.String("addr", "127.0.0.1:2398", "telesrv MTProto address")
	dcID := flag.Int("dc", 2, "DC id")
	token := flag.String("token", os.Getenv("TELESRV_BOT_TOKEN"), "bot token <bot_id>:<secret> (defaults to env TELESRV_BOT_TOKEN)")
	rsaPath := flag.String("rsa", "data/server_rsa.pem", "server RSA public key path")
	apiID := flag.Int("api-id", 1, "api_id")
	apiHash := flag.String("api-hash", "hash", "api_hash")

	chatID := flag.Int64("chat", 0, "target supergroup channel id (non-zero sends to the group)")
	chatHash := flag.Int64("chat-hash", 0, "target group access_hash (may be left 0 under telesrv)")
	toID := flag.Int64("to", 0, "target user id (when -chat is 0, send a private message to this user)")
	toHash := flag.Int64("to-hash", 0, "target user access_hash (may be left 0 under telesrv)")

	interval := flag.Duration("interval", 15*time.Second, "timed send interval")
	count := flag.Int("count", 0, "maximum number of sends (0 = keep sending until Ctrl+C)")
	mode := flag.String("mode", "rotate", "content to send: text | photo | caption | rotate")
	text := flag.String("text", "telesrv bot demo · timed proactive message", "text content (a sequence number+time is appended automatically)")
	caption := flag.String("caption", "telesrv bot demo · photo+caption message", "caption text (a sequence number+time is appended automatically)")
	imagePath := flag.String("image", "", "image file path (if empty the program generates a PNG automatically)")
	flag.Parse()

	if *token == "" {
		fmt.Fprintln(os.Stderr, "missing -token (or set the TELESRV_BOT_TOKEN environment variable)")
		os.Exit(2)
	}
	if *chatID == 0 && *toID == 0 {
		fmt.Fprintln(os.Stderr, "target required: -chat <group id> (or -to <user id>)")
		os.Exit(2)
	}
	switch *mode {
	case "text", "photo", "caption", "rotate":
	default:
		fmt.Fprintf(os.Stderr, "unknown -mode=%q (text|photo|caption|rotate)\n", *mode)
		os.Exit(2)
	}

	cfg := config{
		chatID: *chatID, chatHash: *chatHash, toID: *toID, toHash: *toHash,
		interval: *interval, count: *count, mode: *mode,
		text: *text, caption: *caption, imagePath: *imagePath,
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
		DCList:     dcs.List{Options: []tg.DCOption{{ID: *dcID, IPAddress: host, Port: port, Static: true}}},
		Logger:     logzap.New(logger.Named("client")),
	})

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := client.Run(ctx, func(ctx context.Context) error {
		api := tg.NewClient(client)

		// 1) Log in as the bot.
		self, err := loginBot(ctx, api, *apiID, *apiHash, *token)
		if err != nil {
			return err
		}
		fmt.Printf("==== bot logged in: @%s (id=%d) ====\n", self.Username, self.ID)

		// 2) Resolve the target peer.
		peer, label, err := resolveTarget(ctx, api, cfg)
		if err != nil {
			return err
		}
		fmt.Printf(">> target: %s\n>> interval: %s, mode: %s, limit: %s\n", label, cfg.interval, cfg.mode, countLabel(cfg.count))

		// 3) Timed proactive sending.
		return runLoop(ctx, api, uploader.NewUploader(api), peer, cfg, logger)
	}); err != nil {
		logger.Fatal("run failed", zap.Error(err))
	}
	fmt.Println("exited")
}

// loginBot logs in with the token and returns the self user.
func loginBot(ctx context.Context, api *tg.Client, apiID int, apiHash, token string) (*tg.User, error) {
	authz, err := api.AuthImportBotAuthorization(ctx, &tg.AuthImportBotAuthorizationRequest{
		APIID: apiID, APIHash: apiHash, BotAuthToken: token,
	})
	if err != nil {
		return nil, fmt.Errorf("importBotAuthorization: %w", err)
	}
	a, ok := authz.(*tg.AuthAuthorization)
	if !ok {
		return nil, fmt.Errorf("authorization type = %T", authz)
	}
	self, ok := a.User.(*tg.User)
	if !ok || !self.Bot {
		return nil, fmt.Errorf("login account is not a bot: %T", a.User)
	}
	return self, nil
}

// resolveTarget resolves the command-line target into an InputPeer. The group takes
// priority; when access_hash is 0 it tries to resolve the real hash via
// getChannels/getUsers (telesrv allows the hash=0 bypass, but resolving is more
// robust and closer to a real client).
func resolveTarget(ctx context.Context, api *tg.Client, cfg config) (tg.InputPeerClass, string, error) {
	if cfg.chatID != 0 {
		hash := cfg.chatHash
		title := ""
		if res, err := api.ChannelsGetChannels(ctx, []tg.InputChannelClass{
			&tg.InputChannel{ChannelID: cfg.chatID, AccessHash: cfg.chatHash},
		}); err == nil {
			for _, c := range res.GetChats() {
				if ch, ok := c.(*tg.Channel); ok && ch.ID == cfg.chatID {
					hash, title = ch.AccessHash, ch.Title
				}
			}
		}
		return &tg.InputPeerChannel{ChannelID: cfg.chatID, AccessHash: hash},
			fmt.Sprintf("supergroup %q (id=%d)", title, cfg.chatID), nil
	}

	hash := cfg.toHash
	name := ""
	if list, err := api.UsersGetUsers(ctx, []tg.InputUserClass{&tg.InputUser{UserID: cfg.toID, AccessHash: cfg.toHash}}); err == nil {
		for _, uc := range list {
			if u, ok := uc.(*tg.User); ok && u.ID == cfg.toID {
				hash, name = u.AccessHash, u.FirstName
			}
		}
	}
	return &tg.InputPeerUser{UserID: cfg.toID, AccessHash: hash},
		fmt.Sprintf("user %q (id=%d)", name, cfg.toID), nil
}

// runLoop is the core of timed proactive sending: it sends one message per interval,
// picking the content by mode; when count>0 it stops once enough have been sent.
func runLoop(ctx context.Context, api *tg.Client, up *uploader.Uploader, peer tg.InputPeerClass, cfg config, logger *zap.Logger) error {
	ticker := time.NewTicker(cfg.interval)
	defer ticker.Stop()

	seq := 0
	send := func() error {
		seq++
		kind := pickKind(cfg.mode, seq)
		stamp := time.Now().Format("15:04:05")
		switch kind {
		case "text":
			msg := fmt.Sprintf("%s · #%d · %s", cfg.text, seq, stamp)
			if err := sendText(ctx, api, peer, msg); err != nil {
				return err
			}
			fmt.Printf("[%d] text sent: %q\n", seq, msg)
		case "photo":
			if err := sendPhoto(ctx, api, up, peer, seq, "", cfg.imagePath); err != nil {
				return err
			}
			fmt.Printf("[%d] photo sent\n", seq)
		case "caption":
			captionText := fmt.Sprintf("%s · #%d · %s", cfg.caption, seq, stamp)
			if err := sendPhoto(ctx, api, up, peer, seq, captionText, cfg.imagePath); err != nil {
				return err
			}
			fmt.Printf("[%d] photo+caption sent: %q\n", seq, captionText)
		}
		return nil
	}

	// Send the first one immediately (no need to wait a full interval),
	// then send on the ticker period.
	if err := send(); err != nil {
		logger.Warn("send failed", zap.Error(err))
	}
	if cfg.count > 0 && seq >= cfg.count {
		return nil
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := send(); err != nil {
				logger.Warn("send failed", zap.Error(err))
				continue
			}
			if cfg.count > 0 && seq >= cfg.count {
				return nil
			}
		}
	}
}

// pickKind decides what to send this time: a fixed mode returns directly; rotate
// cycles text→photo→caption by sequence number.
func pickKind(mode string, seq int) string {
	if mode != "rotate" {
		return mode
	}
	switch (seq - 1) % 3 {
	case 0:
		return "text"
	case 1:
		return "photo"
	default:
		return "caption"
	}
}

// sendText sends a single plain-text message.
func sendText(ctx context.Context, api *tg.Client, peer tg.InputPeerClass, message string) error {
	rid, _ := randInt64()
	_, err := api.MessagesSendMessage(ctx, &tg.MessagesSendMessageRequest{
		Peer:     peer,
		Message:  message,
		RandomID: rid,
	})
	if err != nil {
		return fmt.Errorf("sendMessage: %w", err)
	}
	return nil
}

// sendPhoto uploads and sends an image; a non-empty caption makes it a
// photo+caption, an empty one makes it a plain photo. Image source: the file
// named by cfg.imagePath, or a PNG generated by the program from the sequence
// number (guaranteeing each one differs).
func sendPhoto(ctx context.Context, api *tg.Client, up *uploader.Uploader, peer tg.InputPeerClass, seq int, caption, imagePath string) error {
	var (
		file tg.InputFileClass
		err  error
	)
	if imagePath != "" {
		// uploader splits into parts automatically via upload.saveFilePart
		// (large files take the big-file path).
		file, err = up.FromPath(ctx, imagePath)
		if err != nil {
			return fmt.Errorf("upload image file %q: %w", imagePath, err)
		}
	} else {
		data := generatePNG(seq)
		file, err = up.FromBytes(ctx, fmt.Sprintf("botdemo-%d.png", seq), data)
		if err != nil {
			return fmt.Errorf("upload generated image: %w", err)
		}
	}

	rid, _ := randInt64()
	_, err = api.MessagesSendMedia(ctx, &tg.MessagesSendMediaRequest{
		Peer:     peer,
		Media:    &tg.InputMediaUploadedPhoto{File: file},
		Message:  caption, // empty = plain photo; non-empty = photo+caption (photo + caption text)
		RandomID: rid,
	})
	if err != nil {
		return fmt.Errorf("sendMedia(photo): %w", err)
	}
	return nil
}

// generatePNG programmatically generates a 600x400 PNG whose colors vary with the
// sequence number, drawing a block that moves with the sequence number, so
// consecutively sent images are visually distinguishable. Pure standard library,
// no external image file needed, so the demo runs directly.
func generatePNG(seq int) []byte {
	const w, h = 600, 400
	img := image.NewRGBA(image.Rect(0, 0, w, h))

	// Background: a horizontal gradient whose hue rotates with the sequence number.
	bg := hsv(float64((seq*37)%360), 0.55, 0.95)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			shade := uint8(40 + (x*40)/w)
			img.Set(x, y, color.RGBA{
				R: clamp(int(bg.R) - 20 + int(shade)),
				G: clamp(int(bg.G) - 20 + int(shade)),
				B: clamp(int(bg.B) - 20 + int(shade)),
				A: 255,
			})
		}
	}
	// A contrasting-color block that moves horizontally with the sequence
	// number -- visually showing "which message this is".
	block := hsv(float64((seq*37+180)%360), 0.8, 0.9)
	bx := 30 + (seq*45)%(w-130)
	for y := 150; y < 250; y++ {
		for x := bx; x < bx+100 && x < w; x++ {
			img.Set(x, y, block)
		}
	}

	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}

// hsv converts HSV(h∈[0,360), s,v∈[0,1]) to RGBA (the standard library has no HSV, so a simple one is implemented).
func hsv(hDeg, s, v float64) color.RGBA {
	c := v * s
	x := c * (1 - absf(modf(hDeg/60, 2)-1))
	m := v - c
	var r, g, b float64
	switch {
	case hDeg < 60:
		r, g, b = c, x, 0
	case hDeg < 120:
		r, g, b = x, c, 0
	case hDeg < 180:
		r, g, b = 0, c, x
	case hDeg < 240:
		r, g, b = 0, x, c
	case hDeg < 300:
		r, g, b = x, 0, c
	default:
		r, g, b = c, 0, x
	}
	return color.RGBA{
		R: uint8((r + m) * 255),
		G: uint8((g + m) * 255),
		B: uint8((b + m) * 255),
		A: 255,
	}
}

func absf(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}

func modf(a, m float64) float64 {
	for a >= m {
		a -= m
	}
	for a < 0 {
		a += m
	}
	return a
}

func clamp(v int) uint8 {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return uint8(v)
}

func countLabel(count int) string {
	if count <= 0 {
		return "unlimited"
	}
	return strconv.Itoa(count)
}

// randInt64 generates a non-zero random random_id (to prevent (sender,random_id) idempotency-key collisions).
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
