package rpc

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"strconv"
	"time"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"github.com/iamxvbaba/td/tlprofile"
	walletapp "telesrv/internal/app/wallet"
	"telesrv/internal/domain"
)

// WalletService is the in-app TON wallet boundary declared in deps.go; this file only
// maps it to the Layer 230 wallet.* / toncenter.* semantic methods.
func (r *Router) registerWallet(d *tlprofile.Dispatcher) {
	registerRPC[*tg.WalletSendTransferRequest](d, tlprofile.SemanticMethodWalletSendTransfer, func(ctx context.Context, req *tg.WalletSendTransferRequest) (any, error) {
		return r.onWalletSendTransfer(ctx, req)
	})
	registerRPC[*tg.WalletGetProofChallengeRequest](d, tlprofile.SemanticMethodWalletGetProofChallenge, func(ctx context.Context, _ *tg.WalletGetProofChallengeRequest) (any, error) {
		uid, key, err := r.walletCaller(ctx, "challenge", 60)
		if err != nil {
			return nil, err
		}
		c, err := r.deps.Wallet.GetProofChallenge(ctx, uid, key)
		if err != nil {
			return nil, walletRPCError(err)
		}
		return &tg.WalletProofChallenge{Payload: c.Payload, Expires: int(c.ExpiresAt.Unix()), Domain: c.Domain}, nil
	})
	registerRPC[*tg.WalletReplaceWalletRequest](d, tlprofile.SemanticMethodWalletReplaceWallet, func(ctx context.Context, req *tg.WalletReplaceWalletRequest) (any, error) {
		return r.onWalletReplace(ctx, req)
	})
	registerRPC[*tg.WalletGetStateRequest](d, tlprofile.SemanticMethodWalletGetState, func(ctx context.Context, _ *tg.WalletGetStateRequest) (any, error) {
		return r.onWalletState(ctx)
	})
	registerRPC[*tg.WalletGetExistingWaltBalanceRequest](d, tlprofile.SemanticMethodWalletGetExistingWaltBalance, func(ctx context.Context, _ *tg.WalletGetExistingWaltBalanceRequest) (any, error) {
		if _, _, err := r.walletCaller(ctx, "existing-balance", 60); err != nil {
			return nil, err
		}
		// telesrv holds balances in its own ledger and configures no external
		// TON provider, so there is no on-chain balance page to send the client
		// to. Reporting has_balance=false is the honest answer.
		return &tg.WalletExistingBalance{}, nil
	})
	registerRPC[*tg.WalletGetUserAddressesRequest](d, tlprofile.SemanticMethodWalletGetUserAddresses, func(ctx context.Context, req *tg.WalletGetUserAddressesRequest) (any, error) {
		return r.onWalletAddresses(ctx, req)
	})
	registerRPC[*tg.ToncenterPerformAPIRequestRequest](d, tlprofile.SemanticMethodToncenterPerformAPIRequest, func(ctx context.Context, req *tg.ToncenterPerformAPIRequestRequest) (any, error) {
		if _, _, err := r.walletCaller(ctx, "provider", 120); err != nil {
			return nil, err
		}
		var query, payload *string
		if v, ok := req.GetQuery(); ok {
			query = &v
		}
		if v, ok := req.GetPayload(); ok {
			payload = &v
		}
		result, err := r.deps.Wallet.Perform(ctx, req.Endpoint, query, payload)
		if err != nil {
			return nil, walletRPCError(err)
		}
		return &tg.ToncenterAPIResponse{Response: tg.DataJSON{Data: result}}, nil
	})

	// -- TON Connect -------------------------------------------------------
	//
	// TON Connect is the bridge to external wallet apps. telesrv's wallet is
	// server-side, so it holds no TON Connect sessions and cannot serve one.
	//
	// Registering these is the point of the exercise, not implementing them: an
	// unregistered method is logged as an "Unhandled RPC (compatibility trace)"
	// warning on *every* call, and clients poll tonConnectGetSessions on a loop,
	// so leaving them out costs hundreds of warn lines a day. Two of the nine can
	// still answer truthfully -- there are no sessions, and therefore no events --
	// and the rest refuse, because each of them would have to create or drive a
	// session that cannot exist here.
	//
	// The gasless pair is the same story: telesrv fronts transfers itself rather
	// than relaying them, so there is nothing to report.
	registerRPC[*tg.WalletTonConnectGetSessionsRequest](d, tlprofile.SemanticMethodWalletTonConnectGetSessions, func(ctx context.Context, _ *tg.WalletTonConnectGetSessionsRequest) (any, error) {
		if _, _, err := r.walletCaller(ctx, "tonconnect-sessions", 30); err != nil {
			return nil, err
		}
		return &tg.WalletTonConnectSessions{Sessions: []tg.TonConnectSession{}}, nil
	})
	registerRPC[*tg.WalletTonConnectNextEventIDRequest](d, tlprofile.SemanticMethodWalletTonConnectNextEventID, func(ctx context.Context, _ *tg.WalletTonConnectNextEventIDRequest) (any, error) {
		if _, _, err := r.walletCaller(ctx, "tonconnect-events", 30); err != nil {
			return nil, err
		}
		// Layer 230 returns the boxed TonConnectNextEventId constructor.
		return &tg.TonConnectNextEventID{EventID: 0}, nil
	})
	// The four methods the client polls on the Money screen. All of them were
	// unregistered, and an unregistered method logs a compatibility trace on
	// every call, so this screen was producing ~200 warnings a minute on its own.
	//
	// Only getTransactions has anything real behind it. The others answer
	// truthfully about a deployment that has no TON node and no onramp:
	// getNfts reports an empty collection, getOnrampProviders an empty provider
	// list, and getStreamingUrl refuses, because handing back a URL telesrv does
	// not serve would be worse than saying so.
	registerRPC[*tg.WalletGetTransactionsRequest](d, tlprofile.SemanticMethodWalletGetTransactions, func(ctx context.Context, req *tg.WalletGetTransactionsRequest) (any, error) {
		return r.onWalletTransactions(ctx, req)
	})
	registerRPC[*tg.WalletGetNftsRequest](d, tlprofile.SemanticMethodWalletGetNfts, func(ctx context.Context, req *tg.WalletGetNftsRequest) (any, error) {
		if _, _, err := r.walletCaller(ctx, "nfts", 60); err != nil {
			return nil, err
		}
		if req.Limit < 0 || req.Limit > 100 {
			return nil, inputRequestInvalidErr()
		}
		return &tg.WalletNftItems{Items: []tg.WalletNftItem{}, NextOffset: ""}, nil
	})
	registerRPC[*tg.PaymentsGetOnrampProvidersRequest](d, tlprofile.SemanticMethodPaymentsGetOnrampProviders, func(ctx context.Context, _ *tg.PaymentsGetOnrampProvidersRequest) (any, error) {
		if _, _, err := r.walletCaller(ctx, "onramp", 60); err != nil {
			return nil, err
		}
		return []tg.OnrampProviderInfo{}, nil
	})
	registerRPC[*tg.ToncenterGetStreamingURLRequest](d, tlprofile.SemanticMethodToncenterGetStreamingURL, func(ctx context.Context, _ *tg.ToncenterGetStreamingURLRequest) (any, error) {
		return nil, r.walletCapabilityUnsupported(ctx, "toncenter-stream", 60)
	})

	// No gasless relay is configured. Refuse this capability consistently with
	// the app config rather than reporting a successful no-op.
	registerRPC[*tg.WalletGetGaslessInfoRequest](d, tlprofile.SemanticMethodWalletGetGaslessInfo, func(ctx context.Context, _ *tg.WalletGetGaslessInfoRequest) (any, error) {
		return nil, r.walletCapabilityUnsupported(ctx, "gasless-info", 30)
	})

	// Each of these would have to create or drive a session, so each refuses.
	registerRPC[*tg.WalletTonConnectGetPendingRequest](d, tlprofile.SemanticMethodWalletTonConnectGetPending, func(ctx context.Context, _ *tg.WalletTonConnectGetPendingRequest) (any, error) {
		return nil, r.walletCapabilityUnsupported(ctx, "tonconnect-pending", 30)
	})
	registerRPC[*tg.WalletTonConnectCreateSessionRequest](d, tlprofile.SemanticMethodWalletTonConnectCreateSession, func(ctx context.Context, _ *tg.WalletTonConnectCreateSessionRequest) (any, error) {
		return nil, r.walletCapabilityUnsupported(ctx, "tonconnect-create", 5)
	})
	registerRPC[*tg.WalletTonConnectRegisterKeyRequest](d, tlprofile.SemanticMethodWalletTonConnectRegisterKey, func(ctx context.Context, _ *tg.WalletTonConnectRegisterKeyRequest) (any, error) {
		return nil, r.walletCapabilityUnsupported(ctx, "tonconnect-register", 5)
	})
	registerRPC[*tg.WalletTonConnectSubmitConnectResultRequest](d, tlprofile.SemanticMethodWalletTonConnectSubmitConnectResult, func(ctx context.Context, _ *tg.WalletTonConnectSubmitConnectResultRequest) (any, error) {
		return nil, r.walletCapabilityUnsupported(ctx, "tonconnect-connect-result", 5)
	})
	registerRPC[*tg.WalletTonConnectClaimRequestRequest](d, tlprofile.SemanticMethodWalletTonConnectClaimRequest, func(ctx context.Context, _ *tg.WalletTonConnectClaimRequestRequest) (any, error) {
		return nil, r.walletCapabilityUnsupported(ctx, "tonconnect-claim", 10)
	})
	registerRPC[*tg.WalletTonConnectSubmitResponseRequest](d, tlprofile.SemanticMethodWalletTonConnectSubmitResponse, func(ctx context.Context, _ *tg.WalletTonConnectSubmitResponseRequest) (any, error) {
		return nil, r.walletCapabilityUnsupported(ctx, "tonconnect-response", 10)
	})
	registerRPC[*tg.WalletTonConnectCloseSessionRequest](d, tlprofile.SemanticMethodWalletTonConnectCloseSession, func(ctx context.Context, _ *tg.WalletTonConnectCloseSessionRequest) (any, error) {
		return nil, r.walletCapabilityUnsupported(ctx, "tonconnect-close", 10)
	})
}

