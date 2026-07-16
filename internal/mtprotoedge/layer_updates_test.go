package mtprotoedge

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tlprofile"
)

type epochBlockingTransport struct {
	started     chan struct{}
	release     chan struct{}
	startedOnce sync.Once
	closeOnce   sync.Once
}

func newEpochBlockingTransport() *epochBlockingTransport {
	return &epochBlockingTransport{started: make(chan struct{}), release: make(chan struct{})}
}

func (t *epochBlockingTransport) Send(context.Context, *bin.Buffer) error {
	t.startedOnce.Do(func() { close(t.started) })
	<-t.release
	return nil
}

func (t *epochBlockingTransport) Recv(context.Context, *bin.Buffer) error { return io.EOF }
func (t *epochBlockingTransport) Close() error {
	t.closeOnce.Do(func() { close(t.release) })
	return nil
}

func testLayerUpdatesValue(expires int) tg.UpdatesClass {
	return &tg.UpdateShort{
		Update: &tg.UpdateUserStatus{
			UserID: 42,
			Status: &tg.UserStatusOnline{
				Expires: expires,
			},
		},
		Date: 1_900_000_000,
	}
}

func testLayerChannelUpdatesValue(expires int) tg.UpdatesClass {
	return &tg.Updates{
		Updates: []tg.UpdateClass{
			&tg.UpdateUserStatus{
				UserID: 42,
				Status: &tg.UserStatusOnline{
					Expires: expires,
				},
			},
		},
		Users: []tg.UserClass{},
		Chats: []tg.ChatClass{testLayerChannel()},
		Date:  1_900_000_000,
		Seq:   1,
	}
}

func testConnWithLayerProfile(t *testing.T, profile tlprofile.Profile) *Conn {
	t.Helper()
	c := &Conn{}
	if err := c.FreezeLayerProfile(profile); err != nil {
		t.Fatalf("freeze profile %d: %v", profile, err)
	}
	return c
}

func TestLayerUpdatesFanoutPreparesExactMixedProfiles(t *testing.T) {
	fanout, err := newLayerUpdatesFanout(testLayerUpdatesValue(123))
	if err != nil {
		t.Fatalf("freeze updates: %v", err)
	}
	for _, profile := range []tlprofile.Profile{tlprofile.Profile225, tlprofile.Profile227, tlprofile.Profile228} {
		t.Run(fmt.Sprintf("layer_%d", profile), func(t *testing.T) {
			c := testConnWithLayerProfile(t, profile)
			encoded, err := fanout.prepareForConn(context.Background(), c)
			if err != nil {
				t.Fatalf("prepare profile %d: %v", profile, err)
			}
			if encoded.layer == nil || encoded.layer.profile != profile {
				t.Fatalf("binding = %#v, want profile %d", encoded.layer, profile)
			}
			input := bin.Buffer{Buf: encoded.body}
			decoded, err := tlprofile.DecodeObject(profile, &input, tlprofile.Limits{})
			if err != nil {
				t.Fatalf("decode profile %d: %v", profile, err)
			}
			if input.Len() != 0 {
				t.Fatalf("profile %d left %d trailing bytes", profile, input.Len())
			}
			short, ok := decoded.(*tg.UpdateShort)
			if !ok {
				t.Fatalf("decoded %T, want *tg.UpdateShort", decoded)
			}
			statusUpdate, ok := short.Update.(*tg.UpdateUserStatus)
			if !ok {
				t.Fatalf("nested update %T, want *tg.UpdateUserStatus", short.Update)
			}
			status, ok := statusUpdate.Status.(*tg.UserStatusOnline)
			if !ok || status.Expires != 123 {
				t.Fatalf("decoded status = %#v", statusUpdate.Status)
			}
		})
	}
}

