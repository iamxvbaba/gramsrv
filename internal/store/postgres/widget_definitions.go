package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"telesrv/internal/domain"
	"telesrv/internal/store/postgres/sqlcgen"
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
	queries := sqlcgen.New(tx)
	rows, err := queries.CreateBotWidgetIfAbsent(ctx, sqlcgen.CreateBotWidgetIfAbsentParams{
		WidgetID: widget.WidgetID, BotUserID: widget.OwnerBotID,
		Payload: widget.Payload, Width: int32(widget.Width), Height: int32(widget.Height),
		AllowQueries: widget.AllowQueries, PublicData: widget.PublicData, PublicDataHash: widget.PublicDataHash,
	})
	if err != nil {
		return err
	}
	if rows == 0 {
		// The id already exists: only the same bot's definition may be reused.
		existing, err := queries.GetBotWidget(ctx, widget.WidgetID)
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrWidgetNotFound
		}
		if err != nil {
			return err
		}
		if existing.BotUserID != widget.OwnerBotID {
			return domain.ErrWidgetNotFound
		}
	}
	return nil
}
