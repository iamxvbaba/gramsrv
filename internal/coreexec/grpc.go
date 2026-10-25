package coreexec

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/tgerr"
	"github.com/iamxvbaba/td/tlprofile"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"telesrv/internal/coreexec/coreexecpb"
	"telesrv/internal/observability/dbtrace"
	"telesrv/internal/postresponse"
	"telesrv/internal/store"
)

const (
	defaultGRPCClientTimeout                = 5 * time.Second
	defaultMaxGRPCMessageSize               = 16 << 20
	accountTeardownCommitMaxAttempts        = 3
	accountTeardownCommitInitialBackoff     = 25 * time.Millisecond
	coreExecGRPCProtocolVersion             = 5
	coreExecGRPCMinSupportedProtocolVersion = 5
	coreExecGRPCRequestIDMetadataKey        = "x-telesrv-coreexec-request-id"
	coreExecGRPCRequestIDBytes              = 16
)

var coreExecGRPCCapabilities = []string{
	"tl-wire-passthrough",
	"updates-lifecycle-fencing",
	"dispatch-admitted",
	"profile-evidence",
	"identity-hint",
	"session-updates-readiness-hint",
	"session-offline-batch",
	"init-connection-observer",
	"replay-prepare-commit",
	"auth-key-authority",
}

var coreExecGRPCRequiredCapabilities = []string{
	"auth-key-authority",
	"session-offline-batch",
	"session-updates-readiness-hint",
	"updates-lifecycle-fencing",
}

var (
	ErrGRPCAddrMissing    = errors.New("coreexec grpc: core addr is empty")
	ErrGRPCTargetsMissing = errors.New("coreexec grpc: targets are empty")
	ErrGRPCTokenMissing   = errors.New("coreexec grpc: bearer token is required")
)

var (
	coreExecGRPCRequestIDPrefix = newCoreExecGRPCRequestIDPrefix()
	coreExecGRPCRequestIDSeq    atomic.Uint64
)

type coreExecGRPCRequestIDContextKey struct{}

type GRPCServerConfig struct {
	Addr              string
	InstanceID        string
	Token             string
	TLSCertFile       string
	TLSKeyFile        string
	TLSClientCAFile   string
	Handler           Handler
	AuthKeys          store.AuthKeyStore
	AuthInvalidations store.AuthInvalidationBroker
	Logger            *zap.Logger
	Metrics           Metrics
	MaxRecvMsgBytes   int
	MaxSendMsgBytes   int
}

type GRPCClientConfig struct {
	Targets         []string
	Resolver        GRPCResolverProvider
	ResolverKind    string
	Token           string
	Logger          *zap.Logger
	RequestTimeout  time.Duration
	TLSCAFile       string
	TLSServerName   string
	TLSCertFile     string
	TLSKeyFile      string
	Metrics         Metrics
	MaxRecvMsgBytes int
	MaxSendMsgBytes int
	DialOptions     []grpc.DialOption
	InstanceID      string
}

type GRPCRemote struct {
	// edgeCodec is the Edge-side TL profile/admission shell. It validates and
	// captures request bytes for CoreExec, but GRPCRemote never exposes it as a
	// business execution fallback.
	edgeCodec *Local

	client coreexecpb.CoreExecServiceClient
	health healthpb.HealthClient

	requestTimeout time.Duration
	metrics        Metrics
	log            *zap.Logger
	instanceID     string

	mu                   sync.Mutex
	pending              map[tlprofile.PreparedIdentity][]capturedAdmission
	pendingCount         int
	pendingAdmissionTTL  time.Duration
	maxPendingAdmissions int
}

func NewGRPCRemote(edgeCodec Handler, client coreexecpb.CoreExecServiceClient, healthClient healthpb.HealthClient) *GRPCRemote {
	return &GRPCRemote{
		edgeCodec:            NewLocal(edgeCodec),
		client:               client,
		health:               healthClient,
		requestTimeout:       defaultGRPCClientTimeout,
		metrics:              NopMetrics{},
		log:                  zap.NewNop(),
		pending:              make(map[tlprofile.PreparedIdentity][]capturedAdmission),
		pendingAdmissionTTL:  defaultPendingAdmissionTTL,
		maxPendingAdmissions: defaultMaxPendingAdmissions,
	}
}

func DialGRPCRemote(ctx context.Context, edgeCodec Handler, cfg GRPCClientConfig) (*GRPCRemote, *grpc.ClientConn, error) {
	if strings.TrimSpace(cfg.Token) == "" {
		return nil, nil, ErrGRPCTokenMissing
	}
	resolverProvider, err := grpcResolverProvider(cfg)
	if err != nil {
		return nil, nil, err
	}
	target := strings.TrimSpace(resolverProvider.Target())
	if target == "" {
		return nil, nil, ErrGRPCTargetsMissing
	}
	opts, err := grpcClientDialOptions(cfg, resolverProvider)
	if err != nil {
		return nil, nil, err
	}
	conn, err := grpc.NewClient(target, opts...)
	if err != nil {
		return nil, nil, fmt.Errorf("coreexec grpc client: %w", err)
	}
	remote := NewGRPCRemote(edgeCodec, coreexecpb.NewCoreExecServiceClient(conn), healthpb.NewHealthClient(conn))
	remote.requestTimeout = grpcClientRequestTimeout(cfg.RequestTimeout)
	remote.metrics = coreExecMetrics(cfg.Metrics)
	if cfg.Logger != nil {
		remote.log = cfg.Logger
	}
	remote.instanceID = strings.TrimSpace(cfg.InstanceID)
	if ctx == nil {
		ctx = context.Background()
	}
	if err := remote.Check(ctx); err != nil {
		_ = conn.Close()
		return nil, nil, err
	}
	return remote, conn, nil
}

func StartGRPC(ctx context.Context, cfg GRPCServerConfig) (*grpc.Server, error) {
	if strings.TrimSpace(cfg.Addr) == "" {
		return nil, ErrGRPCAddrMissing
	}
	if strings.TrimSpace(cfg.Token) == "" {
		return nil, ErrGRPCTokenMissing
	}
	if cfg.Handler == nil {
		return nil, ErrNilHandler
	}
	if cfg.Logger == nil {
		cfg.Logger = zap.NewNop()
	}
	transportCreds, tlsEnabled, err := grpcServerTransportCredentials(cfg)
	if err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", cfg.Addr)
	if err != nil {
		return nil, fmt.Errorf("listen coreexec grpc %s: %w", cfg.Addr, err)
	}
	opts := []grpc.ServerOption{
		grpc.ChainUnaryInterceptor(
			grpcRequestIDUnaryServerInterceptor(cfg.Logger),
			grpcMetricsUnaryServerInterceptor(cfg.Metrics),
			grpcBearerUnaryServerInterceptor(cfg.Token),
		),
		grpc.MaxRecvMsgSize(grpcMessageSize(cfg.MaxRecvMsgBytes)),
		grpc.MaxSendMsgSize(grpcMessageSize(cfg.MaxSendMsgBytes)),
	}
	if tlsEnabled {
		opts = append(opts, grpc.Creds(transportCreds))
	}
	srv := grpc.NewServer(opts...)
	coreexecpb.RegisterCoreExecServiceServer(srv, newCoreExecGRPCServerWithRuntime(cfg.Handler, cfg.InstanceID, cfg.AuthKeys, cfg.AuthInvalidations, cfg.Metrics))
	healthSrv := health.NewServer()
	healthSrv.SetServingStatus(coreexecpb.CoreExecService_ServiceDesc.ServiceName, healthpb.HealthCheckResponse_SERVING)
	healthpb.RegisterHealthServer(srv, healthSrv)
	go func() {
		<-ctx.Done()
		healthSrv.Shutdown()
		stopped := make(chan struct{})
		go func() {
			srv.GracefulStop()
			close(stopped)
		}()
		select {
		case <-stopped:
		case <-time.After(2 * time.Second):
			srv.Stop()
		}
	}()
	go func() {
		cfg.Logger.Info("coreexec grpc listening", zap.String("addr", ln.Addr().String()))
		if err := srv.Serve(ln); err != nil {
			cfg.Logger.Warn("coreexec grpc exited", zap.Error(err))
		}
	}()
	return srv, nil
}

func (r *GRPCRemote) Check(ctx context.Context) error {
	if r == nil || r.client == nil {
		return ErrRemoteRPCUnavailable
	}
	checkCtx, cancel := r.withRequestTimeout(ctx)
	defer cancel()
	if r.health == nil {
		return r.checkInfo(checkCtx)
	}
	start := time.Now()
	res, err := r.health.Check(checkCtx, &healthpb.HealthCheckRequest{
		Service: coreexecpb.CoreExecService_ServiceDesc.ServiceName,
	})
	if err != nil {
		callErr := grpcRemoteCallError("health", err)
		observeCoreExecGRPCCall(r.metrics, "client", "health", start, grpcRemoteCallOutcome(callErr))
		return callErr
	}
	if res.GetStatus() != healthpb.HealthCheckResponse_SERVING {
		err := fmt.Errorf("coreexec grpc health: status %s", res.GetStatus())
		observeCoreExecGRPCCall(r.metrics, "client", "health", start, "not_serving")
		return err
	}
	observeCoreExecGRPCCall(r.metrics, "client", "health", start, "ok")
	return r.checkInfo(checkCtx)
}

