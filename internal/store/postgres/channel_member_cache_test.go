package postgres

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"telesrv/internal/domain"
)

func TestChannelMemberCacheNilDisabled(t *testing.T) {
	if c := NewChannelMemberCache(0); c != nil {
		t.Fatalf("max<=0 应返回 nil(禁用)，got %v", c)
	}
	var nilCache *ChannelMemberCache
	nilCache.put(domain.ChannelMember{ChannelID: 1, UserID: 2})
	nilCache.delete(1, 2)
	nilCache.deleteChannel(1)
	nilCache.flush()
	if _, ok := nilCache.get(1, 2); ok {
		t.Fatalf("nil 缓存 get 必须返回 ok=false")
	}
}

func TestChannelMemberCachePutGetDeleteFlush(t *testing.T) {
	c := NewChannelMemberCache(16)
	member := domain.ChannelMember{
		ChannelID:      10,
		UserID:         20,
		Role:           domain.ChannelRoleAdmin,
		Status:         domain.ChannelMemberActive,
		ReadInboxMaxID: 9,
	}

	if _, ok := c.get(10, 20); ok {
		t.Fatalf("空缓存不应命中")
	}
	c.put(member)
	got, ok := c.get(10, 20)
	if !ok || got.ChannelID != 10 || got.UserID != 20 || got.Role != domain.ChannelRoleAdmin || got.ReadInboxMaxID != 9 {
		t.Fatalf("put/get 往返失败: %+v ok=%v", got, ok)
	}

	c.delete(10, 20)
	if _, ok := c.get(10, 20); ok {
		t.Fatalf("delete 后不应命中")
	}
	c.put(member)
	c.flush()
	if _, ok := c.get(10, 20); ok {
		t.Fatalf("flush 后不应命中")
	}
}

func TestChannelMemberCacheDeleteChannelAndCap(t *testing.T) {
	c := NewChannelMemberCache(2)
	c.put(domain.ChannelMember{ChannelID: 1, UserID: 10})
	c.put(domain.ChannelMember{ChannelID: 1, UserID: 11})
	c.deleteChannel(1)
	if _, ok := c.get(1, 10); ok {
		t.Fatalf("deleteChannel 应失效同频道成员")
	}
	if _, ok := c.get(1, 11); ok {
		t.Fatalf("deleteChannel 应失效同频道成员")
	}

	c.put(domain.ChannelMember{ChannelID: 2, UserID: 10})
	c.put(domain.ChannelMember{ChannelID: 2, UserID: 11})
	c.put(domain.ChannelMember{ChannelID: 2, UserID: 12}) // 超 cap=2:LRU 单条驱逐最旧的 (2,10)
	if _, ok := c.get(2, 10); ok {
		t.Fatalf("超限后最旧条目 (2,10) 应被驱逐")
	}
	if _, ok := c.get(2, 11); !ok {
		t.Fatalf("LRU 单条驱逐应保留次新条目 (2,11),证明非整表 flush")
	}
	if _, ok := c.get(2, 12); !ok {
		t.Fatalf("超限后最新写入的条目 (2,12) 应在")
	}
}

func TestChannelMemberCacheSingleflightsColdLoad(t *testing.T) {
	c := NewChannelMemberCache(16)
	var calls atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	load := func() (domain.ChannelMember, error) {
		calls.Add(1)
		once.Do(func() { close(started) })
		<-release
		return domain.ChannelMember{ChannelID: 7, UserID: 100, Status: domain.ChannelMemberActive}, nil
	}

	const goroutines = 16
	var wg sync.WaitGroup
	errs := make(chan error, goroutines)
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			member, err := c.getOrLoad(context.Background(), 7, 100, load)
			if err != nil {
				errs <- err
				return
			}
			if member.ChannelID != 7 || member.UserID != 100 || member.Status != domain.ChannelMemberActive {
				errs <- fmt.Errorf("member = %+v, want active 7/100", member)
			}
		}()
	}
	<-started
	time.Sleep(20 * time.Millisecond)
	close(release)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("load calls = %d, want 1", got)
	}
	if _, err := c.getOrLoad(context.Background(), 7, 100, load); err != nil {
		t.Fatalf("cached getOrLoad: %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("cache hit called load again: calls=%d", got)
	}
}

