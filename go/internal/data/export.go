package data

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/duckdb/duckdb-go/v2"
)

// ErrOverwriteSource is Export's error for an output path that is the file
// being explored.
var ErrOverwriteSource = errors.New("refusing to overwrite the file being explored")

// Export writes view v (columns cols, or all) to path in format f with
// DuckDB's COPY, and returns the number of rows written (pqx's export).
// A file view keeps its filter and sort (ties in file order); a SQL view is
// its query. path is expanded (~) and made absolute; it may not be the
// file being explored under any spelling (a symlink, a hard link, "..").
func (d *dataset) Export(ctx context.Context, v View, path string, f ExportFormat, cols []string) (int64, error) {
	out, err := d.exportPath(path)
	if err != nil {
		return 0, err
	}
	var opts string
	switch f {
	case ExportParquet:
		opts = "FORMAT parquet, COMPRESSION zstd"
	case ExportCSV:
		opts = "FORMAT csv, HEADER true"
	case ExportJSON:
		opts = "FORMAT json"
	default:
		return 0, fmt.Errorf("unknown export format %d", f)
	}
	if err := d.prepare(ctx, v); err != nil {
		return 0, err
	}
	base, err := d.exportSQL(v, cols)
	if err != nil {
		return 0, err
	}
	copySQL := "COPY (\n" + base + "\n) TO " + quoteStr(out) + " (" + opts + ")"
	var n int64
	err = d.withConn(ctx, func(c *duckdbConn) error {
		// the query, parenthesized as COPY has it, must be one SELECT…
		if err := checkSelect(c, "SELECT * FROM (\n"+base+"\n)"); err != nil {
			return err
		}
		// …and the whole one COPY statement (duckdb-go's Prepare refuses more than one)
		st, err := c.Prepare(copySQL)
		if err != nil {
			if strings.Contains(err.Error(), "multiple statements") || strings.Contains(err.Error(), "PrepareContext") {
				return errNotOneSelect
			}
			return duckError(err)
		}
		defer st.Close()
		ds, ok := st.(*duckdb.Stmt)
		if !ok {
			return errors.New("not a DuckDB statement")
		}
		if typ, err := ds.StatementType(); err != nil || typ != duckdb.STATEMENT_TYPE_COPY {
			return errNotOneSelect
		}
		rows, err := ds.QueryContext(ctx, nil)
		if err != nil {
			return duckError(err)
		}
		defer rows.Close()
		dest := make([]driver.Value, len(rows.Columns()))
		if err := rows.Next(dest); err != nil && err != io.EOF {
			return duckError(err)
		}
		if len(dest) > 0 {
			switch x := dest[0].(type) {
			case int64:
				n = x
			case uint64:
				n = int64(x)
			case int32:
				n = int64(x)
			}
		}
		return nil
	})
	return n, err
}

// exportPath is path expanded and absolute, unless it is the file being
// explored: the same path, or (when it exists) the same file.
func (d *dataset) exportPath(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", errors.New("no file name to export to")
	}
	if path == "~" || strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			path = filepath.Join(home, path[1:])
		}
	}
	out, err := filepath.Abs(path)
	if err != nil {
		return "", safeErr(err)
	}
	if out == d.path {
		return "", ErrOverwriteSource
	}
	if fi, err := os.Stat(out); err == nil {
		if src, err := d.f.Stat(); err == nil && os.SameFile(fi, src) {
			return "", ErrOverwriteSource
		}
		if src, err := os.Stat(d.path); err == nil && os.SameFile(fi, src) {
			return "", ErrOverwriteSource
		}
	}
	return out, nil
}

// exportSQL is the query Export writes: a SQL view's (relationSQL), or the
// file's columns with the filter and the sort, NULLs last and ties in file
// order.
func (d *dataset) exportSQL(v View, cols []string) (string, error) {
	if v.IsSQL() {
		return d.relationSQL(v, cols, Sample{})
	}
	q, err := d.relationSQL(View{Where: v.Where}, cols, Sample{})
	if err != nil {
		return "", err
	}
	if len(v.OrderBy) == 0 {
		return q, nil
	}
	keys := make([]string, 0, len(v.OrderBy)+1)
	for _, s := range v.OrderBy {
		c, err := d.colRef(v, s.Column)
		if err != nil {
			return "", err
		}
		dir := "ASC"
		if s.Desc {
			dir = "DESC"
		}
		keys = append(keys, c+" "+dir+" NULLS LAST")
	}
	if d.hasRowNum {
		keys = append(keys, "file_row_number")
	}
	return q + " ORDER BY " + strings.Join(keys, ", "), nil
}
