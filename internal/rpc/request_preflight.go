package rpc

import (
	"encoding/binary"
	"fmt"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/tg"

	"github.com/iamxvbaba/td/tlprofile"
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

type rpcPreflightWire interface {
	WireSize() int
	ByteAt(offset int) (byte, error)
	Uint32At(offset int) (uint32, error)
}

type rawRPCPreflightWire []byte

func (w rawRPCPreflightWire) WireSize() int { return len(w) }

func (w rawRPCPreflightWire) ByteAt(offset int) (byte, error) {
	if offset < 0 || offset >= len(w) {
		return 0, fmt.Errorf("byte offset %d outside request size %d", offset, len(w))
	}
	return w[offset], nil
}

func (w rawRPCPreflightWire) Uint32At(offset int) (uint32, error) {
	if offset < 0 || offset > len(w) || len(w)-offset < 4 {
		return 0, fmt.Errorf("uint32 offset %d outside request size %d", offset, len(w))
	}
	return binary.LittleEndian.Uint32(w[offset:]), nil
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

// layerRPCVectorPolicies bind resource limits to generated semantic field
// identities, not constructor CRCs, profile-specific field order, flags bits,
// or byte offsets. gotdgen proves at generation time that each registered
// field exposes the same metric in every routable profile. Importing a future
// schema therefore either extends these policies automatically or makes
// dispatcher construction fail closed until an explicit conversion policy is
// supplied.
var layerRPCVectorPolicies = []struct {
	fieldID tlprofile.FieldID
	max     int
	tooLong func() error
}{
	{tlprofile.FieldUsersGetUsersID, 100, inputRequestTooLongErr},
	{tlprofile.FieldUsersGetRequirementsToContactID, maxRequirementsToContactUsers, limitInvalidErr},
	{tlprofile.FieldContactsImportContactsContacts, maxContactImportBatch, limitInvalidErr},
	{tlprofile.FieldContactsDeleteContactsID, maxContactDeleteBatch, limitInvalidErr},
	{tlprofile.FieldContactsEditCloseFriendsID, maxCloseFriendsCount, limitInvalidErr},
	{tlprofile.FieldContactsSetBlockedID, maxContactSetBlocked, limitInvalidErr},
	{tlprofile.FieldMessagesGetMessagesID, maxGetMessagesIDs, limitInvalidErr},
	{tlprofile.FieldMessagesGetChatsID, maxGetMessagesIDs, limitInvalidErr},
	{tlprofile.FieldMessagesGetPeerDialogsPeers, maxDialogInputPeers, limitInvalidErr},
	{tlprofile.FieldMessagesReadMessageContentsID, maxGetMessagesIDs, limitInvalidErr},
	{tlprofile.FieldMessagesGetCustomEmojiDocumentsDocumentID, maxEmojiDocuments, limitInvalidErr},
	{tlprofile.FieldMessagesDeleteMessagesID, domain.MaxDeleteMessageIDs, limitInvalidErr},
	{tlprofile.FieldMessagesCreateChatUsers, 200, limitInvalidErr},
	{tlprofile.FieldChannelsGetChannelsID, maxGetMessagesIDs, limitInvalidErr},
}

func registerLayerRPCAdmissionFieldPreflights(d *tlprofile.Dispatcher) error {
	if d == nil {
		return fmt.Errorf("nil layer RPC dispatcher")
	}
	for _, policy := range layerRPCVectorPolicies {
		policy := policy
		if err := d.OnFieldPreflight(policy.fieldID, func(view tlprofile.FieldView) error {
			length, ok := view.VectorLength()
			if !ok {
				return inputRequestInvalidErr()
			}
			if length <= policy.max {
				return nil
			}
			if policy.tooLong != nil {
				return policy.tooLong()
			}
			return inputRequestTooLongErr()
		}); err != nil {
			return fmt.Errorf("register vector field %#016x: %w", uint64(policy.fieldID), err)
		}
	}

	for _, fieldID := range []tlprofile.FieldID{
		tlprofile.FieldUploadSaveFilePartBytes,
		tlprofile.FieldUploadSaveBigFilePartBytes,
	} {
		if err := d.OnFieldPreflight(fieldID, func(view tlprofile.FieldView) error {
			length, ok := view.BytesLength()
			if !ok {
				return inputRequestInvalidErr()
			}
			if length > appfiles.MaxUploadPartBytes {
				return filePartTooBigErr()
			}
			return nil
		}); err != nil {
			return fmt.Errorf("register upload bytes field %#016x: %w", uint64(fieldID), err)
		}
	}

	if err := d.OnFieldPreflight(
		tlprofile.FieldUploadSaveBigFilePartFileTotalParts,
		func(view tlprofile.FieldView) error {
			totalParts, ok := view.Int32()
			if !ok {
				return inputRequestInvalidErr()
			}
			if totalParts <= 0 || totalParts > int32(appfiles.MaxUploadParts) {
				return filePartInvalidErr()
			}
			return nil
		},
	); err != nil {
		return fmt.Errorf("register upload total-parts field: %w", err)
	}
	return nil
}

func preflightRPCRequest(id uint32, b *bin.Buffer) error {
	if b == nil {
		return inputRequestInvalidErr()
	}
	return preflightRPCWire(id, rawRPCPreflightWire(b.Buf))
}

func preflightRPCWire(id uint32, wire rpcPreflightWire) error {
	if wire == nil {
		return inputRequestInvalidErr()
	}
	if policy, ok := requestVectorPolicies[id]; ok {
		if err := preflightFixedVector(wire, policy); err != nil {
			return err
		}
	}
	switch id {
	case tg.UploadSaveFilePartRequestTypeID:
		return preflightUploadPart(wire, 16, false)
	case tg.UploadSaveBigFilePartRequestTypeID:
		return preflightUploadPart(wire, 20, true)
	default:
		return nil
	}
}

func preflightFixedVector(wire rpcPreflightWire, policy requestVectorPolicy) error {
	if policy.vectorOffset < 4 || policy.minElemBytes <= 0 || wire.WireSize() < policy.vectorOffset+8 {
		return inputRequestInvalidErr()
	}
	typeID, err := wire.Uint32At(policy.vectorOffset)
	if err != nil || typeID != tlVectorTypeID {
		return inputRequestInvalidErr()
	}
	rawCount, err := wire.Uint32At(policy.vectorOffset + 4)
	if err != nil {
		return inputRequestInvalidErr()
	}
	count := int64(int32(rawCount))
	if count < 0 {
		return inputRequestInvalidErr()
	}
	remaining := int64(wire.WireSize() - policy.vectorOffset - 8)
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

func preflightUploadPart(wire rpcPreflightWire, bytesOffset int, big bool) error {
	if big {
		if wire.WireSize() < 20 {
			return inputRequestInvalidErr()
		}
		rawTotalParts, err := wire.Uint32At(16)
		if err != nil {
			return inputRequestInvalidErr()
		}
		totalParts := int32(rawTotalParts)
		if totalParts <= 0 || totalParts > appfiles.MaxUploadParts {
			return filePartInvalidErr()
		}
	}
	n, encoded, err := tlBytesSizeAt(wire, bytesOffset)
	if err != nil {
		return inputRequestInvalidErr()
	}
	if encoded != wire.WireSize()-bytesOffset {
		return inputRequestInvalidErr()
	}
	if n > appfiles.MaxUploadPartBytes {
		return filePartTooBigErr()
	}
	return nil
}

// tlBytesSizeAt parses a TL bytes prefix without copying the payload.  encoded includes prefix,
// payload and 4-byte padding.
func tlBytesSizeAt(wire rpcPreflightWire, offset int) (n, encoded int, err error) {
	if offset < 0 || offset >= wire.WireSize() {
		return 0, 0, fmt.Errorf("bytes prefix out of range")
	}
	first, err := wire.ByteAt(offset)
	if err != nil {
		return 0, 0, err
	}
	prefix := 1
	switch {
	case first < 254:
		n = int(first)
	case first == 254:
		if wire.WireSize()-offset < 4 {
			return 0, 0, fmt.Errorf("truncated long bytes prefix")
		}
		second, secondErr := wire.ByteAt(offset + 1)
		third, thirdErr := wire.ByteAt(offset + 2)
		fourth, fourthErr := wire.ByteAt(offset + 3)
		if secondErr != nil || thirdErr != nil || fourthErr != nil {
			return 0, 0, fmt.Errorf("truncated long bytes prefix")
		}
		n = int(second) | int(third)<<8 | int(fourth)<<16
		prefix = 4
	default:
		return 0, 0, fmt.Errorf("invalid bytes prefix")
	}
	total := prefix + n
	padding := (4 - total%4) % 4
	if total > wire.WireSize()-offset-padding {
		return 0, 0, fmt.Errorf("truncated bytes payload")
	}
	return n, total + padding, nil
}