func TestReadModelChangeListenerInvalidatesChannelCaches(t *testing.T) {
	rows := NewChannelRowCache(16)
	members := NewChannelMemberCache(16)
	fullBots := &fakeChannelFullBotReadModelCache{}
	botMembers := &fakeChannelBotMemberReadModelCache{}
	mediaCounts := &fakeChannelMediaCountReadModelCache{}
	rows.put(domain.Channel{ID: 7, Title: "old"})
	members.put(domain.ChannelMember{ChannelID: 7, UserID: 100, Status: domain.ChannelMemberActive})
	members.put(domain.ChannelMember{ChannelID: 7, UserID: 200, Status: domain.ChannelMemberActive})
	members.put(domain.ChannelMember{ChannelID: 8, UserID: 100, Status: domain.ChannelMemberActive})

	listener := NewReadModelChangeListener("", ReadModelCacheSet{
		ChannelRows:        rows,
		ChannelMembers:     members,
		ChannelFullBots:    fullBots,
		ChannelBotMembers:  botMembers,
		ChannelMediaCounts: mediaCounts,
	}, nil)
	listener.handlePayload(`{"model":"channel_member","owner_user_id":100,"peer_type":"channel","peer_id":7,"version":2}`)
	if _, ok := members.get(7, 100); ok {
		t.Fatalf("channel_member 事件应失效指定成员")
	}
	if _, ok := members.get(7, 200); !ok {
		t.Fatalf("channel_member 事件不应失效同频道其它成员")
	}
	if got := fullBots.channelsSnapshot(); len(got) != 1 || got[0] != 7 {
		t.Fatalf("channel_member 应失效 full bot info: %+v", got)
	}
	if got := botMembers.channelsSnapshot(); len(got) != 1 || got[0] != 7 {
		t.Fatalf("channel_member 应失效 bot member ids: %+v", got)
	}
	if got := mediaCounts.viewerSnapshot(); len(got) != 1 || got[0] != [2]int64{100, 7} {
		t.Fatalf("channel_member 应失效该 viewer 的 media count: %+v", got)
	}

	listener.handlePayload(`{"model":"channel_base","owner_user_id":0,"peer_type":"channel","peer_id":7,"version":3}`)
	if _, ok := rows.get(7); ok {
		t.Fatalf("channel_base 事件应失效频道行")
	}
	if _, ok := members.get(7, 200); ok {
		t.Fatalf("channel_base 事件应失效该频道所有成员")
	}
	if _, ok := members.get(8, 100); !ok {
		t.Fatalf("channel_base 事件不应失效其它频道成员")
	}
	if got := fullBots.channelsSnapshot(); len(got) != 2 || got[1] != 7 {
		t.Fatalf("channel_base 应失效 full bot info: %+v", got)
	}
	if got := botMembers.channelsSnapshot(); len(got) != 2 || got[1] != 7 {
		t.Fatalf("channel_base 应失效 bot member ids: %+v", got)
	}

	listener.handlePayload(`{"model":"channel_participants","owner_user_id":0,"peer_type":"channel","peer_id":7,"version":4}`)
	if got := botMembers.channelsSnapshot(); len(got) != 3 || got[2] != 7 {
		t.Fatalf("channel_participants 应失效 bot member ids: %+v", got)
	}

	listener.handlePayload(`{"model":"channel_media_counts","owner_user_id":0,"peer_type":"channel","peer_id":7,"version":5}`)
	if got := mediaCounts.channelSnapshot(); len(got) != 1 || got[0] != 7 {
		t.Fatalf("channel_media_counts 应失效该频道 media count: %+v", got)
	}
}

func TestReadModelChangeListenerInvalidatesPrivateMediaCountCache(t *testing.T) {
	privateCounts := &fakePrivateMediaCountReadModelCache{}
	listener := NewReadModelChangeListener("", ReadModelCacheSet{
		PrivateMediaCounts: privateCounts,
	}, nil)

	listener.handlePayload(`{"model":"private_media_counts","owner_user_id":100,"peer_type":"user","peer_id":200,"version":2}`)
	if got := privateCounts.keysSnapshot(); len(got) != 1 || got[0] != [2]int64{100, 200} {
		t.Fatalf("private_media_counts 应失效 owner+peer media count: %+v", got)
	}
}

