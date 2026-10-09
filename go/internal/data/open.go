package data

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/parquet"
	"github.com/apache/arrow-go/v18/parquet/file"
	"github.com/apache/arrow-go/v18/parquet/metadata"
	"github.com/apache/arrow-go/v18/parquet/pqarrow"
	"github.com/apache/arrow-go/v18/parquet/schema"
	"github.com/duckdb/duckdb-go/v2"
)

// dataset is the Dataset of one Parquet file.
type dataset struct {
	path    string
	f       *os.File
	size    int64
	md      *metadata.FileMetaData
	cols    []Column
	byName  map[string]int // column name -> index (the first, for exact duplicates)
	leaves  [][]int        // Parquet leaf column indices of each top-level column
	rgRows  []int64
	rgStart []int64 // first file row of each row group, then the row count
	numRows int64

	db  *sql.DB
	src string // read_parquet(...) with file_row_number, for DuckDB queries
	// DuckDB binds the file (and caches its footer) on a background goroutine
	// started by Open; queries wait for it. duckNames are DuckDB's names for
	// the columns, which differ for names that are equal but for case.
	bound     chan struct{}
	bindErr   error
	duckNames []string
	duckTypes []string
	// conv converts the plain view's values of each column so they are
	// DuckDB's (see plainValueFunc); nil entries use ValueAt.
	conv      []func(arrow.Array, int) Value
	hasRowNum bool // the file has no column of its own named file_row_number
	closeOnce sync.Once

	an analysis // footer scan, encodings, totals and the view t (meta.go)
}

// Open opens a Parquet file: it parses the footer and schema once with
// arrow-go, and starts DuckDB binding the file in the background.
func Open(path string, opts Options) (Dataset, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, safeErr(err)
	}
	f, err := os.Open(abs)
	if err != nil {
		return nil, safeErr(err)
	}
	d, err := open(abs, f, opts)
	if err != nil {
		f.Close()
		return nil, err
	}
	return d, nil
}

func open(path string, f *os.File, opts Options) (_ *dataset, err error) {
	st, err := f.Stat()
	if err != nil {
		return nil, safeErr(err)
	}
	if st.IsDir() {
		return nil, safeErr(fmt.Errorf("%s is a directory", path))
	}
	d := &dataset{path: path, f: f, size: st.Size(), bound: make(chan struct{})}

	// DuckDB first, so its bind overlaps arrow-go's footer parse.
	dsn := ":memory:?TimeZone=UTC&enable_object_cache=true"
	if opts.Threads > 0 {
		dsn += "&threads=" + strconv.Itoa(opts.Threads)
	}
	connector, err := duckdb.NewConnector(dsn, nil)
	if err != nil {
		return nil, safeErr(err)
	}
	d.db = sql.OpenDB(connector)
	d.db.SetMaxIdleConns(4)
	go d.bind()
	defer func() {
		if err != nil {
			<-d.bound
			d.db.Close()
			err = safeErr(fmt.Errorf("%s: %w", path, err))
		}
	}()

	pf, err := file.NewParquetReader(f)
	if err != nil {
		return nil, err
	}
	d.md = pf.MetaData()
	sc, err := pqarrow.FromParquet(d.md.Schema, &pqarrow.ArrowReadProperties{}, d.md.KeyValueMetadata())
	if err != nil {
		return nil, err
	}
	d.byName = make(map[string]int, sc.NumFields())
	for i, fld := range sc.Fields() {
		c := Column{Name: fld.Name, Type: duckType(fld.Type), Arrow: fld.Type, Nullable: fld.Nullable, SQLName: fld.Name}
		c.Unit, c.Description = unitDesc(fld.Metadata)
		d.cols = append(d.cols, c)
		if _, dup := d.byName[fld.Name]; !dup {
			d.byName[fld.Name] = i
		}
	}
	root := d.md.Schema.Root()
	if root.NumFields() != len(d.cols) {
		return nil, fmt.Errorf("%d top-level columns in the Parquet schema, %d in Arrow's", root.NumFields(), len(d.cols))
	}
	leaf := 0
	for i := range root.NumFields() {
		k := countLeaves(root.Field(i))
		idx := make([]int, k)
		for j := range idx {
			idx[j] = leaf + j
		}
		d.leaves = append(d.leaves, idx)
		leaf += k
	}
	d.rgStart = []int64{0}
	for i := range d.md.NumRowGroups() {
		n := d.md.RowGroup(i).NumRows()
		d.rgRows = append(d.rgRows, n)
		d.rgStart = append(d.rgStart, d.rgStart[i]+n)
	}
	d.numRows = d.md.NumRows
	if got := d.rgStart[len(d.rgStart)-1]; got != d.numRows {
		return nil, fmt.Errorf("the row groups hold %d rows, the footer says %d", got, d.numRows)
	}
	// DuckDB refuses file_row_number=true on a file with a column of that name.
	d.hasRowNum = true
	for _, c := range d.cols {
		if strings.EqualFold(c.Name, "file_row_number") {
			d.hasRowNum = false
		}
	}
	d.src = readParquet(path, d.hasRowNum)

	// Wait for DuckDB's bind (it ran alongside the footer parse above), to
	// show DuckDB's types and to format the plain view's cells to match.
	// If DuckDB can't read the file, the plain view still works.
	<-d.bound
	if d.bindErr == nil && len(d.duckTypes) == len(d.cols) {
		d.conv = make([]func(arrow.Array, int) Value, len(d.cols))
		for i := range d.cols {
			d.cols[i].Type = d.duckTypes[i]
			d.cols[i].SQLName = d.duckNames[i]
			int96 := len(d.leaves[i]) == 1 && d.md.Schema.Column(d.leaves[i][0]).PhysicalType() == parquet.Types.Int96
			d.conv[i] = plainValueFunc(sc.Field(i).Type, d.duckTypes[i], int96)
		}
	}
	return d, nil
}

