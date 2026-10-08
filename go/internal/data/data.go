// Package data reads a Parquet file for pqx: schema and footer, windows of
// rows of a view (the whole file, a filter, a sort or a SQL query), counts,
// statistics, histograms, 2-D bins and export. The plain view reads row
// groups with arrow-go; everything else goes through DuckDB. Every call that
// reads takes a context and stops when it is cancelled, returning an error
// for which errors.Is(err, context.Canceled) holds.
//
// This file and value.go are the contract between the data layer and the
// rest of pqx (docs/design/go-port.md). Work packages don't change them;
// changes go through the integrator.
//
// Values are raw: strings and names are exactly as in the file and can hold
// control characters. Nothing from this package may reach the terminal
// without going through fmtx (Format or Sanitize).
package data

import (
	"context"
	"errors"
	"strings"

	"github.com/apache/arrow-go/v18/arrow"
)

// Column is one top-level column of the file, or of a SQL view's result.
type Column struct {
	// Name is the column's name as the file has it, unsanitized.
	Name string
	// Type is DuckDB's name for the column's type ("BIGINT", "DOUBLE",
	// "TIMESTAMP WITH TIME ZONE", "STRUCT(ra DOUBLE, dec DOUBLE)", …).
	Type string
	// Arrow is the column's Arrow type as arrow-go reads it from the file
	// (for a SQL view, as DuckDB returns it). fmtx.ShortType and the Schema
	// tab use it.
	Arrow    arrow.DataType
	Nullable bool
	// Unit and Description come from the field's metadata: keys "unit" or
	// "units", and "description", "doc" or "comment"; a felis-style
	// description "[unit] text" gives both when there is no unit key.
	Unit        string
	Description string
	// SQLName is how DuckDB names the column in queries. It differs from
	// Name only for case duplicates (a file with both "Name" and "name":
	// DuckDB calls the second "name_1"); "=" builds filters with it.
	SQLName string
}

// IsNumeric, IsFloat, IsTemporal and IsNested classify Arrow, as pqx's
// ColumnInfo does.
func (c Column) IsNumeric() bool  { return isNumeric(c.Arrow) }
func (c Column) IsFloat() bool    { return isFloat(c.Arrow) }
func (c Column) IsTemporal() bool { return isTemporal(c.Arrow) }
func (c Column) IsNested() bool   { return isNested(c.Arrow) }

// Sort is one sort key.
type Sort struct {
	Column string // Name of a column of the file
	Desc   bool
}

// View is what the grid shows: the whole file, a filter and/or a sort, or a
// full SQL query over the table t.
type View struct {
	Where   string // a SQL WHERE expression; blank for none
	OrderBy []Sort // sorted with NULLS LAST, ties in file order
	// SQL is a full query over t (the file); when set, Where and OrderBy
	// are ignored. Its rows have no file row numbers.
	SQL string
}

// Plain reports whether the view is the whole file in file order.
func (v View) Plain() bool {
	return strings.TrimSpace(v.Where) == "" && len(v.OrderBy) == 0 && !v.IsSQL()
}

// IsSQL reports whether the view is a full SQL query.
func (v View) IsSQL() bool { return strings.TrimSpace(v.SQL) != "" }

// Window is rows [Start, Start+Len) of a view, for the requested columns.
// Start is the start that was asked for (FetchAround documents its
// exception); a Len shorter than asked for means the view ends there (0 if
// start is at or past its end).
type Window struct {
	Start int64
	Len   int
	// FileRows is the file row number of each row; nil for a SQL view. In a
	// filtered or sorted view of a file that has a column of its own named
	// file_row_number (any case), DuckDB can't number the rows, and the
	// entries are -1.
	FileRows []int64
	// Cols maps a requested column's name to its Len values.
	Cols map[string][]Value
	// Failed holds the requested columns that couldn't be read for these
	// rows (by either reader), with the reason; they are not in Cols. The
	// grid shows them as ✗ and doesn't retry them for the same rows.
	Failed map[string]error
}

// Sample asks stats and plots to use about Rows rows of the view, spread
// through the file; 0 means all rows.
type Sample struct{ Rows int64 }

// ColumnStats is a profile of one column of a view (pqx's column_stats).
type ColumnStats struct {
	Name  string
	Count int64 // rows, including NULLs
	Nulls int64
	// NaNs is -1 unless the column is a float column.
	NaNs int64
	// Distinct is approximate, or exact when DistinctExact (the Top list is
	// the whole distribution); -1 if not computed (nested, boolean).
	Distinct      int64
	DistinctExact bool
	Min, Max      Value    // NaN-free; nil for nested columns or no values
	Mean, Std     *float64 // over finite values; nil unless numeric
	// Quantiles maps 0.01, 0.05, 0.25, 0.5, 0.75, 0.95, 0.99 to approximate
	// quantiles of the finite values (numeric, non-decimal columns).
	Quantiles map[float64]float64
	Top       []ValueCount // most frequent values, most frequent first
	Sampled   bool
}

// ValueCount is one of the most frequent values of a column and its count.
type ValueCount struct {
	Value Value
	Count int64
}

// HistOptions configure Histogram.
type HistOptions struct {
	Bins     int // default 40
	Sample   Sample
	Lo, Hi   *float64 // range; nil for the column's min and max
	Log      bool     // bin log10 of the positive values
	Temporal bool     // bin epoch seconds
}

