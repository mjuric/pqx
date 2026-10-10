package plots

import (
	"math"
	"strings"

	"github.com/mjuric/pqx/go/internal/styled"
)

// The Mollweide sky map: a port of acid's skymap_art (via plots.py). Each
// character cell is inverse-projected to (lon, lat) and looked up in the
// count grid; area is carried by 2×2 quadrant glyphs, density by colour, and
// the limb and graticule are braille dots. RA increases to the left.

var (
	sqrt2 = math.Sqrt(2.0)
	xExt  = 2.0 * sqrt2
	yExt  = sqrt2
)

const trace = "·"

// SkyOpts configure SkyMap.
type SkyOpts struct {
	Width, Height int     // default 72 wide; Height 0 for the map's natural height
	Cmap          string  // default magma
	Center        float64 // RA at the centre, 0 or 180
	Caption       string
}

// SkyShape is the map's size in cells for a width and height (sky_shape);
// height 0 or less means no limit.
func SkyShape(width, height int) (int, int) {
	w := max(8, width)
	if height > 0 {
		w = min(w, max(8, height*4))
	}
	return w, max(3, int(math.RoundToEven(float64(w)/4.0)))
}

// inverseMollweide is (lon, lat) in radians of a point of the Mollweide plane,
// and whether it lies on the map (_inverse_mollweide).
func inverseMollweide(x, y float64) (lon, lat float64, valid bool) {
	t := y / sqrt2
	valid = math.Abs(t) <= 1.0
	theta := math.Asin(clip1(t))
	s := (2.0*theta + math.Sin(2.0*theta)) / pi
	valid = valid && math.Abs(s) <= 1.0
	lat = math.Asin(clip1(s))
	cosT := math.Cos(theta)
	lon = pi * x / (2.0 * sqrt2 * cosT)
	valid = valid && !math.IsNaN(lon) && !math.IsInf(lon, 0) && math.Abs(lon) <= pi+1e-9
	return lon, lat, valid
}

func clip1(v float64) float64 { return math.Max(-1, math.Min(1, v)) }

// grid is an (nlat, nlon) equirectangular grid, row 0 at −90°.
type grid struct {
	nlat, nlon int
	v          []float64
}

// coarsen sums f×f blocks of g, f reduced until it divides both
// dimensions (_coarsen).
func coarsen(g grid, f int) grid {
	if f <= 1 {
		return g
	}
	for g.nlat%f != 0 || g.nlon%f != 0 {
		f--
	}
	if f <= 1 {
		return g
	}
	out := grid{g.nlat / f, g.nlon / f, make([]float64, g.nlat/f*(g.nlon/f))}
	for j := range g.nlat {
		row := g.v[j*g.nlon : (j+1)*g.nlon]
		orow := out.v[(j/f)*out.nlon : (j/f+1)*out.nlon]
		for i, x := range row {
			orow[i/f] += x
		}
	}
	return out
}

// lookup is the cell of g holding (lon, lat) in degrees, and its row (_lookup).
func (g grid) lookup(lonDeg, latDeg float64) (float64, int) {
	i := min(max(int(pyMod(lonDeg, 360.0)/360.0*float64(g.nlon)), 0), g.nlon-1)
	j := min(max(int((latDeg+90.0)/180.0*float64(g.nlat)), 0), g.nlat-1)
	return g.v[j*g.nlon+i], j
}

// cellAreaDeg2 is the area (deg²) of the cells in latitude row j (_cell_area_deg2).
func cellAreaDeg2(nlat, nlon, j int) float64 {
	dlat := 180.0 / float64(nlat)
	lat1 := (-90.0 + float64(j)*dlat) * deg2rad
	lat2 := (-90.0 + float64(j+1)*dlat) * deg2rad
	return (360.0 / float64(nlon) * deg2rad) * (math.Sin(lat2) - math.Sin(lat1)) * (rad2deg * rad2deg)
}

// brailleDots are the dot bits of a 2×4 braille cell by (dx, dy).
var brailleDots = [2][4]int{{0x01, 0x02, 0x04, 0x40}, {0x08, 0x10, 0x20, 0x80}}

