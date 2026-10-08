// Package data reads a Parquet file for the pqx grid: schema, windows of rows,
// filtered views and counts. Plain views read row groups with arrow-go;
// filtered views go through DuckDB. Every call that reads can be cancelled
// through its context.
package data

import (
	"context"
	"strings"
)

// Column is one top-level column of the file.
type Column struct {
	// Name is the column's name as the file has it, unsanitized: it can hold
	// control characters. Pass it through Sanitize before showing it.
	Name string
	Type string // DuckDB type name, e.g. "BIGINT", "DOUBLE", "VARCHAR"
}

// View selects rows of the file.
type View struct {
	Where string // "" (or blank) for the plain view; a SQL WHERE expression otherwise
}

// Plain reports whether the view is the whole file in file order: Where is
// empty or only white space.
func (v View) Plain() bool { return strings.TrimSpace(v.Where) == "" }

// Window is rows [Start, Start+Len) of a view, for the requested columns.
// Start is the start that was asked for; a Len shorter than asked for means
// the view ends there (0 if start is at or past its end).
type Window struct {
	Start int64
	Len   int
	// FileRows is the file row number of each row. In a filtered view of a
	// file that has a column of its own named file_row_number (any case),
	// DuckDB can't number the rows, and the entries are -1.
	FileRows []int64
	Cols     map[string][]string // column name -> cell text, len == Len
}

// Null is the cell text for SQL NULL.
const Null = "∅"

// Dataset is an open Parquet file.
type Dataset interface {
	Path() string
	NumRows() int64 // rows in the file
	Columns() []Column
	RowGroups() []int64 // rows in each row group

	// CheckWhere rejects anything that isn't one expression (the rules of
	// pqx's where_sql: balanced parentheses, no statement separators), then
	// has DuckDB bind it without running it. It runs on the caller's
	// goroutine and gives up after a bounded time (10 s by default).
	CheckWhere(where string) error

	// Fetch reads a window of at most n rows starting at row start of the
	// view. The plain view reads row groups directly; a filtered view uses
	// DuckDB with LIMIT/OFFSET. Cancelling ctx stops it and returns an error
	// for which errors.Is(err, context.Canceled) holds (ctx.Err() itself).
	Fetch(ctx context.Context, v View, start int64, n int, cols []string) (Window, error)

	// Count counts the rows of a view; the plain view answers from the footer.
	// Cancelling ctx stops it as Fetch.
	Count(ctx context.Context, v View) (int64, error)

	Close() error
}

// Options configure Open.
type Options struct {
	Threads int // DuckDB threads; 0 for DuckDB's default
}
