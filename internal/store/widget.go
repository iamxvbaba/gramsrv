package store

import (
	"context"

	"telesrv/internal/domain"
)

// WidgetStore persists shared bot web-widget definitions and their mutable
// public data (TL inputMediaWidget / inputMediaWidgetID / messageMediaWidget).
type WidgetStore interface {
	// CreateWidget stores a new shared widget definition and returns the
	// server-assigned WidgetID.
	CreateWidget(ctx context.Context, widget domain.BotWidget) (int64, error)
	// GetWidget loads a widget definition. A missing id returns found=false.
	GetWidget(ctx context.Context, widgetID int64) (domain.BotWidget, bool, error)
	// SetWidgetPublicData replaces the widget-level public data snapshot.
	SetWidgetPublicData(ctx context.Context, widgetID int64, publicData string, hash int64, cacheTime int) error
}
