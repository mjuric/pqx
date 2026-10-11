// Command q4 measures open question 4 of docs/design/native-port.md: how long
// a window of rows deep in a big row group takes, with arrow-go (the plain
// view's reader) and with DuckDB, for a few columns and for all of them. It
// only reads the file. If the file isn't there it says so and exits 0.
//
//	go run -tags duckdb_arrow ./bench/q4 [-file F] [-row 5000000] [-n 100] [-runs 3]
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/duckdb/duckdb-go/v2"
	"github.com/mjuric/pqx/go/internal/data"
)

const sssource = "/sdf/data/rubin/user/mjuric/shutter-timing-ssp/rerun/2026-10-06/run/delivery/SSSource.parquet"

func main() {
	path := flag.String("file", sssource, "Parquet file")
	row := flag.Int64("row", 5_000_000, "first row of the window")
	n := flag.Int("n", 100, "rows in the window")
	runs := flag.Int("runs", 3, "runs of each")
	few := flag.Int("cols", 15, "columns in the narrow case (the first ones)")
	flag.Parse()
	if _, err := os.Stat(*path); err != nil {
		fmt.Printf("q4: %v; skipped\n", err)
		return
	}
	ctx := context.Background()
	defaultParallel := data.ReadParallel

	t0 := time.Now()
	ds, err := data.Open(*path, data.Options{})
	check(err)
	defer ds.Close()
	fmt.Printf("open: %v (%d rows, %d columns, %d row groups)\n", ms(time.Since(t0)), ds.NumRows(), len(ds.Columns()), len(ds.RowGroups()))
	var all []string
	for _, c := range ds.Columns() {
		all = append(all, c.Name)
	}
	sets := []struct {
		name string
		cols []string
	}{{fmt.Sprintf("%d cols", *few), all[:min(*few, len(all))]}, {fmt.Sprintf("all %d cols", len(all)), all}}

	type cfg struct {
		name     string
		buffered bool
		buf      int64
		parallel int
	}
	for _, c := range []cfg{
		{"arrow-go, 1 MiB buffered stream", true, 1 << 20, 0},
		{"arrow-go, 4 MiB buffered stream", true, 4 << 20, 0},
		{"arrow-go, whole column chunks", false, 0, 0},
		{"arrow-go, 1 MiB, 8 columns at once", true, 1 << 20, 8},
		{"arrow-go, 1 MiB, 1 column at a time", true, 1 << 20, 1},
	} {
		data.BufferedStream, data.ReadBufferSize = c.buffered, max(c.buf, 1<<14)
		data.ReadParallel = defaultParallel
		if c.parallel > 0 {
			data.ReadParallel = c.parallel
		}
		for _, s := range sets {
			var times []string
			for range *runs {
				t := time.Now()
				w, err := ds.Fetch(ctx, data.View{}, *row, *n, s.cols)
				check(err)
				if w.Len != *n {
					check(fmt.Errorf("got %d rows", w.Len))
				}
				times = append(times, ms(time.Since(t)))
			}
			fmt.Printf("%-38s %-14s %s\n", c.name, s.name, strings.Join(times, ", "))
		}
	}
	data.BufferedStream, data.ReadBufferSize, data.ReadParallel = true, 1<<20, defaultParallel

	// DuckDB, the way pqx's plain view uses it: the file_row_number filter.
	connector, err := duckdb.NewConnector(":memory:?TimeZone=UTC&enable_object_cache=true", nil)
	check(err)
	db := sql.OpenDB(connector)
	defer db.Close()
	lit := "'" + strings.NewReplacer("'", "''", "*", "[*]", "?", "[?]", "[", "[[]").Replace(*path) + "'"
	t0 = time.Now()
	_, err = db.Exec("SELECT * FROM read_parquet(" + lit + ") LIMIT 0")
	check(err)
	fmt.Printf("DuckDB bind: %v\n", ms(time.Since(t0)))
	for _, s := range sets {
		q := make([]string, len(s.cols))
		for i, c := range s.cols {
			q[i] = `"` + strings.ReplaceAll(c, `"`, `""`) + `"`
		}
		query := fmt.Sprintf("SELECT file_row_number, %s FROM read_parquet(%s, file_row_number=true) WHERE file_row_number >= %d AND file_row_number < %d ORDER BY file_row_number",
			strings.Join(q, ", "), lit, *row, *row+int64(*n))
		var times []string
		for range *runs {
			t := time.Now()
			rows, err := db.QueryContext(ctx, query)
			check(err)
			k := 0
			for rows.Next() {
				k++
			}
			check(rows.Err())
			rows.Close()
			if k != *n {
				check(fmt.Errorf("DuckDB returned %d rows", k))
			}
			times = append(times, ms(time.Since(t)))
		}
		fmt.Printf("%-38s %-14s %s\n", "DuckDB", s.name, strings.Join(times, ", "))
	}

	// How fast a plain-view read of all columns stops when cancelled.
	for _, after := range []time.Duration{20 * time.Millisecond, 100 * time.Millisecond} {
		var out []string
		for range *runs {
			cctx, cancel := context.WithCancel(ctx)
			time.AfterFunc(after, cancel)
			t := time.Now()
			_, err := ds.Fetch(cctx, data.View{}, *row, *n, all)
			el := time.Since(t)
			cancel()
			out = append(out, fmt.Sprintf("%v (%v)", ms(el), err))
		}
		fmt.Printf("arrow-go all cols, cancelled after %v: returned after %s\n", after, strings.Join(out, ", "))
	}
}

func ms(d time.Duration) string { return fmt.Sprintf("%.0f ms", d.Seconds()*1000) }

func check(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "q4:", err)
		os.Exit(1)
	}
}
