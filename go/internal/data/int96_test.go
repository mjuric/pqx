package data

import (
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/apache/arrow-go/v18/parquet"
	"github.com/apache/arrow-go/v18/parquet/file"
	"github.com/apache/arrow-go/v18/parquet/schema"
)

// writeInt96 writes one INT96 column t holding the given days since the
// Unix epoch (at 01:00) and then a NULL.
func writeInt96(t *testing.T, path string, days ...int64) {
	t.Helper()
	node, err := schema.NewPrimitiveNode("t", parquet.Repetitions.Optional, parquet.Types.Int96, -1, -1)
	if err != nil {
		t.Fatal(err)
	}
	root, _ := schema.NewGroupNode("schema", parquet.Repetitions.Required, schema.FieldList{node}, -1)
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	w := file.NewParquetWriter(f, root)
	rg, err := w.AppendRowGroupChecked()
	if err != nil {
		t.Fatal(err)
	}
	cw, _ := rg.NextColumn()
	vals := make([]parquet.Int96, len(days))
	defs := make([]int16, len(days)+1)
	for i, d := range days {
		binary.LittleEndian.PutUint64(vals[i][:8], uint64(time.Hour))
		binary.LittleEndian.PutUint32(vals[i][8:], uint32(d+2440588))
		defs[i] = 1
	}
	if _, err := cw.(*file.Int96ColumnChunkWriter).WriteBatch(vals, defs, nil); err != nil {
		t.Fatal(err)
	}
	cw.Close()
	rg.Close()
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
}

// INT96 timestamps outside the nanosecond range (years past 2262): arrow-go
// refuses them (no silent overflow), so DuckDB reads the column; without
// DuckDB the column is Failed, not wrong.
func TestInt96OutOfRange(t *testing.T) {
	p := filepath.Join(t.TempDir(), "int96.parquet")
	far := int64(200_000 * 365)
	writeInt96(t, p, 0, 1, far, -far)
	ds, err := Open(p, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer ds.Close()
	d := ds.(*dataset)
	plain := mustFetch(t, d, View{}, 0, 5, []string{"t"})
	sameWindow(t, "int96", plain, mustFetch(t, d, View{Where: "true"}, 0, 5, []string{"t"}))
	if ts, ok := plain.Cols["t"][2].(Timestamp); !ok || ts.T.Year() < 100_000 {
		t.Fatalf("%#v", plain.Cols["t"][2])
	}
	// as if DuckDB couldn't read the file
	d.bindErr = errors.New("DuckDB can't read it")
	for i := range d.direct {
		d.direct[i] = ValueAt
	}
	w := mustFetch(t, d, View{}, 0, 5, []string{"t"})
	if w.Failed["t"] == nil || w.Cols["t"] != nil {
		t.Fatalf("%+v", w)
	}
	if w := mustFetch(t, d, View{}, 0, 2, []string{"t"}); len(w.Failed) > 0 || w.Cols["t"][1].(Timestamp).T.Unix() != 86400+3600 {
		t.Fatalf("in range: %+v", w)
	}
}
