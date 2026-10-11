package tondns

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/xssnick/tonutils-go/liteclient"
	"github.com/xssnick/tonutils-go/ton"
	"github.com/xssnick/tonutils-go/ton/dns"
)

const defaultConfigURL = "https://ton-blockchain.github.io/global.config.json"

// Resolver answers which wallet address a .ton name points at, so an operator
// can enter the name a collector actually uses instead of its raw form. It
// keeps one lite server connection for its lifetime, the same way the custom
// fragment verifier does, and connects lazily on the first lookup.
type Resolver struct {
	configURL string

	mu     sync.Mutex
	pool   *liteclient.ConnectionPool
	api    ton.APIClientWrapped
	client *dns.Client
}

// New returns a resolver reading the mainnet config from configURL, falling
// back to the public config when none is configured.
func New(configURL string) *Resolver {
	configURL = strings.TrimSpace(configURL)
	if configURL == "" {
		configURL = defaultConfigURL
	}
	return &Resolver{configURL: configURL}
}

// Resolve returns the raw mainnet address published by name's wallet record.
func (r *Resolver) Resolve(ctx context.Context, name string) (string, error) {
	if r == nil {
		return "", errors.New("ton dns resolver is not configured")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	client, err := r.dnsClient(ctx)
	if err != nil {
		return "", err
	}
	domain, err := client.Resolve(ctx, name)
	if err != nil {
		if errors.Is(err, dns.ErrNoSuchRecord) {
			// WHY: an unregistered .ton name is the common operator mistake, so
			// the message names the domain and says what to send instead.
			return "", fmt.Errorf("%s is not registered on ton dns", name)
		}
		return "", fmt.Errorf("resolve %s: %w", name, err)
	}
	wallet := domain.GetWalletRecord()
	if wallet == nil || wallet.IsAddrNone() || wallet.Workchain() != 0 || wallet.IsTestnetOnly() {
		return "", fmt.Errorf("%s has no mainnet wallet record", name)
	}
	return wallet.StringRaw(), nil
}

// Close releases the lite server connections; a later Resolve reconnects.
func (r *Resolver) Close() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.pool != nil {
		r.pool.Stop()
	}
	r.pool, r.api, r.client = nil, nil, nil
}

func (r *Resolver) dnsClient(ctx context.Context) (*dns.Client, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.client != nil {
		return r.client, nil
	}
	pool := liteclient.NewConnectionPool()
	connectCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := pool.AddConnectionsFromConfigUrl(connectCtx, r.configURL); err != nil {
		pool.Stop()
		return nil, fmt.Errorf("connect ton lite servers: %w", err)
	}
	api := ton.NewAPIClient(pool, ton.ProofCheckPolicyFast).WithRetryTimeout(2, 4*time.Second)
	root, err := dns.GetRootContractAddr(ctx, api)
	if err != nil {
		pool.Stop()
		return nil, fmt.Errorf("read ton dns root: %w", err)
	}
	r.pool, r.api, r.client = pool, api, dns.NewDNSClient(api, root)
	return r.client, nil
}
