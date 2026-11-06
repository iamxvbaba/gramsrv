// Command giftfetch snapshots the official Telegram star-gift catalog, the
// complete current upgrade-attribute pools, and all document resources
// referenced by either response. It is a read-only fetcher: it never imports
// data into telesrv and never copies the authorization session.
//
// Usage:
//
//	SESSION=/path/to/session giftfetch -out <directory>
//	SESSION=/path/to/session giftfetch -login-qr
//
// payments.getStarGifts only answers for an authorized *user* session, so API
// credentials alone are never enough. -login-qr obtains that session through a
// QR login, which needs neither a phone number nor an SMS code, and writes it to
// the SESSION path for every later fetch to reuse.
package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/telegram"
	"github.com/iamxvbaba/td/telegram/auth/qrlogin"
	"github.com/iamxvbaba/td/telegram/dcs"
	"github.com/iamxvbaba/td/telegram/downloader"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"golang.org/x/net/proxy"
	"golang.org/x/sync/errgroup"
	"golang.org/x/term"
	"rsc.io/qr"

	"telesrv/internal/app/stargifts"
)

const (
	apiID              = 17349
	apiHash            = "344583e45741c457fe1862106095a5eb"
	defaultMaxDocBytes = int64(16 << 20)
	defaultWorkers     = 8
	maxCatalogGifts    = 5000
	maxUpgradeAttrs    = 10000
	maxDocuments       = 50000
	maxWorkers         = 128
	floodWaitMargin    = 5 * time.Second
	// defaultFetchTimeout covers the entire fetch: a full document download can take a long time.
	defaultFetchTimeout = 4 * time.Hour
	// defaultLoginTimeout only needs to cover a single QR scan: the QR code expires in ~30 seconds and Auth reissues it automatically.
	defaultLoginTimeout = 15 * time.Minute
)

type catalogManifest struct {
	Schema                   int                           `json:"schema"`
	Hash                     int                           `json:"hash"`
	RawCatalog               fileArtifact                  `json:"raw_catalog"`
	GiftCount                int                           `json:"gift_count"`
	ChatCount                int                           `json:"chat_count"`
	UserCount                int                           `json:"user_count"`
	UpgradeableGiftCount     int                           `json:"upgradeable_gift_count"`
	UpgradeAttributeSetCount int                           `json:"upgrade_attribute_set_count"`
	UpgradeAttributeCount    int                           `json:"upgrade_attribute_count"`
	UpgradeModelCount        int                           `json:"upgrade_model_count"`
	UpgradePatternCount      int                           `json:"upgrade_pattern_count"`
	UpgradeBackdropCount     int                           `json:"upgrade_backdrop_count"`
	MissingThumbCount        int                           `json:"missing_thumb_count"`
	Gifts                    []giftManifest                `json:"gifts"`
	UpgradeAttributeSets     []upgradeAttributeSetManifest `json:"upgrade_attribute_sets"`
	Documents                []documentManifest            `json:"documents"`
	TotalBytes               int64                         `json:"total_document_bytes"`
	BoundaryNote             string                        `json:"boundary_note"`
}

type socks5ProxyConfig struct {
	Address string
	Auth    *proxy.Auth
}

type giftManifest struct {
	Index               int                 `json:"index"`
	Kind                string              `json:"kind"`
	ID                  int64               `json:"id"`
	GiftID              int64               `json:"gift_id,omitempty"`
	Title               string              `json:"title,omitempty"`
	Slug                string              `json:"slug,omitempty"`
	Number              int                 `json:"number,omitempty"`
	Stars               int64               `json:"stars,omitempty"`
	ConvertStars        int64               `json:"convert_stars,omitempty"`
	UpgradeStars        int64               `json:"upgrade_stars,omitempty"`
	ResellMinStars      int64               `json:"resell_min_stars,omitempty"`
	Limited             bool                `json:"limited,omitempty"`
	SoldOut             bool                `json:"sold_out,omitempty"`
	Birthday            bool                `json:"birthday,omitempty"`
	RequirePremium      bool                `json:"require_premium,omitempty"`
	LimitedPerUser      bool                `json:"limited_per_user,omitempty"`
	PeerColorAvailable  bool                `json:"peer_color_available,omitempty"`
	Auction             bool                `json:"auction,omitempty"`
	AvailabilityRemains int                 `json:"availability_remains,omitempty"`
	AvailabilityTotal   int                 `json:"availability_total,omitempty"`
	AvailabilityResale  int64               `json:"availability_resale,omitempty"`
	AvailabilityIssued  int                 `json:"availability_issued,omitempty"`
	PerUserTotal        int                 `json:"per_user_total,omitempty"`
	PerUserRemains      int                 `json:"per_user_remains,omitempty"`
	FirstSaleDate       int                 `json:"first_sale_date,omitempty"`
	LastSaleDate        int                 `json:"last_sale_date,omitempty"`
	LockedUntilDate     int                 `json:"locked_until_date,omitempty"`
	AuctionSlug         string              `json:"auction_slug,omitempty"`
	GiftsPerRound       int                 `json:"gifts_per_round,omitempty"`
	AuctionStartDate    int                 `json:"auction_start_date,omitempty"`
	UpgradeVariants     int                 `json:"upgrade_variants,omitempty"`
	DocumentIDs         []int64             `json:"document_ids,omitempty"`
	Background          *backgroundManifest `json:"background,omitempty"`
}

type backgroundManifest struct {
	CenterColor int `json:"center_color"`
	EdgeColor   int `json:"edge_color"`
	TextColor   int `json:"text_color"`
}

type upgradeAttributeSetManifest struct {
	GiftID         int64                     `json:"gift_id"`
	RawAttributes  fileArtifact              `json:"raw_attributes"`
	AttributeCount int                       `json:"attribute_count"`
	Models         []upgradeModelManifest    `json:"models"`
	Patterns       []upgradePatternManifest  `json:"patterns"`
	Backdrops      []upgradeBackdropManifest `json:"backdrops"`
	DocumentIDs    []int64                   `json:"document_ids"`
}

type upgradeModelManifest struct {
	Name       string         `json:"name"`
	DocumentID int64          `json:"document_id"`
	Crafted    bool           `json:"crafted"`
	Rarity     rarityManifest `json:"rarity"`
}

type upgradePatternManifest struct {
	Name       string         `json:"name"`
	DocumentID int64          `json:"document_id"`
	Rarity     rarityManifest `json:"rarity"`
}

type upgradeBackdropManifest struct {
	Name         string         `json:"name"`
	BackdropID   int            `json:"backdrop_id"`
	CenterColor  int            `json:"center_color"`
	EdgeColor    int            `json:"edge_color"`
	PatternColor int            `json:"pattern_color"`
	TextColor    int            `json:"text_color"`
	Rarity       rarityManifest `json:"rarity"`
}

type rarityManifest struct {
	Kind          string `json:"kind"`
	ConstructorID string `json:"constructor_id"`
	Permille      *int   `json:"permille,omitempty"`
}

