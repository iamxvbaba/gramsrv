package mtprotoedge

import (
	"context"
	"errors"
	"fmt"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/mt"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"github.com/iamxvbaba/td/tlprofile"
	"go.uber.org/zap"
)

var inboundLayerDecodeLimits = tlprofile.Limits{
	MaxWireBytes: maxInflightRPCBytes,
	// contacts.editCloseFriends and contacts.setBlocked deliberately allow
	// 5,000 entries. Keep the coarse generated allocation ceiling above every
	// registered semantic field cap; field callbacks reject 5,001 before the
	// typed decoder allocates the vector.
	MaxVectorElements:    8 << 10,
	MaxAggregateElements: 128 << 10,
	MaxDepth:             32,
}

var errDefaultLayerAdmission = errors.New("selected layer profile rejected naked RPC")

const (
	maxLayerRPCDependencyIDs = 128

	// Exact admission materializes canonical request structs, interface-backed
	// nested objects, vectors, strings, and byte slices before a scheduler task
	// exists. Current generated request-reachable TL structs are at most 376
	// bytes (guarded by TestLayerRPCAdmissionMaterializationConstants), so 512
	// bytes is the forward-drift ceiling. One ceiling per maximum decode depth is
	// reserved as fixed graph slack; the 60x wire factor covers repeated nested
	// values, slice/interface headers, payload copies, and allocator rounding.
	// The largest legal upload part (512 KiB) charges less than the ordinary
	// 32-MiB per-connection budget and therefore remains admissible by default.
	layerRPCAdmissionStaticObjectBytes = 512
	layerRPCAdmissionWireFactor        = 60
	layerRPCAdmissionGraphSlack        = layerRPCAdmissionStaticObjectBytes * 32
)

type layerRPCDependencySet struct {
	waiters []*rpcResultWaiter
	failed  bool
}

type layerRPCProfileEvidence struct {
	profile      tlprofile.Profile
	admissionSeq uint64
	present      bool
	fresh        bool
	commit       bool
	publish      bool
}

// layerRPCAdmissionCursor is the wire-ordered, side-effect-free view used while
// decoding one MTProto container. evidenceMsgID is the last explicit
// invokeWithLayer proof, not merely the profile used by an arbitrary cached
// request. It therefore advances only after generated admission reports
// ProfileEvidence.
type layerRPCAdmissionCursor struct {
	state         LayerProfileSnapshot
	rawLayer      int
	evidenceMsgID int64
}

func (c *layerRPCAdmissionCursor) observe(profile tlprofile.Profile, msgID int64) error {
	return c.observeRaw(int(profile), msgID)
}

func (c *layerRPCAdmissionCursor) observeRaw(layer int, msgID int64) error {
	if c == nil || msgID <= 0 {
		return fmt.Errorf("invalid provisional Layer evidence msg_id %d", msgID)
	}
	if layer <= 0 {
		return fmt.Errorf("invalid provisional Layer evidence %d", layer)
	}
	if c.evidenceMsgID != 0 {
		switch {
		case msgID < c.evidenceMsgID:
			return nil
		case msgID == c.evidenceMsgID:
			if c.rawLayer != layer {
				return fmt.Errorf("%w: msg_id %d selected Layer %d after Layer %d", ErrLayerProfileConflict, msgID, layer, c.rawLayer)
			}
			return nil
		}
	}
	c.state = LayerProfileSnapshot{}
	if profile, supported := tlprofile.ResolveProfile(layer); supported {
		c.state = LayerProfileSnapshot{Profile: profile, Origin: LayerProfileExplicit}
	}
	c.rawLayer = layer
	c.evidenceMsgID = msgID
	return nil
}

func (s *Server) initialLayerRPCAdmissionCursor(ctx context.Context, c *Conn) (layerRPCAdmissionCursor, error) {
	if c == nil {
		return layerRPCAdmissionCursor{}, fmt.Errorf("nil Conn for provisional Layer admission")
	}
	state, rawLayer, msgID := c.layerProfileRawEvidenceState()
	if rawLayer == 0 && state.Origin != LayerProfileUnknown {
		rawLayer = int(state.Profile)
	}
	cursor := layerRPCAdmissionCursor{state: state, rawLayer: rawLayer, evidenceMsgID: msgID}
	var (
		layer         int
		registryMsgID int64
		found         bool
	)
	if _, durable := s.layerRPC.(LayerRPCDurableSessionProfileResolver); durable {
		// Production resolves durable exact-session evidence once while creating
		// this physical Conn (seedInitialLayerProfile). The Conn snapshot is then
		// the admission authority until its own wire carries invokeWithLayer.
		// Re-reading PostgreSQL for every RPC/container batch puts a database RTT on
		// the protocol hot path and would also let another process silently rewrite
		// an already-live physical connection. A stale grammar self-corrects through
		// CONNECTION_LAYER_INVALID -> invokeWithLayer; a replacement Conn performs
		// a fresh durable seed before admitting its first request.
		return cursor, nil
	} else if resolver, ok := s.layerRPC.(LayerRPCOrderedSessionProfileResolver); ok {
		layer, registryMsgID, found = resolver.NegotiatedSessionLayerEvidence(c.authKeyID, c.sessionID)
	} else {
		return cursor, nil
	}
	if !found {
		return cursor, nil
	}
	if registryMsgID < 0 || layer <= 0 {
		return layerRPCAdmissionCursor{}, fmt.Errorf("invalid exact session Layer evidence layer=%d msg_id=%d", layer, registryMsgID)
	}
	if registryMsgID == 0 {
		if cursor.evidenceMsgID == 0 {
			cursor.state = LayerProfileSnapshot{}
			if profile, supported := tlprofile.ResolveProfile(layer); supported {
				cursor.state = LayerProfileSnapshot{Profile: profile, Origin: LayerProfileExplicit}
			}
			cursor.rawLayer = layer
		}
		return cursor, nil
	}
	if err := cursor.observeRaw(layer, registryMsgID); err != nil {
		return layerRPCAdmissionCursor{}, fmt.Errorf("merge exact session Layer evidence: %w", err)
	}
	// Restore the same raw watermark onto this physical generation. Unsupported
	// future Layers intentionally leave its usable profile unknown.
	if registryMsgID > 0 {
		if err := c.seedRawLayerEvidence(layer, registryMsgID); err != nil {
			return layerRPCAdmissionCursor{}, fmt.Errorf("restore exact session raw Layer evidence: %w", err)
		}
		cursor.state, cursor.rawLayer, cursor.evidenceMsgID = c.layerProfileRawEvidenceState()
	}
	return cursor, nil
}

