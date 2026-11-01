package files

import (
	"context"
	"fmt"

	"go.uber.org/zap"

	"telesrv/internal/domain"
)

// CreateEncryptedFileFromUpload assembles uploaded parts into a secret chat file blob and mints
// an EncryptedFile snapshot. Blind relay: the content is client-encrypted bytes, not parsed, not
// classified, not thumbnailed; the blob lands at location_key "enc:<id>" (reusing BlobBackend,
// downloads go through inputEncryptedFileLocation → the same key). access_hash is not strictly
// checked (following the existing dev posture for media, relying on non-enumerable ids). Metadata
// persistence is the caller's responsibility (the rpc layer via SecretChats.PutEncryptedFile).
func (s *Service) CreateEncryptedFileFromUpload(ctx context.Context, file domain.UploadedFileRef, keyFingerprint int) (domain.EncryptedFileRef, error) {
	body, err := s.assembleUploadBlob(ctx, file.OwnerUserID, file.FileID, file.Parts)
	if err != nil {
		return domain.EncryptedFileRef{}, err
	}
	if body.Size == 0 {
		return domain.EncryptedFileRef{}, domain.ErrDocumentInvalid
	}
	id := randomID()
	if err := s.media.PutFileBlob(ctx, domain.FileBlob{
		LocationKey: fmt.Sprintf("enc:%d", id),
		Backend:     domain.MediaBackend(s.blobs.Name()),
		ObjectKey:   body.ObjectKey,
		Size:        body.Size,
		SHA256:      body.SHA256,
		MimeType:    "application/octet-stream",
	}); err != nil {
		return domain.EncryptedFileRef{}, err
	}
	if err := s.cleanupUploadParts(ctx, file.OwnerUserID, file.FileID); err != nil {
		s.log.Warn("cleanup encrypted file upload parts failed",
			zap.Int64("owner_user_id", file.OwnerUserID),
			zap.Int64("file_id", file.FileID),
			zap.Int64("encrypted_file_id", id),
			zap.Error(err))
	}
	return domain.EncryptedFileRef{
		ID:             id,
		AccessHash:     randomID(),
		Size:           body.Size,
		DCID:           s.dc,
		KeyFingerprint: keyFingerprint,
	}, nil
}