func TestReadModelChangeListenerInvalidatesAccountSettingsCache(t *testing.T) {
	settings := &fakeAccountSettingsReadModelCache{}
	listener := NewReadModelChangeListener("", ReadModelCacheSet{
		AccountSettings: settings,
	}, nil)

	listener.handlePayload(`{"model":"account_settings","owner_user_id":100,"peer_type":"user","peer_id":100,"version":2}`)
	if len(settings.invalidated) != 1 || settings.invalidated[0] != 100 {
		t.Fatalf("account_settings invalidations = %v, want [100]", settings.invalidated)
	}
	if len(settings.warmed) != 1 || settings.warmed[0] != 100 {
		t.Fatalf("account_settings warmups = %v, want [100]", settings.warmed)
	}

	listener.flush("test")
	if settings.flushes != 1 {
		t.Fatalf("account_settings flushes = %d, want 1", settings.flushes)
	}
}

type fakeAccountSettingsReadModelCache struct {
	invalidated []int64
	warmed      []int64
	flushes     int
}

func (f *fakeAccountSettingsReadModelCache) InvalidateAccountSettingsReadModel(userID int64) {
	f.invalidated = append(f.invalidated, userID)
}

func (f *fakeAccountSettingsReadModelCache) FlushAccountSettingsReadModel() {
	f.flushes++
}

func (f *fakeAccountSettingsReadModelCache) WarmAccountSettingsReadModel(_ context.Context, userID int64) error {
	f.warmed = append(f.warmed, userID)
	return nil
}

// TestReadModelChangeListenerBotFullFlushesChannelFullBots 回归: bot 改资料(bot_full 事件,
// 迁移 0013)须 flush channelFullBotInfoCache(否则群信息页 bot 简介/命令跨实例陈旧至 TTL);
// 普通用户的 user_base 事件不得 flush(否则该缓存形同虚设)。
func TestReadModelChangeListenerBotFullFlushesChannelFullBots(t *testing.T) {
	fullBots := &fakeChannelFullBotReadModelCache{}
	listener := NewReadModelChangeListener("", ReadModelCacheSet{
		ChannelFullBots: fullBots,
	}, nil)

	listener.handlePayload(`{"model":"bot_full","owner_user_id":1780243210,"peer_type":"user","peer_id":1780243210,"version":2}`)
	if got := fullBots.flushCount(); got != 1 {
		t.Fatalf("bot_full 应 flush channelFullBotInfoCache: flushes=%d", got)
	}

	listener.handlePayload(`{"model":"user_base","owner_user_id":500,"peer_type":"user","peer_id":500,"version":3}`)
	if got := fullBots.flushCount(); got != 1 {
		t.Fatalf("user_base(普通用户) 不应 flush channelFullBots: flushes=%d", got)
	}
}

type fakeChannelFullBotReadModelCache struct {
	mu       sync.Mutex
	channels []int64
	flushes  int
}

func (f *fakeChannelFullBotReadModelCache) InvalidateChannelFullBotInfoReadModel(channelID int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.channels = append(f.channels, channelID)
}

func (f *fakeChannelFullBotReadModelCache) FlushChannelFullBotInfoReadModel() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.flushes++
}

func (f *fakeChannelFullBotReadModelCache) channelsSnapshot() []int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]int64(nil), f.channels...)
}

func (f *fakeChannelFullBotReadModelCache) flushCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.flushes
}

type fakeChannelBotMemberReadModelCache struct {
	mu       sync.Mutex
	channels []int64
	flushes  int
}

func (f *fakeChannelBotMemberReadModelCache) InvalidateActiveBotMemberIDsReadModel(channelID int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.channels = append(f.channels, channelID)
}

func (f *fakeChannelBotMemberReadModelCache) FlushActiveBotMemberIDsReadModel() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.flushes++
}

func (f *fakeChannelBotMemberReadModelCache) channelsSnapshot() []int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]int64(nil), f.channels...)
}