// layerRPCAdmissionReservationSize returns a saturating conservative charge.
// Saturation deliberately turns hostile integer-sized inputs into ordinary
// capacity rejection; it must never wrap into a small accepted reservation.
func layerRPCAdmissionReservationSize(wireBytes int) int {
	if wireBytes < 0 {
		wireBytes = 0
	}
	maxInt := int(^uint(0) >> 1)
	if wireBytes > (maxInt-layerRPCAdmissionGraphSlack)/layerRPCAdmissionWireFactor {
		return maxInt
	}
	return wireBytes*layerRPCAdmissionWireFactor + layerRPCAdmissionGraphSlack
}

// prepareInboundLayerRPCBatch is the production API path. The whole container
// reserves conservative task/materialization capacity before the first exact
// decoder callback. Admission then classifies every request; fresh owners keep
// their original tickets, while replay/error/join entries are released without
// a release/reacquire gap for the retained tasks.
func (s *Server) prepareInboundLayerRPCBatch(ctx context.Context, c *Conn, plan *inboundPlan) error {
	var candidateItems []int
	var provisionalSpecs []inboundRPCSpec
	for index := range plan.items {
		item := &plan.items[index]
		if item.kind == inboundItemRPC {
			candidateItems = append(candidateItems, index)
			provisionalSpecs = append(provisionalSpecs, inboundRPCSpec{
				method: s.typeName(item.typeID),
				size:   layerRPCAdmissionReservationSize(len(item.body)),
			})
		}
	}
	if len(candidateItems) == 0 {
		return nil
	}

	reservation, err := c.reserveInboundRPCBatch(ctx, provisionalSpecs)
	if err != nil {
		if errors.Is(err, ErrInboundRPCQueueFull) {
			for _, index := range candidateItems {
				plan.items[index].kind = inboundItemCapacityError
			}
			return nil
		}
		return err
	}
	// Install ownership immediately: every return below is covered by plan.close.
	plan.rpcReservation = reservation

	// Decode the container in wire order with a provisional cursor. Explicit
	// evidence may select the grammar for a following naked item even when the
	// evidence request is already pending/completed. The ordered msg_id watermark
	// makes an old replay inert and rejects a same-msg_id conflicting selector.
	// Nothing mutates Conn/registry until exact flight identity accepts the item.
	admissionCursor, err := s.initialLayerRPCAdmissionCursor(ctx, c)
	if err != nil {
		return err
	}
	evidence := make([]layerRPCProfileEvidence, len(plan.items))
	for _, index := range candidateItems {
		item := &plan.items[index]
		itemState := admissionCursor.state
		existingProfile, existing := s.rpcResults.ExactAdmissionProfile(c.authKeyID, c.sessionID, item.msgID)
		if existing {
			itemState = LayerProfileSnapshot{Profile: existingProfile, Origin: LayerProfileExplicit}
		}
		admitted, method, err := s.decodeInboundLayerRPC(itemState, item.body)
		if err != nil {
			if terminal, recognized := wrappedDestroyAuthKeyTerminal(err); recognized {
				if terminal.WireSize != bin.Word || !validWrappedDestroyAuthKeyChain(terminal) {
					s.log.Debug("Wrapped destroy_auth_key terminal rejected",
						zap.Int("profile", int(terminal.Profile)),
						zap.Int("wire_size", terminal.WireSize),
						zap.Int("wrapper_count", terminal.WrapperCount()),
						zap.Int64("msg_id", item.msgID),
					)
					item.kind = inboundItemRPCAdmissionError
					item.method = "destroy_auth_key"
					item.payload = &mt.RPCError{ErrorCode: 400, ErrorMessage: "INPUT_REQUEST_INVALID"}
					c.metrics.InboundRPCDropped(item.method, "layer_admission")
					continue
				}
				if len(plan.items) != 1 {
					return errDestroyAuthKeyMustBeExclusive
				}
				item.kind = inboundItemDestroyAuthKey
				item.method = "destroy_auth_key"
				item.payload = destroyAuthKeyRequest{}
				continue
			}
			if errors.Is(err, ErrLayerProfileConflict) {
				return err
			}
			s.log.Debug("RPC exact admission rejected",
				zap.String("method", method),
				zap.String("auth_key_id", c.authKeyHex),
				zap.Int64("session_id", c.sessionID),
				zap.Int64("msg_id", item.msgID),
				zap.Error(err),
			)
			item.kind = inboundItemRPCAdmissionError
			item.method = method
			item.payload = layerRPCAdmissionError(err)
			c.metrics.InboundRPCDropped(method, "layer_admission")
			continue
		}
		item.admitted = admitted
		item.method = method
		if profile, hasEvidence := admitted.ProfileEvidence(); hasEvidence {
			if existing && profile != existingProfile {
				return fmt.Errorf("%w: cached msg_id %d used Layer %d but replay selected Layer %d", ErrLayerProfileConflict, item.msgID, existingProfile, profile)
			}
			evidence[index] = layerRPCProfileEvidence{profile: profile, present: true, fresh: item.profileEvidenceFresh()}
			if evidence[index].fresh {
				if err := admissionCursor.observe(profile, item.msgID); err != nil {
					return err
				}
			}
		}
	}

	var indices []int
	var reservationIndices []int
	var specs []inboundRPCSpec
	var ownersInPlan map[int64]*rpcResultOwnerLease
	flightCapacity := false
	clearedPostInitCandidates := false
	provisionalRewrapInitialized := c.rpcRewrapInitialized.Load()
	acceptedInitState := false
	for reservationIndex, index := range candidateItems {
		item := &plan.items[index]
		if item.kind != inboundItemRPC {
			continue
		}
		method := item.method
		_, isInitRewrap := admittedRPCRewrapInit(item.admitted)
		if isInitRewrap {
			if item.profileEvidenceFresh() {
				provisionalRewrapInitialized = true
				acceptedInitState = true
			}
			if candidate := s.rpcRewrap.claimSemantic(
				c,
				item.admitted.Prepared().SemanticIdentity(),
				item.admitted.Call().Identity(),
			); candidate != nil {
				claim, err := s.acquireAdmittedLayerRPC(c, item, &evidence[index])
				if errors.Is(err, ErrRPCResultFlightCapacity) {
					s.rpcRewrap.release(candidate)
					c.metrics.InboundRPCDropped(candidate.method, "flight_capacity")
					flightCapacity = true
					item.kind = inboundItemCapacityError
					continue
				}
				if err != nil {
					s.rpcRewrap.release(candidate)
					return err
				}
				method = item.method
				item.admissionSeq = claim.admissionSeq
				if evidence[index].present && evidence[index].fresh {
					evidence[index].commit = true
					evidence[index].admissionSeq = claim.admissionSeq
					evidence[index].publish = claim.state == rpcResultAcquireOwner
				}
				switch claim.state {
				case rpcResultAcquireCompleted:
					after, prepareErr := s.prepareAdmittedLayerRPCReplay(ctx, c, item.msgID, claim.admissionSeq, item.profileEvidenceFresh(), item.admitted)
					if prepareErr != nil {
						s.rpcRewrap.release(candidate)
						return prepareErr
					}
					s.rpcRewrap.commit(candidate)
					item.kind = inboundItemReplayRPC
					item.payload = claim.encoded
					if claim.executionKnown && claim.executionOK {
						item.replayAfterSuccessfulDelivery = after
					}
				case rpcResultAcquirePending:
					after, prepareErr := s.prepareAdmittedLayerRPCReplay(ctx, c, item.msgID, claim.admissionSeq, item.profileEvidenceFresh(), item.admitted)
					if prepareErr != nil {
						s.rpcRewrap.release(candidate)
						return prepareErr
					}
					s.rpcRewrap.commit(candidate)
					item.kind = inboundItemRewrappedRPC
					item.payload = claim.waiter
					plan.rewrapAliases = append(plan.rewrapAliases, &rpcRewrapAlias{
						conn: c, itemIndex: index, newReqID: item.msgID, method: candidate.method,
						oldWaiter:               claim.waiter,
						afterSuccessfulDelivery: after,
					})
				case rpcResultAcquireOwner:
					after, prepareErr := s.prepareAdmittedLayerRPCReplay(ctx, c, item.msgID, claim.admissionSeq, item.profileEvidenceFresh(), item.admitted)
					if prepareErr != nil {
						s.rpcRewrap.release(candidate)
						claim.owner.Abort()
						return prepareErr
					}
					if ownersInPlan == nil {
						ownersInPlan = make(map[int64]*rpcResultOwnerLease)
					}
					ownersInPlan[item.msgID] = claim.owner
					item.kind = inboundItemRewrappedRPC
					item.payload = claim.owner
					plan.rewrapAliases = append(plan.rewrapAliases, &rpcRewrapAlias{
						conn: c, itemIndex: index, newReqID: item.msgID, method: candidate.method,
						oldWaiter: candidate.waiter, newOwner: claim.owner,
						sourceConn: candidate.source, sourceOwner: candidate.owner,
						afterSuccessfulDelivery: after,
						candidate:               candidate, registry: s.rpcRewrap,
					})
				default:
					s.rpcRewrap.release(candidate)
					return ErrRPCResultFlightInvalid
				}
				s.log.Info("RPC init rewrap matched",
					zap.String("method", candidate.method),
					zap.Int64("old_req_msg_id", candidate.reqMsgID),
					zap.Int64("new_req_msg_id", item.msgID),
					zap.Bool("same_connection", candidate.source == c),
					zap.String("auth_key_id", c.authKeyHex), zap.Int64("session_id", c.sessionID))
				continue
			}
		} else if provisionalRewrapInitialized && !clearedPostInitCandidates {
			s.rpcRewrap.clearSession(c)
			clearedPostInitCandidates = true
		}

		claim, err := s.acquireAdmittedLayerRPC(c, item, &evidence[index])
		if errors.Is(err, ErrRPCResultFlightCapacity) {
			c.metrics.InboundRPCDropped(method, "flight_capacity")
			flightCapacity = true
			item.kind = inboundItemCapacityError
			continue
		}
		if err != nil {
			return err
		}
		method = item.method
		item.admissionSeq = claim.admissionSeq
		if evidence[index].present && evidence[index].fresh {
			evidence[index].commit = true
			evidence[index].publish = claim.state == rpcResultAcquireOwner
			evidence[index].admissionSeq = claim.admissionSeq
		}
		switch claim.state {
		case rpcResultAcquireCompleted:
			after, prepareErr := s.prepareAdmittedLayerRPCReplay(ctx, c, item.msgID, claim.admissionSeq, item.profileEvidenceFresh(), item.admitted)
			if prepareErr != nil {
				return prepareErr
			}
			item.kind = inboundItemReplayRPC
			item.payload = claim.encoded
			if claim.executionKnown && claim.executionOK {
				item.replayAfterSuccessfulDelivery = after
			}
		case rpcResultAcquirePending:
			if ownersInPlan[item.msgID] != nil {
				item.kind = inboundItemDuplicate
				item.payload = nil
			} else {
				after, prepareErr := s.prepareAdmittedLayerRPCReplay(ctx, c, item.msgID, claim.admissionSeq, item.profileEvidenceFresh(), item.admitted)
				if prepareErr != nil {
					return prepareErr
				}
				item.kind = inboundItemRewrappedRPC
				item.payload = claim.waiter
				plan.rewrapAliases = append(plan.rewrapAliases, &rpcRewrapAlias{
					conn: c, itemIndex: index, newReqID: item.msgID, method: method, oldWaiter: claim.waiter,
					afterSuccessfulDelivery: after,
				})
			}
		case rpcResultAcquireOwner:
			if ownersInPlan == nil {
				ownersInPlan = make(map[int64]*rpcResultOwnerLease)
				indices = make([]int, 0, len(plan.items))
				specs = make([]inboundRPCSpec, 0, len(plan.items))
			}
			ownersInPlan[item.msgID] = claim.owner
			plan.rpcOwners = append(plan.rpcOwners, claim.owner)
			item.payload = claim.owner
			indices = append(indices, index)
			reservationIndices = append(reservationIndices, reservationIndex)
			specs = append(specs, inboundRPCSpec{
				method: method,
				size:   layerRPCAdmissionReservationSize(item.admitted.Prepared().WireSize()),
			})
			if !provisionalRewrapInitialized && !isInitRewrap {
				s.rpcRewrap.registerSemantic(
					c,
					item.admitted.Prepared().SemanticIdentity(),
					item.admitted.Call().Identity(),
					item.msgID,
					method,
					claim.owner,
				)
			}
		default:
			return ErrRPCResultFlightInvalid
		}
	}

	if flightCapacity {
		for _, index := range indices {
			plan.items[index].kind = inboundItemCapacityError
		}
		plan.rejectNewRPCOwners(indices)
		for _, index := range candidateItems {
			plan.items[index].admitted = tlprofile.Admission{}
		}
		if err := reservation.retain(nil, nil); err != nil {
			return err
		}
		plan.rpcReservation = nil
		return nil
	}
	var profileCapacityItems map[int]struct{}
	for _, index := range candidateItems {
		proof := evidence[index]
		if !proof.present || !proof.commit {
			continue
		}
		current, err := s.commitLayerProfileEvidence(ctx, c, proof.profile, plan.items[index].msgID)
		if err != nil {
			if isLayerEvidenceDurabilityUnavailable(err) {
				if _, localErr := c.freezeLayerProfileAt(proof.profile, plan.items[index].msgID); localErr != nil {
					return fmt.Errorf("apply connection-local Layer evidence fallback: %w", localErr)
				}
				// The durable failure limits propagation, not validity of the
				// selector on this physical connection. Keep fresh=true so an
				// initConnection can initialize this Conn, enable updates and retain
				// its device metadata. Only exact/auth-key/session publication is
				// suppressed; a replacement connection must negotiate again.
				evidence[index].publish = false
				s.log.Error("Durable exact session Layer unavailable; using connection-local request profile",
					zap.String("auth_key_id", c.authKeyHex), zap.Int64("session_id", c.sessionID),
					zap.Int64("msg_id", plan.items[index].msgID), zap.Int("layer", int(proof.profile)), zap.Error(err))
				continue
			}
			if isExactSessionProfileCapacityError(err) {
				if profileCapacityItems == nil {
					profileCapacityItems = make(map[int]struct{})
				}
				profileCapacityItems[index] = struct{}{}
				continue
			}
			return err
		}
		evidence[index].publish = evidence[index].publish && current
	}
	if len(profileCapacityItems) != 0 {
		// Match the existing whole-container flight-capacity policy: no newly
		// acquired owner from this admission unit may execute or publish shared
		// evidence. Completed/pending joins remain replayable unless their own
		// fresh selector was the capacity failure.
		for index := range profileCapacityItems {
			item := &plan.items[index]
			item.kind = inboundItemCapacityError
			if _, ownsFlight := item.payload.(*rpcResultOwnerLease); !ownsFlight {
				item.payload = nil
			}
		}
		plan.rejectNewRPCOwners(indices)
		keptAliases := plan.rewrapAliases[:0]
		for _, alias := range plan.rewrapAliases {
			if alias == nil {
				continue
			}
			if _, rejected := profileCapacityItems[alias.itemIndex]; rejected {
				alias.releaseCandidate()
				continue
			}
			keptAliases = append(keptAliases, alias)
		}
		plan.rewrapAliases = keptAliases
		for _, index := range candidateItems {
			plan.items[index].admitted = tlprofile.Admission{}
		}
		if err := reservation.retain(nil, nil); err != nil {
			return err
		}
		plan.rpcReservation = nil
		return nil
	}
	if acceptedInitState {
		// Every explicit profile commit, including durable local-only fallback, has
		// now classified the item. Only a still-fresh init wrapper may advance the
		// connection-initialized marker.
		for _, index := range candidateItems {
			if plan.items[index].profileEvidenceFresh() {
				if _, isInit := admittedRPCRewrapInit(plan.items[index].admitted); isInit {
					c.rpcRewrapInitialized.Store(true)
					break
				}
			}
		}
	}
	for _, index := range candidateItems {
		proof := evidence[index]
		if !proof.present || !proof.commit {
			continue
		}
		if evidence[index].publish {
			publisher, ok := s.layerRPC.(LayerRPCAdmissionProfilePublisher)
			if ok {
				if proof.admissionSeq == 0 {
					return ErrRPCAdmissionSeqExhausted
				}
				safeFloor := s.rpcResults.stableAdmissionSafeFloor()
				if safeFloor == 0 || safeFloor > proof.admissionSeq {
					return fmt.Errorf("invalid active admission safe floor %d for sequence %d", safeFloor, proof.admissionSeq)
				}
				if err := publisher.PublishAdmittedLayerProfileEvidence(
					ctx, c.authKeyID, c.sessionID, plan.items[index].msgID,
					proof.admissionSeq, safeFloor, int(proof.profile),
				); err != nil {
					return fmt.Errorf("publish admitted Layer profile evidence: %w", err)
				}
			}
		}
	}
	if len(specs) == 0 {
		for _, index := range candidateItems {
			plan.items[index].admitted = tlprofile.Admission{}
		}
		if err := reservation.retain(nil, nil); err != nil {
			return err
		}
		plan.rpcReservation = nil
		return nil
	}

	dependencies := make([]layerRPCDependencySet, len(indices))
	for taskIndex, itemIndex := range indices {
		item := &plan.items[itemIndex]
		dependencies[taskIndex] = s.layerRPCDependencies(c, item.msgID, item.admitted)
	}
	plan.rpcTasks = make([]inboundRPC, len(indices))
	for taskIndex, itemIndex := range indices {
		item := &plan.items[itemIndex]
		owner, _ := item.payload.(*rpcResultOwnerLease)
		plan.rpcTasks[taskIndex] = s.newInboundLayerRPCTask(c, item.msgID, item.admissionSeq, item.method, item.profileEvidenceFresh(), item.admitted, dependencies[taskIndex], owner)
	}
	// Tasks now own the admitted request leases. Drop the plan's value copies
	// before non-fresh reservations become reusable.
	for _, index := range candidateItems {
		plan.items[index].admitted = tlprofile.Admission{}
	}
	if err := reservation.retain(reservationIndices, specs); err != nil {
		return err
	}
	return nil
}

