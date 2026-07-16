package rpc

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/iamxvbaba/td/bin"
	"go.uber.org/zap"

	"github.com/iamxvbaba/td/tlprofile"
	compatandroid "telesrv/internal/compat/android"
	"telesrv/internal/observability/dbtrace"
)

type layerWrappersAppliedKey struct{}
type layerAdmissionSequenceKey struct{}
type layerRPCProfileEvidenceFreshKey struct{}

// WithLayerRPCProfileEvidenceFresh records whether an admitted request's
// explicit selector is inside MTProto's mutable msg_id freshness window. A
// stale selector still owns its immutable request/result codec, but wrapper
// metadata, readiness and auth-key default effects must not escape into shared
// state. mtprotoedge discovers this method through a structural interface.
func (r *Router) WithLayerRPCProfileEvidenceFresh(ctx context.Context, fresh bool) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, layerRPCProfileEvidenceFreshKey{}, fresh)
}

func layerRPCProfileEvidenceFresh(ctx context.Context) bool {
	fresh, specified := ctx.Value(layerRPCProfileEvidenceFreshKey{}).(bool)
	return !specified || fresh
}

func withLayerAdmissionSequence(ctx context.Context, sequence uint64) context.Context {
	return context.WithValue(ctx, layerAdmissionSequenceKey{}, sequence)
}

func layerAdmissionSequenceFrom(ctx context.Context) uint64 {
	sequence, _ := ctx.Value(layerAdmissionSequenceKey{}).(uint64)
	return sequence
}

type layerRPCWrapperApplyMode uint8

const (
	layerRPCWrapperApplyDispatch layerRPCWrapperApplyMode = iota
	layerRPCWrapperApplyReplayRestore
)

const layerRPCReplayRestoreTimeout = 5 * time.Second

// AdmitLayer performs generated, bounded, exact-profile admission without
// touching auth/session stores. The MTProto edge must call it before acquiring
// an RPC flight/cache slot or scheduling business work.
func (r *Router) AdmitLayer(profile tlprofile.Profile, b *bin.Buffer, limits tlprofile.Limits) (tlprofile.Admission, error) {
	if r == nil || r.dispatcher == nil {
		return tlprofile.Admission{}, internalErr()
	}
	if b == nil {
		return tlprofile.Admission{}, inputRequestInvalidErr()
	}
	return r.dispatcher.Admit(profile, b, limits)
}

// AdmitDefaultLayer admits a request using an inherited auth-key profile as
// its effective codec while still allowing an explicit invokeWithLayer in the
// same wrapper chain to correct that default. Generated admission preserves
// the distinction through EffectiveProfile and ProfileEvidence.
func (r *Router) AdmitDefaultLayer(profile tlprofile.Profile, b *bin.Buffer, limits tlprofile.Limits) (tlprofile.Admission, error) {
	if r == nil || r.dispatcher == nil {
		return tlprofile.Admission{}, internalErr()
	}
	if b == nil {
		return tlprofile.Admission{}, inputRequestInvalidErr()
	}
	return r.dispatcher.AdmitDefault(profile, b, limits)
}

// registerAndroidLayerRPCAdapter installs the only client-private schema seam.
// The generated exact switch invokes it only for an unknown innermost terminal
// after recursively peeling every official wrapper. AdaptCanonical runs this
// dispatcher's semantic field policies before the first generated typed
// materialization, then core revalidates exact wire with the adapter disabled.
func (r *Router) registerAndroidLayerRPCAdapter(d *tlprofile.Dispatcher) {
	if d == nil {
		panic("rpc: register Android layer RPC adapter on nil dispatcher")
	}
	d.OnUnknownMethod(func(view tlprofile.UnknownMethodView) (tlprofile.OutboundCall, bool, error) {
		outbound, handled, err := compatandroid.AdaptPrivateLayerRPC(view)
		if !handled {
			return tlprofile.OutboundCall{}, false, nil
		}
		if err != nil {
			return tlprofile.OutboundCall{}, true, err
		}
		if r.log != nil {
			_, method, _ := tlprofile.SemanticName(outbound.Method())
			r.log.Info("Android private RPC admitted through generated exact adapter",
				zap.Int("profile", int(view.Profile())),
				zap.String("method", method),
				zap.String("private_wire_id", fmt.Sprintf("%#x", view.WireID())),
				zap.String("exact_wire_id", fmt.Sprintf("%#x", outbound.WireID())))
		}
		return outbound, true, nil
	})
}

