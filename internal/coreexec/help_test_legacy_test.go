package coreexec

import (
	"errors"
	"testing"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tlprofile"
	"go.uber.org/zap/zaptest"

	"telesrv/internal/rpc"
)

// legacyHelpTestRequest encodes the removed help.test#c0e202f7 method.
type legacyHelpTestRequest struct{}

func (*legacyHelpTestRequest) Encode(b *bin.Buffer) error {
	b.PutID(0xc0e202f7)
	return nil
}

func (*legacyHelpTestRequest) Decode(b *bin.Buffer) error {
	return b.ConsumeID(0xc0e202f7)
}

// In the split runtime the Edge answers the legacy help.test probe itself. The
// GRPCRemote admission must hand the exact wrapper parser's terminal evidence
// back to mtprotoedge unchanged and must never capture the wire for CoreExec.
func TestGRPCRemoteAdmissionLeavesWrappedHelpTestToEdge(t *testing.T) {
	edgeRouter := rpc.New(rpc.Config{DC: 2, IP: "127.0.0.1", Port: 2398}, rpc.Deps{}, zaptest.NewLogger(t), clock.System)
	remote := NewGRPCRemote(edgeRouter, nil, nil)

	request := &tg.InvokeWithLayerRequest{
		Layer: 228,
		Query: &tg.InitConnectionRequest{
			APIID: 1, DeviceModel: "help-test", SystemVersion: "test",
			AppVersion: "test", SystemLangCode: "en", LangCode: "en",
			Query: &legacyHelpTestRequest{},
		},
	}
	var body bin.Buffer
	if err := request.Encode(&body); err != nil {
		t.Fatalf("encode wrapped help.test: %v", err)
	}
	_, err := remote.AdmitLayer(tlprofile.Profile228, &body, tlprofile.Limits{})
	var terminal *tlprofile.UnknownTerminalError
	if !errors.As(err, &terminal) || terminal.WireID != 0xc0e202f7 || terminal.WrapperCount() != 2 {
		t.Fatalf("wrapped help.test admission err = %v, want unknown help.test terminal behind two wrappers", err)
	}
	if inner, ok := terminal.Wrapper(1); !ok || inner.Semantic() != tlprofile.SemanticMethodInitConnection {
		t.Fatalf("innermost wrapper = %#v ok=%v, want initConnection", inner, ok)
	}
	if remote.pendingCount != 0 {
		t.Fatalf("CoreExec captured legacy help.test wire: pending=%d", remote.pendingCount)
	}
}
