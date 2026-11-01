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

// segmentDurationMs is the fixed duration of a broadcast part. tgcalls
// StreamingMediaContext hardcodes _segmentDuration=1000 (scale 0); the timeline advances in
// steps of +1000, and segments must be cut exactly every 1 second (transcoding forces a
// keyframe every second to guarantee the cut point).
const segmentDurationMs = 1000

// minSegmentsBeforeAnnounce: after receiving last_timestamp_ms the client pulls starting
// from last-2000, so accumulate at least 3 segments before announcing the timeline, avoiding
// a playback start that requests a nonexistent negative-offset segment.
const minSegmentsBeforeAnnounce = 3

// stream is one active RTMP publish: FLV into ffmpeg (transcode + one-second MPEG-TS
// splits) → packed parts into the in-memory ring. Timeline: T0 is the first segment's
// completion time floored to the second; segment i's time_ms = T0+i*1000.
type stream struct {
	channelID int64
	log       *zap.Logger
	dir       string
	keep      int

	cmd    *exec.Cmd
	stdin  io.WriteCloser
	closer io.Closer // RTMP connection; the publisher is kicked on DropChannel

	mu          sync.Mutex
	flvStarted  bool
	baseMs      int64            // T0; 0 = no segment produced yet
	segments    map[int64][]byte // time_ms → packed part
	order       []int64          // ascending by time_ms (for ring eviction)
	lastMs      int64            // time_ms of the newest segment
	segmentSeq  int64            // count of completed segments
	ended       bool
	oversizeWas bool

	nowMs func() int64
}

// ffmpegArgs assembles the transcode + segment command. Key points:
//   - Force a keyframe every second (-force_key_frames) to keep -f segment cutting strictly
//     at 1s;
//   - Strict bitrate cap: TDesktop pulls a part with a single
//     `upload.getFile(offset=0,limit=128KiB)` and **does not continue reading**; a single
//     segment (video+audio+TS overhead) >128KiB is silently truncated, causing artifacts.
//     So the unified single quality must stay under ~1Mbps — target ~640kbps here: video 480k
//     (maxrate/bufsize=480k tightened to per-second VBV, preventing keyframe-segment bursts)
//     plus audio 64k, and lower to 640x360/24fps for extra headroom;
//   - Output a self-contained mp4 (each segment has its own moov and can be
//     avformat_open_input'd independently). ⚠ MPEG-TS cannot be used: TDesktop's trimmed
//     ffmpeg demuxer whitelist only has mov/mp4-family demuxers
//     (prepare.py --enable-demuxer), and mpegts makes tgcalls fail to open the container →
//     black screen;
//   - -segment_list pipe:1 prints one filename line per completed segment, serving as the
//     completion event.
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

// readSegmentList consumes ffmpeg's segment-completion event stream.
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
		// Diagnostics: when TELESRV_LIVESTREAM_DUMP_DIR is non-empty, archive the raw TS segments for ffprobe inspection.
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

// channels returns the current timeline (not announced until at least minSegmentsBeforeAnnounce segments).
func (s *stream) channels() []domain.LiveStreamChannel {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ended || len(s.order) < minSegmentsBeforeAnnounce {
		return nil
	}
	return []domain.LiveStreamChannel{{Channel: 1, Scale: 0, LastTimestampMs: s.lastMs}}
}

// part fetches the packed part for the specified time_ms.
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

// stop ends publishing: cut the RTMP connection, close ffmpeg stdin (natural exit), and clear the buffer directory.
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

// ---- FLV writing (RTMP tag → ffmpeg stdin) ----

var flvHeader = []byte{'F', 'L', 'V', 0x01, 0x05, 0x00, 0x00, 0x00, 0x09, 0x00, 0x00, 0x00, 0x00}

// writeTag writes one RTMP media/data tag, FLV-framed, into ffmpeg stdin.
// tagType: 8=audio 9=video 18=script data.
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
