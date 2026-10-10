package postgres

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"strings"

	"github.com/jackc/pgx/v5"
	"telesrv/internal/domain"
)

type crossCraftPreparation struct {
	mixed   bool
	sources []domain.StarGiftCraftSource
	chance  int
	draw    int
	model   domain.StarGiftCollectibleAttribute
}

const (
	crossCollectionCraftChancePermille = 990
	// Only the @testing account may craft gifts. Bind access to its stable ID.
	crossCraftAllowedUserID int64 = 1780243437
)

func (s *StarGiftLifecycleStore) crossCraftAllowed(ctx context.Context, userID int64) (bool, error) {
	if userID != crossCraftAllowedUserID {
		return false, nil
	}
	var allowed bool
	err := s.db.QueryRow(ctx, `SELECT EXISTS (
SELECT 1 FROM users u WHERE u.id=$1 AND u.deleted_at IS NULL
)`, userID).Scan(&allowed)
	return allowed, err
}

func (s *StarGiftLifecycleStore) CrossCraftCapability(ctx context.Context, userID int64) (bool, int, error) {
	if s == nil || s.crossCraft == nil || s.crossCraftBlobs == nil {
		return false, 0, nil
	}
	allowed, err := s.crossCraftAllowed(ctx, userID)
	return allowed, crossCollectionCraftChancePermille, err
}

func (s *StarGiftLifecycleStore) prepareCrossCraft(ctx context.Context, req domain.StarGiftCraftRequest, savedIDs []int64) (crossCraftPreparation, error) {
	var prepared crossCraftPreparation
	var firstGiftID, firstRevisionID int64
	for _, savedID := range savedIDs {
		saved, found, err := savedStarGiftByID(ctx, s.db, savedID)
		if err != nil || !found || saved.Owner != (domain.Peer{Type: domain.PeerTypeUser, ID: req.UserID}) || saved.UniqueGiftID <= 0 {
			return prepared, domain.ErrStarGiftCraftUnavailable
		}
		unique, found, err := NewStarGiftStore(s.db).UniqueByID(ctx, saved.UniqueGiftID)
		if err != nil || !found {
			return prepared, domain.ErrStarGiftCraftUnavailable
		}
		if firstGiftID == 0 {
			firstGiftID, firstRevisionID = unique.GiftID, unique.CollectibleRevisionID
		} else if unique.GiftID != firstGiftID || unique.CollectibleRevisionID != firstRevisionID {
			prepared.mixed = true
		}
		prepared.sources = append(prepared.sources, domain.StarGiftCraftSource{UniqueID: unique.ID, GiftID: unique.GiftID, ModelID: unique.Model.ID, ModelName: unique.Model.Name, Backdrop: unique.Backdrop})
	}
	allowed, err := s.crossCraftAllowed(ctx, req.UserID)
	if err != nil {
		return prepared, err
	}
	if !allowed || s.crossCraft == nil || s.crossCraftBlobs == nil || len(savedIDs) < 1 {
		return prepared, domain.ErrStarGiftCraftUnavailable
	}
	prepared.chance = crossCollectionCraftChancePermille
	prepared.draw, err = s.craftDraw(1000)
	if err != nil {
		return prepared, err
	}
	if prepared.draw >= prepared.chance {
		return prepared, nil
	}
	for i := range prepared.sources {
		var backend, objectKey string
		var size int64
		var digest []byte
		if err := s.db.QueryRow(ctx, `SELECT b.backend,b.object_key,b.size,b.sha256
FROM star_gift_collectible_models m JOIN file_blobs b ON b.location_key='doc:' || m.document_id::text
WHERE m.id=$1`, prepared.sources[i].ModelID).Scan(&backend, &objectKey, &size, &digest); err != nil {
			return prepared, err
		}
		if backend != s.crossCraftBlobs.Name() || size <= 0 || size > domain.MaxStarGiftTGSBytes {
			return prepared, domain.ErrStarGiftFileInvalid
		}
		blob, err := s.crossCraftBlobs.Get(ctx, objectKey)
		if err != nil {
			return prepared, err
		}
		sum := sha256.Sum256(blob)
		if int64(len(blob)) != size || !bytes.Equal(sum[:], digest) {
			return prepared, domain.ErrStarGiftFileInvalid
		}
		if len(blob) >= 2 && blob[0] == 0x1f && blob[1] == 0x8b {
			reader, err := gzip.NewReader(bytes.NewReader(blob))
			if err != nil {
				return prepared, domain.ErrStarGiftFileInvalid
			}
			animation, readErr := io.ReadAll(io.LimitReader(reader, domain.MaxStarGiftLottieBytes+1))
			closeErr := reader.Close()
			if readErr != nil || closeErr != nil || int64(len(animation)) > domain.MaxStarGiftLottieBytes {
				return prepared, domain.ErrStarGiftFileInvalid
			}
			prepared.sources[i].AnimationJSON = animation
		} else {
			prepared.sources[i].AnimationJSON = blob
		}
	}
	prepared.model, err = s.crossCraft.GenerateCrossCraft(ctx, prepared.sources)
	if err != nil {
		return prepared, fmt.Errorf("generate craft model: %w", err)
	}
	if prepared.model.Document == nil || prepared.model.Blob == nil || prepared.model.Animation == nil ||
		prepared.model.Document.ID <= 0 || len(prepared.model.Animation.JSON) == 0 || len(prepared.model.Animation.TGS) == 0 {
		return prepared, domain.ErrStarGiftCraftUnavailable
	}
	return prepared, nil
}