// PrepareAdmittedReplay restores wrapper/client metadata and stages only the
// connection-local delivery effects of a successful cached request. It never
// calls the generated handler and therefore cannot repeat business side
// effects or advance a difference cursor a second time.
func (r *Router) PrepareAdmittedReplay(
	ctx context.Context,
	rawAuthKeyID [8]byte,
	sessionID int64,
	msgID int64,
	admissionSeq uint64,
	request tlprofile.Admission,
) (func() error, error) {
	if r == nil || r.dispatcher == nil || request.Prepared().WireSize() <= 0 {
		return nil, inputRequestInvalidErr()
	}
	_, method, ok := tlprofile.SemanticName(request.Call().Method())
	if !ok || method == "" {
		return nil, inputRequestInvalidErr()
	}
	effects, err := r.snapshotLayerRPCWrapperEffects(ctx, request)
	if err != nil {
		return nil, err
	}
	wireSize := request.Prepared().WireSize()
	profile := request.Call().Profile()
	_, profileKnown := request.EffectiveProfile()
	identity := request.Prepared().Identity()
	// The callback owns every cache/binder/store side effect and is attached to
	// the concrete rpc_result write attempt. Admission and protocol-plan commit
	// can therefore fail without publishing client metadata or readiness for a
	// response that never reached the replacement transport.
	var once sync.Once
	return func() error {
		var restoreErr error
		once.Do(func() {
			// A replay may happen after the request/read context is canceled, but it
			// must never turn slow auth/store/membership work into an unbounded
			// delivery hook. One deadline covers the complete restore transaction.
			replayCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), layerRPCReplayRestoreTimeout)
			defer cancel()
			replayCtx = withLayerAdmissionSequence(replayCtx, admissionSeq)
			replayCtx, updatesDelivery, prepareErr := r.prepareRPCDispatchContext(
				replayCtx,
				rawAuthKeyID,
				sessionID,
				wireSize,
				method,
			)
			if prepareErr != nil {
				r.log.Warn("prepare delivered exact RPC replay metadata",
					zap.String("method", method),
					zap.Int64("session_id", sessionID),
					zap.Error(prepareErr))
				restoreErr = fmt.Errorf("prepare delivered exact RPC replay metadata: %w", prepareErr)
				return
			}
			replayCtx = r.applyLayerRPCWrapperEffects(replayCtx, profile, profileKnown, identity, effects, msgID, admissionSeq, layerRPCWrapperApplyReplayRestore)
			if profileKnown && layerRPCProfileEvidenceFresh(replayCtx) {
				r.maybeMarkSessionReceivesUpdates(replayCtx)
			} else {
				updatesDelivery.suppressSessionActivation()
			}
			if updatesDelivery.hasWork() {
				// prepareRPCDispatchContext normally snapshots a cancellation-free
				// base for ordinary post-response work. Replay restoration is a
				// stricter ordered barrier, so every phase shares the overall deadline.
				updatesDelivery.baseCtx = replayCtx
				r.runUpdatesDeliveryPlan(updatesDelivery.snapshot())
			}
			if err := replayCtx.Err(); err != nil {
				restoreErr = fmt.Errorf("restore delivered exact RPC replay metadata: %w", err)
			}
		})
		return restoreErr
	}, nil
}

// AdmitUnprofiled is the only first-request admission path. Generated code
// either obtains authoritative profile evidence from invokeWithLayer or admits
// a closed terminal whose complete request and result wire graphs were proven
// invariant across every generated profile. The latter never freezes a layer.
func (r *Router) AdmitUnprofiled(b *bin.Buffer, limits tlprofile.Limits) (tlprofile.Admission, error) {
	if r == nil || r.dispatcher == nil {
		return tlprofile.Admission{}, internalErr()
	}
	return r.dispatcher.AdmitUnprofiled(b, limits)
}

