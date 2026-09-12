// Command reactionfetch 从官方 Telegram 拉取默认消息 reaction 集
// (messages.getAvailableReactions),写成 telesrv SeedMedia 可直接导入的
// seed 目录格式:
//
//	<out>/telegram_reactions_export/global_json/available_reactions_raw.json
//	<out>/telegram_reactions_export/reactions/<docid>.<ext>
//
// 字段/目录约定对齐 internal/app/files/seed.go 的 seedReactions 解析逻辑 --
// 与 stickerfetch 写 sticker set 是同一套约定(scanSeedDir 按文件名里最后一段
// 数字当 document id 建索引),reaction 只是把每个 document 内联嵌进它所属的
// reaction 对象,而不是像 sticker pack 那样只存一份 id 列表。
//
// 需登录会话(复用 appearancefetch/giftfetch 的会话文件,SESSION env 可覆盖)。
//
// 用法: SESSION=/root/giftfetch.session reactionfetch <out_dir>
package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/iamxvbaba/td/telegram"
	"github.com/iamxvbaba/td/telegram/downloader"
	"github.com/iamxvbaba/td/tg"
)

const (
	tdesktopAPIID   = 17349
	tdesktopAPIHash = "344583e45741c457fe1862106095a5eb"
)

func sessionPath() string {
	if p := os.Getenv("SESSION"); p != "" {
		return p
	}
	return "/tmp/appearance.session"
}

// ---- seed JSON 结构(字段对齐 internal/app/files/seed.go 的 seedDocumentJSON/seedReactionJSON) ----

type attrJSON struct {
	Type              string           `json:"_"`
	W                 int              `json:"w,omitempty"`
	H                 int              `json:"h,omitempty"`
	Alt               string           `json:"alt,omitempty"`
	Mask              bool             `json:"mask,omitempty"`
	Free              bool             `json:"free,omitempty"`
	TextColor         bool             `json:"text_color,omitempty"`
	FileName          string           `json:"file_name,omitempty"`
	Duration          float64          `json:"duration,omitempty"`
	RoundMessage      bool             `json:"round_message,omitempty"`
	SupportsStreaming bool             `json:"supports_streaming,omitempty"`
	Voice             bool             `json:"voice,omitempty"`
	Title             string           `json:"title,omitempty"`
	Performer         string           `json:"performer,omitempty"`
	Stickerset        *inputSetRefJSON `json:"stickerset,omitempty"`
}

type inputSetRefJSON struct {
	ID         int64 `json:"id"`
	AccessHash int64 `json:"access_hash"`
}

type documentJSON struct {
	ID            int64      `json:"id"`
	AccessHash    int64      `json:"access_hash"`
	FileReference string     `json:"file_reference"`
	Date          string     `json:"date"`
	MimeType      string     `json:"mime_type"`
	Size          int64      `json:"size"`
	DCID          int        `json:"dc_id"`
	Attributes    []attrJSON `json:"attributes"`
}

type reactionJSON struct {
	Reaction          string        `json:"reaction"`
	Title             string        `json:"title"`
	Inactive          bool          `json:"inactive"`
	Premium           bool          `json:"premium"`
	StaticIcon        *documentJSON `json:"static_icon,omitempty"`
	AppearAnimation   *documentJSON `json:"appear_animation,omitempty"`
	SelectAnimation   *documentJSON `json:"select_animation,omitempty"`
	ActivateAnimation *documentJSON `json:"activate_animation,omitempty"`
	EffectAnimation   *documentJSON `json:"effect_animation,omitempty"`
	AroundAnimation   *documentJSON `json:"around_animation,omitempty"`
	CenterIcon        *documentJSON `json:"center_icon,omitempty"`
}

type rawFileJSON struct {
	APICall string `json:"api_call"`
	Result  struct {
		Hash      int            `json:"hash"`
		Reactions []reactionJSON `json:"reactions"`
	} `json:"result"`
}

func mapAttrs(in []tg.DocumentAttributeClass) []attrJSON {
	out := make([]attrJSON, 0, len(in))
	for _, a := range in {
		switch v := a.(type) {
		case *tg.DocumentAttributeImageSize:
			out = append(out, attrJSON{Type: "DocumentAttributeImageSize", W: v.W, H: v.H})
		case *tg.DocumentAttributeAnimated:
			out = append(out, attrJSON{Type: "DocumentAttributeAnimated"})
		case *tg.DocumentAttributeSticker:
			aj := attrJSON{Type: "DocumentAttributeSticker", Alt: v.Alt, Mask: v.Mask}
			if id, ok := v.Stickerset.(*tg.InputStickerSetID); ok {
				aj.Stickerset = &inputSetRefJSON{ID: id.ID, AccessHash: id.AccessHash}
			}
			out = append(out, aj)
		case *tg.DocumentAttributeVideo:
			out = append(out, attrJSON{Type: "DocumentAttributeVideo", W: v.W, H: v.H, Duration: v.Duration, RoundMessage: v.RoundMessage, SupportsStreaming: v.SupportsStreaming})
		case *tg.DocumentAttributeFilename:
			out = append(out, attrJSON{Type: "DocumentAttributeFilename", FileName: v.FileName})
		}
	}
	return out
}