func (r *GRPCRemote) checkInfo(ctx context.Context) error {
	start := time.Now()
	res, err := r.client.GetInfo(ctx, &coreexecpb.CoreExecInfoRequest{
		ProtocolVersion:             coreExecGRPCProtocolVersion,
		MinSupportedProtocolVersion: coreExecGRPCMinSupportedProtocolVersion,
		Capabilities:                coreExecGRPCCapabilitiesCopy(),
	})
	if err != nil {
		callErr := grpcRemoteCallError("get_info", err)
		observeCoreExecGRPCCall(r.metrics, "client", "get_info", start, grpcRemoteCallOutcome(callErr))
		return callErr
	}
	if res.GetError() != "" {
		err := fmt.Errorf("%w: %s", ErrGRPCProtocolVersionMismatch, res.GetError())
		observeCoreExecGRPCCall(r.metrics, "client", "get_info", start, grpcRemoteCallOutcome(err))
		return err
	}
	if !coreExecProtocolRangesOverlap(
		coreExecGRPCMinSupportedProtocolVersion,
		coreExecGRPCProtocolVersion,
		res.GetMinSupportedProtocolVersion(),
		res.GetProtocolVersion(),
	) {
		err := fmt.Errorf("%w: edge=%d..%d core=%d..%d",
			ErrGRPCProtocolVersionMismatch,
			coreExecGRPCMinSupportedProtocolVersion,
			coreExecGRPCProtocolVersion,
			res.GetMinSupportedProtocolVersion(),
			res.GetProtocolVersion(),
		)
		observeCoreExecGRPCCall(r.metrics, "client", "get_info", start, grpcRemoteCallOutcome(err))
		return err
	}
	if missing := missingCoreExecGRPCCapabilities(res.GetCapabilities(), coreExecGRPCRequiredCapabilities); len(missing) > 0 {
		err := fmt.Errorf("%w: missing capabilities %s", ErrGRPCProtocolVersionMismatch, strings.Join(missing, ","))
		observeCoreExecGRPCCall(r.metrics, "client", "get_info", start, grpcRemoteCallOutcome(err))
		return err
	}
	observeCoreExecGRPCCall(r.metrics, "client", "get_info", start, "ok")
	return nil
}

func (r *GRPCRemote) AdmitLayer(profile tlprofile.Profile, b *bin.Buffer, limits tlprofile.Limits) (tlprofile.Admission, error) {
	return r.AdmitLayerWithOptions(profile, b, tlprofile.AdmissionOptions{Limits: limits})
}

func (r *GRPCRemote) AdmitLayerWithOptions(profile tlprofile.Profile, b *bin.Buffer, options tlprofile.AdmissionOptions) (tlprofile.Admission, error) {
	raw := copyBufferRaw(b)
	admitted, err := r.edgeAdmitter().AdmitLayerWithOptions(profile, b, options)
	if err != nil {
		return tlprofile.Admission{}, err
	}
	if err := r.remember(admitted, capturedAdmission{
		Mode:    AdmissionModeLayer,
		Profile: profile,
		Limits:  options.Limits,
		Wire:    raw,
		Proof:   proofFromAdmission(admitted),
	}); err != nil {
		return tlprofile.Admission{}, err
	}
	return admitted, nil
}

func (r *GRPCRemote) AdmitDefaultLayer(profile tlprofile.Profile, b *bin.Buffer, limits tlprofile.Limits) (tlprofile.Admission, error) {
	return r.AdmitDefaultLayerWithOptions(profile, b, tlprofile.AdmissionOptions{Limits: limits})
}

func (r *GRPCRemote) AdmitDefaultLayerWithOptions(profile tlprofile.Profile, b *bin.Buffer, options tlprofile.AdmissionOptions) (tlprofile.Admission, error) {
	raw := copyBufferRaw(b)
	admitted, err := r.edgeAdmitter().AdmitDefaultLayerWithOptions(profile, b, options)
	if err != nil {
		return tlprofile.Admission{}, err
	}
	if err := r.remember(admitted, capturedAdmission{
		Mode:    AdmissionModeDefault,
		Profile: profile,
		Limits:  options.Limits,
		Wire:    raw,
		Proof:   proofFromAdmission(admitted),
	}); err != nil {
		return tlprofile.Admission{}, err
	}
	return admitted, nil
}

func (r *GRPCRemote) AdmitUnprofiled(b *bin.Buffer, limits tlprofile.Limits) (tlprofile.Admission, error) {
	return r.AdmitUnprofiledWithOptions(b, tlprofile.AdmissionOptions{Limits: limits})
}

func (r *GRPCRemote) AdmitUnprofiledWithOptions(b *bin.Buffer, options tlprofile.AdmissionOptions) (tlprofile.Admission, error) {
	raw := copyBufferRaw(b)
	admitted, err := r.edgeAdmitter().AdmitUnprofiledWithOptions(b, options)
	if err != nil {
		return tlprofile.Admission{}, err
	}
	if err := r.remember(admitted, capturedAdmission{
		Mode:   AdmissionModeUnprofiled,
		Limits: options.Limits,
		Wire:   raw,
		Proof:  proofFromAdmission(admitted),
	}); err != nil {
		return tlprofile.Admission{}, err
	}
	return admitted, nil
}

func (r *GRPCRemote) DispatchAdmitted(ctx context.Context, authKeyID [8]byte, sessionID int64, msgID int64, admissionSeq uint64, request tlprofile.Admission) (tlprofile.Result, string, error) {
	if r == nil || r.client == nil {
		return nil, "", ErrRemoteRPCUnavailable
	}
	captured, ok := r.take(request.Prepared().Identity())
	if !ok {
		return nil, "", ErrRemoteAdmissionLost
	}
	if captured.Proof != proofFromAdmission(request) {
		return nil, "", ErrWireProofMismatch
	}
	callCtx, cancel := r.withRequestTimeout(ctx)
	defer cancel()
	start := time.Now()
	res, err := r.client.DispatchAdmitted(callCtx, grpcDispatchRequest(&captured, authKeyID, sessionID, msgID, admissionSeq, profileEvidenceFresh(ctx), identityHintToPB(ctx), clientIPToPB(ctx)))
	if err != nil {
		callErr := grpcRemoteCallError("dispatch_admitted", err)
		observeCoreExecGRPCCall(r.metrics, "client", "dispatch_admitted", start, grpcRemoteCallOutcome(callErr))
		return nil, "", callErr
	}
	if rpcErr := res.GetRpcError(); rpcErr != nil {
		observeCoreExecGRPCCall(r.metrics, "client", "dispatch_admitted", start, "rpc_error")
		return nil, res.GetMethod(), tgerr.New(int(rpcErr.GetCode()), rpcErr.GetMessage())
	}
	if res.GetError() != "" {
		err := remoteControlError(res.GetError())
		observeCoreExecGRPCCall(r.metrics, "client", "dispatch_admitted", start, grpcRemoteResponseErrorOutcome(err))
		return nil, res.GetMethod(), err
	}
	actions, err := postResponseActionsFromPB(res.GetPostResponseActions())
	if err != nil {
		controlErr := remoteControlError(err.Error())
		observeCoreExecGRPCCall(r.metrics, "client", "dispatch_admitted", start, grpcRemoteResponseErrorOutcome(controlErr))
		return nil, res.GetMethod(), controlErr
	}
	for _, action := range actions {
		if postresponse.RegisterAction(ctx, action) {
			continue
		}
		if action.Kind == postresponse.ActionAccountAuthorizationTeardown {
			controlErr := remoteControlError("coreexec grpc: account authorization teardown requires edge post-response delivery")
			observeCoreExecGRPCCall(r.metrics, "client", "dispatch_admitted", start, grpcRemoteResponseErrorOutcome(controlErr))
			return nil, res.GetMethod(), controlErr
		}
		if err := r.RunPostResponseActions(context.Background(), []postresponse.Action{action}); err != nil {
			controlErr := remoteControlError(err.Error())
			observeCoreExecGRPCCall(r.metrics, "client", "dispatch_admitted", start, grpcRemoteResponseErrorOutcome(controlErr))
			return nil, res.GetMethod(), controlErr
		}
	}
	observeCoreExecGRPCCall(r.metrics, "client", "dispatch_admitted", start, "ok")
	// grpc-go has completed unary response unmarshalling and will not reuse or
	// mutate this message. Transfer its owned result slice directly into the
	// request-bound result instead of cloning the complete TL payload.
	resultWire := res.ResultWire
	res.ResultWire = nil
	return &encodedResult{
		prepared:      request.Prepared(),
		wireInvariant: res.GetWireInvariant(),
		body:          resultWire,
	}, res.GetMethod(), nil
}

func commitGRPCRemoteReplayToken(r *GRPCRemote, token, operation string) error {
	if r == nil || r.client == nil {
		return ErrRemoteRPCUnavailable
	}
	commitCtx, cancel := r.withRequestTimeout(context.Background())
	defer cancel()
	start := time.Now()
	commitRes, err := r.client.CommitAdmittedReplay(commitCtx, &coreexecpb.CommitReplayRequest{Token: token})
	commitErr := grpcRemoteCallError(operation, err)
	outcome := grpcRemoteCallOutcome(commitErr)
	if commitErr == nil && commitRes.GetError() != "" {
		commitErr = remoteControlError(commitRes.GetError())
		outcome = grpcRemoteResponseErrorOutcome(commitErr)
	}
	observeCoreExecGRPCCall(r.metrics, "client", operation, start, outcome)
	return commitErr
}

