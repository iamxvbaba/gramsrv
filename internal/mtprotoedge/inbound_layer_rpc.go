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

	"telesrv/internal/store"
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
var errLayerRPCGZIPCapacity = errors.New("exact layer gzip admission capacity exhausted")
var errLayerRPCAdmissionCapability = errors.New("exact layer RPC admission capability unavailable")

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
	layerRPCAdmissionFlatBytesFactor   = 2
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
// invokeWithLayer proof, not merely the profile used by an arbitrary retained
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
	return saturatingLayerRPCAdmissionCharge(
		layerRPCAdmissionGraphSlack,
		layerRPCAdmissionWireCharge(wireBytes),
	)
}

func layerRPCAdmissionWireCharge(wireBytes int) int {
	if wireBytes < 0 {
		wireBytes = 0
	}
	maxInt := int(^uint(0) >> 1)
	if wireBytes > maxInt/layerRPCAdmissionWireFactor {
		return maxInt
	}
	return wireBytes * layerRPCAdmissionWireFactor
}

// layerRPCFlatBytesWireCharge keeps the generic factor on fixed TL wire and
// applies a tight copy factor only to a payload which the production Router has
// already proven to be one bounded flat bytes field. The temporary expanded
// buffer remains independently owned by inboundFrameBudget while generated
// admission materializes the request; 2x covers the retained bytes copy plus
// allocator rounding without treating every payload byte as a possible nested
// object/vector node. The per-request graph slack is owned by the base
// reservation and must not be repeated for each nested gzip expansion.
func layerRPCFlatBytesWireCharge(wireBytes, payloadBytes int) int {
	if wireBytes < 0 || payloadBytes < 0 || payloadBytes > wireBytes {
		return layerRPCAdmissionWireCharge(wireBytes)
	}
	fixedBytes := wireBytes - payloadBytes
	maxInt := int(^uint(0) >> 1)
	if fixedBytes > maxInt/layerRPCAdmissionWireFactor {
		return maxInt
	}
	charge := fixedBytes * layerRPCAdmissionWireFactor
	if payloadBytes > (maxInt-charge)/layerRPCAdmissionFlatBytesFactor {
		return maxInt
	}
	return charge + payloadBytes*layerRPCAdmissionFlatBytesFactor
}

func saturatingLayerRPCAdmissionCharge(left, right int) int {
	if left < 0 {
		left = 0
	}
	if right < 0 {
		right = 0
	}
	maxInt := int(^uint(0) >> 1)
	if right > maxInt-left {
		return maxInt
	}
	return left + right
}

func (s *Server) layerRPCExpandedWireCharge(wire []byte) int {
	if s != nil {
		if sizer, ok := s.layerRPC.(LayerRPCFlatBytesPayloadSizer); ok {
			if payloadBytes, flat := sizer.LayerRPCFlatBytesPayloadSize(wire); flat && payloadBytes >= 0 && payloadBytes <= len(wire) {
				return layerRPCFlatBytesWireCharge(len(wire), payloadBytes)
			}
		}
	}
	return layerRPCAdmissionWireCharge(len(wire))
}

// layerRPCGZIPExpansionBudget bridges transient process-wide expansion memory
// to the durable scheduler charge of one exact typed request. The original
// compressed wire retains the generic graph charge. Every successful expansion
// adds its own charge; only a handler-proven flat bytes terminal can use the
// tighter payload factor. An authoritative-profile re-decode reuses the largest
// already-held charge instead of double-counting sequential attempts.
type layerRPCGZIPExpansionBudget struct {
	server                *Server
	plan                  *inboundPlan
	reservation           *inboundRPCBatchReservation
	entry                 int
	baseCharge            int
	attemptExpandedCharge int
	chargedSize           int
}

func (b *layerRPCGZIPExpansionBudget) beginAttempt() {
	if b != nil {
		b.attemptExpandedCharge = 0
	}
}

