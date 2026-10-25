//go:build !rlottie || !cgo

package lottierender

import "image"

// Render keeps ordinary cross-platform builds free of a native dependency.
// Production gift-image builds use -tags=rlottie and link librlottie.
func Render(_ []byte, _, _ int, _ float64) (*image.NRGBA, error) {
	return nil, ErrUnavailable
}
