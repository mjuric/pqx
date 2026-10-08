package cells

import (
	"encoding/hex"
	"math"
	"math/rand/v2"
	"strings"
	"testing"
	"time"

	"github.com/mjuric/pqx/go/internal/data"
	"github.com/mjuric/pqx/go/internal/fmtx"
	"github.com/mjuric/pqx/go/internal/golden"
)

// toValue is a golden typed value as the data layer holds it.
func toValue(t testing.TB, g golden.Value) data.Value {
	t.Helper()
	switch g.Kind {
	case golden.KindNull:
		return nil
	case golden.KindInt:
		return g.Int.Int64()
	case golden.KindUint:
		return g.Int.Uint64()
	case golden.KindF64:
		return g.Float
	case golden.KindF32:
		return g.Float32
	case golden.KindBool:
		return g.Bool
	case golden.KindStr:
		return g.Str
	case golden.KindBytes:
		return g.Bytes
	case golden.KindTS:
		units := map[string]time.Duration{"s": time.Second, "ms": time.Millisecond, "us": time.Microsecond, "ns": time.Nanosecond}
		return data.Timestamp{T: g.Time.UTC(), Zoned: g.TZ != "", Unit: units[g.Unit]}
	case golden.KindDate:
		return data.Date(g.Time.Unix() / 86400)
	case golden.KindTime:
		return data.TimeOfDay(g.Nanos)
	case golden.KindDur:
		return data.Duration(g.Int.Int64())
	case golden.KindDec:
		return data.Decimal{Unscaled: g.Int, Scale: int32(g.Scale), Precision: int32(g.Precision)}
	case golden.KindUUID:
		b, err := hex.DecodeString(strings.ReplaceAll(g.Str, "-", ""))
		if err != nil || len(b) != 16 {
			t.Fatalf("bad uuid %q", g.Str)
		}
		var u data.UUID
		copy(u[:], b)
		return u
	case golden.KindList:
		l := data.List{}
		for _, x := range g.List {
			l = append(l, toValue(t, x))
		}
		return l
	case golden.KindStruct:
		s := data.Struct{}
		for _, f := range g.Fields {
			s = append(s, data.Field{Name: f.Name, Value: toValue(t, f.Value)})
		}
		return s
	case golden.KindMap:
		m := data.Map{}
		for _, e := range g.Entries {
			m = append(m, data.KV{Key: toValue(t, e.Key), Value: toValue(t, e.Value)})
		}
		return m
	}
	t.Fatalf("unknown golden kind %q", g.Kind)
	return nil
}

func TestGoldenTextWidth(t *testing.T) {
	f := golden.Load(t, "cells.json")
	for _, r := range f.Section(t, "text_width") {
		s := r.String(t, "s")
		if got, want := Width(s), r.Int(t, "width"); got != want {
			t.Errorf("%s: Width(%q) = %d, want %d", r.ID(), s, got, want)
		}
	}
	for _, r := range f.Section(t, "text_width_formatted") {
		gv := r.Value(t, "v")
		raw := r.Bool(t, "raw")
		text := fmtx.Format(toValue(t, gv), fmtx.Kind(r.String(t, "kind")), fmtx.Opts{Raw: raw, Width: fmtx.DefaultWidth})
		if gv.Kind == golden.KindF32 && raw {
			continue // the shortest float32 text, not Python's widened repr (see fmtx)
		}
		if want := r.String(t, "text"); text != want {
			t.Errorf("%s: text %q, want %q", r.ID(), text, want)
		}
		if got, want := Width(text), r.Int(t, "width"); got != want {
			t.Errorf("%s: Width(%q) = %d, want %d", r.ID(), text, got, want)
		}
	}
}