// DispatchAdmitted executes one generated admission lease. invokeAfterMsg(s)
// dependencies must already have reached a terminal business outcome at the
// MTProto edge before this method is called.
func (r *Router) DispatchAdmitted(
	ctx context.Context,
	rawAuthKeyID [8]byte,
	sessionID int64,
	msgID int64,
	admissionSeq uint64,
	request tlprofile.Admission,
) (tlprofile.Result, string, error) {
	if r == nil || r.dispatcher == nil {
		return nil, "", internalErr()
	}
	prepared := request.Prepared()
	call := request.Call()
	category, method, ok := tlprofile.SemanticName(call.Method())
	if !ok || category != "function" || method == "" || prepared.WireSize() <= 0 {
		return nil, "", inputRequestInvalidErr()
	}
	_, profileKnown := request.EffectiveProfile()
	profileEvidenceFresh := layerRPCProfileEvidenceFresh(ctx)
	ctx = withLayerAdmissionSequence(ctx, admissionSeq)
	ctx, updatesDelivery, err := r.prepareRPCDispatchContext(ctx, rawAuthKeyID, sessionID, prepared.WireSize(), method)
	if err != nil {
		return nil, method, err
	}
	ctx, err = r.applyLayerRPCWrappers(ctx, msgID, admissionSeq, request)
	if err != nil {
		return nil, method, err
	}
	if !r.dispatcher.Has(call.Method()) {
		fields := append([]zap.Field{
			zap.String("method", method),
			zap.String("type_id", fmt.Sprintf("%#x", call.WireID())),
			zap.Int("profile", int(call.Profile())),
		}, r.contextLogFields(ctx)...)
		if r.log != nil {
			r.log.Warn("Unhandled RPC (compatibility trace)", fields...)
		}
		return nil, method, notImplementedErr()
	}
	canonicalID, hasCanonicalID := tlprofile.WireID(tlprofile.ProfileCanonical, call.Method())
	if !hasCanonicalID {
		return nil, method, inputRequestInvalidErr()
	}
	if r.deps.Auth != nil {
		if _, authorized := UserIDFrom(ctx); !authorized && !rpcAllowedWithoutAuthorization(canonicalID) {
			fields := append([]zap.Field{
				zap.String("method", method),
				zap.String("type_id", fmt.Sprintf("%#x", call.WireID())),
				zap.Int("profile", int(call.Profile())),
			}, r.contextLogFields(ctx)...)
			r.log.Info("RPC rejected before authorization", fields...)
			return nil, method, authKeyUnregisteredErr()
		}
	}
	if err := r.checkFrozenRPC(ctx, method); err != nil {
		return nil, method, err
	}
	if profileKnown && profileEvidenceFresh {
		r.maybeMarkSessionReceivesUpdates(ctx)
	}
	dbBefore := dbtrace.SnapshotFromContext(ctx)
	start := time.Now()
	result, err := r.dispatcher.Dispatch(ctx, request)
	dur := time.Since(start)
	dbDelta := dbtrace.SnapshotFromContext(ctx).Sub(dbBefore)
	fields := append([]zap.Field{
		zap.String("method", method),
		zap.String("type_id", fmt.Sprintf("%#x", call.WireID())),
		zap.Int("profile", int(call.Profile())),
		zap.Duration("dur", dur),
	}, r.contextLogFields(ctx)...)
	fields = dbtrace.AppendZapFields(fields, "handler_", dbDelta)
	if err != nil || dur > 100*time.Millisecond {
		if err != nil {
			fields = append(fields, zap.Error(err))
		}
		r.log.Info("RPC inner handled", fields...)
	} else {
		r.log.Debug("RPC inner handled", fields...)
	}
	if err == nil && result != nil {
		if !profileKnown || !profileEvidenceFresh {
			// A generated wire-invariant terminal can safely complete before the
			// client profile is known, but it is not evidence that this physical
			// session can consume proactive updates. Preserve delivery-gated cursor
			// and secret-event facts while withholding readiness/bootstrap effects.
			updatesDelivery.suppressSessionActivation()
		}
		r.registerUpdatesDeliveryPlan(ctx, updatesDelivery)
	}
	return result, method, err
}

type layerRPCWrapperEffect struct {
	semantic tlprofile.SemanticID
	layer    int
	info     ClientInfo
}