// SkyMap draws counts[lat][lon] (an equirectangular grid, row 0 at −90°)
// as a Mollweide map (render_skymap).
func SkyMap(counts [][]int64, o SkyOpts, l Look) styled.Text {
	width := o.Width
	if width == 0 {
		width = 72
	}
	cmap := o.Cmap
	if cmap == "" {
		cmap = DefaultCmap
	}
	width, height := SkyShape(width, o.Height)
	ctr := o.Center
	dim := l.dim()
	if len(counts) == 0 || len(counts[0]) == 0 {
		return styled.Text{}
	}
	g := grid{len(counts), len(counts[0]), make([]float64, len(counts)*len(counts[0]))}
	occ := grid{g.nlat, g.nlon, make([]float64, len(g.v))}
	for j, row := range counts {
		for i, n := range row[:g.nlon] {
			g.v[j*g.nlon+i] = float64(n)
			if n > 0 {
				occ.v[j*g.nlon+i] = 1
			}
		}
	}
	res := 180.0 / float64(g.nlat)
	toLonLat := func(x, y float64) (float64, float64, bool) {
		lon, lat, valid := inverseMollweide(x, y)
		return pyMod(lon*rad2deg+ctr, 360.0), lat * rad2deg, valid
	}

	// density per character: surface density at about character resolution
	fch := max(1, int(math.RoundToEven((360.0/float64(width))/res)))
	cg := coarsen(g, fch)
	area := make([]float64, cg.nlat)
	for j := range area {
		area[j] = cellAreaDeg2(cg.nlat, cg.nlon, j)
	}
	sigma := make([]float64, height*width)
	sub := [3]float64{1.0 / 6, 0.5, 5.0 / 6}
	for r := range height {
		for c := range width {
			s := 0.0
			for _, sy := range sub {
				for _, sx := range sub {
					x := xExt * (1.0 - 2.0*((float64(c)+sx)/float64(width)))
					y := yExt * (1.0 - 2.0*((float64(r)+sy)/float64(height)))
					lon, lat, valid := toLonLat(x, y)
					if !valid {
						continue
					}
					n, j := cg.lookup(lon, lat)
					s = math.Max(s, n/area[j])
				}
			}
			sigma[r*width+c] = s
		}
	}
	smin, smax := 0.0, 0.0
	first := true
	for _, s := range sigma {
		if s > 0 {
			if first || s < smin {
				smin = s
			}
			if first || s > smax {
				smax = s
			}
			first = false
		}
	}
	frac := make([]float64, len(sigma))
	lsmin, lsmax := math.Log(smin), math.Log(smax)
	for k, s := range sigma {
		switch {
		case !(s > 0):
		case smax > smin:
			frac[k] = (math.Log(math.Max(s, smin)) - lsmin) / (lsmax - lsmin)
		default:
			frac[k] = 1
		}
	}

	// coverage per 2×2 sub-cell: occupied fine cells / all fine cells in the lookup cell
	sw, sh := 2*width, 2*height
	fsub := max(1, int(math.RoundToEven((360.0/float64(sw))/res)))
	occ = coarsen(occ, fsub)
	ftrue := float64(g.nlat / occ.nlat)
	for k := range occ.v {
		occ.v[k] /= ftrue * ftrue
	}
	cov := make([]float64, sh*sw)
	svalid := make([]bool, sh*sw)
	for r := range sh {
		for c := range sw {
			x := xExt * (1.0 - 2.0*((float64(c)+0.5)/float64(sw)))
			y := yExt * (1.0 - 2.0*((float64(r)+0.5)/float64(sh)))
			lon, lat, valid := toLonLat(x, y)
			if valid {
				svalid[r*sw+c] = true
				cov[r*sw+c], _ = occ.lookup(lon, lat)
			}
		}
	}

	// braille limb and graticule
	bw, bh := 2*width, 4*height
	bvalid := make([]bool, bh*bw)
	blat := make([]float64, bh*bw) // degrees, 99 off the map
	for r := range bh {
		for c := range bw {
			x := xExt * (1.0 - 2.0*((float64(c)+0.5)/float64(bw)))
			y := yExt * (1.0 - 2.0*((float64(r)+0.5)/float64(bh)))
			_, lat, valid := inverseMollweide(x, y)
			bvalid[r*bw+c] = valid
			blat[r*bw+c] = lat * rad2deg
		}
	}
	limb := make([]bool, bh*bw)
	grat := make([]bool, bh*bw)
	for c := range bw {
		lo, hi := -1, -1
		for r := range bh {
			if bvalid[r*bw+c] {
				if lo < 0 {
					lo = r
				}
				hi = r
			}
		}
		if lo >= 0 {
			limb[lo*bw+c], limb[hi*bw+c] = true, true
		}
	}
	// nearestRow is the row holding the valid dot whose latitude is nearest
	// to lat0 (the first such row on ties).
	nearestRow := func(lat0 float64) int {
		best, bestD := 0, math.Inf(1)
		for r := range bh {
			d := math.Inf(1)
			for c := range bw {
				v := 99.0
				if bvalid[r*bw+c] {
					v = blat[r*bw+c] - lat0
				}
				d = math.Min(d, math.Abs(v))
			}
			if d < bestD {
				best, bestD = r, d
			}
		}
		return best
	}
	eq := nearestRow(0)
	for c := range bw {
		if bvalid[eq*bw+c] {
			grat[eq*bw+c] = true
		}
	}
	for r := range bh {
		if bvalid[r*bw+bw/2] {
			grat[r*bw+bw/2] = true
		}
	}
	for _, dlat := range []float64{-60, -30, 30, 60} { // faint parallels
		r := nearestRow(dlat)
		for c := 0; c < bw; c += 4 {
			if bvalid[r*bw+c] {
				grat[r*bw+c] = true
			}
		}
	}

	var t builder
	for r := range height {
		for c := range width {
			y0, x0 := 2*r, 2*c
			at := func(dy, dx int) int { return (y0+dy)*sw + x0 + dx }
			ch := ""
			if svalid[at(0, 0)] || svalid[at(0, 1)] || svalid[at(1, 0)] || svalid[at(1, 1)] {
				ch = string(quadrant(cov[at(0, 0)] >= 0.5, cov[at(0, 1)] >= 0.5, cov[at(1, 0)] >= 0.5, cov[at(1, 1)] >= 0.5))
				if ch == " " {
					ch = ""
					if max(cov[at(0, 0)], cov[at(0, 1)], cov[at(1, 0)], cov[at(1, 1)]) > 0 {
						ch = trace
					}
				}
			}
			if ch != "" {
				t.add(ch, densityStyle(cmap, frac[r*width+c], l.DarkBG, l))
				continue
			}
			code := 0 // limb dots and graticule dots are both drawn dim
			by0, bx0 := 4*r, 2*c
			for dx := range 2 {
				for dy := range 4 {
					k := (by0+dy)*bw + bx0 + dx
					if limb[k] || grat[k] {
						code |= brailleDots[dx][dy]
					}
				}
			}
			if code != 0 {
				t.add(string(rune(0x2800+code)), dim)
			} else {
				t.add(" ", styled.Style{})
			}
		}
		t.add("\n", styled.Style{})
	}

	// RA labels under the equator's ends and the centre
	lbl := []rune(strings.Repeat(" ", width))
	for _, sp := range []struct {
		s string
		p int
	}{
		{pyF(pyMod(ctr+180, 360), 0) + "°", 0},
		{pyF(pyMod(ctr, 360), 0) + "°", width / 2},
		{pyF(pyMod(ctr-180, 360), 0) + "°", width - 1},
	} {
		n := runeLen(sp.s)
		lbl = splice(lbl, max(0, min(width-n, sp.p-n/2)), sp.s)
	}
	t.add(string(lbl)+"\n", dim)
	if smax > 0 {
		t.addText(Colorbar(cmap, smin, smax, "deg⁻²", "density", width, l))
	}
	if o.Caption != "" {
		t.add("\n"+center(o.Caption, width), dim)
	}
	return t.text()
}
