package mtprotoedge

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"time"

	"go.uber.org/zap"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/mt"
	"github.com/gotd/td/proto"
)

type inboundItemKind uint8

const (
	inboundItemDuplicate inboundItemKind = iota + 1
	inboundItemServiceDuplicate
	inboundItemPing
	inboundItemPingDelay
	inboundItemFutureSalts
	inboundItemMsgsAck
	inboundItemStateReq
	inboundItemResendReq
	inboundItemStateInfo
	inboundItemAllInfo
	inboundItemDestroySession
	inboundItemHTTPWait
	inboundItemDropAnswer
	inboundItemDestroyAuthKey
	inboundItemRPC
	inboundItemCapacityError
	inboundItemPendingRPC
	// inboundItemReplayRPC is a request first observed by this physical Conn whose
	// terminal result already exists in the cross-connection cache. It is distinct
	// from inboundItemDuplicate: a duplicate already present in this Conn's seen
	// table must only be ACKed. The original owner/result is already using the same
	// reliable TCP stream, so replaying it once per retransmit wave amplifies a
	// client retry burst and can starve newer request IDs behind orphan results.
	inboundItemReplayRPC
)

type inboundItem struct {
	kind    inboundItemKind
	msgID   int64
	seqNo   int32
	typeID  uint32
	content bool
	body    []byte
	payload any
}

type stagedClientMessage struct {
	msgID   int64
	seqNo   int32
	content bool
	service bool
}

type inboundPlan struct {
	items      []inboundItem
	staged     []stagedClientMessage
	ackIDs     []int64
	logicalMin int64
	releases   []func()

	rpcReservation *inboundRPCBatchReservation
	rpcTasks       []inboundRPC
	rpcOwners      []*rpcResultOwnerLease
}

func (p *inboundPlan) close() {
	if p == nil {
		return
	}
	if p.rpcReservation != nil {
		p.rpcReservation.abort()
		p.rpcReservation = nil
	}
	for _, owner := range p.rpcOwners {
		owner.Abort()
	}
	p.rpcOwners = nil
	for i := len(p.releases) - 1; i >= 0; i-- {
		p.releases[i]()
	}
	p.releases = nil
}

func (p *inboundPlan) commitRPCBatch() error {
	if p == nil || p.rpcReservation == nil {
		return nil
	}
	// handleEncrypted calls this only after ownership/session-control barriers,
	// connState commit and every synchronous service action have completed. The
	// batch is runnable immediately; using the old deferred scheduler token here
	// was not a real barrier on a busy Conn because an existing ready token could
	// dequeue newly appended tasks before activateRPCBatch ran.
	err := p.rpcReservation.commit(p.rpcTasks)
	if err != nil {
		return err
	}
	p.rpcReservation = nil
	p.rpcTasks = nil
	// Every owner is now attached to exactly one queued task. Its task.release
	// path aborts if no terminal rpc_result was published.
	p.rpcOwners = nil
	return nil
}

func (p *inboundPlan) includeLogicalID(msgID int64) {
	if p.logicalMin == 0 || msgID < p.logicalMin {
		p.logicalMin = msgID
	}
}

func (p *inboundPlan) commitState(cs *connState) {
	for _, m := range p.staged {
		cs.trackInbound(m.msgID, m.seqNo, m.content, m.service, msgStateReceived)
	}
}

type connStateOverlay struct {
	base            *connState
	staged          []stagedClientMessage
	maxContentMsgID int64
	maxContentSeqNo int32
}

func newConnStateOverlay(base *connState) connStateOverlay {
	return connStateOverlay{
		base:            base,
		maxContentMsgID: base.maxContentMsgID,
		maxContentSeqNo: base.maxContentSeqNo,
	}
}

func (o *connStateOverlay) seenRecord(msgID int64) (clientMsgRecord, bool) {
	for i := len(o.staged) - 1; i >= 0; i-- {
		m := o.staged[i]
		if m.msgID == msgID {
			return clientMsgRecord{state: msgStateReceived, seqNo: m.seqNo, content: m.content, service: m.service}, true
		}
	}
	return o.base.seenRecord(msgID)
}

