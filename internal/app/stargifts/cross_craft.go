package stargifts

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"io"
	"math"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"telesrv/internal/app/ai"
	"telesrv/internal/domain"
	"telesrv/internal/lottierender"
)

// CrossCraftGenerator uses one vision request to design a composition from
// rendered source frames, then validates the result locally.
const (
	crossCraftMaxAIRequests    = 1
	crossCraftAttemptTTL       = 10 * time.Minute
	crossCraftFailedAttemptTTL = 30 * time.Second
	crossCraftAttemptLimit     = 128
)

type crossCraftAttempt struct {
	done       chan struct{}
	finishedAt time.Time
	model      domain.StarGiftCollectibleAttribute
	err        error
}

type crossCraftAIRequestBudget struct{ used int }

type CrossCraftGenerator struct {
	endpoint  string
	key       string
	model     string
	client    *http.Client
	aiGate    chan struct{}
	blobs     BlobBackend
	dc        int
	attemptMu sync.Mutex
	attempts  map[string]*crossCraftAttempt
}

func NewCrossCraftGenerator(cfg ai.ProviderConfig, blobs BlobBackend, dc int) *CrossCraftGenerator {
	if cfg.Kind != ai.ProviderKindOpenAIChat || cfg.APIKey == "" || cfg.Model == "" || blobs == nil || dc <= 0 {
		return nil
	}
	base := strings.TrimRight(cfg.BaseURL, "/")
	if base == "" {
		base = "https://api.openai.com/v1"
	}
	base = strings.TrimSuffix(strings.TrimSuffix(base, "/chat/completions"), "/responses")
	if strings.HasPrefix(cfg.Model, "cx/") {
		base += "/responses"
	} else {
		base += "/chat/completions"
	}
	timeout := cfg.Timeout
	if timeout < 5*time.Minute {
		// Vision requests can spend more than two minutes generating before headers arrive.
		timeout = 5 * time.Minute
	}
	return &CrossCraftGenerator{endpoint: base, key: cfg.APIKey, model: cfg.Model, client: &http.Client{Timeout: timeout}, aiGate: make(chan struct{}, 1), blobs: blobs, dc: dc}
}

type crossCraftPart struct {
	Source   int     `json:"source"`
	Role     string  `json:"role"`
	X        float64 `json:"x"`
	Y        float64 `json:"y"`
	Scale    float64 `json:"scale"`
	Rotation float64 `json:"rotation"`
	Layers   []int   `json:"layers,omitempty"`
}

type crossCraftRecolor struct {
	Source   int      `json:"source"`
	Layer    *int     `json:"layer,omitempty"`
	Color    string   `json:"color,omitempty"`
	Gradient []string `json:"gradient,omitempty"`
}

type craftRecolorRule struct {
	Solid       [3]float64
	HasSolid    bool
	Gradient    [2][3]float64
	HasGradient bool
}

type crossCraftPlan struct {
	Parts      []crossCraftPart    `json:"parts"`
	Recolors   []crossCraftRecolor `json:"recolors"`
	Foreground []int               `json:"foreground_layers"`
	Hidden     []int               `json:"hidden_layers"`
	Story      string              `json:"story"`
	Name       string              `json:"name"`
}

func (g *CrossCraftGenerator) GenerateCrossCraft(ctx context.Context, sources []domain.StarGiftCraftSource) (domain.StarGiftCollectibleAttribute, error) {
	if g == nil || len(sources) < 1 || len(sources) > 4 {
		return domain.StarGiftCollectibleAttribute{}, domain.ErrStarGiftCraftUnavailable
	}
	key := crossCraftAttemptKey(sources)
	g.attemptMu.Lock()
	if g.attempts == nil {
		g.attempts = make(map[string]*crossCraftAttempt)
	}
	now := time.Now()
	for existingKey, attempt := range g.attempts {
		ttl := crossCraftAttemptTTL
		if attempt.err != nil {
			ttl = crossCraftFailedAttemptTTL
		}
		if !attempt.finishedAt.IsZero() && now.Sub(attempt.finishedAt) >= ttl {
			delete(g.attempts, existingKey)
		}
	}
	if attempt := g.attempts[key]; attempt != nil {
		g.attemptMu.Unlock()
		select {
		case <-attempt.done:
			return attempt.model, attempt.err
		case <-ctx.Done():
			return domain.StarGiftCollectibleAttribute{}, ctx.Err()
		}
	}
	if len(g.attempts) >= crossCraftAttemptLimit {
		g.attemptMu.Unlock()
		return domain.StarGiftCollectibleAttribute{}, domain.ErrStarGiftCraftUnavailable
	}
	attempt := &crossCraftAttempt{done: make(chan struct{})}
	g.attempts[key] = attempt
	g.attemptMu.Unlock()

	// MTProto clients may stop waiting before a vision request finishes. Keep
	// the one generation alive so the next craft call can reuse its result.
	// The local AI gateway admits one heavy vision request at a time. Allow
	// queued crafts to wait through several preceding generations.
	workTimeout := 10 * time.Minute
	if g.client != nil && 5*g.client.Timeout > workTimeout {
		workTimeout = 5 * g.client.Timeout
	}
	workCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), workTimeout)
	go func() {
		defer cancel()
		model, err := g.generateCrossCraft(workCtx, sources)
		g.attemptMu.Lock()
		attempt.model, attempt.err, attempt.finishedAt = model, err, time.Now()
		close(attempt.done)
		g.attemptMu.Unlock()
	}()
	select {
	case <-attempt.done:
		return attempt.model, attempt.err
	case <-ctx.Done():
		return domain.StarGiftCollectibleAttribute{}, ctx.Err()
	}
}

