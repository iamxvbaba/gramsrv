package mtprotoedge

import (
	"context"
	"errors"
	"time"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/mt"
	"github.com/iamxvbaba/td/tgerr"
	"go.uber.org/zap"

	"github.com/iamxvbaba/td/tlprofile"
	"telesrv/internal/observability/dbtrace"
	"telesrv/internal/postresponse"
)

// layerRPCResultEncoder keeps the generated result bound to the immutable
// admitted call, but deliberately does not prepare a second byte snapshot in
// the inbound worker. The only Encode call happens later under outbound encode
// and retained-byte admission.
type layerRPCResultEncoder struct {
	call   tlprofile.Call
	result tlprofile.Result
}

func (e *layerRPCResultEncoder) Encode(b *bin.Buffer) error {
	if e == nil {
		return errors.New("nil layer RPC result")
	}
	if e.result == nil {
		return errors.New("nil generated layer RPC result")
	}
	return e.result.Encode(b)
}

func (e *layerRPCResultEncoder) exactLayerRPCResultBinding() outboundLayerBinding {
	if e == nil {
		return outboundLayerBinding{}
	}
	return outboundLayerBinding{
		profile:       e.call.Profile(),
		wireInvariant: e.call.WireInvariant(),
		kind:          outboundLayerBindingRequest,
	}
}

type exactLayerRPCResultEncoder interface {
	bin.Encoder
	exactLayerRPCResultBinding() outboundLayerBinding
}

// legacyTestRPCResultEncoder migrates the unexported legacyRPC package-test
// hook onto the generated result codec. The hook may still exercise the old
// scheduling API, but it no longer has a canonical-bytes escape hatch.
type legacyTestRPCResultEncoder struct {
	call   tlprofile.Call
	result bin.Encoder
}

func (e *legacyTestRPCResultEncoder) Encode(b *bin.Buffer) error {
	if e == nil || e.result == nil {
		return errors.New("nil legacy test RPC result")
	}
	return e.result.Encode(b)
}

func (e *legacyTestRPCResultEncoder) exactLayerRPCResultBinding() outboundLayerBinding {
	if e == nil {
		return outboundLayerBinding{}
	}
	return outboundLayerBinding{
		profile:       e.call.Profile(),
		wireInvariant: e.call.WireInvariant(),
		kind:          outboundLayerBindingRequest,
	}
}

var errLayerRPCResultIdentityMismatch = errors.New("layer RPC result does not match admitted request identity")

// bindAdmittedLayerRPCResult closes the integration boundary around the
// generated dispatcher. A LayerRPCHandler implementation must return the
// result capability created from this exact admission; accepting a result from
// another request would pair the wrong result TypeRef/profile with this
// flight/cache identity even when both methods happen to share a Go type.
func bindAdmittedLayerRPCResult(request tlprofile.Admission, result tlprofile.Result) (*layerRPCResultEncoder, error) {
	if result == nil {
		return nil, nil
	}
	if result.Prepared().Identity() != request.Prepared().Identity() {
		return nil, errLayerRPCResultIdentityMismatch
	}
	return &layerRPCResultEncoder{call: request.Call(), result: result}, nil
}

func (s *Server) newInboundLayerRPCTask(
	c *Conn,
	msgID int64,
	admissionSeq uint64,
	method string,
	profileEvidenceFresh bool,
	request tlprofile.Admission,
	dependencies layerRPCDependencySet,
	owner *rpcResultOwnerLease,
) inboundRPC {
	wireSize := request.Prepared().WireSize()
	gate := newLayerRPCExecutionGate(c, dependencies)
	timeoutResponse := func() {
		writeTimeout := c.writeTimeout
		if writeTimeout <= 0 || writeTimeout > 5*time.Second {
			writeTimeout = 5 * time.Second
		}
		responseCtx, cancel := context.WithTimeout(context.Background(), writeTimeout)
		defer cancel()
		if sendErr := s.sendResult(responseCtx, c, msgID, &mt.RPCError{
			ErrorCode: 500, ErrorMessage: layerRPCTimeoutMessage(gate),
		}); sendErr != nil && !isClientDisconnect(sendErr) {
			s.log.Debug("Send RPC timeout failed",
				zap.String("method", method), zap.Int64("msg_id", msgID),
				zap.String("auth_key_id", c.authKeyHex), zap.Int64("session_id", c.sessionID),
				zap.Error(sendErr))
		}
	}
	return inboundRPC{
		method:    method,
		size:      wireSize,
		onTimeout: timeoutResponse,
		release: func() {
			if owner != nil && owner.Abort() {
				c.fenceUndeliveredRPCResult()
			}
		},
		gate: gate,
		run: func(taskCtx context.Context) error {
			if gate != nil && !gate.success() {
				return s.publishAdmittedLayerRPCResult(c, msgID, method, owner, false, &mt.RPCError{
					ErrorCode: 500, ErrorMessage: "MSG_WAIT_FAILED",
				}, nil)
			}
			if err := s.handleAdmittedLayerRPC(s.withLayerRPCProfileEvidenceFresh(taskCtx, profileEvidenceFresh), c, msgID, admissionSeq, method, request, owner); err != nil {
				fields := []zap.Field{
					zap.Int64("msg_id", msgID), zap.String("auth_key_id", c.authKeyHex),
					zap.Int64("session_id", c.sessionID), zap.Error(err),
				}
				if isClientDisconnect(err) {
					s.log.Debug("RPC async handler canceled", fields...)
				} else {
					s.log.Info("RPC async handler failed", fields...)
				}
				return err
			}
			return nil
		},
	}
}

