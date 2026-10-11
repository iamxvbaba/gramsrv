package stargifts

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"telesrv/internal/app/ai"
	"telesrv/internal/domain"
	"telesrv/internal/lottierender"
)

func TestCrossCraftUsesOneAIRequest(t *testing.T) {
	frogPath, bagPath := os.Getenv("TELESRV_CROSS_CRAFT_FROG"), os.Getenv("TELESRV_CROSS_CRAFT_BAG")
	if frogPath == "" || bagPath == "" {
		t.Skip("frog and bag fixtures not supplied")
	}
	frog, err := os.ReadFile(frogPath)
	if err != nil {
		t.Fatal(err)
	}
	bag, err := os.ReadFile(bagPath)
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	var requestPrompt atomic.Value
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var request struct {
			Messages []struct {
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err == nil && len(request.Messages) == 1 && len(request.Messages[0].Content) > 0 {
			requestPrompt.Store(request.Messages[0].Content[0].Text)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": `{"parts":[{"source":0,"role":"subject","x":256,"y":256,"scale":0.95,"rotation":0},{"source":1,"role":"prop","x":285,"y":305,"scale":0.42,"rotation":0}],"foreground_layers":[2,4,27],"story":"The frog holds the handbag by its handle.","name":"Toad Carrier"}`}}}})
	}))
	defer server.Close()
	blobs := &testGiftBlob{data: make(map[string][]byte)}
	generator := &CrossCraftGenerator{endpoint: server.URL, key: "test", model: "test", client: server.Client(), blobs: blobs, dc: 2}
	sources := []domain.StarGiftCraftSource{{ModelName: "Louis Vuittoad", AnimationJSON: frog}, {ModelName: "Verso Blue", AnimationJSON: bag}}
	model, err := generator.GenerateCrossCraft(context.Background(), sources)
	if err != nil {
		t.Fatal(err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("AI requests = %d, want 1", got)
	}
	prompt, _ := requestPrompt.Load().(string)
	for _, example := range []string{"Clown statue + smiley face", "Frog + handbag", "Robot + flower", "Turtle + house", "hidden_layers"} {
		if !strings.Contains(prompt, example) {
			t.Fatalf("AI prompt misses %q", example)
		}
	}
	if model.Name != "Toad Carrier" || model.Animation == nil || model.Blob == nil || len(blobs.data) != 1 {
		t.Fatalf("craft result was not materialized: %+v", model)
	}
	replayed, err := generator.GenerateCrossCraft(context.Background(), sources)
	if err != nil {
		t.Fatal(err)
	}
	if replayed.Document == nil || replayed.Document.ID != model.Document.ID || calls.Load() != 1 {
		t.Fatalf("retry generated another AI request or document: calls=%d", calls.Load())
	}
	reversed, err := generator.GenerateCrossCraft(context.Background(), []domain.StarGiftCraftSource{sources[1], sources[0]})
	if err != nil || reversed.Document == nil || reversed.Document.ID != model.Document.ID || calls.Load() != 1 {
		t.Fatalf("reordered retry generated another AI request: calls=%d err=%v", calls.Load(), err)
	}
}

func TestCrossCraftDoesNotRetryFailedAI(t *testing.T) {
	frogPath, bagPath := os.Getenv("TELESRV_CROSS_CRAFT_FROG"), os.Getenv("TELESRV_CROSS_CRAFT_BAG")
	if frogPath == "" || bagPath == "" {
		t.Skip("frog and bag fixtures not supplied")
	}
	frog, err := os.ReadFile(frogPath)
	if err != nil {
		t.Fatal(err)
	}
	bag, err := os.ReadFile(bagPath)
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	generator := &CrossCraftGenerator{endpoint: server.URL, key: "test", model: "test", client: server.Client(), blobs: &testGiftBlob{data: make(map[string][]byte)}, dc: 2}
	sources := []domain.StarGiftCraftSource{{ModelName: "Louis Vuittoad", AnimationJSON: frog}, {ModelName: "Verso Blue", AnimationJSON: bag}}
	for attempt := 0; attempt < 2; attempt++ {
		if _, err := generator.GenerateCrossCraft(context.Background(), sources); err == nil {
			t.Fatal("AI failure unexpectedly succeeded")
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("AI failure retry sent %d requests, want 1", got)
	}
}

func TestCrossCraftAIRequestBudget(t *testing.T) {
	var calls atomic.Int32
	var tokenLimitSent atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var request map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode craft AI request: %v", err)
		}
		if _, ok := request["max_tokens"]; ok {
			tokenLimitSent.Store(true)
		}
		if _, ok := request["max_output_tokens"]; ok {
			tokenLimitSent.Store(true)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"OK"}}]}`))
	}))
	defer server.Close()
	generator := &CrossCraftGenerator{endpoint: server.URL, key: "test", model: "test", client: server.Client()}
	budget := &crossCraftAIRequestBudget{}
	if _, err := generator.analyze(context.Background(), "test", nil, budget); err != nil {
		t.Fatal(err)
	}
	if _, err := generator.analyze(context.Background(), "test", nil, budget); err == nil {
		t.Fatal("accepted a second AI request for one craft")
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("outbound AI requests = %d, want 1", got)
	}
	if tokenLimitSent.Load() {
		t.Fatal("craft AI request unexpectedly sets an output token limit")
	}
}

func TestCrossCraftQueuesConcurrentAIRequests(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		close(entered)
		<-release
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"OK"}}]}`))
	}))
	defer server.Close()
	generator := NewCrossCraftGenerator(ai.ProviderConfig{Kind: ai.ProviderKindOpenAIChat,
		APIKey: "test", Model: "test", BaseURL: server.URL}, &testGiftBlob{}, 2)
	generator.client = server.Client()
	firstDone := make(chan error, 1)
	go func() {
		_, err := generator.analyze(context.Background(), "first", nil, &crossCraftAIRequestBudget{})
		firstDone <- err
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("first AI request did not start")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := generator.analyze(ctx, "second", nil, &crossCraftAIRequestBudget{}); err != context.Canceled {
		t.Fatalf("queued request error = %v, want context canceled", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("queued craft sent %d AI requests, want 1 active request", got)
	}
	close(release)
	select {
	case err := <-firstDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("first AI request did not finish")
	}
}

func TestCrossCraftCXUsesOneResponsesRequest(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/v1/responses" {
			t.Errorf("request path = %q", r.URL.Path)
		}
		var request struct {
			Model           string `json:"model"`
			MaxOutputTokens int    `json:"max_output_tokens"`
			Input           []struct {
				Content []struct {
					Type     string `json:"type"`
					ImageURL string `json:"image_url"`
				} `json:"content"`
			} `json:"input"`
			Messages json.RawMessage `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		if request.Model != "cx/gpt-5.6-luna-high" || request.MaxOutputTokens != 2048 ||
			len(request.Messages) != 0 || len(request.Input) != 1 || len(request.Input[0].Content) != 2 ||
			request.Input[0].Content[0].Type != "input_text" ||
			request.Input[0].Content[1].Type != "input_image" ||
			!strings.HasPrefix(request.Input[0].Content[1].ImageURL, "data:image/png;base64,") {
			t.Errorf("unexpected Responses request: %+v", request)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"{\"name\":\"Fusion\"}"}]}]}`))
	}))
	defer server.Close()
	generator := NewCrossCraftGenerator(ai.ProviderConfig{Kind: ai.ProviderKindOpenAIChat,
		APIKey: "test", Model: "cx/gpt-5.6-luna-high", BaseURL: server.URL + "/v1"}, &testGiftBlob{}, 2)
	if generator == nil {
		t.Fatal("CX generator is unavailable")
	}
	generator.client = server.Client()
	budget := &crossCraftAIRequestBudget{}
	answer, err := generator.analyze(context.Background(), "merge", [][]byte{{1, 2, 3}}, budget)
	if err != nil || answer != `{"name":"Fusion"}` {
		t.Fatalf("response answer=%q err=%v", answer, err)
	}
	if _, err := generator.analyze(context.Background(), "merge", nil, budget); err == nil || calls.Load() != 1 {
		t.Fatalf("expected one AI request, calls=%d err=%v", calls.Load(), err)
	}
}