func crossCraftAttemptKey(sources []domain.StarGiftCraftSource) string {
	keys := make([]string, 0, len(sources))
	for _, source := range sources {
		hash := sha256.New()
		fmt.Fprintf(hash, "%d:%d:%d:%q:%d:%d:%d:", source.UniqueID, source.GiftID, source.ModelID, source.ModelName,
			source.Backdrop.CenterColor, source.Backdrop.EdgeColor, len(source.AnimationJSON))
		_, _ = hash.Write(source.AnimationJSON)
		keys = append(keys, fmt.Sprintf("%x", hash.Sum(nil)))
	}
	sort.Strings(keys)
	return strings.Join(keys, ":")
}

func (g *CrossCraftGenerator) generateCrossCraft(ctx context.Context, sources []domain.StarGiftCraftSource) (domain.StarGiftCollectibleAttribute, error) {
	images := make([][]byte, 0, 2*len(sources))
	names := make([]string, 0, len(sources))
	for _, source := range sources {
		names = append(names, source.ModelName)
		for _, position := range []float64{0.25, 0.75} {
			frame, err := crossCraftPreview(source.AnimationJSON, source.Backdrop.CenterColor, source.Backdrop.EdgeColor, position)
			if err != nil {
				return domain.StarGiftCollectibleAttribute{}, fmt.Errorf("render craft source: %w", err)
			}
			images = append(images, frame)
		}
	}
	parts, err := craftLayerCatalog(sources)
	if err != nil {
		return domain.StarGiftCollectibleAttribute{}, err
	}
	sameCollection := len(sources) > 1 && sources[0].GiftID > 0
	for _, source := range sources[1:] {
		if source.GiftID != sources[0].GiftID {
			sameCollection = false
			break
		}
	}
	collectionGuidance := ""
	if sameCollection {
		collectionGuidance = "These gifts belong to ONE collection. You may keep one gift as the full subject and take only a few meaningful top-level layers from the others, recoloring individual layers or gradients as needed. A source part with layers:[indices] contributes ONLY those layers. Prefer a new unified character or details over stacking complete gifts."
	} else if len(sources) == 1 {
		collectionGuidance = "Only one gift is supplied. Design a meaningful new variant by changing selected layers or gradients while keeping its character recognizable; use one subject part and no props."
	}
	prompt := fmt.Sprintf(`Act as an animation art director. Invent ONE coherent new character or scene from all %d Telegram gift models. The attached images show two frames per model in source order, named %q. Use the actual visual meaning of these models, not a generic overlay. These are examples of DIFFERENT transformations, not templates to copy:
- Clown statue + smiley face: the smiley REPLACES the statue's head. Hide ONLY isolated original head/face layers, position the smiley at the neck, and keep the statue's body visible.
- Frog + handbag: the frog HOLDS the smaller bag by its handle; redraw a paw in front of the handle.
- Cat + crown: the cat WEARS the crown, positioned above its ears.
- Robot + flower: the robot OFFERS the flower from its hand, with the hand in front of the stem.
- Dragon + rider: the rider SITS on the dragon's back, scaled to fit rather than covering the dragon.
- Ghost + lantern: the ghost CARRIES the lantern below its arm, with a visible contact point.
- Turtle + house: the house BECOMES a shell on the turtle's back, at shell scale.
- Bird + telescope: the bird LOOKS through the telescope, with the eyepiece at its eye.
- Astronaut + planet: the astronaut STANDS on the planet, feet touching its surface.
You may recolor whole source objects or individual top-level layers when a shared palette strengthens the new idea. Use a layer index for a particular head, paw, garment, or prop without changing the rest of its source. You may also recolor gradient fills and strokes using two endpoint colors, retaining their stop positions and opacity. Preserve shapes and animation. Do not recolor merely to hide an awkward overlay.
Choose a fresh interaction that fits THESE supplied images. The outcome must read as one idea at both animation frames. Never leave two centered full-size gifts superimposed.
Return ONLY JSON: {"parts":[{"source":0,"role":"subject","x":256,"y":256,"scale":0.9,"rotation":0}, ...],"recolors":[{"source":1,"layer":3,"color":"#70B84A"},{"source":0,"gradient":["#FFCC00","#8934DC"]}],"foreground_layers":[],"hidden_layers":[],"story":"one sentence describing the interaction","name":"short new model name"}. Use recolors [] when no palette change helps. A recolor has source and optional layer (a 0-based top-level index); omit layer to target the whole source. Supply color for solid fills/strokes, gradient for gradient fills/strokes, or both. Gradient has exactly two #RRGGBB endpoint colors; color is #RRGGBB. Use at most 12 recolors and do not repeat a source/layer pair. Include every source exactly once; exactly one role subject, all others role prop. Coordinates are centers in a 512x512 canvas. Subject scale 0.65..1, props scale 0.15..0.6, rotations -35..35 degrees. Each part may optionally include layers:[0-based top-level layer indices] to contribute only those layers of that source; omit layers to keep the full source. Select at least one layer per source, and choose indices from its own catalog. Position each prop at the meaningful contact point. foreground_layers are 0-based indices of the SUBJECT's hand/paw/limb layers redrawn OVER the prop; use [] when none fit. hidden_layers are 0-based indices of isolated SUBJECT layers that the prop REPLACES, such as an original head; use [] for holding, wearing, or whenever a layer contains body parts that must remain. Never hide the whole subject or a generic root layer. Decide from these top-level layer names by index: %s. %s`, len(sources), names, parts, collectionGuidance)
	budget := &crossCraftAIRequestBudget{}
	answer, err := g.analyze(ctx, prompt, images, budget)
	if err != nil {
		return domain.StarGiftCollectibleAttribute{}, fmt.Errorf("plan cross craft: %w", err)
	}
	plan, err := parseCrossCraftPlan(answer, len(sources))
	if err != nil {
		return domain.StarGiftCollectibleAttribute{}, err
	}
	final, err := composeCrossCraftLottie(sources, plan)
	if err != nil {
		return domain.StarGiftCollectibleAttribute{}, err
	}
	gradient := domain.BlendStarGiftCraftBackdrops(sources)
	for _, position := range []float64{0.25, 0.75} {
		if _, err := crossCraftPreview(final, gradient.CenterColor, gradient.EdgeColor, position); err != nil {
			return domain.StarGiftCollectibleAttribute{}, fmt.Errorf("render final craft: %w", err)
		}
	}
	animation, err := prepareAnimation("crafted-mix.json", final)
	if err != nil {
		return domain.StarGiftCollectibleAttribute{}, fmt.Errorf("validate crafted TGS: %w", err)
	}
	items := []domain.StarGiftCollectibleAttribute{{Kind: domain.StarGiftCollectibleModel, Name: plan.Name, RarityKind: domain.StarGiftRarityLegendary, Crafted: true, Animation: &animation}}
	service := &Service{blobs: g.blobs, dc: g.dc}
	if err := service.materializeCollectibleAttributes(ctx, items); err != nil {
		return domain.StarGiftCollectibleAttribute{}, err
	}
	return items[0], nil
}

