package coreexec

import (
	"bytes"
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tlprofile"
	"go.uber.org/zap"

	"telesrv/internal/rpc"
)

func TestGRPCDispatchRequestTransfersOwnedWire(t *testing.T) {
	for _, size := range []int{64 << 10, 1 << 20} {
		t.Run(fmt.Sprintf("bytes_%d", size), func(t *testing.T) {
			wire := makePatternBytes(size)
			captured := capturedAdmission{
				Mode:    AdmissionModeLayer,
				Profile: tlprofile.Profile228,
				Wire:    wire,
			}

			req := grpcDispatchRequest(&captured, [8]byte{1}, 2, 3, 4, true, nil, "")
			if req == nil {
				t.Fatal("grpcDispatchRequest returned nil")
			}
			if captured.Wire != nil {
				t.Fatal("captured admission retained transferred request wire")
			}
			if len(req.RequestWire) != size || &req.RequestWire[0] != &wire[0] {
				t.Fatal("request wire was cloned instead of ownership-transferred")
			}
		})
	}
}

func TestAdmitCapturedBorrowsWireAndMaterializesOwnedFields(t *testing.T) {
	for _, size := range []int{64 << 10, 1 << 20} {
		t.Run(fmt.Sprintf("bytes_%d", size), func(t *testing.T) {
			payload := makePatternBytes(size)
			var encoded bin.Buffer
			if err := tlprofile.EncodeObject(tlprofile.Profile228, &tg.UploadSaveFilePartRequest{
				FileID:   11,
				FilePart: 7,
				Bytes:    payload,
			}, &encoded); err != nil {
				t.Fatal(err)
			}
			wire := encoded.Copy()
			wireBefore := append([]byte(nil), wire...)

			dispatcher := tlprofile.NewDispatcher()
			var capturedPayload []byte
			if err := dispatcher.Register(tlprofile.SemanticMethodUploadSaveFilePart, func(_ context.Context, object bin.Object) (any, error) {
				req, ok := object.(*tg.UploadSaveFilePartRequest)
				if !ok {
					return nil, fmt.Errorf("request = %T", object)
				}
				capturedPayload = append([]byte(nil), req.Bytes...)
				return true, nil
			}); err != nil {
				t.Fatal(err)
			}
			handler := &admissionOnlyHandler{dispatcher: dispatcher}
			admitted, err := admitCaptured(handler, AdmissionModeLayer, int(tlprofile.Profile228), tlprofile.Limits{}, wire)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(wire, wireBefore) {
				t.Fatal("borrowed protobuf request wire was mutated during admission")
			}

			// Production retains the protobuf request through synchronous dispatch.
			// Deliberately mutating it here is a stronger ownership check: the
			// materialized TL bytes field must not alias the borrowed wire.
			for i := range wire {
				wire[i] ^= 0xff
			}
			if _, err := dispatcher.Dispatch(context.Background(), admitted); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(capturedPayload, payload) {
				t.Fatal("materialized request retained borrowed protobuf wire")
			}
		})
	}
}

func TestTakeBufferRawTransfersOwnedBackingArray(t *testing.T) {
	for _, size := range []int{64 << 10, 1 << 20} {
		t.Run(fmt.Sprintf("bytes_%d", size), func(t *testing.T) {
			var buffer bin.Buffer
			buffer.Put(makePatternBytes(size))
			original := buffer.Raw()

			owned := takeBufferRaw(&buffer)
			if buffer.Raw() != nil || buffer.Len() != 0 {
				t.Fatal("source buffer retained transferred backing array")
			}
			if len(owned) != size || &owned[0] != &original[0] {
				t.Fatal("owned result wire was cloned instead of transferred")
			}
		})
	}
}

func TestGRPCRoundTripTransfersLargeResultOwnership(t *testing.T) {
	for _, size := range []int{64 << 10, 1 << 20} {
		t.Run(fmt.Sprintf("bytes_%d", size), func(t *testing.T) {
			want := makePatternBytes(size)
			coreRouter := rpc.New(rpc.Config{DC: 2, IP: "127.0.0.1", Port: 2398}, rpc.Deps{}, zap.NewNop(), clock.System)
			core := &fixedResultHandler{Handler: coreRouter, body: append([]byte(nil), want...)}
			edgeRouter := rpc.New(rpc.Config{DC: 2, IP: "127.0.0.1", Port: 2398}, rpc.Deps{}, zap.NewNop(), clock.System)
			remote, cleanup := newBufGRPCRemote(t, core, edgeRouter, "secret", "secret")

			admitted := admitHelpGetConfigForGRPCTest(t, remote)
			result, method, err := remote.DispatchAdmitted(context.Background(), [8]byte{1}, 2, 3, 4, admitted)
			if err != nil {
				cleanup()
				t.Fatal(err)
			}
			if method != "help.getConfig" || result == nil {
				cleanup()
				t.Fatalf("dispatch result = method:%q result:%T", method, result)
			}
			cleanup()

			// Both the server-side result and the gRPC response are now dead. The
			// Edge result must exclusively retain the protobuf decoder's owned bytes.
			for i := range core.body {
				core.body[i] ^= 0xff
			}
			var encoded bin.Buffer
			if err := result.Encode(&encoded); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(encoded.Raw(), want) {
				t.Fatal("large result changed after server payload and connection release")
			}
		})
	}
}

func TestEncodedResultOwnedWireConcurrentEncode(t *testing.T) {
	for _, size := range []int{64 << 10, 1 << 20} {
		t.Run(fmt.Sprintf("bytes_%d", size), func(t *testing.T) {
			body := makePatternBytes(size)
			result := &encodedResult{body: body}

			var wg sync.WaitGroup
			for worker := 0; worker < 8; worker++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					for iteration := 0; iteration < 8; iteration++ {
						var encoded bin.Buffer
						if err := result.Encode(&encoded); err != nil {
							t.Errorf("Encode: %v", err)
							return
						}
						if !bytes.Equal(encoded.Raw(), body) {
							t.Error("encoded result changed immutable owned wire")
							return
						}
					}
				}()
			}
			wg.Wait()
		})
	}
}

type admissionOnlyHandler struct {
	Handler
	dispatcher *tlprofile.Dispatcher
}

type fixedResultHandler struct {
	Handler
	body []byte
}

func (h *fixedResultHandler) DispatchAdmitted(_ context.Context, _ [8]byte, _ int64, _ int64, _ uint64, request tlprofile.Admission) (tlprofile.Result, string, error) {
	return &encodedResult{
		prepared:      request.Prepared(),
		wireInvariant: true,
		body:          h.body,
	}, "help.getConfig", nil
}

func (h *admissionOnlyHandler) AdmitLayerWithOptions(profile tlprofile.Profile, body *bin.Buffer, options tlprofile.AdmissionOptions) (tlprofile.Admission, error) {
	return h.dispatcher.AdmitWithOptions(profile, body, options)
}

func makePatternBytes(size int) []byte {
	out := make([]byte, size)
	for i := range out {
		out[i] = byte(i*31 + 17)
	}
	return out
}
