package tlprofile

import (
	"errors"
	"fmt"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/tg"
)

// EncodePageBlockVector encodes one complete boxed Vector<PageBlock> using the
// exact wire grammar selected by profile. The operation is transactional: an
// error leaves out unchanged.
func EncodePageBlockVector(profile Profile, blocks []tg.PageBlockClass, out *bin.Buffer) error {
	if out == nil {
		return errors.New("tlprofile: encode PageBlock vector into nil buffer")
	}
	if _, ok := ResolveProfile(int(profile)); !ok {
		return fmt.Errorf("tlprofile: unsupported exact profile %d", profile)
	}
	if len(blocks) > layerCodecMaxVectorElements {
		return fmt.Errorf("tlprofile: PageBlock vector length %d exceeds hard limit %d", len(blocks), layerCodecMaxVectorElements)
	}

	return layerCodecEncodeAtomic(profile, out, func() error {
		state, err := layerCodecDescend(profile, "encode PageBlock vector", &layerCodecState{})
		if err != nil {
			return err
		}
		out.PutVectorHeader(len(blocks))
		for i, block := range blocks {
			if block == nil {
				return fmt.Errorf("tlprofile: encode nil PageBlock at index %d", i)
			}
			if err := layerEncodeClassPageBlockBody(profile, block, out, &state); err != nil {
				return fmt.Errorf("tlprofile: encode PageBlock at index %d: %w", i, err)
			}
		}
		return nil
	})
}

// DecodePageBlockVector decodes one complete boxed Vector<PageBlock> using the
// exact wire grammar selected by profile. It applies the same generated
// allocation/depth limits as the object codec and consumes in only on success.
func DecodePageBlockVector(profile Profile, in *bin.Buffer, limits Limits) ([]tg.PageBlockClass, error) {
	if in == nil {
		return nil, errors.New("tlprofile: decode PageBlock vector from nil buffer")
	}
	if _, ok := ResolveProfile(int(profile)); !ok {
		return nil, fmt.Errorf("tlprofile: unsupported exact profile %d", profile)
	}

	cursor := &bin.Buffer{Buf: in.Raw()}
	state, err := newLayerCodecDecodeState(profile, cursor.Len(), layerCodecDecodeLimits{
		maxWireBytes:         limits.MaxWireBytes,
		maxVectorElements:    limits.MaxVectorElements,
		maxAggregateElements: limits.MaxAggregateElements,
		maxDepth:             limits.MaxDepth,
	})
	if err != nil {
		return nil, err
	}
	vectorState, err := layerCodecDescend(profile, "decode PageBlock vector", state)
	if err != nil {
		return nil, err
	}
	length, err := layerDecodeVectorLength(profile, nil, cursor, true, &vectorState)
	if err != nil {
		return nil, fmt.Errorf("tlprofile: decode PageBlock vector header: %w", err)
	}
	blocks := make([]tg.PageBlockClass, 0, length)
	for i := 0; i < length; i++ {
		block, err := layerDecodeClassPageBlock(profile, cursor, &vectorState)
		if err != nil {
			return nil, fmt.Errorf("tlprofile: decode PageBlock at index %d: %w", i, err)
		}
		blocks = append(blocks, block)
	}
	if cursor.Len() != 0 {
		return nil, fmt.Errorf("tlprofile: PageBlock vector left %d trailing bytes", cursor.Len())
	}
	in.Skip(in.Len())
	return blocks, nil
}
