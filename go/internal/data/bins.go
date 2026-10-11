package data

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/apache/arrow-go/v18/arrow"
)

// pyRepr is Python's repr of a float64: the shortest digits that read back
// as x, fixed-point (with ".0" if integral) for exponents -4 to 15, else
// d.ddde±XX. pqx writes its bin edges into SQL this way, so the queries here
// are pqx's to the character.
func pyRepr(x float64) string {
	switch {
	case math.IsNaN(x):
		return "nan"
	case math.IsInf(x, 1):
		return "inf"
	case math.IsInf(x, -1):
		return "-inf"
	}
	if x == 0 {
		if math.Signbit(x) {
			return "-0.0"
		}
		return "0.0"
	}
	e := strconv.FormatFloat(x, 'e', -1, 64) // d.ddde±XX
	i := strings.IndexByte(e, 'e')
	exp, _ := strconv.Atoi(e[i+1:])
	if exp < -4 || exp >= 16 {
		return e
	}
	f := strconv.FormatFloat(x, 'f', -1, 64)
	if !strings.ContainsAny(f, ".") {
		f += ".0"
	}
	return f
}

// finite refuses NaN and ±inf as limits.
func finite(what string, xs ...float64) error {
	for _, x := range xs {
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return fmt.Errorf("the %s must be finite, not %s", what, pyRepr(x))
		}
	}
	return nil
}

// Histogram is an equal-width histogram of a numeric (or, Temporal, a
// timestamp or date) column of view v, over o.Lo to o.Hi or the column's
// finite range (pqx's histogram). Bins are closed on the left; the last one
// also takes Hi. With Log it bins log10 of the positive values.
func (d *dataset) Histogram(ctx context.Context, v View, col string, o HistOptions) (Histogram, error) {
	if err := d.prepare(ctx, v); err != nil {
		return Histogram{}, err
	}
	bins := o.Bins
	if bins <= 0 {
		bins = 40
	}
	if bins > MaxCells {
		return Histogram{}, fmt.Errorf("%d bins are too many (at most %d)", bins, MaxCells)
	}
	rel, err := d.histRel(v, col, o)
	if err != nil {
		return Histogram{}, err
	}
	var lo, hi float64
	var haveLo, haveHi bool
	if o.Lo != nil {
		lo, haveLo = *o.Lo, true
	}
	if o.Hi != nil {
		hi, haveHi = *o.Hi, true
	}
	if !haveLo || !haveHi {
		err := d.query(ctx, "SELECT min(v), max(v) FROM "+rel, func(rec arrow.RecordBatch) error {
			if rec.NumRows() == 0 || rec.NumCols() != 2 {
				return nil
			}
			if !haveLo {
				lo, haveLo = float64At(rec.Column(0), 0)
			}
			if !haveHi {
				hi, haveHi = float64At(rec.Column(1), 0)
			}
			return nil
		})
		if err != nil {
			return Histogram{}, err
		}
	}
	if !haveLo || !haveHi {
		return Histogram{}, nil
	}
	if err := finite("histogram's range", lo, hi); err != nil {
		return Histogram{}, err
	}
	lo, hi = widen(lo, hi)
	bn, err := newBinning(lo, hi, bins)
	if err != nil {
		return Histogram{}, err
	}
	counts := make([]int64, bins)
	// Limits taken from the values filter nothing: pqx filters on them
	// anyway, but where DuckDB's math (log10 on macOS) gives a value a
	// different last digit in this query than in the min/max one, that
	// would lose the maximum.
	var limits string
	if o.Lo != nil {
		limits += " AND v >= " + pyRepr(lo)
	}
	if o.Hi != nil {
		limits += " AND v <= " + pyRepr(hi)
	}
	sql := fmt.Sprintf("SELECT least(greatest(%s, 0), %d)::INT AS b, count(*) FROM %s%s GROUP BY b",
		bn.sql("v"), bins-1, rel, limits)
	err = d.query(ctx, sql, func(rec arrow.RecordBatch) error {
		for i := range int(rec.NumRows()) {
			b, ok1 := int64At(rec.Column(0), i)
			n, ok2 := int64At(rec.Column(1), i)
			if ok1 && ok2 && b >= 0 && b < int64(bins) {
				counts[b] = n
			}
		}
		return nil
	})
	if err != nil {
		return Histogram{}, err
	}
	edges := make([]float64, bins+1)
	for i := range edges {
		edges[i] = bn.edge(i)
	}
	return Histogram{Edges: edges, Counts: counts}, nil
}

