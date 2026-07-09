package rpc

import (
	"context"
	"sync"
	"time"
)

type botAPIUpdateNotifier struct {
	mu      sync.Mutex
	version map[int64]uint64
	waiters map[int64]map[chan struct{}]struct{}
}

func newBotAPIUpdateNotifier() *botAPIUpdateNotifier {
	return &botAPIUpdateNotifier{
		version: make(map[int64]uint64),
		waiters: make(map[int64]map[chan struct{}]struct{}),
	}
}

func (n *botAPIUpdateNotifier) current(botID int64) uint64 {
	if n == nil || botID == 0 {
		return 0
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.version[botID]
}

func (n *botAPIUpdateNotifier) wait(ctx context.Context, botID int64, version uint64, timeout time.Duration) bool {
	if n == nil || botID == 0 || timeout <= 0 {
		return false
	}
	ch := make(chan struct{})
	n.mu.Lock()
	if n.version[botID] != version {
		n.mu.Unlock()
		return true
	}
	waiters := n.waiters[botID]
	if waiters == nil {
		waiters = make(map[chan struct{}]struct{})
		n.waiters[botID] = waiters
	}
	waiters[ch] = struct{}{}
	n.mu.Unlock()

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	defer n.remove(botID, ch)
	select {
	case <-ch:
		return true
	case <-ctx.Done():
		return false
	case <-timer.C:
		return false
	}
}

func (n *botAPIUpdateNotifier) notify(botID int64) {
	if n == nil || botID == 0 {
		return
	}
	n.mu.Lock()
	n.version[botID]++
	waiters := n.waiters[botID]
	delete(n.waiters, botID)
	n.mu.Unlock()
	for ch := range waiters {
		close(ch)
	}
}

func (n *botAPIUpdateNotifier) remove(botID int64, ch chan struct{}) {
	if n == nil || botID == 0 || ch == nil {
		return
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	waiters := n.waiters[botID]
	if waiters == nil {
		return
	}
	delete(waiters, ch)
	if len(waiters) == 0 {
		delete(n.waiters, botID)
	}
}

func (r *Router) BotAPIUpdateWaitVersion(botID int64) uint64 {
	if r == nil {
		return 0
	}
	return r.botAPIUpdates.current(botID)
}

func (r *Router) WaitBotAPIUpdate(ctx context.Context, botID int64, version uint64, timeout time.Duration) bool {
	if r == nil {
		return false
	}
	return r.botAPIUpdates.wait(ctx, botID, version, timeout)
}

func (r *Router) notifyBotAPIUpdate(botID int64) {
	if r == nil {
		return
	}
	r.botAPIUpdates.notify(botID)
}