// Histogram is len(Counts)+1 bin edges and the counts.
type Histogram struct {
	Edges  []float64
	Counts []int64
}

// Grid2D is a 2-D histogram: Counts[iy][ix], with the ranges binned. For
// SkyCounts, x is longitude (0–360°, 360/res bins) and y latitude
// (−90–90°, 180/res bins).
type Grid2D struct {
	Counts [][]int64
	X, Y   [2]float64
}

// ExportFormat is the format Export writes.
type ExportFormat int

const (
	ExportParquet ExportFormat = iota // zstd
	ExportCSV                         // with a header line
	ExportJSON                        // newline-delimited
)

// ChunkSummary is one leaf column's storage and statistics over all row
// groups, from the footer (pqx's column_chunk_summary).
type ChunkSummary struct {
	Path         string // dotted leaf path
	Physical     string // Parquet physical type, e.g. "INT64"
	Logical      string // logical type, "" if none
	Compression  string // codec of its first chunk, e.g. "ZSTD"
	Compressed   int64
	Uncompressed int64
	Min, Max     Value // nil when not every chunk has min/max statistics
	Nulls        int64
	HasStats     bool
}

// RowGroup is one row group, from the footer.
type RowGroup struct {
	Index        int
	Start, Rows  int64
	Compressed   int64
	Uncompressed int64
}

// FileInfo is what the footer says about the file as a whole.
type FileInfo struct {
	Path          string // absolute
	Size          int64  // bytes
	FooterSize    int64
	FormatVersion string // e.g. "2.6"
	CreatedBy     string // raw; can hold anything
	NumLeaves     int
	Compressed    int64
	Uncompressed  int64
}

// KeyValue is one entry of the file's key-value metadata, in file order.
type KeyValue struct {
	Key, Value string // raw
}

// ErrNotImplemented is returned by methods not built yet.
var ErrNotImplemented = errors.New("not implemented yet")

// Dataset is an open Parquet file. Its methods may be called from several
// goroutines at once; each reading call uses its own DuckDB connection, so
// cancelling one leaves the others running.
type Dataset interface {
	Path() string
	Info() FileInfo
	NumRows() int64 // rows in the file
	Columns() []Column
	RowGroups() []int64 // rows in each row group
	KeyValueMetadata() []KeyValue

	// SetupErr is non-nil when DuckDB can't read the file. The footer
	// (Info, Columns, FooterSummary, RowGroupInfo, Encodings, key-value
	// metadata) still works, as does Fetch of the plain view.
	SetupErr() error

	// Validate checks a view without reading data: a filter must be one
	// expression (balanced parentheses, see where_sql) and any query one
	// SELECT; then DuckDB binds it. It returns the view's columns (the
	// file's, or a SQL query's result columns). Errors are short, sanitized
	// and fit for the status line.
	Validate(ctx context.Context, v View) ([]Column, error)

	// Fetch reads at most n rows of the view from row start, for cols.
	// The plain view reads row groups directly, with columns arrow-go can't
	// read falling back to DuckDB; other views go through DuckDB.
	Fetch(ctx context.Context, v View, start int64, n int, cols []string) (Window, error)

	// FetchColumns reads cols for the given file rows (lazy columns for rows
	// already shown). The result's FileRows are fileRows and Start is 0.
	FetchColumns(ctx context.Context, fileRows []int64, cols []string) (Window, error)

	// Count counts the rows of a view; the plain view answers from the footer.
	Count(ctx context.Context, v View) (int64, error)

	// FindRow is the position of file row fileRow in the view; found is
	// false if it isn't in it or the view has no file rows.
	FindRow(ctx context.Context, v View, fileRow int64) (pos int64, found bool, err error)

	// FetchAround is Fetch(v, start, n, cols) for a window holding file row
	// fileRow at view position pos (start <= pos < start+n), found by file
	// row number near fileRow so its cost doesn't grow with pos. If fewer
	// rows precede the record than pos-start, the window starts later than
	// asked and Start says where.
	FetchAround(ctx context.Context, v View, fileRow, pos, start int64, n int, cols []string) (Window, error)

	ColumnStats(ctx context.Context, v View, col string, s Sample) (ColumnStats, error)
	Histogram(ctx context.Context, v View, col string, o HistOptions) (Histogram, error)
	// SkyCounts bins (lon, lat) in degrees on a resDeg equirectangular grid.
	SkyCounts(ctx context.Context, v View, lon, lat string, resDeg float64, s Sample) (Grid2D, error)
	// XYCounts is a 2-D histogram of two numeric columns over xlim and
	// ylim, or, when nil, their 0.1–99.9% range.
	XYCounts(ctx context.Context, v View, x, y string, nx, ny int, s Sample, xlim, ylim *[2]float64) (Grid2D, error)

	// Export writes the view (cols, or all if nil) to path and returns the
	// rows written. It refuses to overwrite the file being explored.
	Export(ctx context.Context, v View, path string, f ExportFormat, cols []string) (int64, error)

	// FooterSummary and RowGroupInfo scan every column chunk in the footer
	// once (cached); cancelling ctx stops the scan, and a later call starts
	// over.
	FooterSummary(ctx context.Context) ([]ChunkSummary, error)
	RowGroupInfo(ctx context.Context) ([]RowGroup, error)
	// Encodings are the encodings leaf column path uses in any row group.
	Encodings(path string) []string

	Close() error
}

// Options configure Open.
type Options struct {
	Threads int // DuckDB threads; 0 for DuckDB's default
}
