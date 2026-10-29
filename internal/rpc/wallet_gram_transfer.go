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
	if gasless, _ := req.GetDataGasless(); len(gasless) != 0 {
		return nil, notImplementedErr()
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
	blocked, err := r.peerBlocksUser(ctx, uid, recipient.ID)
	if err != nil {
		return nil, err
	}
	if blocked {
		return nil, userIsBlockedErr()
	}
	transfers, ok := r.deps.Wallet.(WalletTransferService)
	if !ok {
		return nil, walletTransferErr(walletapp.ErrTransferDisabled)
	}
	intent, err := walletapp.ParseTransferBody(data)
	if err != nil {
		return nil, walletTransferErr(err)
	}
	defer clear(intent.TmpPassword)
	if intent.RecipientUserID != recipient.ID || intent.RandomID != req.RandomID {
		return nil, inputRequestInvalidErr()
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
	transfer, err := transfers.TransferWalletGram(ctx, domain.WalletGramTransferRequest{
		AuthorizationHash: sha256.Sum256(intent.TmpPassword),
		FromUserID:        uid,
		ToUserID:          recipient.ID,
		AmountNanoton:     intent.AmountNanoton,
		Reason:            domain.StarsReasonTransfer,
		PeerAddress:       peerAddress,
		Comment:           intent.Comment,
		Date:              int(r.clock.Now().Unix()),
		Title:             "Wallet transfer",
		CommandKey:        commandKey,
		OriginAuthKeyID:   key,
		OriginSessionID:   sessionIDOrZero(ctx),
	})
	if err != nil {
		return nil, walletTransferErr(err)
	}
	// The service message is the answer's payload: the sender's client sees
	// the transfer as a message in the conversation, exactly as it will see it
	// in the chat with the receiver.
	updates := r.starGiftSendUpdates(ctx, uid, transfer.Send)
	// Replays preserve the address saved with the original receipt.
	if media := transfer.Send.SenderMessage.Media; media != nil && media.ServiceAction != nil && media.ServiceAction.GramTransfer != nil {
		peerAddress = media.ServiceAction.GramTransfer.PeerAddress
	}
	hash := sha256.Sum256(data)
	receipt := &tg.UpdateSentWalletTransaction{MsgHash: base64.StdEncoding.EncodeToString(hash[:])}
	receipt.SetTransaction(gramTransferTransactionFromLedger(domain.TonTransaction{ID: transfer.TransactionID, Amount: -intent.AmountNanoton, Date: transfer.Date, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: recipient.ID}, Comment: intent.Comment}, map[int64]string{recipient.ID: peerAddress}))
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
		// Balance pushes are best-effort hints. The durable chat/ledger receipt
		// is committed above; wallet.getState recovers the balance after reconnect.
		if hub, ok := r.deps.Sessions.(SemanticTransientSessionBinder); ok {
			_, _ = hub.PushToUserTransientCompatible(ctx, userID, tlprofile.SemanticTypeUpdateWalletState, proto.MessageFromServer, &tg.Updates{Updates: []tg.UpdateClass{state}, Date: int(r.clock.Now().Unix())}, time.Second)
		}
	}

	return updates, nil
}

func walletTransferErr(err error) error {
	switch {
	case errors.Is(err, walletapp.ErrTransferDisabled), errors.Is(err, domain.ErrWalletTransferStartingGrant):
		return tgerr.New(400, "WALLET_TRANSFER_DISABLED")
	case errors.Is(err, domain.ErrWalletTransferRecipientBlocked):
		return userIsBlockedErr()
	case errors.Is(err, walletapp.ErrInvalidTransferBody):
		return tgerr.New(400, "WALLET_TRANSFER_AUTH_REQUIRED")
	case errors.Is(err, domain.ErrTmpPasswordInvalid):
		return tgerr.New(400, "TMP_PASSWORD_INVALID")
	case errors.Is(err, domain.ErrStarGiftIdempotencyConflict):
		return tgerr.New(400, "RANDOM_ID_DUPLICATE")
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
