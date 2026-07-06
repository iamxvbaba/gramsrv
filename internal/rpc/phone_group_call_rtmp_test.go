package rpc

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"go.uber.org/zap/zaptest"

	appchannels "telesrv/internal/app/channels"
	appgroupcalls "telesrv/internal/app/groupcalls"
	appusers "telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

// fakeLiveStreams 是 LiveStreamsService 的测试替身，按 channelID 存可拉流段。
type fakeLiveStreams struct {
	channels map[int64][]domain.LiveStreamChannel
	parts    map[int64]map[int64][]byte // channelID → time_ms → part
	dropped  map[int64]bool
}

func newFakeLiveStreams() *fakeLiveStreams {
	return &fakeLiveStreams{
		channels: map[int64][]domain.LiveStreamChannel{},
		parts:    map[int64]map[int64][]byte{},
		dropped:  map[int64]bool{},
	}
}

func (f *fakeLiveStreams) StreamChannels(channelID int64) []domain.LiveStreamChannel {
	return f.channels[channelID]
}

func (f *fakeLiveStreams) StreamPart(channelID int64, timeMs int64, scale int) ([]byte, error) {
	if scale != 0 {
		return nil, domain.ErrLiveStreamPartExpired
	}
	byTime, ok := f.parts[channelID]
	if !ok {
		return nil, domain.ErrLiveStreamNoStream
	}
	part, ok := byTime[timeMs]
	if !ok {
		return nil, domain.ErrLiveStreamPartNotReady
	}
	return part, nil
}

func (f *fakeLiveStreams) DropChannel(channelID int64) { f.dropped[channelID] = true }

type rtmpFixture struct {
	*groupCallFixture
	live *fakeLiveStreams
}

// newRtmpFixture 复制 newGroupCallFixture 的用户/频道搭建，但注入 LiveStreams 替身。
func newRtmpFixture(t *testing.T) *rtmpFixture {
	t.Helper()
	ctx := t.Context()
	userStore := memory.NewUserStore()
	channelStore := memory.NewChannelStore()
	sessions := &groupCallSessions{}
	clk := &phoneTestClock{now: time.Unix(1_700_000_000, 0)}
	live := newFakeLiveStreams()
	router := New(Config{GroupCallMaxParticipants: 8, IP: "203.0.113.7"}, Deps{
		Users:       appusers.NewService(userStore),
		Channels:    appchannels.NewService(channelStore),
		GroupCalls:  appgroupcalls.NewService(memory.NewGroupCallStore()),
		LiveStreams: live,
		Sessions:    sessions,
	}, zaptest.NewLogger(t), clk)
	f := &groupCallFixture{t: t, ctx: ctx, router: router, sessions: sessions, clk: clk}
	mk := func(hash int64, phone, name string) domain.User {
		u, err := userStore.Create(ctx, domain.User{AccessHash: hash, Phone: phone, FirstName: name})
		if err != nil {
			t.Fatalf("create user %s: %v", name, err)
		}
		return u
	}
	f.owner = mk(2001, "13900000001", "Owner")
	f.member = mk(2002, "13900000002", "Member")
	f.outsider = mk(2003, "13900000003", "Outsider")
	created, err := router.onMessagesCreateChat(f.userCtx(f.owner, 11), &tg.MessagesCreateChatRequest{
		Users: []tg.InputUserClass{&tg.InputUser{UserID: f.member.ID, AccessHash: f.member.AccessHash}},
		Title: "live room",
	})
	if err != nil {
		t.Fatalf("create chat: %v", err)
	}
	for _, chat := range created.Updates.(*tg.Updates).Chats {
		if ch, ok := chat.(*tg.Channel); ok {
			f.channel = ch
			break
		}
	}
	if f.channel == nil {
		t.Fatalf("no channel in create chat result")
	}
	f.sessions.online = []int64{f.owner.ID, f.member.ID}
	f.sessions.reset()
	return &rtmpFixture{groupCallFixture: f, live: live}
}

