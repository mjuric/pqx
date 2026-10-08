package data

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/memory"
	"github.com/apache/arrow-go/v18/parquet"
	"github.com/apache/arrow-go/v18/parquet/compress"
	"github.com/apache/arrow-go/v18/parquet/file"
	"github.com/apache/arrow-go/v18/parquet/pqarrow"
	"github.com/apache/arrow-go/v18/parquet/schema"
)

// duckExec runs a statement in a scratch DuckDB (through a dataset's pool).
func duckExec(t *testing.T, ds Dataset, q string) {
	t.Helper()
	if _, err := ds.(*dataset).db.Exec(q); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

// agree checks that every cell of the file reads the same in the plain view
// (arrow-go) and in a filtered view that keeps every row (DuckDB), and returns
// the plain view.
func agree(t *testing.T, path string) (Dataset, Window) {
	t.Helper()
	ds, err := Open(path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ds.Close() })
	cols := colNames(ds)
	plain, err := ds.Fetch(context.Background(), View{}, 0, int(ds.NumRows()), cols)
	if err != nil {
		t.Fatal(err)
	}
	all, err := ds.Fetch(context.Background(), View{Where: "true"}, 0, int(ds.NumRows()), cols)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cols {
		if !reflect.DeepEqual(plain.Cols[c], all.Cols[c]) {
			t.Errorf("%s: plain view %q, DuckDB %q", c, plain.Cols[c], all.Cols[c])
		}
	}
	return ds, plain
}

// Types that arrow-go reads differently from DuckDB still read the same.
func TestTypesAgree(t *testing.T) {
	dir := t.TempDir()
	mem := memory.DefaultAllocator
	ns := []int64{
		time.Date(2024, 1, 2, 3, 4, 5, 123456789, time.UTC).UnixNano(),
		time.Date(1969, 12, 31, 23, 59, 59, 999999999, time.UTC).UnixNano(),
		-1500, 0,
		time.Date(1900, 6, 1, 0, 0, 0, 1, time.UTC).UnixNano(),
	}
	tsCol := func(typ *arrow.TimestampType) arrow.Array {
		b := array.NewTimestampBuilder(mem, typ)
		for _, v := range ns {
			b.Append(arrow.Timestamp(v))
		}
		b.AppendNull()
		return b.NewArray()
	}
	write := func(name string, int96 bool, fields []arrow.Field, cols []arrow.Array) string {
		sc := arrow.NewSchema(fields, nil)
		rec := array.NewRecordBatch(sc, cols, int64(cols[0].Len()))
		tbl := array.NewTableFromRecords(sc, []arrow.RecordBatch{rec})
		p := filepath.Join(dir, name)
		f, err := os.Create(p)
		if err != nil {
			t.Fatal(err)
		}
		props := parquet.NewWriterProperties(parquet.WithCompression(compress.Codecs.Snappy))
		if err := pqarrow.WriteTable(tbl, f, 1<<20, props, pqarrow.NewArrowWriterProperties(pqarrow.WithDeprecatedInt96Timestamps(int96))); err != nil {
			t.Fatal(err)
		}
		return p
	}
	// INT96 timestamps (DuckDB: TIMESTAMP, microseconds)
	p := write("int96.parquet", true, []arrow.Field{{Name: "t96", Type: &arrow.TimestampType{Unit: arrow.Nanosecond}, Nullable: true}},
		[]arrow.Array{tsCol(&arrow.TimestampType{Unit: arrow.Nanosecond})})
	ds, _ := agree(t, p)
	if ty := ds.Columns()[0].Type; ty != "TIMESTAMP" && ty != "TIMESTAMP_NS" {
		t.Errorf("INT96: %s", ty)
	}
	// nanosecond timestamps with and without a zone
	p = write("ns.parquet", false, []arrow.Field{
		{Name: "tz", Type: &arrow.TimestampType{Unit: arrow.Nanosecond, TimeZone: "UTC"}, Nullable: true},
		{Name: "naive", Type: &arrow.TimestampType{Unit: arrow.Nanosecond}, Nullable: true},
		{Name: "tzus", Type: &arrow.TimestampType{Unit: arrow.Microsecond, TimeZone: "UTC"}, Nullable: true},
	}, []arrow.Array{
		tsCol(&arrow.TimestampType{Unit: arrow.Nanosecond, TimeZone: "UTC"}),
		tsCol(&arrow.TimestampType{Unit: arrow.Nanosecond}),
		tsCol(&arrow.TimestampType{Unit: arrow.Microsecond, TimeZone: "UTC"}),
	})
	ds, w := agree(t, p)
	t.Logf("ns types: %v; tz %q", ds.Columns(), w.Cols["tz"])

	// what DuckDB writes: ENUM, JSON, UUID, decimals, times, blobs, nested
	ds = fixtureDS(t)
	q := filepath.Join(dir, "duck.parquet")
	duckExec(t, ds, "CREATE TYPE mood AS ENUM ('sad', 'ok', 'happy')")
	duckExec(t, ds, `COPY (SELECT
		i,
		(['sad', 'ok', 'happy'])[i % 3 + 1]::mood AS e,
		CASE WHEN i % 4 = 0 THEN NULL ELSE ('{"a": ' || i || ', "s": "x\u001b"}')::JSON END AS j,
		uuid() AS u,
		(i * 1.25 - 3)::DECIMAL(9, 2) AS d9,
		(i * 1.25 - 3)::DECIMAL(18, 4) AS d18,
		(i * 1.25 - 3)::DECIMAL(38, 6) AS d38,
		(i::HUGEINT * 1000000000000000000) AS h,
		DATE '1999-12-31' + i::INTEGER AS dt,
		TIME '01:02:03.25' + INTERVAL (i) SECOND AS tm,
		TIMESTAMP_NS '1960-01-01 00:00:00.000000001' + INTERVAL (i) DAY AS tns,
		TIMESTAMPTZ '2001-02-03 04:05:06.789' + INTERVAL (i) MINUTE AS ttz,
		('ab' || chr(27) || i)::BLOB AS bl,
		[i, i + 1] AS l,
		{'x': i, 's': 'v' || i} AS st,
		i::UTINYINT AS u8,
		(i / 7)::FLOAT AS f
		FROM range(40) r(i)) TO '`+strings.ReplaceAll(q, "'", "''")+`' (FORMAT parquet)`)
	ds2, w := agree(t, q)
	types := map[string]string{}
	for _, c := range ds2.Columns() {
		types[c.Name] = c.Type
	}
	if types["e"] != "VARCHAR" || w.Cols["e"][1] != "ok" {
		t.Errorf("enum: %s %q", types["e"], w.Cols["e"][:3])
	}
	if types["j"] != "JSON" {
		t.Errorf("json: %s", types["j"])
	}

	// Parquet's ENUM and JSON logical types (arrow-go reads them as binary)
	p = filepath.Join(dir, "enum.parquet")
	writeLogicalStrings(t, p, map[string]schema.LogicalType{"e": schema.EnumLogicalType{}, "j": schema.JSONLogicalType{}},
		[]string{`"sad"`, `["ok"]`, `7`, `{"x": 1}`}) // (JSON for both: DuckDB checks it)
	ds3, w3 := agree(t, p)
	if w3.Cols["e"][1] != `["ok"]` || w3.Cols["j"][3] != `{"x": 1}` {
		t.Errorf("enum/json: %v %q", ds3.Columns(), w3.Cols)
	}
	if w.Cols["j"][1] != `{"a": 1, "s": "x\u001b"}` {
		t.Errorf("json: %s %q", types["j"], w.Cols["j"][:2])
	}
	t.Logf("DuckDB-written types: %v", types)
}

