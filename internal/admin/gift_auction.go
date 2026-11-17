package admin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"telesrv/internal/domain"
)

var auctionReleaseSlug = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,47}$`)

// CreateStarGiftAuctionRequest creates a separate release; existing inventory is immutable.
type CreateStarGiftAuctionRequest struct {
	ReleasedBy     *string `json:"released_by_peer,omitempty"`
	UpgradeStars   *int64  `json:"upgrade_stars,omitempty"`
	PerUserTotal   *int    `json:"per_user_total,omitempty"`
	RequirePremium *bool   `json:"require_premium,omitempty"`
	Birthday       *bool   `json:"birthday,omitempty"`
	SortOrder      int     `json:"sort_order"`
	SourceSHA      string  `json:"source_sha256,omitempty"`
	CommandMeta
	SourceGiftID         int64  `json:"source_gift_id"`
	SourceRevisionID     int64  `json:"source_revision_id"`
	Title                string `json:"title"`
	Stars                int64  `json:"stars"`
	ConvertStars         int64  `json:"convert_stars"`
	Enabled              bool   `json:"enabled"`
	SupportOnly          bool   `json:"support_only"`
	AuctionSlug          string `json:"auction_slug"`
	AvailabilityTotal    int    `json:"availability_total"`
	GiftsPerRound        int    `json:"gifts_per_round"`
	AuctionStartDate     int    `json:"auction_start_date"`
	AuctionRoundDuration int    `json:"auction_round_duration"`
}

func (s *Service) CreateStarGiftAuction(ctx context.Context, req CreateStarGiftAuctionRequest) (CommandResult, error) {
	if s == nil || s.gifts == nil {
		return CommandResult{}, fmt.Errorf("star gift service is not configured")
	}
	return s.runCommand(ctx, req.CommandMeta, "create_star_gift_auction", 0, domain.Peer{}, req, func() (CommandResult, error) {
		return s.createStarGiftAuction(ctx, req)
	})
}

// Mutable template reads and clock checks belong after command replay.
func (s *Service) createStarGiftAuction(ctx context.Context, req CreateStarGiftAuctionRequest) (CommandResult, error) {
	req.Title = strings.TrimSpace(req.Title)
	req.AuctionSlug = strings.ToLower(strings.TrimSpace(req.AuctionSlug))
	if !auctionReleaseSlug.MatchString(req.AuctionSlug) {
		return CommandResult{}, domain.ErrStarGiftLifecycleInvalid
	}
	if req.AvailabilityTotal > 2147483647 || req.GiftsPerRound > 2147483647 || req.AuctionRoundDuration > 2147483647 || req.AuctionStartDate > 2147483647 || req.SourceGiftID <= 0 || req.SourceRevisionID <= 0 || req.Stars <= 0 || req.ConvertStars < 0 || req.ConvertStars > req.Stars || len([]rune(req.Title)) > domain.MaxStarGiftTitleRunes {
		return CommandResult{}, domain.ErrStarGiftInvalid
	}
	gifts, err := s.gifts.CatalogAll(ctx)
	if err != nil {
		return CommandResult{}, err
	}
	var source domain.StarGift
	for _, gift := range gifts {
		if gift.ID == req.SourceGiftID {
			source = gift
			break
		}
	}
	if source.ID == 0 {
		return CommandResult{}, domain.ErrStarGiftNotFound
	}
	if source.RevisionID != req.SourceRevisionID {
		return CommandResult{}, fmt.Errorf("SOURCE_GIFT_CHANGED: refresh the imported gift")
	}
	if req.Title == "" {
		req.Title = source.Title
	}
	write := domain.StarGiftCatalogBundleWrite{Catalog: domain.StarGiftCatalogWrite{
		Title: req.Title, Stars: req.Stars, ConvertStars: req.ConvertStars, Enabled: req.Enabled, SupportOnly: req.SupportOnly,
		Auction: true, ReserveAuctionSlug: true, AuctionSlug: req.AuctionSlug, AvailabilityTotal: req.AvailabilityTotal, GiftsPerRound: req.GiftsPerRound,
		AuctionStartDate: req.AuctionStartDate, AuctionRoundDuration: req.AuctionRoundDuration,
		Birthday: source.Birthday, RequirePremium: source.RequirePremium, ReleasedBy: source.ReleasedBy,
		LimitedPerUser: source.LimitedPerUser, PerUserTotal: source.PerUserTotal, PeerColorAvailable: source.PeerColorAvailable,
		UpgradeVariants: source.UpgradeVariants, Background: source.Background, Actor: req.Actor, CommandID: req.CommandID,
	}}
	if req.SortOrder < -2147483648 || req.SortOrder > 2147483647 {
		return CommandResult{}, domain.ErrStarGiftInvalid
	}
	write.Catalog.SortOrder = req.SortOrder
	if req.ReleasedBy != nil {
		peer, err := s.resolveReleasedBy(ctx, *req.ReleasedBy)
		if err != nil {
			return CommandResult{}, err
		}
		write.Catalog.ReleasedBy = peer
	}
	if req.PerUserTotal != nil {
		if *req.PerUserTotal < 0 || *req.PerUserTotal > 2147483647 {
			return CommandResult{}, domain.ErrStarGiftInvalid
		}
		write.Catalog.PerUserTotal = *req.PerUserTotal
		write.Catalog.LimitedPerUser = *req.PerUserTotal > 0
	}
	if req.RequirePremium != nil {
		write.Catalog.RequirePremium = *req.RequirePremium
	}
	if req.Birthday != nil {
		write.Catalog.Birthday = *req.Birthday
	}
	if err = write.Catalog.ValidateLifecycleAuthoring(int(s.now().Unix())); err != nil {
		return CommandResult{}, err
	}
	raw, ok, err := s.gifts.AnimationJSON(ctx, source.ID)
	if err != nil {
		return CommandResult{}, err
	}
	if !ok {
		return CommandResult{}, domain.ErrStarGiftNotFound
	}
	write.Catalog.Animation, err = s.gifts.PrepareOfficialAnimation("imported.json", raw)
	if err != nil {
		return CommandResult{}, err
	}
	pool, hasPool, err := s.gifts.CollectiblePreview(ctx, source.ID)
	if err != nil {
		return CommandResult{}, err
	}
	if hasPool {
		collectible := domain.StarGiftCollectibleWrite{UpgradeStars: pool.UpgradeStars, SupplyTotal: req.AvailabilityTotal, SlugPrefix: req.AuctionSlug, Actor: req.Actor, CommandID: req.CommandID,
			Models: append([]domain.StarGiftCollectibleAttribute(nil), pool.Models...), Patterns: append([]domain.StarGiftCollectibleAttribute(nil), pool.Patterns...), Backdrops: append([]domain.StarGiftCollectibleAttribute(nil), pool.Backdrops...)}
		if req.UpgradeStars != nil {
			if *req.UpgradeStars <= 0 {
				return CommandResult{}, domain.ErrStarGiftCollectibleInvalid
			}
			collectible.UpgradeStars = *req.UpgradeStars
		}
		for _, attributes := range [][]domain.StarGiftCollectibleAttribute{collectible.Models, collectible.Patterns} {
			for i := range attributes {
				attr := &attributes[i]
				data, found, e := s.gifts.CollectibleAnimationJSON(ctx, source.ID, attr.Kind, attr.ID)
				if e != nil {
					return CommandResult{}, e
				}
				if !found {
					return CommandResult{}, domain.ErrStarGiftCollectibleInvalid
				}
				animation, e := s.gifts.PrepareOfficialAnimation(string(attr.Kind)+".json", data)
				if e != nil {
					return CommandResult{}, e
				}
				attr.Animation = &animation
				attr.Document = nil
				attr.Blob = nil
			}
		}
		// Validate before any blob or database write, including during dry runs.
		validation := collectible
		validation.GiftID = 1
		if err = domain.ValidateStarGiftCollectibleDraft(validation); err != nil {
			return CommandResult{}, err
		}
		write.Collectible = &collectible
	}
	// Reject a source switched while its animation and attribute assets were being read.
	current, err := s.gifts.CatalogAll(ctx)
	if err != nil {
		return CommandResult{}, err
	}
	sourceCurrent := false
	for _, gift := range current {
		if gift.ID == source.ID && gift.RevisionID == source.RevisionID {
			sourceCurrent = true
			break
		}
	}
	currentPool, currentHasPool, err := s.gifts.CollectiblePreview(ctx, source.ID)
	if err != nil {
		return CommandResult{}, err
	}
	if !sourceCurrent || currentHasPool != hasPool || (hasPool && currentPool.Revision != pool.Revision) {
		return CommandResult{}, fmt.Errorf("SOURCE_GIFT_CHANGED: validate the release again")
	}
	// Bind confirmation to all selected assets and pool metadata, not only the form.
	snapshot := write
	snapshot.Catalog.Actor, snapshot.Catalog.CommandID = "", ""
	if write.Collectible != nil {
		copy := *write.Collectible
		copy.Actor, copy.CommandID = "", ""
		snapshot.Collectible = &copy
	}
	fingerprint, e := json.Marshal(snapshot)
	if e != nil {
		return CommandResult{}, e
	}
	sum := sha256.Sum256(fingerprint)
	sourceSHA := hex.EncodeToString(sum[:])
	if !req.DryRun && req.SourceSHA != sourceSHA {
		return CommandResult{}, fmt.Errorf("SOURCE_GIFT_CHANGED: validate the release again")
	}

	for _, gift := range gifts {
		if gift.Auction && strings.EqualFold(gift.AuctionSlug, req.AuctionSlug) {
			return CommandResult{}, fmt.Errorf("AUCTION_SLUG_IN_USE")
		}
	}
	details := map[string]any{"source_gift_id": strconv.FormatInt(source.ID, 10), "title": req.Title, "auction_slug": req.AuctionSlug, "availability_total": req.AvailabilityTotal, "gifts_per_round": req.GiftsPerRound, "stars": strconv.FormatInt(req.Stars, 10), "includes_collectibles": hasPool, "source_sha256": sourceSHA}
	if hasPool {
		details["models"] = len(pool.Models)
		details["patterns"] = len(pool.Patterns)
		details["backdrops"] = len(pool.Backdrops)
		details["upgrade_stars"] = strconv.FormatInt(write.Collectible.UpgradeStars, 10)
	}
	if req.DryRun {
		return CommandResult{Message: "auction release validated", Details: details}, nil
	}
	write.Catalog.NormalizeLifecycleAuthoring(int(s.now().Unix()))
	result, err := s.gifts.CreateCatalogBundle(ctx, write)
	if err != nil {
		return CommandResult{Details: details}, err
	}
	details["gift_id"] = strconv.FormatInt(result.Catalog.Gift.ID, 10)
	return CommandResult{Message: "auction release created", Details: details}, nil
}
