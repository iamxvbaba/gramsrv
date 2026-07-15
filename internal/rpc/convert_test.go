package rpc

import (
	"testing"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

func TestTGMessagesMessagesMarksViewerSelfAndKeepsProjectedPhone(t *testing.T) {
	const viewerID int64 = 1001
	res := tgMessagesMessages(viewerID, domain.MessageList{
		Users: []domain.User{
			{ID: viewerID, AccessHash: 11, Phone: "15550000001", FirstName: "Owner"},
			{ID: 1002, AccessHash: 22, Phone: "", FirstName: "Peer"},
		},
	})
	full, ok := res.(*tg.MessagesMessages)
	if !ok {
		t.Fatalf("result = %T, want *tg.MessagesMessages", res)
	}
	self, ok := full.Users[0].(*tg.User)
	if !ok || !self.Self || self.Phone != "15550000001" {
		t.Fatalf("self user = %+v ok=%v, want self with phone", full.Users[0], ok)
	}
	peer, ok := full.Users[1].(*tg.User)
	if !ok || peer.Self || peer.Phone != "" || peer.FirstName != "Peer" {
		t.Fatalf("peer user = %+v ok=%v, want projected non-self without phone", full.Users[1], ok)
	}
}

func TestTGMessagesDialogsIncludesUserProfilePhoto(t *testing.T) {
	const viewerID int64 = 1001
	const peerID int64 = 1002
	res := tgMessagesDialogs(viewerID, domain.DialogList{
		Dialogs: []domain.Dialog{{
			Peer:           domain.Peer{Type: domain.PeerTypeUser, ID: peerID},
			TopMessage:     1,
			TopMessageDate: 10,
		}},
		Users: []domain.User{{
			ID:            peerID,
			AccessHash:    22,
			FirstName:     "Alice A",
			PhotoID:       9301,
			PhotoDCID:     2,
			PhotoStripped: []byte{9, 10},
		}},
	})
	full, ok := res.(*tg.MessagesDialogs)
	if !ok {
		t.Fatalf("result = %T, want *tg.MessagesDialogs", res)
	}
	peer, ok := full.Users[0].(*tg.User)
	if !ok {
		t.Fatalf("user = %T, want *tg.User", full.Users[0])
	}
	photo, ok := peer.Photo.(*tg.UserProfilePhoto)
	if !ok || photo.PhotoID != 9301 || photo.DCID != 2 || string(photo.StrippedThumb) != string([]byte{9, 10}) {
		t.Fatalf("photo = %+v ok=%v, want userProfilePhoto 9301/2/[9 10]", peer.Photo, ok)
	}
}

func TestTGPhotoRejectsVideoOnlyAvatar(t *testing.T) {
	photo := tgPhoto(domain.Photo{
		ID:         9302,
		AccessHash: 7,
		DCID:       2,
		Sizes: []domain.PhotoSize{
			{Kind: domain.PhotoSizeKindVideo, Type: "u", W: 640, H: 640, Size: 1024},
			{Kind: domain.PhotoSizeKindVideoEmojiMarkup, EmojiID: 99, BackgroundColors: []int{0xffffff}},
		},
	})
	if _, ok := photo.(*tg.PhotoEmpty); !ok {
		t.Fatalf("photo = %T %+v, want PhotoEmpty for video-only avatar", photo, photo)
	}
}

func TestTGPhotoKeepsAnimatedAvatarWithStaticSizes(t *testing.T) {
	photo := tgPhoto(domain.Photo{
		ID:         9303,
		AccessHash: 7,
		DCID:       2,
		Sizes: []domain.PhotoSize{
			{Kind: domain.PhotoSizeKindDefault, Type: "a", W: 160, H: 160, Size: 1024},
			{Kind: domain.PhotoSizeKindDefault, Type: "c", W: 640, H: 640, Size: 1024},
			{Kind: domain.PhotoSizeKindVideo, Type: "u", W: 640, H: 640, Size: 2048},
			{Kind: domain.PhotoSizeKindVideoEmojiMarkup, EmojiID: 99, BackgroundColors: []int{0xffffff}},
		},
	})
	full, ok := photo.(*tg.Photo)
	if !ok {
		t.Fatalf("photo = %T %+v, want Photo", photo, photo)
	}
	if len(full.Sizes) != 2 || len(full.VideoSizes) != 2 {
		t.Fatalf("sizes = %d video_sizes = %d, want 2/2", len(full.Sizes), len(full.VideoSizes))
	}
}

func TestMessageEntitiesRoundTripAllStyledTypes(t *testing.T) {
	const viewerID int64 = 1001
	in := []tg.MessageEntityClass{
		&tg.MessageEntityBold{Offset: 0, Length: 1},
		&tg.MessageEntityItalic{Offset: 1, Length: 2},
		&tg.MessageEntityUnderline{Offset: 2, Length: 3},
		&tg.MessageEntityStrike{Offset: 3, Length: 4},
		&tg.MessageEntityCode{Offset: 4, Length: 5},
		&tg.MessageEntityPre{Offset: 5, Length: 6, Language: "go"},
		&tg.MessageEntityTextURL{Offset: 6, Length: 7, URL: "https://example.org"},
		&tg.MessageEntityMentionName{Offset: 7, Length: 8, UserID: 1002},
		&tg.InputMessageEntityMentionName{Offset: 8, Length: 9, UserID: &tg.InputUserSelf{}},
		&tg.MessageEntitySpoiler{Offset: 9, Length: 10},
		&tg.MessageEntityBlockquote{Offset: 10, Length: 11, Collapsed: true},
		&tg.MessageEntityCustomEmoji{Offset: 11, Length: 12, DocumentID: 777},
		&tg.MessageEntityMention{Offset: 12, Length: 13},
		&tg.MessageEntityHashtag{Offset: 13, Length: 14},
		&tg.MessageEntityURL{Offset: 14, Length: 15},
		&tg.MessageEntityFormattedDate{Offset: 15, Length: 16, Date: 1773436800, ShortTime: true, LongDate: true, DayOfWeek: true},
	}
	converted := domainMessageEntitiesForViewer(viewerID, in)
	if len(converted) != len(in) {
		t.Fatalf("converted %d entities, want %d: no styled entity may be dropped", len(converted), len(in))
	}
	if converted[6].URL != "https://example.org" {
		t.Fatalf("text_url URL = %q, want preserved", converted[6].URL)
	}
	if converted[7].UserID != 1002 {
		t.Fatalf("mention_name user = %d, want 1002", converted[7].UserID)
	}
	if converted[8].UserID != viewerID {
		t.Fatalf("input mention self user = %d, want viewer %d", converted[8].UserID, viewerID)
	}
	if converted[11].DocumentID != 777 {
		t.Fatalf("custom emoji document = %d, want 777", converted[11].DocumentID)
	}
	if formatted := converted[15]; formatted.Type != domain.MessageEntityFormattedDate || formatted.Date != 1773436800 || !formatted.ShortTime || !formatted.LongDate || !formatted.DayOfWeek {
		t.Fatalf("formatted date entity = %+v, want date and flags preserved", formatted)
	}
	out := tgMessageEntities(converted)
	if len(out) != len(in) {
		t.Fatalf("round-trip produced %d entities, want %d", len(out), len(in))
	}
	if quote, ok := out[10].(*tg.MessageEntityBlockquote); !ok || !quote.Collapsed {
		t.Fatalf("blockquote = %#v, want collapsed flag preserved", out[10])
	}
	if mention, ok := out[8].(*tg.MessageEntityMentionName); !ok || mention.UserID != viewerID {
		t.Fatalf("self mention round-trip = %#v, want messageEntityMentionName self", out[8])
	}
	if formatted, ok := out[15].(*tg.MessageEntityFormattedDate); !ok || formatted.Date != 1773436800 || !formatted.ShortTime || !formatted.LongDate || !formatted.DayOfWeek {
		t.Fatalf("formatted date round-trip = %#v, want date and flags preserved", out[15])
	}
}

func TestMessageEntityFormattedDatePreservesFlagsOnlyInput(t *testing.T) {
	var flags bin.Fields
	flags.Set(1) // short_time
	flags.Set(4) // long_date
	converted := domainMessageEntities([]tg.MessageEntityClass{
		&tg.MessageEntityFormattedDate{Flags: flags, Offset: 1, Length: 2, Date: 1773436800},
	})
	if len(converted) != 1 {
		t.Fatalf("converted %d entities, want one", len(converted))
	}
	if got := converted[0]; got.Type != domain.MessageEntityFormattedDate || got.Date != 1773436800 || !got.ShortTime || !got.LongDate {
		t.Fatalf("formatted date flags-only input = %+v, want short_time/long_date preserved", got)
	}
}