type documentManifest struct {
	ID                 int64          `json:"id"`
	Date               int            `json:"date"`
	DCID               int            `json:"dc_id"`
	MimeType           string         `json:"mime_type"`
	ExpectedSize       int64          `json:"expected_size"`
	FileName           string         `json:"file_name,omitempty"`
	StickerAlt         string         `json:"sticker_alt,omitempty"`
	Purposes           []string       `json:"purposes"`
	File               fileArtifact   `json:"file"`
	AnimationValidated bool           `json:"animation_validated,omitempty"`
	ValidationError    string         `json:"validation_error,omitempty"`
	Thumbs             []fileArtifact `json:"thumbs,omitempty"`
	MissingThumbs      []missingThumb `json:"missing_thumbs,omitempty"`
}

type missingThumb struct {
	Kind         string `json:"kind"`
	Type         string `json:"type"`
	ExpectedSize int64  `json:"expected_size"`
	Error        string `json:"error"`
}

type fileArtifact struct {
	Kind   string `json:"kind,omitempty"`
	Type   string `json:"type,omitempty"`
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type documentSource struct {
	document *tg.Document
	purposes map[string]struct{}
}

type addDocumentFunc func(tg.DocumentClass, string) (*tg.Document, error)

func main() {
	outDir := flag.String("out", "", "output directory")
	maxDocBytes := flag.Int64("max-document-bytes", defaultMaxDocBytes, "maximum bytes accepted for one document or thumbnail")
	skipThumbs := flag.Bool("skip-thumbs", false, "download main documents only")
	workers := flag.Int("workers", defaultWorkers, "concurrent document downloads")
	reuseMetadata := flag.Bool("reuse-metadata", false, "reuse and strictly decode catalog.tl plus upgrade-attributes/*.tl instead of refetching metadata")
	allowedMissingThumbsRaw := flag.String("allow-missing-thumb", "", "comma-separated document_id:photo|video:type entries that may be recorded as explicitly missing after a failed download")
	loginQR := flag.Bool("login-qr", false, "authorize the SESSION file through a QR login and exit without fetching (Telegram -> Settings -> Devices -> Link Desktop Device)")
	qrPNG := flag.String("qr-png", "", "also write each rotated QR login token as a PNG to this path (default: <session>.qr.png)")
	flag.Parse()

	session := strings.TrimSpace(os.Getenv("SESSION"))
	if session == "" {
		fmt.Fprintln(os.Stderr, "ERROR: SESSION is required")
		os.Exit(2)
	}

	// The SOCKS5 egress is shared by QR login and catalog fetching, so it is resolved only once.
	opts, err := clientOptions(session)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ERROR:", err)
		os.Exit(2)
	}

	if *loginQR {
		// Login only needs the update channel to receive the loginToken: it does not fetch the catalog and does not need -out.
		dispatcher := tg.NewUpdateDispatcher()
		opts.UpdateHandler = dispatcher
		run(defaultLoginTimeout, opts, func(ctx context.Context, client *telegram.Client) error {
			return loginQRToken(ctx, client, dispatcher, qrPNGPath(session, *qrPNG))
		})
		return
	}

	if strings.TrimSpace(*outDir) == "" {
		fmt.Fprintln(os.Stderr, "usage: SESSION=/path/to/session giftfetch -out <directory>")
		fmt.Fprintln(os.Stderr, "       SESSION=/path/to/session giftfetch -login-qr")
		os.Exit(2)
	}
	if *maxDocBytes <= 0 || *maxDocBytes > 256<<20 {
		fmt.Fprintln(os.Stderr, "ERROR: max-document-bytes must be in (0, 256 MiB]")
		os.Exit(2)
	}
	if *workers <= 0 || *workers > maxWorkers {
		fmt.Fprintf(os.Stderr, "ERROR: workers must be in [1, %d]\n", maxWorkers)
		os.Exit(2)
	}
	allowedMissingThumbs, err := parseAllowedMissingThumbs(*allowedMissingThumbsRaw)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ERROR:", err)
		os.Exit(2)
	}

	run(defaultFetchTimeout, opts, func(ctx context.Context, client *telegram.Client) error {
		status, err := client.Auth().Status(ctx)
		if err != nil {
			return fmt.Errorf("auth status: %w", err)
		}
		if !status.Authorized {
			return errors.New("SESSION is not authorized (run giftfetch -login-qr first)")
		}
		fmt.Println("[session authorized]")
		return fetchCatalog(ctx, client.API(), *outDir, *maxDocBytes, *skipThumbs, *workers, *reuseMetadata, allowedMissingThumbs)
	})
}

// run connects to official Telegram and executes fn once the connection is ready. Any failure aborts with exit code 1.
func run(timeout time.Duration, opts telegram.Options, fn func(context.Context, *telegram.Client) error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	id, hash := apiCredentials(apiID, apiHash)
	client := telegram.NewClient(id, hash, opts)
	if err := client.Run(ctx, func(ctx context.Context) error {
		return fn(ctx, client)
	}); err != nil {
		fmt.Fprintln(os.Stderr, "ERROR:", err)
		os.Exit(1)
	}
}

// loginQRToken trades a QR login for an authorized user session and persists it to SESSION.
// No phone number or SMS code is needed: just scan the QR code printed by show from a logged-in Telegram.
// An account with two-step verification also needs its cloud password; see resolvePassword.
func loginQRToken(ctx context.Context, client *telegram.Client, dispatcher tg.UpdateDispatcher, pngPath string) error {
	status, err := client.Auth().Status(ctx)
	if err != nil {
		return fmt.Errorf("auth status: %w", err)
	}
	if status.Authorized {
		fmt.Println("[session already authorized]")
		return nil
	}

	// OnLoginToken relies on the dispatcher's handlers map; a map is a reference type, and opts.UpdateHandler
	// holds the same one, so passing a value copy here still receives loginToken updates.
	authorization, err := client.QR().Auth(ctx, qrlogin.OnLoginToken(dispatcher), func(_ context.Context, token qrlogin.Token) error {
		fmt.Println("[qr] scan in Telegram -> Settings -> Devices -> Link Desktop Device")
		return showLoginQR(token.URL(), pngPath)
	})
	if tgerr.Is(err, "SESSION_PASSWORD_NEEDED") {
		// The scan was accepted, but the account has two-step verification:
		// auth.checkPassword finishes the login on the same connection.
		fmt.Println("[2fa] two-step verification password is required")
		pass, passErr := resolvePassword()
		if passErr != nil {
			return fmt.Errorf("resolve 2fa password: %w", passErr)
		}
		authorization, err = client.Auth().Password(ctx, pass)
		if err != nil {
			return fmt.Errorf("2fa password check: %w", err)
		}
	} else if err != nil {
		return fmt.Errorf("qr login: %w", err)
	}
	user, ok := authorization.User.AsNotEmpty()
	if !ok {
		return fmt.Errorf("unexpected user constructor %T", authorization.User)
	}
	fmt.Printf("[authorized] id=%d username=%s bot=%v\n", user.ID, user.Username, user.Bot)
	return nil
}

