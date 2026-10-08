# Go prototype: plan

Status: plan, waiting for the user's approval (2026-10-08). Background and the
open questions are in [native-port.md](native-port.md); results go there too,
under "Prototype results".

The prototype answers the open questions with the least code. It is not the
start of the full port: code may be thrown away, and nothing here touches the
Python package, its tests or its CI.

## Already checked (2026-10-08, this machine)

A 60-line probe (`duckdb-go` v2.10506.0, Go 1.27.1, Rocky 10, against
SSSource on the network filesystem):

| question | answer |
|---|---|
| 1. Cancel | **Yes.** Cancelling the `context` of `QueryRowContext` interrupts DuckDB: a full scan cancelled at 0.40 s returned `INTERRUPT Error` at 0.56 s (the scan takes 2.5 s uncancelled). Latency about 0.16 s; to be measured more carefully. |
| 2. Prebuilt DuckDB | **Yes.** `duckdb-go-bindings` ships `lib/` modules for linux-amd64, linux-arm64, darwin-amd64, darwin-arm64 and windows-amd64. v2.10506.0 bundles **DuckDB 1.5.6**, the same version Python pqx uses. A build takes about 30 s, with no C++ compile. |
| 3. Arrow results | **Yes, behind a build tag.** `duckdb.NewArrowFromConn` needs `-tags duckdb_arrow`. It returns `arrow-go/v18` (v18.5.1) records, the same module used for Parquet. |
| 7. Binary | 77 MB unstripped. It links `libstdc++` and glibc dynamically, so not yet fully static. |

One warning sign for question 4: a 100-row window of all 185 SSSource columns
via DuckDB (`file_row_number between …`) took **1.0–1.2 s**. This is why
Python pqx reads row groups directly with PyArrow, and the Go reader must do
the same with `arrow-go`.

## Layout

All Go code lives in `go/` on this branch (`native-port`), which is the
integration branch:

```
go/
  go.mod                  module github.com/mjuric/pqx/go
  cmd/pqx/main.go         flags, --version, starts the UI
  internal/data/          the dataset: footer, schema, windows, filter, count, cancel
  internal/ui/            Bubble Tea model: grid, filter bar, status, keys
  bench/                  measurement programs and scripts
```

- The `duckdb_arrow` build tag is always on (set in a `Makefile` and in CI).
- CI: a new workflow `.github/workflows/go.yml` that runs only when `go/**`
  changes: `go vet`, `go test ./...`, a build, and the build time and binary
  size printed in the log. `ci.yml` and `publish.yml` stay as they are.

## The seam between the data layer and the UI

Both agents code against this interface (in `internal/data/data.go`, written by
the coordinator before the agents start), so they can work in parallel. The UI
agent uses a fake that implements it.

```go
type Column struct {
    Name string
    Type string // DuckDB type name, e.g. "BIGINT", "DOUBLE", "VARCHAR"
}

type View struct {
    Where string // "" for the plain view; a SQL WHERE expression otherwise
}

// Window is rows [Start, Start+Len) of a view, for the requested columns.
type Window struct {
    Start    int64
    Len      int
    FileRows []int64             // the file row number of each row
    Cols     map[string][]string // column name -> cell text, len == Len
}

type Dataset interface {
    Path() string
    NumRows() int64          // rows in the file
    Columns() []Column
    RowGroups() []int64      // rows in each row group

    // CheckWhere rejects anything that isn't one expression (the rules of
    // pqx's where_sql: balanced parentheses, no statement separators).
    CheckWhere(where string) error

    // Fetch reads a window. The plain view reads row groups with arrow-go;
    // a filtered view uses DuckDB with LIMIT/OFFSET. Cancelling ctx stops it.
    Fetch(ctx context.Context, v View, start int64, n int, cols []string) (Window, error)

    // Count counts the rows of a view; the plain view answers from the footer.
    Count(ctx context.Context, v View) (int64, error)

    Close() error
}
```

Cell text is plain: integers as is, floats with `strconv` `'g'` and 6
significant digits, NULL as `∅`, strings passed through `Sanitize`, a minimal
port of `fmt.sanitize` (C0/C1 controls, bidi and zero-width characters shown
as visible symbols). Full formatting is out of scope.

## Work split

Two agents in parallel, each in its own worktree on a branch from `native-port`,
each opening a PR into `native-port`. Then the coordinator integrates and
measures, with a third agent if needed.