type fakeChannelMediaCountReadModelCache struct {
	mu       sync.Mutex
	channels []int64
	viewers  [][2]int64
	flushes  int
}

func (f *fakeChannelMediaCountReadModelCache) InvalidateChannelMediaCountReadModel(channelID int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.channels = append(f.channels, channelID)
}

func (f *fakeChannelMediaCountReadModelCache) InvalidateChannelMediaCountReadModelForViewer(userID, channelID int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.viewers = append(f.viewers, [2]int64{userID, channelID})
}

func (f *fakeChannelMediaCountReadModelCache) FlushChannelMediaCountReadModel() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.flushes++
}

func (f *fakeChannelMediaCountReadModelCache) channelSnapshot() []int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]int64(nil), f.channels...)
}

func (f *fakeChannelMediaCountReadModelCache) viewerSnapshot() [][2]int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][2]int64(nil), f.viewers...)
}

func (f *fakeChannelMediaCountReadModelCache) flushCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.flushes
}

type fakePrivateMediaCountReadModelCache struct {
	mu      sync.Mutex
	keys    [][2]int64
	flushes int
}

func (f *fakePrivateMediaCountReadModelCache) InvalidatePrivateMediaCountReadModel(userID, peerID int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.keys = append(f.keys, [2]int64{userID, peerID})
}

func (f *fakePrivateMediaCountReadModelCache) FlushPrivateMediaCountReadModel() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.flushes++
}

func (f *fakePrivateMediaCountReadModelCache) keysSnapshot() [][2]int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][2]int64(nil), f.keys...)
}

func (f *fakePrivateMediaCountReadModelCache) flushCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.flushes
}

type fakeContactReadModelCache struct {
	mu      sync.Mutex
	ids     []int64
	flushes int
}

func (f *fakeContactReadModelCache) InvalidateViewers(ids ...int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ids = append(f.ids, ids...)
}

func (f *fakeContactReadModelCache) FlushReadModelCache() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.flushes++
}

func (f *fakeContactReadModelCache) idsSnapshot() []int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]int64(nil), f.ids...)
}

func (f *fakeContactReadModelCache) flushCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.flushes
}

type fakeDialogReadModelCache struct {
	mu      sync.Mutex
	keys    []domain.Peer
	owners  []int64
	flushes int
}

func (f *fakeDialogReadModelCache) InvalidateDialog(ownerUserID int64, peer domain.Peer) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.owners = append(f.owners, ownerUserID)
	f.keys = append(f.keys, peer)
}

func (f *fakeDialogReadModelCache) FlushReadModelCache() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.flushes++
}

func (f *fakeDialogReadModelCache) entriesSnapshot() ([]int64, []domain.Peer) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]int64(nil), f.owners...), append([]domain.Peer(nil), f.keys...)
}

func (f *fakeDialogReadModelCache) flushCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.flushes
}

type fakePrivacyReadModelCache struct {
	mu      sync.Mutex
	ids     []int64
	warmed  []int64
	flushes int
}

func (f *fakePrivacyReadModelCache) InvalidateOwners(ids ...int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ids = append(f.ids, ids...)
}

func (f *fakePrivacyReadModelCache) WarmOwners(_ context.Context, ids ...int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.warmed = append(f.warmed, ids...)
	return nil
}

func (f *fakePrivacyReadModelCache) FlushReadModelCache() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.flushes++
}

func (f *fakePrivacyReadModelCache) idsSnapshot() []int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]int64(nil), f.ids...)
}

func (f *fakePrivacyReadModelCache) warmedSnapshot() []int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]int64(nil), f.warmed...)
}

func (f *fakePrivacyReadModelCache) flushCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.flushes
}

type fakeProfilePhotoReadModelCache struct {
	mu      sync.Mutex
	owners  []domain.Peer
	flushes int
}

func (f *fakeProfilePhotoReadModelCache) InvalidateOwner(ownerType domain.PeerType, ownerID int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.owners = append(f.owners, domain.Peer{Type: ownerType, ID: ownerID})
}

func (f *fakeProfilePhotoReadModelCache) FlushReadModelCache() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.flushes++
}

