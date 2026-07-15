package mtprotoedge

import (
	"context"
	"fmt"
	"time"
)

const (
	rpcReplayRestoreTimeout   = 5 * time.Second
	rpcReplayRestoreMaxActive = 8
)

// rpcReplayRestoreSlots bounds callbacks that ignore their own store/context
// deadline. The caller waits only until ctx (and at most
// rpcReplayRestoreTimeout); a stuck callback can retain one of these fixed
// slots, but it cannot retain a rewrap worker, a live Conn scheduler barrier,
// or create an unbounded goroutine population.
var rpcReplayRestoreSlots = make(chan struct{}, rpcReplayRestoreMaxActive)

func boundedRPCReplayRestoreContext(parent context.Context) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	return context.WithTimeout(parent, rpcReplayRestoreTimeout)
}

// runBoundedRPCReplayRestore runs replacement metadata first and the shared
// logical delivery hook second. On timeout it fences the physical generation
// before returning, so releasing that Conn's scheduler barrier cannot expose a
// following RPC to partially restored state. A claim that has not entered the
// hook is abandoned and may be acquired by a later replacement replay; an
// in-progress hook remains at-most-once and every later replay waits for Done.
func (s *Server) runBoundedRPCReplayRestore(
	ctx context.Context,
	c *Conn,
	source string,
	claim *rpcResultDeliveryHookClaim,
	replacement func() error,
) error {
	if claim == nil && replacement == nil {
		return nil
	}
	boundedCtx, cancel := boundedRPCReplayRestoreContext(ctx)
	defer cancel()

	select {
	case rpcReplayRestoreSlots <- struct{}{}:
	case <-boundedCtx.Done():
		claim.abandon()
		if c != nil {
			c.fenceUndeliveredRPCResult()
		}
		return fmt.Errorf("restore replay state after %s: %w", source, boundedCtx.Err())
	}

	result := make(chan error, 1)
	var logical func()
	if claim != nil {
		logical = claim.run
	}
	go func() {
		defer func() { <-rpcReplayRestoreSlots }()
		result <- s.runRPCReplayRestore(c, source, composeRPCReplayRestore(logical, replacement))
	}()

	select {
	case err := <-result:
		return err
	case <-boundedCtx.Done():
		// Prefer a restore that reached its terminal state concurrently with the
		// deadline; its result channel publishes coordinator Done before send.
		select {
		case err := <-result:
			return err
		default:
		}
		// If replacement metadata is still blocked, the hook is only Claimed and
		// can be safely re-acquired. Once hook execution is InProgress, abandon
		// deliberately fails: retrying an unknown partial side effect is forbidden.
		claim.abandon()
		if c != nil {
			c.fenceUndeliveredRPCResult()
		}
		return fmt.Errorf("restore replay state after %s: %w", source, boundedCtx.Err())
	}
}
