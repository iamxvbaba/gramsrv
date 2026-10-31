// Package livestream implements the media plane for channel RTMP live streaming:
// RTMP ingest (OBS pushes) → ffmpeg transcodes and splits into one-second segments →
// an in-memory ring of tgcalls broadcast parts → viewers pull via
// upload.getFile(inputGroupCallStream). The signaling plane (groupCall/participants)
// still belongs to app/groupcalls; this package only knows the stream key ↔ channelID
// binding.
//
// Positioning: dev main path (single instance, in-memory ring, no CDN/multi-bitrate);
// production-grade transcode clusters and distribution are left for follow-up tasks
// (see the live-stream section of docs/voip-module.md).
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

// KeyResolver validates the RTMP stream key and returns the channelID it is bound to (implemented by app/groupcalls).
type KeyResolver interface {
	VerifyRtmpStreamKey(ctx context.Context, key string) (channelID int64, ok bool, err error)
}

// Config is the live-stream media plane configuration.
type Config struct {
	// ListenAddr is the RTMP ingest listen address (e.g. ":2400").
	ListenAddr string
	// FFmpegPath is the ffmpeg executable path (default "ffmpeg", resolved via PATH).
	FFmpegPath string
	// WorkDir is the segmenting temp directory (defaults to the system temp directory).
	WorkDir string
	// SegmentKeep is the number of segments (seconds) kept in memory per stream, default 32.
	SegmentKeep int
}

// Service manages all active publish sessions and provides pull queries to the rpc layer.
type Service struct {
	cfg  Config
	keys KeyResolver
	log  *zap.Logger

	mu       sync.Mutex
	streams  map[int64]*stream // channelID → active stream
	listener net.Listener
}

// NewService creates the live-stream service (does not listen; Start begins ingest).
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

// Start begins the RTMP ingest listener.
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

// Close stops listening and ends all publishing.
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

// startPublish authenticates the stream key and establishes a new stream; when the same
// channel already has an active stream the old one is displaced (the natural semantics of OBS
// reconnect).
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

// endPublish cleans up when the publish connection drops (removes it only if it is still the current stream).
func (s *Service) endPublish(st *stream) {
	s.mu.Lock()
	if s.streams[st.channelID] == st {
		delete(s.streams, st.channelID)
	}
	s.mu.Unlock()
	st.stop()
	s.log.Info("live stream publish ended", zap.Int64("channel_id", st.channelID))
}

// StreamChannels returns the channel's current live timeline; empty when there is no active stream.
func (s *Service) StreamChannels(channelID int64) []domain.LiveStreamChannel {
	s.mu.Lock()
	st := s.streams[channelID]
	s.mu.Unlock()
	if st == nil || !st.active() {
		return nil
	}
	return st.channels()
}

// StreamPart fetches the packed broadcast part for a time_ms (scale 0 only).
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

// DropChannel disconnects the channel's publish session and clears the buffer (discard live / revoke key).
func (s *Service) DropChannel(channelID int64) {
	s.mu.Lock()
	st := s.streams[channelID]
	delete(s.streams, channelID)
	s.mu.Unlock()
	if st != nil {
		st.stop()
	}
}
