package data

import (
	"math"
	"path/filepath"
	"testing"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/float16"
	"github.com/apache/arrow-go/v18/arrow/memory"
)

// halfRef is half-precision bits h as a float64, from the definition.
func halfRef(h uint16) float64 {
	sign := 1.0
	if h&0x8000 != 0 {
		sign = -1
	}
	exp := int(h>>10) & 0x1f
	frac := float64(h & 0x3ff)
	switch exp {
	case 0x1f:
		if frac != 0 {
			return math.NaN()
		}
		return sign * math.Inf(1)
	case 0:
		return sign * frac * math.Pow(2, -24)
	}
	return sign * (1 + frac/1024) * math.Pow(2, float64(exp-15))
}

// Every half-precision value, subnormals, ±0, ±inf and NaN included.
func TestHalfToFloat32(t *testing.T) {
	for h := range 1 << 16 {
		got, want := halfToFloat32(uint16(h)), halfRef(uint16(h))
		switch {
		case math.IsNaN(want):
			if got == got {
				t.Fatalf("%04x: %v, want NaN", h, got)
			}
		case float64(got) != want || math.Signbit(float64(got)) != math.Signbit(want):
			t.Fatalf("%04x: %v, want %v", h, got, want)
		}
	}
}

// Both readers agree on float16 subnormals, zeros and specials.
func TestFloat16Values(t *testing.T) {
	sc := arrow.NewSchema([]arrow.Field{{Name: "h", Type: arrow.FixedWidthTypes.Float16, Nullable: true}}, nil)
	b := array.NewRecordBuilder(memory.DefaultAllocator, sc)
	bits := []uint16{0x0001, 0x03ff, 0x8001, 0x0000, 0x8000, 0x7c00, 0xfc00, 0x7e00, 0x3c00, 0x7bff, 0x0400}
	for _, h := range bits {
		b.Field(0).(*array.Float16Builder).Append(float16.FromBits(h))
	}
	b.Field(0).AppendNull()
	rec := b.NewRecordBatch()
	p := filepath.Join(t.TempDir(), "f16.parquet")
	writeTable(t, p, array.NewTableFromRecords(sc, []arrow.RecordBatch{rec}), 100, 1<<20)
	ds, err := Open(p, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer ds.Close()
	plain := mustFetch(t, ds, View{}, 0, 20, []string{"h"})
	sameWindow(t, "float16", plain, mustFetch(t, ds, View{Where: "true"}, 0, 20, []string{"h"}))
	if plain.Cols["h"][0] != float32(math.Pow(2, -24)) || plain.Cols["h"][len(bits)] != nil {
		t.Fatalf("%v", plain.Cols["h"])
	}
}
