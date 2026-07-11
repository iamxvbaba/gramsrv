package files

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"

	"go.uber.org/zap"

	"telesrv/internal/domain"
)

const uploadedMediaIntentVersion = 1

type uploadedMediaIntent struct {
	Version int                      `json:"version"`
	Kind    domain.UploadedMediaKind `json:"kind"`
	File    domain.UploadedFileRef   `json:"file"`
	Spec    *domain.DocumentSpec     `json:"spec,omitempty"`
}

func uploadedMediaIntentHash(kind domain.UploadedMediaKind, file domain.UploadedFileRef, spec *domain.DocumentSpec) ([]byte, error) {
	payload, err := json.Marshal(uploadedMediaIntent{
		Version: uploadedMediaIntentVersion,
		Kind:    kind,
		File:    file,
		Spec:    spec,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal uploaded media intent: %w", err)
	}
	sum := sha256.Sum256(payload)
	return sum[:], nil
}

func sameUploadedMediaReceipt(receipt domain.UploadedMediaReceipt, kind domain.UploadedMediaKind, intentHash []byte) bool {
	return receipt.Kind == kind && len(intentHash) == sha256.Size && bytes.Equal(receipt.IntentHash, intentHash)
}

func (s *Service) replayUploadedPhoto(ctx context.Context, file domain.UploadedFileRef, intentHash []byte) (domain.Photo, bool, error) {
	receipt, found, err := s.media.GetUploadedMediaReceipt(ctx, file.OwnerUserID, file.FileID)
	if err != nil || !found {
		return domain.Photo{}, false, err
	}
	if !sameUploadedMediaReceipt(receipt, domain.UploadedMediaPhoto, intentHash) {
		return domain.Photo{}, false, domain.ErrFilePartsInvalid
	}
	photo, found, err := s.media.GetPhoto(ctx, receipt.MediaID)
	if err != nil {
		return domain.Photo{}, false, err
	}
	if !found {
		return domain.Photo{}, false, fmt.Errorf("uploaded photo receipt %d/%d references missing photo %d", file.OwnerUserID, file.FileID, receipt.MediaID)
	}
	s.cleanupMaterializedUpload(ctx, file, "photo replay")
	return photo, true, nil
}

func (s *Service) replayUploadedDocument(ctx context.Context, file domain.UploadedFileRef, intentHash []byte) (domain.Document, bool, error) {
	receipt, found, err := s.media.GetUploadedMediaReceipt(ctx, file.OwnerUserID, file.FileID)
	if err != nil || !found {
		return domain.Document{}, false, err
	}
	if !sameUploadedMediaReceipt(receipt, domain.UploadedMediaDocument, intentHash) {
		return domain.Document{}, false, domain.ErrFilePartsInvalid
	}
	doc, found, err := s.media.GetDocument(ctx, receipt.MediaID)
	if err != nil {
		return domain.Document{}, false, err
	}
	if !found {
		return domain.Document{}, false, fmt.Errorf("uploaded document receipt %d/%d references missing document %d", file.OwnerUserID, file.FileID, receipt.MediaID)
	}
	s.cleanupMaterializedUpload(ctx, file, "document replay")
	return doc, true, nil
}

func (s *Service) commitUploadedMediaReceipt(ctx context.Context, file domain.UploadedFileRef, kind domain.UploadedMediaKind, intentHash []byte, mediaID int64) (domain.UploadedMediaReceipt, error) {
	receipt, _, err := s.media.PutUploadedMediaReceipt(ctx, domain.UploadedMediaReceipt{
		OwnerUserID: file.OwnerUserID,
		FileID:      file.FileID,
		IntentHash:  intentHash,
		Kind:        kind,
		MediaID:     mediaID,
	})
	if err != nil {
		return domain.UploadedMediaReceipt{}, err
	}
	if !sameUploadedMediaReceipt(receipt, kind, intentHash) {
		return domain.UploadedMediaReceipt{}, domain.ErrFilePartsInvalid
	}
	return receipt, nil
}

func (s *Service) cleanupMaterializedUpload(ctx context.Context, file domain.UploadedFileRef, reason string) {
	if err := s.cleanupUploadParts(ctx, file.OwnerUserID, file.FileID); err != nil {
		s.log.Warn("cleanup materialized upload parts failed",
			zap.String("reason", reason),
			zap.Int64("owner_user_id", file.OwnerUserID),
			zap.Int64("file_id", file.FileID),
			zap.Error(err),
		)
	}
}