// acquireAdmittedLayerRPC resolves the only legal admission TOCTOU: two
// replacement physical connections may both miss the pre-decode hint and use
// different inherited defaults for the same naked body. The winner's stored
// profile is then authoritative for this msg_id. Re-decode once under that
// profile and retry the full identity claim; a changed body still mismatches.
// The mismatch carries that profile from inside the shard lock, so an immediate
// winner abort/eviction cannot make us fall back to an ambiguous loser default.
func (s *Server) acquireAdmittedLayerRPC(
	c *Conn,
	item *inboundItem,
	evidence *layerRPCProfileEvidence,
) (rpcResultAcquire, error) {
	if s == nil || c == nil || item == nil {
		return rpcResultAcquire{}, ErrRPCResultFlightInvalid
	}
	acquire := func() (rpcResultAcquire, error) {
		profile := tlprofile.Profile(0)
		if effective, known := item.admitted.EffectiveProfile(); known {
			profile = effective
		}
		return s.rpcResults.AcquireLayerIdentified(
			c.authKeyID, c.sessionID, item.msgID,
			profile, item.admitted.Prepared().Identity(),
		)
	}
	claim, err := acquire()
	if !errors.Is(err, ErrRPCResultIdentityMismatch) {
		return claim, err
	}
	var mismatch *rpcResultIdentityMismatchError
	if !errors.As(err, &mismatch) || !mismatch.hasProfile {
		return rpcResultAcquire{}, err
	}
	winnerProfile := mismatch.profile
	if winnerProfile == item.admitted.Call().Profile() {
		// Same profile plus a different full identity is a same-msg_id body
		// mutation, not an inherited-default race.
		return rpcResultAcquire{}, err
	}
	redecoded, method, decodeErr := s.decodeInboundLayerRPC(
		LayerProfileSnapshot{Profile: winnerProfile, Origin: LayerProfileExplicit},
		item.body,
	)
	if decodeErr != nil {
		return rpcResultAcquire{}, decodeErr
	}
	item.admitted = redecoded
	item.method = method
	if evidence != nil {
		fresh := evidence.fresh
		*evidence = layerRPCProfileEvidence{fresh: fresh}
		if profile, present := redecoded.ProfileEvidence(); present {
			evidence.profile = profile
			evidence.present = true
		}
	}
	return acquire()
}