func (o *connStateOverlay) validateSeq(msgID int64, seqNo int32, content bool) int {
	if !content {
		return 0
	}
	if msgID > o.maxContentMsgID && seqNo > o.maxContentSeqNo {
		return 0
	}
	for seenMsgID, record := range o.base.seen {
		if !record.content {
			continue
		}
		if seenMsgID < msgID && record.seqNo >= seqNo {
			return badMsgSeqTooLow
		}
		if seenMsgID > msgID && record.seqNo <= seqNo {
			return badMsgSeqTooHigh
		}
	}
	for _, record := range o.staged {
		if !record.content {
			continue
		}
		if record.msgID < msgID && record.seqNo >= seqNo {
			return badMsgSeqTooLow
		}
		if record.msgID > msgID && record.seqNo <= seqNo {
			return badMsgSeqTooHigh
		}
	}
	return 0
}

func (o *connStateOverlay) stage(msgID int64, seqNo int32, content, service bool) {
	o.staged = append(o.staged, stagedClientMessage{msgID: msgID, seqNo: seqNo, content: content, service: service})
	if content {
		if msgID > o.maxContentMsgID {
			o.maxContentMsgID = msgID
		}
		if seqNo > o.maxContentSeqNo {
			o.maxContentSeqNo = seqNo
		}
	}
}

type inboundScope struct {
	insideContainer bool
	mustBeDuplicate bool
}

type inboundPreflightBudget struct {
	depth          int
	containerDepth int
	expanded       int
	now            time.Time
}

func (s *Server) preflightInbound(cs *connState, msgID int64, seqNo int32, body []byte) (*inboundPlan, error) {
	plan := &inboundPlan{logicalMin: 0}
	overlay := newConnStateOverlay(cs)
	// All envelope checks in one transport frame use the same clock sample. Apart
	// from making boundary behavior deterministic, this lets walkInbound reject
	// an invalid outer msg_id before spending CPU or memory on gzip expansion.
	budget := &inboundPreflightBudget{now: s.clock.Now()}
	if err := s.walkInbound(plan, &overlay, msgID, seqNo, body, inboundScope{}, budget); err != nil {
		plan.close()
		return nil, err
	}
	plan.staged = overlay.staged
	if plan.logicalMin == 0 {
		plan.close()
		return nil, fmt.Errorf("inbound plan accepted no logical message")
	}
	for _, item := range plan.items {
		if item.kind == inboundItemDestroyAuthKey && len(plan.items) != 1 {
			plan.close()
			return nil, &dispatchBadMsgError{msgID: msgID, seqNo: seqNo, code: badMsgContainer}
		}
	}
	return plan, nil
}