// walletCapabilityUnsupported authenticates and rate-limits the caller, then
// refuses a wallet capability telesrv does not have.
//
// WALLET_UNAVAILABLE rather than a bespoke code: it is the same answer this file
// gives elsewhere for an unavailable wallet capability, and it is an error code
// clients already handle.
func (r *Router) walletCapabilityUnsupported(ctx context.Context, scope string, limit int) error {
	if _, _, err := r.walletCaller(ctx, scope, limit); err != nil {
		return err
	}
	return tgerr.New(400, "WALLET_UNAVAILABLE")
}

// walletCaller authenticates and rate-limits every wallet method.
func (r *Router) walletCaller(ctx context.Context, scope string, limit int) (int64, [8]byte, error) {
	uid, ok, err := r.currentUserID(ctx)
	if err != nil {
		return 0, [8]byte{}, internalErr()
	}
	if !ok || uid <= 0 {
		return 0, [8]byte{}, authKeyUnregisteredErr()
	}
	key, ok := AuthKeyIDFrom(ctx)
	if !ok || key == ([8]byte{}) {
		return 0, [8]byte{}, authKeyUnregisteredErr()
	}
	if r.deps.Wallet == nil {
		return 0, key, tgerr.New(400, "WALLET_UNAVAILABLE")
	}
	if r.deps.Limiter != nil {
		allowed, retry, err := r.deps.Limiter.AllowN(ctx, walletRateLimitKey(scope, uid, key), 1, limit, time.Minute)
		if err != nil {
			return 0, key, internalErr()
		}
		if !allowed {
			return 0, key, floodWaitErr(retry)
		}
	}

	return uid, key, nil
}

