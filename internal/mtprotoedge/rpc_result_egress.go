package mtprotoedge

import (
	"context"
	"strings"
	"sync"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/proto"
)

const (
	rpcResultGZIPMinBytes        = 4 << 10
	rpcResultGZIPMaxInputBytes   = (10 << 20) - 1 // gotd client decompression hard limit.
	rpcResultGZIPMinSavedBytes   = 1 << 10
	rpcResultGZIPMinSavedDivisor = 12 // Require roughly 8.3% reduction.
	rpcResultGZIPConcurrency     = 8
	rpcDeliveryHookConcurrency   = 8
	rpcDeliveryHookQueueSize     = 1024
)

var rpcResultGZIPSlots = make(chan struct{}, rpcResultGZIPConcurrency)

var (
	rpcDeliveryHooksOnce sync.Once
	rpcDeliveryHooks     chan func()
)

// scheduleRPCDeliveryHook keeps database/update follow-up work off the sole
// socket writer. Hooks are internal, bounded and timeout-aware at registration
// sites; the fixed worker set prevents one delivered result from stalling every
// subsequent frame on that connection.
func scheduleRPCDeliveryHook(fn func()) {
	if fn == nil {
		return
	}
	rpcDeliveryHooksOnce.Do(func() {
		rpcDeliveryHooks = make(chan func(), rpcDeliveryHookQueueSize)
		for range rpcDeliveryHookConcurrency {
			go func() {
				for hook := range rpcDeliveryHooks {
					hook()
				}
			}()
		}
	})
	rpcDeliveryHooks <- fn
}

// encodeAdaptiveRPCResultInner returns either the original layer-specific TL
// object or one complete gzip_packed object. Compression is CPU bounded and is
// retained only when it materially reduces the non-preemptible transport frame.
func encodeAdaptiveRPCResultInner(ctx context.Context, stop <-chan struct{}, inner []byte) ([]byte, bool, error) {
	if len(inner) < rpcResultGZIPMinBytes || len(inner) > rpcResultGZIPMaxInputBytes {
		return inner, false, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case rpcResultGZIPSlots <- struct{}{}:
	case <-ctx.Done():
		return nil, false, ctx.Err()
	case <-stop:
		return nil, false, ErrConnClosed
	}
	defer func() { <-rpcResultGZIPSlots }()

	var packed bin.Buffer
	if err := (proto.GZIP{Data: inner}).Encode(&packed); err != nil {
		return nil, false, err
	}
	saved := len(inner) - packed.Len()
	required := max(rpcResultGZIPMinSavedBytes, len(inner)/rpcResultGZIPMinSavedDivisor)
	if saved < required {
		return inner, false, nil
	}
	return packed.Raw(), true, nil
}

// rpcResultPriority is protocol scheduling metadata, not handler business
// behavior. Difference/state responses converge the update state, while the
// dialogs+pinned pair converges the initial chat list in both TDesktop and
// Android. These bootstrap barriers must pass background prefetch regardless of
// platform or their own encoded size.
func rpcResultPriority(method string, encoded *encodedOutboundMessage) outboundPriority {
	base := method
	if i := strings.IndexByte(base, '#'); i >= 0 {
		base = base[:i]
	}
	switch base {
	case "updates.getDifference", "updates.getChannelDifference", "updates.getState",
		"messages.getDialogs", "messages.getPinnedDialogs":
		return outboundPriorityCritical
	}
	return classifyOutboundPriority(encoded, false)
}

func (p outboundPriority) String() string {
	switch p {
	case outboundPriorityCritical:
		return "convergence"
	case outboundPriorityBulk:
		return "bulk"
	case outboundPriorityControl:
		return "control"
	default:
		return "normal"
	}
}