func (f *rtmpFixture) createLive(t *testing.T) *tg.GroupCall {
	t.Helper()
	res, err := f.router.onPhoneCreateGroupCall(f.userCtx(f.owner, 11), &tg.PhoneCreateGroupCallRequest{
		Peer:       &tg.InputPeerChannel{ChannelID: f.channel.ID, AccessHash: f.channel.AccessHash},
		RandomID:   1,
		RtmpStream: true,
	})
	if err != nil {
		t.Fatalf("createGroupCall(rtmp): %v", err)
	}
	call := findUpdate[*tg.UpdateGroupCall](t, res).Call.(*tg.GroupCall)
	if !call.RtmpStream {
		t.Fatalf("created group call missing rtmp_stream flag: %+v", call)
	}
	if _, ok := call.GetStreamDCID(); !ok {
		t.Fatalf("rtmp group call missing stream_dc_id")
	}
	return call
}

// TestRtmpCreateAndAdminGetUrl 验证 RTMP 直播创建后管理员可取 url/key，revoke 轮换 key。
func TestRtmpCreateAndAdminGetUrl(t *testing.T) {
	f := newRtmpFixture(t)
	ownerCtx := f.userCtx(f.owner, 11)
	f.createLive(t)

	res, err := f.router.onPhoneGetGroupCallStreamRtmpURL(ownerCtx, &tg.PhoneGetGroupCallStreamRtmpURLRequest{
		Peer: &tg.InputPeerChannel{ChannelID: f.channel.ID, AccessHash: f.channel.AccessHash},
	})
	if err != nil {
		t.Fatalf("getGroupCallStreamRtmpUrl: %v", err)
	}
	if res.URL == "" || res.Key == "" {
		t.Fatalf("empty rtmp url/key: %+v", res)
	}
	key1 := res.Key

	// 非管理员不得取推流凭据。
	if _, err := f.router.onPhoneGetGroupCallStreamRtmpURL(f.userCtx(f.member, 22), &tg.PhoneGetGroupCallStreamRtmpURLRequest{
		Peer: &tg.InputPeerChannel{ChannelID: f.channel.ID, AccessHash: f.channel.AccessHash},
	}); err == nil {
		t.Fatalf("non-admin got rtmp url without error")
	}

	// revoke 轮换 key 并断开推流会话。
	res2, err := f.router.onPhoneGetGroupCallStreamRtmpURL(ownerCtx, &tg.PhoneGetGroupCallStreamRtmpURLRequest{
		Peer:   &tg.InputPeerChannel{ChannelID: f.channel.ID, AccessHash: f.channel.AccessHash},
		Revoke: true,
	})
	if err != nil {
		t.Fatalf("getGroupCallStreamRtmpUrl(revoke): %v", err)
	}
	if res2.Key == key1 {
		t.Fatalf("revoke did not rotate key")
	}
	if !f.live.dropped[f.channel.ID] {
		t.Fatalf("revoke did not drop live stream channel")
	}
}

// TestRtmpJoinReturnsStreamParams 验证 RTMP viewer join 返回 stream:true 的 connection params，
// 管理员附带 rtmp_stream_url/key，普通观众不下发凭据。
func TestRtmpJoinReturnsStreamParams(t *testing.T) {
	f := newRtmpFixture(t)
	call := f.createLive(t)

	// 管理员 join：带 url/key。
	ownerRes, err := f.router.onPhoneJoinGroupCall(f.userCtx(f.owner, 12), &tg.PhoneJoinGroupCallRequest{
		Call:   &tg.InputGroupCall{ID: call.ID, AccessHash: call.AccessHash},
		JoinAs: &tg.InputPeerSelf{},
		Params: groupCallJoinParams(t, 5001),
	})
	if err != nil {
		t.Fatalf("owner joinGroupCall(rtmp): %v", err)
	}
	conn := findUpdate[*tg.UpdateGroupCallConnection](t, ownerRes)
	var params rtmpConnectionParams
	if err := json.Unmarshal([]byte(conn.Params.Data), &params); err != nil {
		t.Fatalf("parse connection params: %v", err)
	}
	if !params.Stream || !params.Rtmp {
		t.Fatalf("owner connection params not stream/rtmp: %+v", params)
	}
	if params.RtmpStreamURL == "" || params.RtmpStreamKey == "" {
		t.Fatalf("admin join missing rtmp url/key: %+v", params)
	}

	// 普通成员 join：stream:true 但无凭据。
	memberRes, err := f.router.onPhoneJoinGroupCall(f.userCtx(f.member, 22), &tg.PhoneJoinGroupCallRequest{
		Call:   &tg.InputGroupCall{ID: call.ID, AccessHash: call.AccessHash},
		JoinAs: &tg.InputPeerSelf{},
		Params: groupCallJoinParams(t, 5002),
	})
	if err != nil {
		t.Fatalf("member joinGroupCall(rtmp): %v", err)
	}
	connM := findUpdate[*tg.UpdateGroupCallConnection](t, memberRes)
	var paramsM rtmpConnectionParams
	if err := json.Unmarshal([]byte(connM.Params.Data), &paramsM); err != nil {
		t.Fatalf("parse member connection params: %v", err)
	}
	if !paramsM.Stream || !paramsM.Rtmp {
		t.Fatalf("member connection params not stream/rtmp: %+v", paramsM)
	}
	if paramsM.RtmpStreamURL != "" || paramsM.RtmpStreamKey != "" {
		t.Fatalf("non-admin join leaked rtmp url/key: %+v", paramsM)
	}
}

