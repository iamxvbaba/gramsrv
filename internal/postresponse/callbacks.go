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
	run := Take(ctx)
	if run != nil {
		run()
	}
}

// Take transfers ownership of every currently registered callback to the caller.
// The returned function is idempotent and may safely outlive the request context.
// MTProto uses this to release a business worker after admitting rpc_result while
// still delaying follow-up updates until the result reaches the reliable stream.
func Take(ctx context.Context) func() {
	cbs, ok := ctx.Value(callbacksKey{}).(*callbacks)
	if !ok || cbs == nil {
		return nil
	}
	cbs.mu.Lock()
	list := append([]callback(nil), cbs.list...)
	cbs.list = nil
	cbs.mu.Unlock()
	if len(list) == 0 {
		return nil
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			for _, cb := range list {
				cb()
			}
		})
	}
}
