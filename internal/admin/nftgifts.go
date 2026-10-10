package admin

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"telesrv/internal/domain"
)

// ActionSetNftGiftWallet names the panel command that re-owns a minted
// collectible in the wallet ledger.
const ActionSetNftGiftWallet = "gifts.set_nft_wallet"

// UniqueGiftWalletStore resolves a unique gift by slug, NFT address or id and
// re-owns it in the wallet ledger; postgres.StarGiftClaimStore implements it.
type UniqueGiftWalletStore interface {
	ResolveGiftByRef(ctx context.Context, ref string) (domain.UniqueStarGift, bool, error)
	WalletBindGift(ctx context.Context, req domain.StarGiftWalletBind) (domain.UniqueStarGift, error)
	WalletReleaseGift(ctx context.Context, uniqueGiftID, actorUserID int64) (domain.UniqueStarGift, error)
}

// TonDNSResolver maps the .ton name an operator typed in place of an address
// to the wallet record it points at; tondns.Resolver implements it.
type TonDNSResolver interface {
	Resolve(ctx context.Context, name string) (string, error)
}

// UsernameWalletLookup answers which wallet the server can prove for a Telegram
// username: the wallet that paid for the collectible username mint, else the
// wallet the account verified through TON Connect. postgres.StarGiftClaimStore
// implements it.
type UsernameWalletLookup interface {
	WalletForUsername(
		ctx context.Context,
		username string) (address, source string, found bool, err error)
}

// SetNftGiftWalletRequest puts a collectible on an owner's TON wallet or puts
// it back on its Telegram peer. Clear is the release half of the same command,
// so the panel never has to guess which of two actions an empty form means.
type SetNftGiftWalletRequest struct {
	CommandMeta
	Ref           string `json:"ref"`
	Clear         bool   `json:"clear,omitempty"`
	WalletName    string `json:"wallet_name,omitempty"`
	WalletAddress string `json:"wallet_address,omitempty"`
	HostUserID    int64  `json:"host_user_id,omitempty"`
}

func (s *Service) SetNftGiftWallet(ctx context.Context, req SetNftGiftWalletRequest) (CommandResult, error) {
	if s == nil || s.uniqueGifts == nil {
		return CommandResult{}, fmt.Errorf("unique gift wallet store is not configured")
	}
	req.Ref = strings.TrimSpace(req.Ref)
	req.WalletName = strings.TrimSpace(req.WalletName)
	req.WalletAddress = strings.TrimSpace(req.WalletAddress)
	if req.Ref == "" || len(req.Ref) > 256 {
		return CommandResult{}, fmt.Errorf("ref is required and must be <= 256 bytes")
	}
	walletAddress := ""
	walletAlias := ""
	walletSource := ""
	if req.Clear {
		if req.WalletName != "" || req.WalletAddress != "" || req.HostUserID != 0 {
			return CommandResult{}, fmt.Errorf("clear must be sent without wallet_name, wallet_address or host_user_id")
		}
	} else {
		if len(req.WalletName) > 64 {
			return CommandResult{}, fmt.Errorf("wallet_name must be <= 64 bytes")
		}
		canonical, alias, source, err := s.resolveWalletAddress(ctx, req.WalletAddress)
		if err != nil {
			return CommandResult{}, err
		}
		walletAddress, walletAlias, walletSource = canonical, alias, source
		if req.WalletName == "" {
			req.WalletName = walletAlias
		}
		if req.WalletName == "" {
			return CommandResult{}, fmt.Errorf("wallet_name is required and must be <= 64 bytes")
		}
	}
	gift, found, err := s.uniqueGifts.ResolveGiftByRef(ctx, req.Ref)
	if err != nil {
		return CommandResult{}, err
	}
	if !found {
		return CommandResult{}, domain.ErrStarGiftNotFound
	}
	if gift.Burned {
		return CommandResult{}, domain.ErrStarGiftUnavailable
	}
	if !req.Clear && gift.GiftAddress != "" {
		return CommandResult{}, domain.ErrStarGiftUnavailable
	}
	hostID := req.HostUserID
	if hostID < 0 {
		return CommandResult{}, fmt.Errorf("host_user_id must not be negative")
	}
	// WHY: a console operator has no Telegram user id, so the ledger keeps the
	// relayer profile as actor while admin_commands records the operator, reason
	// and full request that actually authorized the change.
	return s.runCommand(ctx, req.CommandMeta, ActionSetNftGiftWallet, 0, gift.Owner, req, func() (CommandResult, error) {
		details := map[string]any{
			"ref":     req.Ref,
			"gift_id": strconv.FormatInt(gift.ID, 10),
			"slug":    gift.Slug,
		}
		if walletAlias != "" {
			details["wallet_alias"] = walletAlias
		}
		if walletSource != "" {
			details["wallet_source"] = walletSource
		}
		if req.Clear {
			if gift.OwnerAddress == "" {
				return CommandResult{}, domain.ErrStarGiftUnavailable
			}
			// WHY: the release response is an audit receipt, so it names the
			// wallet that gave the gift back, not the empty post-state.
			details["released_wallet"] = gift.OwnerAddress
			if req.DryRun {
				return CommandResult{Message: "wallet release validated", Details: details}, nil
			}
			released, err := s.uniqueGifts.WalletReleaseGift(ctx, gift.ID, domain.GiftRelayerUserID)
			if err != nil {
				return CommandResult{Message: "wallet release rejected", Details: details}, err
			}
			details["slug"] = released.Slug
			return CommandResult{Message: "collectible returned to its Telegram owner", Details: details}, nil
		}
		if req.DryRun {
			details["owner_name"] = req.WalletName
			details["owner_address"] = walletAddress
			details["host_user_id"] = strconv.FormatInt(hostID, 10)
			return CommandResult{Message: "wallet bind validated", Details: details}, nil
		}
		bound, err := s.uniqueGifts.WalletBindGift(ctx, domain.StarGiftWalletBind{
			UniqueGiftID:  gift.ID,
			WalletName:    req.WalletName,
			WalletAddress: walletAddress,
			ActorUserID:   domain.GiftRelayerUserID,
			HostPeerType:  "user",
			HostPeerID:    hostID,
		})
		if err != nil {
			return CommandResult{Message: "wallet bind rejected", Details: details}, err
		}
		details["owner_name"] = bound.OwnerName
		details["owner_address"] = bound.OwnerAddress
		details["host_user_id"] = strconv.FormatInt(bound.Host.ID, 10)
		return CommandResult{Message: "collectible bound to the TON wallet", Details: details}, nil
	})
}

