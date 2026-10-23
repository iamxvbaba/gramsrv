package gen

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// TestGeneratedLayerSourceBudget makes the sparse sidecar, rather than tg,
// own all exact-profile source. The caps deliberately leave schema-growth
// headroom while rejecting a return to per-profile dense backends.
func TestGeneratedLayerSourceBudget(t *testing.T) {
	legacy, err := filepath.Glob(filepath.Join("..", "tg", "tl_layer*_gen.go"))
	if err != nil {
		t.Fatal(err)
	}
	if len(legacy) != 0 {
		t.Fatalf("legacy tg layer backend was regenerated: %v", legacy)
	}

	files, err := filepath.Glob(filepath.Join("..", "tlprofile", "tl_profile*_gen.go"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("sparse tlprofile source is absent")
	}
	var size, lines int64
	for _, name := range files {
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		size += int64(len(data))
		lines += int64(bytes.Count(data, []byte{'\n'}))
	}
	const maxBytes = 16 << 20
	const maxLines = 400_000
	if size > maxBytes || lines > maxLines {
		t.Fatalf("sparse exact-profile source is too large: %.2f MiB/%d lines; budget %.2f MiB/%d lines",
			float64(size)/(1<<20), lines, float64(maxBytes)/(1<<20), maxLines)
	}
	t.Logf("sparse exact-profile source: %.2f MiB, %d lines across %d files", float64(size)/(1<<20), lines, len(files))
}
