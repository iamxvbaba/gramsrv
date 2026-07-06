package livestream

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"

	"go.uber.org/zap"

	"telesrv/internal/domain"
)

// segmentDurationMs 是 broadcast part 的固定时长。tgcalls StreamingMediaContext
// 写死 _segmentDuration=1000（scale 0），时间轴推进按 +1000 走，segment 必须严格
// 1 秒切齐（转码强制每秒关键帧保证切点）。
const segmentDurationMs = 1000

// minSegmentsBeforeAnnounce：客户端拿到 last_timestamp_ms 后从 last-2000 开始拉，
// 至少积 3 段再对外公布时间轴，避免起播即请求不存在的负偏移段。
const minSegmentsBeforeAnnounce = 3

// stream 是一路活跃 RTMP 推流：FLV 入 ffmpeg（转码+按秒切 MPEG-TS）→ 打包 part
// 入内存 ring。时间轴：T0 取首段完成时刻向下取整秒，第 i 段的 time_ms = T0+i*1000。
type stream struct {
	channelID int64
	log       *zap.Logger
	dir       string
	keep      int

	cmd    *exec.Cmd
	stdin  io.WriteCloser
	closer io.Closer // RTMP 连接，DropChannel 时踢掉推流端

	mu          sync.Mutex
	flvStarted  bool
	baseMs      int64            // T0；0=尚未产出任何 segment
	segments    map[int64][]byte // time_ms → packed part
	order       []int64          // 按 time_ms 升序（ring 淘汰用）
	lastMs      int64            // 最新 segment 的 time_ms
	segmentSeq  int64            // 已完成 segment 计数
	ended       bool
	oversizeWas bool

	nowMs func() int64
}

// ffmpegArgs 组装转码+切段命令。要点：
//   - 强制每秒关键帧（-force_key_frames）保证 -f segment 严格按 1s 切；
//   - 严格码率上限：TDesktop 拉 part 单次 `upload.getFile(offset=0,limit=128KiB)`
//     且**不续读**，单段（视频+音频+TS 开销）>128KiB 会被静默截断致花屏。
//     故 unified 单质量必须压在 ~1Mbps 以下——这里目标 ~640kbps：视频
//     480k（maxrate/bufsize=480k 收紧到每秒 VBV，杜绝关键帧段爆量）+ 音频 64k，
//     并降到 640x360/24fps 进一步留余量；
//   - 输出自包含 mp4（每段独立 moov，可单独 avformat_open_input）。⚠ 不能用
//     MPEG-TS：TDesktop 裁剪版 ffmpeg 的 demuxer 白名单只有 mov/mp4 系
//     （prepare.py --enable-demuxer），mpegts 会让 tgcalls 打不开容器 → 黑屏；
//   - -segment_list pipe:1 每完成一段输出一行文件名，作为完成事件。
func ffmpegArgs(outDir string) []string {
	return []string{
		"-hide_banner", "-nostats", "-loglevel", "warning",
		"-fflags", "+genpts",
		"-f", "flv", "-i", "pipe:0",
		"-vf", "scale=-2:360", "-r", "24",
		"-c:v", "libx264", "-preset", "veryfast", "-tune", "zerolatency",
		"-profile:v", "main", "-pix_fmt", "yuv420p",
		"-b:v", "480k", "-maxrate", "480k", "-bufsize", "480k",
		"-g", "24", "-keyint_min", "24",
		"-force_key_frames", "expr:gte(t,n_forced*1)", "-sc_threshold", "0",
		"-c:a", "aac", "-b:a", "64k", "-ar", "48000", "-ac", "2",
		"-f", "segment",
		"-segment_time", "1",
		"-segment_format", "mp4",
		"-segment_format_options", "movflags=+faststart",
		"-segment_list", "pipe:1",
		"-segment_list_type", "flat",
		"-reset_timestamps", "1",
		filepath.Join(outDir, "seg%06d.mp4"),
	}
}

func newStream(channelID int64, ffmpegPath, workDir string, keep int, closer io.Closer, nowMs func() int64, log *zap.Logger) (*stream, error) {
	dir, err := os.MkdirTemp(workDir, fmt.Sprintf("live_%d_", channelID))
	if err != nil {
		return nil, fmt.Errorf("livestream: workdir: %w", err)
	}
	cmd := exec.Command(ffmpegPath, ffmpegArgs(dir)...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("livestream: ffmpeg stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("livestream: ffmpeg stdout: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("livestream: ffmpeg stderr: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("livestream: start ffmpeg: %w", err)
	}
	s := &stream{
		channelID: channelID,
		log:       log,
		dir:       dir,
		keep:      keep,
		cmd:       cmd,
		stdin:     stdin,
		closer:    closer,
		segments:  make(map[int64][]byte),
		nowMs:     nowMs,
	}
	go s.readSegmentList(stdout)
	go s.logStderr(stderr)
	go func() {
		_ = cmd.Wait()
		s.mu.Lock()
		s.ended = true
		s.mu.Unlock()
	}()
	return s, nil
}

// readSegmentList 消费 ffmpeg 的 segment 完成事件流。
func (s *stream) readSegmentList(r io.Reader) {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		name := scanner.Text()
		if name == "" {
			continue
		}
		path := filepath.Join(s.dir, filepath.Base(name))
		data, err := os.ReadFile(path)
		if err != nil {
			s.log.Warn("live stream read segment", zap.String("path", path), zap.Error(err))
			continue
		}
		// 诊断：TELESRV_LIVESTREAM_DUMP_DIR 非空时把原始 TS 切片留档供 ffprobe 检查。
		if dump := os.Getenv("TELESRV_LIVESTREAM_DUMP_DIR"); dump != "" {
			_ = os.MkdirAll(dump, 0o755)
			_ = os.WriteFile(filepath.Join(dump, fmt.Sprintf("ch%d_%s", s.channelID, filepath.Base(name))), data, 0o644)
		}
		_ = os.Remove(path)
		s.addSegment(data)
	}
}

func (s *stream) logStderr(r io.Reader) {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		s.log.Info("ffmpeg", zap.Int64("channel_id", s.channelID), zap.String("line", scanner.Text()))
	}
}

