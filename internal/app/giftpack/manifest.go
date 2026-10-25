// Adapted from owpengram-server dev@55c3e84 under Apache-2.0; see NOTICE.md.
// Package giftpack validates portable ZIP gift packs and prepares catalog
// bundles for the existing star gift write path.
package giftpack

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"telesrv/internal/domain"
)

// Manifest is the parsed contents of a pack's pack.json.
type Manifest struct {
	PackName string     `json:"pack_name"`
	Author   string     `json:"author,omitempty"`
	Gifts    []GiftSpec `json:"gifts"`
}

// GiftSpec describes one gift, mirroring the fields of
// domain.StarGiftCatalogWrite that a pack author may set. Animation paths
// are resolved against the pack's AssetResolver; colors are "#RRGGBB" hex
// strings for authoring ergonomics (domain wants a packed 24-bit int).
type GiftSpec struct {
	IDSlug               string       `json:"id_slug"`
	Title                string       `json:"title"`
	Stars                int64        `json:"stars"`
	ConvertStars         int64        `json:"convert_stars"`
	BaseAnimation        string       `json:"base_animation"`
	Limited              bool         `json:"limited,omitempty"`
	AvailabilityTotal    int          `json:"availability_total,omitempty"`
	Birthday             bool         `json:"birthday,omitempty"`
	RequirePremium       bool         `json:"require_premium,omitempty"`
	SupportOnly          bool         `json:"support_only,omitempty"`
	LimitedPerUser       bool         `json:"limited_per_user,omitempty"`
	PerUserTotal         int          `json:"per_user_total,omitempty"`
	ResellMinStars       int64        `json:"resell_min_stars,omitempty"`
	Auction              bool         `json:"auction,omitempty"`
	AuctionSlug          string       `json:"auction_slug,omitempty"`
	GiftsPerRound        int          `json:"gifts_per_round,omitempty"`
	AuctionStartDate     int          `json:"auction_start_date,omitempty"`
	AuctionRoundDuration int          `json:"auction_round_duration,omitempty"`
	Upgrade              *UpgradeSpec `json:"upgrade,omitempty"`
}

// UpgradeSpec describes the optional collectible attribute pool (model /
// pattern / backdrop) a gift can be upgraded into.
type UpgradeSpec struct {
	UpgradeStars int64          `json:"upgrade_stars"`
	SupplyTotal  int            `json:"supply_total"`
	SlugPrefix   string         `json:"slug_prefix"`
	Models       []AttrSpec     `json:"models"`
	Patterns     []AttrSpec     `json:"patterns"`
	Backdrops    []BackdropSpec `json:"backdrops"`
}

// AttrSpec is one model or pattern option. Rarity defaults to "permille"
// (the only kind eligible for the regular random-draw upgrade) when left
// empty; Crafted is only legal on a model, and a crafted attribute must
// carry Permille 0 (it's obtained through crafting, never the random draw).
type AttrSpec struct {
	Name      string `json:"name"`
	Animation string `json:"animation"`
	Permille  int    `json:"permille"`
	Crafted   bool   `json:"crafted,omitempty"`
	Rarity    string `json:"rarity,omitempty"`
}

// BackdropSpec is one backdrop option: colors only, no animation.
type BackdropSpec struct {
	Name     string `json:"name"`
	Center   string `json:"center"`
	Edge     string `json:"edge"`
	Pattern  string `json:"pattern"`
	Text     string `json:"text"`
	Permille int    `json:"permille"`
}

