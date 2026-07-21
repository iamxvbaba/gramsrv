package userprojection

import (
	"context"
	"reflect"
	"testing"

	privacyapp "telesrv/internal/app/privacy"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

func TestProjectorCombinesProfilePhotosAndViewerContacts(t *testing.T) {
	ctx := context.Background()
	const viewerID int64 = 1001
	const friendID int64 = 1002
	const strangerID int64 = 1003
	contacts := memory.NewContactStore()
	if _, err := contacts.Upsert(ctx, viewerID, domain.ContactInput{
		ContactUserID: friendID,
		Phone:         "1111",
		FirstName:     "Alice",
		LastName:      "Contact",
		Note:          "private note",
		NoteEntities:  []domain.MessageEntity{{Type: domain.MessageEntityBold, Offset: 0, Length: 7}},
	}); err != nil {
		t.Fatalf("upsert contact: %v", err)
	}
	projector := New(
		WithContactStore(contacts),
		WithPhotoProvider(fakeProfilePhotos{
			profile: map[int64]domain.ProfilePhotoRef{
				friendID:   {PhotoID: 9001, DCID: 2, Stripped: []byte{1, 2}},
				strangerID: {PhotoID: 9002, DCID: 3, Stripped: []byte{3, 4}},
			},
		}),
	)

	users, err := projector.ForViewer(ctx, viewerID, []domain.User{
		{ID: viewerID, Phone: "15550000001", FirstName: "Owner"},
		{ID: friendID, AccessHash: 22, Phone: "15550000002", FirstName: "Public", LastName: "Name"},
		{ID: strangerID, AccessHash: 33, Phone: "15550000003", FirstName: "Stranger"},
	})
	if err != nil {
		t.Fatalf("ForViewer: %v", err)
	}

	friend := projectionUser(t, users, friendID)
	if friend.FirstName != "Alice" || friend.LastName != "Contact" || friend.Phone != "1111" || !friend.Contact {
		t.Fatalf("friend projection = %+v, want contact name/phone", friend)
	}
	if friend.ContactNote != "private note" || len(friend.ContactNoteEntities) != 1 || friend.ContactNoteEntities[0].Type != domain.MessageEntityBold {
		t.Fatalf("friend contact note = %q %+v, want owner-scoped note", friend.ContactNote, friend.ContactNoteEntities)
	}
	if friend.PhotoID != 9001 || friend.PhotoDCID != 2 || string(friend.PhotoStripped) != string([]byte{1, 2}) {
		t.Fatalf("friend photo = id %d dc %d stripped %v, want 9001/2/[1 2]", friend.PhotoID, friend.PhotoDCID, friend.PhotoStripped)
	}
	stranger := projectionUser(t, users, strangerID)
	if stranger.Phone != "" || stranger.Contact || stranger.ContactNote != "" || len(stranger.ContactNoteEntities) != 0 {
		t.Fatalf("stranger projection = %+v, want hidden phone and no contact note", stranger)
	}
	if stranger.PhotoID != 9002 || stranger.PhotoDCID != 3 {
		t.Fatalf("stranger photo = id %d dc %d, want 9002/3", stranger.PhotoID, stranger.PhotoDCID)
	}
}

func TestProjectorPersonalPhotoWinsOverProfile(t *testing.T) {
	ctx := context.Background()
	const viewerID int64 = 2001
	const friendID int64 = 2002
	contacts := memory.NewContactStore()
	if _, err := contacts.Upsert(ctx, viewerID, domain.ContactInput{ContactUserID: friendID, FirstName: "Friend"}); err != nil {
		t.Fatalf("upsert contact: %v", err)
	}
	if _, _, err := contacts.SetPersonalPhoto(ctx, viewerID, friendID, 9100, 100); err != nil {
		t.Fatalf("set personal photo: %v", err)
	}
	projector := New(
		WithContactStore(contacts),
		WithPhotoProvider(fakeProfilePhotos{
			profile: map[int64]domain.ProfilePhotoRef{friendID: {PhotoID: 9001, DCID: 2}},
		}),
	)
	users, err := projector.ForViewer(ctx, viewerID, []domain.User{{ID: friendID, FirstName: "Public"}})
	if err != nil {
		t.Fatalf("ForViewer: %v", err)
	}
	friend := projectionUser(t, users, friendID)
	if friend.PhotoID != 9100 || !friend.PhotoPersonal {
		t.Fatalf("friend photo = id %d personal %v, want personal 9100", friend.PhotoID, friend.PhotoPersonal)
	}
}

func TestProjectorUsesFallbackWhenProfilePhotoHidden(t *testing.T) {
	ctx := context.Background()
	const viewerID int64 = 3001
	const ownerID int64 = 3002
	contacts := memory.NewContactStore()
	rules := memory.NewPrivacyStore()
	privacy := privacyapp.NewService(rules, contacts)
	if _, err := privacy.SetRules(ctx, ownerID, domain.PrivacyKeyProfilePhoto, []domain.PrivacyRule{{Kind: domain.PrivacyRuleDisallowAll}}); err != nil {
		t.Fatalf("set privacy: %v", err)
	}
	projector := New(
		WithContactStore(contacts),
		WithPrivacyEvaluator(privacy),
		WithPhotoProvider(fakeProfilePhotos{
			profile:  map[int64]domain.ProfilePhotoRef{ownerID: {PhotoID: 9001, DCID: 2}},
			fallback: map[int64]domain.ProfilePhotoRef{ownerID: {PhotoID: 9002, DCID: 3}},
		}),
	)
	users, err := projector.ForViewer(ctx, viewerID, []domain.User{{ID: ownerID, Phone: "15550003002", FirstName: "Owner"}})
	if err != nil {
		t.Fatalf("ForViewer: %v", err)
	}
	owner := projectionUser(t, users, ownerID)
	if owner.PhotoID != 9002 || owner.PhotoDCID != 3 || owner.Phone != "" {
		t.Fatalf("owner projection = %+v, want fallback photo and hidden phone", owner)
	}
}

// TestForViewersEquivalentToForViewer 锁定 fan-out 模板化的核心安全网：ForViewers(viewers, users)
// 的每个 viewer 切片必须与逐 viewer 的 ForViewer(viewer, users) 字节等价（隐私/改名/头像投影
// 不能因 O(owner) 模板化而漂移泄漏）。**唯一允许的差异是 personal photo overlay**：v1 模板不做
// per-viewer personal photo，故对「该 viewer 给该 owner 设过 personal photo」的对，比较前 mask 掉
// 5 个头像字段；其余对做完整字节比较。覆盖：默认规则陌生人/联系人改名+电话/status 隐藏/profile
// 头像隐藏走 fallback/self/bot/系统账号/viewer 自身也作为 owner 出现。
func TestForViewersEquivalentToForViewer(t *testing.T) {
	ctx := context.Background()
	const (
		v1  = int64(5001)
		v2  = int64(5002)
		o1  = int64(5101) // 陌生人，默认规则
		o2  = int64(5102) // v1 的联系人（改名+电话），且 v1 给 o2 设了 personal photo
		o3  = int64(5103) // status 隐藏
		o4  = int64(5104) // profile photo 隐藏 → 走 fallback
		bot = int64(5105)
	)
	viewers := []int64{v1, v2}

	contacts := memory.NewContactStore()
	rules := memory.NewPrivacyStore()
	privacy := privacyapp.NewService(rules, contacts)

	if _, err := contacts.Upsert(ctx, v1, domain.ContactInput{ContactUserID: o2, Phone: "1111", FirstName: "Alice", LastName: "Friend"}); err != nil {
		t.Fatalf("upsert contact: %v", err)
	}
	// v1 给 o2 设 personal photo（仅 v1 视角生效 → ForViewer 会带它，ForViewers v1 跳过 → 该对需 mask）。
	if _, _, err := contacts.SetPersonalPhoto(ctx, v1, o2, 9300, 300); err != nil {
		t.Fatalf("set personal photo: %v", err)
	}
	if _, err := privacy.SetRules(ctx, o3, domain.PrivacyKeyStatusTimestamp, []domain.PrivacyRule{{Kind: domain.PrivacyRuleDisallowAll}}); err != nil {
		t.Fatalf("set o3 status: %v", err)
	}
	if _, err := privacy.SetRules(ctx, o4, domain.PrivacyKeyProfilePhoto, []domain.PrivacyRule{{Kind: domain.PrivacyRuleDisallowAll}}); err != nil {
		t.Fatalf("set o4 photo: %v", err)
	}

	projector := New(
		WithContactStore(contacts),
		WithPrivacyEvaluator(privacy),
		WithPhotoProvider(fakeProfilePhotos{
			profile: map[int64]domain.ProfilePhotoRef{
				o1: {PhotoID: 9001, DCID: 1, Stripped: []byte{1}},
				o2: {PhotoID: 9002, DCID: 2, Stripped: []byte{2}},
				o3: {PhotoID: 9003, DCID: 3, Stripped: []byte{3}},
				o4: {PhotoID: 9004, DCID: 4, Stripped: []byte{4}},
				v1: {PhotoID: 9005, DCID: 5, Stripped: []byte{5}},
				v2: {PhotoID: 9006, DCID: 6, Stripped: []byte{6}},
			},
			fallback: map[int64]domain.ProfilePhotoRef{
				o4: {PhotoID: 9404, DCID: 4, Stripped: []byte{44}},
			},
		}),
	)

	users := []domain.User{
		{ID: o1, AccessHash: 11, Phone: "15550000001", FirstName: "Stranger", Status: domain.UserStatus{Kind: domain.UserStatusOnline}},
		{ID: o2, AccessHash: 12, Phone: "15550000002", FirstName: "PublicO2", Status: domain.UserStatus{Kind: domain.UserStatusOnline}},
		{ID: o3, AccessHash: 13, Phone: "15550000003", FirstName: "O3", Status: domain.UserStatus{Kind: domain.UserStatusOnline}, LastSeenAt: 123},
		{ID: o4, AccessHash: 14, Phone: "15550000004", FirstName: "O4"},
		{ID: bot, AccessHash: 15, FirstName: "Bot", Bot: true},
		{ID: domain.OfficialSystemUserID, FirstName: "System"},
		{ID: v1, AccessHash: 16, Phone: "15550000016", FirstName: "Viewer1"}, // viewer 自身也作为 owner 出现
	}

	// 哪些 (viewer, owner) 对存在 personal photo —— 比较时需 mask 头像字段（v1 模板有意跳过）。
	personalPairs := map[[2]int64]bool{{v1, o2}: true}

	batch, err := projector.ForViewers(ctx, viewers, users)
	if err != nil {
		t.Fatalf("ForViewers: %v", err)
	}
	for _, viewer := range viewers {
		want, err := projector.ForViewer(ctx, viewer, users)
		if err != nil {
			t.Fatalf("ForViewer(%d): %v", viewer, err)
		}
		got, ok := batch[viewer]
		if !ok {
			t.Fatalf("ForViewers missing viewer %d", viewer)
		}
		if len(got) != len(want) {
			t.Fatalf("viewer %d len(got)=%d len(want)=%d", viewer, len(got), len(want))
		}
		for i := range want {
			w, g := want[i], got[i]
			if w.ID != g.ID {
				t.Fatalf("viewer %d idx %d id mismatch got=%d want=%d", viewer, i, g.ID, w.ID)
			}
			if personalPairs[[2]int64{viewer, w.ID}] {
				maskPhoto(&w)
				maskPhoto(&g)
			}
			if !reflect.DeepEqual(w, g) {
				t.Fatalf("viewer %d owner %d: ForViewers != ForViewer\n got=%+v\nwant=%+v", viewer, w.ID, g, w)
			}
		}
	}
}

func maskPhoto(u *domain.User) {
	u.PhotoID = 0
	u.PhotoDCID = 0
	u.PhotoStripped = nil
	u.PhotoPersonal = false
	u.PhotoHasVideo = false
}

func projectionUser(t *testing.T, users []domain.User, id int64) domain.User {
	t.Helper()
	for _, user := range users {
		if user.ID == id {
			return user
		}
	}
	t.Fatalf("user %d not found in %+v", id, users)
	return domain.User{}
}

type fakeProfilePhotos struct {
	profile  map[int64]domain.ProfilePhotoRef
	fallback map[int64]domain.ProfilePhotoRef
}

func (p fakeProfilePhotos) CurrentProfilePhotos(_ context.Context, _ domain.PeerType, ids []int64) (map[int64]domain.ProfilePhotoRef, error) {
	return p.CurrentProfilePhotosKind(context.Background(), domain.PeerTypeUser, ids, domain.ProfilePhotoKindProfile)
}

func (p fakeProfilePhotos) CurrentProfilePhotosKind(_ context.Context, _ domain.PeerType, ids []int64, kind domain.ProfilePhotoKind) (map[int64]domain.ProfilePhotoRef, error) {
	source := p.profile
	if kind == domain.ProfilePhotoKindFallback {
		source = p.fallback
	}
	out := make(map[int64]domain.ProfilePhotoRef, len(ids))
	for _, id := range ids {
		if ref, ok := source[id]; ok {
			out[id] = ref
		}
	}
	return out, nil
}