func (s *Server) prepareAdmittedLayerRPCReplay(ctx context.Context, c *Conn, msgID int64, admissionSeq uint64, profileEvidenceFresh bool, request tlprofile.Admission) (func() error, error) {
	preparer, ok := s.layerRPC.(LayerRPCReplayPreparer)
	if !ok || c == nil {
		return nil, nil
	}
	ctx = s.withLayerRPCProfileEvidenceFresh(ctx, profileEvidenceFresh)
	after, err := preparer.PrepareAdmittedReplay(ctx, c.authKeyID, c.sessionID, msgID, admissionSeq, request)
	if err != nil {
		s.log.Warn("Prepare exact RPC replay side effects failed",
			zap.String("auth_key_id", c.authKeyHex),
			zap.Int64("session_id", c.sessionID),
			zap.Error(err))
		return nil, fmt.Errorf("prepare exact RPC replay effects: %w", err)
	}
	return after, nil
}

func (s *Server) withLayerRPCProfileEvidenceFresh(ctx context.Context, fresh bool) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if decorator, ok := s.layerRPC.(LayerRPCProfileEvidenceContext); ok {
		if decorated := decorator.WithLayerRPCProfileEvidenceFresh(ctx, fresh); decorated != nil {
			return decorated
		}
	}
	return ctx
}

