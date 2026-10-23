package domain

import (
	"reflect"
	"strings"
	"testing"
)

func TestDetectAutomaticMessageEntitiesUsesUTF16AndWhitespaceBoundaries(t *testing.T) {
	message := "اعلان 🚀\n\n@matrixG"
	want := []MessageEntity{{Type: MessageEntityMention, Offset: 10, Length: 8}}
	if got := DetectAutomaticMessageEntities(message, nil); !reflect.DeepEqual(got, want) {
		t.Fatalf("entities = %+v, want %+v", got, want)
	}
}

func TestDetectAutomaticMessageEntitiesCoversLexicalTypes(t *testing.T) {
	message := "@alice #golang $USD /help@matrix_bot"
	want := []MessageEntity{
		{Type: MessageEntityMention, Offset: 0, Length: 6},
		{Type: MessageEntityHashtag, Offset: 7, Length: 7},
		{Type: MessageEntityCashtag, Offset: 15, Length: 4},
		{Type: MessageEntityBotCommand, Offset: 20, Length: 16},
	}
	if got := DetectAutomaticMessageEntities(message, nil); !reflect.DeepEqual(got, want) {
		t.Fatalf("entities = %+v, want %+v", got, want)
	}
}

func TestDetectAutomaticMessageEntitiesSkipsEmailURLsAndOccupiedRanges(t *testing.T) {
	message := "mail bob@example.com https://sgq.me/@scam github.com/@other @real"
	got := DetectAutomaticMessageEntities(message, []MessageEntitySpan{{Offset: 58, Length: 5}})
	if len(got) != 0 {
		t.Fatalf("entities = %+v, want no email, URL-path or occupied mention", got)
	}
}

func TestDetectAutomaticMessageEntitiesMentionBoundaries(t *testing.T) {
	tests := []struct {
		name    string
		message string
		want    []MessageEntity
	}{
		{
			name:    "maximum username length",
			message: "\n@" + strings.Repeat("a", 32),
			want:    []MessageEntity{{Type: MessageEntityMention, Offset: 1, Length: 33}},
		},
		{
			name:    "username too long",
			message: "@" + strings.Repeat("a", 33),
		},
		{
			name:    "unicode word prefix",
			message: "نام@matrixG",
		},
		{
			name:    "duplicate at prefix",
			message: "@@matrixG",
		},
		{
			name:    "punctuation boundary",
			message: "(@matrixG)",
			want:    []MessageEntity{{Type: MessageEntityMention, Offset: 1, Length: 8}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DetectAutomaticMessageEntities(tt.message, nil); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("entities = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestDetectAutomaticMessageEntitiesCapsDerivedEntities(t *testing.T) {
	message := strings.Repeat("@a ", MaxMessageEntityCount+1)
	got := DetectAutomaticMessageEntities(message, nil)
	if len(got) != MaxMessageEntityCount {
		t.Fatalf("entity count = %d, want %d", len(got), MaxMessageEntityCount)
	}
}
