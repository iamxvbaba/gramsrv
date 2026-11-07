package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"

	"telesrv/internal/domain"
)

// ensureSharedWidgetDefinitionTx persists the shared definition of a bot web
// widget whose id and owner were assigned while resolving the input media. It
// runs inside the message send transaction so a row exists only once the message
// actually commits: uploadMedia, setInlineBotResults before its query check and
// sends that fail later never reach here and therefore leak no rows.
//
// The full definition is carried on the media snapshot (Payload is the widget
// markup), so no extra request field is needed. A repeated send of the same
// media is a no-op.
func ensureSharedWidgetDefinitionTx(ctx context.Context, tx pgx.Tx, media *domain.MessageMedia) error {
	if media == nil || media.Widget == nil {
		return nil
	}
	widget := media.Widget
	if !widget.SharedWidget || widget.WidgetID == 0 || widget.OwnerBotID == 0 {
		return nil
	}
	_, err := tx.Exec(ctx, `
INSERT INTO bot_widgets (widget_id, bot_user_id, html, payload, width, height, allow_queries, public_data, public_data_hash, cache_time)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, 0)
ON CONFLICT (widget_id) DO NOTHING`,
		widget.WidgetID, widget.OwnerBotID, string(widget.Payload), widget.Payload,
		widget.Width, widget.Height, widget.AllowQueries, widget.PublicData, widget.PublicDataHash)
	return err
}
