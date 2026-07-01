package main

import (
	"encoding/hex"
	"testing"
)

func TestPythonBytesLiteralToHex(t *testing.T) {
	got, converted, err := pythonBytesLiteralToHex(`b'\x19\x00A\\\n\101'`)
	if err != nil {
		t.Fatalf("pythonBytesLiteralToHex: %v", err)
	}
	if !converted {
		t.Fatal("converted = false, want true")
	}
	want := hex.EncodeToString([]byte{0x19, 0x00, 'A', '\\', '\n', 'A'})
	if got != want {
		t.Fatalf("hex = %q, want %q", got, want)
	}
}

func TestNormalizeSeedJSON(t *testing.T) {
	value := map[string]any{
		"file_reference_hex": "abcd",
		"thumbs": []any{
			map[string]any{"bytes": `b'\x01A'`},
		},
	}
	normalized, err := normalizeSeedJSON(value)
	if err != nil {
		t.Fatalf("normalizeSeedJSON: %v", err)
	}
	m := normalized.(map[string]any)
	if _, ok := m["file_reference_hex"]; ok {
		t.Fatal("file_reference_hex still present")
	}
	if m["file_reference"] != "abcd" {
		t.Fatalf("file_reference = %v, want abcd", m["file_reference"])
	}
	thumbs := m["thumbs"].([]any)
	thumb := thumbs[0].(map[string]any)
	if thumb["bytes"] != "0141" {
		t.Fatalf("thumb bytes = %v, want 0141", thumb["bytes"])
	}
}