func (r *GRPCRemote) RunPostResponseActions(ctx context.Context, actions []postresponse.Action) error {
	if len(actions) == 0 {
		return nil
	}
	if r == nil || r.client == nil {
		return ErrRemoteRPCUnavailable
	}
	// Validate the complete batch before committing any subset. Account teardown
	// is the only idempotent action with an explicit retry contract; separating
	// it prevents an ambiguous transport failure from replaying cursor/readiness
	// transitions that may already have committed on Core.
	if _, err := postResponseActionsToPB(actions); err != nil {
		return err
	}
	ordinary, accountTeardowns := partitionPostResponseActions(actions)
	var firstErr error
	if len(ordinary) != 0 {
		firstErr, _ = r.commitPostResponseActionsOnce(ctx, ordinary)
	}
	if len(accountTeardowns) != 0 {
		if err := r.commitAccountTeardownActions(ctx, accountTeardowns); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (r *GRPCRemote) commitPostResponseActionsOnce(ctx context.Context, actions []postresponse.Action) (error, bool) {
	pbActions, err := postResponseActionsToPB(actions)
	if err != nil {
		return err, false
	}
	callCtx, cancel := r.withRequestTimeout(ctx)
	defer cancel()
	start := time.Now()
	res, err := r.client.CommitPostResponseActions(callCtx, &coreexecpb.CommitPostResponseActionsRequest{Actions: pbActions})
	retryable := accountTeardownCommitRPCErrorRetryable(err, callCtx.Err())
	callErr := grpcRemoteCallError("commit_post_response_actions", err)
	outcome := grpcRemoteCallOutcome(callErr)
	if callErr == nil && res.GetError() != "" {
		callErr = remoteControlError(res.GetError())
		outcome = grpcRemoteResponseErrorOutcome(callErr)
		retryable = false
	}
	observeCoreExecGRPCCall(r.metrics, "client", "commit_post_response_actions", start, outcome)
	return callErr, retryable
}

func (r *GRPCRemote) commitAccountTeardownActions(ctx context.Context, actions []postresponse.Action) error {
	var lastErr error
	for attempt := 1; attempt <= accountTeardownCommitMaxAttempts; attempt++ {
		var retryable bool
		lastErr, retryable = r.commitPostResponseActionsOnce(ctx, actions)
		if lastErr == nil {
			return nil
		}
		if !retryable || attempt == accountTeardownCommitMaxAttempts {
			r.log.Error("commit account authorization teardown post-response failed",
				zap.Int("attempts", attempt),
				zap.Error(lastErr))
			return lastErr
		}
		r.log.Warn("retry account authorization teardown post-response",
			zap.Int("attempt", attempt),
			zap.Int("max_attempts", accountTeardownCommitMaxAttempts),
			zap.Error(lastErr))
		if err := waitAccountTeardownCommitRetry(ctx, attempt); err != nil {
			r.log.Error("commit account authorization teardown post-response retry canceled",
				zap.Int("attempts", attempt),
				zap.Error(err))
			return err
		}
	}
	return lastErr
}

// accountTeardownCommitRPCErrorRetryable intentionally inspects the raw gRPC
// result rather than the public CoreExec error classification. Control-response
// errors and statuses such as Internal/Aborted/ResourceExhausted can all map to
// ErrRemoteRPCUnavailable for callers, but none proves a transient transport
// failure and therefore none is safe to retry here.
func accountTeardownCommitRPCErrorRetryable(rpcErr error, callCtxErr error) bool {
	if rpcErr == nil {
		return false
	}
	switch status.Code(rpcErr) {
	case codes.Unavailable, codes.DeadlineExceeded:
		return true
	default:
		return errors.Is(callCtxErr, context.DeadlineExceeded)
	}
}

func partitionPostResponseActions(actions []postresponse.Action) (ordinary, accountTeardowns []postresponse.Action) {
	ordinary = make([]postresponse.Action, 0, len(actions))
	accountTeardowns = make([]postresponse.Action, 0, len(actions))
	for _, action := range actions {
		if action.Kind == postresponse.ActionAccountAuthorizationTeardown {
			accountTeardowns = append(accountTeardowns, action)
			continue
		}
		ordinary = append(ordinary, action)
	}
	return ordinary, accountTeardowns
}

func waitAccountTeardownCommitRetry(ctx context.Context, attempt int) error {
	if ctx == nil {
		ctx = context.Background()
	}
	delay := accountTeardownCommitInitialBackoff << (attempt - 1)
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *GRPCRemote) PrepareAdmittedReplay(ctx context.Context, authKeyID [8]byte, sessionID int64, msgID int64, admissionSeq uint64, request tlprofile.Admission) (func() error, error) {
	if r == nil || r.client == nil {
		return nil, ErrRemoteRPCUnavailable
	}
	captured, ok := r.peek(request.Prepared().Identity())
	if !ok {
		return nil, ErrRemoteAdmissionLost
	}
	if captured.Proof != proofFromAdmission(request) {
		return nil, ErrWireProofMismatch
	}
	callCtx, cancel := r.withRequestTimeout(ctx)
	defer cancel()
	start := time.Now()
	res, err := r.client.PrepareAdmittedReplay(callCtx, grpcDispatchRequest(&captured, authKeyID, sessionID, msgID, admissionSeq, profileEvidenceFresh(ctx), identityHintToPB(ctx), clientIPToPB(ctx)))
	if err != nil {
		callErr := grpcRemoteCallError("prepare_admitted_replay", err)
		observeCoreExecGRPCCall(r.metrics, "client", "prepare_admitted_replay", start, grpcRemoteCallOutcome(callErr))
		return nil, callErr
	}
	if res.GetError() != "" {
		err := remoteControlError(res.GetError())
		observeCoreExecGRPCCall(r.metrics, "client", "prepare_admitted_replay", start, grpcRemoteResponseErrorOutcome(err))
		return nil, err
	}
	observeCoreExecGRPCCall(r.metrics, "client", "prepare_admitted_replay", start, "ok")
	if res.GetToken() == "" {
		return nil, nil
	}
	var once sync.Once
	var commitErr error
	return func() error {
		once.Do(func() {
			commitErr = commitGRPCRemoteReplayToken(r, res.GetToken(), "commit_admitted_replay")
		})
		return commitErr
	}, nil
}

func (r *GRPCRemote) withRequestTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	if _, ok := ctx.Deadline(); ok {
		return ctx, func() {}
	}
	timeout := defaultGRPCClientTimeout
	if r != nil && r.requestTimeout > 0 {
		timeout = r.requestTimeout
	}
	return context.WithTimeout(ctx, timeout)
}

func (r *GRPCRemote) WithLayerRPCProfileEvidenceFresh(ctx context.Context, fresh bool) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if local := r.edgeAdmitter(); local != nil {
		ctx = local.WithLayerRPCProfileEvidenceFresh(ctx, fresh)
	}
	return context.WithValue(ctx, profileEvidenceFreshKey{}, fresh)
}

func (r *GRPCRemote) edgeAdmitter() *Local {
	if r == nil {
		return nil
	}
	return r.edgeCodec
}

func (r *GRPCRemote) LayerRPCFlatBytesPayloadSize(wire []byte) (int, bool) {
	return r.edgeAdmitter().LayerRPCFlatBytesPayloadSize(wire)
}

func (r *GRPCRemote) NegotiatedSessionLayer(authKeyID [8]byte, sessionID int64) (int, bool) {
	return r.edgeAdmitter().NegotiatedSessionLayer(authKeyID, sessionID)
}

func (r *GRPCRemote) NegotiatedSessionLayerEvidence(authKeyID [8]byte, sessionID int64) (layer int, msgID int64, ok bool) {
	return r.edgeAdmitter().NegotiatedSessionLayerEvidence(authKeyID, sessionID)
}

func (r *GRPCRemote) FreezeNegotiatedSessionLayer(authKeyID [8]byte, sessionID int64, layer int) error {
	return r.edgeAdmitter().FreezeNegotiatedSessionLayer(authKeyID, sessionID, layer)
}

func (r *GRPCRemote) FreezeNegotiatedSessionLayerAt(authKeyID [8]byte, sessionID int64, layer int, msgID int64) (bool, error) {
	return r.edgeAdmitter().FreezeNegotiatedSessionLayerAt(authKeyID, sessionID, layer, msgID)
}

func (r *GRPCRemote) ForgetNegotiatedSessionLayer(authKeyID [8]byte, sessionID int64) {
	r.edgeAdmitter().ForgetNegotiatedSessionLayer(authKeyID, sessionID)
}

func (r *GRPCRemote) ForgetNegotiatedAuthKey(authKeyID [8]byte) {
	r.edgeAdmitter().ForgetNegotiatedAuthKey(authKeyID)
}

func (r *GRPCRemote) remember(admitted tlprofile.Admission, captured capturedAdmission) error {
	if r == nil {
		return nil
	}
	now := time.Now()
	captured.CreatedAt = now
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cleanupPendingLocked(now)
	limit := pendingAdmissionLimit(r.maxPendingAdmissions)
	if r.pendingCount >= limit {
		observeCoreExecPendingAdmissionRejected(r.metrics, "grpc", "capacity")
		return ErrRemoteAdmissionCapacity
	}
	identity := admitted.Prepared().Identity()
	r.pending[identity] = append(r.pending[identity], captured)
	r.pendingCount++
	return nil
}

