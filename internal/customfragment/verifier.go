package customfragment

import (
	"context"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"time"

	"github.com/xssnick/tonutils-go/liteclient"
	"github.com/xssnick/tonutils-go/ton"
	"github.com/xssnick/tonutils-go/ton/nft"
	tonwallet "github.com/xssnick/tonutils-go/ton/wallet"
)

const defaultMainnetConfigURL = "https://ton-blockchain.github.io/global.config.json"

type MainnetVerifier struct {
	configURL string
	mu        sync.Mutex
	pool      *liteclient.ConnectionPool
	api       ton.APIClientWrapped
}

// VerifyWalletProof validates the complete ton_proof against the wallet code
// and data obtained from state-init (or mainnet for an already deployed
// wallet). The proof domain and timestamp are part of the signed message.
func (v *MainnetVerifier) VerifyWalletProof(ctx context.Context, ownerRaw, proofDomain string, proof tonwallet.TonConnectProof, stateInit []byte, ttl time.Duration) error {
	owner, err := parseMainnetAddress(ownerRaw)
	if err != nil {
		return err
	}
	if proof.Domain.LengthBytes != uint32(len([]byte(proof.Domain.Value))) {
		return fmt.Errorf("TON Proof domain length mismatch")
	}
	if ttl <= 0 || ttl > 15*time.Minute {
		return fmt.Errorf("invalid TON Proof TTL")
	}
	api, err := v.client(ctx)
	if err != nil {
		return err
	}
	return tonwallet.NewTonConnectVerifier(proofDomain, ttl, api).
		VerifyProof(ctx, owner, proof, proof.Payload, stateInit)
}

func NewMainnetVerifier(configURL string) *MainnetVerifier {
	configURL = strings.TrimSpace(configURL)
	if configURL == "" {
		configURL = defaultMainnetConfigURL
	}
	return &MainnetVerifier{configURL: configURL}
}

func (v *MainnetVerifier) VerifyMint(ctx context.Context, collectionRaw string, itemIndex *big.Int, ownerRaw string) (string, error) {
	if itemIndex == nil || itemIndex.Sign() < 0 {
		return "", fmt.Errorf("invalid item index")
	}
	collection, err := parseMainnetAddress(collectionRaw)
	if err != nil {
		return "", err
	}
	owner, err := parseMainnetAddress(ownerRaw)
	if err != nil {
		return "", err
	}
	api, err := v.client(ctx)
	if err != nil {
		return "", err
	}
	itemAddress, err := nft.NewCollectionClient(api, collection).GetNFTAddressByIndex(ctx, itemIndex)
	if err != nil {
		return "", fmt.Errorf("resolve NFT address: %w", err)
	}
	data, err := nft.NewItemClient(api, itemAddress).GetNFTData(ctx)
	if err != nil {
		return "", fmt.Errorf("read NFT data: %w", err)
	}
	if !data.Initialized || data.Index == nil || data.Index.Cmp(itemIndex) != 0 ||
		data.CollectionAddress == nil || !data.CollectionAddress.Equals(collection) ||
		data.OwnerAddress == nil || !data.OwnerAddress.Equals(owner) {
		return "", fmt.Errorf("NFT owner, collection or index does not match authorization")
	}
	return itemAddress.StringRaw(), nil
}

// CurrentNFTOwner resolves the current owner of one NFT from the configured
// collection. The boolean is false when itemRaw belongs to another collection
// generation. That distinction lets the ownership watcher ignore immutable
// NFTs minted by an older deployment without mutating their Gramsrv records.
func (v *MainnetVerifier) CurrentNFTOwner(ctx context.Context, collectionRaw string, itemIndex *big.Int, itemRaw string) (string, bool, error) {
	if itemIndex == nil || itemIndex.Sign() < 0 {
		return "", false, fmt.Errorf("invalid item index")
	}
	collection, err := parseMainnetAddress(collectionRaw)
	if err != nil {
		return "", false, err
	}
	item, err := parseMainnetAddress(itemRaw)
	if err != nil {
		return "", false, err
	}
	api, err := v.client(ctx)
	if err != nil {
		return "", false, err
	}
	derived, err := nft.NewCollectionClient(api, collection).GetNFTAddressByIndex(ctx, itemIndex)
	if err != nil {
		return "", false, fmt.Errorf("resolve NFT address: %w", err)
	}
	if !derived.Equals(item) {
		return "", false, nil
	}
	data, err := nft.NewItemClient(api, item).GetNFTData(ctx)
	if err != nil {
		return "", true, fmt.Errorf("read NFT data: %w", err)
	}
	if !data.Initialized || data.Index == nil || data.Index.Cmp(itemIndex) != 0 ||
		data.CollectionAddress == nil || !data.CollectionAddress.Equals(collection) ||
		data.OwnerAddress == nil || data.OwnerAddress.IsAddrNone() {
		return "", true, fmt.Errorf("NFT data does not match collection identity")
	}
	return data.OwnerAddress.StringRaw(), true, nil
}

func (v *MainnetVerifier) client(ctx context.Context) (ton.APIClientWrapped, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.api != nil {
		return v.api, nil
	}
	pool := liteclient.NewConnectionPool()
	connectCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := pool.AddConnectionsFromConfigUrl(connectCtx, v.configURL); err != nil {
		pool.Stop()
		return nil, fmt.Errorf("connect mainnet lite servers: %w", err)
	}
	v.pool = pool
	v.api = ton.NewAPIClient(pool, ton.ProofCheckPolicyFast).WithRetryTimeout(2, 4*time.Second)
	return v.api, nil
}

func (v *MainnetVerifier) Close() {
	if v == nil {
		return
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.pool != nil {
		v.pool.Stop()
	}
	v.pool, v.api = nil, nil
}

var _ Verifier = (*MainnetVerifier)(nil)
