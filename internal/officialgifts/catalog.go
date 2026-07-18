// Package officialgifts reads the local, immutable snapshot produced by cmd/giftfetch.
// It never performs network I/O. Selected document bytes are accepted only after their
// manifest size and SHA-256 have been verified beneath the configured snapshot root.
package officialgifts

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

const manifestSchema = 2

var (
	ErrUnavailable = errors.New("official gifts snapshot is unavailable")
	ErrInvalid     = errors.New("official gifts snapshot is invalid")
	ErrNotFound    = errors.New("official gift not found")
)

type Catalog struct {
	root string
	once sync.Once
	snap *snapshot
	err  error
}

type GiftSummary struct {
	ID                  int64
	Title               string
	Stars               int64
	ConvertStars        int64
	UpgradeStars        int64
	AvailabilityTotal   int
	AvailabilityRemains int
	AvailabilityResale  int64
	Limited             bool
	SoldOut             bool
	Birthday            bool
	RequirePremium      bool
	LimitedPerUser      bool
	PeerColorAvailable  bool
	Auction             bool
	FirstSaleDate       int
	LastSaleDate        int
	ResellMinStars      int64
	PerUserTotal        int
	PerUserRemains      int
	LockedUntilDate     int
	AuctionSlug         string
	GiftsPerRound       int
	AuctionStartDate    int
	UpgradeVariants     int
	Background          *Background
	ModelCount          int
	PatternCount        int
	BackdropCount       int
	CraftedModelCount   int
	DocumentID          int64
	AnimationValidated  bool
}

// CanUpgrade reports whether this snapshot contains the complete immutable
// attribute pool and a positive official upgrade price required to mint a
// collectible from the regular gift.
func (g GiftSummary) CanUpgrade() bool {
	return g.UpgradeStars > 0 && g.ModelCount > 0 && g.PatternCount > 0 && g.BackdropCount > 0
}

// CanCraft reports whether upgraded collectibles from this gift can reach an
// official craft-only model. Craft is not advertised without a valid upgrade
// path even if a malformed snapshot were to contain a crafted model.
func (g GiftSummary) CanCraft() bool {
	return g.CanUpgrade() && g.CraftedModelCount > 0
}

type Bundle struct {
	ManifestSHA256 []byte
	SourceJSON     []byte
	Gift           Gift
	BaseDocument   Document
	Collectible    *CollectibleSet
}

type Gift struct {
	ID                  int64
	Title               string
	Stars               int64
	ConvertStars        int64
	UpgradeStars        int64
	AvailabilityTotal   int
	AvailabilityRemains int
	Limited             bool
	SoldOut             bool
	Birthday            bool
	RequirePremium      bool
	LimitedPerUser      bool
	PeerColorAvailable  bool
	Auction             bool
	AvailabilityResale  int64
	FirstSaleDate       int
	LastSaleDate        int
	ResellMinStars      int64
	PerUserTotal        int
	PerUserRemains      int
	LockedUntilDate     int
	AuctionSlug         string
	GiftsPerRound       int
	AuctionStartDate    int
	UpgradeVariants     int
	Background          *Background
	DocumentID          int64
}

type Background struct {
	CenterColor int `json:"center_color"`
	EdgeColor   int `json:"edge_color"`
	TextColor   int `json:"text_color"`
}

type CollectibleSet struct {
	Models    []Model
	Patterns  []Pattern
	Backdrops []Backdrop
}

type Rarity struct {
	Kind     string `json:"kind"`
	Permille *int   `json:"permille,omitempty"`
}

type Model struct {
	Name       string
	DocumentID int64
	Crafted    bool
	Rarity     Rarity
	Document   Document
}

type Pattern struct {
	Name       string
	DocumentID int64
	Rarity     Rarity
	Document   Document
}

type Backdrop struct {
	Name         string
	BackdropID   int
	CenterColor  int
	EdgeColor    int
	PatternColor int
	TextColor    int
	Rarity       Rarity
}

type Document struct {
	ID                 int64
	FileName           string
	Path               string
	Size               int64
	SHA256             string
	AnimationValidated bool
	ValidationError    string
	Data               []byte
}