func layerRPCTimeoutMessage(gate *inboundRPCGate) string {
	if gate != nil && !gate.runnable() {
		return "MSG_WAIT_TIMEOUT"
	}
	return "RPC_TIMEOUT"
}

func newLayerRPCExecutionGate(c *Conn, dependencies layerRPCDependencySet) *inboundRPCGate {
	if len(dependencies.waiters) == 0 && !dependencies.failed {
		return nil
	}
	gate := newInboundRPCGate(len(dependencies.waiters), c.wakeInboundRPC)
	if dependencies.failed {
		gate.failed.Store(true)
	}
	for _, waiter := range dependencies.waiters {
		if err := waiter.SubscribeExecution(gate.resolve); err != nil {
			gate.resolve(false)
		}
	}
	// Release the subscriber-installation sentinel.
	gate.resolve(true)
	return gate
}

func (s *Server) publishAdmittedLayerRPCResult(
	c *Conn,
	msgID int64,
	method string,
	owner *rpcResultOwnerLease,
	success bool,
	result bin.Encoder,
	after func(),
) error {
	if owner != nil {
		owner.CompleteExecution(success)
	}
	return s.publishRPCResult(c, msgID, method, owner, result, after)
}

func (s *Server) handleAdmittedLayerRPC(
	ctx context.Context,
	c *Conn,
	msgID int64,
	admissionSeq uint64,
	method string,
	request tlprofile.Admission,
	owner *rpcResultOwnerLease,
) error {
	if s.layerRPC == nil {
		return s.publishAdmittedLayerRPCResult(c, msgID, method, owner, false, &mt.RPCError{
			ErrorCode: 500, ErrorMessage: "NOT_IMPLEMENTED",
		}, nil)
	}
	ctx = postresponse.WithCallbacks(ctx)
	ctx, dbStats := dbtrace.WithStats(ctx)
	start := s.clock.Now()
	result, effectiveMethod, err := s.layerRPC.DispatchAdmitted(ctx, c.authKeyID, c.sessionID, msgID, admissionSeq, request)
	businessSucceeded := err == nil
	if effectiveMethod == "" {
		effectiveMethod = method
	}
	var exact *layerRPCResultEncoder
	if err == nil && result != nil {
		exact, err = bindAdmittedLayerRPCResult(request, result)
	}
	dur := s.clock.Now().Sub(start)
	s.metrics.RPCHandled(effectiveMethod, dur, err)
	fields := []zap.Field{
		zap.String("method", effectiveMethod), zap.String("auth_key_id", c.authKeyHex),
		zap.Int64("session_id", c.sessionID), zap.Int64("msg_id", msgID),
		zap.Int("profile", int(request.Call().Profile())), zap.Duration("dur", dur),
	}
	if effectiveMethod != method {
		fields = append(fields, zap.String("outer_method", method))
	}
	if businessAuthKeyHex, ok := c.BusinessAuthKeyHex(); ok {
		fields = append(fields, zap.String("business_auth_key_id", businessAuthKeyHex))
	}
	if userID := c.UserID(); userID != 0 {
		fields = append(fields, zap.Int64("user_id", userID))
	}
	fields = dbtrace.AppendZapFields(fields, "", dbStats.Snapshot())

	if ctxErr := ctx.Err(); ctxErr != nil {
		var terminal bin.Encoder
		var after func()
		if err == nil && exact != nil {
			terminal = exact
			after = postresponse.Take(context.WithoutCancel(ctx))
		} else if errors.Is(ctxErr, context.DeadlineExceeded) {
			terminal = &mt.RPCError{ErrorCode: 500, ErrorMessage: "RPC_TIMEOUT"}
		}
		if terminal != nil {
			if sendErr := s.publishAdmittedLayerRPCResult(c, msgID, effectiveMethod, owner, err == nil && exact != nil, terminal, after); sendErr != nil {
				s.log.Debug("Publish canceled RPC result failed", append(fields, zap.Error(sendErr))...)
			}
		}
		s.log.Info("RPC canceled", append(fields, zap.NamedError("context_error", ctxErr))...)
		return ctxErr
	}
	if err != nil {
		var rpcErr *tgerr.Error
		if errors.As(err, &rpcErr) {
			s.log.Info("RPC error", append(fields, zap.Int("code", rpcErr.Code), zap.String("error", rpcErr.Message))...)
			return s.publishAdmittedLayerRPCResult(c, msgID, effectiveMethod, owner, businessSucceeded, &mt.RPCError{
				ErrorCode: rpcErr.Code, ErrorMessage: rpcErr.Message,
			}, nil)
		}
		s.log.Info("RPC internal error", append(fields, zap.Error(err))...)
		return s.publishAdmittedLayerRPCResult(c, msgID, effectiveMethod, owner, businessSucceeded, &mt.RPCError{
			ErrorCode: 500, ErrorMessage: "INTERNAL",
		}, nil)
	}
	if exact == nil {
		return s.publishAdmittedLayerRPCResult(c, msgID, effectiveMethod, owner, businessSucceeded, &mt.RPCError{
			ErrorCode: 500, ErrorMessage: "INTERNAL",
		}, nil)
	}
	s.log.Info("RPC handled", fields...)
	return s.publishAdmittedLayerRPCResult(c, msgID, effectiveMethod, owner, true, exact, postresponse.Take(ctx))
}

var _ exactLayerRPCResultEncoder = (*layerRPCResultEncoder)(nil)