// admitInboundLayerRPC is the force-style compatibility entry point used by
// focused tests and old embedders. Production must call admitInboundLayerRPCAt
// with the real inner MTProto client msg_id.
func (s *Server) admitInboundLayerRPC(c *Conn, body []byte) (tlprofile.Admission, string, error) {
	return s.admitInboundLayerRPCAt(c, 0, body)
}

func (s *Server) admitInboundLayerRPCAt(c *Conn, msgID int64, body []byte) (tlprofile.Admission, string, error) {
	if s == nil || s.layerRPC == nil || c == nil || len(body) < bin.Word {
		return tlprofile.Admission{}, "unknown", fmt.Errorf("invalid exact RPC admission input")
	}
	request, method, err := s.decodeInboundLayerRPC(c.LayerProfileState(), body)
	if err != nil {
		return tlprofile.Admission{}, method, err
	}
	if profile, hasEvidence := request.ProfileEvidence(); hasEvidence {
		if _, err := s.commitLayerProfileEvidence(context.Background(), c, profile, msgID); err != nil {
			if !isLayerEvidenceDurabilityUnavailable(err) {
				return tlprofile.Admission{}, method, err
			}
			if msgID > 0 {
				if _, localErr := c.freezeLayerProfileAt(profile, msgID); localErr != nil {
					return tlprofile.Admission{}, method, localErr
				}
			}
		}
	}
	return request, method, nil
}