// histRel is the relation of a histogram's values v of column col of view
// v: finite, as doubles (epoch seconds if Temporal, log10 of the positive
// values if Log).
func (d *dataset) histRel(v View, col string, o HistOptions) (string, error) {
	q, err := d.colRef(v, col)
	if err != nil {
		return "", err
	}
	vexpr := "CAST(" + q + " AS DOUBLE)"
	if o.Temporal {
		vexpr = "CAST(epoch(" + q + ") AS DOUBLE)"
	}
	if o.Log {
		vexpr = "log10(CASE WHEN " + q + " > 0 THEN CAST(" + q + " AS DOUBLE) END)"
	}
	base, err := d.relationSQL(v, []string{col}, o.Sample)
	if err != nil {
		return "", err
	}
	return "(SELECT " + vexpr + " AS v FROM (\n" + base + "\n)) WHERE v IS NOT NULL AND isfinite(v)", nil
}

// SkyCounts bins (lon, lat) in degrees on a resDeg equirectangular grid
// (pqx's sky_counts): longitude wrapped to [0, 360), latitudes outside
// [-90, 90] and non-finite positions left out; the last row and column also
// take 360 and 90.
func (d *dataset) SkyCounts(ctx context.Context, v View, lon, lat string, resDeg float64, s Sample) (Grid2D, error) {
	if !(resDeg > 0) || math.IsInf(resDeg, 0) {
		return Grid2D{}, errors.New("the sky map's resolution must be a positive number of degrees")
	}
	fl, fa := math.RoundToEven(360/resDeg), math.RoundToEven(180/resDeg)
	if fl*fa > MaxCells {
		return Grid2D{}, fmt.Errorf("a resolution of %s° makes %.3g cells (at most %d)", pyRepr(resDeg), fl*fa, MaxCells)
	}
	nlon, nlat := int(fl), int(fa)
	if nlon < 1 || nlat < 1 {
		return Grid2D{}, fmt.Errorf("a resolution of %s° leaves no cells", pyRepr(resDeg))
	}
	if err := d.prepare(ctx, v); err != nil {
		return Grid2D{}, err
	}
	a, err := d.colRef(v, lon)
	if err != nil {
		return Grid2D{}, err
	}
	dd, err := d.colRef(v, lat)
	if err != nil {
		return Grid2D{}, err
	}
	base, err := d.relationSQL(v, []string{lon, lat}, s)
	if err != nil {
		return Grid2D{}, err
	}
	rel := "(SELECT CAST(" + a + " AS DOUBLE) AS a, CAST(" + dd + " AS DOUBLE) AS d FROM (\n" + base + "\n)) " +
		"WHERE isfinite(a) AND isfinite(d) AND d BETWEEN -90 AND 90"
	r := pyRepr(resDeg)
	sql := fmt.Sprintf("SELECT least(floor((((a %% 360) + 360) %% 360) / %s), %d)::INT AS i, "+
		"least(floor((d + 90) / %s), %d)::INT AS j, count(*) AS n FROM %s GROUP BY i, j", r, nlon-1, r, nlat-1, rel)
	grid := newGrid(nlat, nlon)
	err = d.query(ctx, sql, func(rec arrow.RecordBatch) error {
		for k := range int(rec.NumRows()) {
			i, ok1 := int64At(rec.Column(0), k)
			j, ok2 := int64At(rec.Column(1), k)
			n, ok3 := int64At(rec.Column(2), k)
			if ok1 && ok2 && ok3 && i >= 0 && i < int64(nlon) && j >= 0 && j < int64(nlat) {
				grid[j][i] = n
			}
		}
		return nil
	})
	if err != nil {
		return Grid2D{}, err
	}
	return Grid2D{Counts: grid, X: [2]float64{0, 360}, Y: [2]float64{-90, 90}}, nil
}

