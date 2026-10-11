// Package cells is the port of Python pqx's cells.py helpers the grid
// needs: terminal widths of text and the values most likely to be the
// widest when formatted. Part of the contract (docs/design/go-port.md).
//
// Widths follow Rich's cell_len, which Python pqx measures with, from the
// same table (table.go, generated from Rich's by gen_table.py).
package cells

import (
	"cmp"
	"math"
	"math/big"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/mjuric/pqx/go/internal/data"
	"github.com/mjuric/pqx/go/internal/fmtx"
)

// Placeholder is the text of a cell not loaded yet; FailedMark of one that
// couldn't be loaded.
const (
	Placeholder = "…"
	FailedMark  = "✗"
)

// Width is the number of terminal cells s takes (East Asian wide
// characters 2, combining marks 0; cells.text_width): its widest line, with
// tabs expanded to 8 columns.
func Width(s string) int {
	ascii := true
	for i := 0; i < len(s); i++ {
		if c := s[i]; c < 0x20 || c > 0x7E {
			ascii = false
			break
		}
	}
	if ascii {
		return len(s)
	}
	if strings.IndexByte(s, '\t') >= 0 {
		s = expandTabs(s, 8)
	}
	if strings.IndexByte(s, '\n') < 0 {
		return cellLen(s)
	}
	w := 0
	for _, line := range strings.Split(s, "\n") {
		w = max(w, cellLen(line))
	}
	return w
}

// expandTabs is Python's str.expandtabs: columns count code points and
// restart after \n and \r.
func expandTabs(s string, size int) string {
	var b strings.Builder
	col := 0
	for _, r := range s {
		switch r {
		case '\t':
			n := size - col%size
			b.WriteString(strings.Repeat(" ", n))
			col += n
		case '\n', '\r':
			b.WriteRune(r)
			col = 0
		default:
			b.WriteRune(r)
			col++
		}
	}
	return b.String()
}

// charWidth is Rich's get_character_cell_size.
func charWidth(r rune) int {
	if (r != 0 && r < 32) || (0x7F <= r && r < 0xA0) {
		return 0
	}
	if r > widthTable[len(widthTable)-1].hi {
		return 1
	}
	lo, hi := 0, len(widthTable)-1
	for lo <= hi {
		m := (lo + hi) >> 1
		e := widthTable[m]
		switch {
		case r < e.lo:
			hi = m - 1
		case r > e.hi:
			lo = m + 1
		default:
			return int(e.w)
		}
	}
	return 1
}

// cellLen is Rich's cell_len: the sum of the characters' widths, except
// that a zero-width joiner hides the character after it and a VS16 widens
// the character before it if that has a wide emoji form.
func cellLen(s string) int {
	if !strings.ContainsRune(s, 0x200D) && !strings.ContainsRune(s, 0xFE0F) {
		w := 0
		for _, r := range s {
			w += charWidth(r)
		}
		return w
	}
	rs := []rune(s)
	total := 0
	var last rune
	haveLast := false
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		switch r {
		case 0x200D:
			i++ // the joiner and the character it joins
		case 0xFE0F:
			if haveLast {
				if narrowToWide[last] {
					total++
				}
				haveLast = false
			}
		default:
			if w := charWidth(r); w != 0 {
				last, haveLast = r, true
				total += w
			}
		}
	}
	return total
}

// guessable are the kinds whose widest cell WidestCandidates can pick out.
func guessable(k fmtx.Kind) bool {
	switch k {
	case fmtx.KindInt, fmtx.KindFloat, fmtx.KindFloat32, fmtx.KindMJD, fmtx.KindAngle, fmtx.KindMag,
		fmtx.KindFlux, fmtx.KindErr, fmtx.KindStr, fmtx.KindTime:
		return true
	}
	return false
}

