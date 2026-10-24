package rpc

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"github.com/iamxvbaba/td/proto"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"github.com/iamxvbaba/td/tlprofile"
	"strconv"
	walletapp "telesrv/internal/app/wallet"
	"telesrv/internal/domain"
	"time"
)

func (r *Router) onWalletSendTransfer(ctx context.Context, req *tg.WalletSendTransferRequest) (any, error) {
	uid, key, err := r.walletCaller(ctx, "transfer", 60)
	if err != nil {
		return nil, err
	}
	if req == nil || req.RandomID == 0 {
		return nil, inputRequestInvalidErr()
	}
	data := req.GetDataNormal()
	if len(data) == 0 {
		data, _ = req.GetDataGasless()
	}
	if len(data) == 0 {
		return nil, inputRequestInvalidErr()
	}
	if req.UserID == nil {
		return nil, inputRequestInvalidErr()
	}
	recipient, found, err := r.userFromInput(ctx, uid, req.UserID)
	if err != nil {
		return nil, internalErr()
	}
	if !found || recipient.ID <= 0 || recipient.ID == uid || recipient.Bot || recipient.Deleted {
		return nil, userIDInvalidErr()
	}
	if r.deps.Gifts == nil {
		return nil, notImplementedErr()
	}
	link, _, ok, err := r.deps.Wallet.State(ctx, uid)
	if err != nil {
		return nil, walletRPCError(err)
	}
	if !ok {
		link, _, ok, err = r.provisionWallet(ctx, uid, key)
		if err != nil || !ok {
			return nil, walletRPCError(err)
		}
	}
	intent, err := walletapp.ParseTransferBody(data, link.PublicKey, link.Address)
	if err != nil {
		return nil, tgerr.New(400, "WALLET_TRANSFER_INVALID")
	}
	// The body names a recipient address. When the receiver has a wallet of
	// its own, the address has to be that wallet: a body that names a third
	// address while the request names a different account would move the
	// money to an account the caller did not ask for.
	if intent.RecipientAddress != "" && r.deps.Wallet != nil {
		links, err := r.deps.Wallet.Links(ctx, []int64{recipient.ID})
		if err != nil {
			return nil, walletRPCError(err)
		}
		// The body names the address in its canonical form and the wallet
		// keeps it in the friendly one, so the two are compared canonically:
		// they are the same account written two ways.
		bodyAddress, bodyErr := domain.CanonicalTONAddress(intent.RecipientAddress)
		for _, candidate := range links {
			linkAddress, linkErr := domain.CanonicalTONAddress(candidate.Address)
			if bodyErr != nil || linkErr != nil || bodyAddress != linkAddress {
				return nil, tgerr.New(400, "WALLET_TRANSFER_INVALID")
			}
		}
	}
	commandKey := fmt.Sprintf("wallet-transfer:%d:%d", uid, req.RandomID)
	// The recipient's wallet address travels with the message, so the chat
	// shows where the money arrived.
	peerAddress := ""
	if r.deps.Wallet != nil {
		if links, linkErr := r.deps.Wallet.Links(ctx, []int64{recipient.ID}); linkErr == nil {
			for _, candidate := range links {
				peerAddress = candidate.Address
			}
		}
	}
	transfer, err := r.deps.Gifts.TransferWalletGram(ctx, domain.WalletGramTransferRequest{
		FromUserID:      uid,
		ToUserID:        recipient.ID,
		AmountNanoton:   intent.AmountNanoton,
		Reason:          domain.StarsReasonTransfer,
		PeerAddress:     peerAddress,
		Comment:         intent.Comment,
		Date:            int(r.clock.Now().Unix()),
		Title:           "Wallet transfer",
		CommandKey:      commandKey,
		OriginAuthKeyID: rawAuthKeyIDForOrigin(ctx),
		OriginSessionID: sessionIDOrZero(ctx),
	})
	if err != nil {
		return nil, walletTransferErr(err)
	}
	// The service message is the answer's payload: the sender's client sees
	// the transfer as a message in the conversation, exactly as it will see it
	// in the chat with the receiver.
	updates := r.starGiftSendUpdates(ctx, uid, transfer.Send)
	hash := sha256.Sum256(data)
	receipt := &tg.UpdateSentWalletTransaction{MsgHash: base64.StdEncoding.EncodeToString(hash[:])}
	receipt.SetTransaction(gramTransferTransactionFromLedger(domain.TonTransaction{ID: transfer.TransactionID, Amount: -intent.AmountNanoton, Date: int(r.clock.Now().Unix()), Peer: domain.Peer{Type: domain.PeerTypeUser, ID: recipient.ID}, Comment: intent.Comment}, map[int64]string{recipient.ID: peerAddress}))
	updates.Updates = append(updates.Updates, receipt)
	for _, userID := range []int64{uid, recipient.ID} {
		current, balance, exists, stateErr := r.deps.Wallet.State(ctx, userID)
		if stateErr != nil || !exists {
			continue
		}
		state := &tg.UpdateWalletState{State: &tg.WalletState{Address: current.Address, PublicKey: current.PublicKey, Balance: balance}}
		if userID == uid {
			updates.Updates = append(updates.Updates, state)
		}
		if hub, ok := r.deps.Sessions.(SemanticTransientSessionBinder); ok {
			_, _ = hub.PushToUserTransientCompatible(ctx, userID, tlprofile.SemanticTypeUpdateWalletState, proto.MessageFromServer, &tg.Updates{Updates: []tg.UpdateClass{state}, Date: int(r.clock.Now().Unix())}, time.Second)
		}
	}

	return updates, nil
}

func walletTransferErr(err error) error {
	switch {
	case errors.Is(err, domain.ErrStarsInsufficient):
		return balanceTooLowErr()
	case errors.Is(err, domain.ErrStarGiftOwnerInvalid):
		return inputRequestInvalidErr()
	}
	return internalErr()
}

func gramTransferTransactionFromLedger(txn domain.TonTransaction, peerAddresses map[int64]string) tg.WalletTransaction {
	address := ""
	if txn.Peer.Type == domain.PeerTypeUser {
		address = peerAddresses[txn.Peer.ID]
	}
	item := tg.WalletTransaction{
		ID:       strconv.FormatInt(txn.ID, 10),
		Amount:   txn.Amount,
		Date:     txn.Date,
		Incoming: txn.Amount > 0,
		Peer:     &tg.WalletTransactionPeerUser{UserID: txn.Peer.ID, Address: address},
	}
	if txn.Comment != "" {
		item.SetComment(txn.Comment)
	}
	return item
}
