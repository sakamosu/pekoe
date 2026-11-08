package daemon

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"math"
	"net/http"
	"sync"
)

var (
	iconMu  sync.Mutex
	iconPNG = map[int][]byte{}
)

// serveIcon: the home screen icon (iOS wants a PNG), size×size. A pearl on
// the bg colour, drawn once per size.
func serveIcon(size int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		iconMu.Lock()
		b := iconPNG[size]
		if b == nil {
			b = drawIcon(size)
			iconPNG[size] = b
		}
		iconMu.Unlock()
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Cache-Control", "max-age=86400")
		w.Write(b)
	}
}

func drawIcon(n int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, n, n))
	bg := color.RGBA{0x15, 0x14, 0x21, 0xff}
	pearl := color.RGBA{0xff, 0x87, 0xda, 0xff} // primary
	hi := color.RGBA{0xff, 0xd6, 0xf2, 0xff}
	c, r := float64(n)/2, float64(n)*0.30
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			fx, fy := float64(x)+0.5, float64(y)+0.5
			col := bg
			if math.Hypot(fx-c, fy-c*1.08) <= r {
				col = pearl
				if math.Hypot(fx-(c-r*0.35), fy-(c*1.08-r*0.4)) <= r*0.22 {
					col = hi
				}
			}
			img.Set(x, y, col)
		}
	}
	var b bytes.Buffer
	png.Encode(&b, img)
	return b.Bytes()
}