// WidestCandidates are the few values of vals whose text is likely the
// widest for kind (cells.widest_candidates: numbers' extremes and the values
// nearest zero; the longest strings), or nil if kind can't be guessed this
// way and every value must be formatted.
func WidestCandidates(vals []data.Value, k fmtx.Kind, raw bool, n int) []data.Value {
	if !guessable(k) {
		return nil
	}
	if k == fmtx.KindStr || k == fmtx.KindTime {
		// the longest few first (cheap), then by cell width: a wide character counts twice
		type cand struct {
			v    data.Value
			s    string
			l, w int
		}
		var cs []cand
		for _, v := range vals {
			if v != nil {
				s := pyStr(v)
				cs = append(cs, cand{v: v, s: s, l: utf8.RuneCountInString(s)})
			}
		}
		slices.SortStableFunc(cs, func(a, b cand) int { return cmp.Compare(b.l, a.l) })
		cs = cs[:min(len(cs), 4*n)]
		for i := range cs {
			cs[i].w = cellLen(cs[i].s)
		}
		slices.SortStableFunc(cs, func(a, b cand) int { return cmp.Compare(b.w, a.w) })
		out := make([]data.Value, 0, min(n, len(cs)))
		for _, c := range cs[:min(n, len(cs))] {
			out = append(out, c.v)
		}
		return out
	}
	nums := make([]data.Value, 0, len(vals))
	for _, v := range vals {
		if v == nil {
			continue
		}
		fin, ok := finite(v)
		if !ok {
			return nil // not plain numbers after all
		}
		if fin {
			nums = append(nums, v)
		}
	}
	if raw {
		if len(nums) == 0 {
			return []data.Value{}
		}
		best, bl := nums[0], -1
		for _, v := range nums {
			if l := utf8.RuneCountInString(reprText(v)); l > bl {
				best, bl = v, l
			}
		}
		return []data.Value{best}
	}
	slices.SortStableFunc(nums, compareNum)
	if len(nums) <= 4*n {
		return nums
	}
	neg := sort0(nums, func(s int) bool { return s < 0 })  // bisect_left(vals, 0)
	pos := sort0(nums, func(s int) bool { return s <= 0 }) // bisect_right(vals, 0)
	out := append([]data.Value{}, nums[:n]...)
	out = append(out, nums[max(0, neg-n):neg]...)
	out = append(out, nums[pos:min(len(nums), pos+n)]...)
	return append(out, nums[len(nums)-n:]...)
}

// sort0 is the number of leading values (of sorted nums) whose sign passes.
func sort0(nums []data.Value, below func(int) bool) int {
	lo, hi := 0, len(nums)
	for lo < hi {
		m := (lo + hi) / 2
		if below(sign(nums[m])) {
			lo = m + 1
		} else {
			hi = m
		}
	}
	return lo
}

// finite reports whether v is a finite number (ok false: not a number).
func finite(v data.Value) (fin, ok bool) {
	switch x := v.(type) {
	case int64, uint64, bool, data.Decimal:
		return true, true
	case float64:
		return !math.IsNaN(x) && !math.IsInf(x, 0), true
	case float32:
		f := float64(x)
		return !math.IsNaN(f) && !math.IsInf(f, 0), true
	}
	return false, false
}

// rat is a number exactly.
func rat(v data.Value) *big.Rat {
	switch x := v.(type) {
	case int64:
		return new(big.Rat).SetInt64(x)
	case uint64:
		return new(big.Rat).SetInt(new(big.Int).SetUint64(x))
	case bool:
		if x {
			return big.NewRat(1, 1)
		}
		return new(big.Rat)
	case float64:
		return new(big.Rat).SetFloat64(x)
	case float32:
		return new(big.Rat).SetFloat64(float64(x))
	case data.Decimal:
		r := new(big.Rat)
		if x.Unscaled != nil {
			r.SetInt(x.Unscaled)
		}
		e := int64(x.Scale)
		if e < 0 {
			e = -e
		}
		p := new(big.Rat).SetInt(new(big.Int).Exp(big.NewInt(10), big.NewInt(e), nil))
		if x.Scale >= 0 {
			return r.Quo(r, p)
		}
		return r.Mul(r, p)
	}
	return new(big.Rat)
}

// compareNum orders numbers exactly (Python compares int and float exactly).
func compareNum(a, b data.Value) int {
	switch x := a.(type) {
	case float64:
		if y, ok := b.(float64); ok {
			return cmp.Compare(x, y)
		}
	case int64:
		if y, ok := b.(int64); ok {
			return cmp.Compare(x, y)
		}
	}
	return rat(a).Cmp(rat(b))
}

func sign(v data.Value) int {
	switch x := v.(type) {
	case float64:
		return cmp.Compare(x, 0)
	case int64:
		return cmp.Compare(x, 0)
	}
	return rat(v).Sign()
}

// reprText is the raw text of a number (Python's repr, but the float32
// shortest text the Go port shows raw).
func reprText(v data.Value) string {
	switch x := v.(type) {
	case bool:
		if x {
			return "True"
		}
		return "False"
	case data.Decimal:
		return "Decimal('" + fmtx.Format(v, fmtx.KindStr, fmtx.Opts{Raw: true, Unsafe: true}) + "')"
	}
	return fmtx.Format(v, fmtx.KindFloat, fmtx.Opts{Raw: true, Unsafe: true})
}

// pyStr is str(v) as widest_candidates measures it.
func pyStr(v data.Value) string {
	switch x := v.(type) {
	case string:
		return x
	case data.Timestamp:
		// str(datetime) keeps "+00:00", where the cell shows "Z"
		s := fmtx.Format(v, fmtx.KindTime, fmtx.Opts{Raw: true, Unsafe: true})
		if x.Zoned {
			s = strings.TrimSuffix(s, "Z") + "+00:00"
		}
		return s
	}
	return fmtx.Format(v, fmtx.KindStr, fmtx.Opts{Raw: true, Unsafe: true})
}
