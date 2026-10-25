package bots

import (
	"context"
	"strings"
	"testing"

	"telesrv/internal/domain"
)

// stubDonationsSource is a minimal donationsSource double: no real wallet,
// no real chain, just canned answers so premiumBotDepositReply's branches
// (unconfigured, no watchable chain, address assignment failure, happy
// path) can each be exercised without a database or a blockchain.
type stubDonationsSource struct {
	ready      bool
	chains     []domain.DonationChain
	chainsErr  error
	address    string
	addressErr error
	deposits   []domain.DonationDeposit
}

func (s *stubDonationsSource) Ready() bool { return s.ready }
func (s *stubDonationsSource) AddressForUser(context.Context, int64) (string, error) {
	return s.address, s.addressErr
}
func (s *stubDonationsSource) EnabledChains(context.Context) ([]domain.DonationChain, error) {
	return s.chains, s.chainsErr
}
func (s *stubDonationsSource) UserDeposits(context.Context, int64, int) ([]domain.DonationDeposit, error) {
	return s.deposits, nil
}

func TestPremiumBotDepositTextUnconfigured(t *testing.T) {
	svc, _, _, _ := newTestService(t)
	// No SetDonationsSource call at all -- the zero value (nil) must answer
	// gracefully, not panic, exactly like an operator who hasn't set
	// TELESRV_DONATION_WALLET_KEY.
	got := svc.premiumBotDepositReply(context.Background(), 42).Text
	if !strings.Contains(got, "not available") {
		t.Fatalf("unconfigured text = %q, want it to say deposits are not available", got)
	}
}

func TestPremiumBotDepositTextNotReady(t *testing.T) {
	svc, _, _, _ := newTestService(t)
	svc.SetDonationsSource(&stubDonationsSource{ready: false})
	got := svc.premiumBotDepositReply(context.Background(), 42).Text
	if !strings.Contains(got, "not available") {
		t.Fatalf("not-ready text = %q, want it to say deposits are not available", got)
	}
}

func TestPremiumBotDepositTextNoWatchableChain(t *testing.T) {
	svc, _, _, _ := newTestService(t)
	svc.SetDonationsSource(&stubDonationsSource{
		ready: true,
		chains: []domain.DonationChain{
			{Key: "ethereum", Name: "Ethereum", ChainID: 1, NativeSymbol: "ETH", NativeDecimals: 18, ConfirmationsRequired: 12, Enabled: true}, // no RPCURL -> not watchable
		},
	})
	got := svc.premiumBotDepositReply(context.Background(), 42).Text
	if !strings.Contains(got, "not available") {
		t.Fatalf("no-watchable-chain text = %q, want it to say deposits are not available", got)
	}
}

func TestPremiumBotDepositTextAddressFailure(t *testing.T) {
	svc, _, _, _ := newTestService(t)
	svc.SetDonationsSource(&stubDonationsSource{
		ready: true,
		chains: []domain.DonationChain{
			{Key: "ganache", Name: "Ganache (local)", ChainID: 1337, RPCURL: "http://127.0.0.1:7545", NativeSymbol: "ETH", NativeDecimals: 18, ConfirmationsRequired: 1, Enabled: true},
		},
		addressErr: domain.ErrDonationWalletNotConfigured,
	})
	got := svc.premiumBotDepositReply(context.Background(), 42).Text
	if !strings.Contains(got, "try again") {
		t.Fatalf("address-failure text = %q, want a retry message", got)
	}
}

func TestPremiumBotDepositTextHappyPath(t *testing.T) {
	svc, _, _, _ := newTestService(t)
	const address = "0xabc0000000000000000000000000000000dead"
	svc.SetDonationsSource(&stubDonationsSource{
		ready: true,
		chains: []domain.DonationChain{
			{Key: "ganache", Name: "Ganache (local)", ChainID: 1337, RPCURL: "http://127.0.0.1:7545", NativeSymbol: "ETH", NativeDecimals: 18, ConfirmationsRequired: 1, Enabled: true},
			{Key: "sepolia", Name: "Sepolia (testnet)", ChainID: 11155111, RPCURL: "https://sepolia.example", NativeSymbol: "ETH", NativeDecimals: 18, ConfirmationsRequired: 10, Enabled: true},
			{Key: "ethereum", Name: "Ethereum", ChainID: 1, NativeSymbol: "ETH", NativeDecimals: 18, ConfirmationsRequired: 12, Enabled: true}, // disabled by empty RPCURL, must be left out
		},
		address: address,
	})
	reply := svc.premiumBotDepositReply(context.Background(), 42)
	got := reply.Text
	if !strings.Contains(got, address) {
		t.Fatalf("happy-path text = %q, want the assigned address", got)
	}
	if strings.Contains(got, "`") {
		t.Fatalf("happy-path text = %q, want no literal backticks -- the server has no markdown parser, the address must be a Code entity instead", got)
	}
	if !strings.Contains(got, "Ganache (local)") || !strings.Contains(got, "Sepolia (testnet)") {
		t.Fatalf("happy-path text = %q, want both watchable chains listed", got)
	}
	if strings.Contains(got, "Ethereum (ETH)") {
		t.Fatalf("happy-path text = %q, want the unwatchable Ethereum row left out", got)
	}
	if len(reply.Entities) != 1 || reply.Entities[0].Type != domain.MessageEntityCode {
		t.Fatalf("entities = %+v, want exactly one Code entity for the address", reply.Entities)
	}
	entity := reply.Entities[0]
	if got[entity.Offset:entity.Offset+entity.Length] != address {
		t.Fatalf("Code entity covers %q, want the address %q", got[entity.Offset:entity.Offset+entity.Length], address)
	}
}

// TestPremiumBotRespondsToDepositCommand pins that /deposit is wired into
// the command dispatcher at all, not just that premiumBotDepositReply itself
// behaves -- the two were separate bugs to make in the same change.
func TestPremiumBotRespondsToDepositCommand(t *testing.T) {
	svc, users, _, messages := newTestService(t)
	owner := newOwner(t, users, "15550001234")
	svc.SetDonationsSource(&stubDonationsSource{
		ready: true,
		chains: []domain.DonationChain{
			{Key: "ganache", Name: "Ganache (local)", ChainID: 1337, RPCURL: "http://127.0.0.1:7545", NativeSymbol: "ETH", NativeDecimals: 18, ConfirmationsRequired: 1, Enabled: true},
		},
		address: "0xabc0000000000000000000000000000000dead",
	})
	svc.respondAsPremium(domain.PremiumBotUserID, owner.ID, domain.Message{
		From: domain.Peer{Type: domain.PeerTypeUser, ID: owner.ID},
		Peer: domain.Peer{Type: domain.PeerTypeUser, ID: domain.PremiumBotUserID},
		Body: "/deposit",
	}, domain.ClientSessionMetadata{})
	list, err := messages.ListByUser(context.Background(), owner.ID, domain.MessageFilter{
		HasPeer: true, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: domain.PremiumBotUserID}, Limit: 100,
	})
	if err != nil {
		t.Fatalf("list premiumbot history: %v", err)
	}
	var reply domain.Message
	for _, msg := range list.Messages {
		if msg.From.ID == domain.PremiumBotUserID && msg.ID > reply.ID {
			reply = msg
		}
	}
	if !strings.Contains(reply.Body, "0xabc0000000000000000000000000000000dead") {
		t.Fatalf("premiumbot /deposit reply = %q, want the assigned address", reply.Body)
	}
}
