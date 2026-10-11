// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

package site

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"sync"
)

// The app icon is a white five-petal flower (the ✿ in the logo) on a
// pink-to-purple gradient. It's drawn here rather than kept as binary
// files, at whatever sizes the web app manifest wants.

var (
	pink   = [3]float64{0xff, 0x3e, 0x9a}
	purple = [3]float64{0xb1, 0x4c, 0xf0}
	centre = [3]float64{0xff, 0xd1, 0xe8}
)

// appIcons returns every icon file, drawn once per process.
var appIcons = sync.OnceValue(func() map[string][]byte {
	return map[string][]byte{
		"icon-192.png":          iconPNG(192, false),
		"icon-512.png":          iconPNG(512, false),
		"icon-maskable-512.png": iconPNG(512, true),
		"apple-touch-icon.png":  iconPNG(180, true),
		"icon.svg":              []byte(iconSVG()),
	}
})

// iconPNG draws the icon at size pixels. A maskable icon fills the whole
// square and keeps the flower inside the middle 80%, as platforms may crop
// it to any shape; the plain one has rounded corners.
func iconPNG(size int, maskable bool) []byte {
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	const ss = 4 // samples per pixel side, for smooth edges
	flower := 0.36
	if maskable {
		flower = 0.28
	}
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			var r, g, b, a float64
			for sy := 0; sy < ss; sy++ {
				for sx := 0; sx < ss; sx++ {
					u := (float64(x) + (float64(sx)+0.5)/ss) / float64(size)
					v := (float64(y) + (float64(sy)+0.5)/ss) / float64(size)
					c, in := iconSample(u, v, flower, maskable)
					if !in {
						continue
					}
					r, g, b, a = r+c[0], g+c[1], b+c[2], a+1
				}
			}
			if a == 0 {
				continue
			}
			img.SetNRGBA(x, y, color.NRGBA{uint8(r / a), uint8(g / a), uint8(b / a), uint8(255 * a / (ss * ss))})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		panic(err) // can't happen writing to a buffer
	}
	return buf.Bytes()
}

// iconSample is the icon's colour at (u, v) in the unit square, and
// whether that point is inside the icon at all.
func iconSample(u, v, flower float64, maskable bool) ([3]float64, bool) {
	if !maskable && !inRoundedSquare(u, v, 0.22) {
		return [3]float64{}, false
	}
	t := (u + v) / 2
	bg := [3]float64{}
	for i := range bg {
		bg[i] = pink[i] + (purple[i]-pink[i])*t
	}
	dx, dy := u-0.5, v-0.5
	d := math.Hypot(dx, dy)
	if d < flower*0.3 {
		return centre, true
	}
	petal := flower * 0.36
	for k := 0; k < 5; k++ {
		ang := -math.Pi/2 + float64(k)*2*math.Pi/5
		px, py := 0.5+math.Cos(ang)*flower*0.62, 0.5+math.Sin(ang)*flower*0.62
		if math.Hypot(u-px, v-py) < petal {
			return [3]float64{255, 255, 255}, true
		}
	}
	return bg, true
}

func inRoundedSquare(u, v, r float64) bool {
	cx := math.Max(r, math.Min(1-r, u))
	cy := math.Max(r, math.Min(1-r, v))
	return math.Hypot(u-cx, v-cy) <= r
}

// iconSVG is the same icon as SVG, for favicons.
func iconSVG() string {
	const flower = 0.36
	s := `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 100 100">` +
		`<defs><linearGradient id="g" x1="0" y1="0" x2="1" y2="1">` +
		`<stop offset="0" stop-color="#ff3e9a"/><stop offset="1" stop-color="#b14cf0"/></linearGradient></defs>` +
		`<rect width="100" height="100" rx="22" fill="url(#g)"/><g fill="#fff">`
	for k := 0; k < 5; k++ {
		ang := -math.Pi/2 + float64(k)*2*math.Pi/5
		s += fmt.Sprintf(`<circle cx="%.2f" cy="%.2f" r="%.2f"/>`,
			50+math.Cos(ang)*flower*62, 50+math.Sin(ang)*flower*62, flower*36)
	}
	return s + fmt.Sprintf(`</g><circle cx="50" cy="50" r="%.2f" fill="#ffd1e8"/></svg>`, flower*30)
}
