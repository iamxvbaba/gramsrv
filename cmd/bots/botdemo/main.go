// Command botdemo 是一个「定时向群/用户主动发消息」的 bot 参考示例（开发/教学用）。
//
// 它演示一个外部 bot 程序如何：
//  1. 用 BotFather 发的 token 经 auth.importBotAuthorization 登录 telesrv；
//  2. 解析目标 peer（超级群 channel 或用户 user）；
//  3. 在一个定时循环里，按设定间隔【主动】发送三种内容：
//     - 纯文本   （messages.sendMessage）
//     - 图片     （upload 文件 + messages.sendMedia(inputMediaUploadedPhoto)）
//     - 图文     （同上，但带 caption）
//
// bot 在群里发消息和任何普通成员一样走 messages.sendMessage/sendMedia，无需被「@/命令」
// 触发——这里的定时器就是「主动发」的触发源；换成业务事件/cron 也是同一套调用。隐私模式
// （bot_chat_history）只影响 bot 能【收到】哪些消息，不限制它【发送】。
//
// 用法示例（向超级群定时轮流发文本/图片/图文，每 15s 一条）：
//
//	go run ./cmd/bots/botdemo \
//	    -token "<bot_id>:<secret>" \
//	    -chat <channel_id> -chat-hash <access_hash> \
//	    -interval 15s -mode rotate
//
// 其它常用参数：
//
//	-to <user_id> -to-hash <hash>   # 改为给某个用户私聊发（需用户先与 bot 交互过）
//	-mode text|photo|caption|rotate # 只发文本 / 只发图片 / 只发图文 / 三者轮流（默认 rotate）
//	-image path/to/pic.jpg          # 指定图片文件；不指定则程序自动生成一张 PNG
//	-text "..."  -caption "..."     # 自定义文本与图文说明（程序会自动追加序号+时间使每条不同）
//	-count 5                        # 发 5 条后退出（默认 0 = 一直发到 Ctrl+C）
//
// 连接 telesrv 用标准无-secret MTProto TCP obfuscation（obfuscated2），与生产 server 一致。
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
	"github.com/gotd/td/exchange"
	"github.com/gotd/td/mtproxy"
	"github.com/gotd/td/mtproxy/obfuscator"
	"github.com/gotd/td/proto/codec"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/dcs"
	"github.com/gotd/td/telegram/uploader"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/transport"

	"telesrv/internal/mtprotoedge"
)

// obfuscatedResolver 用标准无-secret MTProto TCP obfuscation（obfuscated2）连接 telesrv，
// 匹配生产 server 的 transport.ObfuscatedListener。gotd 内置 dcs.Plain 的 obfuscated 路径
// 走 MTProxy（强制 secret），不适用这里，所以自定义一个 Resolver。
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
	addr := flag.String("addr", "127.0.0.1:2398", "telesrv MTProto 地址")
	dcID := flag.Int("dc", 2, "DC id")
	token := flag.String("token", os.Getenv("TELESRV_BOT_TOKEN"), "bot token <bot_id>:<secret>（默认取 env TELESRV_BOT_TOKEN）")
	rsaPath := flag.String("rsa", "data/server_rsa.pem", "server RSA 公钥路径")
	apiID := flag.Int("api-id", 1, "api_id")
	apiHash := flag.String("api-hash", "hash", "api_hash")

	chatID := flag.Int64("chat", 0, "目标超级群 channel id（非 0 即发到群）")
	chatHash := flag.Int64("chat-hash", 0, "目标群 access_hash（telesrv 下可留 0）")
	toID := flag.Int64("to", 0, "目标用户 id（-chat 为 0 时改为私聊该用户）")
	toHash := flag.Int64("to-hash", 0, "目标用户 access_hash（telesrv 下可留 0）")

	interval := flag.Duration("interval", 15*time.Second, "定时发送间隔")
	count := flag.Int("count", 0, "发送条数上限（0 = 一直发到 Ctrl+C）")
	mode := flag.String("mode", "rotate", "发送内容：text | photo | caption | rotate")
	text := flag.String("text", "telesrv bot demo · 定时主动消息", "文本内容（会自动追加 #序号+时间）")
	caption := flag.String("caption", "telesrv bot demo · 图文消息", "图文说明（会自动追加 #序号+时间）")
	imagePath := flag.String("image", "", "图片文件路径（留空则程序自动生成一张 PNG）")
	flag.Parse()

	if *token == "" {
		fmt.Fprintln(os.Stderr, "缺少 -token（或设环境变量 TELESRV_BOT_TOKEN）")
		os.Exit(2)
	}
	if *chatID == 0 && *toID == 0 {
		fmt.Fprintln(os.Stderr, "需指定目标：-chat <群id>（或 -to <用户id>）")
		os.Exit(2)
	}
	switch *mode {
	case "text", "photo", "caption", "rotate":
	default:
		fmt.Fprintf(os.Stderr, "未知 -mode=%q（text|photo|caption|rotate）\n", *mode)
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
		logger.Fatal("加载 RSA key 失败", zap.Error(err))
	}
	host, portStr, err := net.SplitHostPort(*addr)
	if err != nil {
		logger.Fatal("解析地址失败", zap.Error(err))
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

		// 1) 登录为 bot。
		self, err := loginBot(ctx, api, *apiID, *apiHash, *token)
		if err != nil {
			return err
		}
		fmt.Printf("==== bot 登录成功：@%s (id=%d) ====\n", self.Username, self.ID)

		// 2) 解析目标 peer。
		peer, label, err := resolveTarget(ctx, api, cfg)
		if err != nil {
			return err
		}
		fmt.Printf(">> 目标：%s\n>> 间隔：%s，模式：%s，上限：%s\n", label, cfg.interval, cfg.mode, countLabel(cfg.count))

		// 3) 定时主动发送。
		return runLoop(ctx, api, uploader.NewUploader(api), peer, cfg, logger)
	}); err != nil {
		logger.Fatal("运行失败", zap.Error(err))
	}
	fmt.Println("已退出")
}

