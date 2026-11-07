package rpc

import (
	"context"
	"hash/fnv"

	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap"

	"telesrv/internal/domain"
)

const (
	// maxWidgetHTMLBytes bounds the inline widget markup accepted on send.
	maxWidgetHTMLBytes = 128 << 10
	// maxWidgetDataBytes bounds public_data / chat_data snapshots.
	maxWidgetDataBytes = 16 << 10
	// maxWidgetDimension bounds the client-requested rendering box.
	maxWidgetDimension = 10000
)

// widgetDataHash is the server-computed public_data hash (messageMediaWidget.public_data_hash).
// Clients echo it back to messages.getWidgetPublicData and get widgetPublicDataNotModified on a match.
func widgetDataHash(data string) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(data))
	return int64(h.Sum64())
}

// domainWidgetFromInline converts inputMediaWidget: the markup is stored on the
// message, and (when shared_widget) as a reusable widget definition. Widgets
// may only be authored by bots: client-rendered HTML must not turn an ordinary
// account into a query target that receives other users' data.
func (r *Router) domainWidgetFromInline(ctx context.Context, userID int64, in *tg.InputMediaWidget) (*domain.MessageMedia, error) {
	if !r.userIsBot(ctx, userID) {
		return nil, mediaInvalidErr()
	}
	if in == nil || len(in.HTML) == 0 || len(in.HTML) > maxWidgetHTMLBytes {
		return nil, mediaInvalidErr()
	}
	if in.Width < 0 || in.Height < 0 || in.Width > maxWidgetDimension || in.Height > maxWidgetDimension {
		return nil, mediaInvalidErr()
	}
	widget := &domain.MessageWidget{
		AllowQueries: in.AllowQueries,
		SharedWidget: in.SharedWidget,
		CanQuery:     in.AllowQueries,
		Width:        in.Width,
		Height:       in.Height,
		HTML:         in.HTML,
		Payload:      []byte(in.HTML),
	}
	if publicData, ok := in.GetPublicData(); ok {
		if len(publicData) > maxWidgetDataBytes {
			return nil, mediaInvalidErr()
		}
		widget.PublicData = publicData
		widget.PublicDataHash = widgetDataHash(publicData)
	}
	if chatData, ok := in.GetChatData(); ok {
		if len(chatData) > maxWidgetDataBytes {
			return nil, mediaInvalidErr()
		}
		widget.ChatData = chatData
	}
	if in.SharedWidget {
		if r.deps.Widgets == nil {
			return nil, notImplementedErr()
		}
		id, err := r.deps.Widgets.CreateWidget(ctx, domain.BotWidget{
			BotUserID:      userID,
			HTML:           in.HTML,
			Payload:        append([]byte(nil), widget.Payload...),
			Width:          in.Width,
			Height:         in.Height,
			AllowQueries:   in.AllowQueries,
			PublicData:     widget.PublicData,
			PublicDataHash: widget.PublicDataHash,
		})
		if err != nil {
			r.log.Warn("create widget failed", zap.Error(err))
			return nil, internalErr()
		}
		widget.WidgetID = id
	}
	return &domain.MessageMedia{Kind: domain.MessageMediaKindWidget, Widget: widget}, nil
}

// domainWidgetFromID converts inputMediaWidgetID: the definition is resolved by
// its server-assigned widget id and copied onto the message as is_copy. Only
// bots may send widget media.
func (r *Router) domainWidgetFromID(ctx context.Context, userID int64, in *tg.InputMediaWidgetID) (*domain.MessageMedia, error) {
	if !r.userIsBot(ctx, userID) {
		return nil, mediaInvalidErr()
	}
	if in == nil || in.WidgetID == 0 {
		return nil, mediaInvalidErr()
	}
	if r.deps.Widgets == nil {
		return nil, notImplementedErr()
	}
	def, found, err := r.deps.Widgets.GetWidget(ctx, in.WidgetID)
	if err != nil {
		r.log.Warn("load widget failed", zap.Error(err))
		return nil, internalErr()
	}
	if !found {
		return nil, mediaInvalidErr()
	}
	widget := &domain.MessageWidget{
		WidgetID:       def.WidgetID,
		AllowQueries:   def.AllowQueries,
		SharedWidget:   true,
		IsCopy:         true,
		CanQuery:       def.AllowQueries,
		Width:          def.Width,
		Height:         def.Height,
		HTML:           def.HTML,
		Payload:        append([]byte(nil), def.Payload...),
		PublicData:     def.PublicData,
		PublicDataHash: def.PublicDataHash,
	}
	if chatData, ok := in.GetChatData(); ok {
		if len(chatData) > maxWidgetDataBytes {
			return nil, mediaInvalidErr()
		}
		widget.ChatData = chatData
	}
	return &domain.MessageMedia{Kind: domain.MessageMediaKindWidget, Widget: widget}, nil
}

// tgWidgetMedia projects a stored widget snapshot to TL messageMediaWidget.
func tgWidgetMedia(w domain.MessageWidget) tg.MessageMediaClass {
	out := &tg.MessageMediaWidget{
		IsCopy:          w.IsCopy,
		CanQuery:        w.CanQuery,
		BackgroundQuery: w.BackgroundQuery,
		Width:           w.Width,
		Height:          w.Height,
		Payload:         append([]byte(nil), w.Payload...),
	}
	if w.WidgetID != 0 {
		out.SetWidgetID(w.WidgetID)
	}
	if w.PublicData != "" {
		out.SetPublicData(w.PublicData)
		out.SetPublicDataHash(w.PublicDataHash)
	}
	if w.ChatData != "" {
		out.SetChatData(w.ChatData)
	}
	return out
}
