package fmtx

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/apache/arrow-go/v18/arrow/memory"
	"github.com/apache/arrow-go/v18/parquet/file"
	"github.com/apache/arrow-go/v18/parquet/pqarrow"

	"github.com/mjuric/pqx/go/internal/data"
)

// TestRealData formats every cell of the first rows of the delivery files
// (PQX_REAL_DATA=<dir> to run; read-only) and reports the time per cell.
func TestRealData(t *testing.T) {
	dir := os.Getenv("PQX_REAL_DATA")
	if dir == "" {
		t.Skip("PQX_REAL_DATA not set")
	}
	for _, name := range []string{"SSSource.parquet", "mpc_orbits.parquet", "SSObject.parquet"} {
		pf, err := file.OpenParquetFile(dir+"/"+name, false)
		if err != nil {
			t.Fatal(err)
		}
		fr, err := pqarrow.NewFileReader(pf, pqarrow.ArrowReadProperties{BatchSize: 20000}, memory.DefaultAllocator)
		if err != nil {
			t.Fatal(err)
		}
		rr, err := fr.GetRecordReader(context.Background(), nil, []int{0})
		if err != nil {
			t.Fatal(err)
		}
		if !rr.Next() {
			t.Fatal("no rows")
		}
		rec := rr.RecordBatch()
		sc := rec.Schema()
		kinds := map[Kind]int{}
		var cells int
		var took time.Duration
		for c := 0; c < int(rec.NumCols()); c++ {
			f := sc.Field(c)
			unit, _ := f.Metadata.GetValue("unit")
			k := KindFor(f.Name, f.Type, unit)
			kinds[k]++
			col := rec.Column(c)
			vals := make([]data.Value, col.Len())
			for i := range vals {
				vals[i] = data.ValueAt(col, i)
			}
			o := Opts{Width: DefaultWidth}
			start := time.Now()
			for _, v := range vals {
				s := Format(v, k, o)
				if HasControls(s, true) {
					t.Errorf("%s %s: controls in %q", name, f.Name, s)
				}
			}
			took += time.Since(start)
			cells += len(vals)
			if c < 3 || k == KindMJD {
				t.Logf("%s %s (%s, %s): %q / %q", name, f.Name, ShortType(f.Type), k, Format(vals[0], k, o), Derived(f.Name, k, vals[0], unit))
			}
		}
		t.Logf("%s: %d columns %v, %d cells formatted in %v (%.0f ns/cell)", name, rec.NumCols(), kinds, cells, took,
			float64(took.Nanoseconds())/float64(cells))
		rr.Release()
		pf.Close()
	}
}
