package stargifts

import (
	"encoding/json"
	"testing"

	"telesrv/internal/domain"
)

func TestCrossCraftCompositionKeepsBothSourceAnimations(t *testing.T) {
	fixture := []byte(`{"v":"5.7.4","fr":30,"ip":0,"op":60,"w":512,"h":512,"assets":[{"id":"shape","w":512,"h":512,"layers":[{"ty":4,"ind":1}]}],"layers":[{"ty":4,"ind":1,"nm":"hand"},{"ty":0,"ind":2,"refId":"shape"}]}`)
	sources := []domain.StarGiftCraftSource{{ModelName: "A", AnimationJSON: fixture}, {ModelName: "B", AnimationJSON: fixture}}
	plan, err := parseCrossCraftPlan(`{"parts":[{"source":0,"role":"subject","x":256,"y":256,"scale":0.9,"rotation":0},{"source":1,"role":"prop","x":320,"y":290,"scale":0.4,"rotation":10}],"foreground_layers":[0],"story":"The character holds the smaller gift.","name":"Fusion"}`, 2)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := composeCrossCraftLottie(sources, plan)
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Assets []struct {
			ID     string `json:"id"`
			Layers []struct {
				RefID  string `json:"refId"`
				Hidden bool   `json:"hd"`
			} `json:"layers"`
		} `json:"assets"`
		Layers []struct {
			RefID string `json:"refId"`
			Ks    struct {
				P struct {
					K []float64 `json:"k"`
				} `json:"p"`
				S struct {
					K []float64 `json:"k"`
				} `json:"s"`
			} `json:"ks"`
		} `json:"layers"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Assets) != 5 || len(out.Layers) != 3 ||
		out.Assets[1].Layers[1].RefID != "craft0_shape" || !out.Assets[2].Layers[1].Hidden ||
		out.Assets[4].Layers[1].RefID != "craft1_shape" || out.Layers[0].RefID != "craft0_foreground" ||
		out.Layers[1].RefID != "craft1_root" || out.Layers[2].RefID != "craft0_root" ||
		out.Layers[1].Ks.P.K[0] != 320 || out.Layers[1].Ks.S.K[0] != 40 {
		t.Fatalf("lost animation sources: %+v", out)
	}
}

func TestCrossCraftPlanRejectsMissingSource(t *testing.T) {
	if _, err := parseCrossCraftPlan(`{"parts":[{"source":0,"role":"subject","x":256,"y":256,"scale":0.9},{"source":0,"role":"prop","x":320,"y":290,"scale":0.4}],"story":"The character holds the gift.","name":"Fusion"}`, 2); err == nil {
		t.Fatal("accepted duplicate source")
	}
	if _, err := parseCrossCraftPlan(`{"parts":[{"source":0,"role":"subject","x":256,"y":256,"scale":0.9},{"source":1,"role":"prop","x":256,"y":256,"scale":0.9}],"story":"The character holds the gift.","name":"Fusion"}`, 2); err == nil {
		t.Fatal("accepted full-size overlay")
	}
}

func TestCrossCraftRecolorsSelectedObject(t *testing.T) {
	fixture := []byte(`{"v":"5.7.4","fr":30,"ip":0,"op":60,"w":512,"h":512,"layers":[{"ty":4,"ind":1,"nm":"Body","shapes":[{"ty":"fl","c":{"a":0,"k":[1,1,1,1]}}]}]}`)
	plan, err := parseCrossCraftPlan(`{"parts":[{"source":0,"role":"subject","x":256,"y":256,"scale":0.9},{"source":1,"role":"prop","x":310,"y":280,"scale":0.4}],"recolors":[{"source":1,"color":"#FF0000"}],"story":"The two characters share a red costume.","name":"Red Duo"}`, 2)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := composeCrossCraftLottie([]domain.StarGiftCraftSource{{AnimationJSON: fixture}, {AnimationJSON: fixture}}, plan)
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Assets []struct {
			Layers []struct {
				Shapes []struct {
					C struct {
						K []float64 `json:"k"`
					} `json:"c"`
				} `json:"shapes"`
			} `json:"layers"`
		} `json:"assets"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	base := out.Assets[0].Layers[0].Shapes[0].C.K
	colored := out.Assets[1].Layers[0].Shapes[0].C.K
	if base[0] != 1 || base[1] != 1 || base[2] != 1 || colored[0] != 1 || colored[1] != 0 || colored[2] != 0 {
		t.Fatalf("unexpected object colors: base=%v recolored=%v", base, colored)
	}
	if _, err := parseCrossCraftPlan(`{"parts":[{"source":0,"role":"subject","x":256,"y":256,"scale":0.9},{"source":1,"role":"prop","x":310,"y":280,"scale":0.4}],"recolors":[{"source":1,"color":"red"}],"story":"The two characters share a red costume.","name":"Red Duo"}`, 2); err == nil {
		t.Fatal("accepted invalid recolor")
	}
}

func TestCrossCraftReplacesSubjectHeadLayer(t *testing.T) {
	statue := []byte(`{"v":"5.7.4","fr":30,"ip":0,"op":60,"w":512,"h":512,"layers":[{"ty":4,"ind":1,"nm":"Body"},{"ty":4,"ind":2,"nm":"Head"},{"ty":4,"ind":3,"nm":"Arm"}]}`)
	smile := []byte(`{"v":"5.7.4","fr":30,"ip":0,"op":60,"w":512,"h":512,"layers":[{"ty":4,"ind":1,"nm":"Smile"}]}`)
	plan, err := parseCrossCraftPlan(`{"parts":[{"source":0,"role":"subject","x":256,"y":256,"scale":0.9},{"source":1,"role":"prop","x":256,"y":145,"scale":0.4}],"foreground_layers":[],"hidden_layers":[1],"story":"The smile replaces the clown statue's head.","name":"Smiling Clown"}`, 2)
	if err != nil {
		t.Fatal(err)
	}
	merged, err := composeCrossCraftLottie([]domain.StarGiftCraftSource{{AnimationJSON: statue}, {AnimationJSON: smile}}, plan)
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Assets []struct {
			ID     string `json:"id"`
			Layers []struct {
				Hidden bool `json:"hd"`
			} `json:"layers"`
		} `json:"assets"`
	}
	if err := json.Unmarshal(merged, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Assets) != 2 || out.Assets[0].ID != "craft0_root" || len(out.Assets[0].Layers) != 3 ||
		out.Assets[0].Layers[0].Hidden || !out.Assets[0].Layers[1].Hidden || out.Assets[0].Layers[2].Hidden {
		t.Fatalf("head replacement changed the wrong subject layers: %+v", out.Assets)
	}
	if _, err := parseCrossCraftPlan(`{"parts":[{"source":0,"role":"subject","x":256,"y":256,"scale":0.9},{"source":1,"role":"prop","x":256,"y":145,"scale":0.4}],"foreground_layers":[1],"hidden_layers":[1],"story":"The smile replaces the clown statue's head.","name":"Smiling Clown"}`, 2); err == nil {
		t.Fatal("accepted a hidden layer as foreground")
	}
}
