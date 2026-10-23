package gen

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
)

const LayerPolicyVersion = 1

// LayerPolicyDocument is the strict, versioned on-disk policy consumed by
// gotdgen. Exact obligation keys already contain both source and target shape
// digests, so unchanged entries remain valid when a newer Layer is appended.
type LayerPolicyDocument struct {
	Version int                          `json:"version"`
	Entries []LayerObligationPolicyEntry `json:"entries"`
}

// ReadLayerPolicy decodes one strict JSON policy document.
func ReadLayerPolicy(r io.Reader) (LayerObligationPolicy, error) {
	decoder := json.NewDecoder(r)
	decoder.DisallowUnknownFields()
	var document LayerPolicyDocument
	if err := decoder.Decode(&document); err != nil {
		return LayerObligationPolicy{}, fmt.Errorf("gen: decode layer policy: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return LayerObligationPolicy{}, fmt.Errorf("gen: layer policy has multiple JSON values")
		}
		return LayerObligationPolicy{}, fmt.Errorf("gen: decode trailing layer policy data: %w", err)
	}
	if document.Version != LayerPolicyVersion {
		return LayerObligationPolicy{}, fmt.Errorf("gen: unsupported layer policy version %d", document.Version)
	}
	return LayerObligationPolicy{Entries: document.Entries}, nil
}

// LoadLayerPolicy reads a policy document from path.
func LoadLayerPolicy(path string) (LayerObligationPolicy, error) {
	f, err := os.Open(path)
	if err != nil {
		return LayerObligationPolicy{}, fmt.Errorf("gen: open layer policy: %w", err)
	}
	defer func() { _ = f.Close() }()
	return ReadLayerPolicy(f)
}