// layerRPCAdmissionHasExplicitSelector distinguishes a failed naked request
// under a stale default from a failed explicit correction. Generated
// unsupported/conflicting invokeWithLayer failures carry the wrapper semantic
// in LayerCodecError. A truncated selector can fail before that typed error is
// constructed, so the bounded fallback walks only transparent wrapper prefixes
// whose query offset is fixed and allocation-free.
func layerRPCAdmissionHasExplicitSelector(body []byte, admissionErr error) bool {
	var codecErr *tlprofile.LayerCodecError
	if errors.As(admissionErr, &codecErr) && codecErr.Semantic == tlprofile.SemanticMethodInvokeWithLayer {
		return true
	}

	probe := &bin.Buffer{Buf: body}
	for depth := 0; depth < inboundLayerDecodeLimits.MaxDepth; depth++ {
		id, err := probe.PeekID()
		if err != nil {
			return false
		}
		switch id {
		case tg.InvokeWithLayerRequestTypeID:
			// Seeing the constructor is enough: a missing/malformed layer field is
			// itself an explicit correction attempt and must retain its real error.
			return true
		case tg.InvokeWithoutUpdatesRequestTypeID:
			if err := probe.ConsumeID(id); err != nil {
				return false
			}
		case tg.InvokeAfterMsgRequestTypeID:
			if err := probe.ConsumeID(id); err != nil {
				return false
			}
			if _, err := probe.Long(); err != nil {
				return false
			}
		case tg.InvokeAfterMsgsRequestTypeID:
			if err := probe.ConsumeID(id); err != nil {
				return false
			}
			count, err := probe.VectorHeader()
			if err != nil || count > probe.Len()/8 {
				return false
			}
			probe.Buf = probe.Buf[count*8:]
		default:
			return false
		}
	}
	return false
}

// decodeInboundLayerRPC is side-effect free. Production batch admission uses
// it with a wire-ordered provisional profile cursor, then publishes explicit
// evidence only after the full request identity has acquired an owner (or a
// genuine new-msg_id rewrap alias).
func (s *Server) decodeInboundLayerRPC(state LayerProfileSnapshot, body []byte) (tlprofile.Admission, string, error) {
	if s == nil || s.layerRPC == nil || len(body) < bin.Word {
		return tlprofile.Admission{}, "unknown", fmt.Errorf("invalid exact RPC admission input")
	}
	b := &bin.Buffer{Buf: body}
	var (
		request tlprofile.Admission
		err     error
	)
	if state.Origin != LayerProfileUnknown {
		if admitter, ok := s.layerRPC.(LayerRPCDefaultProfileAdmitter); ok {
			request, err = admitter.AdmitDefaultLayer(state.Profile, b, inboundLayerDecodeLimits)
		} else {
			// Compatibility fallback for old package tests/mocks. Production Router
			// implements default admission so explicit invokeWithLayer can correct.
			request, err = s.layerRPC.AdmitLayer(state.Profile, b, inboundLayerDecodeLimits)
		}
	} else {
		request, err = s.layerRPC.AdmitUnprofiled(b, inboundLayerDecodeLimits)
	}
	method := "unknown"
	if err == nil {
		_, method, _ = tlprofile.SemanticName(request.Call().Method())
		if b.Len() != 0 {
			return tlprofile.Admission{}, method, fmt.Errorf("exact RPC admission left %d bytes", b.Len())
		}
		if effective, known := request.EffectiveProfile(); known && effective != request.Call().Profile() {
			return tlprofile.Admission{}, method, fmt.Errorf("%w: effective profile %d differs from call profile %d", ErrLayerProfileConflict, effective, request.Call().Profile())
		}
		// A generated invariant terminal may use canonical decoding internally
		// before the client declares a layer. Only explicit invokeWithLayer (or the
		// strict compatibility fallback above) publishes new profile evidence.
		if profile, hasEvidence := request.ProfileEvidence(); hasEvidence {
			if profile != request.Call().Profile() {
				return tlprofile.Admission{}, method, fmt.Errorf("%w: generated profile evidence %d differs from call profile %d", ErrLayerProfileConflict, profile, request.Call().Profile())
			}
		}
		return request, method, nil
	}
	if state.Origin != LayerProfileUnknown && !layerRPCAdmissionHasExplicitSelector(body, err) {
		// Both inherited metadata and a restored same-session snapshot may be stale
		// after a client upgrade. One naked-RPC failure asks the official client to
		// clear connectionInited and resend explicit invokeWithLayer. Explicit
		// retries keep their real unsupported/conflict/malformed error even when the
		// selector sits below invokeAfter* or invokeWithoutUpdates.
		err = fmt.Errorf("%w: %w", errDefaultLayerAdmission, err)
	}
	if id, peekErr := (&bin.Buffer{Buf: body}).PeekID(); peekErr == nil {
		method = s.typeName(id)
	}
	if codecErr := new(tlprofile.LayerCodecError); errors.As(err, &codecErr) {
		if codecErr.Semantic != 0 {
			if _, semanticMethod, ok := tlprofile.SemanticName(codecErr.Semantic); ok && semanticMethod != "" {
				method = semanticMethod
			}
		} else if codecErr.WireID != 0 {
			// Nested unknown terminals have no semantic id. Prefer the generated
			// failing constructor over the top-level transparent wrapper so the
			// compatibility trace names the actual missing RPC.
			method = s.typeName(codecErr.WireID)
		}
	}
	if errors.Is(err, tlprofile.ErrUnknownRPCMethod) && s.log != nil {
		if terminal, recognized := wrappedDestroyAuthKeyTerminal(err); recognized {
			method = "destroy_auth_key"
			s.log.Debug("Generated wrapper admission exposed MTProto service terminal",
				zap.String("method", method),
				zap.Int("profile", int(terminal.Profile)),
				zap.Uint32("wire_id", terminal.WireID),
				zap.Int("wire_size", terminal.WireSize),
			)
		} else {
			s.log.Warn("Unhandled RPC admission (compatibility trace)",
				zap.String("method", method), zap.Error(err))
		}
	}
	return tlprofile.Admission{}, method, err
}