func (s *stream) addSegment(tsData []byte) {
	part := packUnifiedPart(tsData)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.baseMs == 0 {
		s.baseMs = s.nowMs() / segmentDurationMs * segmentDurationMs
	}
	timeMs := s.baseMs + s.segmentSeq*segmentDurationMs
	s.segmentSeq++
	s.segments[timeMs] = part
	s.order = append(s.order, timeMs)
	s.lastMs = timeMs
	for len(s.order) > s.keep {
		delete(s.segments, s.order[0])
		s.order = s.order[1:]
	}
	s.log.Debug("live stream segment produced",
		zap.Int64("channel_id", s.channelID), zap.Int64("time_ms", timeMs),
		zap.Int("bytes", len(part)), zap.Int64("seq", s.segmentSeq),
		zap.Int64("wall_ms", s.nowMs()))
	if len(part) > 128<<10 && !s.oversizeWas {
		s.oversizeWas = true
		s.log.Warn("live stream segment exceeds 128KiB, client will truncate",
			zap.Int64("channel_id", s.channelID), zap.Int("bytes", len(part)))
	}
}

// channels 返回当前时间轴（不足 minSegmentsBeforeAnnounce 段时不公布）。
func (s *stream) channels() []domain.LiveStreamChannel {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ended || len(s.order) < minSegmentsBeforeAnnounce {
		return nil
	}
	return []domain.LiveStreamChannel{{Channel: 1, Scale: 0, LastTimestampMs: s.lastMs}}
}

// part 取指定 time_ms 的打包 part。
func (s *stream) part(timeMs int64) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.baseMs == 0 {
		return nil, domain.ErrLiveStreamPartNotReady
	}
	if timeMs < s.baseMs || (timeMs-s.baseMs)%segmentDurationMs != 0 {
		return nil, domain.ErrLiveStreamPartExpired
	}
	if timeMs > s.lastMs {
		if s.ended {
			return nil, domain.ErrLiveStreamNoStream
		}
		return nil, domain.ErrLiveStreamPartNotReady
	}
	part, ok := s.segments[timeMs]
	if !ok {
		return nil, domain.ErrLiveStreamPartExpired
	}
	return part, nil
}

func (s *stream) active() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.ended
}

// stop 结束推流：断 RTMP 连接、关 ffmpeg stdin（自然退出），清空缓冲目录。
func (s *stream) stop() {
	s.mu.Lock()
	if s.ended {
		s.mu.Unlock()
		return
	}
	s.ended = true
	s.mu.Unlock()
	if s.closer != nil {
		_ = s.closer.Close()
	}
	_ = s.stdin.Close()
	go func() {
		_ = s.cmd.Wait()
		_ = os.RemoveAll(s.dir)
	}()
}

// ---- FLV 写入（RTMP tag → ffmpeg stdin）----

var flvHeader = []byte{'F', 'L', 'V', 0x01, 0x05, 0x00, 0x00, 0x00, 0x09, 0x00, 0x00, 0x00, 0x00}

// writeTag 把一条 RTMP media/data tag 以 FLV 封装写进 ffmpeg stdin。
// tagType：8=audio 9=video 18=script data。
func (s *stream) writeTag(tagType byte, timestampMs uint32, body []byte) error {
	s.mu.Lock()
	if s.ended {
		s.mu.Unlock()
		return domain.ErrLiveStreamNoStream
	}
	started := s.flvStarted
	s.flvStarted = true
	s.mu.Unlock()
	if !started {
		if _, err := s.stdin.Write(flvHeader); err != nil {
			return err
		}
	}
	var hdr [11]byte
	hdr[0] = tagType
	hdr[1] = byte(len(body) >> 16)
	hdr[2] = byte(len(body) >> 8)
	hdr[3] = byte(len(body))
	hdr[4] = byte(timestampMs >> 16)
	hdr[5] = byte(timestampMs >> 8)
	hdr[6] = byte(timestampMs)
	hdr[7] = byte(timestampMs >> 24)
	// stream id hdr[8:11] = 0
	if _, err := s.stdin.Write(hdr[:]); err != nil {
		return err
	}
	if _, err := s.stdin.Write(body); err != nil {
		return err
	}
	var prev [4]byte
	binary.BigEndian.PutUint32(prev[:], uint32(11+len(body)))
	_, err := s.stdin.Write(prev[:])
	return err
}
