// Package data reads a Parquet file for the pqx grid: schema, windows of rows,
// filtered views and counts. Plain views read row groups with arrow-go;
// filtered views go through DuckDB. Every call that reads can be cancelled
// through its context.
package data

import "context"

// Column is one top-level column of the file.
type Column struct {
	Name string
	Type string // DuckDB type name, e.g. "BIGINT", "DOUBLE", "VARCHAR"
}

// View selects rows of the file.
type View struct {
	Where string // "" for the plain view; a SQL WHERE expression otherwise
}

// Plain reports whether the view is the whole file in file order.
func (v View) Plain() bool { return v.Where == "" }

// Window is rows [Start, Start+Len) of a view, for the requested columns.
type Window struct {
	Start    int64
	Len      int
	FileRows []int64             // the file row number of each row
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
	// pqx's where_sql: balanced parentheses, no statement separators).
	CheckWhere(where string) error

	// Fetch reads a window of at most n rows starting at row start of the
	// view. The plain view reads row groups directly; a filtered view uses
	// DuckDB with LIMIT/OFFSET. Cancelling ctx stops it and returns ctx.Err().
	Fetch(ctx context.Context, v View, start int64, n int, cols []string) (Window, error)

	// Count counts the rows of a view; the plain view answers from the footer.
	Count(ctx context.Context, v View) (int64, error)

	Close() error
}

// Options configure Open.
type Options struct {
	Threads int // DuckDB threads; 0 for DuckDB's default
}
