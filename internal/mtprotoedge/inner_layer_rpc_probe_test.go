package mtprotoedge

import (
	"fmt"
	"testing"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/tg"
)

// The admission rejection log is the only place that shows which method a
// layer-wrapped request actually asked for, so the peeker has to be right. A
// regression here is silent: the log keeps emitting, the field just reads "-",
// and the measurement that motivated it comes back empty.
func TestInnerLayerRPCWireIDName(t *testing.T) {
	// The expected id is whatever the query encodes to, read back independently,
	// rather than a hardcoded constant: a query's wire id is profile-dependent
	// (the canonical and Layer 230 ids for a wallet method differ), so pinning a
	// literal here would assert the wrong thing.
	for name, query := range map[string]bin.Object{
		"wallet.getProofChallenge":     &tg.WalletGetProofChallengeRequest{},
		"wallet.tonConnectGetSessions": &tg.WalletTonConnectGetSessionsRequest{},
		"payments.getCurrencyRates":    &tg.PaymentsGetCurrencyRatesRequest{},
		"wallet.getState":              &tg.WalletGetStateRequest{},
	} {
		var standalone bin.Buffer
		if err := query.Encode(&standalone); err != nil {
			t.Fatal(err)
		}
		wantID, err := standalone.PeekID()
		if err != nil {
			t.Fatal(err)
		}
		want := fmt.Sprintf("#%08x", wantID)

		var buf bin.Buffer
		req := &tg.InvokeWithLayerRequest{Layer: 231, Query: query}
		if err := req.Encode(&buf); err != nil {
			t.Fatal(err)
		}
		if got := innerLayerRPCWireIDName(buf.Buf); got != want {
			t.Fatalf("%s: inner wire id = %s, want %s", name, got, want)
		}
	}
}

// Anything that is not a readable invokeWithLayer envelope reports "-" rather
// than a guess, including a naked request, which has no layer field to skip.
func TestInnerLayerRPCWireIDNameNonEnvelope(t *testing.T) {
	var naked bin.Buffer
	if err := (&tg.WalletGetStateRequest{}).Encode(&naked); err != nil {
		t.Fatal(err)
	}
	if got := innerLayerRPCWireIDName(naked.Buf); got != "-" {
		t.Fatalf("naked request reported %s, want -", got)
	}
	if got := innerLayerRPCWireIDName(nil); got != "-" {
		t.Fatalf("empty body reported %s, want -", got)
	}
	// A truncated envelope must not panic or invent a wire id.
	var buf bin.Buffer
	if err := (&tg.InvokeWithLayerRequest{Layer: 231, Query: &tg.WalletGetStateRequest{}}).Encode(&buf); err != nil {
		t.Fatal(err)
	}
	if got := innerLayerRPCWireIDName(buf.Buf[:6]); got != "-" {
		t.Fatalf("truncated envelope reported %s, want -", got)
	}
}
