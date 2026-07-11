package files

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	gifTranscodeTimeout        = 20 * time.Second
	gifTranscodeMaxInputBytes  = 50 << 20
	gifTranscodeMaxOutputBytes = 200 << 20
	gifTranscodeMaxConcurrent  = 2
)

// GIFVideo 是服务端把真实 GIF 规范化为 Telegram GIFv 后的结果。
type GIFVideo struct {
	Data     []byte
	Width    int
	Height   int
	Duration float64
}

// GIFTranscoder 必须输出无声、faststart 的 H.264 MP4，并返回可持久化的视频元数据。
type GIFTranscoder interface {
	Transcode(ctx context.Context, data []byte) (GIFVideo, error)
}

type FFmpegGIFTranscoder struct {
	ffmpeg  string
	ffprobe string
	timeout time.Duration
	slots   chan struct{}
}

func NewFFmpegGIFTranscoder() (*FFmpegGIFTranscoder, error) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		return nil, err
	}
	ffprobeName := "ffprobe"
	if ext := filepath.Ext(ffmpeg); ext != "" {
		ffprobeName += ext
	}
	ffprobe := filepath.Join(filepath.Dir(ffmpeg), ffprobeName)
	if _, err := os.Stat(ffprobe); err != nil {
		ffprobe, err = exec.LookPath("ffprobe")
		if err != nil {
			return nil, err
		}
	}
	return &FFmpegGIFTranscoder{
		ffmpeg: ffmpeg, ffprobe: ffprobe, timeout: gifTranscodeTimeout,
		slots: make(chan struct{}, gifTranscodeMaxConcurrent),
	}, nil
}

func (t *FFmpegGIFTranscoder) Transcode(ctx context.Context, data []byte) (GIFVideo, error) {
	if t == nil || t.ffmpeg == "" || t.ffprobe == "" {
		return GIFVideo{}, fmt.Errorf("gif transcoder unavailable")
	}
	if len(data) == 0 || len(data) > gifTranscodeMaxInputBytes {
		return GIFVideo{}, fmt.Errorf("gif input size out of range: %d", len(data))
	}
	select {
	case t.slots <- struct{}{}:
		defer func() { <-t.slots }()
	case <-ctx.Done():
		return GIFVideo{}, ctx.Err()
	}
	runCtx, cancel := context.WithTimeout(ctx, t.timeout)
	defer cancel()

	input, err := os.CreateTemp("", "telesrv-gif-*.gif")
	if err != nil {
		return GIFVideo{}, fmt.Errorf("create gif input: %w", err)
	}
	inputPath := input.Name()
	defer os.Remove(inputPath)
	if _, err := input.Write(data); err != nil {
		input.Close()
		return GIFVideo{}, fmt.Errorf("write gif input: %w", err)
	}
	if err := input.Close(); err != nil {
		return GIFVideo{}, fmt.Errorf("close gif input: %w", err)
	}
	output, err := os.CreateTemp("", "telesrv-gifv-*.mp4")
	if err != nil {
		return GIFVideo{}, fmt.Errorf("create gif output: %w", err)
	}
	outputPath := output.Name()
	output.Close()
	defer os.Remove(outputPath)

	cmd := exec.CommandContext(runCtx, t.ffmpeg,
		"-hide_banner", "-loglevel", "error", "-y",
		"-i", inputPath, "-map", "0:v:0", "-an",
		"-vf", "scale=ceil(iw/2)*2:ceil(ih/2)*2:flags=lanczos",
		"-c:v", "libx264", "-preset", "veryfast", "-crf", "23",
		"-pix_fmt", "yuv420p", "-movflags", "+faststart", outputPath)
	stderr, err := cmd.CombinedOutput()
	if runCtx.Err() != nil {
		return GIFVideo{}, runCtx.Err()
	}
	if err != nil {
		return GIFVideo{}, commandError("ffmpeg gif transcode", err, stderr)
	}
	info, err := os.Stat(outputPath)
	if err != nil || info.Size() <= 0 || info.Size() > gifTranscodeMaxOutputBytes {
		return GIFVideo{}, fmt.Errorf("gif output size invalid")
	}
	meta, err := t.probe(runCtx, outputPath)
	if err != nil {
		return GIFVideo{}, err
	}
	video, err := os.ReadFile(outputPath)
	if err != nil {
		return GIFVideo{}, fmt.Errorf("read gif output: %w", err)
	}
	meta.Data = video
	return meta, nil
}

func (t *FFmpegGIFTranscoder) probe(ctx context.Context, path string) (GIFVideo, error) {
	cmd := exec.CommandContext(ctx, t.ffprobe, "-v", "error", "-select_streams", "v:0",
		"-show_entries", "stream=width,height,duration:format=duration", "-of", "json", path)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return GIFVideo{}, commandError("ffprobe gif output", err, out)
	}
	var result struct {
		Streams []struct {
			Width, Height int
			Duration      string
		} `json:"streams"`
		Format struct {
			Duration string `json:"duration"`
		} `json:"format"`
	}
	if err := json.Unmarshal(out, &result); err != nil || len(result.Streams) != 1 {
		return GIFVideo{}, fmt.Errorf("invalid ffprobe gif metadata")
	}
	duration := parsePositiveFloat(result.Streams[0].Duration)
	if duration == 0 {
		duration = parsePositiveFloat(result.Format.Duration)
	}
	stream := result.Streams[0]
	if stream.Width <= 0 || stream.Height <= 0 || duration <= 0 {
		return GIFVideo{}, fmt.Errorf("incomplete gif video metadata")
	}
	return GIFVideo{Width: stream.Width, Height: stream.Height, Duration: duration}, nil
}

func parsePositiveFloat(value string) float64 {
	v, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil || v <= 0 {
		return 0
	}
	return v
}

func commandError(op string, err error, output []byte) error {
	msg := strings.TrimSpace(string(output))
	if len(msg) > 512 {
		msg = msg[:512]
	}
	if msg == "" {
		return fmt.Errorf("%s: %w", op, err)
	}
	return fmt.Errorf("%s: %w: %s", op, err, msg)
}