func TestCrossCraftDoesNotFollowAIRequestRedirect(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path == "/" {
			http.Redirect(w, r, "/again", http.StatusTemporaryRedirect)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	generator := &CrossCraftGenerator{endpoint: server.URL, key: "test", model: "test", client: server.Client()}
	if _, err := generator.analyze(context.Background(), "test", nil, &crossCraftAIRequestBudget{}); err == nil {
		t.Fatal("AI redirect unexpectedly succeeded")
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("redirect sent %d outbound AI requests, want 1", got)
	}
}

func TestCrossCraftLayerRecolorClonesSharedAsset(t *testing.T) {
	first := []byte(`{"v":"5.5.7","fr":30,"ip":0,"op":30,"w":512,"h":512,"assets":[{"id":"shared","w":512,"h":512,"fr":30,"layers":[{"ty":4,"shapes":[{"ty":"fl","c":{"a":0,"k":[1,0,0,1]}},{"ty":"gf","g":{"p":2,"k":{"a":0,"k":[0,1,0,0,1,0,0,1,0,0.2,1,1,1,0.8]}}}]}]}],"layers":[{"ty":0,"nm":"Head","refId":"shared"},{"ty":0,"nm":"Body","refId":"shared"}]}`)
	second := []byte(`{"v":"5.5.7","fr":30,"ip":0,"op":30,"w":512,"h":512,"layers":[{"ty":4,"nm":"Prop","shapes":[{"ty":"fl","c":{"a":0,"k":[0,0,1,1]}}]}]}`)
	layer := 0
	plan := crossCraftPlan{
		Parts:    []crossCraftPart{{Source: 0, Role: "subject", X: 256, Y: 256, Scale: .8}, {Source: 1, Role: "prop", X: 300, Y: 300, Scale: .3}},
		Recolors: []crossCraftRecolor{{Source: 0, Layer: &layer, Color: "#00FF00", Gradient: []string{"#FFFF00", "#00FFFF"}}},
	}
	merged, err := composeCrossCraftLottie([]domain.StarGiftCraftSource{{ModelName: "Subject", AnimationJSON: first}, {ModelName: "Prop", AnimationJSON: second}}, plan)
	if err != nil {
		t.Fatal(err)
	}
	var output struct {
		Assets []json.RawMessage `json:"assets"`
	}
	if err := json.Unmarshal(merged, &output); err != nil {
		t.Fatal(err)
	}
	assets := make(map[string]json.RawMessage)
	for _, raw := range output.Assets {
		var asset struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(raw, &asset); err != nil {
			t.Fatal(err)
		}
		assets[asset.ID] = raw
	}
	var root struct {
		Layers []struct {
			Ref string `json:"refId"`
		} `json:"layers"`
	}
	if err := json.Unmarshal(assets["craft0_root"], &root); err != nil {
		t.Fatal(err)
	}
	if len(root.Layers) != 2 || root.Layers[0].Ref != "craft0_layer0_shared" || root.Layers[1].Ref != "craft0_shared" {
		t.Fatalf("unexpected layer references: %+v", root.Layers)
	}
	readColors := func(raw json.RawMessage) ([]float64, []float64) {
		t.Helper()
		var asset struct {
			Layers []struct {
				Shapes []struct {
					C struct {
						K []float64 `json:"k"`
					} `json:"c"`
					G struct {
						K struct {
							K []float64 `json:"k"`
						} `json:"k"`
					} `json:"g"`
				} `json:"shapes"`
			} `json:"layers"`
		}
		if err := json.Unmarshal(raw, &asset); err != nil {
			t.Fatal(err)
		}
		return asset.Layers[0].Shapes[0].C.K, asset.Layers[0].Shapes[1].G.K.K
	}
	baseFill, baseGradient := readColors(assets["craft0_shared"])
	newFill, newGradient := readColors(assets["craft0_layer0_shared"])
	if baseFill[0] != 1 || baseFill[1] != 0 || baseGradient[1] != 1 || baseGradient[6] != 0 {
		t.Fatalf("shared asset changed: fill=%v gradient=%v", baseFill, baseGradient)
	}
	if newFill[0] != 0 || newFill[1] <= 0 || newGradient[1] != 1 || newGradient[2] != 1 || newGradient[3] != 0 ||
		newGradient[5] != 0 || newGradient[6] != 1 || newGradient[7] != 1 {
		t.Fatalf("selected layer was not recolored: fill=%v gradient=%v", newFill, newGradient)
	}
	for _, index := range []int{0, 4, 8, 9, 10, 11, 12, 13} {
		if newGradient[index] != baseGradient[index] {
			t.Fatalf("gradient stop position/alpha changed at %d: %v -> %v", index, baseGradient[index], newGradient[index])
		}
	}
}

func TestCrossCraftAnimatedGradientRecolor(t *testing.T) {
	raw := json.RawMessage(`{"ty":"gf","g":{"p":2,"k":{"a":1,"k":[{"t":0,"s":[0,1,0,0,1,0,0,1],"e":[0,0,1,0,1,1,0,0]},{"t":10,"s":[0,0,0,1,1,1,1,1]}]}}}`)
	first, _ := craftParseRGB("#000000")
	last, _ := craftParseRGB("#FFFFFF")
	updated := recolorCraftJSONGradients(raw, [2][3]float64{first, last})
	var result struct {
		G struct {
			K struct {
				K []struct {
					S []float64 `json:"s"`
					E []float64 `json:"e"`
				} `json:"k"`
			} `json:"k"`
		} `json:"g"`
	}
	if err := json.Unmarshal(updated, &result); err != nil {
		t.Fatal(err)
	}
	for _, values := range [][]float64{result.G.K.K[0].S, result.G.K.K[0].E, result.G.K.K[1].S} {
		if values[0] != 0 || values[1] != 0 || values[2] != 0 || values[3] != 0 ||
			values[4] != 1 || values[5] != 1 || values[6] != 1 || values[7] != 1 {
			t.Fatalf("animated gradient stops were not recolored: %v", values)
		}
	}
}

func TestCrossCraftSelectedLayersOnlyContributeChosenDetails(t *testing.T) {
	first := []byte(`{"v":"5.5.7","fr":30,"ip":0,"op":30,"w":512,"h":512,"layers":[{"ty":4,"nm":"Old head"},{"ty":4,"nm":"Body"}]}`)
	second := []byte(`{"v":"5.5.7","fr":30,"ip":0,"op":30,"w":512,"h":512,"layers":[{"ty":4,"nm":"Hat"},{"ty":4,"nm":"Other body"}]}`)
	plan := crossCraftPlan{Parts: []crossCraftPart{
		{Source: 0, Role: "subject", X: 256, Y: 256, Scale: .8, Layers: []int{1}},
		{Source: 1, Role: "prop", X: 256, Y: 135, Scale: .4, Layers: []int{0}},
	}}
	merged, err := composeCrossCraftLottie([]domain.StarGiftCraftSource{{AnimationJSON: first}, {AnimationJSON: second}}, plan)
	if err != nil {
		t.Fatal(err)
	}
	var output struct {
		Assets []json.RawMessage `json:"assets"`
	}
	if err := json.Unmarshal(merged, &output); err != nil {
		t.Fatal(err)
	}
	for _, raw := range output.Assets {
		var asset struct {
			ID     string `json:"id"`
			Layers []struct {
				Hidden bool `json:"hd"`
			} `json:"layers"`
		}
		if err := json.Unmarshal(raw, &asset); err != nil {
			t.Fatal(err)
		}
		if asset.ID == "craft0_root" && (len(asset.Layers) != 2 || !asset.Layers[0].Hidden || asset.Layers[1].Hidden) {
			t.Fatalf("subject layer selection failed: %+v", asset.Layers)
		}
		if asset.ID == "craft1_root" && (len(asset.Layers) != 2 || asset.Layers[0].Hidden || !asset.Layers[1].Hidden) {
			t.Fatalf("prop layer selection failed: %+v", asset.Layers)
		}
	}
}

func TestCrossCraftSingleGiftCanProduceAIVariant(t *testing.T) {
	animation := []byte(`{"v":"5.5.7","fr":30,"ip":0,"op":30,"w":512,"h":512,"layers":[{"ty":4,"nm":"Face","shapes":[{"ty":"fl","c":{"a":0,"k":[1,0,0,1]}}]}]}`)
	plan := crossCraftPlan{
		Parts:    []crossCraftPart{{Source: 0, Role: "subject", X: 256, Y: 256, Scale: .9}},
		Recolors: []crossCraftRecolor{{Source: 0, Color: "#00FF00"}},
	}
	merged, err := composeCrossCraftLottie([]domain.StarGiftCraftSource{{AnimationJSON: animation}}, plan)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parseCrossCraftPlan(`{"parts":[{"source":0,"role":"subject","x":256,"y":256,"scale":0.9}],"recolors":[{"source":0,"color":"#00FF00"}],"story":"The face becomes a fresh green variant.","name":"Green Face"}`, 1); err != nil {
		t.Fatal(err)
	}
	var result struct {
		Assets []json.RawMessage `json:"assets"`
	}
	if err := json.Unmarshal(merged, &result); err != nil || len(result.Assets) != 1 {
		t.Fatalf("single gift composition failed: assets=%d err=%v", len(result.Assets), err)
	}
}

func TestCrossCraftPlanAcceptsTargetedRecolors(t *testing.T) {
	planJSON := `{"parts":[{"source":0,"role":"subject","x":256,"y":256,"scale":0.8,"layers":[1]},{"source":1,"role":"prop","x":300,"y":300,"scale":0.3}],"recolors":[{"source":0,"layer":0,"color":"#00FF00"},{"source":0,"layer":1,"gradient":["#000000","#FFFFFF"]}],"story":"A new character wears the selected detail.","name":"Layered Gift"}`
	plan, err := parseCrossCraftPlan(planJSON, 2)
	if err != nil || len(plan.Recolors) != 2 || plan.Recolors[0].Layer == nil || *plan.Recolors[0].Layer != 0 {
		t.Fatalf("valid targeted recolors rejected: plan=%+v err=%v", plan, err)
	}
	duplicate := strings.Replace(planJSON, `"layer":1,"gradient"`, `"layer":0,"gradient"`, 1)
	if _, err := parseCrossCraftPlan(duplicate, 2); err == nil {
		t.Fatal("duplicate source/layer recolor accepted")
	}
	invalidGradient := strings.Replace(planJSON, `"#FFFFFF"`, `"white"`, 1)
	if _, err := parseCrossCraftPlan(invalidGradient, 2); err == nil {
		t.Fatal("invalid gradient color accepted")
	}
}

// Set fixture paths to exercise the native renderer with real, read-only
// model snapshots. Ordinary unit tests skip this probe.
func TestCrossCraftRenderSourceSnapshots(t *testing.T) {
	a, b := os.Getenv("TELESRV_CROSS_CRAFT_SOURCE_A"), os.Getenv("TELESRV_CROSS_CRAFT_SOURCE_B")
	if a == "" || b == "" {
		t.Skip("source snapshots not supplied")
	}
	first, err := os.ReadFile(a)
	if err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(b)
	if err != nil {
		t.Fatal(err)
	}
	sources := []domain.StarGiftCraftSource{
		{ModelName: "First", AnimationJSON: first, Backdrop: domain.StarGiftCollectibleAttribute{CenterColor: 0x7755aa, EdgeColor: 0x554477}},
		{ModelName: "Second", AnimationJSON: second, Backdrop: domain.StarGiftCollectibleAttribute{CenterColor: 0xaabbcc, EdgeColor: 0x7799aa}},
	}
	for i, source := range sources {
		for _, position := range []float64{0, .25, .5, .75, 1} {
			frame, err := lottierender.Render(source.AnimationJSON, 256, 256, position)
			if err != nil {
				t.Fatalf("source %d render: %v", i, err)
			}
			visible := 0
			nonzero := 0
			for j := 3; j < len(frame.Pix); j += 4 {
				if frame.Pix[j] > 16 {
					visible++
				}
				if frame.Pix[j-3] != 0 || frame.Pix[j-2] != 0 || frame.Pix[j-1] != 0 || frame.Pix[j] != 0 {
					nonzero++
				}
			}
			t.Logf("source %d frame %.2f visible=%d nonzero=%d first=%v", i, position, visible, nonzero, frame.Pix[:16])
		}
	}
	plan := crossCraftPlan{Parts: []crossCraftPart{{Source: 0, Role: "subject", X: 256, Y: 256, Scale: .8}, {Source: 1, Role: "prop", X: 300, Y: 320, Scale: .4}}, Story: "The subject carries the smaller prop.", Name: "Fusion"}
	merged, err := composeCrossCraftLottie(sources, plan)
	if err != nil {
		t.Fatal(err)
	}
	gradient := domain.BlendStarGiftCraftBackdrops(sources)
	for _, position := range []float64{.25, .75} {
		if _, err := crossCraftPreview(merged, gradient.CenterColor, gradient.EdgeColor, position); err != nil {
			t.Fatalf("merged render: %v", err)
		}
	}
	animation, err := prepareAnimation("merged.json", merged)
	if err != nil {
		t.Fatalf("TGS validation: %v", err)
	}
	t.Logf("merged model JSON=%d bytes TGS=%d bytes", len(merged), len(animation.TGS))
}

func TestCrossCraftFrogHoldsBag(t *testing.T) {
	frogPath, bagPath := os.Getenv("TELESRV_CROSS_CRAFT_FROG"), os.Getenv("TELESRV_CROSS_CRAFT_BAG")
	if frogPath == "" || bagPath == "" {
		t.Skip("frog and bag fixtures not supplied")
	}
	frog, err := os.ReadFile(frogPath)
	if err != nil {
		t.Fatal(err)
	}
	bag, err := os.ReadFile(bagPath)
	if err != nil {
		t.Fatal(err)
	}
	sources := []domain.StarGiftCraftSource{{ModelName: "Louis Vuittoad", AnimationJSON: frog}, {ModelName: "Verso Blue", AnimationJSON: bag}}
	plan := crossCraftPlan{Parts: []crossCraftPart{{Source: 0, Role: "subject", X: 256, Y: 256, Scale: .95}, {Source: 1, Role: "prop", X: 285, Y: 305, Scale: .42}}, Foreground: []int{2, 4, 27}, Story: "The frog holds the handbag by its handle.", Name: "Toad Carrier"}
	merged, err := composeCrossCraftLottie(sources, plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, position := range []float64{.25, .5, .75} {
		preview, err := crossCraftPreview(merged, 0xf2b85f, 0x8c83bb, position)
		if err != nil {
			t.Fatal(err)
		}
		if output := os.Getenv("TELESRV_CROSS_CRAFT_PREVIEW"); output != "" && position == .5 {
			if err := os.WriteFile(output, preview, 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := prepareAnimation("frog-holds-bag.json", merged); err != nil {
		t.Fatal(err)
	}
}