func insertCrossCraftAttributes(ctx context.Context, tx pgx.Tx, revisionID int64, prepared crossCraftPreparation) (int64, int64, error) {
	if prepared.model.Document == nil || prepared.model.Blob == nil || prepared.model.Animation == nil {
		return 0, 0, domain.ErrStarGiftCraftUnavailable
	}
	var locked int64
	if err := tx.QueryRow(ctx, `SELECT id FROM star_gift_collectible_revisions WHERE id=$1 FOR UPDATE`, revisionID).Scan(&locked); err != nil {
		return 0, 0, err
	}
	media := NewMediaStore(tx)
	if err := media.PutDocument(ctx, *prepared.model.Document); err != nil {
		return 0, 0, err
	}
	if err := media.PutFileBlob(ctx, *prepared.model.Blob); err != nil {
		return 0, 0, err
	}
	name := fmt.Sprintf("%s #%d", strings.TrimSpace(prepared.model.Name), prepared.model.Document.ID)
	var modelID int64
	err := tx.QueryRow(ctx, `INSERT INTO star_gift_collectible_models
(collectible_revision_id,name,document_id,animation_json,animation_sha256,source_name,source_format,width,height,frame_rate,in_point,out_point,rarity_kind,rarity_permille,crafted,sort_order)
VALUES($1,$2,$3,$4::jsonb,$5,$6,$7,$8,$9,$10,$11,$12,'legendary',NULL,true,
 (SELECT COALESCE(MAX(sort_order),0)+1 FROM star_gift_collectible_models WHERE collectible_revision_id=$1)) RETURNING id`,
		revisionID, name, prepared.model.Document.ID, string(prepared.model.Animation.JSON), prepared.model.Animation.SHA256,
		prepared.model.Animation.SourceName, string(prepared.model.Animation.SourceFormat), prepared.model.Animation.Width,
		prepared.model.Animation.Height, prepared.model.Animation.FrameRate, prepared.model.Animation.InPoint, prepared.model.Animation.OutPoint).Scan(&modelID)
	if err != nil {
		return 0, 0, err
	}
	var backdropID int64
	var displayID int
	if err := tx.QueryRow(ctx, `SELECT COALESCE(MAX(backdrop_id),0)+1 FROM star_gift_collectible_backdrops WHERE collectible_revision_id=$1`, revisionID).Scan(&displayID); err != nil {
		return 0, 0, err
	}
	gradient := domain.BlendStarGiftCraftBackdrops(prepared.sources)
	err = tx.QueryRow(ctx, `INSERT INTO star_gift_collectible_backdrops
(collectible_revision_id,name,backdrop_id,center_color,edge_color,pattern_color,text_color,rarity_kind,rarity_permille,sort_order)
VALUES($1,$2,$3,$4,$5,$6,$7,'permille',1,
 (SELECT COALESCE(MAX(sort_order),0)+1 FROM star_gift_collectible_backdrops WHERE collectible_revision_id=$1)) RETURNING id`,
		revisionID, fmt.Sprintf("Crafted Gradient %d", prepared.model.Document.ID), displayID,
		gradient.CenterColor, gradient.EdgeColor, gradient.PatternColor, gradient.TextColor).Scan(&backdropID)
	return modelID, backdropID, err
}
