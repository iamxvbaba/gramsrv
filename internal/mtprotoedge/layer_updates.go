package mtprotoedge

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tlprofile"
)

var (
	ErrOutboundLayerProfileUnknown  = errors.New("outbound exact layer profile is unknown")
	ErrOutboundLayerProfileMismatch = errors.New("outbound exact layer profile mismatch")
	// ErrOutboundLayerBindingRequired means application bytes reached the edge
	// without the request- or session-owned profile proof required by the native
	// generated codec. Never substitute canonical bytes or the retired legacy
	// transcoder on a production connection.
	ErrOutboundLayerBindingRequired = errors.New("outbound application value requires exact layer binding")
	// ErrOutboundLayerProfileStale means a proactive update was prepared for an
	// older connection profile epoch. It is deliberately non-terminal: discard
	// the online accelerator and let durable difference recovery fill the gap.
	ErrOutboundLayerProfileStale = errors.New("outbound exact layer profile epoch is stale")
)

type outboundLayerBindingKind uint8

const (
	// The zero value preserves strict connection binding for defensive and
	// legacy constructions which do not declare a stronger ownership model.
	outboundLayerBindingSession outboundLayerBindingKind = iota
	// A request-bound result retains the profile captured by admission. A later
	// invokeWithLayer correction must not invalidate that in-flight/cached result.
	outboundLayerBindingRequest
)

type outboundLayerBinding struct {
	profile       tlprofile.Profile
	wireInvariant bool
	kind          outboundLayerBindingKind
	// epoch is required for proactive updates. Zero is accepted only for older
	// defensive/test bindings which predate mutable profile correction.
	epoch uint32
}

type preparedLayerUpdates struct {
	done    chan struct{}
	encoded *encodedOutboundMessage
	err     error
}

// layerUpdatesFanout is one immutable canonical Updates snapshot plus a
// request-scoped cache of exact prepared bytes. FreezeObject and
// FrozenObject.Prepare use the same sparse TypeRef execution plans as RPC results
// and differences; this type adds only fan-out singleflight and ownership.
type layerUpdatesFanout struct {
	frozen *tlprofile.FrozenObject
	size   int

	mu       sync.Mutex
	prepared map[tlprofile.Profile]*preparedLayerUpdates
}

func newLayerUpdatesFanout(value tg.UpdatesClass) (*layerUpdatesFanout, error) {
	frozen, err := tlprofile.FreezeObject(value)
	if err != nil {
		return nil, fmt.Errorf("freeze exact layer updates: %w", err)
	}
	return &layerUpdatesFanout{
		frozen:   frozen,
		size:     frozen.CanonicalSize(),
		prepared: make(map[tlprofile.Profile]*preparedLayerUpdates),
	}, nil
}

func (u *layerUpdatesFanout) canonicalSize() int {
	if u == nil {
		return 0
	}
	return u.size
}

func (u *layerUpdatesFanout) prepareForConn(ctx context.Context, c *Conn) (*encodedOutboundMessage, error) {
	if u == nil || c == nil {
		return nil, errors.New("nil exact layer update or connection")
	}
	state := c.LayerProfileState()
	if state.Origin == LayerProfileUnknown {
		return nil, ErrOutboundLayerProfileUnknown
	}
	base, err := u.prepare(ctx, state.Profile)
	if err != nil {
		return nil, err
	}
	if base == nil || base.layer == nil {
		return nil, errors.New("prepared exact layer update lost binding")
	}
	// Exact bytes stay shared per profile. Only the small binding is copied per
	// physical target because epoch belongs to that connection.
	encoded := *base
	binding := *base.layer
	binding.kind = outboundLayerBindingSession
	binding.epoch = state.Epoch
	encoded.layer = &binding
	return &encoded, nil
}

func (u *layerUpdatesFanout) prepare(ctx context.Context, profile tlprofile.Profile) (*encodedOutboundMessage, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	u.mu.Lock()
	entry := u.prepared[profile]
	if entry == nil {
		entry = &preparedLayerUpdates{done: make(chan struct{})}
		u.prepared[profile] = entry
		u.mu.Unlock()

		entry.encoded, entry.err = prepareFrozenLayerUpdatesContext(ctx, profile, u.frozen)
		close(entry.done)
		if entry.err != nil {
			// Context/encode admission failure is attempt-local. Do not poison this
			// semantic update for every later connection or pending-flush retry.
			u.mu.Lock()
			if u.prepared[profile] == entry {
				delete(u.prepared, profile)
			}
			u.mu.Unlock()
		}
	} else {
		u.mu.Unlock()
		// A completed profile is immutable and no longer needs caller budget.
		// Prefer it deterministically even when the shared fan-out deadline became
		// ready at the same instant; otherwise Go's select may randomly choose the
		// canceled branch and skip a healthy later connection of the same profile.
		select {
		case <-entry.done:
			return entry.encoded, entry.err
		default:
		}
		select {
		case <-entry.done:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return entry.encoded, entry.err
}

func (u *layerUpdatesFanout) discardPrepared(profile tlprofile.Profile, encoded *encodedOutboundMessage) {
	if u == nil || encoded == nil {
		return
	}
	u.mu.Lock()
	entry := u.prepared[profile]
	if entry != nil {
		select {
		case <-entry.done:
			if entry.encoded == encoded || (entry.encoded != nil &&
				entry.encoded.layer != nil && encoded.layer != nil &&
				entry.encoded.layer.profile == encoded.layer.profile &&
				sameBacking(entry.encoded.body, encoded.body)) {
				delete(u.prepared, profile)
			}
		default:
		}
	}
	u.mu.Unlock()
}

func prepareFrozenLayerUpdatesContext(
	ctx context.Context,
	profile tlprofile.Profile,
	frozen *tlprofile.FrozenObject,
) (*encodedOutboundMessage, error) {
	var encoded *encodedOutboundMessage
	err := withOutboundEncodeSlot(ctx, nil, func() error {
		var body bin.Buffer
		if err := frozen.Encode(profile, &body); err != nil {
			return err
		}
		id, err := body.PeekID()
		if err != nil {
			return fmt.Errorf("peek exact updates constructor: %w", err)
		}
		encoded = &encodedOutboundMessage{
			body: body.Copy(), typeID: id,
			layer: &outboundLayerBinding{profile: profile},
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("prepare updates for layer %d: %w", profile, err)
	}
	return encoded, nil
}

func validateOutboundLayerBinding(c *Conn, encoded *encodedOutboundMessage) error {
	if encoded == nil || encoded.layer == nil {
		return nil
	}
	if encoded.layer.wireInvariant || encoded.layer.kind == outboundLayerBindingRequest {
		return nil
	}
	state := c.LayerProfileState()
	if state.Origin == LayerProfileUnknown {
		return ErrOutboundLayerProfileUnknown
	}
	if encoded.layer.epoch != 0 && state.Epoch != encoded.layer.epoch {
		return fmt.Errorf("%w: connection=%d encoded=%d", ErrOutboundLayerProfileStale, state.Epoch, encoded.layer.epoch)
	}
	if state.Profile != encoded.layer.profile {
		return fmt.Errorf("%w: connection=%d encoded=%d", ErrOutboundLayerProfileMismatch, state.Profile, encoded.layer.profile)
	}
	return nil
}

func isOutboundStaleLayerEpoch(err error) bool {
	return errors.Is(err, ErrOutboundLayerProfileStale)
}

func isOutboundLayerProfileError(err error) bool {
	return errors.Is(err, ErrOutboundLayerProfileUnknown) ||
		errors.Is(err, ErrOutboundLayerProfileMismatch)
}