func walletRateLimitKey(scope string, userID int64, key [8]byte) string {
	return "wallet:" + scope + ":" + strconv.FormatInt(userID, 10) + ":" + hex.EncodeToString(key[:])
}

func (r *Router) onWalletReplace(ctx context.Context, req *tg.WalletReplaceWalletRequest) (any, error) {
	uid, key, err := r.walletCaller(ctx, "replace", 10)
	if err != nil {
		return nil, err
	}
	// inputWalletNew asks telesrv to mint the wallet: no client-side secret is
	// involved, so the account password is what protects an existing binding.
	// inputWalletImported carries an Ed25519 ownership proof instead.
	var imported *tg.InputWalletImported
	create := false
	switch wallet := req.Wallet.(type) {
	case *tg.InputWalletImported:
		imported = wallet
	case *tg.InputWalletNew:
		create = true
	default:
		return nil, tgerr.New(400, "WALLET_UNAVAILABLE")
	}
	links, err := r.deps.Wallet.Links(ctx, []int64{uid})
	if err != nil {
		return nil, walletRPCError(err)
	}
	password, provided := req.GetPassword()
	needsReplacement := len(links) > 0 && (create || !bytes.Equal(links[0].PublicKey, imported.PublicKey))
	allowReplacement := false
	if provided || needsReplacement {
		if r.deps.Account == nil {
			return nil, internalErr()
		}
		if err = r.deps.Account.CheckPassword(ctx, uid, domainPasswordCheck(password)); err != nil {
			return nil, passwordErr(err)
		}
		allowReplacement = true
	}
	var link domain.WalletLink
	var balance int64
	if create {
		link, balance, err = r.deps.Wallet.Create(ctx, uid, key, allowReplacement)
	} else {
		link, balance, err = r.deps.Wallet.ReplaceImported(ctx, uid, key, imported.PublicKey, imported.Proof.Timestamp, imported.Proof.Signature, allowReplacement)
	}
	if err != nil {
		return nil, walletRPCError(err)
	}
	// Backup, phrase export and gasless are server capabilities telesrv does not
	// implement yet. Advertising them would make clients offer flows that fail.
	return &tg.WalletState{Address: link.Address, PublicKey: link.PublicKey, Balance: balance}, nil
}