func (s *Server) walkInbound(
	plan *inboundPlan,
	overlay *connStateOverlay,
	msgID int64,
	seqNo int32,
	body []byte,
	scope inboundScope,
	budget *inboundPreflightBudget,
) error {
	if budget.depth > maxDispatchDepth {
		return fmt.Errorf("mtproto wrapper depth %d exceeds %d", budget.depth, maxDispatchDepth)
	}
	b := &bin.Buffer{Buf: body}
	typeID, err := b.PeekID()
	if err != nil {
		return fmt.Errorf("peek type id: %w", err)
	}
	if code := validateInboundMessageID(budget.now, msgID, scope.insideContainer); code != 0 {
		if scope.insideContainer {
			code = badMsgContainer
		}
		return &dispatchBadMsgError{msgID: msgID, seqNo: seqNo, code: code}
	}

	// A repeated outer container still has to be decoded far enough to enumerate
	// its inner request ids, but none of those already-accepted inner bodies needs
	// decoding again. In particular, never reinflate a gzip body merely to discover
	// the content bit we already retained with its msg_id. If an outer duplicate
	// introduces an unseen inner id, reject the container before touching its body.
	// A seen top-level content gzip is also safe to short-circuit: a container is
	// non-content, so it cannot be hidden behind that retained record. Ambiguous
	// non-content top-level gzip still expands to distinguish a container wrapper.
	if record, seen := overlay.seenRecord(msgID); scope.mustBeDuplicate ||
		(typeID == proto.GZIPTypeID && seen && (scope.insideContainer || record.content)) {
		if !seen || record.seqNo != seqNo {
			return &dispatchBadMsgError{msgID: msgID, seqNo: seqNo, code: badMsgContainer}
		}
		return appendInboundDuplicate(plan, msgID, seqNo, typeID, record)
	}
	if typeID == proto.GZIPTypeID {
		data, release, err := s.decodeGZIPWithGlobalBudget(b)
		if err != nil {
			return fmt.Errorf("decode gzip: %w", err)
		}
		plan.releases = append(plan.releases, release)
		budget.expanded += len(data)
		if budget.expanded > maxDispatchExpandedBytes {
			return fmt.Errorf("cumulative gzip expansion %d exceeds %d", budget.expanded, maxDispatchExpandedBytes)
		}
		budget.depth++
		err = s.walkInbound(plan, overlay, msgID, seqNo, data, scope, budget)
		budget.depth--
		return err
	}

	if typeID == proto.MessageContainerTypeID {
		if scope.insideContainer || budget.containerDepth != 0 {
			return &dispatchBadMsgError{msgID: msgID, seqNo: seqNo, code: badMsgContainer}
		}
		if code := validateClientEnvelope(budget.now, msgID, seqNo, typeID); code != 0 {
			return &dispatchBadMsgError{msgID: msgID, seqNo: seqNo, code: code}
		}
		outerRecord, outerSeen := overlay.seenRecord(msgID)
		if outerSeen {
			if outerRecord.content || outerRecord.seqNo != seqNo {
				return &dispatchBadMsgError{msgID: msgID, seqNo: seqNo, code: badMsgContainer}
			}
		} else {
			if code := overlay.validateSeq(msgID, seqNo, false); code != 0 {
				return &dispatchBadMsgError{msgID: msgID, seqNo: seqNo, code: code}
			}
			overlay.stage(msgID, seqNo, false, false)
		}

		count, err := containerMessageCount(b)
		if err != nil {
			return fmt.Errorf("decode container count: %w", err)
		}
		if count > maxContainerMessages {
			return &dispatchBadMsgError{msgID: msgID, seqNo: seqNo, code: badMsgContainer}
		}
		container, release, err := s.decodeMessageContainerViews(b, count)
		if err != nil {
			return fmt.Errorf("decode container: %w", err)
		}
		plan.releases = append(plan.releases, release)
		plan.items = growInboundItems(plan.items, count)
		plan.ackIDs = growInt64s(plan.ackIDs, count)
		overlay.staged = growStagedMessages(overlay.staged, count+1)
		if len(container.Messages) == 0 {
			plan.includeLogicalID(msgID)
			return nil
		}

		budget.depth++
		budget.containerDepth++
		for _, m := range container.Messages {
			if m.ID >= msgID || int32(m.SeqNo) > seqNo {
				return &dispatchBadMsgError{msgID: msgID, seqNo: seqNo, code: badMsgContainer}
			}
			if err := s.walkInbound(plan, overlay, m.ID, int32(m.SeqNo), m.Body, inboundScope{
				insideContainer: true,
				mustBeDuplicate: outerSeen,
			}, budget); err != nil {
				return err
			}
		}
		budget.containerDepth--
		budget.depth--
		return nil
	}

	if scope.insideContainer {
		if code := validateClientContainerEnvelope(msgID, seqNo, typeID); code != 0 {
			return &dispatchBadMsgError{msgID: msgID, seqNo: seqNo, code: badMsgContainer}
		}
	} else if code := validateClientEnvelope(budget.now, msgID, seqNo, typeID); code != 0 {
		return &dispatchBadMsgError{msgID: msgID, seqNo: seqNo, code: code}
	}

	content := clientMessageNeedsAck(typeID)
	if record, seen := overlay.seenRecord(msgID); seen {
		if record.seqNo != seqNo || record.content != content {
			return &dispatchBadMsgError{msgID: msgID, seqNo: seqNo, code: badMsgContainer}
		}
		return appendInboundDuplicate(plan, msgID, seqNo, typeID, record)
	}
	if scope.mustBeDuplicate {
		return &dispatchBadMsgError{msgID: msgID, seqNo: seqNo, code: badMsgContainer}
	}
	if code := overlay.validateSeq(msgID, seqNo, content); code != 0 {
		return &dispatchBadMsgError{msgID: msgID, seqNo: seqNo, code: code}
	}
	overlay.stage(msgID, seqNo, content, inboundTypeIsService(typeID))
	plan.includeLogicalID(msgID)

	item, err := preflightInboundItem(msgID, seqNo, typeID, content, body)
	if err != nil {
		return err
	}
	plan.items = append(plan.items, item)
	if content {
		plan.ackIDs = append(plan.ackIDs, msgID)
	}
	return nil
}