func TestGoldenWidestCandidates(t *testing.T) {
	for _, r := range golden.Load(t, "cells.json").Section(t, "widest_candidates") {
		var gvals []golden.Value
		r.Decode(t, "values", &gvals)
		vals := make([]data.Value, len(gvals))
		for i, g := range gvals {
			vals[i] = toValue(t, g)
		}
		got := WidestCandidates(vals, fmtx.Kind(r.String(t, "kind")), r.Bool(t, "raw"), r.Int(t, "k"))
		if r.IsNull("out") {
			if got != nil {
				t.Errorf("%s: got %v, want nil", r.ID(), got)
			}
			continue
		}
		var gwant []golden.Value
		r.Decode(t, "out", &gwant)
		want := make([]data.Value, len(gwant))
		for i, g := range gwant {
			want[i] = toValue(t, g)
		}
		if got == nil || !sameValues(got, want) {
			t.Errorf("%s: got %v, want %v", r.ID(), got, want)
		}
	}
}

func sameValues(a, b []data.Value) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if fmtx.Format(a[i], fmtx.KindStr, fmtx.Opts{Raw: true}) != fmtx.Format(b[i], fmtx.KindStr, fmtx.Opts{Raw: true}) {
			return false
		}
	}
	return true
}

// test_cells.py::test_widest_candidates_find_the_widest_number
func TestWidestCandidatesFindTheWidestNumber(t *testing.T) {
	rng := rand.New(rand.NewPCG(3, 3))
	exact, total := 0, 0
	for _, k := range []fmtx.Kind{fmtx.KindFloat, fmtx.KindMag, fmtx.KindInt, fmtx.KindFloat32} {
		for _, scale := range []float64{1e-4, 1e-2, 1, 1e3, 1e8} {
			for _, raw := range []bool{false, true} {
				for range 10 {
					vals := make([]data.Value, 300)
					for i := range vals {
						x := rng.NormFloat64() * scale
						if k == fmtx.KindInt {
							vals[i] = int64(x)
						} else {
							vals[i] = x
						}
					}
					vals[5] = nil
					if k != fmtx.KindInt {
						vals[7] = math.NaN()
					} else {
						vals[7] = nil
					}
					o := fmtx.Opts{Raw: raw, Width: fmtx.DefaultWidth}
					widest, got := 0, 0
					for _, v := range vals {
						widest = max(widest, Width(fmtx.Format(v, k, o)))
					}
					for _, v := range WidestCandidates(vals, k, raw, 3) {
						got = max(got, Width(fmtx.Format(v, k, o)))
					}
					if got < widest-2 || got > widest {
						t.Errorf("%s scale %g raw %v: widest %d, candidates %d", k, scale, raw, widest, got)
					}
					if got == widest {
						exact++
					}
					total++
				}
			}
		}
	}
	if float64(exact)/float64(total) <= 0.98 {
		t.Errorf("exact %d of %d", exact, total)
	}
	eq := func(got []data.Value, want ...data.Value) {
		t.Helper()
		if !sameValues(got, want) {
			t.Errorf("got %v, want %v", got, want)
		}
	}
	eq(WidestCandidates([]data.Value{"a", "abcd", nil, "ab"}, fmtx.KindStr, false, 3), "abcd", "ab", "a")
	eq(WidestCandidates([]data.Value{"abcd", "日本語"}, fmtx.KindStr, false, 1), "日本語")
	if WidestCandidates([]data.Value{[]byte("x")}, fmtx.KindBinary, false, 3) != nil {
		t.Error("binary guessed")
	}
	if got := WidestCandidates([]data.Value{nil, math.NaN()}, fmtx.KindFloat, false, 3); got == nil || len(got) != 0 {
		t.Errorf("got %v", got)
	}
	if WidestCandidates([]data.Value{1.5, "x"}, fmtx.KindFloat, false, 3) != nil {
		t.Error("mixed guessed")
	}
}

func TestWidthTabsAndLines(t *testing.T) {
	for s, w := range map[string]int{"": 0, "abc": 3, "a\tb": 9, "ab\n日本語\nx": 6, "\x1b[31m": 4, "é": 1,
		"👨‍👩‍👧": 2, "❤️": 2, "a\r\tb": 10, "\xff": 1} {
		if got := Width(s); got != w {
			t.Errorf("Width(%q) = %d, want %d", s, got, w)
		}
	}
}