// onWalletState answers wallet.getState. An account without a wallet gets
// walletStateEmpty, which is how the client renders "create a wallet"; telesrv
// never invents an address, and it never advertises backup, phrase export or
// enable-backup capabilities it does not implement.
func (r *Router) onWalletState(ctx context.Context) (any, error) {
	uid, key, err := r.walletCaller(ctx, "state", 60)
	if err != nil {
		return nil, err
	}
	link, balance, ok, err := r.deps.Wallet.State(ctx, uid)
	if err != nil {
		return nil, walletRPCError(err)
	}
	if !ok {
		link, balance, ok, err = r.provisionWallet(ctx, uid, key)
		if err != nil {
			return nil, walletRPCError(err)
		}
	}
	if !ok {
		return &tg.WalletStateEmpty{}, nil
	}
	return &tg.WalletState{Address: link.Address, PublicKey: link.PublicKey, Balance: balance}, nil
}

// onWalletTransactions answers wallet.getTransactions from telesrv's own
// nanoton ledger.
//
// This is the history behind the Money screen, and the ledger is real: entries
// are written by the same paths that move TON for star gifts and suggested
// posts. So the page is populated from the ledger rather than reported empty,
// which would have left the client's history section permanently blank.
//
// Peer is reported as an address rather than a user when the ledger records one:
// the ledger stores a peer type and id, and resolving it to a user object would
// mean a second lookup per row to render a name the screen already has.
func (r *Router) onWalletTransactions(ctx context.Context, req *tg.WalletGetTransactionsRequest) (any, error) {
	uid, _, err := r.walletCaller(ctx, "transactions", 60)
	if err != nil {
		return nil, err
	}
	if req.Flags&^3 != 0 || req.Limit < 0 || req.Limit > 100 || len(req.Offset) > domain.MaxStarsTransactionsOffsetBytes {
		return nil, inputRequestInvalidErr()
	}
	limit := req.Limit
	if limit == 0 {
		limit = 50
	}
	query := domain.StarsTransactionQuery{Limit: limit, Offset: req.Offset}
	// A caller that asks for neither direction wants everything, which is the
	// union; asking for one wants only that side.
	switch {
	case req.Inbound && !req.Outbound:
		query.Direction = domain.StarsTransactionDirectionIncoming
	case req.Outbound && !req.Inbound:
		query.Direction = domain.StarsTransactionDirectionOutgoing
	}

	page, err := r.deps.Wallet.Transactions(ctx, uid, query)
	if err != nil {
		return nil, walletRPCError(err)
	}
	out := &tg.WalletTransactions{
		Balance:      page.Balance,
		Transactions: make([]tg.WalletTransaction, 0, len(page.Transactions)),
	}
	if page.NextOffset != "" {
		out.SetNextOffset(page.NextOffset)
	}
	for _, txn := range page.Transactions {
		item := tg.WalletTransaction{
			ID:       strconv.FormatInt(txn.ID, 10),
			Amount:   txn.Amount,
			Date:     txn.Date,
			Incoming: txn.Amount > 0,
			Peer:     walletTransactionPeer(txn.Peer),
		}
		if txn.Comment != "" {
			item.SetComment(txn.Comment)
		}
		out.Transactions = append(out.Transactions, item)
	}
	return out, nil
}