// resolvePassword returns the 2FA cloud password: TG_PASSWORD when set, otherwise a
// prompt that does not echo on a terminal, or the first line of piped stdin. The password
// is never taken from a flag, because arguments are visible to other users through ps.
// It is used exactly as given (a cloud password may start or end with spaces); only the
// line terminator of a piped line is removed.
func resolvePassword() (string, error) {
	if pass := os.Getenv("TG_PASSWORD"); pass != "" {
		return pass, nil
	}
	fd := int(os.Stdin.Fd())
	if term.IsTerminal(fd) {
		fmt.Fprint(os.Stderr, "Enter 2FA cloud password: ")
		pass, err := term.ReadPassword(fd)
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return "", fmt.Errorf("read password: %w", err)
		}
		if len(pass) == 0 {
			return "", errors.New("empty 2FA password")
		}
		return string(pass), nil
	}
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("read password from stdin: %w", err)
	}
	pass := strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
	if pass == "" {
		return "", errors.New("2FA password required: set TG_PASSWORD, or run in a terminal or pipe it on stdin")
	}
	return pass, nil
}

// qrPNGPath decides where the QR PNG is written: by default the SESSION name plus a suffix.
func qrPNGPath(session, override string) string {
	if override = strings.TrimSpace(override); override != "" {
		return override
	}
	return session + ".qr.png"
}

// showLoginQR renders the login link as a scannable QR code.
//
// Official clients treat tg://login?token= only as QR content; no client supports opening it as a link,
// so it must be drawn: print it directly in the terminal with half-block characters and also write a PNG for zoomed-in scanning.
func showLoginQR(url, pngPath string) error {
	// H-level error correction (65% redundancy) has more modules but tolerates more damage, which is steadier for terminal scaling and phone photos.
	code, err := qr.Encode(url, qr.H)
	if err != nil {
		return fmt.Errorf("encode login qr: %w", err)
	}
	if pngPath != "" {
		if err := os.WriteFile(pngPath, code.PNG(), 0o600); err != nil {
			fmt.Fprintf(os.Stderr, "[qr] write %s: %v\n", pngPath, err)
		} else {
			fmt.Printf("[qr] png %s\n", pngPath)
		}
	}
	fmt.Print(renderTerminalQR(code))
	return nil
}

// renderTerminalQR draws the QR code into the terminal with half-block characters, each character carrying two rows of modules.
func renderTerminalQR(code *qr.Code) string {
	const (
		quiet = 2 // quiet zone: at least two modules of white border, otherwise phone scan recognition drops sharply
		upper = "▀"
		lower = "▄"
		full  = "█"
		blank = " "
	)
	size := code.Size
	dim := size + 2*quiet
	var out strings.Builder
	for y := 0; y < dim; y += 2 {
		for x := 0; x < dim; x++ {
			top := y >= quiet && y-quiet < size && x >= quiet && x-quiet < size && code.Black(x-quiet, y-quiet)
			bottom := y+1 >= quiet && y+1-quiet < size && x >= quiet && x-quiet < size && code.Black(x-quiet, y+1-quiet)
			switch {
			case top && bottom:
				out.WriteString(full)
			case top:
				out.WriteString(upper)
			case bottom:
				out.WriteString(lower)
			default:
				out.WriteString(blank)
			}
		}
		out.WriteByte('\n')
	}
	return out.String()
}

// clientOptions assembles the common official-client options: session file storage and an optional SOCKS5 egress.
func clientOptions(session string) (telegram.Options, error) {
	opts := telegram.Options{
		SessionStorage: &telegram.FileSessionStorage{Path: session},
	}
	socksProxy := strings.TrimSpace(os.Getenv("SOCKS5_PROXY"))
	if socksProxy == "" {
		return opts, nil
	}
	proxyConfig, err := parseSOCKS5Proxy(socksProxy)
	if err != nil {
		return telegram.Options{}, fmt.Errorf("invalid SOCKS5_PROXY: %w", err)
	}
	dialer, err := proxy.SOCKS5("tcp", proxyConfig.Address, proxyConfig.Auth, proxy.Direct)
	if err != nil {
		return telegram.Options{}, fmt.Errorf("invalid SOCKS5_PROXY: %w", err)
	}
	opts.Resolver = dcs.Plain(dcs.PlainOptions{
		Dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return dialer.Dial(network, addr)
		},
	})
	return opts, nil
}

func parseSOCKS5Proxy(raw string) (socks5ProxyConfig, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return socks5ProxyConfig{}, errors.New("must not be empty")
	}
	if strings.Contains(raw, "://") {
		parsed, err := url.Parse(raw)
		if err != nil {
			return socks5ProxyConfig{}, err
		}
		if parsed.Scheme != "socks5" {
			return socks5ProxyConfig{}, fmt.Errorf("scheme must be socks5, got %q", parsed.Scheme)
		}
		if parsed.Host == "" {
			return socks5ProxyConfig{}, errors.New("missing proxy host")
		}
		if (parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" {
			return socks5ProxyConfig{}, errors.New("must not include path, query, or fragment")
		}
		address, err := normalizeProxyAddress(parsed.Host)
		if err != nil {
			return socks5ProxyConfig{}, err
		}
		var auth *proxy.Auth
		if parsed.User != nil {
			password, _ := parsed.User.Password()
			auth = &proxy.Auth{User: parsed.User.Username(), Password: password}
		}
		return socks5ProxyConfig{Address: address, Auth: auth}, nil
	}
	address, err := normalizeProxyAddress(raw)
	if err != nil {
		return socks5ProxyConfig{}, err
	}
	return socks5ProxyConfig{Address: address}, nil
}

func normalizeProxyAddress(raw string) (string, error) {
	host, port, err := net.SplitHostPort(strings.TrimSpace(raw))
	if err != nil {
		return "", fmt.Errorf("must be host:port or socks5://host:port: %w", err)
	}
	if strings.TrimSpace(host) == "" || strings.TrimSpace(port) == "" {
		return "", errors.New("host and port are required")
	}
	return net.JoinHostPort(host, port), nil
}