// writeLogicalStrings writes BYTE_ARRAY columns with the given logical types
// (one per name, sorted), each holding vals and then a NULL.
func writeLogicalStrings(t *testing.T, path string, cols map[string]schema.LogicalType, vals []string) {
	t.Helper()
	var names []string
	for n := range cols {
		names = append(names, n)
	}
	sort.Strings(names)
	var fields schema.FieldList
	for _, n := range names {
		node, err := schema.NewPrimitiveNodeLogical(n, parquet.Repetitions.Optional, cols[n], parquet.Types.ByteArray, -1, -1)
		if err != nil {
			t.Fatal(err)
		}
		fields = append(fields, node)
	}
	root, err := schema.NewGroupNode("schema", parquet.Repetitions.Required, fields, -1)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	w := file.NewParquetWriter(f, root)
	rg := w.AppendRowGroup()
	data := make([]parquet.ByteArray, len(vals))
	defs := make([]int16, len(vals)+1)
	for i, v := range vals {
		data[i] = parquet.ByteArray(v)
		defs[i] = 1
	}
	for range names {
		cw, err := rg.NextColumn()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := cw.(*file.ByteArrayColumnChunkWriter).WriteBatch(data, defs, nil); err != nil {
			t.Fatal(err)
		}
		cw.Close()
	}
	rg.Close()
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
}

func fixtureDS(t *testing.T) Dataset {
	_, ds := fixture(t)
	return ds
}

// pqx reads exactly the file: no columns from a hive-style path.
func TestHivePath(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "year=2024", "band=r")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "x.parquet")
	writeInts(t, p, 1, 2, 3)
	ds, err := Open(p, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer ds.Close()
	if len(ds.Columns()) != 1 {
		t.Fatalf("columns %v", ds.Columns())
	}
	if err := ds.CheckWhere("a > 1"); err != nil {
		t.Fatal(err)
	}
	if n, err := ds.Count(context.Background(), View{Where: "a > 1"}); err != nil || n != 2 {
		t.Fatalf("Count %d %v", n, err)
	}
	w, err := ds.Fetch(context.Background(), View{Where: "a > 1"}, 0, 10, []string{"a"})
	if err != nil || !reflect.DeepEqual(w.Cols["a"], []string{"2", "3"}) {
		t.Fatalf("%v %v", w.Cols, err)
	}
	if err := ds.CheckWhere("year = 2024"); err == nil {
		t.Fatal("a column from the path")
	}
}

