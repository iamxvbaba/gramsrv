package customfragment

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/xssnick/tonutils-go/address"
	"github.com/xssnick/tonutils-go/tvm/cell"

	"telesrv/internal/domain"
)

type fakeLedger struct {
	withdrawal           domain.StarGiftWithdrawal
	completed            bool
	owner                string
	item                 string
	animationKind        domain.StarGiftCollectibleAttributeKind
	animationAttributeID int64
	animationCalls       int
	animationKinds       []domain.StarGiftCollectibleAttributeKind
	animationIDs         []int64
	renderPositions      []float64
}

func (f *fakeLedger) ResolveWithdrawal(_ context.Context, requestID string) (domain.StarGiftWithdrawal, bool, error) {
	if requestID != f.withdrawal.ProviderRequestID {
		return domain.StarGiftWithdrawal{}, false, nil
	}
	return f.withdrawal, true, nil
}

func (f *fakeLedger) CompleteWithdrawalOnChain(_ context.Context, requestID, owner, item string, _ int) (domain.StarGiftWithdrawal, error) {
	if requestID != f.withdrawal.ProviderRequestID {
		return domain.StarGiftWithdrawal{}, ErrInvalidRequest
	}
	f.completed, f.owner, f.item = true, owner, item
	f.withdrawal.Status = "completed"
	f.withdrawal.Gift.OwnerAddress = owner
	f.withdrawal.Gift.GiftAddress = item
	return f.withdrawal, nil
}

func (f *fakeLedger) UniqueBySlug(_ context.Context, slug string) (domain.UniqueStarGift, bool, error) {
	return f.withdrawal.Gift, slug == f.withdrawal.Gift.Slug, nil
}

func (f *fakeLedger) CollectibleAnimationJSON(_ context.Context, _ int64, kind domain.StarGiftCollectibleAttributeKind, attributeID int64) ([]byte, bool, error) {
	f.animationKind, f.animationAttributeID = kind, attributeID
	f.animationCalls++
	f.animationKinds = append(f.animationKinds, kind)
	f.animationIDs = append(f.animationIDs, attributeID)
	return []byte(`{"v":"5.5.2"}`), true, nil
}

func (f *fakeLedger) hasAnimationRequest(kind domain.StarGiftCollectibleAttributeKind, attributeID int64) bool {
	for i := range f.animationKinds {
		if f.animationKinds[i] == kind && f.animationIDs[i] == attributeID {
			return true
		}
	}
	return false
}

type fakeVerifier struct {
	item       string
	collection string
	owner      string
	index      *big.Int
	err        error
}

func (f *fakeVerifier) VerifyMint(_ context.Context, collection string, index *big.Int, owner string) (string, error) {
	f.collection, f.index, f.owner = collection, new(big.Int).Set(index), owner
	return f.item, f.err
}
func (*fakeVerifier) Close() {}

func testAddress(fill byte) *address.Address {
	data := make([]byte, 32)
	for i := range data {
		data[i] = fill
	}
	return address.NewAddress(0, 0, data)
}