func (f *fakeProfilePhotoReadModelCache) ownersSnapshot() []domain.Peer {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]domain.Peer(nil), f.owners...)
}

func (f *fakeProfilePhotoReadModelCache) flushCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.flushes
}

func TestReadModelChangeListenerInvalidatesAccountCaches(t *testing.T) {
	contacts := &fakeContactReadModelCache{}
	dialogs := &fakeDialogReadModelCache{}
	privacy := &fakePrivacyReadModelCache{}
	photos := &fakeProfilePhotoReadModelCache{}
	channelMedia := &fakeChannelMediaCountReadModelCache{}
	privateMedia := &fakePrivateMediaCountReadModelCache{}
	listener := NewReadModelChangeListener("", ReadModelCacheSet{
		Contacts:           contacts,
		Dialogs:            dialogs,
		Privacy:            privacy,
		ProfilePhotos:      photos,
		ChannelMediaCounts: channelMedia,
		PrivateMediaCounts: privateMedia,
	}, nil)

	listener.flush()
	if contacts.flushes != 1 || dialogs.flushes != 1 || privacy.flushes != 1 || photos.flushes != 1 ||
		channelMedia.flushCount() != 1 || privateMedia.flushCount() != 1 {
		t.Fatalf("flush counts contacts/dialogs/privacy/photos/channelMedia/privateMedia = %d/%d/%d/%d/%d/%d, want all 1",
			contacts.flushes, dialogs.flushes, privacy.flushes, photos.flushes, channelMedia.flushCount(), privateMedia.flushCount())
	}

	listener.handlePayload(`{"model":"contact_account","owner_user_id":11,"peer_type":"user","peer_id":11,"version":2}`)
	listener.handlePayload(`{"model":"contact_blocklist","owner_user_id":12,"peer_type":"user","peer_id":12,"version":3}`)
	if len(contacts.ids) != 2 || contacts.ids[0] != 11 || contacts.ids[1] != 12 {
		t.Fatalf("contact invalidations = %v, want [11 12]", contacts.ids)
	}

	listener.handlePayload(`{"model":"privacy_rules","owner_user_id":21,"peer_type":"user","peer_id":21,"version":4}`)
	if len(privacy.ids) != 1 || privacy.ids[0] != 21 {
		t.Fatalf("privacy invalidations = %v, want [21]", privacy.ids)
	}
	if warmed := privacy.warmedSnapshot(); len(warmed) != 1 || warmed[0] != 21 {
		t.Fatalf("privacy warms = %v, want [21]", warmed)
	}

	listener.handlePayload(`{"model":"dialog_light","owner_user_id":22,"peer_type":"user","peer_id":32,"version":4}`)
	if len(dialogs.owners) != 1 || dialogs.owners[0] != 22 || dialogs.keys[0] != (domain.Peer{Type: domain.PeerTypeUser, ID: 32}) {
		t.Fatalf("dialog invalidations owners=%v keys=%+v, want owner 22 user 32", dialogs.owners, dialogs.keys)
	}

	listener.handlePayload(`{"model":"channel_member","owner_user_id":23,"peer_type":"channel","peer_id":33,"version":5}`)
	if len(dialogs.owners) != 2 || dialogs.owners[1] != 23 || dialogs.keys[1] != (domain.Peer{Type: domain.PeerTypeChannel, ID: 33}) {
		t.Fatalf("channel member dialog invalidations owners=%v keys=%+v, want owner 23 channel 33", dialogs.owners, dialogs.keys)
	}

	listener.handlePayload(`{"model":"profile_photo","owner_user_id":0,"peer_type":"user","peer_id":31,"version":5}`)
	listener.handlePayload(`{"model":"profile_photo","owner_user_id":0,"peer_type":"channel","peer_id":41,"version":6}`)
	if len(photos.owners) != 2 ||
		photos.owners[0] != (domain.Peer{Type: domain.PeerTypeUser, ID: 31}) ||
		photos.owners[1] != (domain.Peer{Type: domain.PeerTypeChannel, ID: 41}) {
		t.Fatalf("photo invalidations = %+v, want user 31 and channel 41", photos.owners)
	}
}