// XYCounts is a 2-D histogram of numeric columns x and y of view v on an
// nx × ny grid (pqx's xy_counts) over xlim and ylim, or where nil, the
// column's 0.1–99.9% quantiles (its full range if they coincide). Cells are
// closed on the left and bottom; the limits' upper ends are left out.
func (d *dataset) XYCounts(ctx context.Context, v View, x, y string, nx, ny int, s Sample, xlim, ylim *[2]float64) (Grid2D, error) {
	if nx < 1 || ny < 1 {
		return Grid2D{}, fmt.Errorf("a %d × %d grid has no cells", nx, ny)
	}
	if float64(nx)*float64(ny) > MaxCells {
		return Grid2D{}, fmt.Errorf("a %d × %d grid has too many cells (at most %d)", nx, ny, MaxCells)
	}
	for _, l := range []*[2]float64{xlim, ylim} {
		if l != nil {
			if err := finite("plot's limits", l[0], l[1]); err != nil {
				return Grid2D{}, err
			}
		}
	}
	if err := d.prepare(ctx, v); err != nil {
		return Grid2D{}, err
	}
	qx, err := d.colRef(v, x)
	if err != nil {
		return Grid2D{}, err
	}
	qy, err := d.colRef(v, y)
	if err != nil {
		return Grid2D{}, err
	}
	base, err := d.relationSQL(v, []string{x, y}, s)
	if err != nil {
		return Grid2D{}, err
	}
	rel := "(SELECT CAST(" + qx + " AS DOUBLE) AS x, CAST(" + qy + " AS DOUBLE) AS y FROM (\n" + base + "\n)) " +
		"WHERE isfinite(x) AND isfinite(y)"
	if xlim == nil || ylim == nil {
		var r [8]float64
		var have bool
		err := d.query(ctx, "SELECT approx_quantile(x, 0.001), approx_quantile(x, 0.999), "+
			"approx_quantile(y, 0.001), approx_quantile(y, 0.999), min(x), max(x), min(y), max(y) FROM "+rel,
			func(rec arrow.RecordBatch) error {
				if rec.NumRows() == 0 || rec.NumCols() != 8 {
					return nil
				}
				have = true
				for k := range r {
					f, ok := float64At(rec.Column(k), 0)
					if !ok {
						have = false
					}
					r[k] = f
				}
				return nil
			})
		if err != nil {
			return Grid2D{}, err
		}
		if !have {
			return Grid2D{Counts: newGrid(ny, nx), X: [2]float64{0, 1}, Y: [2]float64{0, 1}}, nil
		}
		x0, x1 := r[0], r[1]
		if !(r[1] > r[0]) {
			x0, x1 = r[4], r[5]
		}
		y0, y1 := r[2], r[3]
		if !(r[3] > r[2]) {
			y0, y1 = r[6], r[7]
		}
		if xlim == nil {
			x0, x1 = widen(x0, x1)
			xlim = &[2]float64{x0, x1}
		}
		if ylim == nil {
			y0, y1 = widen(y0, y1)
			ylim = &[2]float64{y0, y1}
		}
	}
	x0, x1, y0, y1 := xlim[0], xlim[1], ylim[0], ylim[1]
	if !(x1 > x0) || !(y1 > y0) { // given limits that hold nothing (as in pqx: no cells match)
		return Grid2D{Counts: newGrid(ny, nx), X: [2]float64{x0, x1}, Y: [2]float64{y0, y1}}, nil
	}
	bx, err := newBinning(x0, x1, nx)
	if err != nil {
		return Grid2D{}, err
	}
	by, err := newBinning(y0, y1, ny)
	if err != nil {
		return Grid2D{}, err
	}
	sql := fmt.Sprintf("SELECT %s::INT AS i, %s::INT AS j, count(*) AS n "+
		"FROM %s AND x >= %s AND x < %s AND y >= %s AND y < %s GROUP BY i, j",
		bx.sql("x"), by.sql("y"), rel, pyRepr(x0), pyRepr(x1), pyRepr(y0), pyRepr(y1))
	grid := newGrid(ny, nx)
	err = d.query(ctx, sql, func(rec arrow.RecordBatch) error {
		for k := range int(rec.NumRows()) {
			i, ok1 := int64At(rec.Column(0), k)
			j, ok2 := int64At(rec.Column(1), k)
			n, ok3 := int64At(rec.Column(2), k)
			if ok1 && ok2 && ok3 {
				i = min(max(i, 0), int64(nx-1))
				j = min(max(j, 0), int64(ny-1))
				grid[j][i] += n
			}
		}
		return nil
	})
	if err != nil {
		return Grid2D{}, err
	}
	return Grid2D{Counts: grid, X: [2]float64{x0, x1}, Y: [2]float64{y0, y1}}, nil
}

