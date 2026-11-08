package botapi

import (
	"reflect"
	"testing"

	"telesrv/internal/domain"
)

func TestHTMLCharacterReferencesOptionalSemicolon(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{`&lt &gt &amp &quot`, `< > & "`},
		{`&lt; &gt; &amp; &quot;`, `< > & "`},
		{`&amp&amp;`, `&&`},
		{`&#65 &#x41 &#X1F680`, "A A 🚀"},
		{`&#65; &#x41; &#X1F680;`, "A A 🚀"},
		{`&#65Z &#x41Z`, `AZ AZ`},
		{`&amplifier &amp1`, `&amplifier &1`},
		{`&unknown; &apos;`, `&unknown; &apos;`},
		{`& &# &#x &#;`, `& &# &#x &#;`},
		{`&#0; &#xD800; &#1114112;`, `&#0; &#xD800; &#1114112;`},
		{`&#999999999999999999999999;`, `&#999999999999999999999999;`},
	} {
		t.Run(tc.input, func(t *testing.T) {
			plain, _, err := parseBotAPIHTML(tc.input)
			if err != nil || plain != tc.want {
				t.Fatalf("HTML text = %q, %v; want %q", plain, err, tc.want)
			}
			attribute, err := decodeBotAPIHTMLString(tc.input)
			if err != nil || attribute != tc.want {
				t.Fatalf("HTML attribute = %q, %v; want %q", attribute, err, tc.want)
			}
		})
	}
}

func TestHTMLCharacterReferencesPreserveEntities(t *testing.T) {
	input := `&#x1F680<b>&#65</b> <a href="https://example.test/?a=1&#38b=2">link</a>`
	plain, entities, err := botAPIFormattedText(input, "HTML", nil, 4096, true)
	if err != nil {
		t.Fatal(err)
	}
	want := []domain.MessageEntity{
		{Type: domain.MessageEntityBold, Offset: 2, Length: 1},
		{Type: domain.MessageEntityTextURL, Offset: 4, Length: 4, URL: "https://example.test/?a=1&b=2"},
	}
	if plain != "🚀A link" || !reflect.DeepEqual(entities, want) {
		t.Fatalf("formatted text = %q, %#v; want %q, %#v", plain, entities, "🚀A link", want)
	}
}
