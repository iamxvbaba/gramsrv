package donations

import (
	"context"
	"errors"
	"math/big"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"telesrv/internal/domain"
)

type depositChainReader interface {
	TransactionReceipt(context.Context, common.Hash) (*types.Receipt, error)
	TransactionByHash(context.Context, common.Hash) (*types.Transaction, bool, error)
	HeaderByNumber(context.Context, *big.Int) (*types.Header, error)
}

// Verify the transfer immediately before ledger credit. Confirmation counts
// alone do not establish success or canonical-chain membership after a reorg.
func verifyDeposit(ctx context.Context, client depositChainReader, d domain.DonationDeposit, address string, token domain.DonationToken, latest uint64, depth int) (bool, error) {
	if d.BlockNumber < 0 || depth <= 0 || uint64(d.BlockNumber) > latest || latest-uint64(d.BlockNumber)+1 < uint64(depth) || !common.IsHexAddress(address) {
		return false, nil
	}
	hash := common.HexToHash(d.TxHash)
	receipt, err := client.TransactionReceipt(ctx, hash)
	if errors.Is(err, ethereum.NotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if receipt == nil || receipt.Status != types.ReceiptStatusSuccessful || receipt.TxHash != hash || receipt.BlockNumber == nil || receipt.BlockNumber.Cmp(big.NewInt(d.BlockNumber)) != 0 {
		return false, nil
	}
	header, err := client.HeaderByNumber(ctx, receipt.BlockNumber)
	if errors.Is(err, ethereum.NotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if header == nil || header.Hash() != receipt.BlockHash {
		return false, nil
	}
	amount, ok := new(big.Int).SetString(d.AmountRaw, 10)
	if !ok || amount.Sign() <= 0 {
		return false, nil
	}
	to := common.HexToAddress(address)
	if d.TokenSymbol == "" {
		tx, pending, err := client.TransactionByHash(ctx, hash)
		if errors.Is(err, ethereum.NotFound) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		return tx != nil && !pending && tx.Hash() == hash && tx.To() != nil && *tx.To() == to && tx.Value().Cmp(amount) == 0 && d.LogIndex == -1, nil
	}
	if !token.Watchable() || token.Symbol != d.TokenSymbol || !common.IsHexAddress(token.ContractAddress) {
		return false, nil
	}
	for _, lg := range receipt.Logs {
		if lg == nil || lg.Removed || int(lg.Index) != d.LogIndex || lg.Address != common.HexToAddress(token.ContractAddress) || len(lg.Topics) != 3 || lg.Topics[0] != erc20TransferSig || len(lg.Data) != 32 {
			continue
		}
		if lg.Topics[2] != common.BytesToHash(to.Bytes()) {
			continue
		}
		return new(big.Int).SetBytes(lg.Data).Cmp(amount) == 0, nil
	}
	return false, nil
}
