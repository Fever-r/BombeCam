package main

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"math"
	"net/http"
	"sync"
)

// BombeCam's icon (tray and browser tab): a camera lens on a blue rounded
// square, drawn here so no image files need to ship.

var (
	appIconOnce sync.Once
	appIconICO  []byte
)

// appIcon returns the icon as a .ico file (16, 32 and 48 px PNG images).
func appIcon() []byte {
	appIconOnce.Do(func() {
		sizes := []int{16, 32, 48}
		var images [][]byte
		for _, sz := range sizes {
			var b bytes.Buffer
			_ = png.Encode(&b, drawAppIcon(sz))
			images = append(images, b.Bytes())
		}
		var out bytes.Buffer
		_ = binary.Write(&out, binary.LittleEndian, [3]uint16{0, 1, uint16(len(sizes))})
		offset := 6 + 16*len(sizes)
		for i, sz := range sizes {
			dim := byte(sz)
			_ = binary.Write(&out, binary.LittleEndian, struct {
				W, H, Colors, Reserved byte
				Planes, BPP            uint16
				Size, Offset           uint32
			}{dim, dim, 0, 0, 1, 32, uint32(len(images[i])), uint32(offset)})
			offset += len(images[i])
		}
		for _, img := range images {
			out.Write(img)
		}
		appIconICO = out.Bytes()
	})
	return appIconICO
}

func drawAppIcon(size int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	s := float64(size)
	blue := color.RGBA{0x3b, 0x82, 0xf6, 0xff}
	white := color.RGBA{0xf8, 0xfa, 0xfc, 0xff}
	dark := color.RGBA{0x0f, 0x17, 0x2a, 0xff}
	radius := s * 0.22
	cx, cy := s/2, s/2
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			px, py := float64(x)+0.5, float64(y)+0.5
			// rounded square, with 1 px of anti-aliasing at the corners
			dx := math.Max(math.Max(radius-px, px-(s-radius)), 0)
			dy := math.Max(math.Max(radius-py, py-(s-radius)), 0)
			cover := clamp01(radius - math.Hypot(dx, dy) + 0.5)
			if cover <= 0 {
				continue
			}
			c := blue
			d := math.Hypot(px-cx, py-cy)
			switch {
			case d < s*0.14:
				c = dark // pupil
			case d < s*0.30:
				c = white // lens
			}
			img.SetRGBA(x, y, color.RGBA{c.R, c.G, c.B, uint8(255 * cover)})
		}
	}
	return img
}

func clamp01(v float64) float64 {
	return math.Max(0, math.Min(1, v))
}

// handleFavicon serves the icon for the browser tab.
func handleFavicon(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "image/x-icon")
	w.Header().Set("Cache-Control", "max-age=86400")
	_, _ = w.Write(appIcon())
}
