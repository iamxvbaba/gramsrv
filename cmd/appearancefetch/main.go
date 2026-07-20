// Command appearancefetch 从官方 Telegram 拉取墙纸 + 聊天主题,下载文档/缩略图,
// 生成 telesrv 外观 seed(default_appearance_seed.json + default_wallpapers/{documents,thumbs/m}/*.dat)。
// 复用 internal/seed/appearance 的结构体保证 schema 完全一致。peer_colors 从现有 JSON 沿用。
//
// 需登录(墙纸/主题接口非免登)。api 凭据用 TDesktop 开源公开的 id/hash。
// 子命令:
//
//	appearancefetch sendcode              env: PHONE              发送登录码,打印 CODE_HASH
//	appearancefetch fetch <out> <carry>   env: PHONE CODE CODE_HASH PASSWORD   登录+拉取+生成
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/iamxvbaba/td/telegram"
	"github.com/iamxvbaba/td/telegram/auth"
	"github.com/iamxvbaba/td/telegram/downloader"
	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/seed/appearance"
)

const (
	apiID   = 17349
	apiHash = "344583e45741c457fe1862106095a5eb"
)

func sessionPath() string {
	if p := os.Getenv("SESSION"); p != "" {
		return p
	}
	return "/tmp/appearance.session"
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: appearancefetch sendcode | appearancefetch fetch <out_dir> <carry_json>")
		os.Exit(2)
	}
	cmd := os.Args[1]
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	client := telegram.NewClient(apiID, apiHash, telegram.Options{
		SessionStorage: &telegram.FileSessionStorage{Path: sessionPath()},
	})
	err := client.Run(ctx, func(ctx context.Context) error {
		switch cmd {
		case "sendcode":
			return doSendCode(ctx, client)
		case "fetch":
			if len(os.Args) < 3 {
				return errors.New("fetch needs <out_dir>")
			}
			return doFetch(ctx, client, os.Args[2])
		default:
			return fmt.Errorf("unknown cmd %q", cmd)
		}
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "ERROR:", err)
		os.Exit(1)
	}
}

func doSendCode(ctx context.Context, client *telegram.Client) error {
	phone := os.Getenv("PHONE")
	if phone == "" {
		return errors.New("PHONE env required")
	}
	sent, err := client.Auth().SendCode(ctx, phone, auth.SendCodeOptions{})
	if err != nil {
		return fmt.Errorf("sendCode: %w", err)
	}
	code, ok := sent.(*tg.AuthSentCode)
	if !ok {
		return fmt.Errorf("sentCode = %T", sent)
	}
	fmt.Println("CODE_HASH=" + code.PhoneCodeHash)
	return nil
}