func (b *layerRPCGZIPExpansionBudget) expand(wire []byte, admissionLimit int) ([]byte, func(), error) {
	noop := func() {}
	if b == nil || b.server == nil || b.plan == nil || b.reservation == nil {
		return nil, noop, errors.New("invalid exact layer gzip expansion budget")
	}
	frameRemaining := maxDispatchExpandedBytes - b.plan.gzipExpandedBytes
	limit := min(admissionLimit, frameRemaining)
	if limit <= 0 {
		return nil, noop, errors.Join(
			errLayerRPCGZIPCapacity,
			fmt.Errorf("cumulative gzip expansion reached %d bytes", maxDispatchExpandedBytes),
		)
	}
	data, release, err := b.server.decodeGZIPWithGlobalBudgetLimit(&bin.Buffer{Buf: wire}, limit)
	if err != nil {
		if work := gzipExpansionWork(err); work > 0 {
			b.plan.gzipExpandedBytes += work
		}
		if errors.Is(err, ErrInboundFrameBudgetExceeded) {
			return nil, release, errors.Join(errLayerRPCGZIPCapacity, err)
		}
		if limit < admissionLimit && errors.Is(err, errGZIPExpansionLimit) {
			return nil, release, errors.Join(errLayerRPCGZIPCapacity, err)
		}
		return nil, release, err
	}
	b.plan.gzipExpandedBytes += len(data)
	b.attemptExpandedCharge = saturatingLayerRPCAdmissionCharge(
		b.attemptExpandedCharge,
		b.server.layerRPCExpandedWireCharge(data),
	)
	targetCharge := saturatingLayerRPCAdmissionCharge(b.baseCharge, b.attemptExpandedCharge)
	if targetCharge > b.chargedSize {
		if err := b.reservation.growEntry(b.entry, targetCharge); err != nil {
			if errors.Is(err, ErrInboundRPCQueueFull) {
				return nil, release, errors.Join(errLayerRPCGZIPCapacity, err)
			}
			return nil, release, err
		}
		b.chargedSize = targetCharge
	}
	return data, release, nil
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
	decodeOptions := make([]tlprofile.AdmissionOptions, len(candidateItems))
	expansionBudgets := make([]*layerRPCGZIPExpansionBudget, len(candidateItems))
	materializationCapacity := false
	for reservationIndex, index := range candidateItems {
		item := &plan.items[index]
		itemState := admissionCursor.state
		existingProfile, existing := s.rpcResults.ExactAdmissionProfile(c.authKeyID, c.sessionID, item.msgID)
		if existing {
			itemState = LayerProfileSnapshot{Profile: existingProfile, Origin: LayerProfileExplicit}
		}
		expansionBudget := &layerRPCGZIPExpansionBudget{
			server: s, plan: plan, reservation: reservation,
			entry:       reservationIndex,
			baseCharge:  provisionalSpecs[reservationIndex].size,
			chargedSize: provisionalSpecs[reservationIndex].size,
		}
		expansionBudgets[reservationIndex] = expansionBudget
		options := tlprofile.AdmissionOptions{
			Limits:     inboundLayerDecodeLimits,
			ExpandGZIP: expansionBudget.expand,
		}
		decodeOptions[reservationIndex] = options
		admitted, method, err := s.decodeInboundLayerRPCWithOptions(itemState, item.body, options)
		if err != nil {
			if errors.Is(err, errLayerRPCGZIPCapacity) {
				materializationCapacity = true
				break
			}
			if errors.Is(err, ErrConnClosed) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return err
			}
			if errors.Is(err, errLayerRPCAdmissionCapability) {
				return err
			}
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
			if terminal, recognized := wrappedHelpTestTerminal(err); recognized {
				if terminal.WireSize != bin.Word || !validWrappedHelpTestChain(terminal) {
					s.log.Debug("Wrapped help.test terminal rejected",
						zap.Int("profile", int(terminal.Profile)),
						zap.Int("wire_size", terminal.WireSize),
						zap.Int("wrapper_count", terminal.WrapperCount()),
						zap.Int64("msg_id", item.msgID),
					)
					item.kind = inboundItemRPCAdmissionError
					item.method = "help.test"
					item.payload = &mt.RPCError{ErrorCode: 400, ErrorMessage: "INPUT_REQUEST_INVALID"}
					c.metrics.InboundRPCDropped(item.method, "layer_admission")
					continue
				}
				item.kind = inboundItemHelpTest
				item.method = "help.test"
				item.payload = helpTestRequest{}
				continue
			}
			if errors.Is(err, ErrLayerProfileConflict) {
				return err
			}
			rpcError := layerRPCAdmissionError(err)
			s.logLayerRPCAdmissionRejection(c, item, itemState, admissionCursor, method, rpcError, err)
			item.kind = inboundItemRPCAdmissionError
			item.method = method
			item.payload = rpcError
			c.metrics.InboundRPCDropped(method, "layer_admission")
			continue
		}
		item.admitted = admitted
		item.method = method
		if profile, hasEvidence := admitted.ProfileEvidence(); hasEvidence {
			if existing && profile != existingProfile {
				return fmt.Errorf("%w: retained msg_id %d used Layer %d but replay selected Layer %d", ErrLayerProfileConflict, item.msgID, existingProfile, profile)
			}
			evidence[index] = layerRPCProfileEvidence{profile: profile, present: true, fresh: item.profileEvidenceFresh()}
			if evidence[index].fresh {
				if err := admissionCursor.observe(profile, item.msgID); err != nil {
					return err
				}
			}
		}
	}
	if materializationCapacity {
		for _, index := range candidateItems {
			item := &plan.items[index]
			item.kind = inboundItemCapacityError
			item.admitted = tlprofile.Admission{}
			c.metrics.InboundRPCDropped(s.typeName(item.typeID), "materialization_capacity")
		}
		if err := reservation.retain(nil, nil); err != nil {
			return err
		}
		plan.rpcReservation = nil
		return nil
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
				claim, err := s.acquireAdmittedLayerRPC(
					c, item, &evidence[index], decodeOptions[reservationIndex], expansionBudgets[reservationIndex],
				)
				if errors.Is(err, errLayerRPCGZIPCapacity) {
					s.rpcRewrap.release(candidate)
					c.metrics.InboundRPCDropped(candidate.method, "materialization_capacity")
					flightCapacity = true
					item.kind = inboundItemCapacityError
					continue
				}
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
					// Transfer the materialization ticket to the plan before any
					// profile-restore step can fail; plan.close is the universal abort
					// path and sendReplayed releases it after outbound-budget handoff.
					item.payload = claim.encoded
					after, prepareErr := s.prepareAdmittedLayerRPCReplay(ctx, c, item.msgID, claim.admissionSeq, item.profileEvidenceFresh(), item.admitted)
					if prepareErr != nil {
						s.rpcRewrap.release(candidate)
						return prepareErr
					}
					s.rpcRewrap.commit(candidate)
					item.kind = inboundItemReplayRPC
					if claim.executionKnown && claim.executionOK {
						item.replayAfterSuccessfulDelivery = after
					}
				case rpcResultAcquireAcknowledged:
					// Explicit ACK is terminal proof for this request msg_id. Keep
					// exact admission metadata, retire the old rewrap candidate and
					// make the duplicate ACK-only without replay side effects.
					s.rpcRewrap.commit(candidate)
					item.kind = inboundItemDuplicate
					item.payload = nil
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

		claim, err := s.acquireAdmittedLayerRPC(
			c, item, &evidence[index], decodeOptions[reservationIndex], expansionBudgets[reservationIndex],
		)
		if errors.Is(err, errLayerRPCGZIPCapacity) {
			c.metrics.InboundRPCDropped(method, "materialization_capacity")
			flightCapacity = true
			item.kind = inboundItemCapacityError
			continue
		}
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
			item.payload = claim.encoded
			after, prepareErr := s.prepareAdmittedLayerRPCReplay(ctx, c, item.msgID, claim.admissionSeq, item.profileEvidenceFresh(), item.admitted)
			if prepareErr != nil {
				return prepareErr
			}
			item.kind = inboundItemReplayRPC
			if claim.executionKnown && claim.executionOK {
				item.replayAfterSuccessfulDelivery = after
			}
		case rpcResultAcquireAcknowledged:
			item.kind = inboundItemDuplicate
			item.payload = nil
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
	options tlprofile.AdmissionOptions,
	expansionBudget *layerRPCGZIPExpansionBudget,
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
	// Drop the losing typed graph before materializing its authoritative-profile
	// replacement. The grown reservation therefore needs to cover the larger
	// graph, not two simultaneous copies.
	item.admitted = tlprofile.Admission{}
	if expansionBudget != nil {
		expansionBudget.beginAttempt()
	}
	redecoded, method, decodeErr := s.decodeInboundLayerRPCWithOptions(
		LayerProfileSnapshot{Profile: winnerProfile, Origin: LayerProfileExplicit},
		item.body,
		options,
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
	ctx = s.withLayerRPCIdentityHint(ctx, c)
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

func (s *Server) withLayerRPCIdentityHint(ctx context.Context, c *Conn) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if s == nil || c == nil {
		return ctx
	}
	decorator, ok := s.layerRPC.(LayerRPCIdentityHintContext)
	if !ok {
		return ctx
	}
	businessAuthKeyID, businessResolved := c.BusinessAuthKeyID()
	userID, userResolved := c.UserIDResolved()
	decorated := decorator.WithLayerRPCIdentityHint(ctx, LayerRPCIdentityHint{
		RawAuthKeyID:                c.AuthKeyID(),
		SessionID:                   c.SessionID(),
		BusinessAuthKeyID:           businessAuthKeyID,
		BusinessAuthKeyResolved:     businessResolved,
		UserID:                      userID,
		UserIDResolved:              userResolved,
		RawAuthKeyExpiresAt:         c.AuthKeyExpiresAt(),
		RawAuthKeyExpiresAtResolved: true,
		SessionUpdatesReady:         s.conns != nil && s.conns.ReceivesUpdatesForAuthKey(c.AuthKeyID(), c.SessionID()),
	})
	if decorated == nil {
		return ctx
	}
	return decorated
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
			return tlprofile.Admission{}, method, err
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
	return s.decodeInboundLayerRPCWithLimits(state, body, inboundLayerDecodeLimits)
}

func (s *Server) decodeInboundLayerRPCWithLimits(state LayerProfileSnapshot, body []byte, limits tlprofile.Limits) (tlprofile.Admission, string, error) {
	return s.decodeInboundLayerRPCWithOptions(state, body, tlprofile.AdmissionOptions{Limits: limits})
}

func (s *Server) decodeInboundLayerRPCWithOptions(state LayerProfileSnapshot, body []byte, options tlprofile.AdmissionOptions) (tlprofile.Admission, string, error) {
	if s == nil || s.layerRPC == nil || len(body) < bin.Word {
		return tlprofile.Admission{}, "unknown", fmt.Errorf("invalid exact RPC admission input")
	}
	b := &bin.Buffer{Buf: body}
	var (
		request tlprofile.Admission
		err     error
	)
	if state.Origin != LayerProfileUnknown {
		if admitter, ok := s.layerRPC.(LayerRPCDefaultProfileOptionsAdmitter); ok {
			request, err = admitter.AdmitDefaultLayerWithOptions(state.Profile, b, options)
		} else if admitter, ok := s.layerRPC.(LayerRPCDefaultProfileAdmitter); ok {
			// Preserve the stable pre-options capability first: unlike strict
			// exact admission, default admission lets an explicit invokeWithLayer
			// correct inherited or restored profile evidence.
			request, err = admitter.AdmitDefaultLayer(state.Profile, b, options.Limits)
		} else if admitter, ok := s.layerRPC.(LayerRPCOptionsAdmitter); ok {
			request, err = admitter.AdmitLayerWithOptions(state.Profile, b, options)
		} else {
			request, err = s.layerRPC.AdmitLayer(state.Profile, b, options.Limits)
		}
	} else if admitter, ok := s.layerRPC.(LayerRPCOptionsAdmitter); ok {
		request, err = admitter.AdmitUnprofiledWithOptions(b, options)
	} else {
		request, err = s.layerRPC.AdmitUnprofiled(b, options.Limits)
	}
	if err != nil && options.ExpandGZIP != nil && errors.Is(err, tlprofile.ErrGZIPExpanderMissing) {
		// A handler compiled against the stable Limits-only boundary remains
		// valid for plain requests. Encountering gzip_packed without the optional
		// capability is instead a server wiring/programming error: returning the
		// generated error as INPUT_REQUEST_INVALID would silently blame a valid
		// client envelope and make recovery impossible.
		err = fmt.Errorf("%w: handler cannot use caller-owned bounded gzip expansion: %w", errLayerRPCAdmissionCapability, err)
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
	var unknownTerminal *tlprofile.UnknownTerminalError
	if errors.As(err, &unknownTerminal) && unknownTerminal.WireID != 0 {
		method = s.typeName(unknownTerminal.WireID)
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
		} else if terminal, recognized := wrappedHelpTestTerminal(err); recognized {
			method = "help.test"
			s.log.Debug("Generated wrapper admission exposed legacy terminal",
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
// overwriting its local wire epoch. Older retained duplicates remain decodable
// and request-bound, but cannot mutate session/profile state.
func (s *Server) commitLayerProfileEvidence(ctx context.Context, c *Conn, profile tlprofile.Profile, msgID int64) (bool, error) {
	if s == nil || c == nil || msgID <= 0 {
		return false, fmt.Errorf("invalid layer profile evidence target")
	}
	registry, ok := s.layerRPC.(LayerRPCDurableSessionProfileAdvancer)
	if !ok {
		return false, fmt.Errorf("durable exact session Layer advancer is required")
	}
	layer, authoritativeMsgID, publishShared, err := registry.AdvanceNegotiatedSessionLayerEvidence(
		ctx, c.authKeyID, c.sessionID, int(profile), msgID,
	)
	if err != nil {
		if errors.Is(err, store.ErrAuthKeySessionLayerConflict) {
			return false, fmt.Errorf("%w: %v", ErrLayerProfileConflict, err)
		}
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

type exactSessionProfileCapacityMarker interface {
	ExactSessionProfileCapacity()
}

func isExactSessionProfileCapacityError(err error) bool {
	var marker exactSessionProfileCapacityMarker
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

// logLayerRPCAdmissionRejection preserves one production-visible diagnostic for
// an otherwise generic RPC 400 without exposing the request body. The INFO path
// is one-shot per physical Conn; compatibility-traced unknown RPCs already have
// their own warning and therefore do not consume this diagnostic slot.
func (s *Server) logLayerRPCAdmissionRejection(
	c *Conn,
	item *inboundItem,
	state LayerProfileSnapshot,
	cursor layerRPCAdmissionCursor,
	method string,
	rpcError *mt.RPCError,
	admissionErr error,
) {
	if s == nil || s.log == nil || c == nil || item == nil || rpcError == nil {
		return
	}
	wireID := item.typeID
	if wireID == 0 {
		if id, err := (&bin.Buffer{Buf: item.body}).PeekID(); err == nil {
			wireID = id
		}
	}
	fields := []zap.Field{
		zap.String("method", method),
		zap.String("auth_key_id", c.authKeyHex),
		zap.Int64("session_id", c.sessionID),
		zap.Int64("msg_id", item.msgID),
		zap.Uint32("top_level_wire_id", wireID),
		zap.Int("wire_bytes", len(item.body)),
		zap.Int("selected_profile", int(state.Profile)),
		zap.String("profile_origin", layerProfileOriginLogName(state.Origin)),
		zap.Uint32("profile_epoch", state.Epoch),
		zap.Int("raw_layer_evidence", cursor.rawLayer),
		zap.Int64("layer_evidence_msg_id", cursor.evidenceMsgID),
		zap.Bool("explicit_layer_selector", layerRPCAdmissionHasExplicitSelector(item.body, admissionErr)),
		zap.String("inner_wire_id", innerLayerRPCWireIDName(item.body)),
		zap.Int("rpc_error_code", rpcError.ErrorCode),
		zap.String("rpc_error_message", rpcError.ErrorMessage),
		zap.Error(admissionErr),
	}
	if !errors.Is(admissionErr, tlprofile.ErrUnknownRPCMethod) && c.layerRPCAdmissionTraceLogged.CompareAndSwap(false, true) {
		s.log.Info("RPC exact admission rejected", fields...)
		return
	}
	s.log.Debug("RPC exact admission rejected", fields...)
}

// innerLayerRPCWireIDName peeks past the invokeWithLayer envelope to report which
// method the client actually asked for, as a hex wire id.
//
// The rejection log records top_level_wire_id, which for a layer-wrapped call is
// always the wrapper itself, so it cannot distinguish "the client wants a method
// we serve, at a layer we do not" from "the client wants a method nobody has
// written". Both look identical in the log, and they need different fixes.
//
// The raw id is logged rather than a resolved name because tlprofile exposes no
// wire-id-to-name lookup; a name is derived from this offline against the
// profile's generated tables.
//
// Best-effort by design. A body that is not an invokeWithLayer envelope, or whose
// layer field does not parse, yields "-" rather than a guess.
func innerLayerRPCWireIDName(body []byte) string {
	envelope := innerLayerRPCEnvelope(body)
	if envelope == nil {
		return "-"
	}
	inner, err := (&bin.Buffer{Buf: envelope}).PeekID()
	if err != nil {
		return "-"
	}
	return fmt.Sprintf("#%08x", inner)
}

// innerLayerRPCEnvelope returns the bytes of the query wrapped by
// invokeWithLayer, or nil when body is not that envelope. The layer field is
// consumed but its value is not interpreted: the point is to reach the query, not
// to second-guess the admission decision that already rejected it.
func innerLayerRPCEnvelope(body []byte) []byte {
	buf := &bin.Buffer{Buf: body}
	id, err := buf.PeekID()
	if err != nil || id != tg.InvokeWithLayerRequestTypeID {
		return nil
	}
	if err = buf.ConsumeID(id); err != nil {
		return nil
	}
	// invokeWithLayer carries the requested layer as a bare int before the query.
	// It is a 4-byte int, not a long: reading 8 bytes here consumed the first four
	// bytes of the query too, and every wrapped call reported as unreadable.
	if _, err = buf.Int(); err != nil {
		return nil
	}
	return buf.Buf
}

func layerProfileOriginLogName(origin LayerProfileOrigin) string {
	switch origin {
	case LayerProfileInherited:
		return "inherited"
	case LayerProfileExplicit:
		return "explicit"
	default:
		return "unknown"
	}
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
