package rpc

import (
	"context"
	"time"

	"go.uber.org/zap"

	"telesrv/internal/domain"
	"telesrv/internal/postresponse"
)

const updatesDeliveryPhaseTimeout = 5 * time.Second

type updatesDeliveryPlanKey struct{}

// updatesDeliveryPlan collects every state transition that is justified by one
// successful RPC result. Dispatch owns the plan and registers exactly one
// post-response callback after the typed handler has succeeded. This keeps the
// baseline result ahead of pending updates on the physical MTProto stream and
// gives secret-chat delivery, session readiness and bootstrap publication one
// explicit order.
type updatesDeliveryPlan struct {
	baseCtx context.Context

	commitCursor  bool
	cursorAuthKey [8]byte
	cursorUserID  int64
	cursorState   domain.UpdateState
	cursorMode    domain.UpdateStateCommitMode

	markSecretDelivered bool
	secretDeviceKey     int64
	secretEventIDs      []int64

	markSessionReady bool
	readyUserID      int64

	publishBootstrap bool
	bootstrapUserID  int64
}

func withUpdatesDeliveryPlan(ctx context.Context) (context.Context, *updatesDeliveryPlan) {
	if ctx == nil {
		ctx = context.Background()
	}
	plan := &updatesDeliveryPlan{baseCtx: context.WithoutCancel(ctx)}
	return context.WithValue(ctx, updatesDeliveryPlanKey{}, plan), plan
}

func updatesDeliveryPlanFrom(ctx context.Context) (*updatesDeliveryPlan, bool) {
	if ctx == nil {
		return nil, false
	}
	plan, ok := ctx.Value(updatesDeliveryPlanKey{}).(*updatesDeliveryPlan)
	return plan, ok && plan != nil
}

func (p *updatesDeliveryPlan) hasWork() bool {
	return p != nil && (p.commitCursor || p.markSecretDelivered || p.markSessionReady || p.publishBootstrap)
}

func (p *updatesDeliveryPlan) stageCursor(authKeyID [8]byte, userID int64, state domain.UpdateState, mode domain.UpdateStateCommitMode) {
	if p == nil || userID == 0 {
		return
	}
	p.commitCursor = true
	p.cursorAuthKey = authKeyID
	p.cursorUserID = userID
	p.cursorState = state
	p.cursorMode = mode
}

func (p *updatesDeliveryPlan) stageSessionReady(userID int64) {
	if p == nil {
		return
	}
	p.markSessionReady = true
	p.readyUserID = userID
}

// suppressSessionActivation removes only effects which would make the current
// physical session eligible for proactive updates. Delivery-gated cursor and
// secret-event facts remain valid for a generated wire-invariant RPC result.
// This is used when exact admission has no authoritative profile evidence.
func (p *updatesDeliveryPlan) suppressSessionActivation() {
	if p == nil {
		return
	}
	p.markSessionReady = false
	p.readyUserID = 0
	p.publishBootstrap = false
	p.bootstrapUserID = 0
}

func (p *updatesDeliveryPlan) stageBaseline(userID, secretDeviceKey int64, secretEventIDs []int64, subscribe, bootstrap bool) {
	if p == nil {
		return
	}
	if secretDeviceKey != 0 && len(secretEventIDs) != 0 {
		p.markSecretDelivered = true
		p.secretDeviceKey = secretDeviceKey
		p.secretEventIDs = appendUniqueInt64s(p.secretEventIDs, secretEventIDs...)
	}
	if !subscribe {
		return
	}
	p.stageSessionReady(userID)
	if bootstrap && userID != 0 {
		p.publishBootstrap = true
		p.bootstrapUserID = userID
	}
}

func appendUniqueInt64s(dst []int64, values ...int64) []int64 {
	for _, value := range values {
		if value == 0 {
			continue
		}
		seen := false
		for _, existing := range dst {
			if existing == value {
				seen = true
				break
			}
		}
		if !seen {
			dst = append(dst, value)
		}
	}
	return dst
}

func (p *updatesDeliveryPlan) snapshot() updatesDeliveryPlan {
	if p == nil {
		return updatesDeliveryPlan{}
	}
	out := *p
	out.secretEventIDs = append([]int64(nil), p.secretEventIDs...)
	return out
}

