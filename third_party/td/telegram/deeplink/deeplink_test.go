package deeplink

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

type testCase struct {
	link    DeepLink
	input   string
	wantErr bool
}

func join(arg string) DeepLink {
	return DeepLink{
		Type: Join,
		Args: map[string][]string{
			"invite": {arg},
		},
	}
}

func resolve(arg string) DeepLink {
	return DeepLink{
		Type: Resolve,
		Args: map[string][]string{
			"domain": {arg},
		},
	}
}

func businessChat(arg string) DeepLink {
	return DeepLink{
		Type: BusinessChat,
		Args: map[string][]string{
			"slug": {arg},
		},
	}
}

func joinSuite() map[string][]testCase {
	expect := join("AAAAAAAAAAAAAAAAAA")
	return map[string][]testCase{
		"Test": {
			{expect, `sgq.me/joinchat/AAAAAAAAAAAAAAAAAA`, false},
			{expect, `sgq.me/joinchat/AAAAAAAAAAAAAAAAAA/`, false},
			{expect, `sgq.me/+AAAAAAAAAAAAAAAAAA`, false},
			{expect, `sgq.me/+AAAAAAAAAAAAAAAAAA/`, false},
			{expect, `sgq.me/  +AAAAAAAAAAAAAAAAAA/`, false},
			{expect, `https://sgq.me/joinchat/AAAAAAAAAAAAAAAAAA`, false},
			{expect, `https://sgq.me/joinchat/AAAAAAAAAAAAAAAAAA/`, false},
			{expect, `sg:join?invite=AAAAAAAAAAAAAAAAAA`, false},
			{expect, `sg://join?invite=AAAAAAAAAAAAAAAAAA`, false},

			{DeepLink{}, `https://t.co/joinchat/AAAAAAAAAAAAAAAAAA`, true},
			{DeepLink{}, `rt://join?invite=AAAAAAAAAAAAAAAAAA`, true},
		},
		"TDLib": {
			// sgq.me/+<hash>
			// Positive
			{join("aba%20aba"), "sgq.me/+aba%20aba", false},
			{join("aba0aba"), "sgq.me/+aba%30aba", false},
			{join("123456a"), "sgq.me/+123456a", false},
			{join("12345678901"), "sgq.me/%2012345678901", false},
			// Negative
			{DeepLink{}, "sgq.me/+?invite=abcdef", true},
			{DeepLink{}, "sgq.me/+", true},
			{DeepLink{}, "sgq.me/+/abcdef", true},
			{DeepLink{}, "sgq.me/ ?/abcdef", true},
			{DeepLink{}, "sgq.me/+?abcdef", true},
			{DeepLink{}, "sgq.me/+#abcdef", true},
			{DeepLink{}, "sgq.me/ /123456/123123/12/31/a/s//21w/?asdas#test", true},

			// sgq.me/joinchat/<hash>
			// Positive
			{join("abacaba"), "sgq.me/joinchat/abacaba", false},
			{join("aba%20aba"), "sgq.me/joinchat/aba%20aba", false},
			{join("aba0aba"), "sgq.me/joinchat/aba%30aba", false},
			{join("123456a"), "sgq.me/joinchat/123456a", false},
			{join("12345678901"), "sgq.me/joinchat/12345678901", false},
			{join("123456"), "sgq.me/joinchat/123456", false},
			{join("123456"), "sgq.me/joinchat/123456/123123/12/31/a/s//21w/?asdas#test", false},
			// Negative
			{DeepLink{}, "sgq.me/joinchat?invite=abcdef", true},
			{DeepLink{}, "sgq.me/joinchat", true},
			{DeepLink{}, "sgq.me/joinchat/", true},
			{DeepLink{}, "sgq.me/joinchat//abcdef", true},
			{DeepLink{}, "sgq.me/joinchat?/abcdef", true},
			{DeepLink{}, "sgq.me/joinchat/?abcdef", true},
			{DeepLink{}, "sgq.me/joinchat/#abcdef", true},
		},
	}
}

