package domain

import "testing"

func TestCraftGradientDoesNotFlattenOppositeSources(t *testing.T) {
	sources := []StarGiftCraftSource{{Backdrop: StarGiftCollectibleAttribute{CenterColor: 0xff0000, EdgeColor: 0x0000ff}}, {Backdrop: StarGiftCollectibleAttribute{CenterColor: 0x0000ff, EdgeColor: 0xff0000}}}
	gradient := BlendStarGiftCraftBackdrops(sources)
	if gradient.CenterColor == gradient.EdgeColor {
		t.Fatalf("flat gradient: %#v", gradient)
	}
}
