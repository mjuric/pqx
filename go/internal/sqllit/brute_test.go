package sqllit

import (
	"context"
	"fmt"
	"math"
	"math/big"
	"math/rand/v2"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/decimal128"
	"github.com/apache/arrow-go/v18/arrow/memory"
	"github.com/apache/arrow-go/v18/parquet/pqarrow"

	"github.com/mjuric/pqx/go/internal/data"
)

// same reports whether two cells are the same value (NaN equal to NaN,
// decimals and times by value).
func same(a, b data.Value) bool {
	switch x := a.(type) {
	case nil:
		return b == nil
	case float64:
		y, ok := b.(float64)
		return ok && (x == y || (math.IsNaN(x) && math.IsNaN(y)))
	case float32:
		y, ok := b.(float32)
		return ok && (x == y || (x != x && y != y))
	case data.Decimal:
		y, ok := b.(data.Decimal)
		if !ok || x.Unscaled == nil || y.Unscaled == nil {
			return false
		}
		ten := func(s int32) *big.Int { return new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(s)), nil) }
		return new(big.Rat).SetFrac(x.Unscaled, ten(x.Scale)).Cmp(new(big.Rat).SetFrac(y.Unscaled, ten(y.Scale))) == 0
	case data.Timestamp:
		y, ok := b.(data.Timestamp)
		return ok && x.T.Equal(y.T)
	}
	return reflect.DeepEqual(a, b)
}

// edgeFile writes decimals of many precisions and scales (random values and
// the extremes, DECIMAL(38,38) among them) and timestamps and dates at the
// ends of their ranges, DuckDB's infinities included.
func edgeFile(t *testing.T) string {
	rng := rand.New(rand.NewPCG(3, 4))
	type dec struct{ p, s int32 }
	decs := []dec{{1, 0}, {4, 2}, {9, 9}, {18, 1}, {18, 18}, {19, 0}, {38, 0}, {38, 10}, {38, 37}, {38, 38}}
	var fields []arrow.Field
	for _, d := range decs {
		fields = append(fields, arrow.Field{Name: fmt.Sprintf("d%d_%d", d.p, d.s), Type: &arrow.Decimal128Type{Precision: d.p, Scale: d.s}, Nullable: true})
	}
	tsTypes := map[string]arrow.DataType{
		"ts_ns": &arrow.TimestampType{Unit: arrow.Nanosecond}, "ts_ns_utc": &arrow.TimestampType{Unit: arrow.Nanosecond, TimeZone: "UTC"},
		"ts_us": &arrow.TimestampType{Unit: arrow.Microsecond}, "ts_us_utc": &arrow.TimestampType{Unit: arrow.Microsecond, TimeZone: "UTC"},
	}
	tsNames := []string{"ts_ns", "ts_ns_utc", "ts_us", "ts_us_utc"}
	for _, n := range tsNames {
		fields = append(fields, arrow.Field{Name: n, Type: tsTypes[n], Nullable: true})
	}
	fields = append(fields, arrow.Field{Name: "day", Type: arrow.FixedWidthTypes.Date32, Nullable: true})
	sc := arrow.NewSchema(fields, nil)
	bld := array.NewRecordBuilder(memory.DefaultAllocator, sc)
	defer bld.Release()
	const rows = 24
	nsVals := []int64{math.MaxInt64, -math.MaxInt64, -1, 0, 1000, 1767323045678901234, -1767323045678901234, 1767323045678901000, -999}
	usVals := []int64{math.MaxInt64, -math.MaxInt64, time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC).UnixMicro(),
		time.Date(9999, 12, 31, 23, 59, 59, 999999000, time.UTC).UnixMicro(), -1, 0, 1767323045678901}
	days := []int32{math.MaxInt32, -math.MaxInt32, int32(time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC).Unix() / 86400),
		int32(time.Date(9999, 12, 31, 0, 0, 0, 0, time.UTC).Unix() / 86400), 0, -1}
	for i := 0; i < rows; i++ {
		for j, d := range decs {
			b := bld.Field(j).(*array.Decimal128Builder)
			var u *big.Int
			switch i % 4 {
			case 0: // the largest
				u = new(big.Int).Sub(new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(d.p)), nil), big.NewInt(1))
			case 1: // the smallest step
				u = big.NewInt(1)
			default:
				digits := make([]byte, 1+rng.IntN(int(d.p)))
				for k := range digits {
					digits[k] = byte('0' + rng.IntN(10))
				}
				u, _ = new(big.Int).SetString(string(digits), 10)
			}
			if rng.IntN(2) == 0 {
				u.Neg(u)
			}
			b.Append(decimal128.FromBigInt(u))
		}
		k := len(decs)
		for c, vals := range [][]int64{nsVals, nsVals, usVals, usVals} {
			bld.Field(k + c).(*array.TimestampBuilder).Append(arrow.Timestamp(vals[i%len(vals)]))
		}
		bld.Field(k + 4).(*array.Date32Builder).Append(arrow.Date32(days[i%len(days)]))
	}
	rec := bld.NewRecordBatch()
	defer rec.Release()
	path := filepath.Join(t.TempDir(), "edges.parquet")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	tbl := array.NewTableFromRecords(sc, []arrow.RecordBatch{rec})
	defer tbl.Release()
	if err := pqarrow.WriteTable(tbl, f, rows, nil, pqarrow.NewArrowWriterProperties(pqarrow.WithStoreSchema())); err != nil {
		t.Fatal(err)
	}
	return path
}

