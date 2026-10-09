package data

import (
	"context"
	"crypto/rand"
	"database/sql/driver"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
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
	// DuckDB writes to a file of ours in the target's directory, which then
	// replaces the target: a failed or cancelled export leaves the target
	// as it was, and DuckDB's own temporary file (tmp_<name>, which could be
	// the file being explored or someone else's) never comes into it.
	tmp, err := d.exportTemp(out)
	if err != nil {
		return 0, err
	}
	copySQL := "COPY (\n" + base + "\n) TO " + quoteStr(tmp) + " (" + opts + ", USE_TMP_FILE false)"
	var n int64
	err = d.withConn(ctx, func(c *duckdbConn) (err error) {
		// (this runs to the end even when Export has returned on a cancel:
		// the temporary file is removed here, never renamed after a cancel.
		// One race is left: a cancel that comes after the ctx check below
		// but before the rename lets the export complete, while Export may
		// already have returned the cancel.)
		defer func() {
			if err == nil {
				err = ctx.Err()
			}
			if err == nil {
				err = finishExport(tmp, out)
			}
			if err != nil {
				os.Remove(tmp)
			}
		}()
		// the query, parenthesized as COPY has it, must be one SELECT…
		if err := checkSelect(c, "SELECT * FROM (\n"+base+"\n)"); err != nil {
			return err
		}
		st, err := prepareCopy(c, copySQL)
		if err != nil {
			return err
		}
		defer st.Close()
		rows, err := st.QueryContext(ctx, nil)
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
	if err != nil {
		os.Remove(tmp) // (if the connection never came, nothing else removes it)
	}
	return n, err
}

// prepareCopy prepares copySQL, which must be exactly one COPY statement
// (duckdb-go's Prepare refuses more than one).
func prepareCopy(c *duckdbConn, copySQL string) (*duckdb.Stmt, error) {
	st, err := c.Prepare(copySQL)
	if err != nil {
		if strings.Contains(err.Error(), "multiple statements") || strings.Contains(err.Error(), "PrepareContext") {
			return nil, errNotOneSelect
		}
		return nil, duckError(err)
	}
	ds, ok := st.(*duckdb.Stmt)
	if !ok {
		st.Close()
		return nil, errors.New("not a DuckDB statement")
	}
	if typ, err := ds.StatementType(); err != nil || typ != duckdb.STATEMENT_TYPE_COPY {
		st.Close()
		return nil, errNotOneSelect
	}
	return ds, nil
}

// exportTemp makes an empty file of our own next to out, for DuckDB to
// write the export into. Its name is short (so a long target name still
// fits), and it is created 0666 so the umask applies, as to any new file.
func (d *dataset) exportTemp(out string) (string, error) {
	dir := filepath.Dir(out)
	var f *os.File
	var err error
	for range 100 {
		var r [4]byte
		rand.Read(r[:])
		f, err = os.OpenFile(filepath.Join(dir, ".pqx-"+hex.EncodeToString(r[:])+".tmp"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o666)
		if !errors.Is(err, fs.ErrExist) {
			break
		}
	}
	if err != nil {
		var pe *fs.PathError
		if errors.As(err, &pe) {
			err = pe.Err // (the temporary file's name means nothing to anyone)
		}
		return "", fmt.Errorf("can't write in %s: %w", Sanitize(dir), safeErr(err))
	}
	tmp := f.Name()
	f.Close()
	if d.isSource(tmp) { // (it can't be: CreateTemp makes a new file)
		os.Remove(tmp)
		return "", ErrOverwriteSource
	}
	return tmp, nil
}

// finishExport puts the written temporary file in out's place, with out's
// permissions if it exists (else those it was created with).
func finishExport(tmp, out string) error {
	if fi, err := os.Stat(out); err == nil {
		if err := os.Chmod(tmp, fi.Mode().Perm()); err != nil {
			return safeErr(err)
		}
	}
	return safeErr(os.Rename(tmp, out))
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
	if out == d.path || d.isSource(out) {
		return "", ErrOverwriteSource
	}
	// an existing target that can't be replaced is refused up front, by name
	if fi, err := os.Stat(out); err == nil {
		if fi.IsDir() {
			return "", fmt.Errorf("%s is a directory", Sanitize(out))
		}
		if !writable(out) {
			return "", fmt.Errorf("%s is read-only", Sanitize(out))
		}
	}
	return out, nil
}

// isSource reports whether path is the file being explored: the file Open
// opened, or the file now at its path.
func (d *dataset) isSource(path string) bool {
	fi, err := os.Stat(path)
	if err != nil {
		return false
	}
	if src, err := d.f.Stat(); err == nil && os.SameFile(fi, src) {
		return true
	}
	if src, err := os.Stat(d.path); err == nil && os.SameFile(fi, src) {
		return true
	}
	return false
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
