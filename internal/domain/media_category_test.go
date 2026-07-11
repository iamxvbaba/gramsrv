package domain

import (
	"reflect"
	"testing"
)

func TestClassifyMediaCategories(t *testing.T) {
	doc := func(attrs ...DocumentAttribute) *MessageMedia {
		return &MessageMedia{Kind: MessageMediaKindDocument, Document: &Document{Attributes: attrs}}
	}
	urlEnt := []MessageEntity{{Type: MessageEntityURL}}
	textURLEnt := []MessageEntity{{Type: MessageEntityTextURL}}
	emailEnt := []MessageEntity{{Type: MessageEntityEmail}}

	cases := []struct {
		name     string
		media    *MessageMedia
		entities []MessageEntity
		want     []MediaCategory
	}{
		{"nil media no entities", nil, nil, []MediaCategory{}},
		{"photo", &MessageMedia{Kind: MessageMediaKindPhoto, Photo: &Photo{}}, nil, []MediaCategory{MediaCategoryPhoto}},
		{"poll", &MessageMedia{Kind: MessageMediaKindPoll}, nil, []MediaCategory{MediaCategoryPoll}},
		{"video", doc(DocumentAttribute{Kind: DocAttrVideo}), nil, []MediaCategory{MediaCategoryVideo}},
		{"round video note", doc(DocumentAttribute{Kind: DocAttrVideo, RoundMessage: true}), nil, []MediaCategory{MediaCategoryRoundVideo}},
		{"gif animation", &MessageMedia{Kind: MessageMediaKindDocument, Document: &Document{MimeType: "video/mp4", Attributes: []DocumentAttribute{{Kind: DocAttrAnimated}, {Kind: DocAttrVideo, W: 320, H: 240, Duration: 1}}}}, nil, []MediaCategory{MediaCategoryGif}},
		{"raw gif is a file", &MessageMedia{Kind: MessageMediaKindDocument, Document: &Document{MimeType: "image/gif", Attributes: []DocumentAttribute{{Kind: DocAttrAnimated}}}}, nil, []MediaCategory{MediaCategoryFile}},
		{"music", doc(DocumentAttribute{Kind: DocAttrAudio}), nil, []MediaCategory{MediaCategoryMusic}},
		{"voice", doc(DocumentAttribute{Kind: DocAttrAudio, Voice: true}), nil, []MediaCategory{MediaCategoryVoice}},
		{"generic file", doc(DocumentAttribute{Kind: DocAttrFilename, FileName: "x.pdf"}), nil, []MediaCategory{MediaCategoryFile}},
		{"sticker excluded", doc(DocumentAttribute{Kind: DocAttrSticker}), nil, []MediaCategory{}},
		{"animated sticker excluded", doc(DocumentAttribute{Kind: DocAttrSticker}, DocumentAttribute{Kind: DocAttrAnimated}), nil, []MediaCategory{}},
		{"photo with url", &MessageMedia{Kind: MessageMediaKindPhoto, Photo: &Photo{}}, urlEnt, []MediaCategory{MediaCategoryPhoto, MediaCategoryURL}},
		{"text-only url", nil, urlEnt, []MediaCategory{MediaCategoryURL}},
		{"text-only text_url", nil, textURLEnt, []MediaCategory{MediaCategoryURL}},
		{"text-only email", nil, emailEnt, []MediaCategory{MediaCategoryURL}},
		{"webpage media", &MessageMedia{Kind: MessageMediaKindWebPage, WebPage: &MessageWebPage{State: MessageWebPageStateDone, ID: 1, URL: "https://example.test"}}, nil, []MediaCategory{MediaCategoryURL}},
		{"webpage media with url entity deduped", &MessageMedia{Kind: MessageMediaKindWebPage, WebPage: &MessageWebPage{State: MessageWebPageStateDone, ID: 1, URL: "https://example.test"}}, urlEnt, []MediaCategory{MediaCategoryURL}},
		{"file with link", doc(DocumentAttribute{Kind: DocAttrFilename}), urlEnt, []MediaCategory{MediaCategoryFile, MediaCategoryURL}},
		{"geo not indexed", &MessageMedia{Kind: MessageMediaKindGeo}, nil, []MediaCategory{}},
		{"contact not indexed", &MessageMedia{Kind: MessageMediaKindContact}, nil, []MediaCategory{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ClassifyMediaCategories(tc.media, tc.entities)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("ClassifyMediaCategories = %v, want %v", got, tc.want)
			}
		})
	}
}
