package rpc

import (
	"context"
	"fmt"
	"time"

	"go.uber.org/zap"

	"github.com/gotd/td/bin"
)

// dispatchCompat handles explicitly allowlisted legacy TL constructors that are
// still emitted by supported clients but are absent from gotd's pinned schema.
func (r *Router) dispatchCompat(ctx context.Context, b *bin.Buffer, id uint32) (bin.Encoder, bool, error) {
	start := time.Now()
	var (
		enc  bin.Encoder
		name string
		err  error
	)

	switch id {
	case legacyLangpackGetLanguagesTypeID:
		name = "langpack.getLanguages#800fd57d"
		enc, err = r.handleLegacyLangpackGetLanguages(ctx, b)
	default:
		return nil, false, nil
	}

	fields := append([]zap.Field{
		zap.String("method", name),
		zap.String("type_id", fmt.Sprintf("%#x", id)),
		zap.Bool("compat", true),
		zap.Duration("dur", time.Since(start)),
	}, r.contextLogFields(ctx)...)
	if err != nil {
		fields = append(fields, zap.Error(err))
		r.log.Info("RPC compat handled", fields...)
	} else {
		r.log.Debug("RPC compat handled", fields...)
	}
	return enc, true, err
}