func validateInboundMessageID(now time.Time, msgID int64, insideContainer bool) int {
	if msgID == 0 || proto.MessageID(msgID).Type() != proto.MessageFromClient {
		return badMsgIDInvalidBits
	}
	// A container's outer envelope supplies the wall-clock admission boundary for
	// its inner messages. Inner ids still need the client low bits checked before
	// wrapper expansion, but intentionally keep the established no-time-check rule.
	if insideContainer {
		return 0
	}
	msgTime := proto.MessageID(msgID).Time()
	if msgTime.Before(now.Add(-300 * time.Second)) {
		return badMsgIDTooLow
	}
	if msgTime.After(now.Add(30 * time.Second)) {
		return badMsgIDTooHigh
	}
	return 0
}

func appendInboundDuplicate(plan *inboundPlan, msgID int64, seqNo int32, typeID uint32, record clientMsgRecord) error {
	plan.includeLogicalID(msgID)
	kind := inboundItemDuplicate
	if record.service {
		kind = inboundItemServiceDuplicate
	}
	plan.items = append(plan.items, inboundItem{
		kind: kind, msgID: msgID, seqNo: seqNo, typeID: typeID, content: record.content,
	})
	if record.content {
		plan.ackIDs = append(plan.ackIDs, msgID)
	}
	return nil
}

func inboundTypeIsService(typeID uint32) bool {
	switch typeID {
	case mt.PingRequestTypeID,
		mt.PingDelayDisconnectRequestTypeID,
		mt.GetFutureSaltsRequestTypeID,
		mt.MsgsAckTypeID,
		mt.MsgsStateReqTypeID,
		mt.MsgResendReqTypeID,
		mt.MsgsStateInfoTypeID,
		mt.MsgsAllInfoTypeID,
		mt.DestroySessionRequestTypeID,
		mt.HTTPWaitRequestTypeID,
		mt.RPCDropAnswerRequestTypeID,
		destroyAuthKeyRequestTypeID:
		return true
	default:
		return false
	}
}

func growInboundItems(items []inboundItem, extra int) []inboundItem {
	if extra <= cap(items)-len(items) {
		return items
	}
	grown := make([]inboundItem, len(items), len(items)+extra)
	copy(grown, items)
	return grown
}

func growStagedMessages(items []stagedClientMessage, extra int) []stagedClientMessage {
	if extra <= cap(items)-len(items) {
		return items
	}
	grown := make([]stagedClientMessage, len(items), len(items)+extra)
	copy(grown, items)
	return grown
}

func growInt64s(items []int64, extra int) []int64 {
	if extra <= cap(items)-len(items) {
		return items
	}
	grown := make([]int64, len(items), len(items)+extra)
	copy(grown, items)
	return grown
}

type stateInfoPayload struct {
	reqMsgID int64
	info     []byte
}

type allInfoPayload struct {
	count int
	info  []byte
}

// int64VectorView is a bounded, immutable view of a TL Vector<long>. Keeping the
// encoded bytes avoids one retained []int64 allocation per service message while
// a whole container is in preflight. Execution materializes the small bounded
// slice only for the existing Conn APIs that require []int64, after all wrappers
// and every sibling message have already passed structural validation.
type int64VectorView struct {
	raw   []byte
	count int
}

