package rpc

import (
	"context"
	"encoding/binary"
	"testing"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"go.uber.org/zap/zaptest"

	appfiles "telesrv/internal/app/files"
)

func TestRequestVectorPreflightMirrorsHandlerCaps(t *testing.T) {
	for id, policy := range requestVectorPolicies {
		id, policy := id, policy
		t.Run(tlTypeName(id), func(t *testing.T) {
			atCap := fixedVectorRequest(id, policy, policy.max)
			if err := preflightRPCRequest(id, &bin.Buffer{Buf: atCap}); err != nil {
				t.Fatalf("cap=%d rejected: %v", policy.max, err)
			}
			over := fixedVectorRequest(id, policy, policy.max+1)
			err := preflightRPCRequest(id, &bin.Buffer{Buf: over})
			if err == nil {
				t.Fatalf("cap+1=%d accepted", policy.max+1)
			}
			want := "LIMIT_INVALID"
			if id == tg.UsersGetUsersRequestTypeID {
				want = "INPUT_REQUEST_TOO_LONG"
			}
			if !tgerr.Is(err, want) {
				t.Fatalf("cap+1 error = %v, want %s", err, want)
			}
		})
	}
}

func TestRequestVectorPreflightRejectsForgedCountInConstantSpace(t *testing.T) {
	policy := requestVectorPolicies[tg.UsersGetUsersRequestTypeID]
	raw := fixedVectorRequest(tg.UsersGetUsersRequestTypeID, policy, 0)
	binary.LittleEndian.PutUint32(raw[policy.vectorOffset+4:], uint32(0x7fffffff))
	if err := preflightRPCRequest(tg.UsersGetUsersRequestTypeID, &bin.Buffer{Buf: raw}); !tgerr.Is(err, "INPUT_REQUEST_INVALID") {
		t.Fatalf("forged count error = %v, want INPUT_REQUEST_INVALID", err)
	}
}

func TestRequestVectorPreflightRunsAfterWrapperBeforeTypedDecode(t *testing.T) {
	ids := make([]tg.InputUserClass, 101)
	for i := range ids {
		ids[i] = &tg.InputUserSelf{}
	}
	wrapped := &tg.InvokeWithLayerRequest{Layer: 227, Query: &tg.UsersGetUsersRequest{ID: ids}}
	var body bin.Buffer
	if err := wrapped.Encode(&body); err != nil {
		t.Fatalf("encode wrapper: %v", err)
	}
	r := New(Config{}, Deps{}, zaptest.NewLogger(t), clock.System)
	_, err := r.Dispatch(context.Background(), [8]byte{1}, 1, &body)
	if !tgerr.Is(err, "INPUT_REQUEST_TOO_LONG") {
		t.Fatalf("wrapped oversized users.getUsers error = %v, want INPUT_REQUEST_TOO_LONG", err)
	}
}

func TestUploadPartPreflightBeforeBytesDecode(t *testing.T) {
	for _, tc := range []struct {
		name       string
		id         uint32
		offset     int
		big        bool
		parts      int
		size       int
		want       string
		truncateBy int
	}{
		{name: "small_at_cap", id: tg.UploadSaveFilePartRequestTypeID, offset: 16, size: appfiles.MaxUploadPartBytes},
		{name: "small_over_cap", id: tg.UploadSaveFilePartRequestTypeID, offset: 16, size: appfiles.MaxUploadPartBytes + 1, want: "FILE_PART_TOO_BIG"},
		{name: "big_at_cap", id: tg.UploadSaveBigFilePartRequestTypeID, offset: 20, big: true, parts: appfiles.MaxUploadParts, size: appfiles.MaxUploadPartBytes},
		{name: "big_parts_over_cap", id: tg.UploadSaveBigFilePartRequestTypeID, offset: 20, big: true, parts: appfiles.MaxUploadParts + 1, size: 1, want: "FILE_PART_INVALID"},
		{name: "truncated", id: tg.UploadSaveFilePartRequestTypeID, offset: 16, size: 1024, truncateBy: 1, want: "INPUT_REQUEST_INVALID"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := uploadPartRequest(tc.id, tc.offset, tc.parts, tc.size)
			if tc.truncateBy > 0 {
				raw = raw[:len(raw)-tc.truncateBy]
			}
			err := preflightRPCRequest(tc.id, &bin.Buffer{Buf: raw})
			if tc.want == "" {
				if err != nil {
					t.Fatalf("preflight: %v", err)
				}
				return
			}
			if !tgerr.Is(err, tc.want) {
				t.Fatalf("error = %v, want %s", err, tc.want)
			}
		})
	}
}

func fixedVectorRequest(id uint32, policy requestVectorPolicy, count int) []byte {
	raw := make([]byte, policy.vectorOffset+8+count*policy.minElemBytes)
	binary.LittleEndian.PutUint32(raw[0:4], id)
	binary.LittleEndian.PutUint32(raw[policy.vectorOffset:policy.vectorOffset+4], tlVectorTypeID)
	binary.LittleEndian.PutUint32(raw[policy.vectorOffset+4:policy.vectorOffset+8], uint32(count))
	return raw
}

func uploadPartRequest(id uint32, offset, parts, size int) []byte {
	raw := make([]byte, offset)
	binary.LittleEndian.PutUint32(raw[:4], id)
	if offset == 20 {
		binary.LittleEndian.PutUint32(raw[16:20], uint32(parts))
	}
	prefix := 1
	if size >= 254 {
		prefix = 4
	}
	total := prefix + size
	padding := (4 - total%4) % 4
	start := len(raw)
	raw = append(raw, make([]byte, total+padding)...)
	if prefix == 1 {
		raw[start] = byte(size)
	} else {
		raw[start] = 254
		raw[start+1] = byte(size)
		raw[start+2] = byte(size >> 8)
		raw[start+3] = byte(size >> 16)
	}
	return raw
}
