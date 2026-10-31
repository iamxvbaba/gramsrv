package files

import (
	"context"
	"testing"

	"telesrv/internal/domain"
)

func hdlrBox(handler string) []byte {
	payload := make([]byte, 25)
	copy(payload[8:12], handler)
	return box("hdlr", payload)
}

func trakBox(handler string) []byte {
	return box("trak", box("mdia", append(box("mdhd", make([]byte, 24)), hdlrBox(handler)...)))
}

// silentMP4 builds ftyp + moov(trak per handler) + mdat, already faststart.
func silentMP4(handlers ...string) []byte {
	var traks []byte
	for _, handler := range handlers {
		traks = append(traks, trakBox(handler)...)
	}
	out := box("ftyp", []byte("isom\x00\x00\x02\x00"))
	out = append(out, box("moov", traks)...)
	return append(out, box("mdat", []byte("frames"))...)
}

func moovOf(t *testing.T, data []byte) []byte {
	t.Helper()
	boxes, ok := parseTopLevelBoxes(data)
	if !ok {
		t.Fatal("fixture is not a valid MP4")
	}
	for _, b := range boxes {
		if b.typ == "moov" {
			return data[b.start:b.end]
		}
	}
	t.Fatal("fixture has no moov")
	return nil
}

func TestMP4TrackKinds(t *testing.T) {
	tests := []struct {
		name                  string
		data                  []byte
		video, audio, wantsOK bool
	}{
		{name: "video only", data: silentMP4("vide"), video: true, wantsOK: true},
		{name: "video and audio", data: silentMP4("vide", "soun"), video: true, audio: true, wantsOK: true},
		{name: "audio only", data: silentMP4("soun"), audio: true, wantsOK: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			video, audio, ok := mp4TrackKinds(moovOf(t, tc.data))
			if video != tc.video || audio != tc.audio || ok != tc.wantsOK {
				t.Fatalf("video=%v audio=%v ok=%v", video, audio, ok)
			}
		})
	}
	moov := moovOf(t, silentMP4("vide"))
	if _, _, ok := mp4TrackKinds(moov[:len(moov)-3]); ok {
		t.Fatal("a truncated moov must not be classified")
	}
}

func TestCreateDocumentFromUploadSilentMP4BecomesGIF(t *testing.T) {
	video := domain.DocumentAttribute{Kind: domain.DocAttrVideo, W: 320, H: 240, Duration: 3}
	round := video
	round.RoundMessage = true
	tests := []struct {
		name    string
		data    []byte
		spec    domain.DocumentSpec
		wantGIF bool
	}{
		{name: "silent video is a GIF", data: silentMP4("vide"), spec: domain.DocumentSpec{MimeType: "video/mp4", Attributes: []domain.DocumentAttribute{video}}, wantGIF: true},
		{name: "nosound_video keeps a video", data: silentMP4("vide"), spec: domain.DocumentSpec{MimeType: "video/mp4", NosoundVideo: true, Attributes: []domain.DocumentAttribute{video}}},
		{name: "audio track keeps a video", data: silentMP4("vide", "soun"), spec: domain.DocumentSpec{MimeType: "video/mp4", Attributes: []domain.DocumentAttribute{video}}},
		{name: "round video stays round", data: silentMP4("vide"), spec: domain.DocumentSpec{MimeType: "video/mp4", Attributes: []domain.DocumentAttribute{round}}},
		{name: "force file stays a file", data: silentMP4("vide"), spec: domain.DocumentSpec{MimeType: "video/mp4", ForceFile: true, Attributes: []domain.DocumentAttribute{video}}},
	}
	for i, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			blobs, err := NewLocalFS(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			svc := NewService(newFakeMediaStore(), blobs, 2, WithVideoThumbnailer(nil))
			fileID := int64(950 + i)
			if _, err := svc.SaveFilePart(ctx, 10, fileID, 0, tc.data); err != nil {
				t.Fatal(err)
			}
			doc, err := svc.CreateDocumentFromUpload(ctx, domain.UploadedFileRef{OwnerUserID: 10, FileID: fileID, Parts: 1}, tc.spec)
			if err != nil {
				t.Fatal(err)
			}
			if doc.IsGif() != tc.wantGIF {
				t.Fatalf("IsGif=%v attributes=%+v", doc.IsGif(), doc.Attributes)
			}
			if tc.wantGIF {
				for _, attr := range doc.Attributes {
					if attr.Kind == domain.DocAttrVideo && !attr.NoSound {
						t.Fatalf("GIF video attribute must be silent: %+v", attr)
					}
				}
			}
		})
	}
}

func TestCreateDocumentFromBytesSilentMP4BecomesGIF(t *testing.T) {
	blobs, err := NewLocalFS(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	svc := NewService(newFakeMediaStore(), blobs, 2, WithVideoThumbnailer(nil))
	doc, err := svc.CreateDocumentFromBytes(context.Background(), silentMP4("vide"), domain.DocumentSpec{
		MimeType:   "video/mp4",
		Attributes: []domain.DocumentAttribute{{Kind: domain.DocAttrVideo, W: 320, H: 240, Duration: 3}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !doc.IsGif() {
		t.Fatalf("attributes=%+v, want a GIF", doc.Attributes)
	}
}
