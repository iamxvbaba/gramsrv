package mtprotoedge

import (
	"errors"
	"fmt"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/proto"
	"github.com/iamxvbaba/td/tlprofile"
)

const (
	// helpTestRequestTypeID is the legacy help.test method. Telegram removed
	// help.test#c0e202f7 = Bool from the published schema, but older and
	// third-party clients still use it as a connectivity probe, so exact Layer
	// admission reports it as an unknown wrapped terminal instead of decoding it.
	helpTestRequestTypeID = 0xc0e202f7
	boolTrueTypeID        = 0x997275b5
)

// wrappedHelpTestTerminal accepts only evidence emitted by the generated exact
// wrapper parser after it has legally reached the innermost non-API terminal.
// It never re-parses wrapper bytes at runtime.
func wrappedHelpTestTerminal(err error) (*tlprofile.UnknownTerminalError, bool) {
	var terminal *tlprofile.UnknownTerminalError
	if !errors.As(err, &terminal) || terminal == nil || terminal.WireID != helpTestRequestTypeID {
		return nil, false
	}
	return terminal, true
}

// validWrappedHelpTestChain accepts only the transparent invokeWithLayer and
// initConnection wrappers, which carry no execution semantics. Wrappers such as
// invokeAfter*, takeout or msg containers are rejected: the service fast path
// must not silently discard their ordering semantics. The innermost wrapper is
// always initConnection; an outer invokeWithLayer is optional.
func validWrappedHelpTestChain(terminal *tlprofile.UnknownTerminalError) bool {
	if terminal == nil {
		return false
	}
	count := terminal.WrapperCount()
	if count < 1 || count > 2 {
		return false
	}
	inner, ok := terminal.Wrapper(count - 1)
	if !ok || inner.Profile() != terminal.Profile || inner.Semantic() != tlprofile.SemanticMethodInitConnection {
		return false
	}
	if count == 2 {
		outer, ok := terminal.Wrapper(0)
		if !ok || outer.Profile() != terminal.Profile || outer.Semantic() != tlprofile.SemanticMethodInvokeWithLayer {
			return false
		}
	}
	return true
}

type helpTestRequest struct{}

func (*helpTestRequest) Encode(b *bin.Buffer) error {
	b.PutID(helpTestRequestTypeID)
	return nil
}

func (*helpTestRequest) Decode(b *bin.Buffer) error {
	if err := b.ConsumeID(helpTestRequestTypeID); err != nil {
		return fmt.Errorf("decode help.test: %w", err)
	}
	return nil
}

// helpTestRPCResult is the layer-invariant rpc_result envelope for the legacy
// help.test method. Its payload is a closed boolTrue terminal, so it can never
// smuggle a profile-dependent API value past the exact Layer binding boundary.
type helpTestRPCResult struct {
	RequestMessageID int64
}

func (r *helpTestRPCResult) Encode(b *bin.Buffer) error {
	if r == nil {
		return fmt.Errorf("encode help.test rpc_result: nil result")
	}
	b.PutID(proto.ResultTypeID)
	b.PutLong(r.RequestMessageID)
	b.PutID(boolTrueTypeID)
	return nil
}
