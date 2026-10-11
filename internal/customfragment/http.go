package customfragment

import (
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
	"net"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"

	"telesrv/internal/domain"
)

const maxGiftMediaCacheEntries = 128

const owlJumpingRopeJSONSHA256 = "85b10fc39941272ad3a370fe07c08d6b36453917550c9aa9f79d94c0814d999b"

// owlJumpingRopeRaster is a deterministic frame rendered from the catalog's
// Owl Jumping Rope model. It is a compatibility fallback for optimized TGS
// documents that older system rlottie releases parse as a transparent frame.
//
//go:embed assets/owl-jumping-rope.png
var owlJumpingRopeRaster []byte

type collectibleModelAnimationResolver interface {
	CollectibleAnimationJSON(ctx context.Context, giftID int64, kind domain.StarGiftCollectibleAttributeKind, attributeID int64) ([]byte, bool, error)
}

type withdrawalPage struct {
	AppName      string
	RequestID    string
	Title        string
	Slug         string
	Number       int
	Status       string
	ExpiresAt    string
	Pending      bool
	Completed    bool
	OwnerAddress string
	GiftAddress  string
}

var withdrawalTemplate = template.Must(template.New("customfragment-withdrawal").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1,viewport-fit=cover">
<meta name="color-scheme" content="dark"><title>{{.Title}} · CustomFragment</title>
<script defer src="https://unpkg.com/@tonconnect/ui@2.3.1/dist/tonconnect-ui.min.js"></script>
<style>
:root{font-family:Arial,system-ui,sans-serif;color:#17212b;background:#f4f6f8;--tg-accent:#2481cc;--tg-muted:#8794a1;--tg-card:#fff;--tg-line:#d8e0e8}*{box-sizing:border-box}body{min-height:100vh;margin:0;background:linear-gradient(#e9f3fc,#f4f6f8 45%);padding:calc(18px + env(safe-area-inset-top)) 18px calc(24px + env(safe-area-inset-bottom))}.shell{width:min(100%,520px);margin:0 auto}.brand{display:flex;align-items:center;gap:10px;color:#52606d;font-size:14px;font-weight:700;margin:4px 0 14px}.brand i{width:34px;height:34px;border-radius:10px;background:var(--tg-accent);display:grid;place-items:center;color:white;font-style:normal}.card{position:relative;overflow:hidden;border:1px solid var(--tg-line);background:var(--tg-card);border-radius:12px;padding:22px;box-shadow:0 2px 12px #17212b12}.orb{width:132px;height:132px;margin:2px auto 20px;border-radius:50%;display:grid;place-items:center;background:#e9f3fc;box-shadow:inset 0 0 0 1px #2481cc22;font-size:48px}.eyebrow{text-align:center;color:var(--tg-accent);text-transform:uppercase;font-size:12px;font-weight:700;letter-spacing:.1em}.title{text-align:center;margin:8px 0 6px;font-size:clamp(28px,8vw,38px);line-height:1.08}.subtitle{text-align:center;color:var(--tg-muted);margin:0 0 24px}.facts{display:grid;grid-template-columns:1fr 1fr;gap:10px;margin:20px 0}.fact{background:#f4f7fa;border:1px solid var(--tg-line);border-radius:8px;padding:13px}.fact span{display:block;color:var(--tg-muted);font-size:11px;text-transform:uppercase;letter-spacing:.08em;margin-bottom:4px}.fact strong{font-size:14px}.notice{color:#52606d;font-size:13px;line-height:1.5;background:#f0f7fd;border-left:3px solid var(--tg-accent);border-radius:8px;padding:13px 14px;margin:16px 0}.actions{display:grid;gap:12px;margin-top:20px}#wallet{display:flex;justify-content:center;min-height:48px}button.primary{appearance:none;border:0;border-radius:8px;padding:13px 18px;background:var(--tg-accent);color:white;font:inherit;font-weight:700;cursor:pointer}button.primary:disabled{opacity:.45;cursor:not-allowed}.status{min-height:22px;text-align:center;color:var(--tg-muted);font-size:13px;margin-top:14px;overflow-wrap:anywhere}.status.error{color:#c83d42}.status.done{color:#168552}.chain{display:flex;align-items:center;justify-content:center;gap:7px;color:var(--tg-muted);font-size:12px;margin-top:20px}.dot{width:7px;height:7px;border-radius:50%;background:#2aa36b}.addresses{margin-top:18px;color:var(--tg-muted);font-size:12px;overflow-wrap:anywhere}.addresses p{padding:10px;background:#f4f7fa;border-radius:8px}.expired{text-align:center;color:#b56b2c}.footer{text-align:center;color:var(--tg-muted);font-size:11px;margin:18px 0}@media(prefers-color-scheme:dark){:root{color:#e8edf2;background:#17212b;--tg-card:#212a33;--tg-line:#35424f;--tg-muted:#aab7c4}body{background:linear-gradient(#1b2d3d,#17212b 50%)}.fact,.addresses p{background:#26333d}.notice{background:#263744;color:#c5d0da}.orb{background:#263744}}
</style></head><body><main class="shell" id="app" data-request="{{.RequestID}}"><div class="brand"><i>◆</i><span>{{.AppName}} CustomFragment</span></div><section class="card"><div class="orb">🎁</div><div class="eyebrow">Collectible gift</div><h1 class="title">{{.Title}}</h1><p class="subtitle">{{.Slug}} · #{{.Number}}</p><div class="facts"><div class="fact"><span>Network</span><strong>TON Mainnet</strong></div><div class="fact"><span>Status</span><strong id="state">{{.Status}}</strong></div></div>
{{if .Pending}}<div class="notice">Connect the wallet that should receive this NFT. The mint authorization is bound to that address and expires at {{.ExpiresAt}}.</div><div class="actions"><div id="wallet"></div><button class="primary" id="mint" disabled>Mint gift on TON</button></div><div class="status" id="message">Connect a TON mainnet wallet to continue.</div>{{else if .Completed}}<div class="status done">This gift is on TON mainnet.</div><div class="addresses"><p>Owner: {{.OwnerAddress}}</p><p>NFT: {{.GiftAddress}}</p></div>{{else}}<p class="expired">This withdrawal link is no longer active.</p>{{end}}
<div class="chain"><span class="dot"></span><span>Verified by a TON lite server</span></div></section><div class="footer">A self-hosted Gramsrv mint flow · no official Fragment server</div></main>
{{if .Pending}}<script>
window.addEventListener('DOMContentLoaded',()=>{const root=document.getElementById('app'),button=document.getElementById('mint'),message=document.getElementById('message'),state=document.getElementById('state'),requestID=root.dataset.request;let account=null,busy=false;const say=(text,kind='')=>{message.textContent=text;message.className='status '+kind};if(!window.TON_CONNECT_UI){say('TON Connect failed to load. Reload the page.','error');return}const tc=new TON_CONNECT_UI.TonConnectUI({manifestUrl:location.origin+'/custom-fragment/tonconnect-manifest.json',buttonRootId:'wallet'});tc.onStatusChange(wallet=>{account=wallet&&wallet.account?wallet.account:null;button.disabled=!account||busy;if(account&&String(account.chain)!=='-239'){button.disabled=true;say('Switch the wallet to TON Mainnet.','error')}else if(account){say('Wallet connected. You can mint the gift.')}else{say('Connect a TON mainnet wallet to continue.')}});const post=async(endpoint)=>{const response=await fetch('/custom-fragment/api/gifts/'+encodeURIComponent(requestID)+'/'+endpoint,{method:'POST',headers:{'content-type':'application/json'},body:JSON.stringify({wallet_address:account.address})});let data={};try{data=await response.json()}catch(_){}return{response,data}};const waitForMint=async()=>{for(let i=0;i<45;i++){const{response,data}=await post('confirm');if(response.ok){state.textContent='completed';say('Gift minted and verified on TON mainnet.','done');button.remove();document.getElementById('wallet').remove();return}if(response.status!==409)throw new Error(data.error||'Could not verify the NFT');say('Transaction sent. Waiting for mainnet finalization…');await new Promise(resolve=>setTimeout(resolve,3000))}throw new Error('Mainnet verification timed out. You can safely retry confirmation.')};button.addEventListener('click',async()=>{if(!account||busy)return;if(String(account.chain)!=='-239'){say('Switch the wallet to TON Mainnet.','error');return}busy=true;button.disabled=true;try{say('Creating a wallet-bound mint authorization…');const{response,data}=await post('intent');if(!response.ok)throw new Error(data.error||'Could not create mint authorization');await tc.sendTransaction({validUntil:data.valid_until,network:data.network,messages:[{address:data.collection_address,amount:data.amount,payload:data.payload}]});await waitForMint()}catch(error){say(error&&error.message?error.message:'Mint cancelled.','error');busy=false;button.disabled=!account}})});
</script>{{end}}</body></html>`))

func (s *Service) ServeWithdrawalPage(w http.ResponseWriter, r *http.Request, requestID string) {
	if s == nil || r.Method != http.MethodGet || !validRequestID(requestID) {
		http.NotFound(w, r)
		return
	}
	withdrawal, found, err := s.ledger.ResolveWithdrawal(r.Context(), requestID)
	if err != nil || !found {
		http.NotFound(w, r)
		return
	}
	page := withdrawalPage{
		AppName: s.appName, RequestID: requestID, Title: withdrawal.Gift.Title,
		Slug: withdrawal.Gift.Slug, Number: withdrawal.Gift.Num, Status: withdrawal.Status,
		ExpiresAt: time.Unix(int64(withdrawal.ExpiresAt), 0).UTC().Format(time.RFC3339),
		Pending:   withdrawal.Status == "pending" && withdrawal.ExpiresAt > int(time.Now().Unix()),
		Completed: withdrawal.Status == "completed", OwnerAddress: withdrawal.Gift.OwnerAddress,
		GiftAddress: withdrawal.Gift.GiftAddress,
	}
	if strings.TrimSpace(page.Title) == "" {
		page.Title = "Gramsrv Gift"
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := withdrawalTemplate.Execute(w, page); err != nil {
		s.logger.Warn("render CustomFragment withdrawal", zap.Error(err))
	}
}

func (s *Service) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if s == nil {
		http.NotFound(w, r)
		return
	}
	if r.Method == http.MethodPost && !s.allowPost(w, r) {
		return
	}
	switch {
	case r.Method == http.MethodGet && (r.URL.Path == "/custom-fragment" || r.URL.Path == "/custom-fragment/"):
		s.serveLanding(w)
	case r.Method == http.MethodGet && r.URL.Path == "/custom-fragment/tonconnect-manifest.json":
		s.serveManifest(w)
	case r.Method == http.MethodGet && r.URL.Path == "/custom-fragment/collection.json":
		s.serveCollectionMetadata(w)
	case r.Method == http.MethodGet && r.URL.Path == "/custom-fragment/icon.svg":
		serveIcon(w)
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/custom-fragment/metadata/gift/"):
		s.serveGiftMetadata(w, r)
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/custom-fragment/media/gift/") && strings.HasSuffix(r.URL.Path, ".lottie.json"):
		s.serveGiftAnimation(w, r)
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/custom-fragment/media/gift/"):
		s.serveGiftImage(w, r)
	case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/custom-fragment/api/gifts/"):
		s.serveGiftAPI(w, r)
	default:
		http.NotFound(w, r)
	}
}

const defaultGiftAPIRateWindow = time.Minute

// allowPost spends the per-IP budget before any mint intent/confirm work runs and writes its own rejection.
func (s *Service) allowPost(w http.ResponseWriter, r *http.Request) bool {
	if s.limiter == nil || !strings.HasPrefix(r.URL.Path, "/custom-fragment/api/gifts/") {
		return true
	}
	if s.apiRateLimit <= 0 {
		return true
	}
	window := s.apiRateWindow
	if window <= 0 {
		window = defaultGiftAPIRateWindow
	}
	sum := sha256.Sum256([]byte(giftAPIIP(r)))
	allowed, retryAfter, err := s.limiter.Allow(
		r.Context(),
		"customfragment:api:"+hex.EncodeToString(sum[:]),
		s.apiRateLimit,
		window)
	if err != nil {
		s.logger.Error("gift api rate limiter failed", zap.Error(err))
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "temporarily unavailable"})
		return false
	}
	if allowed {
		return true
	}
	if retryAfter <= 0 {
		retryAfter = 1
	}
	w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
	writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "too many requests, retry later"})
	return false
}

// giftAPIIP trusts the nginx-set headers first; RemoteAddr (loopback, port stripped) is the direct-call fallback.
func giftAPIIP(r *http.Request) string {
	ip := strings.TrimSpace(r.Header.Get("X-Real-IP"))
	if ip == "" {
		if forwarded := strings.TrimSpace(r.Header.Get("X-Forwarded-For")); forwarded != "" {
			if i := strings.LastIndexByte(forwarded, ','); i >= 0 {
				ip = forwarded[i+1:]
			} else {
				ip = forwarded
			}
			ip = strings.TrimSpace(ip)
		}
	}
	if ip == "" {
		ip = r.RemoteAddr
		if host, _, err := net.SplitHostPort(ip); err == nil {
			ip = host
		}
	}
	return ip
}

func (s *Service) serveGiftAPI(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/custom-fragment/api/gifts/")
	parts := strings.Split(rest, "/")
	if len(parts) != 2 || !validRequestID(parts[0]) || (parts[1] != "intent" && parts[1] != "confirm") {
		http.NotFound(w, r)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	var input struct {
		WalletAddress string `json:"wallet_address"`
	}
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil || strings.TrimSpace(input.WalletAddress) == "" {
		writeAPIError(w, ErrInvalidRequest)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if parts[1] == "intent" {
		intent, err := s.Intent(r.Context(), parts[0], input.WalletAddress, time.Now())
		if err != nil {
			writeAPIError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, intent)
		return
	}
	confirmation, err := s.Confirm(r.Context(), parts[0], input.WalletAddress, time.Now())
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, confirmation)
}

func (s *Service) serveManifest(w http.ResponseWriter) {
	writeJSON(w, http.StatusOK, map[string]string{
		"url": s.publicBaseURL + "/custom-fragment", "name": s.appName + " CustomFragment",
		"iconUrl": s.publicBaseURL + "/custom-fragment/icon.svg",
	})
}

func (s *Service) serveCollectionMetadata(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "public, max-age=3600")
	writeJSON(w, http.StatusOK, map[string]any{
		"name":         s.collectionName,
		"description":  "Unique collectible gifts exported from InvGram/Gramsrv to TON mainnet.",
		"image":        s.publicBaseURL + "/custom-fragment/icon.svg",
		"external_url": s.publicBaseURL + "/custom-fragment",
	})
}

func (s *Service) serveLanding(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = fmt.Fprintf(w, `<!doctype html><meta charset="utf-8"><meta name="viewport" content="width=device-width"><title>CustomFragment</title><style>body{font:16px/1.5 system-ui;background:#0b1220;color:#eef5ff;max-width:680px;margin:12vh auto;padding:24px}code{overflow-wrap:anywhere;color:#76c4ff}</style><h1>%s</h1><p>Self-hosted TON mainnet withdrawal for unique Gramsrv gifts. Open the private withdrawal URL returned by <code>payments.getStarGiftWithdrawalUrl</code>.</p><p>Collection: <code>%s</code></p><p>Signing public key: <code>%s</code></p>`, template.HTMLEscapeString(s.collectionName), s.collection.StringRaw(), s.PublicKeyHex())
}

func (s *Service) serveGiftMetadata(w http.ResponseWriter, r *http.Request) {
	slug := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/custom-fragment/metadata/gift/"), ".json")
	gift, ok := s.resolvePublicGift(w, r, slug)
	if !ok {
		return
	}
	attributes := []map[string]string{
		{"trait_type": "Model", "value": gift.Model.Name},
		{"trait_type": "Pattern", "value": gift.Pattern.Name},
		{"trait_type": "Backdrop", "value": gift.Backdrop.Name},
		{"trait_type": "Number", "value": strconv.Itoa(gift.Num)},
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"name":         gift.Slug,
		"description":  "A unique collectible gift exported from " + s.appName + " to TON mainnet.",
		"image":        s.publicBaseURL + path.Join("/custom-fragment/media/gift/", gift.Slug+".png"),
		"lottie":       s.publicBaseURL + path.Join("/custom-fragment/media/gift/", gift.Slug+".lottie.json"),
		"external_url": s.publicBaseURL + path.Join("/nft/", gift.Slug),
		"attributes":   attributes,
	})
}

func (s *Service) serveGiftImage(w http.ResponseWriter, r *http.Request) {
	slug := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/custom-fragment/media/gift/"), ".png")
	gift, ok := s.resolvePublicGift(w, r, slug)
	if !ok {
		return
	}
	data, err := s.giftImagePNG(r.Context(), gift)
	if err != nil {
		s.logger.Warn("render CustomFragment gift image", zap.String("slug", gift.Slug), zap.Error(err))
		http.Error(w, "gift image is temporarily unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "public, max-age=86400, immutable")
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	_, _ = w.Write(data)
}

func (s *Service) serveGiftAnimation(w http.ResponseWriter, r *http.Request) {
	slug := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/custom-fragment/media/gift/"), ".lottie.json")
	gift, ok := s.resolvePublicGift(w, r, slug)
	if !ok {
		return
	}
	data, err := s.giftAnimationJSON(r.Context(), gift)
	if err != nil {
		s.logger.Warn("load CustomFragment gift animation", zap.String("slug", gift.Slug), zap.Error(err))
		http.Error(w, "gift animation is temporarily unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "public, max-age=86400, immutable")
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	_, _ = w.Write(data)
}

// giftAnimationJSON serves the selected collectible model's Lottie document
// as-is. The endpoint mirrors Fragment's .lottie.json media convention: the
// animation stays JSON (rather than being rasterized or wrapped in metadata)
// so clients can choose their own frame and renderer.
func (s *Service) giftAnimationJSON(ctx context.Context, gift domain.UniqueStarGift) ([]byte, error) {
	return s.collectibleAnimationJSON(ctx, gift, domain.StarGiftCollectibleModel, gift.Model.ID)
}

func (s *Service) collectibleAnimationJSON(ctx context.Context, gift domain.UniqueStarGift, kind domain.StarGiftCollectibleAttributeKind, attributeID int64) ([]byte, error) {
	if attributeID <= 0 {
		return nil, errors.New("collectible animation attribute is unavailable")
	}
	cacheKey := fmt.Sprintf("%s:%s:%d", gift.Slug, kind, attributeID)
	s.imageMu.RLock()
	cached := append([]byte(nil), s.animationCache[cacheKey]...)
	s.imageMu.RUnlock()
	if len(cached) > 0 {
		return cached, nil
	}
	resolver, ok := s.ledger.(collectibleModelAnimationResolver)
	if !ok {
		return nil, errors.New("collectible animation resolver is unavailable")
	}
	modelJSON, found, err := resolver.CollectibleAnimationJSON(ctx, gift.GiftID, kind, attributeID)
	if err != nil {
		return nil, fmt.Errorf("load %s animation: %w", kind, err)
	}
	if !found || len(modelJSON) == 0 {
		return nil, fmt.Errorf("selected %s animation is unavailable", kind)
	}
	if !json.Valid(modelJSON) {
		return nil, fmt.Errorf("selected %s animation is invalid JSON", kind)
	}

	data := append([]byte(nil), modelJSON...)
	s.imageMu.Lock()
	if len(s.animationCache) >= maxGiftMediaCacheEntries {
		clear(s.animationCache)
	}
	s.animationCache[cacheKey] = data
	s.imageMu.Unlock()
	return append([]byte(nil), data...), nil
}

func (s *Service) giftImagePNG(ctx context.Context, gift domain.UniqueStarGift) ([]byte, error) {
	s.imageMu.RLock()
	cached := append([]byte(nil), s.imageCache[gift.Slug]...)
	s.imageMu.RUnlock()
	if len(cached) > 0 {
		return cached, nil
	}
	modelJSON, err := s.collectibleAnimationJSON(ctx, gift, domain.StarGiftCollectibleModel, gift.Model.ID)
	if err != nil {
		return nil, err
	}
	const canvasSize, modelSize = 1024, 1024
	// Render the rest pose (frame 0), not an arbitrary point in the loop. This
	// keeps static posters centered when an animation begins with a reveal or
	// spin and matches the model shown by the official gift catalog.
	const modelRestPosition = 0
	model, renderErr := s.renderModel(modelJSON, modelSize, modelSize, modelRestPosition)
	if renderErr != nil || !hasVisibleAlpha(model) {
		model, err = fallbackModelRaster(modelJSON)
		if err != nil {
			if renderErr != nil {
				return nil, fmt.Errorf("render selected model: %w", renderErr)
			}
			return nil, errors.New("render selected model: transparent Lottie frame")
		}
	}
	canvas := renderBackdrop(canvasSize, gift.Backdrop.CenterColor, gift.Backdrop.EdgeColor)
	// Patterns are a best-effort decorative layer. Older gifts may not have a
	// stored pattern or a renderer-compatible document; that must not make the
	// model poster unavailable.
	s.compositePattern(ctx, canvas, gift)
	draw.Draw(canvas, image.Rect(0, 0, modelSize, modelSize), model, image.Point{}, draw.Over)
	var encoded bytes.Buffer
	encoder := png.Encoder{CompressionLevel: png.BestSpeed}
	if err := encoder.Encode(&encoded, canvas); err != nil {
		return nil, fmt.Errorf("encode gift poster: %w", err)
	}
	data := encoded.Bytes()
	s.imageMu.Lock()
	if len(s.imageCache) >= maxGiftMediaCacheEntries {
		clear(s.imageCache)
	}
	s.imageCache[gift.Slug] = append([]byte(nil), data...)
	s.imageMu.Unlock()
	return data, nil
}

// compositePattern renders the collectible pattern into a small alpha tile,
// tints it with the backdrop's pattern color, and repeats it behind the model.
func (s *Service) compositePattern(ctx context.Context, canvas *image.NRGBA, gift domain.UniqueStarGift) {
	if gift.Pattern.ID <= 0 {
		return
	}
	patternJSON, err := s.collectibleAnimationJSON(ctx, gift, domain.StarGiftCollectiblePattern, gift.Pattern.ID)
	if err != nil {
		s.logger.Debug("CustomFragment pattern animation unavailable", zap.String("slug", gift.Slug), zap.Error(err))
		return
	}
	const tileSize = 132
	tile, err := s.renderModel(patternJSON, tileSize, tileSize, 0)
	if err != nil || !hasVisibleAlpha(tile) {
		return
	}
	tilePattern(canvas, tintPattern(tile, gift.Backdrop.PatternColor, 0.5))
}

func tintPattern(src *image.NRGBA, rgb int, opacity float64) *image.NRGBA {
	if src == nil {
		return nil
	}
	if opacity < 0 {
		opacity = 0
	} else if opacity > 1 {
		opacity = 1
	}
	tint := rgbColor(rgb)
	out := image.NewNRGBA(src.Bounds())
	for i := 0; i+3 < len(src.Pix); i += 4 {
		alpha := src.Pix[i+3]
		if alpha == 0 {
			continue
		}
		out.Pix[i] = tint.R
		out.Pix[i+1] = tint.G
		out.Pix[i+2] = tint.B
		out.Pix[i+3] = uint8(math.Round(float64(alpha) * opacity))
	}
	return out
}

func tilePattern(canvas, tile *image.NRGBA) {
	if canvas == nil || tile == nil {
		return
	}
	tw, th := tile.Bounds().Dx(), tile.Bounds().Dy()
	size := canvas.Bounds().Dx()
	if tw <= 0 || th <= 0 || size <= 0 {
		return
	}
	stepX, stepY := tw+tw/3, th+th/3
	row := 0
	for y := -th; y < size+th; y += stepY {
		offsetX := 0
		if row%2 == 1 {
			offsetX = stepX / 2
		}
		for x := -tw - offsetX; x < size+tw; x += stepX {
			draw.Draw(canvas, tile.Bounds().Add(image.Pt(x, y)), tile, image.Point{}, draw.Over)
		}
		row++
	}
}

func hasVisibleAlpha(model *image.NRGBA) bool {
	if model == nil {
		return false
	}
	for offset := 3; offset < len(model.Pix); offset += 4 {
		if model.Pix[offset] != 0 {
			return true
		}
	}
	return false
}

func fallbackModelRaster(modelJSON []byte) (*image.NRGBA, error) {
	sum := sha256.Sum256(modelJSON)
	if fmt.Sprintf("%x", sum) != owlJumpingRopeJSONSHA256 {
		return nil, errors.New("no raster fallback for selected model")
	}
	decoded, err := png.Decode(bytes.NewReader(owlJumpingRopeRaster))
	if err != nil {
		return nil, fmt.Errorf("decode model raster fallback: %w", err)
	}
	if decoded.Bounds() != image.Rect(0, 0, 1024, 1024) {
		return nil, errors.New("model raster fallback has invalid dimensions")
	}
	out := image.NewNRGBA(decoded.Bounds())
	draw.Draw(out, out.Bounds(), decoded, decoded.Bounds().Min, draw.Src)
	return out, nil
}

func renderBackdrop(size, centerRGB, edgeRGB int) *image.NRGBA {
	if size <= 0 {
		size = 1024
	}
	center, edge := rgbColor(centerRGB), rgbColor(edgeRGB)
	out := image.NewNRGBA(image.Rect(0, 0, size, size))
	cx, cy := float64(size)/2, float64(size)*0.42
	maxDistance := math.Hypot(math.Max(cx, float64(size)-cx), math.Max(cy, float64(size)-cy))
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			t := math.Min(1, math.Hypot(float64(x)-cx, float64(y)-cy)/maxDistance)
			t = t * t * (3 - 2*t)
			offset := y*out.Stride + x*4
			out.Pix[offset] = mixChannel(center.R, edge.R, t)
			out.Pix[offset+1] = mixChannel(center.G, edge.G, t)
			out.Pix[offset+2] = mixChannel(center.B, edge.B, t)
			out.Pix[offset+3] = 255
		}
	}
	return out
}

func rgbColor(value int) color.NRGBA {
	if value < 0 || value > 0xffffff {
		value = 0x15233d
	}
	return color.NRGBA{R: uint8(value >> 16), G: uint8(value >> 8), B: uint8(value), A: 255}
}

func mixChannel(from, to uint8, ratio float64) uint8 {
	return uint8(math.Round(float64(from)*(1-ratio) + float64(to)*ratio))
}

func (s *Service) resolvePublicGift(w http.ResponseWriter, r *http.Request, slug string) (domain.UniqueStarGift, bool) {
	if !validGiftSlug(slug) {
		http.NotFound(w, r)
		return domain.UniqueStarGift{}, false
	}
	gift, found, err := s.ledger.UniqueBySlug(r.Context(), slug)
	if err != nil || !found || gift.Burned {
		http.NotFound(w, r)
		return domain.UniqueStarGift{}, false
	}
	return gift, true
}

func validGiftSlug(slug string) bool {
	if slug == "" || len(slug) > domain.MaxStarGiftSlugBytes {
		return false
	}
	for _, char := range slug {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || char == '.' || char == '_' || char == '-' {
			continue
		}
		return false
	}
	return true
}

func serveIcon(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "image/svg+xml")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	_, _ = w.Write([]byte(`<svg xmlns="http://www.w3.org/2000/svg" width="512" height="512" viewBox="0 0 512 512"><defs><linearGradient id="g" x2="1" y2="1"><stop stop-color="#35adff"/><stop offset="1" stop-color="#725cff"/></linearGradient></defs><rect width="512" height="512" rx="118" fill="url(#g)"/><path fill="white" d="M129 196h254v205H129zM107 144h298v76H107z"/><path fill="none" stroke="#5c63f0" stroke-width="34" d="M256 144v257M178 144c-49-55 38-99 78 0m78 0c49-55-38-99-78 0"/></svg>`))
}

func writeAPIError(w http.ResponseWriter, err error) {
	status, message := http.StatusInternalServerError, "CustomFragment is temporarily unavailable"
	switch {
	case errors.Is(err, ErrInvalidRequest):
		status, message = http.StatusBadRequest, "Invalid withdrawal or wallet address"
	case errors.Is(err, ErrAlreadyExported):
		status, message = http.StatusConflict, "This gift is already exported"
	case errors.Is(err, ErrNotFinalized):
		status, message = http.StatusConflict, "NFT not finalized yet"
	case errors.Is(err, ErrUnavailable):
		status, message = http.StatusGone, "This withdrawal is no longer available"
	}
	writeJSON(w, status, map[string]string{"error": message})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