func doFetch(ctx context.Context, client *telegram.Client, outDir string) error {
	phone := os.Getenv("PHONE")
	code := os.Getenv("CODE")
	codeHash := os.Getenv("CODE_HASH")
	password := os.Getenv("PASSWORD")

	status, err := client.Auth().Status(ctx)
	if err != nil {
		return fmt.Errorf("auth status: %w", err)
	}
	if !status.Authorized {
		_, err := client.Auth().SignIn(ctx, phone, code, codeHash)
		if errors.Is(err, auth.ErrPasswordAuthNeeded) {
			if _, err := client.Auth().Password(ctx, password); err != nil {
				return fmt.Errorf("2FA: %w", err)
			}
		} else if err != nil {
			return fmt.Errorf("signIn: %w", err)
		}
		fmt.Println("[authorized]")
	} else {
		fmt.Println("[already authorized]")
	}

	api := client.API()
	docsDir := filepath.Join(outDir, "default_wallpapers", "documents")
	thumbsDir := filepath.Join(outDir, "default_wallpapers", "thumbs", "m")
	if err := os.MkdirAll(docsDir, 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(thumbsDir, 0o755); err != nil {
		return err
	}

	dl := downloader.NewDownloader()
	download := func(doc *tg.Document, thumb string) ([]byte, error) {
		loc := &tg.InputDocumentFileLocation{
			ID:            doc.ID,
			AccessHash:    doc.AccessHash,
			FileReference: doc.FileReference,
			ThumbSize:     thumb,
		}
		var buf bytes.Buffer
		if _, err := dl.Download(api, loc).Stream(ctx, &buf); err != nil {
			return nil, err
		}
		return buf.Bytes(), nil
	}
	// 转一个 tg.Document → appearance.Document 并下载正文 + "m" 缩略图。
	convDoc := func(dc tg.DocumentClass) (appearance.Document, error) {
		doc, ok := dc.(*tg.Document)
		if !ok || doc.ID == 0 {
			return appearance.Document{}, nil
		}
		out := appearance.Document{
			ID:         doc.ID,
			AccessHash: doc.AccessHash,
			Date:       doc.Date,
			MimeType:   doc.MimeType,
			Size:       doc.Size,
			DCID:       doc.DCID,
		}
		for _, a := range doc.Attributes {
			switch attr := a.(type) {
			case *tg.DocumentAttributeImageSize:
				out.Attributes = append(out.Attributes, appearance.DocumentAttribute{Kind: "image_size", W: attr.W, H: attr.H})
			case *tg.DocumentAttributeFilename:
				out.Attributes = append(out.Attributes, appearance.DocumentAttribute{Kind: "filename", FileName: attr.FileName})
			}
		}
		// 正文
		data, err := download(doc, "")
		if err != nil {
			return appearance.Document{}, fmt.Errorf("download doc %d: %w", doc.ID, err)
		}
		name := fmt.Sprintf("%d.dat", doc.ID)
		if err := os.WriteFile(filepath.Join(docsDir, name), data, 0o644); err != nil {
			return appearance.Document{}, err
		}
		sum := sha256.Sum256(data)
		out.Path = "default_wallpapers/documents/" + name
		out.SHA256 = hex.EncodeToString(sum[:])
		// "m" 缩略图
		for _, t := range doc.Thumbs {
			ps, ok := t.(*tg.PhotoSize)
			if !ok || ps.Type != "m" {
				continue
			}
			tdata, err := download(doc, "m")
			if err != nil {
				return appearance.Document{}, fmt.Errorf("download thumb %d: %w", doc.ID, err)
			}
			if err := os.WriteFile(filepath.Join(thumbsDir, name), tdata, 0o644); err != nil {
				return appearance.Document{}, err
			}
			tsum := sha256.Sum256(tdata)
			out.Thumbs = append(out.Thumbs, appearance.PhotoSize{
				Kind: "size", Type: "m", W: ps.W, H: ps.H, Size: ps.Size,
				Path: "default_wallpapers/thumbs/m/" + name, SHA256: hex.EncodeToString(tsum[:]),
			})
			break
		}
		return out, nil
	}

	convWallpaperSettings := func(s tg.WallPaperSettings) appearance.WallpaperSettings {
		out := appearance.WallpaperSettings{}
		if v, ok := s.GetBackgroundColor(); ok {
			out.BackgroundColor = v
		}
		if v, ok := s.GetSecondBackgroundColor(); ok {
			out.SecondBackgroundColor = v
		}
		if v, ok := s.GetThirdBackgroundColor(); ok {
			out.ThirdBackgroundColor = v
		}
		if v, ok := s.GetFourthBackgroundColor(); ok {
			out.FourthBackgroundColor = v
		}
		if v, ok := s.GetIntensity(); ok {
			out.Intensity = v
		}
		if v, ok := s.GetRotation(); ok {
			out.Rotation = v
		}
		out.Blur = s.Blur
		out.Motion = s.Motion
		return out
	}

	convWallpaper := func(wc tg.WallPaperClass) (appearance.Wallpaper, error) {
		switch wp := wc.(type) {
		case *tg.WallPaper:
			doc, err := convDoc(wp.Document)
			if err != nil {
				return appearance.Wallpaper{}, err
			}
			out := appearance.Wallpaper{
				ID: wp.ID, AccessHash: wp.AccessHash, Type: 0,
				Default: wp.Default, Pattern: wp.Pattern, Dark: wp.Dark,
				Slug: wp.Slug, Document: doc,
			}
			if s, ok := wp.GetSettings(); ok {
				out.Settings = convWallpaperSettings(s)
			}
			return out, nil
		case *tg.WallPaperNoFile:
			out := appearance.Wallpaper{ID: wp.ID, Type: 1, Default: wp.Default, Dark: wp.Dark}
			if s, ok := wp.GetSettings(); ok {
				out.Settings = convWallpaperSettings(s)
			}
			return out, nil
		}
		return appearance.Wallpaper{}, nil
	}

	// ---- 墙纸 ----
	wpRes, err := api.AccountGetWallPapers(ctx, 0)
	if err != nil {
		return fmt.Errorf("getWallPapers: %w", err)
	}
	var wallpapers []appearance.Wallpaper
	if w, ok := wpRes.(*tg.AccountWallPapers); ok {
		for _, wc := range w.Wallpapers {
			conv, err := convWallpaper(wc)
			if err != nil {
				return err
			}
			if conv.ID != 0 {
				wallpapers = append(wallpapers, conv)
			}
		}
	}
	fmt.Printf("[wallpapers] %d\n", len(wallpapers))

	// ---- 聊天主题 ----
	baseThemeName := func(b tg.BaseThemeClass) string {
		switch b.(type) {
		case *tg.BaseThemeClassic:
			return "classic"
		case *tg.BaseThemeDay:
			return "day"
		case *tg.BaseThemeNight:
			return "night"
		case *tg.BaseThemeTinted:
			return "tinted"
		case *tg.BaseThemeArctic:
			return "arctic"
		}
		return ""
	}
	var chatThemes []appearance.ChatTheme
	thRes, err := api.AccountGetChatThemes(ctx, 0)
	if err != nil {
		return fmt.Errorf("getChatThemes: %w", err)
	}
	if t, ok := thRes.(*tg.AccountThemes); ok {
		for _, theme := range t.Themes {
			ct := appearance.ChatTheme{
				ID: theme.ID, AccessHash: theme.AccessHash, Slug: theme.Slug,
				Title: theme.Title, Emoticon: theme.Emoticon, Default: theme.Default,
			}
			for _, s := range theme.Settings {
				ts := appearance.ThemeSettings{
					BaseTheme:   baseThemeName(s.BaseTheme),
					AccentColor: s.AccentColor,
				}
				if v, ok := s.GetOutboxAccentColor(); ok {
					ts.OutboxAccentColor = v
				}
				if v, ok := s.GetMessageColors(); ok {
					ts.MessageColors = v
				}
				if w, ok := s.GetWallpaper(); ok {
					wp, err := convWallpaper(w)
					if err != nil {
						return err
					}
					ts.Wallpaper = wp
				}
				ct.Settings = append(ct.Settings, ts)
			}
			chatThemes = append(chatThemes, ct)
		}
	}
	fmt.Printf("[chat_themes] %d\n", len(chatThemes))

	// ---- peer colors / profile colors 从官方拉 ----
	mapColorSet := func(c tg.HelpPeerColorSetClass) *appearance.ColorSet {
		switch s := c.(type) {
		case *tg.HelpPeerColorSet:
			return &appearance.ColorSet{PredicateName: "help.peerColorSet", Colors: append([]int(nil), s.Colors...)}
		case *tg.HelpPeerColorProfileSet:
			return &appearance.ColorSet{
				PredicateName: "help.peerColorProfileSet",
				PaletteColors: append([]int(nil), s.PaletteColors...),
				BgColors:      append([]int(nil), s.BgColors...),
				StoryColors:   append([]int(nil), s.StoryColors...),
			}
		}
		return nil
	}
	fetchColors := func(profile bool) ([]appearance.ColorOption, error) {
		var res tg.HelpPeerColorsClass
		var err error
		if profile {
			res, err = api.HelpGetPeerProfileColors(ctx, 0)
		} else {
			res, err = api.HelpGetPeerColors(ctx, 0)
		}
		if err != nil {
			return nil, err
		}
		pc, ok := res.(*tg.HelpPeerColors)
		if !ok {
			return nil, nil
		}
		out := make([]appearance.ColorOption, 0, len(pc.Colors))
		for _, o := range pc.Colors {
			co := appearance.ColorOption{ID: o.ColorID, Hidden: o.GetHidden()}
			if v, ok := o.GetChannelMinLevel(); ok {
				co.ChannelMinLevel = v
			}
			if c, ok := o.GetColors(); ok {
				co.Colors = mapColorSet(c)
			}
			if c, ok := o.GetDarkColors(); ok {
				co.DarkColors = mapColorSet(c)
			}
			out = append(out, co)
		}
		return out, nil
	}
	peerColors, err := fetchColors(false)
	if err != nil {
		return fmt.Errorf("getPeerColors: %w", err)
	}
	peerProfileColors, err := fetchColors(true)
	if err != nil {
		return fmt.Errorf("getPeerProfileColors: %w", err)
	}
	fmt.Printf("[peer_colors] %d / [peer_profile_colors] %d\n", len(peerColors), len(peerProfileColors))

	catalog := appearance.Catalog{
		Source:     "upstream appearance snapshot",
		ExportedAt: time.Now().UTC().Format(time.RFC3339),
		Notes: appearance.Notes{
			Server:                   "official",
			ChatThemesTotal:          len(chatThemes),
			WallpaperDocumentsBucket: "documents",
			WallpaperThumbsBucket:    "thumbs",
		},
		ChatThemes:        chatThemes,
		Wallpapers:        wallpapers,
		PeerColors:        peerColors,
		PeerProfileColors: peerProfileColors,
	}
	out, err := json.MarshalIndent(catalog, "", " ")
	if err != nil {
		return err
	}
	jsonPath := filepath.Join(outDir, "default_appearance_seed.json")
	if err := os.WriteFile(jsonPath, out, 0o644); err != nil {
		return err
	}
	fmt.Printf("wrote %s (wallpapers=%d chat_themes=%d peer_colors=%d profile=%d)\n",
		jsonPath, len(wallpapers), len(chatThemes), len(catalog.PeerColors), len(catalog.PeerProfileColors))
	return nil
}