// MaxCells bounds the bins of a histogram and the cells of a 2-D grid.
const MaxCells = 16 << 20

// widen is lo and hi, or when the range is empty, lo to lo + 1 (pqx's);
// for an lo so large that adding 1 changes nothing, a little more than lo,
// or if that overflows (lo is near the largest float64), a little less.
func widen(lo, hi float64) (float64, float64) {
	if hi > lo {
		return lo, hi
	}
	if lo+1 > lo {
		return lo, lo + 1
	}
	if up := lo + math.Abs(lo)*0x1p-40; !math.IsInf(up, 0) {
		return lo, up
	}
	return lo - math.Abs(lo)*0x1p-40, lo
}

// binning is how SQL numbers the n equal bins from lo to hi: as pqx does,
// floor((v - lo) / w), unless the range is wider than a float64 holds (as
// from -1.7e308 to 1.7e308); then in halves, floor((v/2 - lo/2) / (w/2)).
type binning struct {
	lo, w float64 // w is half the width for a halved binning
	half  bool
}

func newBinning(lo, hi float64, n int) (binning, error) {
	b := binning{lo: lo, w: (hi - lo) / float64(n)}
	if math.IsInf(hi-lo, 0) {
		b = binning{lo: lo, w: (hi*0.5 - lo*0.5) / float64(n), half: true}
	}
	if !(b.w > 0) || math.IsInf(b.w, 0) {
		return binning{}, fmt.Errorf("can't make %d bins from %s to %s", n, pyRepr(lo), pyRepr(hi))
	}
	return b, nil
}

// sql is the bin of v (a SQL expression), before clipping.
func (b binning) sql(v string) string {
	if b.half {
		return fmt.Sprintf("floor((%s * 0.5 - %s) / %s)", v, pyRepr(b.lo*0.5), pyRepr(b.w))
	}
	return fmt.Sprintf("floor((%s - %s) / %s)", v, pyRepr(b.lo), pyRepr(b.w))
}

// edge is the lower edge of bin i (the upper edge of the last for i = n).
// The conversions keep the compiler from fusing the multiply and add (it
// does on arm64), so the edges are Python's to the bit on every platform.
func (b binning) edge(i int) float64 {
	if b.half {
		return (b.lo*0.5 + float64(float64(i)*b.w)) * 2
	}
	return b.lo + float64(float64(i)*b.w)
}

func newGrid(rows, cols int) [][]int64 {
	cells := make([]int64, rows*cols)
	g := make([][]int64, rows)
	for i := range g {
		g[i] = cells[i*cols : (i+1)*cols : (i+1)*cols]
	}
	return g
}
