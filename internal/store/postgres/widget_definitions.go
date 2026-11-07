package postgres

import (
	"context"

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
	return sqlcgen.New(tx).CreateBotWidgetIfAbsent(ctx, sqlcgen.CreateBotWidgetIfAbsentParams{
		WidgetID: widget.WidgetID, BotUserID: widget.OwnerBotID, Html: string(widget.Payload),
		Payload: widget.Payload, Width: int32(widget.Width), Height: int32(widget.Height),
		AllowQueries: widget.AllowQueries, PublicData: widget.PublicData, PublicDataHash: widget.PublicDataHash,
	})
}