// ParseManifest decodes and structurally validates a pack.json. It checks
// that required fields are present and paths are non-empty -- the animation
// bytes themselves are only validated later, per-file, by
// stargifts.Service.PrepareAnimation.
func ParseManifest(data []byte) (Manifest, error) {
	var m Manifest
	if len(data) == 0 || len(data) > maxManifestBytes {
		return Manifest{}, fmt.Errorf("pack manifest exceeds size limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&m); err != nil {
		return Manifest{}, fmt.Errorf("parse pack manifest: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return Manifest{}, fmt.Errorf("pack manifest contains trailing JSON")
	}
	if strings.TrimSpace(m.PackName) == "" || len([]rune(m.PackName)) > 128 || len([]rune(m.Author)) > 128 {
		return Manifest{}, fmt.Errorf("pack manifest: pack_name is required and name/author must be <= 128 characters")
	}
	if len(m.Gifts) == 0 || len(m.Gifts) > 50 {
		return Manifest{}, fmt.Errorf("pack manifest: 1..50 gifts are required")
	}
	seenTitles := make(map[string]bool, len(m.Gifts))
	seenSlugs := make(map[string]bool, len(m.Gifts))
	for i, g := range m.Gifts {
		if strings.TrimSpace(g.Title) == "" {
			return Manifest{}, fmt.Errorf("pack manifest: gifts[%d].title is required", i)
		}
		if len([]rune(strings.TrimSpace(g.Title))) > domain.MaxStarGiftTitleRunes {
			return Manifest{}, fmt.Errorf("pack manifest: gifts[%d].title is too long", i)
		}
		key := strings.ToLower(strings.TrimSpace(g.Title))
		if seenTitles[key] {
			return Manifest{}, fmt.Errorf("pack manifest: duplicate gift title %q", g.Title)
		}
		seenTitles[key] = true
		if g.Stars <= 0 {
			return Manifest{}, fmt.Errorf("pack manifest: gift %q: stars must be > 0", g.Title)
		}
		if g.ConvertStars < 0 || g.ConvertStars > g.Stars {
			return Manifest{}, fmt.Errorf("pack manifest: gift %q: convert_stars must be between 0 and stars", g.Title)
		}
		if !validAssetPath(g.BaseAnimation) || g.BaseAnimation == "pack.json" {
			return Manifest{}, fmt.Errorf("pack manifest: gift %q: base_animation is required", g.Title)
		}
		if g.AvailabilityTotal < 0 || g.PerUserTotal < 0 || g.ResellMinStars < 0 ||
			(g.Limited && g.AvailabilityTotal == 0) ||
			(g.LimitedPerUser && g.PerUserTotal == 0) || (!g.LimitedPerUser && g.PerUserTotal > 0) {
			return Manifest{}, fmt.Errorf("pack manifest: gift %q: invalid supply or per-user limit", g.Title)
		}
		if g.Upgrade == nil {
			continue
		}
		u := g.Upgrade
		if strings.TrimSpace(u.SlugPrefix) == "" || u.UpgradeStars <= 0 || u.SupplyTotal <= 0 {
			return Manifest{}, fmt.Errorf("pack manifest: gift %q: upgrade.slug_prefix is required", g.Title)
		}
		slug := strings.ToLower(strings.TrimSpace(u.SlugPrefix))
		if seenSlugs[slug] {
			return Manifest{}, fmt.Errorf("pack manifest: duplicate upgrade slug_prefix %q", u.SlugPrefix)
		}
		seenSlugs[slug] = true
		if len(u.Models) == 0 || len(u.Patterns) == 0 || len(u.Backdrops) == 0 {
			return Manifest{}, fmt.Errorf("pack manifest: gift %q: upgrade needs at least one model, pattern and backdrop", g.Title)
		}
		if len(u.Models) > domain.MaxStarGiftCollectibleAttributesPerKind ||
			len(u.Patterns) > domain.MaxStarGiftCollectibleAttributesPerKind ||
			len(u.Backdrops) > domain.MaxStarGiftCollectibleAttributesPerKind {
			return Manifest{}, fmt.Errorf("pack manifest: gift %q: too many upgrade attributes", g.Title)
		}
		for _, attrs := range [][]AttrSpec{u.Models, u.Patterns} {
			for _, a := range attrs {
				if strings.TrimSpace(a.Name) == "" || !validAssetPath(a.Animation) || a.Animation == "pack.json" {
					return Manifest{}, fmt.Errorf("pack manifest: gift %q: attribute name and animation are required", g.Title)
				}
			}
		}
		for _, b := range u.Backdrops {
			if strings.TrimSpace(b.Name) == "" {
				return Manifest{}, fmt.Errorf("pack manifest: gift %q: backdrop name is required", g.Title)
			}
		}
	}
	return m, nil
}
