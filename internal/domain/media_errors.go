package domain

import "errors"

// File-part upload and media-related business errors. The rpc layer maps these to the corresponding rpc_error (see internal/rpc/errors.go):
// file parts (part invalid/parts invalid/part too big), upload quota, photos and documents.
var (
	ErrFilePartInvalid     = errors.New("file part invalid")
	ErrFilePartsInvalid    = errors.New("file parts invalid")
	ErrFilePartTooBig      = errors.New("file part too big")
	ErrUploadQuotaExceeded = errors.New("upload quota exceeded")
	// ErrStorageFull means the configured storage capacity guard rejected a
	// write. It is distinct from per-account upload quota and must not be mapped
	// to FLOOD_WAIT: retrying cannot create disk or object-store capacity.
	ErrStorageFull     = errors.New("storage full")
	ErrPhotoInvalid    = errors.New("photo invalid")
	ErrDocumentInvalid = errors.New("document invalid")
)