// WHY: the operator may paste an address, a Telegram identity or a .ton name,
// and each means something different, so the ladder keeps them apart: an
// address is taken as is, an identity only ever resolves through flashfragment
// records (claim, mint, owner wallet, TON Connect), and a .ton name first tries
// on-chain DNS then falls back to the bare flashfragment username. No branch
// may turn the typed name itself into an address.
func (s *Service) resolveWalletAddress(
	ctx context.Context,
	raw string) (canonical, alias, source string, err error) {
	if canonical, err = domain.CanonicalTONAddress(raw); err == nil {
		return canonical, "", "", nil
	}
	var identityErr error
	if username, ok := domain.TelegramWalletInput(raw); ok && s.usernameWallets != nil {
		address, recordSource, found, lookupErr := s.usernameWallets.WalletForUsername(ctx, username)
		if lookupErr != nil {
			return "", "", "", fmt.Errorf("wallet_address %s: %w", username, lookupErr)
		}
		if found {
			canonical, err = domain.CanonicalTONAddress(address)
			if err != nil {
				return "", "", "", fmt.Errorf("wallet_address %s: recorded wallet is not a valid mainnet address", username)
			}
			return canonical, strings.TrimSpace(raw), recordSource, nil
		}
		identityErr = fmt.Errorf(
			"no verified wallet is on record in flashfragment for Telegram username %s",
			username)
	}
	var dnsErr error
	if name, ok := domain.TONDNSName(raw); ok {
		if s.tonDNS != nil {
			resolved, resolveErr := s.tonDNS.Resolve(ctx, name)
			if resolveErr != nil {
				dnsErr = fmt.Errorf("wallet_address %s: %s", name, resolveErr)
			} else if canonical, err = domain.CanonicalTONAddress(resolved); err != nil {
				return "", "", "", fmt.Errorf("wallet_address %s resolved to an invalid address", name)
			} else {
				return canonical, name, "ton dns", nil
			}
		}
		if candidate, ok := domain.TelegramWalletInput(
			strings.TrimSuffix(name, ".ton")); ok && s.usernameWallets != nil {
			address, recordSource, found, lookupErr := s.usernameWallets.WalletForUsername(ctx, candidate)
			if lookupErr != nil {
				return "", "", "", fmt.Errorf("wallet_address %s: %w", candidate, lookupErr)
			}
			if found {
				canonical, err = domain.CanonicalTONAddress(address)
				if err != nil {
					return "", "", "", fmt.Errorf(
						"wallet_address %s: recorded wallet is not a valid mainnet address",
						candidate)
				}
				return canonical, name, recordSource, nil
			}
			if dnsErr != nil {
				dnsErr = fmt.Errorf(
					"%v; flashfragment has no verified wallet for username %s either",
					dnsErr, candidate)
			}
		}
		if dnsErr == nil && identityErr == nil && s.tonDNS == nil {
			return "", "", "", fmt.Errorf("ton dns resolver is not configured")
		}
	}
	hint := "enter the wallet address (EQ… or 0:…) instead"
	switch {
	case identityErr != nil && dnsErr != nil:
		return "", "", "", fmt.Errorf("%v; %v; %s", identityErr, dnsErr, hint)
	case identityErr != nil:
		return "", "", "", fmt.Errorf("%w; %s", identityErr, hint)
	case dnsErr != nil:
		return "", "", "", fmt.Errorf("%w; %s", dnsErr, hint)
	}
	return "", "", "", fmt.Errorf(
		"wallet_address must be a TON mainnet address, a .ton name or a Telegram username")
}