func testService(t *testing.T, now time.Time) (*Service, *fakeLedger, *fakeVerifier, *address.Address) {
	t.Helper()
	_, privateKey, err := ed25519.GenerateKey(strings.NewReader(strings.Repeat("s", 64)))
	if err != nil {
		t.Fatal(err)
	}
	collection, owner, item := testAddress(1), testAddress(2), testAddress(3)
	ledger := &fakeLedger{withdrawal: domain.StarGiftWithdrawal{
		ProviderRequestID: "withdraw-token", ExpiresAt: int(now.Add(10 * time.Minute).Unix()), Status: "pending",
		Gift: domain.UniqueStarGift{ID: 42, GiftID: 7, Title: "Crystal Star", Slug: "CrystalStar-42", Num: 42,
			Model:    domain.StarGiftCollectibleAttribute{ID: 11, Name: "Azure"},
			Pattern:  domain.StarGiftCollectibleAttribute{ID: 12, Name: "Comet"},
			Backdrop: domain.StarGiftCollectibleAttribute{Name: "Midnight", CenterColor: 0x336699, EdgeColor: 0x112233}},
	}}
	verifier := &fakeVerifier{item: item.StringRaw()}
	service, err := New(Config{
		PublicBaseURL: "https://grams.example", AppName: "Gramsrv", SigningKey: privateKey,
		GiftCollection: collection.String(), MintAmountNanoton: 80_000_000,
		SubwalletID: defaultSubwalletID, AuthorizationTTL: 5 * time.Minute,
		ModelRenderer: func(_ []byte, width, height int, position float64) (*image.NRGBA, error) {
			ledger.renderPositions = append(ledger.renderPositions, position)
			frame := image.NewNRGBA(image.Rect(0, 0, width, height))
			frame.SetNRGBA(width/2, height/2, color.NRGBA{R: 255, A: 255})
			return frame, nil
		},
	}, ledger, verifier, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(service.Close)
	return service, ledger, verifier, owner
}

func TestIntentBuildsTolkCompatibleSignedPayload(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	service, _, _, owner := testService(t, now)
	intent, err := service.Intent(context.Background(), "withdraw-token", owner.String(), now)
	if err != nil {
		t.Fatal(err)
	}
	if intent.Network != mainnetChain || intent.CollectionAddress != service.collection.String() ||
		strings.Contains(intent.CollectionAddress, ":") || intent.Amount != "80000000" ||
		intent.WalletAddress != owner.StringRaw() || intent.ItemIndex != "42" {
		t.Fatalf("unexpected intent: %+v", intent)
	}
	boc, err := base64.StdEncoding.DecodeString(intent.Payload)
	if err != nil {
		t.Fatal(err)
	}
	body, err := cell.FromBOC(boc)
	if err != nil {
		t.Fatal(err)
	}
	slice, err := body.BeginParse()
	if err != nil {
		t.Fatal(err)
	}
	opcode, err := slice.LoadUInt(32)
	if err != nil || opcode != mintOpcode {
		t.Fatalf("opcode=%x err=%v", opcode, err)
	}
	signature, err := slice.LoadSlice(512)
	if err != nil {
		t.Fatal(err)
	}
	authorization, err := slice.LoadRefCell()
	if err != nil {
		t.Fatal(err)
	}
	if !ed25519.Verify(service.publicKey, authorization.Hash(), signature) {
		t.Fatal("payload signature does not cover the authorization cell")
	}
	auth, err := authorization.BeginParse()
	if err != nil {
		t.Fatal(err)
	}
	subwallet, _ := auth.LoadUInt(32)
	_, _ = auth.LoadUInt(32)
	validTill, _ := auth.LoadUInt(32)
	index, _ := auth.LoadBigUInt(256)
	parsedOwner, err := auth.LoadAddr()
	if err != nil {
		t.Fatal(err)
	}
	content, err := auth.LoadRefCell()
	if err != nil {
		t.Fatal(err)
	}
	contentSlice, err := content.BeginParse()
	if err != nil {
		t.Fatal(err)
	}
	prefix, _ := contentSlice.LoadUInt(8)
	metadata, err := contentSlice.LoadStringSnake()
	if err != nil {
		t.Fatal(err)
	}
	if subwallet != uint64(defaultSubwalletID) || validTill != uint64(intent.ValidUntil) ||
		index.Cmp(big.NewInt(42)) != 0 || !parsedOwner.Equals(owner) || prefix != 1 ||
		metadata != "https://grams.example/custom-fragment/metadata/gift/CrystalStar-42.json" {
		t.Fatalf("authorization mismatch: subwallet=%x validTill=%d index=%s owner=%s prefix=%d metadata=%q",
			subwallet, validTill, index, parsedOwner.StringRaw(), prefix, metadata)
	}
}

func TestConfirmCompletesOnlyAfterVerifierMatches(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	service, ledger, verifier, owner := testService(t, now)
	confirmation, err := service.Confirm(context.Background(), "withdraw-token", owner.String(), now)
	if err != nil {
		t.Fatal(err)
	}
	if !ledger.completed || verifier.index.Cmp(big.NewInt(42)) != 0 || verifier.owner != owner.StringRaw() ||
		confirmation.Status != "completed" || confirmation.GiftAddress != verifier.item {
		t.Fatalf("confirmation=%+v ledger=%+v verifier=%+v", confirmation, ledger, verifier)
	}
}

func TestHTTPPageAndMetadata(t *testing.T) {
	now := time.Now()
	service, ledger, _, _ := testService(t, now)
	page := httptest.NewRecorder()
	service.ServeWithdrawalPage(page, httptest.NewRequest(http.MethodGet, "/gift-withdrawal/withdraw-token", nil), "withdraw-token")
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), "TON Mainnet") || !strings.Contains(page.Body.String(), "Mint gift on TON") {
		t.Fatalf("page status=%d body=%q", page.Code, page.Body.String())
	}
	if strings.Contains(page.Body.String(), string(service.privateKey)) {
		t.Fatal("withdrawal page exposed the private signing key")
	}

	metadata := httptest.NewRecorder()
	service.ServeHTTP(metadata, httptest.NewRequest(http.MethodGet, "/custom-fragment/metadata/gift/CrystalStar-42.json", nil))
	if metadata.Code != http.StatusOK || !strings.Contains(metadata.Body.String(), `"name":"CrystalStar-42"`) ||
		!strings.Contains(metadata.Body.String(), `"image":"https://grams.example/custom-fragment/media/gift/CrystalStar-42.png"`) ||
		!strings.Contains(metadata.Body.String(), `"lottie":"https://grams.example/custom-fragment/media/gift/CrystalStar-42.lottie.json"`) ||
		!strings.Contains(metadata.Body.String(), `"trait_type":"Model"`) {
		t.Fatalf("metadata status=%d body=%q", metadata.Code, metadata.Body.String())
	}

	animation := httptest.NewRecorder()
	service.ServeHTTP(animation, httptest.NewRequest(http.MethodGet, "/custom-fragment/media/gift/CrystalStar-42.lottie.json", nil))
	if animation.Code != http.StatusOK || animation.Header().Get("Content-Type") != "application/json" ||
		animation.Body.String() != `{"v":"5.5.2"}` {
		t.Fatalf("animation status=%d content-type=%q body=%q", animation.Code, animation.Header().Get("Content-Type"), animation.Body.String())
	}
	animationCalls := ledger.animationCalls
	animationCached := httptest.NewRecorder()
	service.ServeHTTP(animationCached, httptest.NewRequest(http.MethodGet, "/custom-fragment/media/gift/CrystalStar-42.lottie.json", nil))
	if animationCached.Code != http.StatusOK || ledger.animationCalls != animationCalls {
		t.Fatalf("cached animation status=%d calls=%d want=%d", animationCached.Code, ledger.animationCalls, animationCalls)
	}

	poster := httptest.NewRecorder()
	service.ServeHTTP(poster, httptest.NewRequest(http.MethodGet, "/custom-fragment/media/gift/CrystalStar-42.png", nil))
	if poster.Code != http.StatusOK || poster.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("poster status=%d content-type=%q body=%q", poster.Code, poster.Header().Get("Content-Type"), poster.Body.String())
	}
	decoded, err := png.Decode(bytes.NewReader(poster.Body.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if !ledger.hasAnimationRequest(domain.StarGiftCollectibleModel, 11) || !ledger.hasAnimationRequest(domain.StarGiftCollectiblePattern, 12) {
		t.Fatalf("poster animation requests = %#v/%#v; want model and pattern", ledger.animationKinds, ledger.animationIDs)
	}
	if len(ledger.renderPositions) == 0 || ledger.renderPositions[0] != 0 {
		t.Fatalf("first render position = %#v; want rest frame 0", ledger.renderPositions)
	}
	r, g, b, a := decoded.At(512, 512).RGBA()
	if r != 0xffff || g != 0 || b != 0 || a != 0xffff {
		t.Fatalf("poster model pixel = %04x/%04x/%04x/%04x", r, g, b, a)
	}
}

func TestParseMainnetAddressRejectsTestnetAndMasterchain(t *testing.T) {
	if _, err := parseMainnetAddress(testAddress(4).Testnet(true).String()); err == nil {
		t.Fatal("accepted a testnet-only friendly address")
	}
	masterchain := address.NewAddress(0, byte(0xff), make([]byte, 32))
	if _, err := parseMainnetAddress(masterchain.StringRaw()); err == nil {
		t.Fatal("accepted a masterchain address")
	}
}