func fetchCatalog(ctx context.Context, api *tg.Client, outDir string, maxDocBytes int64, skipThumbs bool, workers int, reuseMetadata bool, allowedMissingThumbs map[string]struct{}) error {
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}

	var catalog *tg.PaymentsStarGifts
	var rawArtifact fileArtifact
	if reuseMetadata {
		catalog = &tg.PaymentsStarGifts{}
		var err error
		rawArtifact, err = readTLArtifact(outDir, "catalog.tl", catalog)
		if err != nil {
			return fmt.Errorf("reuse catalog metadata: %w", err)
		}
		fmt.Printf("[metadata reused] path=%s\n", rawArtifact.Path)
	} else {
		result, err := api.PaymentsGetStarGifts(ctx, 0)
		if err != nil {
			return fmt.Errorf("payments.getStarGifts: %w", err)
		}
		var ok bool
		catalog, ok = result.(*tg.PaymentsStarGifts)
		if !ok {
			return fmt.Errorf("payments.getStarGifts(hash=0) returned %T", result)
		}
		var raw bin.Buffer
		if err := catalog.Encode(&raw); err != nil {
			return fmt.Errorf("encode raw catalog: %w", err)
		}
		rawArtifact, err = writeArtifact(outDir, "catalog.tl", "tl", "", raw.Buf)
		if err != nil {
			return err
		}
	}
	if len(catalog.Gifts) > maxCatalogGifts {
		return fmt.Errorf("gift catalog has %d entries, limit is %d", len(catalog.Gifts), maxCatalogGifts)
	}
	manifest := catalogManifest{
		Schema:       2,
		Hash:         catalog.Hash,
		RawCatalog:   rawArtifact,
		GiftCount:    len(catalog.Gifts),
		ChatCount:    len(catalog.Chats),
		UserCount:    len(catalog.Users),
		BoundaryNote: "payments.getStarGifts(hash=0) plus payments.getStarGiftUpgradeAttributes for every currently upgradeable base gift: complete current official attribute definitions and referenced documents, not deleted historical definitions or precomputed model-pattern-backdrop combinations",
	}

	documents := make(map[int64]*documentSource)
	addDocument := func(class tg.DocumentClass, purpose string) (*tg.Document, error) {
		doc, ok := class.(*tg.Document)
		if !ok || doc.ID == 0 {
			return nil, fmt.Errorf("%s references invalid document %T", purpose, class)
		}
		if !hasRenderableStickerAttribute(doc) {
			return nil, fmt.Errorf("%s document %d has neither sticker nor custom-emoji attribute", purpose, doc.ID)
		}
		existing := documents[doc.ID]
		if existing == nil {
			existing = &documentSource{document: doc, purposes: make(map[string]struct{})}
			documents[doc.ID] = existing
		} else if existing.document.Size != doc.Size || existing.document.MimeType != doc.MimeType {
			return nil, fmt.Errorf("document %d has conflicting metadata", doc.ID)
		}
		existing.purposes[purpose] = struct{}{}
		return doc, nil
	}

	for index, class := range catalog.Gifts {
		gm, err := collectGift(index, class, addDocument)
		if err != nil {
			return err
		}
		manifest.Gifts = append(manifest.Gifts, gm)
	}

	upgradeableGiftIDs := collectUpgradeableGiftIDs(catalog.Gifts)
	manifest.UpgradeableGiftCount = len(upgradeableGiftIDs)
	for _, giftID := range upgradeableGiftIDs {
		attributeSet, err := fetchUpgradeAttributeSet(ctx, api, outDir, giftID, addDocument, reuseMetadata)
		if err != nil {
			return err
		}
		manifest.UpgradeAttributeSets = append(manifest.UpgradeAttributeSets, attributeSet)
		manifest.UpgradeAttributeSetCount++
		manifest.UpgradeAttributeCount += attributeSet.AttributeCount
		manifest.UpgradeModelCount += len(attributeSet.Models)
		manifest.UpgradePatternCount += len(attributeSet.Patterns)
		manifest.UpgradeBackdropCount += len(attributeSet.Backdrops)
		fmt.Printf("[upgrade attributes] gift_id=%d models=%d patterns=%d backdrops=%d raw=%s\n", giftID, len(attributeSet.Models), len(attributeSet.Patterns), len(attributeSet.Backdrops), attributeSet.RawAttributes.Path)
	}
	if manifest.UpgradeAttributeSetCount != manifest.UpgradeableGiftCount {
		return fmt.Errorf("upgrade attribute set count mismatch: got %d want %d", manifest.UpgradeAttributeSetCount, manifest.UpgradeableGiftCount)
	}
	if len(documents) > maxDocuments {
		return fmt.Errorf("catalog and upgrade attributes reference %d documents, limit is %d", len(documents), maxDocuments)
	}

	ids := make([]int64, 0, len(documents))
	for id := range documents {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	fmt.Printf("[documents discovered] count=%d workers=%d\n", len(ids), workers)
	documentResults := make([]documentManifest, len(ids))
	group, downloadCtx := errgroup.WithContext(ctx)
	group.SetLimit(workers)
	var completed atomic.Int64
	for index, id := range ids {
		index, id := index, id
		group.Go(func() error {
			dl := downloader.NewDownloader().WithRetryHandler(func(event downloader.RetryEvent) {
				fmt.Printf("[download retry] document=%d operation=%s attempt=%d error=%v\n", id, event.Operation, event.Attempt, event.Err)
				if strings.Contains(event.Err.Error(), "FLOOD_WAIT") {
					timer := time.NewTimer(floodWaitMargin)
					defer timer.Stop()
					select {
					case <-downloadCtx.Done():
					case <-timer.C:
					}
				}
			})
			dm, err := fetchDocument(downloadCtx, api, dl, outDir, documents[id], maxDocBytes, skipThumbs, allowedMissingThumbs)
			if err != nil {
				return err
			}
			documentResults[index] = dm
			done := completed.Add(1)
			if done == int64(len(ids)) || done%100 == 0 {
				fmt.Printf("[documents downloaded] completed=%d total=%d\n", done, len(ids))
			}
			return nil
		})
	}
	if err := group.Wait(); err != nil {
		return err
	}
	for _, dm := range documentResults {
		manifest.TotalBytes += dm.File.Size
		for _, thumb := range dm.Thumbs {
			manifest.TotalBytes += thumb.Size
		}
		manifest.MissingThumbCount += len(dm.MissingThumbs)
		manifest.Documents = append(manifest.Documents, dm)
	}

	encoded, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	if err := writeFileAtomic(filepath.Join(outDir, "manifest.json"), append(encoded, '\n')); err != nil {
		return err
	}
	fmt.Printf("[complete] gifts=%d attribute_sets=%d attributes=%d documents=%d missing_thumbs=%d bytes=%d manifest=%s\n", len(manifest.Gifts), len(manifest.UpgradeAttributeSets), manifest.UpgradeAttributeCount, len(manifest.Documents), manifest.MissingThumbCount, manifest.TotalBytes, filepath.Join(outDir, "manifest.json"))
	return nil
}

func collectUpgradeableGiftIDs(classes []tg.StarGiftClass) []int64 {
	ids := make([]int64, 0, len(classes))
	for _, class := range classes {
		gift, ok := class.(*tg.StarGift)
		if !ok || gift.ID == 0 || gift.UpgradeStars <= 0 && gift.UpgradeVariants <= 0 {
			continue
		}
		ids = append(ids, gift.ID)
	}
	return ids
}

func fetchUpgradeAttributeSet(ctx context.Context, api *tg.Client, root string, giftID int64, addDocument addDocumentFunc, reuseMetadata bool) (upgradeAttributeSetManifest, error) {
	var result *tg.PaymentsStarGiftUpgradeAttributes
	var rawArtifact fileArtifact
	if reuseMetadata {
		result = &tg.PaymentsStarGiftUpgradeAttributes{}
		var err error
		rawArtifact, err = readTLArtifact(root, filepath.Join("upgrade-attributes", fmt.Sprintf("%d.tl", giftID)), result)
		if err != nil {
			return upgradeAttributeSetManifest{}, fmt.Errorf("reuse upgrade attributes for gift %d: %w", giftID, err)
		}
	} else {
		var err error
		result, err = api.PaymentsGetStarGiftUpgradeAttributes(ctx, giftID)
		if err != nil {
			return upgradeAttributeSetManifest{}, fmt.Errorf("payments.getStarGiftUpgradeAttributes(gift_id=%d): %w", giftID, err)
		}
		var raw bin.Buffer
		if err := result.Encode(&raw); err != nil {
			return upgradeAttributeSetManifest{}, fmt.Errorf("encode upgrade attributes for gift %d: %w", giftID, err)
		}
		rawArtifact, err = writeArtifact(root, filepath.Join("upgrade-attributes", fmt.Sprintf("%d.tl", giftID)), "tl", "", raw.Buf)
		if err != nil {
			return upgradeAttributeSetManifest{}, err
		}
	}
	if len(result.Attributes) == 0 {
		return upgradeAttributeSetManifest{}, fmt.Errorf("payments.getStarGiftUpgradeAttributes(gift_id=%d) returned no attributes", giftID)
	}
	if len(result.Attributes) > maxUpgradeAttrs {
		return upgradeAttributeSetManifest{}, fmt.Errorf("gift %d has %d upgrade attributes, limit is %d", giftID, len(result.Attributes), maxUpgradeAttrs)
	}
	return collectUpgradeAttributes(giftID, result, rawArtifact, addDocument)
}