func decodeInt64VectorView(b *bin.Buffer, expectedTypeID uint32, max int) (int64VectorView, error) {
	if b == nil || len(b.Buf) < 12 {
		return int64VectorView{}, io.ErrUnexpectedEOF
	}
	if got := binary.LittleEndian.Uint32(b.Buf[:4]); got != expectedTypeID {
		return int64VectorView{}, fmt.Errorf("unexpected constructor %#x", got)
	}
	if got := binary.LittleEndian.Uint32(b.Buf[4:8]); got != bin.TypeVector {
		return int64VectorView{}, fmt.Errorf("unexpected vector constructor %#x", got)
	}
	count := int(int32(binary.LittleEndian.Uint32(b.Buf[8:12])))
	if count < 0 {
		return int64VectorView{}, fmt.Errorf("negative vector count %d", count)
	}
	if count > max {
		return int64VectorView{}, fmt.Errorf("vector count %d exceeds %d", count, max)
	}
	if count > (len(b.Buf)-12)/8 {
		return int64VectorView{}, io.ErrUnexpectedEOF
	}
	end := 12 + count*8
	if end != len(b.Buf) {
		return int64VectorView{}, fmt.Errorf("long vector has %d trailing bytes", len(b.Buf)-end)
	}
	return int64VectorView{raw: b.Buf[12:end:end], count: count}, nil
}

func (v int64VectorView) materialize() []int64 {
	if v.count == 0 {
		return nil
	}
	ids := make([]int64, v.count)
	for i := range ids {
		offset := i * 8
		ids[i] = int64(binary.LittleEndian.Uint64(v.raw[offset : offset+8]))
	}
	return ids
}

func decodeInboundServiceExact(b *bin.Buffer, decoder bin.Decoder) error {
	if err := decoder.Decode(b); err != nil {
		return err
	}
	if b.Len() != 0 {
		return fmt.Errorf("service message has %d trailing bytes", b.Len())
	}
	return nil
}

func preflightInboundItem(msgID int64, seqNo int32, typeID uint32, content bool, body []byte) (inboundItem, error) {
	item := inboundItem{msgID: msgID, seqNo: seqNo, typeID: typeID, content: content, body: body}
	b := &bin.Buffer{Buf: body}
	switch typeID {
	case mt.PingRequestTypeID:
		var value mt.PingRequest
		if err := decodeInboundServiceExact(b, &value); err != nil {
			return item, fmt.Errorf("decode ping: %w", err)
		}
		item.kind, item.payload = inboundItemPing, value
	case mt.PingDelayDisconnectRequestTypeID:
		var value mt.PingDelayDisconnectRequest
		if err := decodeInboundServiceExact(b, &value); err != nil {
			return item, fmt.Errorf("decode ping_delay_disconnect: %w", err)
		}
		item.kind, item.payload = inboundItemPingDelay, value
	case mt.GetFutureSaltsRequestTypeID:
		var value mt.GetFutureSaltsRequest
		if err := decodeInboundServiceExact(b, &value); err != nil {
			return item, fmt.Errorf("decode get_future_salts: %w", err)
		}
		item.kind, item.payload = inboundItemFutureSalts, value
	case mt.MsgsAckTypeID:
		value, err := decodeInt64VectorView(b, mt.MsgsAckTypeID, maxServiceMessageIDs)
		if err != nil {
			return item, fmt.Errorf("decode msgs_ack: %w", err)
		}
		item.kind, item.payload = inboundItemMsgsAck, value
	case mt.MsgsStateReqTypeID:
		value, err := decodeInt64VectorView(b, mt.MsgsStateReqTypeID, maxServiceMessageIDs)
		if err != nil {
			return item, fmt.Errorf("decode msgs_state_req: %w", err)
		}
		item.kind, item.payload = inboundItemStateReq, value
	case mt.MsgResendReqTypeID:
		value, err := decodeInt64VectorView(b, mt.MsgResendReqTypeID, maxServiceMessageIDs)
		if err != nil {
			return item, fmt.Errorf("decode msg_resend_req: %w", err)
		}
		item.kind, item.payload = inboundItemResendReq, value
	case mt.MsgsStateInfoTypeID:
		reqMsgID, info, err := msgsStateInfoView(b)
		if err != nil {
			return item, fmt.Errorf("decode msgs_state_info: %w", err)
		}
		item.kind, item.payload = inboundItemStateInfo, stateInfoPayload{reqMsgID: reqMsgID, info: info}
	case mt.MsgsAllInfoTypeID:
		count, info, err := msgsAllInfoView(b)
		if err != nil {
			return item, fmt.Errorf("decode msgs_all_info: %w", err)
		}
		if len(info) != count {
			return item, fmt.Errorf("decode msgs_all_info: info length %d does not match msg_ids %d", len(info), count)
		}
		item.kind, item.payload = inboundItemAllInfo, allInfoPayload{count: count, info: info}
	case mt.DestroySessionRequestTypeID:
		var value mt.DestroySessionRequest
		if err := decodeInboundServiceExact(b, &value); err != nil {
			return item, fmt.Errorf("decode destroy_session: %w", err)
		}
		item.kind, item.payload = inboundItemDestroySession, value
	case mt.HTTPWaitRequestTypeID:
		var value mt.HTTPWaitRequest
		if err := decodeInboundServiceExact(b, &value); err != nil {
			return item, fmt.Errorf("decode http_wait: %w", err)
		}
		item.kind, item.payload = inboundItemHTTPWait, value
	case mt.RPCDropAnswerRequestTypeID:
		var value mt.RPCDropAnswerRequest
		if err := decodeInboundServiceExact(b, &value); err != nil {
			return item, fmt.Errorf("decode rpc_drop_answer: %w", err)
		}
		item.kind, item.payload = inboundItemDropAnswer, value
	case destroyAuthKeyRequestTypeID:
		var value destroyAuthKeyRequest
		if err := decodeInboundServiceExact(b, &value); err != nil {
			return item, err
		}
		item.kind, item.payload = inboundItemDestroyAuthKey, value
	default:
		item.kind = inboundItemRPC
	}
	return item, nil
}

