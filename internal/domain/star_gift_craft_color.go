package domain

// BlendStarGiftCraftBackdrops keeps the first gift's gradient direction while
// bringing colors from the other collections into both gradient stops.
func BlendStarGiftCraftBackdrops(sources []StarGiftCraftSource) StarGiftCollectibleAttribute {
	if len(sources) == 0 {
		return StarGiftCollectibleAttribute{}
	}
	blend := func(pick func(StarGiftCollectibleAttribute) int) int {
		first := pick(sources[0].Backdrop)
		if len(sources) == 1 {
			return first
		}
		var others [3]int
		for _, source := range sources[1:] {
			value := pick(source.Backdrop)
			others[0] += (value >> 16) & 255
			others[1] += (value >> 8) & 255
			others[2] += value & 255
		}
		var result int
		for i, shift := range []uint{16, 8, 0} {
			channel := (65*((first>>shift)&255) + 35*others[i]/(len(sources)-1) + 50) / 100
			result |= channel << shift
		}
		return result
	}
	return StarGiftCollectibleAttribute{
		CenterColor:  blend(func(a StarGiftCollectibleAttribute) int { return a.CenterColor }),
		EdgeColor:    blend(func(a StarGiftCollectibleAttribute) int { return a.EdgeColor }),
		PatternColor: blend(func(a StarGiftCollectibleAttribute) int { return a.PatternColor }),
		TextColor:    blend(func(a StarGiftCollectibleAttribute) int { return a.TextColor }),
	}
}