// stageSessionUpdatesReadyAfterDelivery is used by ordinary bare RPCs. During
// Router.Dispatch it only mutates the request-owned plan; direct handler tests
// with a postresponse registry still get the same delivery-gated behavior.
func (r *Router) stageSessionUpdatesReadyAfterDelivery(ctx context.Context, userID int64) {
	if userID == 0 || r.deps.Sessions == nil {
		return
	}
	if plan, ok := updatesDeliveryPlanFrom(ctx); ok {
		plan.stageSessionReady(userID)
		return
	}
	plan := updatesDeliveryPlan{baseCtx: context.WithoutCancel(ctx)}
	plan.stageSessionReady(userID)
	r.registerUpdatesDeliveryPlan(ctx, &plan)
}

// stageUpdatesBaselineAfterDelivery adds the extra actions justified by a
// successful getState/getDifference result. A single plan also deduplicates the
// ordinary bare-RPC readiness declaration made by the common router path.
func (r *Router) stageUpdatesBaselineAfterDelivery(
	ctx context.Context,
	userID int64,
	cursor *domain.UpdateState,
	mode domain.UpdateStateCommitMode,
	secretEventIDs []int64,
	bootstrap bool,
) {
	var secretDeviceKey int64
	if len(secretEventIDs) != 0 {
		secretDeviceKey, _ = businessAuthKeyIDFrom(ctx)
	}
	subscribe := !invokeWithoutUpdatesFrom(ctx)
	stage := func(plan *updatesDeliveryPlan) {
		if cursor != nil && r.deps.Updates != nil {
			authKeyID, _ := AuthKeyIDFrom(ctx)
			plan.stageCursor(authKeyID, userID, *cursor, mode)
		}
		plan.stageBaseline(userID, secretDeviceKey, secretEventIDs, subscribe, bootstrap)
	}
	if plan, ok := updatesDeliveryPlanFrom(ctx); ok {
		stage(plan)
		return
	}
	plan := updatesDeliveryPlan{baseCtx: context.WithoutCancel(ctx)}
	stage(&plan)
	r.registerUpdatesDeliveryPlan(ctx, &plan)
}

func (r *Router) registerUpdatesDeliveryPlan(ctx context.Context, plan *updatesDeliveryPlan) {
	if plan == nil || !plan.hasWork() {
		return
	}
	snapshot := plan.snapshot()
	postresponse.Register(ctx, func() {
		r.runUpdatesDeliveryPlan(snapshot)
	})
}

// runUpdatesDeliveryPlan is ordered deliberately:
//  1. commit the exact account cursor carried by the delivered result;
//  2. the just-delivered difference may retire its projected secret-chat events;
//  3. membership routing is rebuilt before SetReceivesUpdates starts FIFO flush;
//  4. bootstrap jobs are published last, so they queue behind older pending updates.
//
// Each phase gets an independent timeout so one failed side effect cannot starve
// the remaining delivery-safe transitions.
func (r *Router) runUpdatesDeliveryPlan(plan updatesDeliveryPlan) {
	baseCtx := plan.baseCtx
	if baseCtx == nil {
		baseCtx = context.Background()
	}
	if plan.commitCursor && r.deps.Updates != nil && plan.cursorUserID != 0 {
		ctx, cancel := context.WithTimeout(baseCtx, updatesDeliveryPhaseTimeout)
		err := r.deps.Updates.CommitDeliveredState(ctx, plan.cursorAuthKey, plan.cursorUserID, plan.cursorState, plan.cursorMode)
		cancel()
		if err != nil {
			r.log.Warn("commit delivered update state after rpc_result",
				zap.Int64("user_id", plan.cursorUserID),
				zap.Int("pts", plan.cursorState.Pts),
				zap.Uint8("mode", uint8(plan.cursorMode)),
				zap.Error(err))
		}
	}
	if plan.markSecretDelivered && r.deps.SecretChats != nil && plan.secretDeviceKey != 0 && len(plan.secretEventIDs) != 0 {
		ctx, cancel := context.WithTimeout(baseCtx, updatesDeliveryPhaseTimeout)
		err := r.deps.SecretChats.MarkStateEventsDelivered(ctx, plan.secretDeviceKey, plan.secretEventIDs)
		cancel()
		if err != nil {
			r.log.Warn("mark encrypted state events delivered after rpc_result",
				zap.Int64("device_auth_key_id", plan.secretDeviceKey),
				zap.Int("event_count", len(plan.secretEventIDs)),
				zap.Error(err))
		}
	}
	if plan.markSessionReady {
		ctx, cancel := context.WithTimeout(baseCtx, updatesDeliveryPhaseTimeout)
		r.markSessionReceivesUpdatesNow(ctx, plan.readyUserID)
		cancel()
	}
	if plan.publishBootstrap {
		r.publishBootstrapAfterBaseline(baseCtx, plan.bootstrapUserID)
	}
}