func walletTransactionPeer(peer domain.Peer) tg.WalletTransactionPeerClass {
	if peer.Type == domain.PeerTypeUser && peer.ID > 0 {
		return &tg.WalletTransactionPeerUser{UserID: peer.ID}
	}
	return &tg.WalletTransactionPeerUnsupported{}
}

// provisionWallet creates the first wallet for an explicitly enabled deployment.
// A concurrent creation may win; only a successful reread can return that wallet.
// Missing keys, storage failures and invalid challenges remain errors.
func (r *Router) provisionWallet(ctx context.Context, uid int64, key [8]byte) (domain.WalletLink, int64, bool, error) {
	if r.deps.Wallet == nil {
		return domain.WalletLink{}, 0, false, walletapp.ErrWalletCreationDisabled
	}
	// Create accepts a challenge bound to this account and auth key, so the
	// challenge has to be issued here rather than expected from the client --
	// which is exactly the step the client was never taking.
	if _, err := r.deps.Wallet.GetProofChallenge(ctx, uid, key); err != nil {
		return domain.WalletLink{}, 0, false, err
	}
	link, balance, err := r.deps.Wallet.Create(ctx, uid, key, false)
	if err != nil {
		switch {
		case errors.Is(err, domain.ErrWalletProofInvalid), errors.Is(err, domain.ErrWalletPasswordRequired):
			// A concurrent request already consumed the challenge and committed
			// its own wallet. Re-read rather than mint a second one.
			existing, existingBalance, ok, stateErr := r.deps.Wallet.State(ctx, uid)
			if stateErr != nil {
				return domain.WalletLink{}, 0, false, stateErr
			}
			if ok {
				return existing, existingBalance, true, nil
			}
		}
		return domain.WalletLink{}, 0, false, err
	}
	return link, balance, true, nil
}