type manifest struct {
	Schema               int                   `json:"schema"`
	GiftCount            int                   `json:"gift_count"`
	Gifts                []giftManifest        `json:"gifts"`
	UpgradeAttributeSets []collectibleManifest `json:"upgrade_attribute_sets"`
	Documents            []documentManifest    `json:"documents"`
}

type giftManifest struct {
	Index               int         `json:"index"`
	Kind                string      `json:"kind"`
	ID                  int64       `json:"id"`
	Title               string      `json:"title"`
	Stars               int64       `json:"stars"`
	ConvertStars        int64       `json:"convert_stars"`
	UpgradeStars        int64       `json:"upgrade_stars"`
	Limited             bool        `json:"limited"`
	SoldOut             bool        `json:"sold_out"`
	Birthday            bool        `json:"birthday"`
	RequirePremium      bool        `json:"require_premium"`
	LimitedPerUser      bool        `json:"limited_per_user"`
	PeerColorAvailable  bool        `json:"peer_color_available"`
	Auction             bool        `json:"auction"`
	AvailabilityRemains int         `json:"availability_remains"`
	AvailabilityTotal   int         `json:"availability_total"`
	AvailabilityResale  int64       `json:"availability_resale"`
	FirstSaleDate       int         `json:"first_sale_date"`
	LastSaleDate        int         `json:"last_sale_date"`
	ResellMinStars      int64       `json:"resell_min_stars"`
	PerUserTotal        int         `json:"per_user_total"`
	PerUserRemains      int         `json:"per_user_remains"`
	LockedUntilDate     int         `json:"locked_until_date"`
	AuctionSlug         string      `json:"auction_slug"`
	GiftsPerRound       int         `json:"gifts_per_round"`
	AuctionStartDate    int         `json:"auction_start_date"`
	UpgradeVariants     int         `json:"upgrade_variants"`
	Background          *Background `json:"background"`
	DocumentIDs         []int64     `json:"document_ids"`
	SourceJSON          []byte      `json:"-"`
}

func (g *giftManifest) UnmarshalJSON(data []byte) error {
	type plain giftManifest
	var value plain
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	*g = giftManifest(value)
	var compact bytes.Buffer
	if err := json.Compact(&compact, data); err != nil {
		return err
	}
	g.SourceJSON = append([]byte(nil), compact.Bytes()...)
	return nil
}

type collectibleManifest struct {
	GiftID64       int64              `json:"gift_id"`
	AttributeCount int                `json:"attribute_count"`
	Models         []modelManifest    `json:"models"`
	Patterns       []patternManifest  `json:"patterns"`
	Backdrops      []backdropManifest `json:"backdrops"`
}

type modelManifest struct {
	Name       string `json:"name"`
	DocumentID int64  `json:"document_id"`
	Crafted    bool   `json:"crafted"`
	Rarity     Rarity `json:"rarity"`
}

type patternManifest struct {
	Name       string `json:"name"`
	DocumentID int64  `json:"document_id"`
	Rarity     Rarity `json:"rarity"`
}

type backdropManifest struct {
	Name         string `json:"name"`
	BackdropID   int    `json:"backdrop_id"`
	CenterColor  int    `json:"center_color"`
	EdgeColor    int    `json:"edge_color"`
	PatternColor int    `json:"pattern_color"`
	TextColor    int    `json:"text_color"`
	Rarity       Rarity `json:"rarity"`
}

type documentManifest struct {
	ID64               int64        `json:"id"`
	FileName           string       `json:"file_name"`
	File               fileArtifact `json:"file"`
	AnimationValidated bool         `json:"animation_validated"`
	ValidationError    string       `json:"validation_error"`
}

type fileArtifact struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type snapshot struct {
	manifestSHA []byte
	gifts       map[int64]giftManifest
	sets        map[int64]collectibleManifest
	documents   map[int64]documentManifest
	ordered     []int64
}

func New(root string) *Catalog {
	return &Catalog{root: strings.TrimSpace(root)}
}