func (g *CrossCraftGenerator) analyze(ctx context.Context, prompt string, images [][]byte, budget *crossCraftAIRequestBudget) (string, error) {
	responsesAPI := strings.HasPrefix(g.model, "cx/")
	textType, imageType := "text", "image_url"
	if responsesAPI {
		textType, imageType = "input_text", "input_image"
	}
	content := []map[string]any{{"type": textType, "text": prompt}}
	for _, data := range images {
		if len(data) == 0 || len(data) > 1<<20 {
			return "", domain.ErrStarGiftFileInvalid
		}
		imageURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(data)
		if responsesAPI {
			content = append(content, map[string]any{"type": imageType, "image_url": imageURL})
		} else {
			content = append(content, map[string]any{"type": imageType, "image_url": map[string]string{"url": imageURL}})
		}
	}
	request := map[string]any{"model": g.model}
	message := []map[string]any{{"role": "user", "content": content}}
	if responsesAPI {
		request["input"] = message
	} else {
		request["messages"] = message
	}
	body, err := json.Marshal(request)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.endpoint, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("authorization", "Bearer "+g.key)
	req.Header.Set("ngrok-skip-browser-warning", "true")
	if budget == nil || budget.used >= crossCraftMaxAIRequests {
		return "", fmt.Errorf("cross craft AI request limit reached")
	}
	if g.aiGate != nil {
		select {
		case g.aiGate <- struct{}{}:
			defer func() { <-g.aiGate }()
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	budget.used++
	client := *g.client
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("craft AI status %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return "", err
	}
	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			CompletionTokens int `json:"completion_tokens"`
			ReasoningTokens  int `json:"reasoning_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return "", err
	}
	if len(result.Choices) == 0 || result.Choices[0].Message.Content == "" {
		if responsesAPI {
			var response struct {
				Status     string `json:"status"`
				OutputText string `json:"output_text"`
				Output     []struct {
					Content []struct {
						Type string `json:"type"`
						Text string `json:"text"`
					} `json:"content"`
				} `json:"output"`
			}
			if err := json.Unmarshal(raw, &response); err != nil {
				return "", err
			}
			if response.Status != "" && response.Status != "completed" {
				return "", fmt.Errorf("craft AI response status %s", response.Status)
			}
			if text := strings.TrimSpace(response.OutputText); text != "" {
				return text, nil
			}
			var parts []string
			for _, item := range response.Output {
				for _, part := range item.Content {
					if part.Type == "output_text" && strings.TrimSpace(part.Text) != "" {
						parts = append(parts, part.Text)
					}
				}
			}
			if len(parts) > 0 {
				return strings.Join(parts, "\n"), nil
			}
		}
		finishReason := "missing_choice"
		if len(result.Choices) > 0 {
			finishReason = result.Choices[0].FinishReason
		}
		return "", fmt.Errorf("craft AI returned no plan (finish_reason=%s, completion_tokens=%d, reasoning_tokens=%d)",
			finishReason, result.Usage.CompletionTokens, result.Usage.ReasoningTokens)
	}
	return result.Choices[0].Message.Content, nil
}

func parseCrossCraftPlan(text string, count int) (crossCraftPlan, error) {
	start, end := strings.IndexByte(text, '{'), strings.LastIndexByte(text, '}')
	if start < 0 || end < start {
		return crossCraftPlan{}, fmt.Errorf("craft AI plan is not JSON")
	}
	var plan crossCraftPlan
	if err := json.Unmarshal([]byte(text[start:end+1]), &plan); err != nil {
		return crossCraftPlan{}, err
	}
	if len(plan.Parts) != count {
		return crossCraftPlan{}, fmt.Errorf("craft AI plan source count is invalid")
	}
	seen := make(map[int]bool, count)
	subjects := 0
	for _, part := range plan.Parts {
		if part.Source < 0 || part.Source >= count || seen[part.Source] || math.IsNaN(part.X) || math.IsNaN(part.Y) ||
			math.IsNaN(part.Scale) || math.IsNaN(part.Rotation) || part.X < 55 || part.X > 457 || part.Y < 55 || part.Y > 457 ||
			part.Rotation < -35 || part.Rotation > 35 {
			return crossCraftPlan{}, fmt.Errorf("craft AI scene geometry is invalid")
		}
		seen[part.Source] = true
		if part.Layers != nil {
			if len(part.Layers) == 0 || len(part.Layers) > 30 {
				return crossCraftPlan{}, fmt.Errorf("craft AI selected layer count is invalid")
			}
			selectedLayers := make(map[int]bool, len(part.Layers))
			for _, index := range part.Layers {
				if index < 0 || index >= 100 || selectedLayers[index] {
					return crossCraftPlan{}, fmt.Errorf("craft AI selected layer is invalid")
				}
				selectedLayers[index] = true
			}
		}
		switch part.Role {
		case "subject":
			subjects++
			if part.Scale < .65 || part.Scale > 1 {
				return crossCraftPlan{}, fmt.Errorf("craft AI subject scale is invalid")
			}
		case "prop":
			if part.Scale < .15 || part.Scale > .6 {
				return crossCraftPlan{}, fmt.Errorf("craft AI prop scale is invalid")
			}
		default:
			return crossCraftPlan{}, fmt.Errorf("craft AI role is invalid")
		}
	}
	if subjects != 1 || len(plan.Foreground) > 12 || len(plan.Hidden) > 12 {
		return crossCraftPlan{}, fmt.Errorf("craft AI scene requires one subject")
	}
	if len(plan.Recolors) > 12 {
		return crossCraftPlan{}, fmt.Errorf("craft AI recolor count is invalid")
	}
	recolored := make(map[string]bool, len(plan.Recolors))
	for _, recolor := range plan.Recolors {
		key := fmt.Sprintf("%d:all", recolor.Source)
		if recolor.Layer != nil {
			key = fmt.Sprintf("%d:%d", recolor.Source, *recolor.Layer)
		}
		if recolor.Source < 0 || recolor.Source >= count ||
			(recolor.Layer != nil && (*recolor.Layer < 0 || *recolor.Layer >= 100)) || recolored[key] ||
			(recolor.Color == "" && len(recolor.Gradient) == 0) ||
			(recolor.Color != "" && !craftHexColor.MatchString(recolor.Color)) ||
			(len(recolor.Gradient) != 0 && len(recolor.Gradient) != 2) {
			return crossCraftPlan{}, fmt.Errorf("craft AI recolor is invalid")
		}
		for _, color := range recolor.Gradient {
			if !craftHexColor.MatchString(color) {
				return crossCraftPlan{}, fmt.Errorf("craft AI gradient is invalid")
			}
		}
		recolored[key] = true
	}
	selected := make(map[int]bool, len(plan.Foreground)+len(plan.Hidden))
	for _, index := range plan.Foreground {
		if index < 0 || index > 250 || selected[index] {
			return crossCraftPlan{}, fmt.Errorf("craft AI foreground layer is invalid")
		}
		selected[index] = true
	}
	for _, index := range plan.Hidden {
		if index < 0 || index > 250 || selected[index] {
			return crossCraftPlan{}, fmt.Errorf("craft AI hidden layer is invalid")
		}
		selected[index] = true
	}
	plan.Story = strings.TrimSpace(plan.Story)
	plan.Name = strings.TrimSpace(plan.Name)
	if plan.Name == "" || len([]rune(plan.Name)) > 48 || len([]rune(plan.Story)) < 12 || len([]rune(plan.Story)) > 240 {
		return crossCraftPlan{}, fmt.Errorf("craft AI model name is invalid")
	}
	return plan, nil
}

func craftLayerCatalog(sources []domain.StarGiftCraftSource) (string, error) {
	var out strings.Builder
	for sourceIndex, source := range sources {
		var root struct {
			Layers []struct {
				Name string `json:"nm"`
			} `json:"layers"`
		}
		if err := json.Unmarshal(source.AnimationJSON, &root); err != nil || len(root.Layers) == 0 {
			return "", domain.ErrStarGiftFileInvalid
		}
		fmt.Fprintf(&out, "source %d: ", sourceIndex)
		for layerIndex, layer := range root.Layers {
			if layerIndex >= 100 {
				break
			}
			name := strings.TrimSpace(layer.Name)
			if name == "" {
				continue
			}
			fmt.Fprintf(&out, "%d=%q, ", layerIndex, name)
		}
	}
	return out.String(), nil
}

func composeCrossCraftLottie(sources []domain.StarGiftCraftSource, plan crossCraftPlan) ([]byte, error) {
	if len(sources) < 1 || len(sources) != len(plan.Parts) {
		return nil, domain.ErrStarGiftCraftUnavailable
	}
	subjectIndex := -1
	for _, part := range plan.Parts {
		if part.Role == "subject" {
			subjectIndex = part.Source
		}
	}
	if subjectIndex < 0 {
		return nil, domain.ErrStarGiftFileInvalid
	}
	partsBySource := make(map[int]crossCraftPart, len(plan.Parts))
	for _, part := range plan.Parts {
		if part.Source < 0 || part.Source >= len(sources) {
			return nil, domain.ErrStarGiftFileInvalid
		}
		if _, exists := partsBySource[part.Source]; exists {
			return nil, domain.ErrStarGiftFileInvalid
		}
		partsBySource[part.Source] = part
	}
	type sourceRoot struct {
		TGS    json.RawMessage   `json:"tgs"`
		V      string            `json:"v"`
		FR     float64           `json:"fr"`
		IP     float64           `json:"ip"`
		OP     float64           `json:"op"`
		NM     string            `json:"nm"`
		DDD    json.RawMessage   `json:"ddd"`
		Assets []json.RawMessage `json:"assets"`
		Layers []json.RawMessage `json:"layers"`
	}
	roots := make([]sourceRoot, 0, len(sources))
	for _, source := range sources {
		var root sourceRoot
		if err := json.Unmarshal(source.AnimationJSON, &root); err != nil || root.FR <= 0 || root.OP <= root.IP || len(root.Layers) == 0 {
			return nil, domain.ErrStarGiftFileInvalid
		}
		roots = append(roots, root)
	}
	base := roots[0]
	globalRecolors := make(map[int]craftRecolorRule, len(plan.Recolors))
	layerRecolors := make(map[int]map[int]craftRecolorRule)
	for _, recolor := range plan.Recolors {
		if recolor.Source < 0 || recolor.Source >= len(roots) {
			return nil, domain.ErrStarGiftFileInvalid
		}
		var rule craftRecolorRule
		if recolor.Color != "" {
			color, err := craftParseRGB(recolor.Color)
			if err != nil {
				return nil, err
			}
			rule.Solid, rule.HasSolid = color, true
		}
		if len(recolor.Gradient) != 0 {
			if len(recolor.Gradient) != 2 {
				return nil, domain.ErrStarGiftFileInvalid
			}
			for stop, value := range recolor.Gradient {
				color, err := craftParseRGB(value)
				if err != nil {
					return nil, err
				}
				rule.Gradient[stop] = color
			}
			rule.HasGradient = true
		}
		if recolor.Layer == nil {
			globalRecolors[recolor.Source] = rule
		} else {
			if *recolor.Layer < 0 || *recolor.Layer >= len(roots[recolor.Source].Layers) {
				return nil, domain.ErrStarGiftFileInvalid
			}
			if layerRecolors[recolor.Source] == nil {
				layerRecolors[recolor.Source] = make(map[int]craftRecolorRule)
			}
			layerRecolors[recolor.Source][*recolor.Layer] = rule
		}
	}
	assets := make([]json.RawMessage, 0, len(sources)*2)
	for i, root := range roots {
		prefix := fmt.Sprintf("craft%d_", i)
		selectedLayers := make(map[int]bool)
		if part, ok := partsBySource[i]; ok && part.Layers != nil {
			if len(part.Layers) == 0 {
				return nil, domain.ErrStarGiftFileInvalid
			}
			for _, index := range part.Layers {
				if index < 0 || index >= len(root.Layers) || selectedLayers[index] {
					return nil, domain.ErrStarGiftFileInvalid
				}
				selectedLayers[index] = true
			}
		}
		originalAssets := make(map[string]json.RawMessage, len(root.Assets))
		for _, asset := range root.Assets {
			var meta struct {
				ID string `json:"id"`
			}
			if err := json.Unmarshal(asset, &meta); err != nil || meta.ID == "" {
				return nil, domain.ErrStarGiftFileInvalid
			}
			originalAssets[meta.ID] = asset
			assets = append(assets, prefixCraftJSONFields(applyCraftRecolor(asset, globalRecolors[i]), prefix, true))
		}
		hidden := make(map[int]bool, len(plan.Hidden))
		if i == subjectIndex {
			if len(plan.Hidden) >= len(root.Layers) {
				return nil, domain.ErrStarGiftFileInvalid
			}
			for _, index := range plan.Hidden {
				if index < 0 || index >= len(root.Layers) {
					return nil, domain.ErrStarGiftFileInvalid
				}
				hidden[index] = true
			}
		}
		layers := make([]json.RawMessage, 0, len(root.Layers))
		visibleLayers := 0
		for index, layer := range root.Layers {
			rule := globalRecolors[i]
			refPrefix := prefix
			if local, ok := layerRecolors[i][index]; ok {
				rule = mergeCraftRecolor(rule, local)
				clonePrefix := fmt.Sprintf("craft%d_layer%d_", i, index)
				seen := make(map[string]bool)
				for _, match := range craftReferencePattern.FindAllSubmatch(layer, -1) {
					if string(match[1]) != "refId" {
						continue
					}
					ref := string(match[2])
					if _, ok := originalAssets[ref]; ok {
						cloneCraftAsset(ref, originalAssets, clonePrefix, rule, seen, &assets)
						refPrefix = clonePrefix
					}
				}
			}
			layer = prefixCraftJSONFields(applyCraftRecolor(layer, rule), refPrefix, false)
			if hidden[index] || (len(selectedLayers) > 0 && !selectedLayers[index]) {
				layer = hideCraftLayer(layer)
			} else {
				visibleLayers++
			}
			layers = append(layers, layer)
		}
		if visibleLayers == 0 {
			return nil, domain.ErrStarGiftFileInvalid
		}
		asset, err := json.Marshal(struct {
			ID     string            `json:"id"`
			W      int               `json:"w"`
			H      int               `json:"h"`
			FR     float64           `json:"fr"`
			Layers []json.RawMessage `json:"layers"`
		}{prefix + "root", 512, 512, root.FR, layers})
		if err != nil {
			return nil, err
		}
		assets = append(assets, asset)
		if i == subjectIndex && len(plan.Foreground) > 0 {
			front := make(map[int]bool, len(plan.Foreground))
			for _, index := range plan.Foreground {
				if index < 0 || index >= len(root.Layers) {
					return nil, domain.ErrStarGiftFileInvalid
				}
				front[index] = true
			}
			frontLayers := make([]json.RawMessage, 0, len(root.Layers))
			for index, layer := range layers {
				if !front[index] {
					layer = hideCraftLayer(layer)
				}
				frontLayers = append(frontLayers, layer)
			}
			frontAsset, err := json.Marshal(struct {
				ID     string            `json:"id"`
				W      int               `json:"w"`
				H      int               `json:"h"`
				FR     float64           `json:"fr"`
				Layers []json.RawMessage `json:"layers"`
			}{prefix + "foreground", 512, 512, root.FR, frontLayers})
			if err != nil {
				return nil, err
			}
			assets = append(assets, frontAsset)
		}
	}
	layers := make([]json.RawMessage, 0, len(sources)+1)
	appendPart := func(part crossCraftPart, ref string) error {
		scale := part.Scale * 100
		layer, err := json.Marshal(struct {
			DDD int     `json:"ddd"`
			Ind int     `json:"ind"`
			Ty  int     `json:"ty"`
			Nm  string  `json:"nm"`
			Ref string  `json:"refId"`
			Sr  float64 `json:"sr"`
			Ks  struct {
				P any `json:"p"`
				A any `json:"a"`
				S any `json:"s"`
				R any `json:"r"`
				O any `json:"o"`
			} `json:"ks"`
			Ao int     `json:"ao"`
			IP float64 `json:"ip"`
			OP float64 `json:"op"`
			St int     `json:"st"`
			Bm int     `json:"bm"`
		}{DDD: 0, Ind: len(layers) + 1, Ty: 0, Nm: sources[part.Source].ModelName,
			Ref: ref, Sr: roots[part.Source].FR / base.FR,
			Ks: struct {
				P any `json:"p"`
				A any `json:"a"`
				S any `json:"s"`
				R any `json:"r"`
				O any `json:"o"`
			}{P: map[string]any{"a": 0, "k": []float64{part.X, part.Y, 0}}, A: map[string]any{"a": 0, "k": []float64{256, 256, 0}}, S: map[string]any{"a": 0, "k": []float64{scale, scale, 100}}, R: map[string]any{"a": 0, "k": part.Rotation}, O: map[string]any{"a": 0, "k": 100}},
			Ao: 0, IP: base.IP, OP: base.OP, St: 0, Bm: 0})
		if err != nil {
			return err
		}
		layers = append(layers, layer)
		return nil
	}
	subject := plan.Parts[0]
	for _, part := range plan.Parts {
		if part.Role == "subject" {
			subject = part
			break
		}
	}
	if len(plan.Foreground) > 0 {
		if err := appendPart(subject, fmt.Sprintf("craft%d_foreground", subjectIndex)); err != nil {
			return nil, err
		}
	}
	for _, part := range plan.Parts {
		if part.Role != "prop" {
			continue
		}
		if err := appendPart(part, fmt.Sprintf("craft%d_root", part.Source)); err != nil {
			return nil, err
		}
	}
	if err := appendPart(subject, fmt.Sprintf("craft%d_root", subjectIndex)); err != nil {
		return nil, err
	}
	return json.Marshal(struct {
		TGS    json.RawMessage   `json:"tgs,omitempty"`
		V      string            `json:"v"`
		FR     float64           `json:"fr"`
		IP     float64           `json:"ip"`
		OP     float64           `json:"op"`
		W      int               `json:"w"`
		H      int               `json:"h"`
		NM     string            `json:"nm,omitempty"`
		DDD    json.RawMessage   `json:"ddd,omitempty"`
		Assets []json.RawMessage `json:"assets"`
		Layers []json.RawMessage `json:"layers"`
	}{base.TGS, base.V, base.FR, base.IP, base.OP, 512, 512, base.NM, base.DDD, assets, layers})
}

func hideCraftLayer(raw json.RawMessage) json.RawMessage {
	if len(raw) < 2 || raw[len(raw)-1] != '}' {
		return raw
	}
	result := make([]byte, 0, len(raw)+10)
	result = append(result, raw[:len(raw)-1]...)
	result = append(result, []byte(`,"hd":true}`)...)
	return result
}

func craftParseRGB(value string) ([3]float64, error) {
	var result [3]float64
	if !craftHexColor.MatchString(value) {
		return result, domain.ErrStarGiftFileInvalid
	}
	for i := range result {
		channel, err := strconv.ParseUint(value[1+i*2:3+i*2], 16, 8)
		if err != nil {
			return result, domain.ErrStarGiftFileInvalid
		}
		result[i] = float64(channel) / 255
	}
	return result, nil
}

func mergeCraftRecolor(global, local craftRecolorRule) craftRecolorRule {
	if local.HasSolid {
		global.Solid, global.HasSolid = local.Solid, true
	}
	if local.HasGradient {
		global.Gradient, global.HasGradient = local.Gradient, true
	}
	return global
}

func applyCraftRecolor(raw json.RawMessage, rule craftRecolorRule) json.RawMessage {
	if rule.HasSolid {
		raw = recolorCraftJSONColors(raw, rule.Solid)
	}
	if rule.HasGradient {
		raw = recolorCraftJSONGradients(raw, rule.Gradient)
	}
	return raw
}

// A layer-specific edit must not change another layer which references the
// same precomposition. Clone its referenced asset graph under a private ID.
func cloneCraftAsset(ref string, originals map[string]json.RawMessage, prefix string, rule craftRecolorRule, seen map[string]bool, assets *[]json.RawMessage) {
	if seen[ref] {
		return
	}
	asset, ok := originals[ref]
	if !ok {
		return
	}
	seen[ref] = true
	for _, match := range craftReferencePattern.FindAllSubmatch(asset, -1) {
		if string(match[1]) == "refId" {
			cloneCraftAsset(string(match[2]), originals, prefix, rule, seen, assets)
		}
	}
	*assets = append(*assets, prefixCraftJSONFields(applyCraftRecolor(asset, rule), prefix, true))
}

var craftReferencePattern = regexp.MustCompile(`"(refId|id)"\s*:\s*"([^"\\]*)"`)
var craftHexColor = regexp.MustCompile(`^#[0-9A-Fa-f]{6}$`)
var craftStaticColorPattern = regexp.MustCompile(`"c"\s*:\s*\{[^{}]*\}`)
var craftGradientPattern = regexp.MustCompile(`"g"\s*:\s*\{`)
var craftGradientArrayPattern = regexp.MustCompile(`"(k|s|e)"\s*:\s*\[`)

