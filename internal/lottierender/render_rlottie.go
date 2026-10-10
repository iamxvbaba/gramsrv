//go:build rlottie && cgo

package lottierender

/*
#cgo LDFLAGS: -lrlottie
#include <stdlib.h>
#include <rlottie_capi.h>
*/
import "C"

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image"
	"math"
	"unsafe"
)

// Render rasterizes one normalized Lottie frame into non-premultiplied NRGBA.
// The imported gift pipeline rejects external assets and expressions, so no
// filesystem or network resource is available to the native renderer.
func Render(data []byte, width, height int, position float64) (*image.NRGBA, error) {
	if len(data) == 0 || width <= 0 || height <= 0 || width > 2048 || height > 2048 ||
		math.IsNaN(position) || position < 0 || position > 1 {
		return nil, fmt.Errorf("%w: invalid render input", ErrUnavailable)
	}
	jsonData := C.CString(string(data))
	defer C.free(unsafe.Pointer(jsonData))
	sum := sha256.Sum256(data)
	keyText := hex.EncodeToString(sum[:])
	key := C.CString(keyText)
	defer C.free(unsafe.Pointer(key))
	resourcePath := C.CString("")
	defer C.free(unsafe.Pointer(resourcePath))
	animation := C.lottie_animation_from_data(jsonData, key, resourcePath)
	if animation == nil {
		return nil, fmt.Errorf("%w: invalid Lottie document", ErrUnavailable)
	}
	defer C.lottie_animation_destroy(animation)
	total := int(C.lottie_animation_get_totalframe(animation))
	if total <= 0 {
		return nil, fmt.Errorf("%w: Lottie document has no frames", ErrUnavailable)
	}
	frame := int(math.Round(position * float64(total-1)))
	pixels := make([]uint32, width*height)
	C.lottie_animation_render(animation, C.size_t(frame), (*C.uint32_t)(unsafe.Pointer(&pixels[0])),
		C.size_t(width), C.size_t(height), C.size_t(width*4))

	out := image.NewNRGBA(image.Rect(0, 0, width, height))
	for i, pixel := range pixels {
		a := uint8(pixel >> 24)
		r := uint8(pixel >> 16)
		g := uint8(pixel >> 8)
		b := uint8(pixel)
		// rlottie surfaces are ARGB32 premultiplied. image.NRGBA expects straight
		// alpha, so recover the edge colors before compositing the model.
		if a != 0 && a != 255 {
			r = unpremultiply(r, a)
			g = unpremultiply(g, a)
			b = unpremultiply(b, a)
		}
		offset := i * 4
		out.Pix[offset], out.Pix[offset+1], out.Pix[offset+2], out.Pix[offset+3] = r, g, b, a
	}
	return out, nil
}

func unpremultiply(color, alpha uint8) uint8 {
	value := (uint32(color)*255 + uint32(alpha)/2) / uint32(alpha)
	if value > 255 {
		return 255
	}
	return uint8(value)
}