func (c *Catalog) List(ctx context.Context) ([]GiftSummary, error) {
	snap, err := c.load()
	if err != nil {
		return nil, err
	}
	out := make([]GiftSummary, 0, len(snap.ordered))
	for _, id := range snap.ordered {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		gift := snap.gifts[id]
		doc := snap.documents[gift.DocumentIDs[0]]
		summary := GiftSummary{
			ID: id, Title: gift.Title, Stars: gift.Stars, ConvertStars: gift.ConvertStars,
			UpgradeStars: gift.UpgradeStars, AvailabilityTotal: gift.AvailabilityTotal,
			AvailabilityRemains: gift.AvailabilityRemains, AvailabilityResale: gift.AvailabilityResale,
			Limited: gift.Limited, SoldOut: gift.SoldOut, Birthday: gift.Birthday,
			RequirePremium: gift.RequirePremium, LimitedPerUser: gift.LimitedPerUser,
			PeerColorAvailable: gift.PeerColorAvailable, Auction: gift.Auction,
			FirstSaleDate: gift.FirstSaleDate, LastSaleDate: gift.LastSaleDate,
			ResellMinStars: gift.ResellMinStars, PerUserTotal: gift.PerUserTotal,
			PerUserRemains: gift.PerUserRemains, LockedUntilDate: gift.LockedUntilDate,
			AuctionSlug: gift.AuctionSlug, GiftsPerRound: gift.GiftsPerRound,
			AuctionStartDate: gift.AuctionStartDate, UpgradeVariants: gift.UpgradeVariants,
			Background: cloneBackground(gift.Background), DocumentID: doc.ID64,
			AnimationValidated: doc.AnimationValidated,
		}
		if set, ok := snap.sets[id]; ok {
			summary.ModelCount, summary.PatternCount, summary.BackdropCount = len(set.Models), len(set.Patterns), len(set.Backdrops)
			for _, model := range set.Models {
				if model.Crafted {
					summary.CraftedModelCount++
				}
			}
		}
		out = append(out, summary)
	}
	return out, nil
}

func (c *Catalog) Bundle(ctx context.Context, giftID int64, includeCollectible bool) (Bundle, error) {
	snap, err := c.load()
	if err != nil {
		return Bundle{}, err
	}
	gift, ok := snap.gifts[giftID]
	if !ok {
		return Bundle{}, ErrNotFound
	}
	base, err := c.readDocument(ctx, snap.documents[gift.DocumentIDs[0]])
	if err != nil {
		return Bundle{}, fmt.Errorf("base document %d: %w", gift.DocumentIDs[0], err)
	}
	out := Bundle{
		ManifestSHA256: append([]byte(nil), snap.manifestSHA...),
		SourceJSON:     append([]byte(nil), gift.SourceJSON...),
		Gift: Gift{ID: gift.ID, Title: gift.Title, Stars: gift.Stars, ConvertStars: gift.ConvertStars,
			UpgradeStars: gift.UpgradeStars, AvailabilityTotal: gift.AvailabilityTotal,
			AvailabilityRemains: gift.AvailabilityRemains, Limited: gift.Limited, SoldOut: gift.SoldOut,
			Birthday: gift.Birthday, RequirePremium: gift.RequirePremium, LimitedPerUser: gift.LimitedPerUser,
			PeerColorAvailable: gift.PeerColorAvailable, Auction: gift.Auction,
			AvailabilityResale: gift.AvailabilityResale, FirstSaleDate: gift.FirstSaleDate,
			LastSaleDate: gift.LastSaleDate, ResellMinStars: gift.ResellMinStars,
			PerUserTotal: gift.PerUserTotal, PerUserRemains: gift.PerUserRemains,
			LockedUntilDate: gift.LockedUntilDate, AuctionSlug: gift.AuctionSlug,
			GiftsPerRound: gift.GiftsPerRound, AuctionStartDate: gift.AuctionStartDate,
			UpgradeVariants: gift.UpgradeVariants, Background: cloneBackground(gift.Background),
			DocumentID: gift.DocumentIDs[0]},
		BaseDocument: base,
	}
	if !includeCollectible {
		return out, nil
	}
	set, ok := snap.sets[giftID]
	if !ok {
		return Bundle{}, fmt.Errorf("%w: gift %d has no collectible attribute set", ErrInvalid, giftID)
	}
	collectible := &CollectibleSet{
		Models: make([]Model, 0, len(set.Models)), Patterns: make([]Pattern, 0, len(set.Patterns)),
		Backdrops: make([]Backdrop, 0, len(set.Backdrops)),
	}
	for _, value := range set.Models {
		doc, err := c.readDocument(ctx, snap.documents[value.DocumentID])
		if err != nil {
			return Bundle{}, fmt.Errorf("model %q document %d: %w", value.Name, value.DocumentID, err)
		}
		collectible.Models = append(collectible.Models, Model{Name: value.Name, DocumentID: value.DocumentID, Crafted: value.Crafted, Rarity: value.Rarity, Document: doc})
	}
	for _, value := range set.Patterns {
		doc, err := c.readDocument(ctx, snap.documents[value.DocumentID])
		if err != nil {
			return Bundle{}, fmt.Errorf("pattern %q document %d: %w", value.Name, value.DocumentID, err)
		}
		collectible.Patterns = append(collectible.Patterns, Pattern{Name: value.Name, DocumentID: value.DocumentID, Rarity: value.Rarity, Document: doc})
	}
	for _, value := range set.Backdrops {
		collectible.Backdrops = append(collectible.Backdrops, Backdrop{Name: value.Name, BackdropID: value.BackdropID,
			CenterColor: value.CenterColor, EdgeColor: value.EdgeColor, PatternColor: value.PatternColor,
			TextColor: value.TextColor, Rarity: value.Rarity})
	}
	out.Collectible = collectible
	return out, nil
}