// TestRtmpGetStreamPart 验证 upload.getFile(inputGroupCallStream) 的取段与错误映射。
func TestRtmpGetStreamPart(t *testing.T) {
	f := newRtmpFixture(t)
	call := f.createLive(t)
	memberCtx := f.userCtx(f.member, 22)
	// 观众须先 join（拉流校验 join 状态经 scope）。
	if _, err := f.router.onPhoneJoinGroupCall(memberCtx, &tg.PhoneJoinGroupCallRequest{
		Call:   &tg.InputGroupCall{ID: call.ID, AccessHash: call.AccessHash},
		JoinAs: &tg.InputPeerSelf{},
		Params: groupCallJoinParams(t, 6001),
	}); err != nil {
		t.Fatalf("member join: %v", err)
	}
	// 备好一段可拉数据。
	f.live.channels[f.channel.ID] = []domain.LiveStreamChannel{{Channel: 1, Scale: 0, LastTimestampMs: 3000}}
	f.live.parts[f.channel.ID] = map[int64][]byte{3000: []byte("SEGMENT-BYTES-0123456789")}

	loc := func(timeMs int64) *tg.InputGroupCallStream {
		return &tg.InputGroupCallStream{Call: &tg.InputGroupCall{ID: call.ID, AccessHash: call.AccessHash}, TimeMs: timeMs, Scale: 0}
	}

	// 命中：分片切片。
	out, err := f.router.onUploadGetFile(memberCtx, &tg.UploadGetFileRequest{Location: loc(3000), Offset: 0, Limit: 8})
	if err != nil {
		t.Fatalf("getFile stream: %v", err)
	}
	uf := out.(*tg.UploadFile)
	if string(uf.Bytes) != "SEGMENT-" {
		t.Fatalf("stream chunk = %q, want first 8 bytes", uf.Bytes)
	}
	// 续段（offset 中段）。
	out2, _ := f.router.onUploadGetFile(memberCtx, &tg.UploadGetFileRequest{Location: loc(3000), Offset: 8, Limit: 1 << 17})
	if string(out2.(*tg.UploadFile).Bytes) != "BYTES-0123456789" {
		t.Fatalf("stream chunk tail = %q", out2.(*tg.UploadFile).Bytes)
	}

	// 未就绪段 → TIME_TOO_BIG。
	if _, err := f.router.onUploadGetFile(memberCtx, &tg.UploadGetFileRequest{Location: loc(4000), Offset: 0, Limit: 1024}); err == nil {
		t.Fatalf("expected TIME_TOO_BIG for future segment")
	} else {
		assertPhoneRPCErr(t, err, "TIME_TOO_BIG")
	}

	// getGroupCallStreamChannels 返回时间轴。
	chRes, err := f.router.onPhoneGetGroupCallStreamChannels(memberCtx, &tg.InputGroupCall{ID: call.ID, AccessHash: call.AccessHash})
	if err != nil {
		t.Fatalf("getGroupCallStreamChannels: %v", err)
	}
	if len(chRes.Channels) != 1 || chRes.Channels[0].LastTimestampMs != 3000 {
		t.Fatalf("stream channels = %+v", chRes.Channels)
	}
}
