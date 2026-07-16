package mtprotoedge

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/mt"
	"github.com/iamxvbaba/td/proto"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tlprofile"
)

// preparedOnlyLayerRPCResult is intentionally incapable of encoding. The
// binding guard must reject it solely from immutable admission identity before
// any result method other than Prepared can be observed.
type preparedOnlyLayerRPCResult struct {
	tlprofile.Result
	prepared tlprofile.PreparedCall
}

func (r *preparedOnlyLayerRPCResult) Prepared() tlprofile.PreparedCall { return r.prepared }

func TestBindAdmittedLayerRPCResultRequiresExactRequestIdentity(t *testing.T) {
	dispatcher := tlprofile.NewDispatcher()
	admit := func(request bin.Encoder) tlprofile.Admission {
		t.Helper()
		body := &bin.Buffer{Buf: exactLayerRPCBody(t, request)}
		admitted, err := dispatcher.Admit(tlprofile.Profile227, body, tlprofile.Limits{})
		if err != nil {
			t.Fatal(err)
		}
		return admitted
	}

	request := admit(&tg.HelpGetConfigRequest{})
	other := admit(&tg.HelpGetNearestDCRequest{})

	if _, err := bindAdmittedLayerRPCResult(request, &preparedOnlyLayerRPCResult{prepared: other.Prepared()}); !errors.Is(err, errLayerRPCResultIdentityMismatch) {
		t.Fatalf("mismatched result error = %v, want %v", err, errLayerRPCResultIdentityMismatch)
	}

	bound, err := bindAdmittedLayerRPCResult(request, &preparedOnlyLayerRPCResult{prepared: request.Prepared()})
	if err != nil {
		t.Fatal(err)
	}
	if bound == nil || bound.call.Identity() != request.Call().Identity() {
		t.Fatal("matching result did not retain the admitted call identity")
	}
}

type mismatchedProjectionLayerRPC struct {
	*admissionOnlyLayerRPC
	result tlprofile.Result
	calls  atomic.Int32
}

func (h *mismatchedProjectionLayerRPC) DispatchAdmitted(
	context.Context,
	[8]byte,
	int64,
	int64,
	uint64,
	tlprofile.Admission,
) (tlprofile.Result, string, error) {
	h.calls.Add(1)
	return h.result, "help.getConfig", nil
}

func TestProjectionFailureCachesInternalWithoutRepeatingBusiness(t *testing.T) {
	dispatcher := tlprofile.NewDispatcher()
	admit := func(request bin.Encoder) tlprofile.Admission {
		t.Helper()
		body := &bin.Buffer{Buf: exactLayerRPCBody(t, request)}
		admitted, err := dispatcher.Admit(tlprofile.Profile227, body, tlprofile.Limits{})
		if err != nil {
			t.Fatal(err)
		}
		return admitted
	}
	request := admit(&tg.HelpGetConfigRequest{})
	other := admit(&tg.HelpGetNearestDCRequest{})
	handler := &mismatchedProjectionLayerRPC{
		admissionOnlyLayerRPC: newAdmissionOnlyLayerRPC(),
		result:                &preparedOnlyLayerRPCResult{prepared: other.Prepared()},
	}
	s := New(Options{DC: 2, LayerRPC: handler})
	c := newOutboundTestConn(t, &collectingSessionTransport{}, newOutboundTrackedBudget(1<<20))
	c.authKeyID = [8]byte{0x41, 0x01}
	c.sessionID = 4101
	const reqMsgID = int64(410100)
	claim, err := s.rpcResults.AcquireLayerIdentified(
		c.authKeyID, c.sessionID, reqMsgID,
		tlprofile.Profile227, request.Prepared().Identity(),
	)
	if err != nil || claim.owner == nil {
		t.Fatalf("owner acquisition err=%v", err)
	}
	if err := s.handleAdmittedLayerRPC(
		context.Background(), c, reqMsgID, claim.admissionSeq,
		"help.getConfig", request, claim.owner,
	); err != nil {
		t.Fatalf("publish projection failure: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	var completed rpcResultAcquire
	for {
		completed, err = s.rpcResults.AcquireLayerIdentified(
			c.authKeyID, c.sessionID, reqMsgID,
			tlprofile.Profile227, request.Prepared().Identity(),
		)
		if err == nil && completed.state == rpcResultAcquireCompleted {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("projection failure did not become completed: state=%d err=%v", completed.state, err)
		}
		time.Sleep(time.Millisecond)
	}
	if !completed.executionKnown || !completed.executionOK {
		t.Fatalf("projection failure lost successful business outcome: known=%v ok=%v", completed.executionKnown, completed.executionOK)
	}
	if got := handler.calls.Load(); got != 1 {
		t.Fatalf("business calls=%d, want 1", got)
	}
	var envelope proto.Result
	if err := envelope.Decode(&bin.Buffer{Buf: completed.encoded.body}); err != nil {
		t.Fatal(err)
	}
	var rpcErr mt.RPCError
	if err := rpcErr.Decode(&bin.Buffer{Buf: envelope.Result}); err != nil {
		t.Fatal(err)
	}
	if rpcErr.ErrorCode != 500 || rpcErr.ErrorMessage != "INTERNAL" {
		t.Fatalf("projection terminal = %+v", rpcErr)
	}
	// A same-msg replay is served from the completed exact identity; there is no
	// second DispatchAdmitted call even though projection failed after business
	// success.
	replay, err := s.rpcResults.AcquireLayerIdentified(
		c.authKeyID, c.sessionID, reqMsgID,
		tlprofile.Profile227, request.Prepared().Identity(),
	)
	if err != nil || replay.state != rpcResultAcquireCompleted || replay.encoded != completed.encoded {
		t.Fatalf("projection replay = state:%d err:%v", replay.state, err)
	}
	if got := handler.calls.Load(); got != 1 {
		t.Fatalf("replay repeated business calls=%d", got)
	}
}
