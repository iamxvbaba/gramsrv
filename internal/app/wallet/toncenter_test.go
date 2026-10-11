package wallet

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func str(s string) *string                                                { return &s }
func reply(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
}

func TestToncenterKeepsProviderSemantics(t *testing.T) {
	calls := 0
	p := NewToncenter("server-test-key")
	p.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Scheme != "https" || r.URL.Host != "toncenter.com" || r.URL.Path != "/api/v2/jsonRPC" || r.URL.RawQuery != "x=a%20b" {
			t.Fatal("wrong destination")
		}
		if r.Method != "POST" || r.Header.Get("X-API-Key") != "server-test-key" {
			t.Fatal("wrong method or credential")
		}
		body, _ := io.ReadAll(r.Body)
		if string(body) != `{"jsonrpc":"2.0","method":"sendBoc","id":17}` {
			t.Fatal("payload changed")
		}
		return reply(200, `{"jsonrpc":"2.0","id":17,"error":{"code":-32000,"message":"rejected"}}`), nil
	})
	result, err := p.Perform(context.Background(), "/api/v2/jsonRPC", str("x=a%20b"), str(`{"jsonrpc":"2.0","method":"sendBoc","id":17}`))
	if err != nil || !strings.Contains(result, `"error"`) || calls != 1 {
		t.Fatal("JSON-RPC error must be returned, without retry", err)
	}
}

func TestToncenterRejectsUnsafeInputBeforeNetwork(t *testing.T) {
	p := NewToncenter("")
	p.client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) { t.Fatal("unexpected network request"); return nil, nil })
	for _, endpoint := range []string{"", "https://evil.test/api/v2/x", "//evil.test/api/v2/x", "/api/v2/../x", "/api/v2/%2e%2e/x", "/api/v2/x?url=x", "/api/v2/x#x", "/api/v2/x\\y", "/api/v2//x", "/unrelated"} {
		if _, err := p.Perform(context.Background(), endpoint, nil, nil); !errors.Is(err, ErrProviderRequest) {
			t.Errorf("accepted path %q", endpoint)
		}
	}
	for _, query := range []string{"x=%xx", "x=1#fragment", "x=1\r\nheader:value", "api_key=caller-key", "API_KEY=caller-key"} {
		if _, err := p.Perform(context.Background(), "/api/v2/jsonRPC", str(query), nil); !errors.Is(err, ErrProviderRequest) {
			t.Fatal("accepted unsafe query")
		}
	}
	if _, err := p.Perform(context.Background(), "/api/v2/jsonRPC", nil, str("not-json")); !errors.Is(err, ErrProviderRequest) {
		t.Fatal("accepted invalid JSON")
	}
}

func TestToncenterNeverFollowsRedirectOrRetries(t *testing.T) {
	p := NewToncenter("")
	calls := 0
	p.client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		r := reply(307, "")
		r.Header.Set("Location", "http://127.0.0.1/secret")
		return r, nil
	})
	_, err := p.Perform(context.Background(), "/api/v3/transactions", nil, nil)
	var status *ProviderHTTPError
	if !errors.As(err, &status) || status.Status != 307 || calls != 1 {
		t.Fatal("redirect was followed or wrong error", err)
	}
	p.client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return nil, errors.New("transport contains sensitive URL")
	})
	calls = 0
	_, err = p.Perform(context.Background(), "/api/v2/jsonRPC", nil, str(`{"method":"sendBoc"}`))
	if !errors.Is(err, ErrProviderUnavailable) || calls != 1 || strings.Contains(err.Error(), "sensitive") {
		t.Fatal("retry or sensitive transport error exposed")
	}
}

func TestToncenterLimitsResponses(t *testing.T) {
	p := NewToncenter("")
	for _, body := range []string{"<html>failure</html>", `"` + strings.Repeat("x", MaxProviderResponse) + `"`} {
		p.client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) { return reply(200, body), nil })
		if _, err := p.Perform(context.Background(), "/api/v2/getAddressBalance", nil, nil); !errors.Is(err, ErrProviderResponse) {
			t.Fatal("unbounded/non-JSON response accepted")
		}
	}
}