func (r *GRPCRemote) take(identity tlprofile.PreparedIdentity) (capturedAdmission, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	queue := r.pending[identity]
	if len(queue) == 0 {
		return capturedAdmission{}, false
	}
	captured := queue[0]
	if len(queue) == 1 {
		delete(r.pending, identity)
	} else {
		queue[0] = capturedAdmission{}
		r.pending[identity] = queue[1:]
	}
	r.pendingCount--
	return captured, true
}

// DiscardAdmitted releases wire bytes captured by the Edge codec shell when a
// higher-level data-plane router takes ownership of the already-admitted call.
// It is intentionally one-way: a discarded admission can never be dispatched
// through CoreExec afterwards.
func (r *GRPCRemote) DiscardAdmitted(identity tlprofile.PreparedIdentity) bool {
	if r == nil {
		return false
	}
	_, ok := r.take(identity)
	return ok
}

func (r *GRPCRemote) peek(identity tlprofile.PreparedIdentity) (capturedAdmission, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	queue := r.pending[identity]
	if len(queue) == 0 {
		return capturedAdmission{}, false
	}
	return queue[0], true
}

func (r *GRPCRemote) cleanupPendingLocked(now time.Time) {
	ttl := pendingAdmissionTTLValue(r.pendingAdmissionTTL)
	cutoff := now.Add(-ttl)
	for identity, queue := range r.pending {
		kept := queue[:0]
		for i := range queue {
			captured := queue[i]
			if !captured.CreatedAt.IsZero() && captured.CreatedAt.Before(cutoff) {
				queue[i] = capturedAdmission{}
				r.pendingCount--
				continue
			}
			kept = append(kept, captured)
		}
		for i := len(kept); i < len(queue); i++ {
			queue[i] = capturedAdmission{}
		}
		if len(kept) == 0 {
			delete(r.pending, identity)
		} else {
			r.pending[identity] = kept
		}
	}
	if r.pendingCount < 0 {
		r.pendingCount = 0
	}
}

type coreExecGRPCServer struct {
	coreexecpb.UnimplementedCoreExecServiceServer

	handler           Handler
	instanceID        string
	authKeys          store.AuthKeyStore
	authInvalidations store.AuthInvalidationBroker
	metrics           Metrics
	replayHookTTL     time.Duration
	maxReplayHooks    int
	replayMu          sync.Mutex
	replayHooks       map[string]replayHookEntry
}

func newCoreExecGRPCServer(handler Handler) *coreExecGRPCServer {
	return newCoreExecGRPCServerWithInstanceID(handler, "")
}

func newCoreExecGRPCServerWithInstanceID(handler Handler, instanceID string) *coreExecGRPCServer {
	return newCoreExecGRPCServerWithRuntime(handler, instanceID, nil, nil)
}

func newCoreExecGRPCServerWithRuntime(handler Handler, instanceID string, authKeys store.AuthKeyStore, authInvalidations store.AuthInvalidationBroker, metrics ...Metrics) *coreExecGRPCServer {
	var registry Metrics
	if len(metrics) != 0 {
		registry = metrics[0]
	}
	return &coreExecGRPCServer{
		handler:           handler,
		instanceID:        strings.TrimSpace(instanceID),
		authKeys:          authKeys,
		authInvalidations: authInvalidations,
		metrics:           coreExecMetrics(registry),
		replayHookTTL:     defaultReplayHookTTL,
		maxReplayHooks:    defaultMaxReplayHooks,
		replayHooks:       make(map[string]replayHookEntry),
	}
}

func (s *coreExecGRPCServer) GetInfo(_ context.Context, req *coreexecpb.CoreExecInfoRequest) (*coreexecpb.CoreExecInfoResponse, error) {
	res := &coreexecpb.CoreExecInfoResponse{
		ProtocolVersion:             coreExecGRPCProtocolVersion,
		MinSupportedProtocolVersion: coreExecGRPCMinSupportedProtocolVersion,
		Capabilities:                coreExecGRPCCapabilitiesCopy(),
		Implementation:              "telesrv-coreexec-grpc",
		InstanceId:                  s.instanceID,
	}
	if !coreExecProtocolRangesOverlap(
		req.GetMinSupportedProtocolVersion(),
		req.GetProtocolVersion(),
		coreExecGRPCMinSupportedProtocolVersion,
		coreExecGRPCProtocolVersion,
	) {
		res.Error = fmt.Sprintf("incompatible protocol edge=%d..%d core=%d..%d",
			req.GetMinSupportedProtocolVersion(),
			req.GetProtocolVersion(),
			coreExecGRPCMinSupportedProtocolVersion,
			coreExecGRPCProtocolVersion,
		)
	}
	return res, nil
}

func (s *coreExecGRPCServer) DispatchAdmitted(ctx context.Context, req *coreexecpb.DispatchRequest) (*coreexecpb.DispatchResponse, error) {
	if s == nil || s.handler == nil {
		return nil, status.Error(codes.Internal, ErrNilHandler.Error())
	}
	authKeyID, err := authKeyIDFromBytes(req.GetAuthKeyId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "bad auth_key_id")
	}
	admitted, err := admitCaptured(s.handler, admissionModeFromPB(req.GetMode()), int(req.GetProfile()), limitsFromPB(req.GetLimits()), req.GetRequestWire())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	if got, want := proofFromAdmission(admitted), proofFromPB(req.GetProof()); got != want {
		return nil, status.Error(codes.InvalidArgument, ErrWireProofMismatch.Error())
	}
	methodForMetrics := "unknown"
	if _, method, ok := tlprofile.SemanticName(admitted.Call().Method()); ok && method != "" {
		methodForMetrics = method
	}
	ctx, databaseStats := dbtrace.WithStats(ctx)
	defer func() {
		if databaseMetrics, ok := s.metrics.(RPCDatabaseMetrics); ok {
			snapshot := databaseStats.Snapshot()
			databaseMetrics.RPCDatabase(methodForMetrics, snapshot.Queries, snapshot.Duration, snapshot.Errors)
		}
	}()
	ctx = postresponse.WithTypedActionDelivery(postresponse.WithCallbacks(ctx))
	ctx = s.handler.WithLayerRPCProfileEvidenceFresh(ctx, req.GetProfileEvidenceFresh())
	ctx, err = applyIdentityHintPB(ctx, s.handler, authKeyID, req.GetSessionId(), req.GetIdentityHint())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	ctx, err = applyClientIPPB(ctx, req.GetClientIp())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	result, method, err := s.handler.DispatchAdmitted(ctx, authKeyID, req.GetSessionId(), req.GetMsgId(), req.GetAdmissionSeq(), admitted)
	if method != "" {
		methodForMetrics = method
	}
	res := &coreexecpb.DispatchResponse{Method: method}
	if err != nil {
		var rpcErr *tgerr.Error
		if errors.As(err, &rpcErr) {
			res.RpcError = &coreexecpb.RPCError{Code: int32(rpcErr.Code), Message: rpcErr.Message}
			return res, nil
		}
		res.Error = err.Error()
		return res, nil
	}
	if result != nil {
		var encoded bin.Buffer
		if err := result.Encode(&encoded); err != nil {
			res.Error = err.Error()
			return res, nil
		}
		// encoded is local, uniquely owned and never reused after this transfer.
		// grpc-go marshals the returned response before releasing it.
		res.ResultWire = takeBufferRaw(&encoded)
		res.WireInvariant = result.WireInvariant()
	}
	res.PostResponseActions, err = postResponseActionsToPB(postresponse.TakeActions(context.WithoutCancel(ctx)))
	if err != nil {
		res.Error = err.Error()
		return res, nil
	}
	if after := postresponse.Take(context.WithoutCancel(ctx)); after != nil {
		res.Error = "coreexec grpc: non-typed postresponse callback is not supported"
		return res, nil
	}
	return res, nil
}

func (s *coreExecGRPCServer) PrepareAdmittedReplay(ctx context.Context, req *coreexecpb.DispatchRequest) (*coreexecpb.PrepareReplayResponse, error) {
	if s == nil || s.handler == nil {
		return nil, status.Error(codes.Internal, ErrNilHandler.Error())
	}
	authKeyID, err := authKeyIDFromBytes(req.GetAuthKeyId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "bad auth_key_id")
	}
	admitted, err := admitCaptured(s.handler, admissionModeFromPB(req.GetMode()), int(req.GetProfile()), limitsFromPB(req.GetLimits()), req.GetRequestWire())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	if got, want := proofFromAdmission(admitted), proofFromPB(req.GetProof()); got != want {
		return nil, status.Error(codes.InvalidArgument, ErrWireProofMismatch.Error())
	}
	ctx = s.handler.WithLayerRPCProfileEvidenceFresh(ctx, req.GetProfileEvidenceFresh())
	ctx, err = applyIdentityHintPB(ctx, s.handler, authKeyID, req.GetSessionId(), req.GetIdentityHint())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	ctx, err = applyClientIPPB(ctx, req.GetClientIp())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	after, err := s.handler.PrepareAdmittedReplay(ctx, authKeyID, req.GetSessionId(), req.GetMsgId(), req.GetAdmissionSeq(), admitted)
	if err != nil {
		return &coreexecpb.PrepareReplayResponse{Error: err.Error()}, nil
	}
	if after == nil {
		return &coreexecpb.PrepareReplayResponse{}, nil
	}
	token, err := s.storeReplayHook(after)
	if err != nil {
		return &coreexecpb.PrepareReplayResponse{Error: err.Error()}, nil
	}
	return &coreexecpb.PrepareReplayResponse{Token: token}, nil
}