### Agent A: data layer (`go/internal/data`)

- Open the file. Read the footer and schema once with `arrow-go`
  (`parquet/file`); map Arrow types to DuckDB type names for `Column.Type`.
- **Plain view:** read only the row groups that hold the window, and only the
  requested columns (`pqarrow` with column indices and row-group selection),
  then slice to the window. Measure this against PyArrow on SSSource (open
  question 4); if `arrow-go` can skip pages within a row group, use it.
- **Filtered view:** DuckDB `read_parquet(…, file_row_number=true)` with the
  WHERE in parentheses, `LIMIT/OFFSET`, `file_row_number` aliased to
  `__pqx_row`. Quote column names; escape `* ? [` in the file name.
- `Count` for a filtered view in DuckDB; cancellation through `ctx` for both
  `Fetch` and `Count`, on separate connections so one can be cancelled alone.
  `TimeZone=UTC` on every connection.
- `CheckWhere` and `Sanitize`, ported from `where_sql` and `fmt.sanitize`,
  with tests ported from `tests/test_security.py` where they apply.
- Tests on small fixtures written by the tests themselves (Go writes Parquet
  through `pqarrow`), including several row groups, NULLs, and a window that
  spans a row-group boundary. Benchmarks (`go test -bench`) for a window at
  the start, middle and end of a file.

### Agent B: UI (`go/internal/ui`, `go/cmd/pqx`)

- Bubble Tea + Lip Gloss. Terminal colours only: default background and
  foreground, faint for secondary text, the 16-colour palette for meaning.
- **Grid:** header with column names (types faint), the cursor cell in
  reverse video, row numbers on the left. Columns are as wide as their header
  and visible values, up to a cap.
- **Keys:** arrows, PgUp/PgDn, Home/End (first/last column), Ctrl+Home/End
  (first/last row), `g` go to row (`1234`, `1.5M`, `50%`, `-1`), `/` filter,
  `x` clear filter, Esc cancel / leave the filter, `q` quit.
- **Lazy loading:** keep a cache of fetched rows × columns; fetch the window
  around the screen (rows and only the columns on screen) in a goroutine
  returning a `tea.Msg`; a newer fetch cancels the older one. Rows not yet
  loaded show a faint placeholder.
- **Filter:** a one-line input; Enter applies it (rejected filters show the
  error inline and keep the old view), the count runs in the background and
  the status line shows `⠸ counting` until it arrives. The grid is usable
  before the count arrives.
- **Status line:** file name, rows, current row, ✓ ! ✗ ⠸ for state.
- `--version` prints `pqx (Go prototype) <version>` in ASCII.
- Tests: model tests with a fake `Dataset` (key handling, the cache, cancel
  on supersede), using `teatest` or plain `Update` calls.
- Report on the mouse cases (question 5): what Bubble Tea's input reader does
  with X10 mouse under GNU screen, and whether it turns on pixel mode (1016).
  Wire up wheel scrolling and clicks on cells; don't port `_terminal.py` yet.

### Coordinator, after both PRs are in: measurements

Run against Python pqx (master) on the same machine and files, with the
results in `native-port.md`:

- time to first screen, cold and warm (SSSource, `mpc_orbits`, a 300-column
  file from `bench/grid_bench.py`, a 2,000-row-group file);
- PgDn and jump-to-row times (`g 50%`, Ctrl+End) on the same files;
- how long Esc takes to stop a full-scan count;
- grid redraw time on a 200×50 screen with 300 columns (question 6);
- binary size, stripped and not; whether a static build works (`-extldflags
  -static`, or musl) (question 7);
- clean build time locally and in CI.

Then a go/no-go recommendation for the full port.

## Out of scope

Formatting beyond the above; the Stats, Plot, Schema and Metadata tabs; the
details pane; dialogs; themes; `formats.yaml`; sorting; export; packaging.

## Rules for the agents

- Go 1.27 (installed at `/root/sdk/go/bin` on this machine); `gofmt`, `go vet`
  and `go test ./...` must pass before each push.
- Commit and push after each logical unit; open the PR into `native-port`
  when done. Don't touch anything outside `go/` (and `go.yml` for the agent
  that adds CI, A).
- The real files under `/sdf/data/rubin/.../delivery/` are read-only.
  Generated test files go in the scratchpad or `t.TempDir()`, and are deleted
  after use.
- Plain, specific wording in commits, comments and PRs.