func cloneBackground(value *Background) *Background {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func (c *Catalog) load() (*snapshot, error) {
	c.once.Do(func() {
		if c.root == "" {
			c.err = ErrUnavailable
			return
		}
		manifestPath := filepath.Join(c.root, "manifest.json")
		raw, err := os.ReadFile(manifestPath)
		if err != nil {
			c.err = fmt.Errorf("%w: %v", ErrUnavailable, err)
			return
		}
		var value manifest
		decoder := json.NewDecoder(bytes.NewReader(raw))
		if err := decoder.Decode(&value); err != nil {
			c.err = fmt.Errorf("%w: decode manifest: %v", ErrInvalid, err)
			return
		}
		if value.Schema != manifestSchema || value.GiftCount != len(value.Gifts) || len(value.Gifts) == 0 {
			c.err = fmt.Errorf("%w: unexpected schema or gift count", ErrInvalid)
			return
		}
		sum := sha256.Sum256(raw)
		snap := &snapshot{manifestSHA: append([]byte(nil), sum[:]...), gifts: make(map[int64]giftManifest, len(value.Gifts)),
			sets: make(map[int64]collectibleManifest, len(value.UpgradeAttributeSets)), documents: make(map[int64]documentManifest, len(value.Documents))}
		for _, doc := range value.Documents {
			if doc.ID64 <= 0 || doc.File.Size <= 0 || len(doc.File.SHA256) != 64 || strings.TrimSpace(doc.File.Path) == "" {
				c.err = fmt.Errorf("%w: invalid document %d", ErrInvalid, doc.ID64)
				return
			}
			if _, duplicate := snap.documents[doc.ID64]; duplicate {
				c.err = fmt.Errorf("%w: duplicate document %d", ErrInvalid, doc.ID64)
				return
			}
			snap.documents[doc.ID64] = doc
		}
		for _, gift := range value.Gifts {
			if gift.Kind != "regular" || gift.ID <= 0 || gift.Stars <= 0 || len(gift.DocumentIDs) != 1 {
				c.err = fmt.Errorf("%w: invalid gift %d", ErrInvalid, gift.ID)
				return
			}
			if _, ok := snap.documents[gift.DocumentIDs[0]]; !ok {
				c.err = fmt.Errorf("%w: gift %d document missing", ErrInvalid, gift.ID)
				return
			}
			if _, duplicate := snap.gifts[gift.ID]; duplicate {
				c.err = fmt.Errorf("%w: duplicate gift %d", ErrInvalid, gift.ID)
				return
			}
			snap.gifts[gift.ID] = gift
			snap.ordered = append(snap.ordered, gift.ID)
		}
		for _, set := range value.UpgradeAttributeSets {
			if _, ok := snap.gifts[set.GiftID64]; !ok || set.AttributeCount != len(set.Models)+len(set.Patterns)+len(set.Backdrops) ||
				len(set.Models) == 0 || len(set.Patterns) == 0 || len(set.Backdrops) == 0 {
				c.err = fmt.Errorf("%w: invalid collectible set %d", ErrInvalid, set.GiftID64)
				return
			}
			if _, duplicate := snap.sets[set.GiftID64]; duplicate {
				c.err = fmt.Errorf("%w: duplicate collectible set %d", ErrInvalid, set.GiftID64)
				return
			}
			for _, model := range set.Models {
				if _, ok := snap.documents[model.DocumentID]; !ok || !validRarity(model.Rarity, model.Crafted) {
					c.err = fmt.Errorf("%w: invalid model %q", ErrInvalid, model.Name)
					return
				}
			}
			for _, pattern := range set.Patterns {
				if _, ok := snap.documents[pattern.DocumentID]; !ok || !validRarity(pattern.Rarity, false) {
					c.err = fmt.Errorf("%w: invalid pattern %q", ErrInvalid, pattern.Name)
					return
				}
			}
			for _, backdrop := range set.Backdrops {
				if backdrop.BackdropID < 0 || !validRarity(backdrop.Rarity, false) {
					c.err = fmt.Errorf("%w: invalid backdrop %q", ErrInvalid, backdrop.Name)
					return
				}
			}
			snap.sets[set.GiftID64] = set
		}
		sort.SliceStable(snap.ordered, func(i, j int) bool { return snap.gifts[snap.ordered[i]].Index < snap.gifts[snap.ordered[j]].Index })
		c.snap = snap
	})
	return c.snap, c.err
}

func validRarity(rarity Rarity, crafted bool) bool {
	if rarity.Kind == "permille" {
		return !crafted && rarity.Permille != nil && *rarity.Permille > 0 && *rarity.Permille <= 1000
	}
	return crafted && rarity.Permille == nil && (rarity.Kind == "uncommon" || rarity.Kind == "rare" || rarity.Kind == "epic" || rarity.Kind == "legendary")
}

func (c *Catalog) readDocument(ctx context.Context, doc documentManifest) (Document, error) {
	if err := ctx.Err(); err != nil {
		return Document{}, err
	}
	root, err := filepath.Abs(c.root)
	if err != nil {
		return Document{}, err
	}
	clean := filepath.Clean(filepath.FromSlash(doc.File.Path))
	if filepath.IsAbs(clean) {
		return Document{}, ErrInvalid
	}
	full := filepath.Join(root, clean)
	rel, err := filepath.Rel(root, full)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return Document{}, ErrInvalid
	}
	data, err := os.ReadFile(full)
	if err != nil {
		return Document{}, err
	}
	if int64(len(data)) != doc.File.Size {
		return Document{}, fmt.Errorf("%w: size mismatch", ErrInvalid)
	}
	sum := sha256.Sum256(data)
	if !strings.EqualFold(hex.EncodeToString(sum[:]), doc.File.SHA256) {
		return Document{}, fmt.Errorf("%w: sha256 mismatch", ErrInvalid)
	}
	name := strings.TrimSpace(doc.FileName)
	if name == "" {
		name = filepath.Base(clean)
	}
	return Document{ID: doc.ID64, FileName: name, Path: doc.File.Path, Size: doc.File.Size,
		SHA256: strings.ToLower(doc.File.SHA256), AnimationValidated: doc.AnimationValidated,
		ValidationError: doc.ValidationError, Data: data}, nil
}