// commitLayerProfileEvidence publishes one generated invokeWithLayer proof.
// The exact-session registry is the cross-physical-connection linearization
// point; the Conn cursor then prevents a concurrent older admission from
// overwriting its local wire epoch. Older cached duplicates remain decodable
// and request-bound, but cannot mutate session/profile state.
func (s *Server) commitLayerProfileEvidence(ctx context.Context, c *Conn, profile tlprofile.Profile, msgID int64) (bool, error) {
	if s == nil || c == nil {
		return false, fmt.Errorf("invalid layer profile evidence target")
	}
	if msgID > 0 {
		if registry, ok := s.layerRPC.(LayerRPCDurableSessionProfileAdvancer); ok {
			layer, authoritativeMsgID, publishShared, err := registry.AdvanceNegotiatedSessionLayerEvidence(
				ctx, c.authKeyID, c.sessionID, int(profile), msgID,
			)
			if err != nil {
				return false, fmt.Errorf("advance durable exact session Layer evidence: %w", err)
			}
			if layer <= 0 || authoritativeMsgID <= 0 {
				return false, fmt.Errorf("%w: durable exact session evidence returned layer=%d msg_id=%d", ErrLayerProfileConflict, layer, authoritativeMsgID)
			}
			if s.conns != nil {
				if _, err := s.conns.ApplyOrderedRawLayerForSession(c, c.authKeyID, c.sessionID, layer, authoritativeMsgID); err != nil {
					return false, err
				}
			} else if _, err := c.freezeRawLayerProfileAt(layer, authoritativeMsgID); err != nil {
				return false, err
			}
			authoritative, supported := tlprofile.ResolveProfile(layer)
			return supported && authoritative == profile && authoritativeMsgID == msgID && publishShared, nil
		}
		if registry, ok := s.layerRPC.(LayerRPCOrderedSessionProfileRegistry); ok {
			_, err := registry.FreezeNegotiatedSessionLayerAt(c.authKeyID, c.sessionID, int(profile), msgID)
			if err != nil {
				if isExactSessionProfileCapacityError(err) {
					return false, fmt.Errorf("freeze ordered exact session registry capacity: %w", err)
				}
				return false, fmt.Errorf("%w: freeze ordered exact session registry: %w", ErrLayerProfileConflict, err)
			}
			// Always re-read and broadcast the authoritative registry value, even
			// when this proof was stale/identical. Two physical generations may
			// interleave registry commit and local publication; broadcasting the
			// max cursor to every active/claim Conn makes their final state converge.
			layer, authoritativeMsgID, found := registry.NegotiatedSessionLayerEvidence(c.authKeyID, c.sessionID)
			if !found || authoritativeMsgID <= 0 {
				return false, fmt.Errorf("%w: ordered exact session evidence disappeared after commit", ErrLayerProfileConflict)
			}
			authoritative, supported := tlprofile.ResolveProfile(layer)
			if s.conns != nil {
				if _, err := s.conns.ApplyOrderedRawLayerForSession(c, c.authKeyID, c.sessionID, layer, authoritativeMsgID); err != nil {
					return false, err
				}
			} else if _, err := c.freezeRawLayerProfileAt(layer, authoritativeMsgID); err != nil {
				return false, err
			}
			if !supported {
				// A future durable Layer is an ordering watermark, not a codec this
				// binary can use. Keep every Conn unknown and let a greater supported
				// invokeWithLayer self-heal it.
				return false, nil
			}
			return authoritative == profile && authoritativeMsgID == msgID, nil
		}
		// Older handlers without a persistent ordered registry still receive
		// per-Conn replay protection. Production Router implements the interface.
		applied, err := c.freezeLayerProfileAt(profile, msgID)
		if err != nil || !applied {
			return applied, err
		}
		if registry, ok := s.layerRPC.(LayerRPCSessionProfileRegistry); ok {
			if err := registry.FreezeNegotiatedSessionLayer(c.authKeyID, c.sessionID, int(profile)); err != nil {
				return false, fmt.Errorf("%w: freeze exact session registry: %v", ErrLayerProfileConflict, err)
			}
		}
		if s.conns != nil {
			s.conns.SeedInheritedLayerForRawAuthKey(c.authKeyID, int(profile))
		}
		return true, nil
	}

	// msgID==0 intentionally preserves the previous force semantics for direct
	// admission tests. It is never used by the production batch path.
	if registry, ok := s.layerRPC.(LayerRPCSessionProfileRegistry); ok {
		if err := registry.FreezeNegotiatedSessionLayer(c.authKeyID, c.sessionID, int(profile)); err != nil {
			return false, fmt.Errorf("%w: freeze exact session registry: %v", ErrLayerProfileConflict, err)
		}
	}
	if err := c.FreezeLayerProfile(profile); err != nil {
		return false, err
	}
	if s.conns != nil {
		s.conns.SeedInheritedLayerForRawAuthKey(c.authKeyID, int(profile))
	}
	return true, nil
}