// loginBot 用 token 登录并返回 self 用户。
func loginBot(ctx context.Context, api *tg.Client, apiID int, apiHash, token string) (*tg.User, error) {
	authz, err := api.AuthImportBotAuthorization(ctx, &tg.AuthImportBotAuthorizationRequest{
		APIID: apiID, APIHash: apiHash, BotAuthToken: token,
	})
	if err != nil {
		return nil, fmt.Errorf("importBotAuthorization: %w", err)
	}
	a, ok := authz.(*tg.AuthAuthorization)
	if !ok {
		return nil, fmt.Errorf("authorization 类型 = %T", authz)
	}
	self, ok := a.User.(*tg.User)
	if !ok || !self.Bot {
		return nil, fmt.Errorf("登录账号不是 bot：%T", a.User)
	}
	return self, nil
}

// resolveTarget 把命令行目标解析成 InputPeer。群优先；access_hash 给 0 时尝试经
// getChannels/getUsers 解析出真实 hash（telesrv 允许 hash=0 旁路，但解析更稳、也更贴近真实客户端）。
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
			fmt.Sprintf("超级群 %q (id=%d)", title, cfg.chatID), nil
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
		fmt.Sprintf("用户 %q (id=%d)", name, cfg.toID), nil
}

// runLoop 是定时主动发送的核心：每 interval 发一条，按 mode 选内容；count>0 时发够即停。
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
			fmt.Printf("[%d] 文本已发：%q\n", seq, msg)
		case "photo":
			if err := sendPhoto(ctx, api, up, peer, seq, "", cfg.imagePath); err != nil {
				return err
			}
			fmt.Printf("[%d] 图片已发\n", seq)
		case "caption":
			captionText := fmt.Sprintf("%s · #%d · %s", cfg.caption, seq, stamp)
			if err := sendPhoto(ctx, api, up, peer, seq, captionText, cfg.imagePath); err != nil {
				return err
			}
			fmt.Printf("[%d] 图文已发：%q\n", seq, captionText)
		}
		return nil
	}

	// 立即发第一条（不必等满一个 interval），随后按 ticker 周期发。
	if err := send(); err != nil {
		logger.Warn("发送失败", zap.Error(err))
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
				logger.Warn("发送失败", zap.Error(err))
				continue
			}
			if cfg.count > 0 && seq >= cfg.count {
				return nil
			}
		}
	}
}

// pickKind 决定本次发什么：固定模式直接返回；rotate 模式按序号轮流 text→photo→caption。
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

// sendText 发一条纯文本消息。
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

// sendPhoto 上传一张图片并发出；caption 非空即「图文」，为空即纯图片。
// 图片来源：cfg.imagePath 指定的文件，或程序按序号生成的 PNG（保证每条都不一样）。
func sendPhoto(ctx context.Context, api *tg.Client, up *uploader.Uploader, peer tg.InputPeerClass, seq int, caption, imagePath string) error {
	var (
		file tg.InputFileClass
		err  error
	)
	if imagePath != "" {
		// uploader 会自动分片 upload.saveFilePart（大文件走 big-file 路径）。
		file, err = up.FromPath(ctx, imagePath)
		if err != nil {
			return fmt.Errorf("上传图片文件 %q: %w", imagePath, err)
		}
	} else {
		data := generatePNG(seq)
		file, err = up.FromBytes(ctx, fmt.Sprintf("botdemo-%d.png", seq), data)
		if err != nil {
			return fmt.Errorf("上传生成图片: %w", err)
		}
	}

	rid, _ := randInt64()
	_, err = api.MessagesSendMedia(ctx, &tg.MessagesSendMediaRequest{
		Peer:     peer,
		Media:    &tg.InputMediaUploadedPhoto{File: file},
		Message:  caption, // 空 = 纯图片；非空 = 图文（图片 + 说明文字）
		RandomID: rid,
	})
	if err != nil {
		return fmt.Errorf("sendMedia(photo): %w", err)
	}
	return nil
}

// generatePNG 程序化生成一张 600x400 的 PNG，颜色随序号变化、并画一个随序号移动的方块，
// 让连发的每张图肉眼可区分。纯标准库实现，无需外部图片文件，方便直接跑 demo。
func generatePNG(seq int) []byte {
	const w, h = 600, 400
	img := image.NewRGBA(image.Rect(0, 0, w, h))

	// 背景：按序号轮转色相的横向渐变。
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
	// 一个随序号横向移动的对比色方块——直观体现「这是第几条」。
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

// hsv 把 HSV(h∈[0,360), s,v∈[0,1]) 转成 RGBA（标准库无 HSV，简单实现一个）。
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
		return "无限"
	}
	return strconv.Itoa(count)
}

// randInt64 生成非零随机 random_id（防 (sender,random_id) 幂等键碰撞）。
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
