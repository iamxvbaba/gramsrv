package mtprotoedge

import (
	"errors"
	"fmt"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/proto"
	"github.com/iamxvbaba/td/tlprofile"
)

const (
	destroyAuthKeyRequestTypeID = 0xd1435160
	destroyAuthKeyOkTypeID      = 0xf660e1d4
	destroyAuthKeyFailTypeID    = 0xea109b13
)

var errDestroyAuthKeyMustBeExclusive = errors.New("wrapped destroy_auth_key must be the only logical message")

// wrappedDestroyAuthKeyTerminal accepts only evidence emitted by the generated
// exact wrapper parser after it has legally reached the innermost non-API
// terminal. It never re-parses wrapper bytes at runtime.
func wrappedDestroyAuthKeyTerminal(err error) (*tlprofile.UnknownTerminalError, bool) {
	var terminal *tlprofile.UnknownTerminalError
	if !errors.As(err, &terminal) || terminal == nil || terminal.WireID != destroyAuthKeyRequestTypeID {
		return nil, false
	}
	return terminal, true
}

// validWrappedDestroyAuthKeyChain is deliberately narrower than "some generated
// wrapper decoded". invokeAfter*, takeout and update-suppression wrappers carry
// execution semantics which the service-message fast path must not silently
// discard. The official first-connection path is exactly
// invokeWithLayer(initConnection(destroy_auth_key)); an already initialized
// connection sends the bare service message and is classified before Layer RPC
// admission.
func validWrappedDestroyAuthKeyChain(terminal *tlprofile.UnknownTerminalError) bool {
	if terminal == nil || terminal.WrapperCount() != 2 {
		return false
	}
	outer, outerOK := terminal.Wrapper(0)
	inner, innerOK := terminal.Wrapper(1)
	return outerOK && innerOK &&
		outer.Profile() == terminal.Profile &&
		inner.Profile() == terminal.Profile &&
		outer.Semantic() == tlprofile.SemanticMethodInvokeWithLayer &&
		inner.Semantic() == tlprofile.SemanticMethodInitConnection
}

type destroyAuthKeyRequest struct{}

func (*destroyAuthKeyRequest) Encode(b *bin.Buffer) error {
	b.PutID(destroyAuthKeyRequestTypeID)
	return nil
}

func (*destroyAuthKeyRequest) Decode(b *bin.Buffer) error {
	if err := b.ConsumeID(destroyAuthKeyRequestTypeID); err != nil {
		return fmt.Errorf("decode destroy_auth_key: %w", err)
	}
	return nil
}

// destroyAuthKeyRPCResult is the only rpc_result envelope admitted as a
// layer-invariant control value. Its closed result set is part of the type, so
// it can never smuggle a profile-dependent API payload past the exact Layer
// binding boundary.
type destroyAuthKeyRPCResult struct {
	RequestMessageID int64
	ResultTypeID     uint32
}

func (r *destroyAuthKeyRPCResult) Encode(b *bin.Buffer) error {
	if r == nil {
		return fmt.Errorf("encode destroy_auth_key rpc_result: nil result")
	}
	switch r.ResultTypeID {
	case destroyAuthKeyOkTypeID, destroyAuthKeyFailTypeID:
	default:
		return fmt.Errorf("encode destroy_auth_key rpc_result: invalid inner constructor %#x", r.ResultTypeID)
	}
	b.PutID(proto.ResultTypeID)
	b.PutLong(r.RequestMessageID)
	b.PutID(r.ResultTypeID)
	return nil
}
