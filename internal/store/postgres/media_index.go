package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"telesrv/internal/domain"
)

// Shared media index maintenance (migration 0118). The single source of truth for categories is
// domain.ClassifyMediaCategories; the write path maintains it only at the two points "create" and
// "edit changes media", while deletion is handled by the read queries joining against deleted and
// is not maintained here.

// insertChannelMediaIndexTx writes index rows for a channel message by its media categories (no-op if there are no categories).
func insertChannelMediaIndexTx(ctx context.Context, tx pgx.Tx, channelID int64, id, date int, media *domain.MessageMedia, entities []domain.MessageEntity) error {
	for _, c := range domain.ClassifyMediaCategories(media, entities) {
		if _, err := tx.Exec(ctx, `
INSERT INTO channel_message_media (channel_id, id, category, message_date)
VALUES ($1,$2,$3,$4)
ON CONFLICT (channel_id, id, category) DO NOTHING`, channelID, id, int16(c), date); err != nil {
			return fmt.Errorf("insert channel media index: %w", err)
		}
	}
	return nil
}

// deleteChannelMediaIndexTx clears all index rows of a channel message (delete then insert before editing media).
func deleteChannelMediaIndexTx(ctx context.Context, tx pgx.Tx, channelID int64, id int) error {
	if _, err := tx.Exec(ctx, `DELETE FROM channel_message_media WHERE channel_id = $1 AND id = $2`, channelID, id); err != nil {
		return fmt.Errorf("delete channel media index: %w", err)
	}
	return nil
}

// replaceChannelMediaIndexTx rebuilds the index rows after an edit replaces the media (the categories may change).
func replaceChannelMediaIndexTx(ctx context.Context, tx pgx.Tx, channelID int64, id, date int, media *domain.MessageMedia, entities []domain.MessageEntity) error {
	if err := deleteChannelMediaIndexTx(ctx, tx, channelID, id); err != nil {
		return err
	}
	return insertChannelMediaIndexTx(ctx, tx, channelID, id, date, media, entities)
}

// insertMessageBoxMediaIndexTx writes index rows for a private owner box by its media categories.
func insertMessageBoxMediaIndexTx(ctx context.Context, tx pgx.Tx, ownerUserID, peerID int64, boxID, date int, media *domain.MessageMedia, entities []domain.MessageEntity) error {
	for _, c := range domain.ClassifyMediaCategories(media, entities) {
		if _, err := tx.Exec(ctx, `
INSERT INTO message_box_media (owner_user_id, box_id, peer_id, category, message_date)
VALUES ($1,$2,$3,$4,$5)
ON CONFLICT (owner_user_id, box_id, category) DO NOTHING`, ownerUserID, boxID, peerID, int16(c), date); err != nil {
			return fmt.Errorf("insert message box media index: %w", err)
		}
	}
	return nil
}

// deleteMessageBoxMediaIndexTx clears all index rows of a private owner box.
func deleteMessageBoxMediaIndexTx(ctx context.Context, tx pgx.Tx, ownerUserID int64, boxID int) error {
	if _, err := tx.Exec(ctx, `DELETE FROM message_box_media WHERE owner_user_id = $1 AND box_id = $2`, ownerUserID, boxID); err != nil {
		return fmt.Errorf("delete message box media index: %w", err)
	}
	return nil
}

// replaceMessageBoxMediaIndexTx rebuilds the index rows after an edit replaces the media.
func replaceMessageBoxMediaIndexTx(ctx context.Context, tx pgx.Tx, ownerUserID, peerID int64, boxID, date int, media *domain.MessageMedia, entities []domain.MessageEntity) error {
	if err := deleteMessageBoxMediaIndexTx(ctx, tx, ownerUserID, boxID); err != nil {
		return err
	}
	return insertMessageBoxMediaIndexTx(ctx, tx, ownerUserID, peerID, boxID, date, media, entities)
}