func (s *coreExecGRPCServer) CommitAdmittedReplay(_ context.Context, req *coreexecpb.CommitReplayRequest) (*coreexecpb.CommitReplayResponse, error) {
	after, ok := s.takeReplayHook(req.GetToken())
	if !ok {
		return &coreexecpb.CommitReplayResponse{Error: "coreexec grpc: replay hook not found"}, nil
	}
	if err := after(); err != nil {
		return &coreexecpb.CommitReplayResponse{Error: err.Error()}, nil
	}
	return &coreexecpb.CommitReplayResponse{}, nil
}

func (s *coreExecGRPCServer) CommitPostResponseActions(ctx context.Context, req *coreexecpb.CommitPostResponseActionsRequest) (*coreexecpb.CommitPostResponseActionsResponse, error) {
	if s == nil || s.handler == nil {
		return nil, status.Error(codes.Internal, ErrNilHandler.Error())
	}
	actions, err := postResponseActionsFromPB(req.GetActions())
	if err != nil {
		return &coreexecpb.CommitPostResponseActionsResponse{Error: err.Error()}, nil
	}
	if err := s.handler.RunPostResponseActions(ctx, actions); err != nil {
		return &coreexecpb.CommitPostResponseActionsResponse{Error: err.Error()}, nil
	}
	return &coreexecpb.CommitPostResponseActionsResponse{}, nil
}

func (s *coreExecGRPCServer) ResolveNegotiatedSessionLayer(ctx context.Context, req *coreexecpb.AuthKeyRequest) (*coreexecpb.ResolveSessionLayerResponse, error) {
	rawAuthKeyID, err := authKeyIDFromBytes(req.GetRawAuthKeyId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "bad raw_auth_key_id")
	}
	layer, msgID, found, err := s.handler.ResolveNegotiatedSessionLayerEvidence(ctx, rawAuthKeyID, req.GetSessionId())
	res := &coreexecpb.ResolveSessionLayerResponse{Layer: int32(layer), MsgId: msgID, Found: found}
	if err != nil {
		res.Error = err.Error()
	}
	return res, nil
}

func (s *coreExecGRPCServer) ResolveInheritedAuthKeyLayer(ctx context.Context, req *coreexecpb.AuthKeyRequest) (*coreexecpb.ResolveInheritedLayerResponse, error) {
	rawAuthKeyID, err := authKeyIDFromBytes(req.GetRawAuthKeyId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "bad raw_auth_key_id")
	}
	layer, found, err := s.handler.ResolveInheritedAuthKeyLayer(ctx, rawAuthKeyID)
	res := &coreexecpb.ResolveInheritedLayerResponse{Layer: int32(layer), Found: found}
	if err != nil {
		res.Error = err.Error()
	}
	return res, nil
}

func (s *coreExecGRPCServer) AdvanceNegotiatedSessionLayer(ctx context.Context, req *coreexecpb.AuthKeyRequest) (*coreexecpb.AdvanceSessionLayerResponse, error) {
	rawAuthKeyID, err := authKeyIDFromBytes(req.GetRawAuthKeyId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "bad raw_auth_key_id")
	}
	layer, msgID, publish, err := s.handler.AdvanceNegotiatedSessionLayerEvidence(ctx, rawAuthKeyID, req.GetSessionId(), int(req.GetLayer()), req.GetMsgId())
	res := &coreexecpb.AdvanceSessionLayerResponse{CurrentLayer: int32(layer), CurrentMsgId: msgID, PublishShared: publish}
	if err != nil {
		res.Error = err.Error()
	}
	return res, nil
}

func (s *coreExecGRPCServer) DeleteNegotiatedSessionLayer(ctx context.Context, req *coreexecpb.AuthKeyRequest) (*coreexecpb.DeleteSessionLayerResponse, error) {
	rawAuthKeyID, err := authKeyIDFromBytes(req.GetRawAuthKeyId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "bad raw_auth_key_id")
	}
	deleted, err := s.handler.DeleteNegotiatedSessionLayerEvidence(ctx, rawAuthKeyID, req.GetSessionId())
	res := &coreexecpb.DeleteSessionLayerResponse{Deleted: deleted}
	if err != nil {
		res.Error = err.Error()
	}
	return res, nil
}

func (s *coreExecGRPCServer) PublishLayerEvidence(ctx context.Context, req *coreexecpb.PublishLayerEvidenceRequest) (*coreexecpb.ErrorResponse, error) {
	rawAuthKeyID, err := authKeyIDFromBytes(req.GetRawAuthKeyId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "bad raw_auth_key_id")
	}
	err = s.handler.PublishAdmittedLayerProfileEvidence(ctx, rawAuthKeyID, req.GetSessionId(), req.GetMsgId(), req.GetAdmissionSeq(), req.GetSafeFloor(), int(req.GetLayer()))
	if err != nil {
		return &coreexecpb.ErrorResponse{Error: err.Error()}, nil
	}
	return &coreexecpb.ErrorResponse{}, nil
}

func (s *coreExecGRPCServer) ObserveInitConnection(ctx context.Context, req *coreexecpb.ObserveInitConnectionRequest) (*coreexecpb.ErrorResponse, error) {
	authKeyID, err := authKeyIDFromBytes(req.GetAuthKeyId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "bad auth_key_id")
	}
	ctx, err = applyClientIPPB(ctx, req.GetClientIp())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	err = s.handler.ObserveInitConnection(
		ctx,
		authKeyID,
		req.GetSessionId(),
		int(req.GetLayer()),
		int(req.GetApiId()),
		req.GetDeviceModel(),
		req.GetSystemVersion(),
		req.GetAppVersion(),
		req.GetSystemLangCode(),
		req.GetLangPack(),
		req.GetLangCode(),
	)
	if err != nil {
		return &coreexecpb.ErrorResponse{Error: err.Error()}, nil
	}
	return &coreexecpb.ErrorResponse{}, nil
}

func (s *coreExecGRPCServer) storeReplayHook(after func() error) (string, error) {
	if s == nil || after == nil {
		return "", nil
	}
	token, err := newReplayToken()
	if err != nil {
		return "", err
	}
	ttl := s.replayHookTTL
	if ttl <= 0 {
		ttl = defaultReplayHookTTL
	}
	limit := s.maxReplayHooks
	if limit <= 0 {
		limit = defaultMaxReplayHooks
	}
	s.replayMu.Lock()
	defer s.replayMu.Unlock()
	s.purgeExpiredReplayHooksLocked(time.Now())
	if len(s.replayHooks) >= limit {
		return "", fmt.Errorf("coreexec grpc: replay hook capacity exceeded")
	}
	if s.replayHooks == nil {
		s.replayHooks = make(map[string]replayHookEntry)
	}
	s.replayHooks[token] = replayHookEntry{after: after, expiresAt: time.Now().Add(ttl)}
	return token, nil
}

func (s *coreExecGRPCServer) takeReplayHook(token string) (func() error, bool) {
	token = strings.TrimSpace(token)
	if s == nil || token == "" {
		return nil, false
	}
	s.replayMu.Lock()
	defer s.replayMu.Unlock()
	now := time.Now()
	s.purgeExpiredReplayHooksLocked(now)
	entry, ok := s.replayHooks[token]
	if !ok || entry.after == nil || !entry.expiresAt.After(now) {
		if ok {
			delete(s.replayHooks, token)
		}
		return nil, false
	}
	delete(s.replayHooks, token)
	return entry.after, true
}

func (s *coreExecGRPCServer) purgeExpiredReplayHooksLocked(now time.Time) {
	for token, entry := range s.replayHooks {
		if entry.expiresAt.Before(now) || entry.expiresAt.Equal(now) || entry.after == nil {
			delete(s.replayHooks, token)
		}
	}
}

func grpcDispatchRequest(captured *capturedAdmission, authKeyID [8]byte, sessionID, msgID int64, admissionSeq uint64, fresh bool, identityHint *coreexecpb.IdentityHint, clientIP string) *coreexecpb.DispatchRequest {
	if captured == nil {
		return nil
	}
	// captured was removed from the pending-admission map and is now exclusively
	// owned by this call. Move request_wire into the protobuf message; grpc-go
	// synchronously finishes marshaling a unary request before Invoke returns.
	requestWire := captured.Wire
	captured.Wire = nil
	return &coreexecpb.DispatchRequest{
		Mode:                 admissionModeToPB(captured.Mode),
		Profile:              int32(captured.Profile),
		Limits:               limitsToPB(captured.Limits),
		RequestWire:          requestWire,
		Proof:                proofToPB(captured.Proof),
		AuthKeyId:            append([]byte(nil), authKeyID[:]...),
		SessionId:            sessionID,
		MsgId:                msgID,
		AdmissionSeq:         admissionSeq,
		ProfileEvidenceFresh: fresh,
		IdentityHint:         identityHint,
		ClientIp:             clientIP,
	}
}

