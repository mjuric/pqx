package theme

import (
	"image/color"
	"math"

	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
)

// Colours are reduced for the terminal the way Python pqx's are (Rich's
// Color.downgrade), not Bubble Tea's way: the two pick different neighbours
// in the 256- and 16-colour palettes, and the parity checks compare them.

// SetProfile sets the terminal's colour profile (colorprofile.Detect on the
// output pqx draws on): with fewer colours than truecolor, the theme's
// colours are reduced as Rich reduces them.
func (t *Theme) SetProfile(p colorprofile.Profile) {
	t.profile = p
	t.build()
}

// out is c as it is sent to a terminal of the theme's profile. With a
// named theme colours stay as they are until Paint, which reduces them.
func (t *Theme) out(c color.Color) color.Color {
	if t.named {
		return c
	}
	return t.reduce(c)
}

// reduce is c for a terminal of the theme's profile.
func (t *Theme) reduce(c color.Color) color.Color {
	switch t.profile {
	case colorprofile.ANSI256:
		if rgb, ok := c.(color.RGBA); ok {
			return ansi.IndexedColor(rich256(rgb.R, rgb.G, rgb.B))
		}
	case colorprofile.ANSI:
		switch c := c.(type) {
		case color.RGBA:
			return ansi.BasicColor(richStandard(c.R, c.G, c.B))
		case ansi.IndexedColor:
			if c >= 16 {
				x := xterm(int(c))
				return ansi.BasicColor(richStandard(x.r, x.g, x.b))
			}
		}
	}
	return c
}

// rich256 is Rich's downgrade of a truecolor colour to the 256-colour
// palette: greys (saturation under 15%) to the grey ramp, the rest to the
// 6×6×6 cube, rounding half to even as Python does.
func rich256(r, g, b uint8) uint8 {
	_, l, s := rgbToHLS(float64(r)/255, float64(g)/255, float64(b)/255)
	if s < 0.15 {
		gray := int(math.RoundToEven(l * 25))
		switch gray {
		case 0:
			return 16
		case 25:
			return 231
		}
		return uint8(231 + gray)
	}
	six := func(v uint8) int {
		x := float64(v) / 95
		if v >= 95 {
			x = 1 + (float64(v)-95)/40
		}
		return int(math.RoundToEven(x))
	}
	return uint8(16 + 36*six(r) + 6*six(g) + six(b))
}

// rgbToHLS is Python's colorsys.rgb_to_hls (lightness and saturation).
func rgbToHLS(r, g, b float64) (h, l, s float64) {
	maxc := math.Max(r, math.Max(g, b))
	minc := math.Min(r, math.Min(g, b))
	sumc := maxc + minc
	rangec := maxc - minc
	l = sumc / 2
	if minc == maxc {
		return 0, l, 0
	}
	if l <= 0.5 {
		s = rangec / sumc
	} else {
		s = rangec / (2 - maxc - minc)
	}
	return 0, l, s
}

// standardPalette is Rich's STANDARD_PALETTE, which it matches 16-colour
// terminals against.
var standardPalette = [16][3]int{
	{0, 0, 0}, {170, 0, 0}, {0, 170, 0}, {170, 85, 0}, {0, 0, 170}, {170, 0, 170}, {0, 170, 170}, {170, 170, 170},
	{85, 85, 85}, {255, 85, 85}, {85, 255, 85}, {255, 255, 85}, {85, 85, 255}, {255, 85, 255}, {85, 255, 255}, {255, 255, 255},
}

// richStandard is Rich's Palette.match against standardPalette.
func richStandard(r, g, b uint8) int {
	best, bestD := 0, math.Inf(1)
	for i, c := range standardPalette {
		rm := (int(r) + c[0]) / 2
		dr, dg, db := int(r)-c[0], int(g)-c[1], int(b)-c[2]
		d := math.Sqrt(float64(((512+rm)*dr*dr)>>8 + 4*dg*dg + ((767-rm)*db*db)>>8))
		if d < bestD {
			best, bestD = i, d
		}
	}
	return best
}