func collectUpgradeAttributes(giftID int64, result *tg.PaymentsStarGiftUpgradeAttributes, rawArtifact fileArtifact, addDocument addDocumentFunc) (upgradeAttributeSetManifest, error) {
	if result == nil {
		return upgradeAttributeSetManifest{}, fmt.Errorf("gift %d has nil upgrade-attribute result", giftID)
	}
	set := upgradeAttributeSetManifest{
		GiftID:         giftID,
		RawAttributes:  rawArtifact,
		AttributeCount: len(result.Attributes),
	}
	documentIDs := make(map[int64]struct{})
	for index, attribute := range result.Attributes {
		switch value := attribute.(type) {
		case *tg.StarGiftAttributeModel:
			rarity, err := collectRarity(value.Rarity)
			if err != nil {
				return upgradeAttributeSetManifest{}, fmt.Errorf("gift %d model %q rarity: %w", giftID, value.Name, err)
			}
			doc, err := addDocument(value.Document, fmt.Sprintf("gift:%d:upgrade-model:%s", giftID, value.Name))
			if err != nil {
				return upgradeAttributeSetManifest{}, err
			}
			set.Models = append(set.Models, upgradeModelManifest{Name: value.Name, DocumentID: doc.ID, Crafted: value.GetCrafted(), Rarity: rarity})
			documentIDs[doc.ID] = struct{}{}
		case *tg.StarGiftAttributePattern:
			rarity, err := collectRarity(value.Rarity)
			if err != nil {
				return upgradeAttributeSetManifest{}, fmt.Errorf("gift %d pattern %q rarity: %w", giftID, value.Name, err)
			}
			doc, err := addDocument(value.Document, fmt.Sprintf("gift:%d:upgrade-pattern:%s", giftID, value.Name))
			if err != nil {
				return upgradeAttributeSetManifest{}, err
			}
			set.Patterns = append(set.Patterns, upgradePatternManifest{Name: value.Name, DocumentID: doc.ID, Rarity: rarity})
			documentIDs[doc.ID] = struct{}{}
		case *tg.StarGiftAttributeBackdrop:
			rarity, err := collectRarity(value.Rarity)
			if err != nil {
				return upgradeAttributeSetManifest{}, fmt.Errorf("gift %d backdrop %q rarity: %w", giftID, value.Name, err)
			}
			set.Backdrops = append(set.Backdrops, upgradeBackdropManifest{
				Name: value.Name, BackdropID: value.BackdropID, CenterColor: value.CenterColor,
				EdgeColor: value.EdgeColor, PatternColor: value.PatternColor, TextColor: value.TextColor,
				Rarity: rarity,
			})
		default:
			return upgradeAttributeSetManifest{}, fmt.Errorf("gift %d upgrade attribute at index %d has unsupported constructor %T", giftID, index, attribute)
		}
	}
	if len(set.Models) == 0 || len(set.Patterns) == 0 || len(set.Backdrops) == 0 {
		return upgradeAttributeSetManifest{}, fmt.Errorf("gift %d incomplete upgrade attributes: models=%d patterns=%d backdrops=%d", giftID, len(set.Models), len(set.Patterns), len(set.Backdrops))
	}
	if len(set.Models)+len(set.Patterns)+len(set.Backdrops) != set.AttributeCount {
		return upgradeAttributeSetManifest{}, fmt.Errorf("gift %d parsed %d of %d upgrade attributes", giftID, len(set.Models)+len(set.Patterns)+len(set.Backdrops), set.AttributeCount)
	}
	for id := range documentIDs {
		set.DocumentIDs = append(set.DocumentIDs, id)
	}
	sort.Slice(set.DocumentIDs, func(i, j int) bool { return set.DocumentIDs[i] < set.DocumentIDs[j] })
	return set, nil
}

func collectRarity(class tg.StarGiftAttributeRarityClass) (rarityManifest, error) {
	switch value := class.(type) {
	case *tg.StarGiftAttributeRarity:
		permille := value.Permille
		return rarityManifest{Kind: "permille", ConstructorID: fmt.Sprintf("0x%08x", value.TypeID()), Permille: &permille}, nil
	case *tg.StarGiftAttributeRarityUncommon:
		return rarityManifest{Kind: "uncommon", ConstructorID: fmt.Sprintf("0x%08x", value.TypeID())}, nil
	case *tg.StarGiftAttributeRarityRare:
		return rarityManifest{Kind: "rare", ConstructorID: fmt.Sprintf("0x%08x", value.TypeID())}, nil
	case *tg.StarGiftAttributeRarityEpic:
		return rarityManifest{Kind: "epic", ConstructorID: fmt.Sprintf("0x%08x", value.TypeID())}, nil
	case *tg.StarGiftAttributeRarityLegendary:
		return rarityManifest{Kind: "legendary", ConstructorID: fmt.Sprintf("0x%08x", value.TypeID())}, nil
	default:
		return rarityManifest{}, fmt.Errorf("unsupported constructor %T", class)
	}
}

// starGift.title is optional; Telegram does not send a title for "regular gifts" (clients just draw the sticker),
// and early limited gifts that sold out may omit it too. When the title is missing, the admin import shows "Unnamed official gift",
// so here we fill in the pattern names used on the Telegram store for the fixed regular gifts; the IDs are stable and do not change between fetches.
var regularGiftTitles = map[int64]string{
	5170145012310081615: "Heart",
	5170250947678437525: "Gift Box",
	5168103777563050263: "Rose",
	5170144170496491616: "Cake",
	5170314324215857265: "Bouquet",
	5170564780938756245: "Rocket",
	6028601630662853006: "Champagne",
	5168043875654172773: "Trophy",
	5170690322832818290: "Ring",
	5170521118301225164: "Diamond",
	5992502615223568003: "Bear",
}

// giftTitle prefers the title sent by Telegram and falls back to the pattern name only for regular gifts without a title.
func giftTitle(gift *tg.StarGift) string {
	if title := strings.TrimSpace(gift.Title); title != "" {
		return title
	}
	return regularGiftTitles[gift.ID]
}