func countLeaves(n schema.Node) int {
	g, ok := n.(*schema.GroupNode)
	if !ok {
		return 1
	}
	k := 0
	for i := range g.NumFields() {
		k += countLeaves(g.Field(i))
	}
	return k
}

// readParquet is DuckDB's table function for the file at path: exactly that
// file (glob characters escaped), with no columns made up from a hive-style
// path (/year=2024/), and numbered rows if asked for.
func readParquet(path string, rowNumbers bool) string {
	opts := ", hive_partitioning=false"
	if rowNumbers {
		opts += ", file_row_number=true"
	}
	return "read_parquet(" + pathLiteral(path) + opts + ")"
}

// bind has DuckDB read the footer (into its cache), and name the columns
// and their types.
func (d *dataset) bind() {
	defer close(d.bound)
	rows, err := d.db.QueryContext(context.Background(), "SELECT column_name, column_type FROM (DESCRIBE SELECT * FROM "+readParquet(d.path, false)+")")
	if err != nil {
		d.bindErr = duckError(err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var name, typ string
		if err := rows.Scan(&name, &typ); err != nil {
			d.bindErr = duckError(err)
			return
		}
		d.duckNames = append(d.duckNames, name)
		d.duckTypes = append(d.duckTypes, typ)
	}
	if err := rows.Err(); err != nil {
		d.bindErr = duckError(err)
	}
}

// waitBound waits for bind, and returns its error, or ctx's.
func (d *dataset) waitBound(ctx context.Context) error {
	select {
	case <-d.bound:
	case <-ctx.Done():
		return ctx.Err()
	}
	if d.bindErr != nil {
		return d.bindErr
	}
	if len(d.duckNames) != len(d.cols) {
		return fmt.Errorf("DuckDB reads %d columns from this file, not %d", len(d.duckNames), len(d.cols))
	}
	return nil
}

func (d *dataset) Path() string       { return d.path }
func (d *dataset) NumRows() int64     { return d.numRows }
func (d *dataset) Columns() []Column  { return append([]Column(nil), d.cols...) }
func (d *dataset) RowGroups() []int64 { return append([]int64(nil), d.rgRows...) }

func (d *dataset) Close() error {
	var err error
	d.closeOnce.Do(func() {
		<-d.bound
		err = errors.Join(d.db.Close(), d.f.Close())
	})
	return err
}

// Fetch reads rows [start, start+n) of view v, for columns cols.
func (d *dataset) Fetch(ctx context.Context, v View, start int64, n int, cols []string) (Window, error) {
	if err := ctx.Err(); err != nil {
		return Window{}, err
	}
	idx, err := d.columnIndices(cols)
	if err != nil {
		return Window{}, err
	}
	start = max(start, 0)
	n = max(n, 0)
	if isPlain(v) {
		return d.fetchPlain(ctx, start, n, cols, idx)
	}
	if v.IsSQL() || len(v.OrderBy) > 0 {
		return Window{}, ErrNotImplemented // WP1
	}
	return d.fetchFiltered(ctx, v.Where, start, n, cols, idx)
}

// Count counts the rows of a view.
func (d *dataset) Count(ctx context.Context, v View) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if isPlain(v) {
		return d.numRows, nil
	}
	if v.IsSQL() || len(v.OrderBy) > 0 {
		return 0, ErrNotImplemented // WP1
	}
	return d.countFiltered(ctx, v.Where)
}