// Find the matching JSON object/array end without changing the surrounding
// Lottie field order, which some official animations require for rendering.
func craftJSONCompositeEnd(raw []byte, start int, open, close byte) int {
	depth, inString, escaped := 0, false, false
	for i := start; i < len(raw); i++ {
		switch {
		case inString && escaped:
			escaped = false
		case inString && raw[i] == '\\':
			escaped = true
		case raw[i] == '"':
			inString = !inString
		case !inString && raw[i] == open:
			depth++
		case !inString && raw[i] == close:
			depth--
			if depth == 0 {
				return i + 1
			}
		}
	}
	return -1
}

// Recolor only RGB gradient stops. Stop positions and trailing alpha stops stay
// byte-for-byte equivalent; animated keyframe s/e arrays are handled too.
func recolorCraftJSONGradients(raw json.RawMessage, gradient [2][3]float64) json.RawMessage {
	var result []byte
	cursor := 0
	for _, match := range craftGradientPattern.FindAllIndex(raw, -1) {
		if match[0] < cursor {
			continue
		}
		start := match[1] - 1
		end := craftJSONCompositeEnd(raw, start, '{', '}')
		if end < 0 {
			continue
		}
		var property struct {
			P int             `json:"p"`
			K json.RawMessage `json:"k"`
		}
		if err := json.Unmarshal(raw[start:end], &property); err != nil || property.P < 2 || property.P > 64 || len(property.K) == 0 {
			continue
		}
		updated := recolorCraftGradientProperty(raw[start:end], property.P, gradient)
		result = append(result, raw[cursor:start]...)
		result = append(result, updated...)
		cursor = end
	}
	if cursor == 0 {
		return raw
	}
	return append(result, raw[cursor:]...)
}

