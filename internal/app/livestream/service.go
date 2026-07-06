// Package livestream 实现频道 RTMP 直播的媒体面：RTMP ingest（OBS 推流）→
// ffmpeg 转码按秒切段 → tgcalls broadcast part 内存 ring → 观众经
// upload.getFile(inputGroupCallStream) 拉流。信令面（groupCall/participants）
// 仍归 app/groupcalls；本包只认 stream key ↔ channelID 绑定。
//
// 定位：dev 主路径（单实例、内存 ring、无 CDN/多码率），
// 生产级转码集群与分发留后续任务（见 docs/voip-module.md 直播小节）。
package livestream

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"time"

	rtmp "github.com/yutopp/go-rtmp"
	"go.uber.org/zap"

	"telesrv/internal/domain"
)

var errPublishRejected = errors.New("livestream: publish rejected")

// KeyResolver 校验 RTMP 推流密钥并返回其绑定的 channelID（app/groupcalls 实现）。
type KeyResolver interface {
	VerifyRtmpStreamKey(ctx context.Context, key string) (channelID int64, ok bool, err error)
}

// Config 是直播媒体面配置。
type Config struct {
	// ListenAddr 是 RTMP ingest 监听地址（如 ":2400"）。
	ListenAddr string
	// FFmpegPath 是 ffmpeg 可执行文件路径（默认 "ffmpeg"，走 PATH）。
	FFmpegPath string
	// WorkDir 是切段临时目录（默认系统临时目录）。
	WorkDir string
	// SegmentKeep 是每路流内存保留的 segment 数（秒），默认 32。
	SegmentKeep int
}

// Service 管理全部活跃推流会话，并向 rpc 层提供拉流查询。
type Service struct {
	cfg  Config
	keys KeyResolver
	log  *zap.Logger

	mu       sync.Mutex
	streams  map[int64]*stream // channelID → 活跃流
	listener net.Listener
}

// NewService 创建直播服务（不监听；Start 启动 ingest）。
func NewService(cfg Config, keys KeyResolver, log *zap.Logger) *Service {
	if cfg.FFmpegPath == "" {
		cfg.FFmpegPath = "ffmpeg"
	}
	if cfg.SegmentKeep <= 0 {
		cfg.SegmentKeep = 32
	}
	if cfg.WorkDir == "" {
		cfg.WorkDir = os.TempDir()
	}
	return &Service{cfg: cfg, keys: keys, log: log, streams: make(map[int64]*stream)}
}

// Start 启动 RTMP ingest 监听。
func (s *Service) Start() error {
	ln, err := net.Listen("tcp", s.cfg.ListenAddr)
	if err != nil {
		return fmt.Errorf("livestream: listen rtmp %s: %w", s.cfg.ListenAddr, err)
	}
	s.mu.Lock()
	s.listener = ln
	s.mu.Unlock()
	srv := rtmp.NewServer(&rtmp.ServerConfig{
		OnConnect: func(conn net.Conn) (io.ReadWriteCloser, *rtmp.ConnConfig) {
			return conn, &rtmp.ConnConfig{
				Handler: &rtmpHandler{svc: s, conn: conn},
				ControlState: rtmp.StreamControlStateConfig{
					DefaultBandwidthWindowSize: 6 * 1024 * 1024 / 8,
				},
			}
		},
	})
	go func() {
		if err := srv.Serve(ln); err != nil {
			s.log.Warn("rtmp server exited", zap.Error(err))
		}
	}()
	s.log.Info("live stream rtmp ingest listening", zap.String("addr", s.cfg.ListenAddr))
	return nil
}

// Close 停止监听并结束全部推流。
func (s *Service) Close() {
	s.mu.Lock()
	ln := s.listener
	streams := make([]*stream, 0, len(s.streams))
	for _, st := range s.streams {
		streams = append(streams, st)
	}
	s.streams = make(map[int64]*stream)
	s.mu.Unlock()
	if ln != nil {
		_ = ln.Close()
	}
	for _, st := range streams {
		st.stop()
	}
}

// startPublish 鉴权 stream key 并建立一路新流；同 channel 已有活跃流时顶掉旧流
// （OBS 断线重连的自然语义）。
func (s *Service) startPublish(ctx context.Context, key string, conn net.Conn) (*stream, error) {
	channelID, ok, err := s.keys.VerifyRtmpStreamKey(ctx, key)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("livestream: bad stream key")
	}
	st, err := newStream(channelID, s.cfg.FFmpegPath, s.cfg.WorkDir, s.cfg.SegmentKeep, conn,
		func() int64 { return time.Now().UnixMilli() }, s.log)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	old := s.streams[channelID]
	s.streams[channelID] = st
	s.mu.Unlock()
	if old != nil {
		old.stop()
	}
	s.log.Info("live stream publish started", zap.Int64("channel_id", channelID),
		zap.String("remote", conn.RemoteAddr().String()))
	return st, nil
}

// endPublish 在推流连接断开时收尾（仅当它仍是当前流时移除）。
func (s *Service) endPublish(st *stream) {
	s.mu.Lock()
	if s.streams[st.channelID] == st {
		delete(s.streams, st.channelID)
	}
	s.mu.Unlock()
	st.stop()
	s.log.Info("live stream publish ended", zap.Int64("channel_id", st.channelID))
}

// StreamChannels 返回 channel 当前直播时间轴；无活跃推流返回空。
func (s *Service) StreamChannels(channelID int64) []domain.LiveStreamChannel {
	s.mu.Lock()
	st := s.streams[channelID]
	s.mu.Unlock()
	if st == nil || !st.active() {
		return nil
	}
	return st.channels()
}

// StreamPart 按 time_ms 取打包好的 broadcast part（仅 scale 0）。
func (s *Service) StreamPart(channelID int64, timeMs int64, scale int) ([]byte, error) {
	if scale != 0 {
		return nil, domain.ErrLiveStreamPartExpired
	}
	s.mu.Lock()
	st := s.streams[channelID]
	s.mu.Unlock()
	if st == nil {
		return nil, domain.ErrLiveStreamNoStream
	}
	return st.part(timeMs)
}

// DropChannel 断开该 channel 的推流会话并清空缓冲（discard 直播 / revoke key）。
func (s *Service) DropChannel(channelID int64) {
	s.mu.Lock()
	st := s.streams[channelID]
	delete(s.streams, channelID)
	s.mu.Unlock()
	if st != nil {
		st.stop()
	}
}