// downloadDoc 下载一个文档主体到 dir/<docid>.<ext> 并返回其 seed 元数据。
func downloadDoc(ctx context.Context, api *tg.Client, dl *downloader.Downloader, dir string, cls tg.DocumentClass) (*documentJSON, error) {
	doc, ok := cls.(*tg.Document)
	if !ok {
		return nil, nil // DocumentEmpty 或未设置
	}
	ext := ".tgs"
	switch doc.MimeType {
	case "video/webm":
		ext = ".webm"
	case "image/webp":
		ext = ".webp"
	case "image/png":
		ext = ".png"
	}
	path := filepath.Join(dir, fmt.Sprintf("%d%s", doc.ID, ext))
	if info, err := os.Stat(path); err != nil || info.Size() != doc.Size {
		tmp, err := os.CreateTemp(dir, ".download-*")
		if err != nil {
			return nil, err
		}
		tmpPath := tmp.Name()
		loc := &tg.InputDocumentFileLocation{ID: doc.ID, AccessHash: doc.AccessHash, FileReference: doc.FileReference}
		if _, err := dl.Download(api, loc).Stream(ctx, tmp); err != nil {
			tmp.Close()
			os.Remove(tmpPath)
			return nil, fmt.Errorf("download doc %d: %w", doc.ID, err)
		}
		tmp.Close()
		os.Remove(path)
		if err := os.Rename(tmpPath, path); err != nil {
			return nil, err
		}
	}
	return &documentJSON{
		ID:            doc.ID,
		AccessHash:    doc.AccessHash,
		FileReference: hex.EncodeToString(doc.FileReference),
		Date:          time.Unix(int64(doc.Date), 0).UTC().Format(time.RFC3339),
		MimeType:      doc.MimeType,
		Size:          doc.Size,
		DCID:          doc.DCID,
		Attributes:    mapAttrs(doc.Attributes),
	}, nil
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: SESSION=/root/giftfetch.session reactionfetch <out_dir>")
		os.Exit(2)
	}
	outRoot := os.Args[1]
	dir := filepath.Join(outRoot, "telegram_reactions_export")
	reactionsDir := filepath.Join(dir, "reactions")
	globalDir := filepath.Join(dir, "global_json")
	if err := os.MkdirAll(reactionsDir, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "ERROR:", err)
		os.Exit(1)
	}
	if err := os.MkdirAll(globalDir, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "ERROR:", err)
		os.Exit(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()

	client := telegram.NewClient(tdesktopAPIID, tdesktopAPIHash, telegram.Options{
		SessionStorage: &telegram.FileSessionStorage{Path: sessionPath()},
	})
	if err := client.Run(ctx, func(ctx context.Context) error {
		api := client.API()
		if status, err := client.Auth().Status(ctx); err != nil || !status.Authorized {
			return fmt.Errorf("session %s 未授权(先用 appearancefetch 登录): %v", sessionPath(), err)
		}
		dl := downloader.NewDownloader()
		res, err := api.MessagesGetAvailableReactions(ctx, 0)
		if err != nil {
			return fmt.Errorf("getAvailableReactions: %w", err)
		}
		full, ok := res.(*tg.MessagesAvailableReactions)
		if !ok {
			return fmt.Errorf("got %T, want messagesAvailableReactions", res)
		}
		var out rawFileJSON
		out.APICall = "messages.getAvailableReactions"
		out.Result.Hash = full.Hash
		fmt.Printf("reactions=%d downloading...\n", len(full.Reactions))
		for i, r := range full.Reactions {
			rj := reactionJSON{Reaction: r.Reaction, Title: r.Title, Inactive: r.Inactive, Premium: r.Premium}
			slots := []struct {
				cls tg.DocumentClass
				dst **documentJSON
			}{
				{r.StaticIcon, &rj.StaticIcon},
				{r.AppearAnimation, &rj.AppearAnimation},
				{r.SelectAnimation, &rj.SelectAnimation},
				{r.ActivateAnimation, &rj.ActivateAnimation},
				{r.EffectAnimation, &rj.EffectAnimation},
			}
			if v, ok := r.GetAroundAnimation(); ok {
				slots = append(slots, struct {
					cls tg.DocumentClass
					dst **documentJSON
				}{v, &rj.AroundAnimation})
			}
			if v, ok := r.GetCenterIcon(); ok {
				slots = append(slots, struct {
					cls tg.DocumentClass
					dst **documentJSON
				}{v, &rj.CenterIcon})
			}
			for _, slot := range slots {
				if slot.cls == nil {
					continue
				}
				dj, err := downloadDoc(ctx, api, dl, reactionsDir, slot.cls)
				if err != nil {
					return fmt.Errorf("reaction %q: %w", r.Reaction, err)
				}
				*slot.dst = dj
			}
			out.Result.Reactions = append(out.Result.Reactions, rj)
			fmt.Printf("[%d/%d] %s %q\n", i+1, len(full.Reactions), r.Reaction, r.Title)
		}
		b, err := json.MarshalIndent(out, "", "  ")
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(globalDir, "available_reactions_raw.json"), append(b, '\n'), 0o644)
	}); err != nil {
		fmt.Fprintln(os.Stderr, "ERROR:", err)
		os.Exit(1)
	}
	fmt.Println("done")
}