func recolorCraftGradientProperty(raw []byte, stops int, gradient [2][3]float64) []byte {
	var result []byte
	cursor := 0
	for _, match := range craftGradientArrayPattern.FindAllIndex(raw, -1) {
		if match[0] < cursor {
			continue
		}
		start := match[1] - 1
		end := craftJSONCompositeEnd(raw, start, '[', ']')
		if end < 0 {
			continue
		}
		var values []float64
		if err := json.Unmarshal(raw[start:end], &values); err != nil || len(values) < 4*stops {
			continue
		}
		for stop := 0; stop < stops; stop++ {
			position := math.Max(0, math.Min(1, values[4*stop]))
			for channel := 0; channel < 3; channel++ {
				values[4*stop+1+channel] = gradient[0][channel]*(1-position) + gradient[1][channel]*position
			}
		}
		encoded, err := json.Marshal(values)
		if err != nil {
			continue
		}
		result = append(result, raw[cursor:start]...)
		result = append(result, encoded...)
		cursor = end
	}
	if cursor == 0 {
		return raw
	}
	return append(result, raw[cursor:]...)
}

// Recolor solid Lottie fills and strokes without re-encoding the whole layer:
// rlottie depends on the original ordering of some official gift JSON fields.
func recolorCraftJSONColors(raw json.RawMessage, target [3]float64) json.RawMessage {
	return craftStaticColorPattern.ReplaceAllFunc(raw, func(match []byte) []byte {
		index := bytes.IndexByte(match, '{')
		if index < 0 {
			return match
		}
		var color struct {
			Animated int       `json:"a"`
			Values   []float64 `json:"k"`
		}
		if err := json.Unmarshal(match[index:], &color); err != nil || color.Animated != 0 || len(color.Values) < 3 || len(color.Values) > 4 {
			return match
		}
		luminance := .2126*color.Values[0] + .7152*color.Values[1] + .0722*color.Values[2]
		if luminance < 0 {
			luminance = 0
		} else if luminance > 1 {
			luminance = 1
		}
		factor := .35 + .65*luminance
		for i := 0; i < 3; i++ {
			color.Values[i] = target[i] * factor
		}
		values, err := json.Marshal(map[string]any{"a": 0, "k": color.Values})
		if err != nil {
			return match
		}
		return append(append([]byte{}, match[:index]...), values...)
	})
}

