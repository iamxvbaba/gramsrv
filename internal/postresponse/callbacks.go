package postresponse

import (
	"context"
	"sync"
)

type callback func()

type callbacksKey struct{}

type callbacks struct {
	mu   sync.Mutex
	list []callback
}

func WithCallbacks(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if _, ok := ctx.Value(callbacksKey{}).(*callbacks); ok {
		return ctx
	}
	return context.WithValue(ctx, callbacksKey{}, &callbacks{})
}

func Register(ctx context.Context, cb func()) bool {
	if cb == nil {
		return false
	}
	cbs, ok := ctx.Value(callbacksKey{}).(*callbacks)
	if !ok || cbs == nil {
		return false
	}
	cbs.mu.Lock()
	cbs.list = append(cbs.list, cb)
	cbs.mu.Unlock()
	return true
}

func Run(ctx context.Context) {
	cbs, ok := ctx.Value(callbacksKey{}).(*callbacks)
	if !ok || cbs == nil {
		return
	}
	cbs.mu.Lock()
	list := append([]callback(nil), cbs.list...)
	cbs.list = nil
	cbs.mu.Unlock()
	for _, cb := range list {
		cb()
	}
}