func TestLayerUpdatesFanoutFreezesDefensivelyAndSharesPreparedProfile(t *testing.T) {
	value := testLayerUpdatesValue(123)
	fanout, err := newLayerUpdatesFanout(value)
	if err != nil {
		t.Fatalf("freeze updates: %v", err)
	}
	value.(*tg.UpdateShort).Update.(*tg.UpdateUserStatus).Status.(*tg.UserStatusOnline).Expires = 999

	c := testConnWithLayerProfile(t, tlprofile.Profile225)
	const workers = 16
	prepared := make([]*encodedOutboundMessage, workers)
	prepareErrs := make([]error, workers)
	var wg sync.WaitGroup
	for i := range prepared {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			prepared[i], prepareErrs[i] = fanout.prepareForConn(context.Background(), c)
		}(i)
	}
	wg.Wait()
	for i, prepareErr := range prepareErrs {
		if prepareErr != nil {
			t.Fatalf("prepare %d: %v", i, prepareErr)
		}
	}
	for i := 1; i < len(prepared); i++ {
		if !sameBacking(prepared[i].body, prepared[0].body) {
			t.Fatalf("profile preparation %d did not share immutable bytes", i)
		}
		if prepared[i].layer == prepared[0].layer || prepared[i].layer.epoch != prepared[0].layer.epoch {
			t.Fatalf("profile preparation %d did not retain per-target epoch binding", i)
		}
	}

	input := bin.Buffer{Buf: prepared[0].body}
	decoded, err := tlprofile.DecodeObject(tlprofile.Profile225, &input, tlprofile.Limits{})
	if err != nil {
		t.Fatalf("decode frozen value: %v", err)
	}
	status := decoded.(*tg.UpdateShort).Update.(*tg.UpdateUserStatus).Status.(*tg.UserStatusOnline)
	if status.Expires != 123 {
		t.Fatalf("frozen value mutated: expires=%d", status.Expires)
	}
}

