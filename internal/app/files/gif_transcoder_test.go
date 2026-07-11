package files

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/gif"
	"testing"
)

func TestFFmpegGIFTranscoderProducesCanonicalMP4(t *testing.T) {
	transcoder, err := NewFFmpegGIFTranscoder()
	if err != nil {
		t.Skipf("ffmpeg/ffprobe unavailable: %v", err)
	}
	palette := color.Palette{color.Black, color.White}
	first := image.NewPaletted(image.Rect(0, 0, 3, 5), palette)
	second := image.NewPaletted(image.Rect(0, 0, 3, 5), palette)
	for i := range second.Pix {
		second.Pix[i] = 1
	}
	var input bytes.Buffer
	if err := gif.EncodeAll(&input, &gif.GIF{
		Image: []*image.Paletted{first, second}, Delay: []int{10, 10}, LoopCount: 0,
	}); err != nil {
		t.Fatal(err)
	}
	result, err := transcoder.Transcode(context.Background(), input.Bytes())
	if err != nil {
		t.Fatalf("Transcode: %v", err)
	}
	if len(result.Data) < 12 || string(result.Data[4:8]) != "ftyp" {
		t.Fatalf("output is not MP4: %x", result.Data[:min(len(result.Data), 16)])
	}
	if result.Width != 4 || result.Height != 6 || result.Duration <= 0 {
		t.Fatalf("metadata = %+v, want even 4x6 positive duration", result)
	}
}
