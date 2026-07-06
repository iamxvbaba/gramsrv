package mtprotoedge

import (
	"context"
	"errors"
	"time"
)

// ErrInboundRPCQueueFull 表示单连接 RPC 队列已满。
var ErrInboundRPCQueueFull = errors.New("inbound rpc queue full")

// maxInflightRPCBytes 是单连接已入队未完成 inbound RPC body 的总字节上限。
// 队列除按条数(queueSize)限制外，再按字节预算兜底：对抗客户端发满大请求时按字节先拒绝。
const maxInflightRPCBytes = 32 << 20 // 32 MiB

// rpcCloseWaitTimeout 是连接关闭时等待 inbound RPC worker 退出的上限。
const rpcCloseWaitTimeout = 5 * time.Second

type inboundRPC struct {
	ctx        context.Context
	method     string
	enqueuedAt time.Time
	size       int
	run        func(context.Context) error
}

func (c *Conn) startInboundRPCScheduler(maxInflight, queueSize int, timeout time.Duration) {
	if c.metrics == nil {
		c.metrics = NopMetrics{}
	}
	if maxInflight <= 0 {
		maxInflight = 1
	}
	if queueSize <= 0 {
		queueSize = 1
	}
	rootCtx, cancel := context.WithCancel(context.Background())
	c.rpcQueue = make(chan inboundRPC, queueSize)
	c.rpcStop = make(chan struct{})
	c.rpcCancel = cancel
	c.rpcTimeout = timeout
	c.rpcRootCtx = rootCtx
	c.rpcMaxInflight = maxInflight
	// worker 懒启动：不在此处起 worker；首个 RPC 入队时由 ensureInboundRPCWorkers 起，
	// 避免握手后静默 / 纯推送目标连接白白钉住 maxInflight 个 goroutine。
}

// ensureInboundRPCWorkers 懒启动 maxInflight 个 RPC worker（仅一次），在 enqueueInboundRPC
// 入队成功后调用。从不发 RPC 的连接（半开 / 纯推送）由此完全不起 worker。
func (c *Conn) ensureInboundRPCWorkers() {
	c.rpcWorkersOnce.Do(func() {
		c.rpcWG.Add(c.rpcMaxInflight)
		for i := 0; i < c.rpcMaxInflight; i++ {
			go c.inboundRPCWorker(c.rpcRootCtx)
		}
	})
}

func (c *Conn) enqueueInboundRPC(ctx context.Context, task inboundRPC) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if c.rpcQueue == nil || c.rpcStop == nil {
		c.metrics.InboundRPCDropped(task.method, "scheduler_closed")
		return ErrConnClosed
	}
	task.ctx = ctx
	task.enqueuedAt = time.Now()
	select {
	case <-ctx.Done():
		c.metrics.InboundRPCDropped(task.method, "context_done")
		return ctx.Err()
	case <-c.rpcStop:
		c.metrics.InboundRPCDropped(task.method, "scheduler_closed")
		return ErrConnClosed
	default:
	}
	// 字节预算：先预扣 size，超 maxInflightRPCBytes 则回滚并拒绝（与条数上限并列的第二道闸）。
	if task.size > 0 {
		if c.inflightRPCBytes.Add(int64(task.size)) > maxInflightRPCBytes {
			c.inflightRPCBytes.Add(-int64(task.size))
			c.metrics.InboundRPCDropped(task.method, "byte_budget")
			return ErrInboundRPCQueueFull
		}
	}
	select {
	case c.rpcQueue <- task:
		c.ensureInboundRPCWorkers()
		c.metrics.InboundRPCQueued(task.method, len(c.rpcQueue), cap(c.rpcQueue))
		return nil
	case <-ctx.Done():
		c.releaseInflightRPCBytes(task.size)
		c.metrics.InboundRPCDropped(task.method, "context_done")
		return ctx.Err()
	case <-c.rpcStop:
		c.releaseInflightRPCBytes(task.size)
		c.metrics.InboundRPCDropped(task.method, "scheduler_closed")
		return ErrConnClosed
	default:
		c.releaseInflightRPCBytes(task.size)
		c.metrics.InboundRPCDropped(task.method, "queue_full")
		return ErrInboundRPCQueueFull
	}
}

// releaseInflightRPCBytes 归还字节预算。与 enqueueInboundRPC 的预扣严格配对：
// 入队失败时回滚、worker 执行完(runInboundRPC)或排空丢弃(drainInboundRPCQueue)时释放。
func (c *Conn) releaseInflightRPCBytes(size int) {
	if size > 0 {
		c.inflightRPCBytes.Add(-int64(size))
	}
}

func (c *Conn) inboundRPCWorker(rootCtx context.Context) {
	defer c.rpcWG.Done()
	for {
		select {
		case <-c.rpcStop:
			return
		default:
		}
		select {
		case task := <-c.rpcQueue:
			c.runInboundRPC(rootCtx, task)
		case <-c.rpcStop:
			return
		}
	}
}

func (c *Conn) runInboundRPC(rootCtx context.Context, task inboundRPC) {
	defer c.releaseInflightRPCBytes(task.size)
	queueWait := time.Since(task.enqueuedAt)
	c.metrics.InboundRPCStarted(task.method, queueWait)
	ctx := task.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	// 合并两个取消源（task.ctx 与 rootCtx）+ 超时为最少的 context 层数：
	// WithTimeout/WithCancel 的 cancel 直接作为 AfterFunc 回调，省掉单独的中间层。
	var cancel context.CancelFunc
	if c.rpcTimeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, c.rpcTimeout)
	} else {
		ctx, cancel = context.WithCancel(ctx)
	}
	defer cancel()
	stopRoot := context.AfterFunc(rootCtx, cancel)
	defer stopRoot()
	_ = task.run(ctx)
}

func (c *Conn) closeInboundRPCScheduler() {
	if c.rpcStop == nil {
		return
	}
	c.rpcClose.Do(func() {
		if c.rpcCancel != nil {
			c.rpcCancel()
		}
		close(c.rpcStop)
		// 抢占懒启动 Once：若 worker 尚未起，封住其启动，避免后续 ensureInboundRPCWorkers 的
		// rpcWG.Add 与下面的 rpcWG.Wait 并发（WaitGroup 误用）。Once 互斥保证 Add happens-before Wait。
		c.rpcWorkersOnce.Do(func() {})
		c.drainInboundRPCQueue()
		// 等 worker 退出，使关闭对 inbound 与 outbound（<-outboundDone）收敛对称；带超时防慢 handler 卡死。
		c.waitInboundWorkers(rpcCloseWaitTimeout)
	})
}

// waitInboundWorkers 等所有 inbound RPC worker 退出，最长 timeout。超时则放弃等待，
// worker 在其阻塞的底层调用返回后自行退出（rpcCancel 已发，最终收敛）。
func (c *Conn) waitInboundWorkers(timeout time.Duration) {
	done := make(chan struct{})
	go func() {
		c.rpcWG.Wait()
		close(done)
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
	}
}

func (c *Conn) drainInboundRPCQueue() {
	for {
		select {
		case task := <-c.rpcQueue:
			c.releaseInflightRPCBytes(task.size)
			c.metrics.InboundRPCDropped(task.method, "connection_closed")
		default:
			return
		}
	}
}