func postResponseActionsToPB(actions []postresponse.Action) ([]*coreexecpb.PostResponseAction, error) {
	if len(actions) == 0 {
		return nil, nil
	}
	out := make([]*coreexecpb.PostResponseAction, 0, len(actions))
	for _, action := range actions {
		switch action.Kind {
		case postresponse.ActionUpdatesDelivery:
			updates := action.UpdatesDelivery
			out = append(out, &coreexecpb.PostResponseAction{
				Action: &coreexecpb.PostResponseAction_UpdatesDelivery{
					UpdatesDelivery: &coreexecpb.UpdatesDeliveryAction{
						CommitCursor:        updates.CommitCursor,
						CursorAuthKeyId:     append([]byte(nil), updates.CursorAuthKey[:]...),
						CursorUserId:        updates.CursorUserID,
						CursorState:         updateStateToPB(updates.CursorState),
						CursorMode:          updateStateCommitModeToPB(updates.CursorMode),
						MarkSecretDelivered: updates.MarkSecretDelivered,
						SecretDeviceKey:     updates.SecretDeviceKey,
						SecretEventIds:      append([]int64(nil), updates.SecretEventIDs...),
						MarkSessionReady:    updates.MarkSessionReady,
						ReadyRawAuthKeyId:   append([]byte(nil), updates.ReadyRawAuthKey[:]...),
						ReadySessionId:      updates.ReadySessionID,
						ReadyUserId:         updates.ReadyUserID,
						ActivationToken:     updates.ActivationToken,
						PublishBootstrap:    updates.PublishBootstrap,
						BootstrapUserId:     updates.BootstrapUserID,
						BootstrapAuthKeyId:  append([]byte(nil), updates.BootstrapAuthKey[:]...),
						BootstrapSessionId:  updates.BootstrapSessionID,
						BootstrapProbeToken: updates.BootstrapProbeToken,
						BootstrapRawAuthKeyId: append(
							[]byte(nil), updates.BootstrapRawAuthKey[:]...,
						),
					},
				},
			})
		case postresponse.ActionAccountAuthorizationTeardown:
			teardown := action.AccountAuthorizationTeardown
			if teardown.UserID <= 0 || teardown.AuthKeyID == ([8]byte{}) {
				return nil, fmt.Errorf("coreexec grpc: invalid account authorization teardown post-response action")
			}
			out = append(out, &coreexecpb.PostResponseAction{
				Action: &coreexecpb.PostResponseAction_AccountAuthorizationTeardown{
					AccountAuthorizationTeardown: &coreexecpb.AccountAuthorizationTeardownAction{
						UserId:    teardown.UserID,
						AuthKeyId: append([]byte(nil), teardown.AuthKeyID[:]...),
					},
				},
			})
		default:
			return nil, fmt.Errorf("coreexec grpc: unsupported post-response action %q", action.Kind)
		}
	}
	return out, nil
}

func postResponseActionsFromPB(actions []*coreexecpb.PostResponseAction) ([]postresponse.Action, error) {
	if len(actions) == 0 {
		return nil, nil
	}
	out := make([]postresponse.Action, 0, len(actions))
	for i, action := range actions {
		if action == nil {
			return nil, fmt.Errorf("coreexec grpc: post-response action %d is nil", i)
		}
		switch body := action.GetAction().(type) {
		case *coreexecpb.PostResponseAction_UpdatesDelivery:
			updatesPB := body.UpdatesDelivery
			if updatesPB == nil {
				return nil, fmt.Errorf("coreexec grpc: post-response action %d has nil updates_delivery", i)
			}
			var cursorAuthKey [8]byte
			if updatesPB.GetCommitCursor() {
				parsed, err := authKeyIDFromBytes(updatesPB.GetCursorAuthKeyId())
				if err != nil {
					return nil, fmt.Errorf("coreexec grpc: post-response action %d has bad cursor_auth_key_id", i)
				}
				cursorAuthKey = parsed
			}
			var readyRawAuthKey [8]byte
			if updatesPB.GetMarkSessionReady() {
				parsed, err := authKeyIDFromBytes(updatesPB.GetReadyRawAuthKeyId())
				if err != nil {
					return nil, fmt.Errorf("coreexec grpc: post-response action %d has bad ready_raw_auth_key_id", i)
				}
				readyRawAuthKey = parsed
			}
			var bootstrapAuthKey [8]byte
			if updatesPB.GetPublishBootstrap() {
				parsed, err := authKeyIDFromBytes(updatesPB.GetBootstrapAuthKeyId())
				if err != nil {
					return nil, fmt.Errorf("coreexec grpc: post-response action %d has bad bootstrap_auth_key_id", i)
				}
				bootstrapAuthKey = parsed
			}
			var bootstrapRawAuthKey [8]byte
			if updatesPB.GetBootstrapProbeToken() != 0 {
				if !updatesPB.GetPublishBootstrap() || updatesPB.GetBootstrapSessionId() == 0 {
					return nil, fmt.Errorf("coreexec grpc: post-response action %d has invalid bootstrap lifecycle", i)
				}
				parsed, err := authKeyIDFromBytes(updatesPB.GetBootstrapRawAuthKeyId())
				if err != nil || parsed == ([8]byte{}) {
					return nil, fmt.Errorf("coreexec grpc: post-response action %d has bad bootstrap_raw_auth_key_id", i)
				}
				bootstrapRawAuthKey = parsed
			}
			if updatesPB.GetActivationToken() != 0 &&
				(!updatesPB.GetMarkSessionReady() || readyRawAuthKey == ([8]byte{}) || updatesPB.GetReadySessionId() == 0) {
				return nil, fmt.Errorf("coreexec grpc: post-response action %d has invalid activation lifecycle", i)
			}
			out = append(out, postresponse.Action{
				Kind: postresponse.ActionUpdatesDelivery,
				UpdatesDelivery: postresponse.UpdatesDeliveryAction{
					CommitCursor:        updatesPB.GetCommitCursor(),
					CursorAuthKey:       cursorAuthKey,
					CursorUserID:        updatesPB.GetCursorUserId(),
					CursorState:         updateStateFromPB(updatesPB.GetCursorState()),
					CursorMode:          updateStateCommitModeFromPB(updatesPB.GetCursorMode()),
					MarkSecretDelivered: updatesPB.GetMarkSecretDelivered(),
					SecretDeviceKey:     updatesPB.GetSecretDeviceKey(),
					SecretEventIDs:      append([]int64(nil), updatesPB.GetSecretEventIds()...),
					MarkSessionReady:    updatesPB.GetMarkSessionReady(),
					ReadyRawAuthKey:     readyRawAuthKey,
					ReadySessionID:      updatesPB.GetReadySessionId(),
					ReadyUserID:         updatesPB.GetReadyUserId(),
					ActivationToken:     updatesPB.GetActivationToken(),
					PublishBootstrap:    updatesPB.GetPublishBootstrap(),
					BootstrapUserID:     updatesPB.GetBootstrapUserId(),
					BootstrapAuthKey:    bootstrapAuthKey,
					BootstrapRawAuthKey: bootstrapRawAuthKey,
					BootstrapSessionID:  updatesPB.GetBootstrapSessionId(),
					BootstrapProbeToken: updatesPB.GetBootstrapProbeToken(),
				},
			})
		case *coreexecpb.PostResponseAction_AccountAuthorizationTeardown:
			teardownPB := body.AccountAuthorizationTeardown
			if teardownPB == nil || teardownPB.GetUserId() <= 0 {
				return nil, fmt.Errorf("coreexec grpc: post-response action %d has invalid account_authorization_teardown", i)
			}
			authKeyID, err := authKeyIDFromBytes(teardownPB.GetAuthKeyId())
			if err != nil || authKeyID == ([8]byte{}) {
				return nil, fmt.Errorf("coreexec grpc: post-response action %d has bad account auth_key_id", i)
			}
			out = append(out, postresponse.Action{
				Kind: postresponse.ActionAccountAuthorizationTeardown,
				AccountAuthorizationTeardown: postresponse.AccountAuthorizationTeardownAction{
					UserID:    teardownPB.GetUserId(),
					AuthKeyID: authKeyID,
				},
			})
		default:
			return nil, fmt.Errorf("coreexec grpc: post-response action %d has no supported body", i)
		}
	}
	return out, nil
}

func updateStateToPB(st postresponse.UpdateState) *coreexecpb.UpdateState {
	return &coreexecpb.UpdateState{
		Pts:  int32(st.Pts),
		Qts:  int32(st.Qts),
		Date: int32(st.Date),
		Seq:  int32(st.Seq),
	}
}

func updateStateFromPB(st *coreexecpb.UpdateState) postresponse.UpdateState {
	if st == nil {
		return postresponse.UpdateState{}
	}
	return postresponse.UpdateState{
		Pts:  int(st.GetPts()),
		Qts:  int(st.GetQts()),
		Date: int(st.GetDate()),
		Seq:  int(st.GetSeq()),
	}
}

func updateStateCommitModeToPB(mode uint8) coreexecpb.UpdateStateCommitMode {
	switch mode {
	case 1:
		return coreexecpb.UpdateStateCommitMode_UPDATE_STATE_COMMIT_DELIVERED_ONLY
	case 2:
		return coreexecpb.UpdateStateCommitMode_UPDATE_STATE_COMMIT_DELIVERED_AND_OBSERVED_BASELINE
	default:
		return coreexecpb.UpdateStateCommitMode_UPDATE_STATE_COMMIT_MODE_UNSPECIFIED
	}
}

