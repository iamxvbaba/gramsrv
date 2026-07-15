package rpc

import (
	"bytes"
	"context"
	"testing"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
)

func TestSendMediaPrivateGeoPoint(t *testing.T) {
	ctx := context.Background()
	r, owner, friend := newMediaTestRouter(t)

	point := &tg.InputGeoPoint{Lat: 39.9042, Long: 116.4074}
	point.SetAccuracyRadius(30) // gotd flag 字段必须 Set*() 置 flag 位，直接赋字段 getter 读不到
	updates, err := r.onMessagesSendMedia(WithUserID(ctx, owner.ID), &tg.MessagesSendMediaRequest{
		Peer:     &tg.InputPeerUser{UserID: friend.ID, AccessHash: friend.AccessHash},
		Media:    &tg.InputMediaGeoPoint{GeoPoint: point},
		RandomID: 2001,
	})
	if err != nil {
		t.Fatalf("sendMedia geo: %v", err)
	}
	msg := newMessageFromUpdates(t, updates)
	media, ok := msg.Media.(*tg.MessageMediaGeo)
	if !ok {
		t.Fatalf("expected MessageMediaGeo, got %T", msg.Media)
	}
	geo, ok := media.Geo.(*tg.GeoPoint)
	if !ok {
		t.Fatalf("expected tg.GeoPoint, got %T", media.Geo)
	}
	if geo.Lat != 39.9042 || geo.Long != 116.4074 {
		t.Errorf("geo = (%v, %v), want (39.9042, 116.4074)", geo.Lat, geo.Long)
	}
	if geo.AccessHash == 0 {
		t.Error("geo access_hash should be non-zero (clients echo it to upload.getWebFile)")
	}
	if radius, ok := geo.GetAccuracyRadius(); !ok || radius != 30 {
		t.Errorf("accuracy_radius = %d (%v), want 30", radius, ok)
	}
}

func TestSendMediaGeoPointOutOfRange(t *testing.T) {
	ctx := context.Background()
	r, owner, friend := newMediaTestRouter(t)

	for _, point := range []*tg.InputGeoPoint{
		{Lat: 90.5, Long: 0},
		{Lat: -91, Long: 0},
		{Lat: 0, Long: 180.01},
		{Lat: 0, Long: -181},
	} {
		_, err := r.onMessagesSendMedia(WithUserID(ctx, owner.ID), &tg.MessagesSendMediaRequest{
			Peer:     &tg.InputPeerUser{UserID: friend.ID, AccessHash: friend.AccessHash},
			Media:    &tg.InputMediaGeoPoint{GeoPoint: point},
			RandomID: 2002,
		})
		if err == nil || !tgerr.Is(err, "MEDIA_INVALID") {
			t.Errorf("geo (%v,%v): err = %v, want MEDIA_INVALID", point.Lat, point.Long, err)
		}
	}
}

func TestSendMediaGeoPointEmpty(t *testing.T) {
	ctx := context.Background()
	r, owner, friend := newMediaTestRouter(t)

	_, err := r.onMessagesSendMedia(WithUserID(ctx, owner.ID), &tg.MessagesSendMediaRequest{
		Peer:     &tg.InputPeerUser{UserID: friend.ID, AccessHash: friend.AccessHash},
		Media:    &tg.InputMediaGeoPoint{GeoPoint: &tg.InputGeoPointEmpty{}},
		RandomID: 2003,
	})
	if err == nil || !tgerr.Is(err, "MEDIA_EMPTY") {
		t.Fatalf("err = %v, want MEDIA_EMPTY", err)
	}
}

func TestSendMediaPrivateVenue(t *testing.T) {
	ctx := context.Background()
	r, owner, friend := newMediaTestRouter(t)

	updates, err := r.onMessagesSendMedia(WithUserID(ctx, owner.ID), &tg.MessagesSendMediaRequest{
		Peer: &tg.InputPeerUser{UserID: friend.ID, AccessHash: friend.AccessHash},
		Media: &tg.InputMediaVenue{
			GeoPoint:  &tg.InputGeoPoint{Lat: 31.2304, Long: 121.4737},
			Title:     "People's Square",
			Address:   "Huangpu, Shanghai",
			Provider:  "foursquare",
			VenueID:   "4b5b3c4ff964a520fd0029e3",
			VenueType: "arts_entertainment/default",
		},
		RandomID: 2004,
	})
	if err != nil {
		t.Fatalf("sendMedia venue: %v", err)
	}
	msg := newMessageFromUpdates(t, updates)
	media, ok := msg.Media.(*tg.MessageMediaVenue)
	if !ok {
		t.Fatalf("expected MessageMediaVenue, got %T", msg.Media)
	}
	if media.Title != "People's Square" || media.Address != "Huangpu, Shanghai" ||
		media.Provider != "foursquare" || media.VenueID != "4b5b3c4ff964a520fd0029e3" ||
		media.VenueType != "arts_entertainment/default" {
		t.Fatalf("venue media = %+v, want preserved venue payload", media)
	}
	geo, ok := media.Geo.(*tg.GeoPoint)
	if !ok || geo.Lat != 31.2304 || geo.Long != 121.4737 {
		t.Fatalf("venue geo = %#v, want (31.2304, 121.4737)", media.Geo)
	}
}

