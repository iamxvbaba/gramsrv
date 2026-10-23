package tlprofile

import (
	"errors"
	"fmt"

	"github.com/iamxvbaba/td/bin"
)

// FrozenObject is an immutable canonical semantic snapshot for proactive
// fan-out. Each target profile is encoded directly from the typed snapshot;
// canonical wire bytes are never reused or post-processed as historical wire.
type FrozenObject struct {
	object        bin.Object
	canonicalSize int
}

// FreezeObject deep-clones one canonical object through the generated
// canonical codec so caller-owned pointers and slices cannot mutate later
// profile encodes.
func FreezeObject(value bin.Object) (*FrozenObject, error) {
	if value == nil {
		return nil, errors.New("tlprofile: freeze nil canonical object")
	}
	var canonical bin.Buffer
	if err := value.Encode(&canonical); err != nil {
		return nil, fmt.Errorf("tlprofile: encode frozen canonical object: %w", err)
	}
	// The decoder advances only cursor's slice header. Generated string/bytes
	// decoders materialize owned fields, so it can consume the uniquely owned
	// canonical buffer directly without a second full-payload clone.
	cursor := &bin.Buffer{Buf: canonical.Raw()}
	clone, err := DecodeObject(ProfileCanonical, cursor, Limits{})
	if err != nil {
		return nil, fmt.Errorf("tlprofile: clone frozen canonical object: %w", err)
	}
	if cursor.Len() != 0 {
		return nil, fmt.Errorf("tlprofile: frozen canonical object left %d bytes", cursor.Len())
	}
	return &FrozenObject{object: clone, canonicalSize: canonical.Len()}, nil
}

func (f *FrozenObject) CanonicalSize() int {
	if f == nil {
		return 0
	}
	return f.canonicalSize
}

func (f *FrozenObject) Encode(profile Profile, out *bin.Buffer) error {
	if f == nil || f.object == nil {
		return errors.New("tlprofile: encode empty frozen object")
	}
	return EncodeObject(profile, f.object, out)
}