func (r *Router) onWalletAddresses(ctx context.Context, req *tg.WalletGetUserAddressesRequest) (any, error) {
	uid, _, err := r.walletCaller(ctx, "addresses", 60)
	if err != nil {
		return nil, err
	}
	// force only asks for a fresh read; we always read links straight from the
	// store, so it changes nothing -- but refusing it breaks clients that set
	// the bit (profile wallet rows among them).
	if req.Flags&^0x1 != 0 || len(req.ID) > 100 || len(req.Addresses) > 100 {
		return nil, inputRequestInvalidErr()
	}
	if r.deps.Users == nil {
		return nil, internalErr()
	}
	ids := make([]int64, 0, len(req.ID))
	validInputs := make([]tg.InputUserClass, 0, len(req.ID))
	for _, input := range req.ID {
		// An arbitrary bare ID must not bypass peer access-hash validation.
		if v, ok := input.(*tg.InputUser); ok && v.UserID != uid && v.AccessHash == 0 {
			return nil, userIDInvalidErr()
		}
		u, found, err := r.userFromInput(ctx, uid, input)
		if err != nil {
			return nil, internalErr()
		}
		if !found {
			return nil, userIDInvalidErr()
		}
		ids = append(ids, u.ID)
		validInputs = append(validInputs, input)
	}
	// Address-only lookup is restricted to the caller's own association. It
	// must not become an account-enumeration endpoint for arbitrary TON addresses.
	if len(ids) == 0 && len(req.Addresses) > 0 {
		ids = append(ids, uid)
		validInputs = append(validInputs, &tg.InputUserSelf{})
	}
	links, err := r.deps.Wallet.Links(ctx, ids)
	if err != nil {
		return nil, walletRPCError(err)
	}
	result := &tg.WalletUserAddresses{Addresses: []tg.WalletUserAddress{}, Users: []tg.UserClass{}}
	for _, link := range links {
		if len(req.ID) == 0 {
			match := false
			for _, a := range req.Addresses {
				if a == link.Address {
					match = true
				}
			}
			if !match {
				continue
			}
		}
		result.Addresses = append(result.Addresses, tg.WalletUserAddress{UserID: link.UserID, Address: link.Address, PublicKey: link.PublicKey})
	}
	result.Users, err = r.onUsersGetUsers(ctx, validInputs)
	if err != nil {
		return nil, err
	}
	return result, nil
}

func walletRPCError(err error) error {
	switch {
	case errors.Is(err, domain.ErrWalletProofInvalid):
		return tgerr.New(400, "WALLET_PROOF_INVALID")
	case errors.Is(err, domain.ErrWalletPasswordRequired):
		return tgerr.New(400, "PASSWORD_HASH_INVALID")
	case errors.Is(err, walletapp.ErrWalletCreationDisabled), errors.Is(err, walletapp.ErrWalletKeyFile):
		// telesrv cannot custody a private key (no master key configured). This is an
		// unavailable capability, not an internal error.
		return tgerr.New(400, "WALLET_UNAVAILABLE")
	case errors.Is(err, walletapp.ErrProviderRequest), errors.Is(err, domain.ErrStarsTransactionQueryInvalid):
		return inputRequestInvalidErr()
	case errors.Is(err, walletapp.ErrProviderResponse):
		return tgerr.New(502, "WALLET_PROVIDER_RESPONSE_INVALID")
	case errors.Is(err, walletapp.ErrProviderUnavailable):
		return tgerr.New(503, "WALLET_PROVIDER_UNAVAILABLE")
	}
	var httpErr *walletapp.ProviderHTTPError
	if errors.As(err, &httpErr) {
		if httpErr.Status == 429 {
			return floodWaitErr(1)
		}
		return tgerr.New(503, "WALLET_PROVIDER_UNAVAILABLE")
	}
	return internalErr()
}