// prepareInboundRPCBatch performs the whole container's count/byte admission
// before copying or scheduling any API RPC. Capacity exhaustion is converted
// into one consistent terminal FLOOD_WAIT result per uncached RPC; no business
// handler from the batch is allowed to start in that case.
func (s *Server) prepareInboundRPCBatch(ctx context.Context, c *Conn, plan *inboundPlan) error {
	// Keep service-only frames (ping/ack/http_wait) allocation-free here. These
	// collections are needed only after the first real API RPC acquires ownership.
	var indices []int
	var specs []inboundRPCSpec
	var ownersInPlan map[int64]*rpcResultOwnerLease
	flightCapacity := false
	for i := range plan.items {
		item := &plan.items[i]
		if item.kind != inboundItemRPC && item.kind != inboundItemDuplicate {
			continue
		}
		localDuplicate := item.kind == inboundItemDuplicate
		if localDuplicate {
			// connState already proves this msg_id was admitted on this physical
			// generation. Its original owner either still holds the flight or has
			// published a result after physical write; do not consume a global flight
			// slot merely to ACK the duplicate.
			continue
		}
		method := s.typeName(item.typeID)
		claim, err := s.rpcResults.Acquire(c.authKeyID, c.sessionID, item.msgID)
		if errors.Is(err, ErrRPCResultFlightCapacity) {
			if item.kind == inboundItemRPC {
				c.metrics.InboundRPCDropped(method, "flight_capacity")
				flightCapacity = true
				item.kind = inboundItemCapacityError
			}
			continue
		}
		if err != nil {
			return err
		}
		switch claim.state {
		case rpcResultAcquireCompleted:
			s.log.Info("RPC duplicate replay from session cache",
				zap.String("method", method),
				zap.Int64("msg_id", item.msgID),
				zap.String("auth_key_id", c.authKeyHex),
				zap.Int64("session_id", c.sessionID),
			)
			item.kind = inboundItemReplayRPC
			item.payload = claim.encoded
		case rpcResultAcquirePending:
			// A malformed/replayed container may repeat the same msg_id after this
			// very plan installed its owner. More generally, any request already in
			// this Conn's seen table shares the owner's reliable response path. Only
			// a fresh physical replacement may wait for and replay the old result.
			if ownersInPlan[item.msgID] != nil {
				item.kind = inboundItemDuplicate
				item.payload = nil
			} else {
				item.kind = inboundItemPendingRPC
				item.payload = claim.waiter
			}
		case rpcResultAcquireOwner:
			if item.kind == inboundItemDuplicate {
				// connState says this request was already accepted, so absence from
				// both completed and in-flight tables is not authority to execute it.
				claim.owner.Abort()
				continue
			}
			if ownersInPlan == nil {
				ownersInPlan = make(map[int64]*rpcResultOwnerLease)
				indices = make([]int, 0, len(plan.items))
				specs = make([]inboundRPCSpec, 0, len(plan.items))
			}
			ownersInPlan[item.msgID] = claim.owner
			plan.rpcOwners = append(plan.rpcOwners, claim.owner)
			item.payload = claim.owner
			indices = append(indices, i)
			specs = append(specs, inboundRPCSpec{method: method, size: len(item.body)})
		default:
			return ErrRPCResultFlightInvalid
		}
	}
	if flightCapacity {
		// One container is one API admission unit. If the cross-connection
		// exactly-once table cannot claim every fresh request, none of this
		// batch may reach a business handler.
		for _, index := range indices {
			plan.items[index].kind = inboundItemCapacityError
		}
		return nil
	}
	if len(specs) == 0 {
		return nil
	}

	reservation, err := c.reserveInboundRPCBatch(ctx, specs)
	if err != nil {
		if errors.Is(err, ErrInboundRPCQueueFull) {
			for _, index := range indices {
				plan.items[index].kind = inboundItemCapacityError
			}
			return nil
		}
		return err
	}
	plan.rpcReservation = reservation
	plan.rpcTasks = make([]inboundRPC, len(indices))
	for i, index := range indices {
		item := &plan.items[index]
		body := append([]byte(nil), item.body...)
		owner, _ := item.payload.(*rpcResultOwnerLease)
		plan.rpcTasks[i] = s.newInboundRPCTask(c, item.msgID, specs[i].method, body, owner)
	}
	return nil
}

