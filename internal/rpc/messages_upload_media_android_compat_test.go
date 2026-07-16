package rpc

import (
	"context"
	"testing"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap/zaptest"

	appusers "telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

// TestLegacyAndroidMessagesUploadMediaDispatch 验证 DrKLO Android 仍发出的旧版
// messages.uploadMedia 构造器 #519bc2b1 被 compat 分发到真实 onMessagesUploadMedia
// （而非落到 fallback 的 NOT_IMPLEMENTED）。这是「单附件发送正常、多附件发送失败」的
// 根因修复——相册在 sendMultiMedia 前会对每个附件先 uploadMedia，任一失败即整组 markAsError。
func TestLegacyAndroidMessagesUploadMediaDispatch(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, err := userStore.Create(ctx, domain.User{AccessHash: 61, Phone: "15550001061", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	r := New(Config{DC: 2, IP: "127.0.0.1", Port: 2398}, Deps{
		Users: appusers.NewService(userStore),
		Files: &fakeFiles{},
	}, zaptest.NewLogger(t), clock.System)

	// 按 DrKLO 字段序构造原始 wire：constructor + peer:InputPeer + media:InputMedia（均为 boxed）。
	var in bin.Buffer
	in.PutID(0x519bc2b1)
	if err := (&tg.InputPeerSelf{}).Encode(&in); err != nil {
		t.Fatalf("encode peer: %v", err)
	}
	if err := (&tg.InputMediaUploadedPhoto{File: &tg.InputFile{ID: 10, Parts: 1, Name: "a.jpg"}}).Encode(&in); err != nil {
		t.Fatalf("encode media: %v", err)
	}

	enc, err := r.Dispatch(WithUserID(androidClientContext(), owner.ID), [8]byte{}, 0, &in)
	if err != nil {
		t.Fatalf("dispatch legacy uploadMedia: %v", err)
	}
	// Routed through the generated static client overlay and sparse dispatcher.
	media, ok := enc.(*tg.MessageMediaPhoto)
	if !ok {
		t.Fatalf("response = %T, want *tg.MessageMediaPhoto", enc)
	}
	photo, ok := media.Photo.(*tg.Photo)
	if !ok || photo.ID != 777 {
		t.Fatalf("photo = %T %+v, want photo id 777", media.Photo, media.Photo)
	}
}