// checkSelect refuses more than one statement whatever their order, and runs none.
func TestCheckSelectMultiStatement(t *testing.T) {
	path, ds := fixture(t)
	d := ds.(*dataset)
	pwn := filepath.Join(filepath.Dir(path), "pwn.csv")
	lit := quoteStr(pwn)
	for _, q := range []string{
		"SELECT 1; COPY (SELECT 1) TO " + lit,
		"COPY (SELECT 1) TO " + lit + "; SELECT 1",
		"COPY (SELECT 1) TO " + lit,
		"CREATE TABLE x AS SELECT 1; SELECT * FROM x",
	} {
		err := d.withConn(context.Background(), func(c *duckdbConn) error { return checkSelect(c, q) })
		if !errors.Is(err, errNotOneSelect) {
			t.Errorf("%q: %v", q, err)
		}
		var n int64 = -1
		err = d.query(context.Background(), q, func(rec arrow.RecordBatch) error { n = 0; return nil })
		if err == nil || n != -1 {
			t.Errorf("query(%q) ran: %v", q, err)
		}
	}
	if _, err := os.Stat(pwn); err == nil {
		t.Fatal("a COPY ran")
	}
	if err := d.withConn(context.Background(), func(c *duckdbConn) error { return checkSelect(c, "SELECT 1") }); err != nil {
		t.Fatal(err)
	}
}

// A plain-view read cancelled while it reads stops at its next read.
func TestCancelPlainMidRead(t *testing.T) {
	_, ds := fixture(t)
	var reads atomic.Int64
	beforeRead = func() { reads.Add(1); time.Sleep(20 * time.Millisecond) }
	defer func() { beforeRead = nil }()
	ReadParallel = 1
	defer func() { ReadParallel = defaultReadParallel }()
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)
	t0 := time.Now()
	_, err := ds.Fetch(ctx, View{}, 0, fixRows, colNames(ds))
	el := time.Since(t0)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if el > 200*time.Millisecond {
		t.Fatalf("took %v", el)
	}
	t.Logf("stopped after %v and %d reads", el, reads.Load())
	if r := reads.Load(); r > 6 {
		t.Fatalf("%d reads after the cancel", r)
	}
}

// A file with no rows.
func TestEmptyFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "empty.parquet")
	writeInts(t, p)
	ds, err := Open(p, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer ds.Close()
	ctx := context.Background()
	if ds.NumRows() != 0 || len(ds.Columns()) != 1 {
		t.Fatalf("%d rows %v", ds.NumRows(), ds.Columns())
	}
	for _, v := range []View{{}, {Where: "a > 0"}} {
		w, err := ds.Fetch(ctx, v, 0, 10, []string{"a"})
		if err != nil || w.Len != 0 || len(w.Cols["a"]) != 0 {
			t.Fatalf("%v: %+v %v", v, w, err)
		}
		if n, err := ds.Count(ctx, v); err != nil || n != 0 {
			t.Fatalf("%v: %d %v", v, n, err)
		}
	}
}

// Errors from arrow-go and the OS carry no control characters from the path.
func TestErrorsSanitized(t *testing.T) {
	p := filepath.Join(t.TempDir(), "bad\x1b]0;x\x07.parquet")
	os.WriteFile(p, []byte("PAR1 not really"), 0o600)
	_, err := Open(p, Options{})
	if err == nil || HasControls(err.Error()) || !strings.Contains(err.Error(), "␛") {
		t.Fatalf("%q", err)
	}
	_, err = Open(p+".missing", Options{})
	if err == nil || HasControls(err.Error()) || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("%q", err)
	}
}

// A filter that blocks DuckDB's prepare doesn't block CheckWhere for ever.
func TestCheckWhereTimeout(t *testing.T) {
	_, ds := fixture(t)
	release := make(chan struct{})
	defer close(release)
	// Stand-in for a blocking table function: hold every DuckDB connection.
	d := ds.(*dataset)
	d.db.SetMaxOpenConns(1)
	conn, err := d.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	go func() { <-release; conn.Close() }()
	old := CheckWhereTimeout
	CheckWhereTimeout = 200 * time.Millisecond
	defer func() { CheckWhereTimeout = old }()
	t0 := time.Now()
	err = ds.CheckWhere("id > 3")
	if err == nil || !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "check the filter") {
		t.Fatalf("err = %v", err)
	}
	if el := time.Since(t0); el > time.Second {
		t.Fatalf("took %v", el)
	}
}