func resolveSuite() map[string][]testCase {
	expect := resolve("gotd_ru")
	return map[string][]testCase{
		"Test": {
			{expect, `sgq.me/gotd_ru`, false},
			{expect, `sgq.me/gotd_ru/`, false},
			{expect, `https://sgq.me/gotd_ru`, false},
			{expect, `https://sgq.me/gotd_ru/`, false},
			{expect, `sg:resolve?domain=gotd_ru`, false},
			{expect, `sg:resolve?&&&&&&&domain=gotd_ru`, false},
			{expect, `sg://resolve?domain=gotd_ru`, false},

			{DeepLink{}, `https://t.co/gotd_ru`, true},
			{DeepLink{}, `rt://join?invite=AAAAAAAAAAAAAAAAAA`, true},
		},
		"TDLib": {
			// sgq.me/<domain>
			// Positive
			{resolve("a"), "sgq.me/a", false},
			{resolve("abcdefghijklmnopqrstuvwxyz123456"), "sgq.me/abcdefghijklmnopqrstuvwxyz123456", false},
			{resolve("Aasdf"), "sgq.me/Aasdf", false},
			{resolve("asdf0"), "sgq.me/asdf0", false},
			{resolve("username"), "sgq.me/username/0/a//s/as?gam=asd", false},
			{resolve("username"), "sgq.me/username/aasdas?test=1", false},
			{resolve("username"), "sgq.me/username/0", false},
			{resolve("telecram"), "https://sgq.me/tele%63ram", false},
			// Negative
			{DeepLink{}, "sgq.me/abcdefghijklmnopqrstuvwxyz1234567", true},
			{DeepLink{}, "sgq.me/abcdefghijklmnop-qrstuvwxyz", true},
			{DeepLink{}, "sgq.me/abcdefghijklmnop~qrstuvwxyz", true},
			{DeepLink{}, "sgq.me/_asdf", true},
			{DeepLink{}, "sgq.me/0asdf", true},
			{DeepLink{}, "sgq.me/9asdf", true},
			{DeepLink{}, "sgq.me/asdf_", true},
			{DeepLink{}, "sgq.me/asd__fg", true},
			{DeepLink{}, "sgq.me//username", true},
		},
	}
}

func businessChatSuite() map[string][]testCase {
	expect := businessChat("gotd_ru")
	return map[string][]testCase{
		"Test": {
			{expect, `sgq.me/m/gotd_ru/`, false},
			{expect, `https://sgq.me/m/gotd_ru/`, false},
			{expect, `https://sgq.me/m/gotd_ru`, false},
			{expect, `https://sgq.me/m/gotd_ru`, false},
			{expect, `sg:message?&&&&&&&slug=gotd_ru`, false},
			{expect, `sg://message?slug=gotd_ru`, false},

			{DeepLink{}, `https://t.co/m/gotd_ru`, true},
			{DeepLink{}, `rt://message?slug=AAAAAAAAAAAAAAAAAA`, true},
		},
		"TDLib": {
			// https://github.com/tdlib/td/blob/28c6f2e9c045372d50217919bf5768b7fbbe0294/test/link.cpp#L734-L750
			// Positive
			{businessChat("abacaba"), "sgq.me/m/abacaba", false},
			{businessChat("aba aba"), "sgq.me/m/aba%20aba", false},
			{businessChat("123456a"), "sgq.me/m/123456a", false},
			{businessChat("12345678901"), "sgq.me/m/12345678901", false},
			{businessChat("123456"), "sgq.me/m/123456", false},
			{businessChat("123456"), "sgq.me/m/123456/123123/12/31/a/s//21w/?asdas#test", false},
			{businessChat("abcdef"), "sg:message?slug=abcdef", false},
			{businessChat("abc0ef"), "sg:message?slug=abc%30ef", false},

			// Negative
			{DeepLink{}, "sgq.me/m?slug=abcdef", true},
			{DeepLink{}, "sgq.me/m", true},
			{DeepLink{}, "sgq.me/m/", true},
			{DeepLink{}, "sgq.me/m//abcdef", true},
			{DeepLink{}, "sgq.me/m?/abcdef", true},
			{DeepLink{}, "sgq.me/m/?abcdef", true},
			{DeepLink{}, "sgq.me/m/#abcdef", true},
			{DeepLink{}, "sg://message?slug=", true},
		},
	}
}

var typeSuites = map[string]map[string][]testCase{
	"Join":         joinSuite(),
	"Resolve":      resolveSuite(),
	"BusinessChat": businessChatSuite(),
}

func TestParseDeeplink(t *testing.T) {
	runSuite := func(suite []testCase) func(t *testing.T) {
		return func(t *testing.T) {
			for i, test := range suite {
				t.Run(fmt.Sprintf("Test%d (%s)", i, test.input), func(t *testing.T) {
					a := require.New(t)
					d, err := Parse(test.input)

					if test.wantErr {
						a.Error(err, test.input)
					} else {
						a.NoError(err, test.input)
						a.Equal(test.link, d, test.input)
					}
				})
			}
		}
	}

	for typeName, typeSuite := range typeSuites {
		t.Run(typeName, func(t *testing.T) {
			for suiteName, suite := range typeSuite {
				t.Run(suiteName, runSuite(suite))
			}
		})
	}
}