func collectGift(index int, class tg.StarGiftClass, addDocument addDocumentFunc) (giftManifest, error) {
	switch gift := class.(type) {
	case *tg.StarGift:
		purpose := fmt.Sprintf("gift:%d:sticker", gift.ID)
		doc, err := addDocument(gift.Sticker, purpose)
		if err != nil {
			return giftManifest{}, err
		}
		gm := giftManifest{
			Index:               index,
			Kind:                "regular",
			ID:                  gift.ID,
			Stars:               gift.Stars,
			ConvertStars:        gift.ConvertStars,
			UpgradeStars:        gift.UpgradeStars,
			ResellMinStars:      gift.ResellMinStars,
			Limited:             gift.Limited,
			SoldOut:             gift.SoldOut,
			Birthday:            gift.Birthday,
			RequirePremium:      gift.RequirePremium,
			LimitedPerUser:      gift.LimitedPerUser,
			PeerColorAvailable:  gift.PeerColorAvailable,
			Auction:             gift.Auction,
			AvailabilityRemains: gift.AvailabilityRemains,
			AvailabilityTotal:   gift.AvailabilityTotal,
			AvailabilityResale:  gift.AvailabilityResale,
			PerUserTotal:        gift.PerUserTotal,
			PerUserRemains:      gift.PerUserRemains,
			FirstSaleDate:       gift.FirstSaleDate,
			LastSaleDate:        gift.LastSaleDate,
			LockedUntilDate:     gift.LockedUntilDate,
			AuctionSlug:         gift.AuctionSlug,
			GiftsPerRound:       gift.GiftsPerRound,
			AuctionStartDate:    gift.AuctionStartDate,
			UpgradeVariants:     gift.UpgradeVariants,
			Title:               giftTitle(gift),
			DocumentIDs:         []int64{doc.ID},
		}
		if background, ok := gift.GetBackground(); ok {
			gm.Background = &backgroundManifest{CenterColor: background.CenterColor, EdgeColor: background.EdgeColor, TextColor: background.TextColor}
		}
		return gm, nil
	case *tg.StarGiftUnique:
		gm := giftManifest{
			Index:              index,
			Kind:               "unique",
			ID:                 gift.ID,
			GiftID:             gift.GiftID,
			Title:              gift.Title,
			Slug:               gift.Slug,
			Number:             gift.Num,
			RequirePremium:     gift.RequirePremium,
			AvailabilityIssued: gift.AvailabilityIssued,
			AvailabilityTotal:  gift.AvailabilityTotal,
		}
		for _, attribute := range gift.Attributes {
			var class tg.DocumentClass
			var purpose string
			switch value := attribute.(type) {
			case *tg.StarGiftAttributeModel:
				class = value.Document
				purpose = fmt.Sprintf("unique:%d:model:%s", gift.ID, value.Name)
			case *tg.StarGiftAttributePattern:
				class = value.Document
				purpose = fmt.Sprintf("unique:%d:pattern:%s", gift.ID, value.Name)
			default:
				continue
			}
			doc, err := addDocument(class, purpose)
			if err != nil {
				return giftManifest{}, err
			}
			gm.DocumentIDs = append(gm.DocumentIDs, doc.ID)
		}
		return gm, nil
	default:
		return giftManifest{}, fmt.Errorf("unsupported gift constructor %T at index %d", class, index)
	}
}

func fetchDocument(ctx context.Context, api *tg.Client, dl *downloader.Downloader, root string, source *documentSource, maxBytes int64, skipThumbs bool, allowedMissingThumbs map[string]struct{}) (documentManifest, error) {
	doc := source.document
	if doc.Size <= 0 || doc.Size > maxBytes {
		return documentManifest{}, fmt.Errorf("document %d size %d is outside (0, %d]", doc.ID, doc.Size, maxBytes)
	}
	fileName, stickerAlt := documentNames(doc)
	ext := documentExtension(fileName, doc.MimeType)
	rel := filepath.Join("documents", fmt.Sprintf("%d%s", doc.ID, ext))
	data, reused, err := existingArtifact(root, rel, doc.Size, maxBytes)
	if err != nil {
		return documentManifest{}, err
	}
	if !reused {
		fmt.Printf("[network fetch] document=%d resource=document expected_size=%d part_size=%d\n", doc.ID, doc.Size, downloadPartSize(doc.Size))
		data, err = retryOnTimeout(ctx, fmt.Sprintf("document %d", doc.ID), func() ([]byte, error) {
			return download(ctx, api, dl, doc, "", doc.Size, maxBytes)
		})
		if err != nil {
			return documentManifest{}, fmt.Errorf("download document %d: %w", doc.ID, err)
		}
	}
	if int64(len(data)) != doc.Size {
		return documentManifest{}, fmt.Errorf("document %d size mismatch: got %d want %d", doc.ID, len(data), doc.Size)
	}
	artifact, err := writeArtifact(root, rel, "document", "", data)
	if err != nil {
		return documentManifest{}, err
	}
	purposes := make([]string, 0, len(source.purposes))
	for purpose := range source.purposes {
		purposes = append(purposes, purpose)
	}
	sort.Strings(purposes)
	dm := documentManifest{
		ID:           doc.ID,
		Date:         doc.Date,
		DCID:         doc.DCID,
		MimeType:     doc.MimeType,
		ExpectedSize: doc.Size,
		FileName:     fileName,
		StickerAlt:   stickerAlt,
		Purposes:     purposes,
		File:         artifact,
	}
	if ext == ".tgs" || strings.EqualFold(doc.MimeType, "application/x-tgsticker") {
		validator := &stargifts.Service{}
		if _, err := validator.PrepareAnimation(fmt.Sprintf("%d.tgs", doc.ID), data); err != nil {
			dm.ValidationError = err.Error()
		} else {
			dm.AnimationValidated = true
		}
	}
	if skipThumbs {
		return dm, nil
	}
	thumbs, missingThumbs, err := fetchThumbs(ctx, api, dl, root, doc, maxBytes, allowedMissingThumbs)
	if err != nil {
		return documentManifest{}, err
	}
	dm.Thumbs = thumbs
	dm.MissingThumbs = missingThumbs
	return dm, nil
}