func (r *Router) snapshotLayerRPCWrapperEffects(ctx context.Context, request tlprofile.Admission) ([]layerRPCWrapperEffect, error) {
	if err := r.validateLayerRPCWrappers(ctx, request); err != nil {
		return nil, err
	}
	profile := request.Call().Profile()
	evidenceProfile, hasProfileEvidence := request.ProfileEvidence()
	if hasProfileEvidence && evidenceProfile != profile {
		return nil, inputRequestInvalidErr()
	}
	effectiveProfile, hasEffectiveProfile := request.EffectiveProfile()
	if hasEffectiveProfile && effectiveProfile != profile {
		return nil, inputRequestInvalidErr()
	}
	if !hasEffectiveProfile && request.WrapperCount() != 0 {
		// The generated no-profile route is deliberately a closed terminal. A
		// wrapper chain without an authoritative selector must never smuggle the
		// canonical representative into client/session metadata.
		return nil, inputRequestInvalidErr()
	}
	effects := make([]layerRPCWrapperEffect, 0, request.WrapperCount())
	for index := 0; index < request.WrapperCount(); index++ {
		wrapper, _ := request.Wrapper(index)
		effect := layerRPCWrapperEffect{semantic: wrapper.Semantic()}
		switch wrapper.Semantic() {
		case tlprofile.SemanticMethodInvokeWithLayer:
			layer, err := layerWrapperRequired[int](wrapper, "layer")
			if err != nil || layer != int(profile) {
				return nil, inputRequestInvalidErr()
			}
			effect.layer = layer
		case tlprofile.SemanticMethodInitConnection:
			info, err := clientInfoFromLayerWrapper(wrapper)
			if err != nil {
				return nil, err
			}
			effect.info = info
		case tlprofile.SemanticMethodInvokeAfterMsg, tlprofile.SemanticMethodInvokeAfterMsgs:
			// Dependency completion is an MTProto message-lifecycle fact. The edge
			// validates it before scheduling this one-shot admission lease.
		}
		effects = append(effects, effect)
	}
	return effects, nil
}

func (r *Router) applyLayerRPCWrapperEffects(
	ctx context.Context,
	profile tlprofile.Profile,
	profileKnown bool,
	identity tlprofile.PreparedIdentity,
	effects []layerRPCWrapperEffect,
	msgID int64,
	admissionSeq uint64,
	mode layerRPCWrapperApplyMode,
) context.Context {
	mutableEffectsCurrent := layerRPCProfileEvidenceFresh(ctx)
	rawAuthKeyID, hasRawAuthKeyID := RawAuthKeyIDFrom(ctx)
	sessionID, hasSessionID := SessionIDFrom(ctx)
	if msgID > 0 && hasRawAuthKeyID && hasSessionID {
		if currentLayer, currentMsgID, exists := r.NegotiatedSessionLayerEvidence(rawAuthKeyID, sessionID); exists {
			if currentMsgID > msgID || (currentMsgID == msgID && currentLayer != int(profile)) {
				mutableEffectsCurrent = false
			}
		}
		if mutableEffectsCurrent && hasMutableLayerRPCWrapperEffect(effects) {
			mutableEffectsCurrent = r.claimSessionWrapperEffects(rawAuthKeyID, sessionID, msgID)
		}
	}
	if profileKnown {
		// Handler and wrapper metadata always see the immutable admitted request
		// profile. This may intentionally differ from a later correction to the
		// mutable session default while an older request is still executing.
		ctx = WithLayer(ctx, int(profile))
	} else {
		// A wire-invariant terminal admitted with no effective profile must not
		// inherit an incidental metadata value loaded during context preparation.
		ctx = WithLayer(ctx, 0)
	}
	for _, effect := range effects {
		switch effect.semantic {
		case tlprofile.SemanticMethodInvokeWithLayer:
			ctx = WithLayer(ctx, effect.layer)
			// Shared Layer publication is an admission-time protocol effect. It has
			// already been linearized by admissionSeq before the scheduler/rewrap
			// split; handler execution and physical replay are intentionally unable
			// to move that auth-key-wide default.
		case tlprofile.SemanticMethodInvokeWithoutUpdates:
			ctx = withInvokeWithoutUpdates(ctx)
		case tlprofile.SemanticMethodInitConnection:
			if mode == layerRPCWrapperApplyReplayRestore {
				if _, exists := ClientInfoFrom(ctx); exists {
					// prepareRPCDispatchContext restored newer session/auth metadata.
					// Do not overwrite it with an older cached initConnection.
					continue
				}
			}
			ctx = WithClientInfo(ctx, effect.info)
			if !mutableEffectsCurrent {
				// Preserve request-local wrapper data for the delayed handler while
				// refusing to publish stale session/auth metadata.
				continue
			}
			r.rememberClientInfoAt(ctx, effect.info, admissionSeq)
			if r.log != nil {
				r.log.Debug("initConnection",
					zap.Int("api_id", effect.info.APIID),
					zap.String("device", effect.info.DeviceModel),
					zap.String("app_version", effect.info.AppVersion),
					zap.Int("layer", int(profile)),
					zap.String("client_type", string(ClientTypeFrom(ctx))),
				)
			}
		}
	}
	return context.WithValue(ctx, layerWrappersAppliedKey{}, identity)
}

