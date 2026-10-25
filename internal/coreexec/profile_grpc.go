package coreexec

import (
	"context"
	"time"

	"telesrv/internal/coreexec/coreexecpb"
)

func (r *GRPCRemote) ResolveNegotiatedSessionLayerEvidence(ctx context.Context, rawAuthKeyID [8]byte, sessionID int64) (layer int, msgID int64, found bool, err error) {
	if local := r.edgeAdmitter(); local != nil {
		return local.ResolveNegotiatedSessionLayerEvidence(ctx, rawAuthKeyID, sessionID)
	}
	return 0, 0, false, ErrNilHandler
}

func (r *GRPCRemote) ResolveInheritedAuthKeyLayer(ctx context.Context, rawAuthKeyID [8]byte) (layer int, found bool, err error) {
	if local := r.edgeAdmitter(); local != nil {
		return local.ResolveInheritedAuthKeyLayer(ctx, rawAuthKeyID)
	}
	return 0, false, ErrNilHandler
}

// PrimeAuthKeyLayerIdentity asks Core to run the authoritative cold identity
// loader. Production Edge uses this only for a temporary raw key whose Redis
// mapping is absent; per-selector profile evidence never traverses CoreExec.
func (r *GRPCRemote) PrimeAuthKeyLayerIdentity(ctx context.Context, rawAuthKeyID [8]byte) error {
	_, _, err := r.resolveInheritedAuthKeyLayerRemote(ctx, rawAuthKeyID)
	return err
}

func (r *GRPCRemote) resolveNegotiatedSessionLayerEvidenceRemote(ctx context.Context, rawAuthKeyID [8]byte, sessionID int64) (layer int, msgID int64, found bool, err error) {
	callCtx, cancel := r.withRequestTimeout(ctx)
	defer cancel()
	start := time.Now()
	res, err := r.client.ResolveNegotiatedSessionLayer(callCtx, &coreexecpb.AuthKeyRequest{
		RawAuthKeyId: append([]byte(nil), rawAuthKeyID[:]...),
		SessionId:    sessionID,
	})
	if err != nil {
		callErr := grpcRemoteCallError("resolve_negotiated_session_layer", err)
		observeCoreExecGRPCCall(r.metrics, "client", "resolve_negotiated_session_layer", start, grpcRemoteCallOutcome(callErr))
		return 0, 0, false, callErr
	}
	if err := errorFromResponse(res.GetError()); err != nil {
		observeCoreExecGRPCCall(r.metrics, "client", "resolve_negotiated_session_layer", start, grpcRemoteCallOutcome(err))
		return 0, 0, false, err
	}
	observeCoreExecGRPCCall(r.metrics, "client", "resolve_negotiated_session_layer", start, "ok")
	return int(res.GetLayer()), res.GetMsgId(), res.GetFound(), nil
}

func (r *GRPCRemote) resolveInheritedAuthKeyLayerRemote(ctx context.Context, rawAuthKeyID [8]byte) (layer int, found bool, err error) {
	callCtx, cancel := r.withRequestTimeout(ctx)
	defer cancel()
	start := time.Now()
	res, err := r.client.ResolveInheritedAuthKeyLayer(callCtx, &coreexecpb.AuthKeyRequest{
		RawAuthKeyId: append([]byte(nil), rawAuthKeyID[:]...),
	})
	if err != nil {
		callErr := grpcRemoteCallError("resolve_inherited_auth_key_layer", err)
		observeCoreExecGRPCCall(r.metrics, "client", "resolve_inherited_auth_key_layer", start, grpcRemoteCallOutcome(callErr))
		return 0, false, callErr
	}
	if err := errorFromResponse(res.GetError()); err != nil {
		observeCoreExecGRPCCall(r.metrics, "client", "resolve_inherited_auth_key_layer", start, grpcRemoteCallOutcome(err))
		return 0, false, err
	}
	observeCoreExecGRPCCall(r.metrics, "client", "resolve_inherited_auth_key_layer", start, "ok")
	return int(res.GetLayer()), res.GetFound(), nil
}

func (r *GRPCRemote) AdvanceNegotiatedSessionLayerEvidence(ctx context.Context, rawAuthKeyID [8]byte, sessionID int64, layer int, msgID int64) (currentLayer int, currentMsgID int64, publishShared bool, err error) {
	if local := r.edgeAdmitter(); local != nil {
		return local.AdvanceNegotiatedSessionLayerEvidence(ctx, rawAuthKeyID, sessionID, layer, msgID)
	}
	return 0, 0, false, ErrNilHandler
}

func (r *GRPCRemote) DeleteNegotiatedSessionLayerEvidence(ctx context.Context, rawAuthKeyID [8]byte, sessionID int64) (deleted bool, err error) {
	if local := r.edgeAdmitter(); local != nil {
		return local.DeleteNegotiatedSessionLayerEvidence(ctx, rawAuthKeyID, sessionID)
	}
	return false, ErrNilHandler
}

func (r *GRPCRemote) PublishAdmittedLayerProfileEvidence(ctx context.Context, rawAuthKeyID [8]byte, sessionID int64, msgID int64, admissionSeq uint64, safeFloor uint64, layer int) error {
	if local := r.edgeAdmitter(); local != nil {
		return local.PublishAdmittedLayerProfileEvidence(
			ctx, rawAuthKeyID, sessionID, msgID, admissionSeq, safeFloor, layer,
		)
	}
	return ErrNilHandler
}

func (r *GRPCRemote) ObserveInitConnection(ctx context.Context, authKeyID [8]byte, sessionID int64, layer, apiID int, deviceModel, systemVersion, appVersion, systemLangCode, langPack, langCode string) error {
	callCtx, cancel := r.withRequestTimeout(ctx)
	defer cancel()
	start := time.Now()
	res, err := r.client.ObserveInitConnection(callCtx, &coreexecpb.ObserveInitConnectionRequest{
		AuthKeyId:      append([]byte(nil), authKeyID[:]...),
		SessionId:      sessionID,
		Layer:          int32(layer),
		ApiId:          int32(apiID),
		DeviceModel:    deviceModel,
		SystemVersion:  systemVersion,
		AppVersion:     appVersion,
		SystemLangCode: systemLangCode,
		LangPack:       langPack,
		LangCode:       langCode,
		ClientIp:       clientIPToPB(ctx),
	})
	if err != nil {
		callErr := grpcRemoteCallError("observe_init_connection", err)
		observeCoreExecGRPCCall(r.metrics, "client", "observe_init_connection", start, grpcRemoteCallOutcome(callErr))
		return callErr
	}
	callErr := errorFromResponse(res.GetError())
	observeCoreExecGRPCCall(r.metrics, "client", "observe_init_connection", start, grpcRemoteCallOutcome(callErr))
	return callErr
}