func fetchThumbs(ctx context.Context, api *tg.Client, dl *downloader.Downloader, root string, doc *tg.Document, maxBytes int64, allowedMissingThumbs map[string]struct{}) ([]fileArtifact, []missingThumb, error) {
	seen := make(map[string]struct{})
	artifacts := make([]fileArtifact, 0, len(doc.Thumbs)+len(doc.VideoThumbs))
	missing := make([]missingThumb, 0)
	for _, class := range doc.Thumbs {
		thumbType := class.GetType()
		if thumbType == "" {
			continue
		}
		key := "photo:" + thumbType
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		var data []byte
		var err error
		var expectedSize int64
		downloadAttempted := false
		switch value := class.(type) {
		case *tg.PhotoCachedSize:
			data = append([]byte(nil), value.Bytes...)
		case *tg.PhotoStrippedSize:
			data = append([]byte(nil), value.Bytes...)
		case *tg.PhotoPathSize:
			data = append([]byte(nil), value.Bytes...)
		case *tg.PhotoSize:
			expectedSize = int64(value.Size)
			if expectedSize <= 0 || expectedSize > maxBytes {
				return nil, nil, fmt.Errorf("document %d photo thumb %q size %d is outside (0, %d]", doc.ID, thumbType, expectedSize, maxBytes)
			}
			rel := filepath.Join("thumbs", fmt.Sprintf("%d-photo-%s.bin", doc.ID, safePart(thumbType)))
			var reused bool
			data, reused, err = existingArtifact(root, rel, expectedSize, maxBytes)
			if err == nil && !reused {
				downloadAttempted = true
				fmt.Printf("[network fetch] document=%d resource=photo-thumb type=%s expected_size=%d part_size=%d\n", doc.ID, thumbType, expectedSize, downloadPartSize(expectedSize))
				data, err = retryOnTimeout(ctx, fmt.Sprintf("photo-thumb %d (%s)", doc.ID, thumbType), func() ([]byte, error) {
					return download(ctx, api, dl, doc, thumbType, expectedSize, maxBytes)
				})
			}
		case *tg.PhotoSizeProgressive:
			if len(value.Sizes) == 0 {
				return nil, nil, fmt.Errorf("document %d progressive photo thumb %q has no sizes", doc.ID, thumbType)
			}
			expectedSize = int64(value.Sizes[len(value.Sizes)-1])
			if expectedSize <= 0 || expectedSize > maxBytes {
				return nil, nil, fmt.Errorf("document %d progressive photo thumb %q size %d is outside (0, %d]", doc.ID, thumbType, expectedSize, maxBytes)
			}
			rel := filepath.Join("thumbs", fmt.Sprintf("%d-photo-%s.bin", doc.ID, safePart(thumbType)))
			var reused bool
			data, reused, err = existingArtifact(root, rel, expectedSize, maxBytes)
			if err == nil && !reused {
				downloadAttempted = true
				fmt.Printf("[network fetch] document=%d resource=photo-thumb-progressive type=%s expected_size=%d part_size=%d\n", doc.ID, thumbType, expectedSize, downloadPartSize(expectedSize))
				data, err = retryOnTimeout(ctx, fmt.Sprintf("progressive-thumb %d (%s)", doc.ID, thumbType), func() ([]byte, error) {
					return download(ctx, api, dl, doc, thumbType, expectedSize, maxBytes)
				})
			}
		default:
			continue
		}
		if err != nil {
			if downloadAttempted && missingThumbAllowed(allowedMissingThumbs, doc.ID, "photo", thumbType) {
				missing = append(missing, missingThumb{Kind: "photo", Type: thumbType, ExpectedSize: expectedSize, Error: err.Error()})
				fmt.Printf("[missing thumb] document=%d kind=photo type=%s expected_size=%d error=%v\n", doc.ID, thumbType, expectedSize, err)
				continue
			}
			return nil, nil, fmt.Errorf("download document %d photo thumb %q: %w", doc.ID, thumbType, err)
		}
		if len(data) == 0 {
			continue
		}
		if expectedSize > 0 && int64(len(data)) != expectedSize {
			return nil, nil, fmt.Errorf("document %d photo thumb %q size mismatch: got %d want %d", doc.ID, thumbType, len(data), expectedSize)
		}
		rel := filepath.Join("thumbs", fmt.Sprintf("%d-photo-%s.bin", doc.ID, safePart(thumbType)))
		artifact, err := writeArtifact(root, rel, "photo", thumbType, data)
		if err != nil {
			return nil, nil, err
		}
		artifacts = append(artifacts, artifact)
	}
	for _, class := range doc.VideoThumbs {
		value, ok := class.(*tg.VideoSize)
		if !ok || value.Type == "" {
			continue
		}
		key := "video:" + value.Type
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		rel := filepath.Join("thumbs", fmt.Sprintf("%d-video-%s.bin", doc.ID, safePart(value.Type)))
		data, reused, err := existingArtifact(root, rel, int64(value.Size), maxBytes)
		downloadAttempted := false
		if err == nil && !reused {
			downloadAttempted = true
			fmt.Printf("[network fetch] document=%d resource=video-thumb type=%s expected_size=%d part_size=%d\n", doc.ID, value.Type, value.Size, downloadPartSize(int64(value.Size)))
			data, err = retryOnTimeout(ctx, fmt.Sprintf("video-thumb %d (%s)", doc.ID, value.Type), func() ([]byte, error) {
				return download(ctx, api, dl, doc, value.Type, int64(value.Size), maxBytes)
			})
		}
		if err != nil {
			if downloadAttempted && missingThumbAllowed(allowedMissingThumbs, doc.ID, "video", value.Type) {
				missing = append(missing, missingThumb{Kind: "video", Type: value.Type, ExpectedSize: int64(value.Size), Error: err.Error()})
				fmt.Printf("[missing thumb] document=%d kind=video type=%s expected_size=%d error=%v\n", doc.ID, value.Type, value.Size, err)
				continue
			}
			return nil, nil, fmt.Errorf("download document %d video thumb %q: %w", doc.ID, value.Type, err)
		}
		artifact, err := writeArtifact(root, rel, "video", value.Type, data)
		if err != nil {
			return nil, nil, err
		}
		artifacts = append(artifacts, artifact)
	}
	sort.Slice(artifacts, func(i, j int) bool {
		if artifacts[i].Kind != artifacts[j].Kind {
			return artifacts[i].Kind < artifacts[j].Kind
		}
		return artifacts[i].Type < artifacts[j].Type
	})
	sort.Slice(missing, func(i, j int) bool {
		if missing[i].Kind != missing[j].Kind {
			return missing[i].Kind < missing[j].Kind
		}
		return missing[i].Type < missing[j].Type
	})
	return artifacts, missing, nil
}

func isRetryable(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "-503") ||
		strings.Contains(msg, "Timeout") ||
		strings.Contains(msg, "FLOOD_WAIT") ||
		strings.Contains(msg, "context deadline exceeded") ||
		strings.Contains(msg, "connection reset by peer")
}

func retryOnTimeout(ctx context.Context, desc string, op func() ([]byte, error)) ([]byte, error) {
	const maxRetries = 6
	var lastErr error
	for attempt := 1; attempt <= maxRetries; attempt++ {
		data, err := op()
		if err == nil {
			return data, nil
		}
		lastErr = err
		if !isRetryable(err) {
			return nil, err
		}
		backoff := time.Duration(attempt*2) * time.Second
		fmt.Printf("[retry %d/%d] %s: %v (waiting %v)\n", attempt, maxRetries, desc, err, backoff)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(backoff):
		}
	}
	return nil, fmt.Errorf("failed after %d attempts: %w", maxRetries, lastErr)
}

func download(ctx context.Context, api *tg.Client, dl *downloader.Downloader, doc *tg.Document, thumbType string, expectedSize, maxBytes int64) ([]byte, error) {
	location := &tg.InputDocumentFileLocation{
		ID:            doc.ID,
		AccessHash:    doc.AccessHash,
		FileReference: doc.FileReference,
		ThumbSize:     thumbType,
	}
	partSize := downloadPartSize(expectedSize)
	if expectedSize > 0 && expectedSize < int64(partSize) {
		// TDesktop and DrKLO issue ordinary non-precise upload.getFile requests
		// for regular file chunks. A known-size resource that fits in one valid
		// chunk needs neither gotd's precise mode nor an EOF probe.
		result, err := api.UploadGetFile(ctx, &tg.UploadGetFileRequest{
			Location: location,
			Offset:   0,
			Limit:    partSize,
		})
		if err != nil {
			return nil, err
		}
		file, ok := result.(*tg.UploadFile)
		if !ok {
			return nil, fmt.Errorf("single-chunk upload.getFile returned %T", result)
		}
		if int64(len(file.Bytes)) > maxBytes {
			return nil, fmt.Errorf("download exceeds %d bytes", maxBytes)
		}
		return append([]byte(nil), file.Bytes...), nil
	}
	buffer := &boundedBuffer{max: maxBytes}
	if _, err := dl.WithPartSize(partSize).Download(api, location).Stream(ctx, buffer); err != nil {
		return nil, err
	}
	return append([]byte(nil), buffer.Bytes()...), nil
}

