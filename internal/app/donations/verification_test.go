package donations

import (
	"context"
	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"math/big"
	"telesrv/internal/domain"
	"testing"
)

type verificationChain struct {
	receipt *types.Receipt
	header  *types.Header
	tx      *types.Transaction
	missing bool
}

func (c verificationChain) TransactionReceipt(context.Context, common.Hash) (*types.Receipt, error) {
	if c.missing {
		return nil, ethereum.NotFound
	}
	return c.receipt, nil
}
func (c verificationChain) TransactionByHash(context.Context, common.Hash) (*types.Transaction, bool, error) {
	return c.tx, false, nil
}
func (c verificationChain) HeaderByNumber(context.Context, *big.Int) (*types.Header, error) {
	return c.header, nil
}
func TestVerifyDepositRequiresCanonicalSuccessfulTransfer(t *testing.T) {
	address := common.HexToAddress("0x1111111111111111111111111111111111111111")
	contract := common.HexToAddress("0x2222222222222222222222222222222222222222")
	for _, tc := range []struct {
		name   string
		change func(*verificationChain, *domain.DonationDeposit, *domain.DonationToken)
		want   bool
	}{
		{"native", func(*verificationChain, *domain.DonationDeposit, *domain.DonationToken) {}, true},
		{"failed transaction", func(c *verificationChain, _ *domain.DonationDeposit, _ *domain.DonationToken) {
			c.receipt.Status = types.ReceiptStatusFailed
		}, false},
		{"orphaned", func(c *verificationChain, _ *domain.DonationDeposit, _ *domain.DonationToken) {
			c.header.Extra = []byte("different chain")
		}, false},
		{"missing", func(c *verificationChain, _ *domain.DonationDeposit, _ *domain.DonationToken) { c.missing = true }, false},
		{"changed amount", func(_ *verificationChain, d *domain.DonationDeposit, _ *domain.DonationToken) { d.AmountRaw = "200" }, false},
		{"wrong block", func(_ *verificationChain, d *domain.DonationDeposit, _ *domain.DonationToken) { d.BlockNumber = 8 }, false},
		{"too shallow", func(_ *verificationChain, d *domain.DonationDeposit, _ *domain.DonationToken) { d.BlockNumber = 12 }, false},
		{"token", func(c *verificationChain, d *domain.DonationDeposit, token *domain.DonationToken) {
			d.TokenSymbol = "USDT"
			d.LogIndex = 3
			*token = domain.DonationToken{Symbol: "USDT", ContractAddress: contract.Hex(), Decimals: 6}
			c.receipt.Logs = []*types.Log{{Address: contract, Index: 3, Topics: []common.Hash{erc20TransferSig, {}, common.BytesToHash(address.Bytes())}, Data: common.LeftPadBytes(big.NewInt(100).Bytes(), 32)}}
		}, true},
		{"unconfigured token", func(_ *verificationChain, d *domain.DonationDeposit, _ *domain.DonationToken) {
			d.TokenSymbol = "MISSING"
		}, false},
		{"fake transfer topic", func(c *verificationChain, d *domain.DonationDeposit, token *domain.DonationToken) {
			d.TokenSymbol = "USDT"
			d.LogIndex = 3
			*token = domain.DonationToken{Symbol: "USDT", ContractAddress: contract.Hex()}
			c.receipt.Logs = []*types.Log{{Address: contract, Index: 3, Topics: []common.Hash{{}, {}, common.BytesToHash(address.Bytes())}, Data: common.LeftPadBytes(big.NewInt(100).Bytes(), 32)}}
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tx := types.NewTx(&types.LegacyTx{To: &address, Value: big.NewInt(100)})
			header := &types.Header{Number: big.NewInt(10)}
			c := verificationChain{tx: tx, header: header, receipt: &types.Receipt{Status: types.ReceiptStatusSuccessful, TxHash: tx.Hash(), BlockNumber: big.NewInt(10), BlockHash: header.Hash()}}
			d := domain.DonationDeposit{TxHash: tx.Hash().Hex(), AmountRaw: "100", BlockNumber: 10, LogIndex: -1}
			token := domain.DonationToken{}
			tc.change(&c, &d, &token)
			got, err := verifyDeposit(context.Background(), c, d, address.Hex(), token, 12, 3)
			if err != nil || got != tc.want {
				t.Fatalf("verified=%v err=%v want=%v", got, err, tc.want)
			}
		})
	}
}