func (r *Router) applyLayerRPCWrappers(ctx context.Context, msgID int64, admissionSeq uint64, request tlprofile.Admission) (context.Context, error) {
	effects, err := r.snapshotLayerRPCWrapperEffects(ctx, request)
	if err != nil {
		return nil, err
	}
	_, profileKnown := request.EffectiveProfile()
	return r.applyLayerRPCWrapperEffects(ctx, request.Call().Profile(), profileKnown, request.Prepared().Identity(), effects, msgID, admissionSeq, layerRPCWrapperApplyDispatch), nil
}

func hasMutableLayerRPCWrapperEffect(effects []layerRPCWrapperEffect) bool {
	for _, effect := range effects {
		switch effect.semantic {
		case tlprofile.SemanticMethodInvokeWithLayer, tlprofile.SemanticMethodInitConnection:
			return true
		}
	}
	return false
}

func (r *Router) validateLayerRPCWrappers(ctx context.Context, request tlprofile.Admission) error {
	for index := 0; index < request.WrapperCount(); index++ {
		wrapper, _ := request.Wrapper(index)
		switch wrapper.Semantic() {
		case tlprofile.SemanticMethodInvokeWithLayer,
			tlprofile.SemanticMethodInvokeWithoutUpdates,
			tlprofile.SemanticMethodInitConnection,
			tlprofile.SemanticMethodInvokeAfterMsg,
			tlprofile.SemanticMethodInvokeAfterMsgs:
			continue
		default:
			_, name, _ := tlprofile.SemanticName(wrapper.Semantic())
			fields := append([]zap.Field{
				zap.String("wrapper", name),
				zap.String("type_id", fmt.Sprintf("%#x", wrapper.WireID())),
				zap.Int("profile", int(wrapper.Profile())),
			}, r.contextLogFields(ctx)...)
			if r.log != nil {
				r.log.Warn("Unhandled RPC wrapper (compatibility trace)", fields...)
			}
			return notImplementedErr()
		}
	}
	return nil
}

func (r *Router) consumeLayerRPCWrappers(ctx context.Context, request tlprofile.Admission, next tlprofile.Next) error {
	applied, ok := ctx.Value(layerWrappersAppliedKey{}).(tlprofile.PreparedIdentity)
	if !ok || applied != request.Prepared().Identity() {
		return fmt.Errorf("rpc: exact wrapper context was not applied to admitted request")
	}
	return next(ctx)
}

func layerWrapperRequired[T any](wrapper tlprofile.Wrapper, name string) (T, error) {
	var zero T
	value, present, ok, err := wrapper.Value(name)
	if err != nil || !ok || !present {
		return zero, inputRequestInvalidErr()
	}
	typed, ok := value.(T)
	if !ok {
		return zero, inputRequestInvalidErr()
	}
	return typed, nil
}

func clientInfoFromLayerWrapper(wrapper tlprofile.Wrapper) (ClientInfo, error) {
	apiID, err := layerWrapperRequired[int](wrapper, "api_id")
	if err != nil {
		return ClientInfo{}, err
	}
	deviceModel, err := layerWrapperRequired[string](wrapper, "device_model")
	if err != nil {
		return ClientInfo{}, err
	}
	systemVersion, err := layerWrapperRequired[string](wrapper, "system_version")
	if err != nil {
		return ClientInfo{}, err
	}
	appVersion, err := layerWrapperRequired[string](wrapper, "app_version")
	if err != nil {
		return ClientInfo{}, err
	}
	systemLangCode, err := layerWrapperRequired[string](wrapper, "system_lang_code")
	if err != nil {
		return ClientInfo{}, err
	}
	langPack, err := layerWrapperRequired[string](wrapper, "lang_pack")
	if err != nil {
		return ClientInfo{}, err
	}
	langCode, err := layerWrapperRequired[string](wrapper, "lang_code")
	if err != nil {
		return ClientInfo{}, err
	}
	return ClientInfo{
		APIID:          apiID,
		DeviceModel:    deviceModel,
		SystemVersion:  systemVersion,
		AppVersion:     appVersion,
		SystemLangCode: systemLangCode,
		LangPack:       langPack,
		LangCode:       langCode,
	}, nil
}
