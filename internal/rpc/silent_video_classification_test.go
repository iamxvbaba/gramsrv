package rpc

import (
	"context"
	"testing"

	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap/zaptest"

	"telesrv/internal/domain"
)

// The files service stores a silent MP4 without nosound_video as a GIF, as
// the official server does for message sends. These paths must keep a video
// a video, so they set nosound_video themselves.

func silentVideoUpload() *tg.InputMediaUploadedDocument {
	return &tg.InputMediaUploadedDocument{
		File:       &tg.InputFile{ID: 7101, Parts: 1, Name: "clip.mp4"},
		MimeType:   "video/mp4",
		Attributes: []tg.DocumentAttributeClass{&tg.DocumentAttributeVideo{W: 720, H: 1280, Duration: 3}},
	}
}

func TestStoryUploadKeepsSilentVideoAVideo(t *testing.T) {
	original := silentVideoUpload()
	marked, ok := storyInputMediaKeepsVideo(original).(*tg.InputMediaUploadedDocument)
	if !ok || !marked.NosoundVideo {
		t.Fatalf("story upload = %#v, want nosound_video", marked)
	}
	if original.NosoundVideo {
		t.Fatal("the caller's input media was modified")
	}

	files := &fakeFiles{}
	r := New(Config{}, Deps{Files: files}, zaptest.NewLogger(t), clock.System)
	if _, err := r.resolveStoryInputMedia(context.Background(), 42, original); err != nil {
		t.Fatalf("resolve story media: %v", err)
	}
	if !files.lastDocumentSpec.NosoundVideo {
		t.Fatalf("story document spec = %+v, want nosound_video", files.lastDocumentSpec)
	}
}

func TestBotAPIVideoKeepsSilentVideoAVideo(t *testing.T) {
	files := &fakeFiles{}
	r := New(Config{}, Deps{Files: files}, zaptest.NewLogger(t), clock.System)
	input := domain.BotAPIFileInput{Bytes: []byte("mp4"), MimeType: "video/mp4", FileName: "clip.mp4"}
	for kind, want := range map[string]bool{"video": true, "animation": false} {
		if _, err := r.botAPIDocumentMedia(context.Background(), 1250000001, kind, input); err != nil {
			t.Fatalf("botAPIDocumentMedia(%s): %v", kind, err)
		}
		if files.lastDocumentSpec.NosoundVideo != want {
			t.Fatalf("%s document spec nosound_video = %v, want %v", kind, files.lastDocumentSpec.NosoundVideo, want)
		}
	}
}

func TestInlineVideoResultKeepsSilentVideoAVideo(t *testing.T) {
	document := domain.BotInlineWebDocument{MimeType: "video/mp4"}
	if spec := inlineExternalContentDocumentSpec("video", document); !spec.NosoundVideo {
		t.Fatalf("inline video spec = %+v, want nosound_video", spec)
	}
	if spec := inlineExternalContentDocumentSpec("gif", document); spec.NosoundVideo {
		t.Fatalf("inline gif spec = %+v, want no nosound_video", spec)
	}
}