func isPlain(v View) bool { return v.Plain() }

func (d *dataset) columnIndices(cols []string) ([]int, error) {
	idx := make([]int, len(cols))
	for i, c := range cols {
		j, ok := d.byName[c]
		if !ok {
			return nil, fmt.Errorf("no column %s in this file", Sanitize(c))
		}
		idx[i] = j
	}
	return idx, nil
}

func emptyWindow(start int64, cols []string) Window {
	w := Window{Start: start, FileRows: []int64{}, Cols: make(map[string][]Value, len(cols))}
	for _, c := range cols {
		w.Cols[c] = []Value{}
	}
	return w
}

// duckType is DuckDB's name for the type it reads an Arrow type from Parquet
// as: Column.Type when DuckDB can't read the file (else Open takes DuckDB's).
func duckType(t arrow.DataType) string {
	switch t := t.(type) {
	case *arrow.BooleanType:
		return "BOOLEAN"
	case *arrow.Int8Type:
		return "TINYINT"
	case *arrow.Int16Type:
		return "SMALLINT"
	case *arrow.Int32Type:
		return "INTEGER"
	case *arrow.Int64Type:
		return "BIGINT"
	case *arrow.Uint8Type:
		return "UTINYINT"
	case *arrow.Uint16Type:
		return "USMALLINT"
	case *arrow.Uint32Type:
		return "UINTEGER"
	case *arrow.Uint64Type:
		return "UBIGINT"
	case *arrow.Float16Type, *arrow.Float32Type:
		return "FLOAT"
	case *arrow.Float64Type:
		return "DOUBLE"
	case *arrow.StringType, *arrow.LargeStringType, *arrow.StringViewType:
		return "VARCHAR"
	case *arrow.BinaryType, *arrow.LargeBinaryType, *arrow.BinaryViewType, *arrow.FixedSizeBinaryType:
		return "BLOB"
	case *arrow.Date32Type, *arrow.Date64Type:
		return "DATE"
	case *arrow.Time32Type, *arrow.Time64Type:
		return "TIME"
	case *arrow.TimestampType:
		if t.TimeZone != "" {
			return "TIMESTAMP WITH TIME ZONE"
		}
		if t.Unit == arrow.Nanosecond {
			return "TIMESTAMP_NS"
		}
		return "TIMESTAMP"
	case arrow.DecimalType:
		return fmt.Sprintf("DECIMAL(%d,%d)", t.GetPrecision(), t.GetScale())
	case *arrow.DictionaryType:
		return duckType(t.ValueType)
	case *arrow.ListType:
		return duckType(t.Elem()) + "[]"
	case *arrow.LargeListType:
		return duckType(t.Elem()) + "[]"
	case *arrow.FixedSizeListType:
		return fmt.Sprintf("%s[%d]", duckType(t.Elem()), t.Len())
	case *arrow.MapType:
		return "MAP(" + duckType(t.KeyType()) + ", " + duckType(t.ItemType()) + ")"
	case *arrow.StructType:
		parts := make([]string, t.NumFields())
		for i, f := range t.Fields() {
			q, err := quoteIdent(f.Name)
			if err != nil {
				q = Sanitize(f.Name)
			}
			parts[i] = Sanitize(q) + " " + duckType(f.Type)
		}
		return "STRUCT(" + strings.Join(parts, ", ") + ")"
	case *arrow.NullType:
		return "INTEGER"
	case arrow.ExtensionType:
		if t.ExtensionName() == "arrow.uuid" {
			return "UUID"
		}
		return duckType(t.StorageType())
	}
	return strings.ToUpper(Sanitize(t.String()))
}

// duckTypeOf is DuckDB's type of column name ("" if DuckDB didn't bind the file).
func (d *dataset) duckTypeOf(name string) string {
	if j, ok := d.byName[name]; ok && j < len(d.duckTypes) {
		return d.duckTypes[j]
	}
	return ""
}