func updateStateCommitModeFromPB(mode coreexecpb.UpdateStateCommitMode) uint8 {
	switch mode {
	case coreexecpb.UpdateStateCommitMode_UPDATE_STATE_COMMIT_DELIVERED_ONLY:
		return 1
	case coreexecpb.UpdateStateCommitMode_UPDATE_STATE_COMMIT_DELIVERED_AND_OBSERVED_BASELINE:
		return 2
	default:
		return 0
	}
}

func authKeyIDFromBytes(raw []byte) ([8]byte, error) {
	var out [8]byte
	if len(raw) != len(out) {
		return out, fmt.Errorf("invalid auth key id")
	}
	copy(out[:], raw)
	return out, nil
}

func WithGRPCRequestID(ctx context.Context, requestID string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	requestID = normalizeCoreExecGRPCRequestID(requestID)
	if requestID == "" {
		return ctx
	}
	return context.WithValue(ctx, coreExecGRPCRequestIDContextKey{}, requestID)
}

func GRPCRequestIDFromContext(ctx context.Context) (string, bool) {
	if ctx == nil {
		return "", false
	}
	requestID, ok := ctx.Value(coreExecGRPCRequestIDContextKey{}).(string)
	requestID = normalizeCoreExecGRPCRequestID(requestID)
	return requestID, ok && requestID != ""
}

func grpcRequestIDUnaryServerInterceptor(logger *zap.Logger) grpc.UnaryServerInterceptor {
	if logger == nil {
		logger = zap.NewNop()
	}
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		requestID := ""
		if md, ok := metadata.FromIncomingContext(ctx); ok {
			requestID = normalizeCoreExecGRPCRequestID(firstMetadataValue(md, coreExecGRPCRequestIDMetadataKey))
		}
		if requestID == "" {
			requestID = newCoreExecGRPCRequestID()
		}
		ctx = WithGRPCRequestID(ctx, requestID)
		res, err := handler(ctx, req)
		if err != nil {
			logCoreExecGRPCFailure(logger, "coreexec grpc call failed", requestID, coreExecGRPCOperation(info.FullMethod), err)
		}
		return res, err
	}
}

func grpcBearerUnaryServerInterceptor(token string) grpc.UnaryServerInterceptor {
	token = strings.TrimSpace(token)
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if token == "" {
			return nil, status.Error(codes.Unauthenticated, "bearer token is not configured")
		}
		md, ok := metadata.FromIncomingContext(ctx)
		if !ok {
			return nil, status.Error(codes.Unauthenticated, "missing metadata")
		}
		if subtle.ConstantTimeCompare([]byte(firstMetadataValue(md, "authorization")), []byte("Bearer "+token)) != 1 {
			return nil, status.Error(codes.Unauthenticated, "unauthorized")
		}
		return handler(ctx, req)
	}
}

func grpcRequestIDUnaryClientInterceptor(logger *zap.Logger) grpc.UnaryClientInterceptor {
	if logger == nil {
		logger = zap.NewNop()
	}
	return func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		requestID, ok := GRPCRequestIDFromContext(ctx)
		if !ok {
			if md, mdOK := metadata.FromOutgoingContext(ctx); mdOK {
				requestID = normalizeCoreExecGRPCRequestID(firstMetadataValue(md, coreExecGRPCRequestIDMetadataKey))
			}
		}
		if requestID == "" {
			requestID = newCoreExecGRPCRequestID()
		}
		ctx = metadata.AppendToOutgoingContext(ctx, coreExecGRPCRequestIDMetadataKey, requestID)
		err := invoker(ctx, method, req, reply, cc, opts...)
		if err != nil {
			logCoreExecGRPCFailure(logger, "coreexec grpc client call failed", requestID, coreExecGRPCOperation(method), err)
		}
		return err
	}
}

func logCoreExecGRPCFailure(logger *zap.Logger, message, requestID, operation string, err error) {
	fields := []zap.Field{
		zap.String("request_id", requestID),
		zap.String("operation", operation),
		zap.String("outcome", grpcStatusOutcome(err)),
		zap.Error(err),
	}
	if status.Code(err) == codes.Canceled || errors.Is(err, context.Canceled) {
		logger.Debug(message, fields...)
		return
	}
	logger.Warn(message, fields...)
}

func grpcBearerUnaryClientInterceptor(token string) grpc.UnaryClientInterceptor {
	token = strings.TrimSpace(token)
	return func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		ctx = metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+token)
		return invoker(ctx, method, req, reply, cc, opts...)
	}
}