func TestLayerUpdatesEpochBecomesStaleWithoutRetiringProfile(t *testing.T) {
	fanout, err := newLayerUpdatesFanout(testLayerUpdatesValue(123))
	if err != nil {
		t.Fatal(err)
	}
	c := &Conn{}
	if err := c.SeedInheritedLayerProfile(tlprofile.Profile225); err != nil {
		t.Fatal(err)
	}
	encoded, err := fanout.prepareForConn(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	oldEpoch := encoded.layer.epoch
	if err := c.FreezeLayerProfile(tlprofile.Profile227); err != nil {
		t.Fatal(err)
	}
	if err := validateOutboundLayerBinding(c, encoded); !errors.Is(err, ErrOutboundLayerProfileStale) {
		t.Fatalf("old push validation = %v, want ErrOutboundLayerProfileStale", err)
	}
	state := c.LayerProfileState()
	if state.Profile != tlprofile.Profile227 || state.Origin != LayerProfileExplicit || state.Epoch <= oldEpoch {
		t.Fatalf("corrected profile state = %#v, old epoch %d", state, oldEpoch)
	}
}

func TestRequestBoundLayerResultSurvivesConnectionCorrection(t *testing.T) {
	fanout, err := newLayerUpdatesFanout(testLayerUpdatesValue(123))
	if err != nil {
		t.Fatal(err)
	}
	c := testConnWithLayerProfile(t, tlprofile.Profile225)
	encoded, err := fanout.prepare(context.Background(), tlprofile.Profile225)
	if err != nil {
		t.Fatal(err)
	}
	encoded.layer.kind = outboundLayerBindingRequest
	if err := c.FreezeLayerProfile(tlprofile.Profile227); err != nil {
		t.Fatal(err)
	}
	if err := validateOutboundLayerBinding(c, encoded); err != nil {
		t.Fatalf("request-bound old-profile result rejected after correction: %v", err)
	}
}

func TestProfileCorrectionLinearizesAfterStartedPushWrite(t *testing.T) {
	transport := newEpochBlockingTransport()
	c := newOutboundTestConn(t, transport, nil)
	if err := c.FreezeLayerProfile(tlprofile.Profile225); err != nil {
		t.Fatal(err)
	}
	fanout, err := newLayerUpdatesFanout(testLayerUpdatesValue(123))
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := fanout.prepareForConn(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.SendBestEffortEncoded(context.Background(), 0, encoded, 0); err != nil {
		t.Fatal(err)
	}
	select {
	case <-transport.started:
	case <-time.After(time.Second):
		t.Fatal("profile-bound push did not enter physical write")
	}

	corrected := make(chan error, 1)
	go func() { corrected <- c.FreezeLayerProfile(tlprofile.Profile227) }()
	select {
	case err := <-corrected:
		t.Fatalf("profile correction crossed an old-epoch physical write: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	if err := transport.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-corrected:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("profile correction did not continue after old write completed")
	}
	if err := validateOutboundLayerBinding(c, encoded); !errors.Is(err, ErrOutboundLayerProfileStale) {
		t.Fatalf("completed old push binding = %v, want stale after correction", err)
	}
}

func TestStaleLayerPushIsRemovedFromResendTracking(t *testing.T) {
	c := &Conn{metrics: NopMetrics{}}
	if err := c.FreezeLayerProfile(tlprofile.Profile225); err != nil {
		t.Fatal(err)
	}
	fanout, err := newLayerUpdatesFanout(testLayerUpdatesValue(123))
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := fanout.prepareForConn(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.FreezeLayerProfile(tlprofile.Profile227); err != nil {
		t.Fatal(err)
	}
	frame := &outboundFrame{msgID: 100, body: encoded.body, layer: encoded.layer}
	state := &outboundState{
		pending:    map[int64]*outboundFrame{100: frame},
		order:      []int64{100},
		totalBytes: len(frame.body),
	}
	if _, err := c.handleOutboundResend(state, context.Background(), []int64{100}); err != nil {
		t.Fatal(err)
	}
	if _, ok := state.pending[100]; ok || frame.body != nil {
		t.Fatal("stale profile frame remained resendable")
	}
}

func TestOutboundLayerBindingRejectsUnknownAndMismatchedConnections(t *testing.T) {
	fanout, err := newLayerUpdatesFanout(testLayerUpdatesValue(123))
	if err != nil {
		t.Fatalf("freeze updates: %v", err)
	}
	encoded, err := fanout.prepare(context.Background(), tlprofile.Profile225)
	if err != nil {
		t.Fatalf("prepare profile 225: %v", err)
	}
	if _, err := (&Conn{}).buildFrame(context.Background(), 0, nil, encoded); !errors.Is(err, ErrOutboundLayerProfileUnknown) {
		t.Fatalf("unknown profile error = %v", err)
	}
	wrong := testConnWithLayerProfile(t, tlprofile.Profile227)
	if _, err := wrong.buildFrame(context.Background(), 0, nil, encoded); !errors.Is(err, ErrOutboundLayerProfileMismatch) {
		t.Fatalf("profile mismatch error = %v", err)
	}
}

func TestPendingPushReservationAccountsPreparedProfilesOnce(t *testing.T) {
	budget := newOutboundTrackedBudget(4096)
	if !budget.reserve(100) {
		t.Fatal("reserve canonical snapshot")
	}
	reservation := &pendingPushReservation{budget: budget}
	reservation.bytes.Store(100)
	reservation.refs.Store(1)

	if !reservation.reservePrepared(tlprofile.Profile225, 80) {
		t.Fatal("reserve first profile")
	}
	if !reservation.reservePrepared(tlprofile.Profile225, 80) {
		t.Fatal("reuse first profile reservation")
	}
	if !reservation.reservePrepared(tlprofile.Profile227, 120) {
		t.Fatal("reserve second profile")
	}
	if got := budget.snapshot(); got != 300 {
		t.Fatalf("tracked pending bytes = %d, want canonical + unique profiles = 300", got)
	}
	reservation.release()
	if got := budget.snapshot(); got != 0 {
		t.Fatalf("tracked pending bytes after final release = %d", got)
	}
}