func (s *Server) executeInboundPlan(ctx context.Context, cs *connState, c *Conn, plan *inboundPlan) error {
	for _, item := range plan.items {
		switch item.kind {
		case inboundItemDuplicate:
			// The preflight plan already stages a content ACK for a locally seen
			// duplicate. Do not wait for or replay its result on the same reliable
			// stream; the original owner is solely responsible for that response.
			continue
		case inboundItemServiceDuplicate:
			// Classify from the originally committed connState record, never from the
			// retransmitted body. This prevents same-id payload replacement. Only an
			// already cached answer is eligible for resend (rpc_drop_answer today);
			// other best-effort service traffic uses a later fresh request.
			if err := s.replayRPCResultByRequest(ctx, c, item.msgID); err != nil {
				return err
			}
		case inboundItemReplayRPC:
			if encoded, _ := item.payload.(*encodedOutboundMessage); encoded != nil {
				if err := s.sendCachedRPCResult(ctx, c, encoded); err != nil {
					return err
				}
			} else if err := s.replayRPCResultByRequest(ctx, c, item.msgID); err != nil {
				return err
			}
		case inboundItemPendingRPC:
			// Wait only after this plan's fresh RPC batch has become runnable; see
			// executePendingRPCReplays. Blocking here could otherwise deadlock on
			// an owner appended by the same container.
			continue
		case inboundItemPing:
			if err := s.sendPong(ctx, c, item.msgID, item.payload.(mt.PingRequest).PingID); err != nil {
				return err
			}
		case inboundItemPingDelay:
			if err := s.sendPong(ctx, c, item.msgID, item.payload.(mt.PingDelayDisconnectRequest).PingID); err != nil {
				return err
			}
		case inboundItemFutureSalts:
			if err := s.sendFutureSalts(ctx, c, item.msgID, item.payload.(mt.GetFutureSaltsRequest).Num); err != nil {
				return err
			}
		case inboundItemMsgsAck:
			ids := item.payload.(int64VectorView).materialize()
			c.AckServerMessages(ids)
			s.log.Debug("Received msgs_ack", zap.Int64s("msg_ids", ids))
		case inboundItemStateReq:
			ids := item.payload.(int64VectorView).materialize()
			outgoing, err := c.OutgoingStateInfo(ctx, ids)
			if err != nil {
				return err
			}
			if err := s.sendMsgsStateInfo(ctx, c, item.msgID, mergeStateInfo(outgoing, cs.stateInfo(ids))); err != nil {
				return err
			}
		case inboundItemResendReq:
			ids := item.payload.(int64VectorView).materialize()
			outgoing, err := c.ResendMessages(ctx, ids)
			if err != nil {
				return err
			}
			if err := s.sendMsgsStateInfo(ctx, c, item.msgID, mergeStateInfo(outgoing, cs.stateInfo(ids))); err != nil {
				return err
			}
		case inboundItemStateInfo:
			value := item.payload.(stateInfoPayload)
			s.log.Debug("Received msgs_state_info", zap.Int64("req_msg_id", value.reqMsgID), zap.Int("len", len(value.info)))
		case inboundItemAllInfo:
			value := item.payload.(allInfoPayload)
			s.log.Debug("Received msgs_all_info", zap.Int("msg_ids", value.count), zap.Int("len", len(value.info)))
		case inboundItemDestroySession:
			if err := s.sendDestroySession(ctx, c, item.payload.(mt.DestroySessionRequest).SessionID); err != nil {
				return err
			}
		case inboundItemHTTPWait:
			value := item.payload.(mt.HTTPWaitRequest)
			s.log.Debug("Received http_wait", zap.Int("max_delay", value.MaxDelay), zap.Int("wait_after", value.WaitAfter), zap.Int("max_wait", value.MaxWait))
		case inboundItemDropAnswer:
			value := item.payload.(mt.RPCDropAnswerRequest)
			s.log.Debug("Received rpc_drop_answer", zap.Int64("req_msg_id", value.ReqMsgID))
			if err := s.sendResult(ctx, c, item.msgID, &mt.RPCAnswerUnknown{}); err != nil {
				return err
			}
		case inboundItemDestroyAuthKey:
			s.log.Debug("Received destroy_auth_key", zap.String("auth_key_id", c.authKeyHex))
			if err := s.authKeys.Delete(ctx, c.authKeyID); err != nil {
				s.log.Warn("Delete auth key failed", zap.String("auth_key_id", c.authKeyHex), zap.Error(err))
				return c.SendRequiredControl(ctx, proto.MessageServerResponse, &destroyAuthKeyFail{})
			}
			// Fence every other active/claiming generation before acknowledging the
			// deletion. The exact requester remains writable only long enough to put the
			// required destroy_auth_key_ok frame on the wire.
			s.conns.CloseSessionsForRawAuthKeyExceptConn(c.authKeyID, c)
			if err := c.SendRequiredControl(ctx, proto.MessageServerResponse, &destroyAuthKeyOk{}); err != nil {
				return err
			}
			c.beginTerminalShutdown()
			c.closeTransport()
			return nil
		case inboundItemRPC:
			// prepareInboundRPCBatch owns every fresh RPC before synchronous service
			// execution begins; commitRPCBatch publishes them after all protocol barriers.
			continue
		case inboundItemCapacityError:
			if err := s.sendResult(ctx, c, item.msgID, &mt.RPCError{
				ErrorCode:    420,
				ErrorMessage: "FLOOD_WAIT_1",
			}); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unknown inbound item kind %d", item.kind)
		}
	}
	return nil
}