// Rewriting only reference values keeps every source shape's JSON key order.
// rlottie renders some official gifts as transparent after map re-encoding.
func prefixCraftJSONFields(raw []byte, prefix string, includeIDs bool) json.RawMessage {
	return craftReferencePattern.ReplaceAllFunc(raw, func(match []byte) []byte {
		parts := craftReferencePattern.FindSubmatch(match)
		if string(parts[1]) == "id" && !includeIDs {
			return match
		}
		key := string(parts[1])
		value, _ := json.Marshal(prefix + string(parts[2]))
		return []byte(`"` + key + `":` + string(value))
	})
}

func crossCraftPreview(data []byte, center, edge int, position float64) ([]byte, error) {
	const size = 256
	model, err := lottierender.Render(data, size, size, position)
	if err != nil {
		return nil, err
	}
	visible := 0
	for i := 3; i < len(model.Pix); i += 4 {
		if model.Pix[i] > 16 {
			visible++
		}
	}
	if visible < size*size/100 {
		return nil, fmt.Errorf("model render is almost empty")
	}
	canvas := image.NewRGBA(image.Rect(0, 0, size, size))
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			distance := math.Hypot(float64(x-size/2), float64(y-size/2)) / float64(size) * 1.5
			if distance > 1 {
				distance = 1
			}
			mix := func(shift uint) uint8 {
				a, b := (center>>shift)&255, (edge>>shift)&255
				return uint8(math.Round(float64(a)*(1-distance) + float64(b)*distance))
			}
			canvas.SetRGBA(x, y, color.RGBA{R: mix(16), G: mix(8), B: mix(0), A: 255})
		}
	}
	draw.Draw(canvas, canvas.Bounds(), model, image.Point{}, draw.Over)
	var out bytes.Buffer
	if err := png.Encode(&out, canvas); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}
