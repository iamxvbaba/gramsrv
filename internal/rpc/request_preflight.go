package rpc

import (
	"encoding/binary"
	"fmt"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"

	appfiles "telesrv/internal/app/files"
	"telesrv/internal/domain"
)

const tlVectorTypeID = uint32(0x1cb5c415)

type requestVectorPolicy struct {
	vectorOffset int
	max          int
	minElemBytes int
	tooLong      func() error
}

// requestVectorPolicies mirrors limits already enforced by typed handlers, but does so before
// gotd's generated decoder materializes attacker-controlled interface slices.  users.getUsers is
// the one newly introduced cap: TDesktop's four current call sites all send exactly one user.
var requestVectorPolicies = map[uint32]requestVectorPolicy{
	tg.UsersGetUsersRequestTypeID:                   {vectorOffset: 4, max: 100, minElemBytes: 4, tooLong: inputRequestTooLongErr},
	tg.UsersGetRequirementsToContactRequestTypeID:   {vectorOffset: 4, max: maxRequirementsToContactUsers, minElemBytes: 4, tooLong: limitInvalidErr},
	tg.ContactsImportContactsRequestTypeID:          {vectorOffset: 4, max: maxContactImportBatch, minElemBytes: 4, tooLong: limitInvalidErr},
	tg.ContactsDeleteContactsRequestTypeID:          {vectorOffset: 4, max: maxContactDeleteBatch, minElemBytes: 4, tooLong: limitInvalidErr},
	tg.ContactsEditCloseFriendsRequestTypeID:        {vectorOffset: 4, max: maxCloseFriendsCount, minElemBytes: 8, tooLong: limitInvalidErr},
	tg.ContactsSetBlockedRequestTypeID:              {vectorOffset: 8, max: maxContactSetBlocked, minElemBytes: 4, tooLong: limitInvalidErr},
	tg.MessagesGetMessagesRequestTypeID:             {vectorOffset: 4, max: maxGetMessagesIDs, minElemBytes: 4, tooLong: limitInvalidErr},
	tg.MessagesGetChatsRequestTypeID:                {vectorOffset: 4, max: maxGetMessagesIDs, minElemBytes: 8, tooLong: limitInvalidErr},
	tg.MessagesGetPeerDialogsRequestTypeID:          {vectorOffset: 4, max: maxDialogInputPeers, minElemBytes: 4, tooLong: limitInvalidErr},
	tg.MessagesReadMessageContentsRequestTypeID:     {vectorOffset: 4, max: maxGetMessagesIDs, minElemBytes: 4, tooLong: limitInvalidErr},
	tg.MessagesGetCustomEmojiDocumentsRequestTypeID: {vectorOffset: 4, max: maxEmojiDocuments, minElemBytes: 8, tooLong: limitInvalidErr},
	tg.MessagesDeleteMessagesRequestTypeID:          {vectorOffset: 8, max: domain.MaxDeleteMessageIDs, minElemBytes: 4, tooLong: limitInvalidErr},
	tg.MessagesCreateChatRequestTypeID:              {vectorOffset: 8, max: 200, minElemBytes: 4, tooLong: limitInvalidErr},
	tg.ChannelsGetChannelsRequestTypeID:             {vectorOffset: 4, max: maxGetMessagesIDs, minElemBytes: 4, tooLong: limitInvalidErr},
}

func preflightRPCRequest(id uint32, b *bin.Buffer) error {
	if b == nil {
		return inputRequestInvalidErr()
	}
	if policy, ok := requestVectorPolicies[id]; ok {
		if err := preflightFixedVector(b.Buf, policy); err != nil {
			return err
		}
	}
	switch id {
	case tg.UploadSaveFilePartRequestTypeID:
		return preflightUploadPart(b.Buf, 16, false)
	case tg.UploadSaveBigFilePartRequestTypeID:
		return preflightUploadPart(b.Buf, 20, true)
	default:
		return nil
	}
}

func preflightFixedVector(raw []byte, policy requestVectorPolicy) error {
	if policy.vectorOffset < 4 || policy.minElemBytes <= 0 || len(raw) < policy.vectorOffset+8 {
		return inputRequestInvalidErr()
	}
	if binary.LittleEndian.Uint32(raw[policy.vectorOffset:]) != tlVectorTypeID {
		return inputRequestInvalidErr()
	}
	count := int64(int32(binary.LittleEndian.Uint32(raw[policy.vectorOffset+4:])))
	if count < 0 {
		return inputRequestInvalidErr()
	}
	remaining := int64(len(raw) - policy.vectorOffset - 8)
	// Check the cheapest possible encoding before the policy cap.  A forged MaxInt32 count with
	// a truncated body is malformed, not merely a large valid request, and is rejected O(1).
	if count > remaining/int64(policy.minElemBytes) {
		return inputRequestInvalidErr()
	}
	if count > int64(policy.max) {
		if policy.tooLong != nil {
			return policy.tooLong()
		}
		return inputRequestTooLongErr()
	}
	return nil
}

func preflightUploadPart(raw []byte, bytesOffset int, big bool) error {
	if big {
		if len(raw) < 20 {
			return inputRequestInvalidErr()
		}
		totalParts := int32(binary.LittleEndian.Uint32(raw[16:20]))
		if totalParts <= 0 || totalParts > appfiles.MaxUploadParts {
			return filePartInvalidErr()
		}
	}
	n, encoded, err := tlBytesSizeAt(raw, bytesOffset)
	if err != nil {
		return inputRequestInvalidErr()
	}
	if encoded != len(raw)-bytesOffset {
		return inputRequestInvalidErr()
	}
	if n > appfiles.MaxUploadPartBytes {
		return filePartTooBigErr()
	}
	return nil
}

// tlBytesSizeAt parses a TL bytes prefix without copying the payload.  encoded includes prefix,
// payload and 4-byte padding.
func tlBytesSizeAt(raw []byte, offset int) (n, encoded int, err error) {
	if offset < 0 || offset >= len(raw) {
		return 0, 0, fmt.Errorf("bytes prefix out of range")
	}
	first := raw[offset]
	prefix := 1
	switch {
	case first < 254:
		n = int(first)
	case first == 254:
		if len(raw)-offset < 4 {
			return 0, 0, fmt.Errorf("truncated long bytes prefix")
		}
		n = int(raw[offset+1]) | int(raw[offset+2])<<8 | int(raw[offset+3])<<16
		prefix = 4
	default:
		return 0, 0, fmt.Errorf("invalid bytes prefix")
	}
	total := prefix + n
	padding := (4 - total%4) % 4
	if total > len(raw)-offset-padding {
		return 0, 0, fmt.Errorf("truncated bytes payload")
	}
	return n, total + padding, nil
}
