package donations

import (
	"context"
	"errors"
	"time"

	"go.uber.org/zap"

	"telesrv/internal/domain"
)

// chainRefreshInterval is how often the leader re-reads the chain registry, so
// an operator enabling, disabling or re-pointing a chain in the admin panel
// takes effect without restarting Core.
const chainRefreshInterval = time.Minute

// watcherRestartDelay spaces out restarts of a watcher that stopped on its own
// (an unreachable RPC endpoint, a chain ID mismatch).
const watcherRestartDelay = 30 * time.Second

type runningWatcher struct {
	cancel  context.CancelFunc
	rpcURL  string
	chainID int64
	done    chan struct{}
}

// RunLeader is the singleton half of crypto donations: it provisions the wallet
// if none exists yet and supervises one watcher per enabled, watchable chain
// until ctx ends. v2 (decision D7) runs it only on the Core that holds the
// donations lease; every other Core still serves /deposit from the shared key.
func (s *Service) RunLeader(ctx context.Context, pollInterval time.Duration, log *zap.Logger) error {
	if s == nil || s.store == nil || !s.keyConfigured {
		return domain.ErrDonationWalletNotConfigured
	}
	if log == nil {
		log = zap.NewNop()
	}
	if _, err := s.EnsureWallet(ctx); err == nil {
		log.Warn("generated a new crypto donations wallet; the recovery phrase is stored encrypted, back up the shared donations wallet key file")
	} else if !errors.Is(err, domain.ErrDonationWalletAlreadyExists) {
		return err
	}

	watchers := make(map[string]*runningWatcher)
	defer func() {
		for _, w := range watchers {
			w.cancel()
			<-w.done
		}
	}()
	refresh := func() {
		chains, err := s.EnabledChains(ctx)
		if err != nil {
			log.Warn("list enabled donation chains failed", zap.Error(err))
			return
		}
		want := make(map[string]domain.DonationChain, len(chains))
		for _, chain := range chains {
			if chain.Watchable() {
				want[chain.Key] = chain
			}
		}
		for key, w := range watchers {
			chain, ok := want[key]
			if ok && chain.RPCURL == w.rpcURL && chain.ChainID == w.chainID {
				continue
			}
			w.cancel()
			<-w.done
			delete(watchers, key)
		}
		for key, chain := range want {
			if _, ok := watchers[key]; ok {
				continue
			}
			watchCtx, cancel := context.WithCancel(ctx)
			w := &runningWatcher{cancel: cancel, rpcURL: chain.RPCURL, chainID: chain.ChainID, done: make(chan struct{})}
			watchers[key] = w
			go s.superviseWatcher(watchCtx, key, pollInterval, log.Named(key), w.done)
		}
	}
	refresh()
	ticker := time.NewTicker(chainRefreshInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			refresh()
		}
	}
}

func (s *Service) superviseWatcher(ctx context.Context, chainKey string, pollInterval time.Duration, log *zap.Logger, done chan<- struct{}) {
	defer close(done)
	for {
		err := s.WatchChain(ctx, chainKey, pollInterval, log)
		if ctx.Err() != nil {
			return
		}
		log.Error("donation watcher stopped; restarting", zap.Error(err))
		select {
		case <-ctx.Done():
			return
		case <-time.After(watcherRestartDelay):
		}
	}
}