func firstMetadataValue(md metadata.MD, key string) string {
	values := md.Get(key)
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func normalizeCoreExecGRPCRequestID(requestID string) string {
	requestID = strings.TrimSpace(requestID)
	if requestID == "" || len(requestID) > 128 {
		return ""
	}
	for i := 0; i < len(requestID); i++ {
		c := requestID[i]
		if (c >= 'a' && c <= 'z') ||
			(c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') ||
			c == '-' || c == '_' || c == '.' || c == ':' {
			continue
		}
		return ""
	}
	return requestID
}

func newCoreExecGRPCRequestID() string {
	seq := coreExecGRPCRequestIDSeq.Add(1)
	return coreExecGRPCRequestIDPrefix + ":" + strconv.FormatUint(seq, 16)
}

func newCoreExecGRPCRequestIDPrefix() string {
	var b [coreExecGRPCRequestIDBytes]byte
	if _, err := rand.Read(b[:]); err == nil {
		return hex.EncodeToString(b[:])
	}
	return fmt.Sprintf("%x:%d", time.Now().UnixNano(), os.Getpid())
}

func grpcMetricsUnaryServerInterceptor(metrics Metrics) grpc.UnaryServerInterceptor {
	metrics = coreExecMetrics(metrics)
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		start := time.Now()
		res, err := handler(ctx, req)
		observeCoreExecGRPCCall(metrics, "server", coreExecGRPCOperation(info.FullMethod), start, coreExecGRPCServerOutcome(res, err))
		return res, err
	}
}

func coreExecGRPCOperation(fullMethod string) string {
	switch fullMethod {
	case coreexecpb.CoreExecService_GetInfo_FullMethodName:
		return "get_info"
	case coreexecpb.CoreExecService_DispatchAdmitted_FullMethodName:
		return "dispatch_admitted"
	case coreexecpb.CoreExecService_PrepareAdmittedReplay_FullMethodName:
		return "prepare_admitted_replay"
	case coreexecpb.CoreExecService_CommitAdmittedReplay_FullMethodName:
		return "commit_admitted_replay"
	case coreexecpb.CoreExecService_CommitPostResponseActions_FullMethodName:
		return "commit_post_response_actions"
	case coreexecpb.CoreExecService_ReportSessionOffline_FullMethodName:
		return "report_session_offline"
	case coreexecpb.CoreExecService_ResolveNegotiatedSessionLayer_FullMethodName:
		return "resolve_negotiated_session_layer"
	case coreexecpb.CoreExecService_ResolveInheritedAuthKeyLayer_FullMethodName:
		return "resolve_inherited_auth_key_layer"
	case coreexecpb.CoreExecService_AdvanceNegotiatedSessionLayer_FullMethodName:
		return "advance_negotiated_session_layer"
	case coreexecpb.CoreExecService_DeleteNegotiatedSessionLayer_FullMethodName:
		return "delete_negotiated_session_layer"
	case coreexecpb.CoreExecService_PublishLayerEvidence_FullMethodName:
		return "publish_layer_evidence"
	case coreexecpb.CoreExecService_ObserveInitConnection_FullMethodName:
		return "observe_init_connection"
	case coreexecpb.CoreExecService_SaveAuthKey_FullMethodName:
		return "save_auth_key"
	case coreexecpb.CoreExecService_GetAuthKey_FullMethodName:
		return "get_auth_key"
	case coreexecpb.CoreExecService_UpdateAuthKeyClientInfo_FullMethodName:
		return "update_auth_key_client_info"
	case coreexecpb.CoreExecService_DeleteAuthKey_FullMethodName:
		return "delete_auth_key"
	case "/grpc.health.v1.Health/Check":
		return "health"
	default:
		return "unknown"
	}
}

func grpcStatusOutcome(err error) string {
	if err == nil {
		return "ok"
	}
	switch status.Code(err) {
	case codes.OK:
		return "ok"
	case codes.Canceled:
		return "canceled"
	case codes.DeadlineExceeded:
		return "timeout"
	case codes.Unauthenticated, codes.PermissionDenied:
		return "unauthenticated"
	case codes.InvalidArgument, codes.FailedPrecondition, codes.OutOfRange:
		return "invalid"
	case codes.Unimplemented:
		return "version_mismatch"
	case codes.Unavailable, codes.ResourceExhausted, codes.Aborted:
		return "unavailable"
	default:
		return "error"
	}
}

func coreExecGRPCServerOutcome(res any, err error) string {
	if err != nil {
		return grpcStatusOutcome(err)
	}
	switch typed := res.(type) {
	case *coreexecpb.CoreExecInfoResponse:
		if typed.GetError() != "" {
			return "version_mismatch"
		}
	case *coreexecpb.DispatchResponse:
		if typed.GetRpcError() != nil {
			return "rpc_error"
		}
		if typed.GetError() != "" {
			return "control_error"
		}
	case *coreexecpb.PrepareReplayResponse:
		if typed.GetError() != "" {
			return "control_error"
		}
	case *coreexecpb.CommitReplayResponse:
		if typed.GetError() != "" {
			return "control_error"
		}
	case *coreexecpb.CommitPostResponseActionsResponse:
		if typed.GetError() != "" {
			return "control_error"
		}
	case *coreexecpb.ResolveSessionLayerResponse:
		if typed.GetError() != "" {
			return "control_error"
		}
	case *coreexecpb.ResolveInheritedLayerResponse:
		if typed.GetError() != "" {
			return "control_error"
		}
	case *coreexecpb.AdvanceSessionLayerResponse:
		if typed.GetError() != "" {
			return "control_error"
		}
	case *coreexecpb.DeleteSessionLayerResponse:
		if typed.GetError() != "" {
			return "control_error"
		}
	case *coreexecpb.GetAuthKeyResponse:
		if typed.GetError() != "" {
			return "control_error"
		}
		if !typed.GetFound() {
			return "not_found"
		}
	case *coreexecpb.ErrorResponse:
		if typed.GetError() != "" {
			return "control_error"
		}
	}
	return "ok"
}

func grpcMessageSize(value int) int {
	if value <= 0 {
		return defaultMaxGRPCMessageSize
	}
	return value
}

func grpcClientRequestTimeout(value time.Duration) time.Duration {
	if value <= 0 {
		return defaultGRPCClientTimeout
	}
	return value
}

func coreExecGRPCCapabilitiesCopy() []string {
	return append([]string(nil), coreExecGRPCCapabilities...)
}

func missingCoreExecGRPCCapabilities(peer []string, required []string) []string {
	if len(required) == 0 {
		return nil
	}
	have := make(map[string]struct{}, len(peer))
	for _, capability := range peer {
		capability = strings.TrimSpace(capability)
		if capability != "" {
			have[capability] = struct{}{}
		}
	}
	missing := make([]string, 0, len(required))
	for _, capability := range required {
		capability = strings.TrimSpace(capability)
		if capability == "" {
			continue
		}
		if _, ok := have[capability]; !ok {
			missing = append(missing, capability)
		}
	}
	return missing
}

func coreExecProtocolRangesOverlap(peerMin, peerMax, localMin, localMax uint32) bool {
	if peerMax == 0 {
		return false
	}
	if peerMin == 0 {
		peerMin = peerMax
	}
	if localMax == 0 {
		return false
	}
	if localMin == 0 {
		localMin = localMax
	}
	return peerMax >= localMin && localMax >= peerMin
}

func grpcRemoteCallError(operation string, err error) error {
	if err == nil {
		return nil
	}
	switch status.Code(err) {
	case codes.OK:
		return nil
	case codes.Canceled:
		return fmt.Errorf("%w: %s: %w", context.Canceled, operation, err)
	case codes.DeadlineExceeded:
		return fmt.Errorf("%w: %s: %w", ErrRemoteRequestTimeout, operation, err)
	case codes.Unauthenticated, codes.PermissionDenied:
		return fmt.Errorf("%w: %s: %w", ErrRemoteUnauthenticated, operation, err)
	case codes.InvalidArgument, codes.FailedPrecondition, codes.OutOfRange:
		return fmt.Errorf("%w: %s: %w", ErrRemoteInvalidRequest, operation, err)
	case codes.Unimplemented:
		return fmt.Errorf("%w: %s: %w", ErrGRPCProtocolVersionMismatch, operation, err)
	case codes.Unavailable, codes.ResourceExhausted, codes.Aborted:
		return fmt.Errorf("%w: %s: %w", ErrRemoteRPCUnavailable, operation, err)
	default:
		return fmt.Errorf("%w: %s: %w", ErrRemoteRPCUnavailable, operation, err)
	}
}

func grpcRemoteCallOutcome(err error) string {
	if err == nil {
		return "ok"
	}
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	if errors.Is(err, ErrRemoteRequestTimeout) || errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	if errors.Is(err, ErrRemoteUnauthenticated) {
		return "unauthenticated"
	}
	if errors.Is(err, ErrRemoteInvalidRequest) {
		return "invalid"
	}
	if errors.Is(err, ErrGRPCProtocolVersionMismatch) {
		return "version_mismatch"
	}
	if errors.Is(err, ErrRemoteRPCUnavailable) {
		return "unavailable"
	}
	return "error"
}

func grpcRemoteResponseErrorOutcome(err error) string {
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	if errors.Is(err, ErrRemoteRequestTimeout) ||
		errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	if errors.Is(err, ErrRemoteUnauthenticated) {
		return "unauthenticated"
	}
	if errors.Is(err, ErrRemoteInvalidRequest) {
		return "invalid"
	}
	if errors.Is(err, ErrGRPCProtocolVersionMismatch) {
		return "version_mismatch"
	}
	return "control_error"
}

func grpcTransportRetryable(err error) bool {
	return errors.Is(err, ErrRemoteRequestTimeout) || errors.Is(err, ErrRemoteRPCUnavailable)
}

func grpcClientDialOptions(cfg GRPCClientConfig, resolverProvider GRPCResolverProvider) ([]grpc.DialOption, error) {
	transportCreds, err := grpcClientTransportCredentials(cfg)
	if err != nil {
		return nil, err
	}
	opts := []grpc.DialOption{
		grpc.WithTransportCredentials(transportCreds),
		grpc.WithChainUnaryInterceptor(
			grpcRequestIDUnaryClientInterceptor(cfg.Logger),
			grpcBearerUnaryClientInterceptor(cfg.Token),
		),
		// DispatchAdmitted can have already executed business work when a transport
		// error is observed at Edge. Keep gRPC automatic retries disabled; retries
		// must be explicit and tied to the RPC execution ledger/outbox semantics.
		grpc.WithDisableRetry(),
		grpc.WithDefaultServiceConfig(coreExecGRPCDefaultServiceConfig()),
		grpc.WithDefaultCallOptions(
			grpc.MaxCallRecvMsgSize(grpcMessageSize(cfg.MaxRecvMsgBytes)),
			grpc.MaxCallSendMsgSize(grpcMessageSize(cfg.MaxSendMsgBytes)),
		),
	}
	if resolverProvider != nil {
		opts = append(opts, resolverProvider.DialOptions()...)
	}
	opts = append(opts, cfg.DialOptions...)
	return opts, nil
}

func coreExecGRPCDefaultServiceConfig() string {
	return fmt.Sprintf(`{"loadBalancingConfig":[{"round_robin":{}}],"healthCheckConfig":{"serviceName":%q}}`,
		coreexecpb.CoreExecService_ServiceDesc.ServiceName)
}

func grpcServerTransportCredentials(cfg GRPCServerConfig) (credentials.TransportCredentials, bool, error) {
	certFile := strings.TrimSpace(cfg.TLSCertFile)
	keyFile := strings.TrimSpace(cfg.TLSKeyFile)
	clientCAFile := strings.TrimSpace(cfg.TLSClientCAFile)
	if certFile == "" && keyFile == "" && clientCAFile == "" {
		return nil, false, nil
	}
	if (certFile == "") != (keyFile == "") {
		return nil, false, fmt.Errorf("coreexec grpc tls: server cert and key must be configured together")
	}
	if certFile == "" {
		return nil, false, fmt.Errorf("coreexec grpc tls: server cert and key are required when client CA is configured")
	}
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, false, fmt.Errorf("coreexec grpc tls: load server certificate: %w", err)
	}
	tlsCfg := &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{cert},
	}
	if clientCAFile != "" {
		pool, err := loadCertPool(clientCAFile)
		if err != nil {
			return nil, false, fmt.Errorf("coreexec grpc tls: load client CA: %w", err)
		}
		tlsCfg.ClientCAs = pool
		tlsCfg.ClientAuth = tls.RequireAndVerifyClientCert
	}
	return credentials.NewTLS(tlsCfg), true, nil
}

func grpcClientTransportCredentials(cfg GRPCClientConfig) (credentials.TransportCredentials, error) {
	caFile := strings.TrimSpace(cfg.TLSCAFile)
	serverName := strings.TrimSpace(cfg.TLSServerName)
	certFile := strings.TrimSpace(cfg.TLSCertFile)
	keyFile := strings.TrimSpace(cfg.TLSKeyFile)
	if caFile == "" && serverName == "" && certFile == "" && keyFile == "" {
		return insecure.NewCredentials(), nil
	}
	if (certFile == "") != (keyFile == "") {
		return nil, fmt.Errorf("coreexec grpc tls: client cert and key must be configured together")
	}
	tlsCfg := &tls.Config{
		MinVersion: tls.VersionTLS12,
		ServerName: serverName,
	}
	if caFile != "" {
		pool, err := loadCertPool(caFile)
		if err != nil {
			return nil, fmt.Errorf("coreexec grpc tls: load root CA: %w", err)
		}
		tlsCfg.RootCAs = pool
	}
	if certFile != "" {
		cert, err := tls.LoadX509KeyPair(certFile, keyFile)
		if err != nil {
			return nil, fmt.Errorf("coreexec grpc tls: load client certificate: %w", err)
		}
		tlsCfg.Certificates = []tls.Certificate{cert}
	}
	return credentials.NewTLS(tlsCfg), nil
}

func loadCertPool(path string) (*x509.CertPool, error) {
	pemBytes, err := os.ReadFile(strings.TrimSpace(path))
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pemBytes) {
		return nil, fmt.Errorf("no PEM certificates found in %s", path)
	}
	return pool, nil
}