// executePendingRPCReplays joins owners already running on another physical
// connection for the same MTProto session. It never dispatches business code:
// owner Put publishes the immutable result to every waiter; owner Abort leaves
// the client free to retry after the old execution has definitely stopped.
func (s *Server) executePendingRPCReplays(ctx context.Context, c *Conn, plan *inboundPlan) error {
	for _, item := range plan.items {
		if item.kind != inboundItemPendingRPC {
			continue
		}
		waiter, _ := item.payload.(*rpcResultWaiter)
		if waiter == nil {
			continue
		}
		encoded, ok, err := waiter.Wait(ctx)
		if err != nil {
			return err
		}
		if !ok || encoded == nil {
			// The old owner stopped without publishing a result (normally because
			// replacement cancellation reached the handler before it committed).
			// A fresh-connection item still owns the decoded request body, so reacquire
			// only after the prior flight is definitively gone. This is sequential
			// retry, never concurrent business execution. Same-Conn seen duplicates
			// have no body here and rely on the client's ordinary resend path.
			if len(item.body) > 0 {
				if err := s.enqueueRPC(ctx, c, item.msgID, item.typeID, &bin.Buffer{Buf: item.body}); err != nil {
					return err
				}
			}
			continue
		}
		if err := s.sendCachedRPCResult(ctx, c, encoded); err != nil {
			return err
		}
	}
	return nil
}