func downloadPartSize(expectedSize int64) int {
	const (
		unit = int64(4 << 10)
		max  = int64(512 << 10)
	)
	if expectedSize <= 0 || expectedSize >= max {
		return int(max)
	}
	// Non-precise upload.getFile limits must use the client-compatible chunk
	// ladder (4, 8, ..., 512 KiB), whose values also divide a 1 MiB window.
	// Merely rounding to an arbitrary 4 KiB multiple (for example 48 KiB)
	// is rejected with LIMIT_INVALID by some official file DCs.
	partSize := unit
	for partSize <= expectedSize && partSize < max {
		partSize *= 2
	}
	return int(partSize)
}

func existingArtifact(root, relative string, expectedSize, maxBytes int64) ([]byte, bool, error) {
	data, err := os.ReadFile(filepath.Join(root, relative))
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("read existing artifact %q: %w", relative, err)
	}
	size := int64(len(data))
	if size <= 0 || size > maxBytes || expectedSize >= 0 && size != expectedSize {
		return nil, false, nil
	}
	return data, true, nil
}

type boundedBuffer struct {
	bytes.Buffer
	max int64
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	remaining := b.max - int64(b.Len())
	if remaining <= 0 {
		return 0, fmt.Errorf("download exceeds %d bytes", b.max)
	}
	if int64(len(p)) > remaining {
		written, _ := b.Buffer.Write(p[:remaining])
		return written, fmt.Errorf("download exceeds %d bytes", b.max)
	}
	return b.Buffer.Write(p)
}

func hasRenderableStickerAttribute(doc *tg.Document) bool {
	for _, attribute := range doc.Attributes {
		switch attribute.(type) {
		case *tg.DocumentAttributeSticker, *tg.DocumentAttributeCustomEmoji:
			return true
		}
	}
	return false
}

func documentNames(doc *tg.Document) (fileName, alt string) {
	for _, attribute := range doc.Attributes {
		switch value := attribute.(type) {
		case *tg.DocumentAttributeFilename:
			fileName = filepath.Base(value.FileName)
		case *tg.DocumentAttributeSticker:
			alt = value.Alt
		case *tg.DocumentAttributeCustomEmoji:
			alt = value.Alt
		}
	}
	return fileName, alt
}

func documentExtension(fileName, mimeType string) string {
	ext := strings.ToLower(filepath.Ext(fileName))
	switch ext {
	case ".tgs", ".webm", ".mp4", ".webp", ".png", ".jpg", ".jpeg":
		return ext
	}
	switch strings.ToLower(mimeType) {
	case "application/x-tgsticker", "application/gzip":
		return ".tgs"
	case "video/webm":
		return ".webm"
	case "video/mp4":
		return ".mp4"
	case "image/webp":
		return ".webp"
	case "image/png":
		return ".png"
	case "image/jpeg":
		return ".jpg"
	default:
		return ".bin"
	}
}

func safePart(value string) string {
	var builder strings.Builder
	for _, r := range value {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			builder.WriteRune(r)
		}
	}
	if builder.Len() == 0 {
		return "unknown"
	}
	return builder.String()
}

func parseAllowedMissingThumbs(raw string) (map[string]struct{}, error) {
	allowed := make(map[string]struct{})
	for _, entry := range strings.Split(raw, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		parts := strings.Split(entry, ":")
		if len(parts) != 3 {
			return nil, fmt.Errorf("invalid allow-missing-thumb %q: want document_id:photo|video:type", entry)
		}
		documentID, err := strconv.ParseInt(parts[0], 10, 64)
		if err != nil || documentID <= 0 {
			return nil, fmt.Errorf("invalid allow-missing-thumb document ID %q", parts[0])
		}
		kind := parts[1]
		if kind != "photo" && kind != "video" {
			return nil, fmt.Errorf("invalid allow-missing-thumb kind %q", kind)
		}
		thumbType := parts[2]
		if thumbType == "" || safePart(thumbType) != thumbType {
			return nil, fmt.Errorf("invalid allow-missing-thumb type %q", thumbType)
		}
		allowed[missingThumbKey(documentID, kind, thumbType)] = struct{}{}
	}
	return allowed, nil
}

func missingThumbAllowed(allowed map[string]struct{}, documentID int64, kind, thumbType string) bool {
	_, ok := allowed[missingThumbKey(documentID, kind, thumbType)]
	return ok
}

func missingThumbKey(documentID int64, kind, thumbType string) string {
	return fmt.Sprintf("%d:%s:%s", documentID, kind, thumbType)
}

func writeArtifact(root, relative, kind, artifactType string, data []byte) (fileArtifact, error) {
	path := filepath.Join(root, relative)
	if err := writeFileAtomic(path, data); err != nil {
		return fileArtifact{}, err
	}
	sum := sha256.Sum256(data)
	return fileArtifact{
		Kind:   kind,
		Type:   artifactType,
		Path:   filepath.ToSlash(relative),
		Size:   int64(len(data)),
		SHA256: hex.EncodeToString(sum[:]),
	}, nil
}

func readTLArtifact(root, relative string, target interface{ Decode(*bin.Buffer) error }) (fileArtifact, error) {
	data, err := os.ReadFile(filepath.Join(root, relative))
	if err != nil {
		return fileArtifact{}, err
	}
	if len(data) == 0 {
		return fileArtifact{}, fmt.Errorf("TL artifact %q is empty", relative)
	}
	buffer := &bin.Buffer{Buf: data}
	if err := target.Decode(buffer); err != nil {
		return fileArtifact{}, fmt.Errorf("decode TL artifact %q: %w", relative, err)
	}
	if len(buffer.Buf) != 0 {
		return fileArtifact{}, fmt.Errorf("TL artifact %q has %d trailing bytes", relative, len(buffer.Buf))
	}
	sum := sha256.Sum256(data)
	return fileArtifact{
		Kind:   "tl",
		Path:   filepath.ToSlash(relative),
		Size:   int64(len(data)),
		SHA256: hex.EncodeToString(sum[:]),
	}, nil
}

func writeFileAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".giftfetch-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		if removeErr := os.Remove(path); removeErr != nil && !os.IsNotExist(removeErr) {
			return fmt.Errorf("replace %q: %w (remove existing: %v)", path, err, removeErr)
		}
		if err := os.Rename(tmpName, path); err != nil {
			return err
		}
	}
	return nil
}

// apiCredentials returns TG_API_ID/TG_API_HASH when set (the public TDesktop id
// is rate-limited with API_ID_PUBLISHED_FLOOD), otherwise the given defaults.
func apiCredentials(defID int, defHash string) (int, string) {
	id, hash := defID, defHash
	if v := strings.TrimSpace(os.Getenv("TG_API_ID")); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			id = n
		}
	}
	if v := strings.TrimSpace(os.Getenv("TG_API_HASH")); v != "" {
		hash = v
	}
	return id, hash
}