// Brute force: every value "=" can match, of every column of the fixtures
// and of edgeFile (as the plain view and a filtered view read them), counts
// exactly the rows holding it, and its row is in the view; a value it
// refuses is never one DuckDB could match silently wrong.
func TestEqualsMatchesExactlyItsRows(t *testing.T) {
	files, _ := filepath.Glob("../../testdata/fixtures/*.parquet")
	files = append(files, edgeFile(t))
	ctx := context.Background()
	for _, f := range files {
		t.Run(filepath.Base(f), func(t *testing.T) {
			t.Parallel()
			bruteFile(ctx, t, f)
		})
	}
}

func bruteFile(ctx context.Context, t *testing.T, f string) {
	ds, err := data.Open(f, data.Options{Threads: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer ds.Close()
	n := min(ds.NumRows(), 2000)
	numbered := true // (DuckDB numbers the rows of filtered views)
	for _, c := range ds.Columns() {
		if strings.EqualFold(c.Name, "file_row_number") {
			numbered = false
		}
	}
	for _, c := range ds.Columns() {
		for _, view := range []data.View{{}, {Where: "true"}} {
			// (the values as this view's reader gives them)
			all, err := ds.Fetch(ctx, view, 0, int(ds.NumRows()), []string{c.Name})
			if err != nil || all.Failed[c.Name] != nil {
				break // (a column no query can name: nulname's)
			}
			w, err := ds.Fetch(ctx, view, 0, int(n), []string{c.Name})
			if err != nil || w.Failed[c.Name] != nil {
				t.Errorf("%s %q: %v %v", filepath.Base(f), c.Name, err, w.Failed)
				continue
			}
			done := map[string]bool{}
			for i, v := range w.Cols[c.Name] {
				key := fmt.Sprintf("%T %#v", v, v)
				if done[key] || len(done) > 12 {
					continue
				}
				done[key] = true
				cond, ok := Equals(c.SQLName, v)
				if !ok {
					continue
				}
				want := int64(0)
				for _, u := range all.Cols[c.Name] {
					if same(v, u) {
						want++
					}
				}
				got, err := ds.Count(ctx, data.View{Where: cond})
				found, ferr := true, error(nil)
				if numbered {
					_, found, ferr = ds.FindRow(ctx, data.View{Where: cond}, w.FileRows[i])
				}
				if err != nil || got != want || !found || ferr != nil || HasControls(cond) {
					t.Errorf("%s %q [%s] %v: %s: %d rows, want %d (%v); found %v (%v)",
						filepath.Base(f), c.Name, c.Type, v, cond, got, want, err, found, ferr)
				}
			}
		}
	}
}