func TestSendMediaVenueWithoutTitle(t *testing.T) {
	ctx := context.Background()
	r, owner, friend := newMediaTestRouter(t)

	_, err := r.onMessagesSendMedia(WithUserID(ctx, owner.ID), &tg.MessagesSendMediaRequest{
		Peer: &tg.InputPeerUser{UserID: friend.ID, AccessHash: friend.AccessHash},
		Media: &tg.InputMediaVenue{
			GeoPoint: &tg.InputGeoPoint{Lat: 1, Long: 1},
			Title:    "   ",
		},
		RandomID: 2005,
	})
	if err == nil || !tgerr.Is(err, "MEDIA_EMPTY") {
		t.Fatalf("err = %v, want MEDIA_EMPTY", err)
	}
}

func TestSendMediaPrivateDiceValueRanges(t *testing.T) {
	ctx := context.Background()
	r, owner, friend := newMediaTestRouter(t)

	cases := []struct {
		emoticon string
		want     string
		max      int
	}{
		{"\U0001F3B2", "\U0001F3B2", 6},  // 🎲
		{"\U0001F3AF", "\U0001F3AF", 6},  // 🎯
		{"\U0001F3B3", "\U0001F3B3", 6},  // 🎳
		{"\U0001F3C0", "\U0001F3C0", 5},  // 🏀
		{"⚽", "⚽", 5},                    // 裸足球
		{"⚽️", "⚽", 5},                   // 带 U+FE0F 变体，须归一
		{"\U0001F3B0", "\U0001F3B0", 64}, // 🎰
	}
	randomID := int64(3000)
	for _, tc := range cases {
		// 多抽几次覆盖取值边界；value 必须始终落在 [1, max]。
		for i := 0; i < 8; i++ {
			randomID++
			updates, err := r.onMessagesSendMedia(WithUserID(ctx, owner.ID), &tg.MessagesSendMediaRequest{
				Peer:     &tg.InputPeerUser{UserID: friend.ID, AccessHash: friend.AccessHash},
				Media:    &tg.InputMediaDice{Emoticon: tc.emoticon},
				RandomID: randomID,
			})
			if err != nil {
				t.Fatalf("sendMedia dice %q: %v", tc.emoticon, err)
			}
			msg := newMessageFromUpdates(t, updates)
			media, ok := msg.Media.(*tg.MessageMediaDice)
			if !ok {
				t.Fatalf("expected MessageMediaDice, got %T", msg.Media)
			}
			if media.Emoticon != tc.want {
				t.Fatalf("dice emoticon = %q, want %q", media.Emoticon, tc.want)
			}
			if media.Value < 1 || media.Value > tc.max {
				t.Fatalf("dice %q value = %d, want in [1, %d]", tc.emoticon, media.Value, tc.max)
			}
		}
	}
}

func TestSendMediaDiceUnknownEmoticon(t *testing.T) {
	ctx := context.Background()
	r, owner, friend := newMediaTestRouter(t)

	_, err := r.onMessagesSendMedia(WithUserID(ctx, owner.ID), &tg.MessagesSendMediaRequest{
		Peer:     &tg.InputPeerUser{UserID: friend.ID, AccessHash: friend.AccessHash},
		Media:    &tg.InputMediaDice{Emoticon: "\U0001F600"},
		RandomID: 3100,
	})
	if err == nil || !tgerr.Is(err, "EMOTICON_INVALID") {
		t.Fatalf("err = %v, want EMOTICON_INVALID", err)
	}
}

func TestUploadGetWebFileGeoTile(t *testing.T) {
	ctx := context.Background()
	r, owner, _ := newMediaTestRouter(t)

	req := &tg.UploadGetWebFileRequest{
		Location: &tg.InputWebFileGeoPointLocation{
			GeoPoint:   &tg.InputGeoPoint{Lat: 39.9, Long: 116.4},
			AccessHash: 12345,
			W:          100, H: 100, Zoom: 15, Scale: 1,
		},
		Offset: 0,
		Limit:  1 << 17,
	}
	file, err := r.onUploadGetWebFile(WithUserID(ctx, owner.ID), req)
	if err != nil {
		t.Fatalf("getWebFile: %v", err)
	}
	if file.Size != 12 || len(file.Bytes) != 12 {
		t.Fatalf("size = %d bytes = %d, want 12/12 (fake tile)", file.Size, len(file.Bytes))
	}
	if file.MimeType != "image/png" {
		t.Errorf("mime = %q, want image/png", file.MimeType)
	}

	// offset 续传按字节切片，size 始终是全量大小。
	req.Offset = 4
	req.Limit = 4
	chunk, err := r.onUploadGetWebFile(WithUserID(ctx, owner.ID), req)
	if err != nil {
		t.Fatalf("getWebFile offset: %v", err)
	}
	if chunk.Size != 12 || !bytes.Equal(chunk.Bytes, []byte{0x0D, 0x0A, 0x1A, 0x0A}) {
		t.Fatalf("offset chunk = %v (size %d), want bytes 4..8 of tile", chunk.Bytes, chunk.Size)
	}

	// URL 形态当前无来源，必须显式拒绝而非静默。
	if _, err := r.onUploadGetWebFile(WithUserID(ctx, owner.ID), &tg.UploadGetWebFileRequest{
		Location: &tg.InputWebFileLocation{URL: "https://example.com/a.png", AccessHash: 1},
		Limit:    4096,
	}); err == nil || !tgerr.Is(err, "LOCATION_INVALID") {
		t.Fatalf("url webfile err = %v, want LOCATION_INVALID", err)
	}
}