type exactSessionProfileCapacityMarker interface {
	ExactSessionProfileCapacity()
}

type layerEvidenceDurabilityUnavailableMarker interface {
	LayerEvidenceDurabilityUnavailable()
}

func isExactSessionProfileCapacityError(err error) bool {
	var marker exactSessionProfileCapacityMarker
	return errors.As(err, &marker)
}

func isLayerEvidenceDurabilityUnavailable(err error) bool {
	var marker layerEvidenceDurabilityUnavailableMarker
	return errors.As(err, &marker)
}

func layerRPCAdmissionError(err error) *mt.RPCError {
	if errors.Is(err, errDefaultLayerAdmission) {
		return &mt.RPCError{ErrorCode: 400, ErrorMessage: "CONNECTION_LAYER_INVALID"}
	}
	if errors.Is(err, tlprofile.ErrProfileRequired) {
		return &mt.RPCError{ErrorCode: 400, ErrorMessage: "CONNECTION_NOT_INITED"}
	}
	var rpcErr *tgerr.Error
	if errors.As(err, &rpcErr) {
		return &mt.RPCError{ErrorCode: rpcErr.Code, ErrorMessage: rpcErr.Message}
	}
	if errors.Is(err, tlprofile.ErrUnknownRPCMethod) {
		return &mt.RPCError{ErrorCode: 501, ErrorMessage: "NOT_IMPLEMENTED"}
	}
	return &mt.RPCError{ErrorCode: 400, ErrorMessage: "INPUT_REQUEST_INVALID"}
}

func (s *Server) layerRPCDependencies(c *Conn, msgID int64, request tlprofile.Admission) layerRPCDependencySet {
	result := layerRPCDependencySet{}
	seen := make(map[int64]struct{})
	for index := 0; index < request.WrapperCount(); index++ {
		wrapper, _ := request.Wrapper(index)
		var ids []int64
		switch wrapper.Semantic() {
		case tlprofile.SemanticMethodInvokeAfterMsg:
			id, err := layerRPCWrapperRequired[int64](wrapper, "msg_id")
			if err != nil {
				result.failed = true
				continue
			}
			ids = []int64{id}
		case tlprofile.SemanticMethodInvokeAfterMsgs:
			var err error
			ids, err = layerRPCWrapperRequired[[]int64](wrapper, "msg_ids")
			if err != nil || len(ids) > maxLayerRPCDependencyIDs {
				result.failed = true
				continue
			}
		default:
			continue
		}
		for _, dependencyID := range ids {
			if dependencyID <= 0 || dependencyID >= msgID {
				result.failed = true
				continue
			}
			if _, duplicate := seen[dependencyID]; duplicate {
				continue
			}
			seen[dependencyID] = struct{}{}
			dependency, ok := s.rpcResults.ObserveDependency(c.authKeyID, c.sessionID, dependencyID)
			if !ok {
				result.failed = true
				continue
			}
			if dependency.completed {
				if !dependency.success {
					result.failed = true
				}
				continue
			}
			if dependency.waiter != nil {
				result.waiters = append(result.waiters, dependency.waiter)
			}
		}
	}
	return result
}

func admittedRPCRewrapInit(request tlprofile.Admission) (rpcRewrapInit, bool) {
	if request.WrapperCount() != 2 {
		return rpcRewrapInit{}, false
	}
	layerWrapper, ok := request.Wrapper(0)
	if !ok || layerWrapper.Semantic() != tlprofile.SemanticMethodInvokeWithLayer {
		return rpcRewrapInit{}, false
	}
	initWrapper, ok := request.Wrapper(1)
	if !ok || initWrapper.Semantic() != tlprofile.SemanticMethodInitConnection {
		return rpcRewrapInit{}, false
	}
	layer, err := layerRPCWrapperRequired[int](layerWrapper, "layer")
	if err != nil || layer != int(request.Call().Profile()) {
		return rpcRewrapInit{}, false
	}
	apiID, err := layerRPCWrapperRequired[int](initWrapper, "api_id")
	if err != nil {
		return rpcRewrapInit{}, false
	}
	deviceModel, err := layerRPCWrapperRequired[string](initWrapper, "device_model")
	if err != nil {
		return rpcRewrapInit{}, false
	}
	system, err := layerRPCWrapperRequired[string](initWrapper, "system_version")
	if err != nil {
		return rpcRewrapInit{}, false
	}
	appVersion, err := layerRPCWrapperRequired[string](initWrapper, "app_version")
	if err != nil {
		return rpcRewrapInit{}, false
	}
	systemLang, err := layerRPCWrapperRequired[string](initWrapper, "system_lang_code")
	if err != nil {
		return rpcRewrapInit{}, false
	}
	langPack, err := layerRPCWrapperRequired[string](initWrapper, "lang_pack")
	if err != nil {
		return rpcRewrapInit{}, false
	}
	langCode, err := layerRPCWrapperRequired[string](initWrapper, "lang_code")
	if err != nil {
		return rpcRewrapInit{}, false
	}
	return rpcRewrapInit{
		layer: layer, apiID: apiID, deviceModel: deviceModel, system: system,
		appVersion: appVersion, systemLang: systemLang, langPack: langPack, langCode: langCode,
	}, true
}

func layerRPCWrapperRequired[T any](wrapper tlprofile.Wrapper, name string) (T, error) {
	var zero T
	value, present, ok, err := wrapper.Value(name)
	if err != nil || !ok || !present {
		return zero, fmt.Errorf("invalid RPC wrapper field %q", name)
	}
	typed, ok := value.(T)
	if !ok {
		return zero, fmt.Errorf("RPC wrapper field %q has type %T", name, value)
	}
	return typed, nil
}
