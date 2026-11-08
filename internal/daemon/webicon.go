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

// serveIcon: the home screen icon (iOS wants a PNG), size×size. tapi on
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

// Colours: bg from docs/ui-guidelines.md, tapi's four roles are the dark
// theme default (D) in docs/mascot.md.
var (
	iconBG    = [3]float64{0x15, 0x14, 0x21}
	iconHi    = [3]float64{0xf8, 0xec, 0xd9}
	iconBody  = [3]float64{0xe4, 0xc4, 0x9a}
	iconShade = [3]float64{0xc7, 0xa5, 0x7c}
	iconEye   = [3]float64{0x2b, 0x20, 0x1c}
)

// tapiAt is the colour of the icon at (x, y) in unit coordinates (0..1,
// y down). tapi is the round shape smoothed out: a slightly wide circle
// (the 50×47 of the 5×3 sextant sprite), glint lighting (a highlight spot
// near the top left, not the whole top; shadow along the bottom and the
// right edge) and two eyes on the middle row. The body sits a touch below
// centre and fills about two thirds of the width, so iOS's rounded-corner
// mask never reaches it.
func tapiAt(x, y float64) [3]float64 {
	const (
		cx, cy = 0.5, 0.53
		rx, ry = 0.33, 0.31
	)
	// Soft contact shadow under the body, on the bg.
	col := iconBG
	if d := math.Hypot((x-cx)/(rx*0.85), (y-(cy+ry*0.98))/(ry*0.16)); d < 1 {
		col = mix(col, [3]float64{0x0c, 0x0b, 0x14}, 0.6*(1-d*d))
	}
	u, v := (x-cx)/rx, (y-cy)/ry
	if u*u+v*v > 1 {
		return col
	}
	col = iconBody
	// Shadow: whatever of the body an up-left shifted copy doesn't cover,
	// i.e. a crescent along the bottom and the right side.
	su, sv := u+0.16, v+0.20
	if su*su+sv*sv > 1 {
		col = iconShade
	}
	// Glint: an oval spot over the second column of the top row.
	hu, hv := (u+0.40)/0.22, (v+0.58)/0.13
	if hu*hu+hv*hv <= 1 {
		col = iconHi
	}
	// Eyes: columns 1 and 3 of 5, middle row. A small catch-light in each
	// keeps them from reading as holes at home screen size.
	for _, ex := range []float64{-0.40, 0.40} {
		eu, ev := u-ex, v-0.06
		if eu*eu+ev*ev <= 0.17*0.17 {
			col = iconEye
			if cu, cv := eu+0.06, ev+0.07; cu*cu+cv*cv <= 0.055*0.055 {
				col = iconHi
			}
		}
	}
	return col
}

func mix(a, b [3]float64, t float64) [3]float64 {
	return [3]float64{a[0] + (b[0]-a[0])*t, a[1] + (b[1]-a[1])*t, a[2] + (b[2]-a[2])*t}
}

// drawIcon renders tapiAt at n×n with 4×4 supersampling for smooth edges.
func drawIcon(n int) []byte {
	const ss = 4
	img := image.NewRGBA(image.Rect(0, 0, n, n))
	for py := 0; py < n; py++ {
		for px := 0; px < n; px++ {
			var sum [3]float64
			for sy := 0; sy < ss; sy++ {
				for sx := 0; sx < ss; sx++ {
					c := tapiAt((float64(px)+(float64(sx)+0.5)/ss)/float64(n), (float64(py)+(float64(sy)+0.5)/ss)/float64(n))
					sum[0] += c[0]
					sum[1] += c[1]
					sum[2] += c[2]
				}
			}
			img.SetRGBA(px, py, color.RGBA{
				uint8(math.Round(sum[0] / ss / ss)),
				uint8(math.Round(sum[1] / ss / ss)),
				uint8(math.Round(sum[2] / ss / ss)),
				0xff,
			})
		}
	}
	var b bytes.Buffer
	png.Encode(&b, img)
	return b.Bytes()
}
