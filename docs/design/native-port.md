# Porting pqx to a compiled language

Status: investigation done 2026-10-08; prototype built and measured the same day
(see [Prototype results](#prototype-results)). Recommendation: go ahead with the
full port, after the user decides. This document is the
handoff to the agent who continues the work: it records what was asked, what
was found, what was decided, and what is still unverified.

## What the user wants

- **Speed and ease of install.** The user said they don't care which language
  pqx is written in. The goal is a single binary that starts instantly and
  installs without Python, not a rewrite for its own sake.
- **DuckDB stays.** Use its bindings for the new language. Nobody is rewriting
  the query engine.
- **0.2.0 final ships from the Python code first** (before 2026-10-19, see
  [Constraints](#constraints-and-house-rules)). The port must not hold up that
  release or touch `master` until the user says so.

## Recommendation: Go

| | Go | Rust | C# / .NET |
|---|---|---|---|
| DuckDB | official `duckdb-go` driver (formerly `marcboeker/go-duckdb`), ships prebuilt static DuckDB libraries for Linux/macOS (amd64, arm64) and Windows: no C++ compile | `duckdb` crate; its `bundled` feature compiles DuckDB from C++ (many minutes per clean CI build), or link a prebuilt `libduckdb` (more fiddly) | DuckDB.NET, solid |
| Terminal UI | Charm: Bubble Tea (Elm-style loop), Bubbles (text input with suggestions, list, table, viewport, help), Lip Gloss (styling). The most complete kit outside Python | `ratatui` + `crossterm`: drawing only, no widget tree, focus, dialogs or input widgets beyond small crates (`tui-input`, `tui-textarea`) | Terminal.Gui v2: real widget system with focus, dialogs and layout, closest to Textual |
| Parquet | `apache/arrow-go` (`parquet/file`, `parquet/pqarrow`) | `parquet` / `arrow` crates, the best of the three | Parquet.Net |
| Binary | one static binary per platform, goreleaser for Homebrew / downloads | one binary | Native AOT single binary |
| Build speed | seconds | slow with bundled DuckDB | moderate |
| Who would contribute | common for CLIs | common for CLIs | rare in data/astronomy tools |

Go wins on what the user asked for: the fastest path to a small, quick-to-build
single binary, with the most ready-made UI pieces. Rust would be marginally
faster and leaner, but DuckDB does the heavy work, so that margin doesn't show;
it costs more UI code and slower builds. C# is a fallback if Go's TUI kit turns
out too thin. Zig/C++ (too much by hand) and Node/Bun (big bundles, weak TUIs)
were ruled out.

**The cheap alternative, staying in Python,** was weighed and doesn't meet the
goal. `uvx pqx file.parquet` already runs pqx with no setup if `uv` is
installed (uv's installer is one command and needs no system Python), but it
downloads about 200 MB the first time (PyArrow alone is 152 MB installed).
Startup can't be cut much: see the measurements below.

## Measurements (Python pqx, 2026-10-08)

On an SDF login node (`python -X importtime -c "import pqx.app"`, cumulative,
warm cache), importing pqx takes **0.55–0.63 s** before any file is opened:

| module | import ms |
|---|---|
| duckdb | 106 |
| pyarrow | 95 |
| textual | 71 |
| numpy | 69 |
| asyncio | 39 |
| markdown_it (via textual) | 24 |

All four big libraries are needed before the first screen, so lazy imports
would save perhaps 0.1–0.2 s. A compiled binary removes this cost.

Known numbers from earlier work, which a port must match or beat:

- SSSource (8.07M rows × 184 columns, 8 row groups of about 1M rows) is ready in under 0.5 s.
- A file with 2,000 row groups takes about 1.2 s to start. This was accepted for Python, mostly footer parsing done by both PyArrow and DuckDB.
- Paging, and jumping to any row, takes milliseconds whatever the file size. Only one row group is read, not the rows before it.
- `=` with the cursor kept on the record: the background lookup takes about 0.4 s on SSSource.
- Grid operations on 300 columns are tens of ms. `bench/grid_bench.py` and `bench/startup_bench.py` measure these, and `docs/design/wide-tables.md` has the targets and results.

Re-measure the Python baseline on the same machine before comparing with the prototype.

## What pqx is today

About 7,400 lines of Python plus a 400-line Textual stylesheet, and 5,500
lines of tests (265 tests, about 6 minutes). Dependencies: `textual>=8.2,<9`,
`duckdb>=1.1`, `pyarrow>=15`, `numpy`, `pytz`, `pyyaml`.

| file | lines | what it does | port notes |
|---|---|---|---|
| `pqx/data.py` | 1,524 | `ParquetDataset`: opens the file; reads the footer, schema, units and descriptions; builds DuckDB views; fetches row windows, columns, counts, stats, histograms and sky/xy bins; finds a row; exports; cancels queries | Ports almost 1:1. Read it closely, because most of the speed lives here (next section). |
| `pqx/app.py` | 3,252 | The Textual app: grid, tabs (Data, Schema, Stats, Plot, Metadata), filter bar, details pane, key bar, status, background workers, all actions | The bulk of the rewrite |
| `pqx/widgets.py` | 253 | `DetailList` (details pane) and friends | UI |
| `pqx/screens.py` | 391 | Dialogs: help, go to row, format, column picker, export, Plot drop-down | UI |
| `pqx/app.tcss` | 404 | Layout and colours (terminal palette by default; Textual themes with `--theme`) | Becomes Lip Gloss styles |
| `pqx/cells.py` | 275 | Cell text, widths and placeholders for the grid | Port 1:1 (use `go-runewidth` / `uniseg` for widths) |
| `pqx/fmt.py` | 453 | Number/time/angle formatting, MJD→UTC, sexagesimal, AB magnitudes, format specs, **`sanitize`** | Port 1:1 with the tests as the spec |
| `pqx/plots.py` | 446 | Histograms and the Mollweide sky map (quadrant glyphs, braille limb, magma/viridis/… colormaps) | Plain arithmetic, easy |
| `pqx/config.py` | 132 | `~/.config/pqx/formats.yaml` (remembered column formats; honours `XDG_CONFIG_HOME`) | Keep the file format compatible |
| `pqx/cli.py` | 115 | GNU-style `--help`/`--version`, options (`-w`, `--format`, `--theme`, `--accent`, `--dim`, `--border`) | Keep the option names; `--version` output is plain ASCII and credits "Mario Juric" |
| `pqx/_terminal.py` | 122 | Mouse workarounds: X10/urxvt mouse from GNU screen, lenient input decoding, pixel-mouse mode (1016) kept off for iTerm2 over ssh. Clears the alternate screen before leaving it (intended) | Check what the Go terminal stack does here |
| `pqx/demo.py` | 114 | `python -m pqx.demo` writes a synthetic LSST-like file | Could stay a Python script for generating test data |

### How the data layer is fast (keep these designs)

- **Two readers.**
  - For plain views, rows come straight from the row groups that hold them, through PyArrow.
  - Otherwise DuckDB is used, numbering rows with `file_row_number`, aliased to `__pqx_row` so it can't collide with a column of the file.
  - `window_cost` estimates both and picks the cheaper. In Go, `arrow-go`'s Parquet reader takes PyArrow's place; recalibrate the cost constants (`_PA_NS_PER_BYTE` etc.) for it.
- **Lazy columns.** The grid fetches only the columns on screen. Others load as they scroll into view (`fetch_columns`, by file row). The details pane loads all of them, in the background.
- **Footer parsed once.** DuckDB's `parquet_metadata_cache` is on. PyArrow parses the footer while DuckDB binds types on another thread, because on huge footers each takes most of a second.
- **Tagged cursors and interrupts.** Each kind of background work runs on its own tagged DuckDB cursor so it can be interrupted alone. Esc interrupts all of them (`interrupt`, `_Cancel`). A newer request supersedes an older one.
- **`find_row` and `fetch_around`.** After `=`, the record's position in the filtered view is found in the background, so the cursor stays on it.
- **Sampling.** Stats and plots use about 2M rows (`SAMPLE_ROWS`), by default only above 200M rows. `m` toggles it.
- **UTC.** All timestamps are shown in UTC (`TimeZone` set on the connection).

### Security behaviour (must carry over; see PR #20 and `tests/test_security.py`)

- **`fmt.sanitize`.** Control, C1, bidi and zero-width characters from the file are shown as visible symbols (ESC as ␛). No byte from the file may reach the terminal as an escape sequence: the grid, details pane, copy (OSC 52), notifications and titles all go through it.
- **Filters are checked.** A filter must be one `SELECT` (`check_select`). `where_sql` rejects unbalanced parentheses, so a filter can't escape its own parentheses inside pqx's queries.
- **Names are quoted.** Completion inserts quoted names (`sql_ident`, `quote_ident`).
- **Globs are escaped.** In file names, `*`, `?` and `[` are escaped, so DuckDB reads exactly that one file.
- **Text is text.** Strings from the file are never interpreted as markup.
- **Case-duplicate column names** (`Name` and `name`) each show their own data (`sql_name`/`_qcol`). Known gap: typed filters see DuckDB's renamed `name_1`.
- **Dependency hygiene.** PyArrow is pinned at ≥15 because of CVE-2023-47248. In Go, keep `arrow-go` and `duckdb-go` current.
- **Deferred by the user, not done:** locking down DuckDB (community extensions, `lock_configuration`). Note it, but don't do it unasked.

### Behaviour the user has asked for (easy to lose in a rewrite)

The README's key tables and the `?` help screen (`pqx/screens.py`) are the
feature list. Beyond those, the user specifically asked for:

- **Terminal colours by default.** pqx uses the terminal's background and 16-colour palette, and keeps colour for meaning: ✓ done, ! warning, ✗ error. `--theme` switches to a named theme. The user works in iTerm2 over ssh.
- **Keep the viewport.** Filtering, sorting or changing columns keeps the leftmost column, and a record still in the view keeps its screen row (PR #23). A cursor in a pinned column doesn't scroll the grid left.
- **Details pane.**
  - 53 columns wide; values get 24 columns and wrap beyond that (PR #24).
  - `= y i F < >` act on the selected field (PR #21).
  - `=` keeps the cursor on the record, and keys pressed while it looks the record up are queued.
  - Longitudes are shown in degrees and RA in hours. VizieR names (`RAJ2000`) are recognised.
- **Esc (PR #26).**
  - Esc closes the details pane, from the grid or from inside it.
  - It cancels first only while user-started work runs: the lookup, stats, a plot or an export. pqx's own row, column and count loading carries on.
  - It also leaves the filter box and closes every dialog.
- **Dialogs** don't shift the screen behind them (PR #25).
- **Title bar** shows the full version, which is dropped first when space is short.
- **Filter hint** is built from the file's own columns.
- **Column formats** set with `< > F` are remembered by column name in `formats.yaml`.

## Open questions to settle first (not verified)

These claims were made from memory during the investigation and have **not**
been checked. The prototype exists to check them.

1. **Cancelling a running query.** Does `duckdb-go` cancel a running query when its `context` is cancelled (via DuckDB's `duckdb_interrupt`)? It needs to do so promptly, from another goroutine, on a scan of SSSource. Esc depends on this.
2. **Prebuilt DuckDB.** Does `duckdb-go` really ship prebuilt static DuckDB for linux-amd64, linux-arm64, darwin-amd64/arm64 and windows-amd64? Which DuckDB version does it bundle, and how far does that lag DuckDB releases?
3. **Arrow results.** Can `duckdb-go` return Arrow record batches (its Arrow interface), and do they interoperate with the `arrow-go` version used for Parquet?
4. **Row-group reads.** Can `arrow-go` read the rows `[a, b)` of chosen columns from one row group as fast as PyArrow? SSSource has no page index, so seeking within a row group decodes from its start.
5. **Mouse and input.** Does Bubble Tea handle the mouse cases `_terminal.py` fixes?
   - X10/urxvt mouse from GNU screen 4.x, including coordinates above 95.
   - iTerm2 over ssh with pixel mouse mode.
   - OSC 52 copy.
6. **Grid speed.** Does a Bubble Tea grid redraw a 300-column, 200×50 screen fast enough? It needs to hit the `wide-tables.md` targets with lazy columns.
7. **Binary size and static linking.** How big is the binary, and does a fully static Linux build work (DuckDB is C++, so cgo is needed)?

## Next step: a prototype

The user has not yet approved building anything. **Present this plan and get
approval first** (house rules below). The proposed prototype answers the
questions above with the least code:

- **Scope.**
  - Open a Parquet file given on the command line and show a scrolling grid: arrow keys, PgUp/PgDn, Ctrl+Home/End and `g` row N.
  - Load columns lazily as they scroll into view, and read rows by row group for the plain view.
  - Apply a typed filter (`WHERE`), with a background count.
  - Cancel a slow count or query with Esc.
  - `--version`.
- **Out of scope.** Formatting beyond basic numbers, Stats/Plot/Schema tabs, the details pane, dialogs, themes, config.
- **Where it lives.** A `go/` (or `native/`) directory on a branch off `master`, such as this one, so the Python package and its CI are untouched. A separate repository is the alternative. Ask the user; the default is the subdirectory.
- **Measure, against Python pqx on the same machine and files:**
  - time to first screen, cold and warm;
  - PgDn and new-window times on SSSource and on a 300-column file (`bench/grid_bench.py` makes one);
  - how long Esc takes to stop a full scan;
  - binary size;
  - clean build time locally and in CI.
- **Report.** Answer each open question, give the measurements, and recommend go or no-go for a full port. Write it up here under a "Prototype results" section.

If the prototype says go, a full port is roughly one release of work. Follow
the house workflow for big features: an integration branch, a plan in this
directory, subagents per area, review, then the user's approval before
merging. Port order:
1. Data layer, with its tests ported first.
2. `fmt`, `cells` and `plots`, with their tests as the spec.
3. Grid.
4. Filter bar.
5. Details pane.
6. Tabs.
7. Dialogs.
8. Packaging: goreleaser, Homebrew, and possibly PyPI wheels so `pip install pqx` still works.

## Prototype results

Built 2026-10-08 on `native-port` (plan: [go-prototype.md](go-prototype.md); PRs #27
draft, #28 UI, #29 data layer, #30 integration). The Go code is in `go/`: about 3,300
lines plus 2,600 lines of tests (data 22 tests, UI 27, command 1), in CI as `go.yml`.
It opens a file, shows the grid with lazy columns, pages, `g`, filters with a
background count, Esc, mouse wheel and clicks.

### Measurements

The same machine (128 cores, Rocky 10), the same 200×50 pty, warm page cache (cold
reads couldn't be forced here). Times are from key press until the expected row is on
the emulated screen, three runs each, in ms; first screen includes process start and
Python's imports. Harness: `go/bench/pty/` (`bench.py`, `esc.sh`).

| file | | first screen | PgDn | `g` middle row | Ctrl+End |
|---|---|---|---|---|---|
| SSSource (8.07M × 184, 8 row groups) | Python | 2,031–2,424 | 89–108 | 249–280 | 200–226 |
| | Go | 166–174 | 42–44 | 61–66 | 59–65 |
| mpc_orbits (1.58M × 53, 13 row groups) | Python | 1,874–2,644 | 73–87 | 260–311 | 149–167 |
| | Go | 188–213 | 29–41 | 139–140 | 55–57 |
| wide300 (200k × 300, 4 row groups) | Python | 1,742–1,882 | 112–124 | 168–173 | 126–151 |
| | Go | 153–182 | 44–46 | 65–69 | 42–66 |
| rg2000 (2M × 20, 2,000 row groups) | Python | 1,815–1,859 | 159–183 | 220–247 | 95–132 |
| | Go | 285–319 | 43–48 | 24–41 | 36–41 |

Esc on SSSource, with a filter whose count takes about 7 s
(`levenshtein(repeat(obsid,4), repeat(trksub,4)) > 5`):

| | filter applied, spinner shown | Esc until "cancelled" shown |
|---|---|---|
| Python | 191–238 ms | 136–154 ms |
| Go | 57–62 ms | 61–66 ms |

The Esc time is until the screen says the count stopped. DuckDB itself stops within
about 0.1–0.16 s of the cancel (probe and data-layer tests).

Go's PgDn floor of about 40 ms on every file looks like a fixed cost in the UI or
renderer rather than reading; not investigated.

Window reads alone (data layer, SSSource, 100 rows at row 5,000,000):

| reader | 15 columns | all 184 columns |
|---|---|---|
| arrow-go (Go, columns in parallel) | 18–27 ms | 166–190 ms |
| PyArrow (Python pqx's direct reader) | 43–113 ms | 0.35–1.96 s |
| DuckDB (either language) | 77–96 ms | 0.97–1.07 s |

Build and binary:

| | |
|---|---|
| clean build | 43 s here, 56 s in GitHub Actions; 1.1 s after an edit |
| binary, linux-amd64 | 116 MB; 91 MB stripped (`-s -w`); 31 MB gzipped |
| links dynamically | glibc (needs 2.38 when built on Rocky 10), libstdc++, libgcc_s, libm |

### Open questions, answered

1. **Cancelling a query: yes.** Cancelling the `context` interrupts DuckDB from
   another goroutine; a count returned 0.6 ms after the cancel, a full scan within
   0.16 s. A cancelled arrow-go read stops within about 20–40 ms (it checks between
   reads). Each DuckDB call has its own connection, so cancelling one leaves the others.
2. **Prebuilt DuckDB: yes.** `duckdb-go` v2.10506.0 ships static DuckDB 1.5.6 (the
   version Python pqx uses) for linux-amd64/arm64, darwin-amd64/arm64 and
   windows-amd64, with the parquet, json, icu, tpch, tpcds and autocomplete
   extensions built in. No C++ compile.
3. **Arrow results: yes,** behind the `duckdb_arrow` build tag, as `arrow-go/v18`
   records (the Makefile and CI always set the tag).
4. **Row-group reads: arrow-go is faster than PyArrow** (table above), so the plain
   view always reads with arrow-go; no `window_cost`. Without a page index it still
   decompresses a row group's earlier pages, but `SeekToRow` skips decoding them.
5. **Mouse and input, in Bubble Tea v2.0.10:**
   - X10 mouse from GNU screen decodes correctly, including coordinates above 95
     (bytes above 127), **when a report arrives in one read.** If a report is split
     after `ESC [ M`, the rest arrives as key presses: a click at column 80 types `q`
     and quits. Needs an input filter in pqx (as `_terminal.py` is for Textual) or an
     upstream fix. Likely only over slow links; not seen in practice yet.
   - Pixel mouse mode (1016) is never enabled, so the iTerm2-over-ssh problem can't
     happen.
   - OSC 52 copy is supported (`tea.SetClipboard`).
   - Leaving the alternate screen doesn't clear it; pqx draws a blank last frame on
     quit, as Python pqx does, but not after a crash.
   - Not tried in real GNU screen or iTerm2.
6. **Grid speed: fine.** Building the 200×50 screen with 300 columns takes about
   95 µs; a whole frame through Bubble Tea's renderer 1.5–2.2 ms.
7. **Binary size and static linking.** See above. A fully static glibc build isn't
   practical (and glibc's static libraries aren't installed here); the usual route is
   static libstdc++/libgcc (needs `libstdc++-static`) and a build on an old glibc,
   i.e. a manylinux_2_28 container for Linux wheels and downloads. Leaving out the
   tpch/tpcds extensions would need a custom DuckDB build.

### Found along the way

- `duckdb-go`'s `Query`/`Exec`/`PrepareContext` run every statement in a string but
  the last. The data layer has DuckDB `Prepare` each filter query first (which refuses
  more than one statement without running anything) and accepts only one SELECT.
- Strings read from DuckDB pointed into memory DuckDB frees per batch; they are now
  copied.
- DuckDB auto-detects hive partitioning from the path (`year=2024/`), adding columns
  the Go data layer didn't expect; it now turns it off. (Python pqx works on such
  paths; checked.)
- arrow-go and DuckDB disagree on INT96 and nanosecond timestamps, JSON and ENUM;
  `Column.Type` comes from DuckDB and the plain view formats like DuckDB.

### Known gaps in the prototype

Decimals wider than 38 digits in filtered views (Python's `_fix_wide_decimals` not
ported); no DuckDB fallback for columns arrow-go can't read; file names with a
backslash next to glob characters can't be filtered; a filter whose table function
blocks in the OS (`read_csv('/some/fifo')`) leaves a goroutine and connection behind;
the split-X10 input issue above; wide characters (emoji) untested; macOS, Windows and
real-terminal checks not done; cold-cache times not measured.

### Recommendation: go

The Go version starts 10–14× sooner (0.15–0.3 s against 1.8–2.6 s), pages and jumps
2–4× faster, cancels sooner, and its data layer reads windows faster than PyArrow.
The binary would ship as one download, or as one PyPI wheel per platform with no
Python dependencies (about 30 MB compressed), so `pip install pqx` and `uvx pqx` keep
working. The risks found are bounded: the mouse input filter, the manylinux build, and
the data-layer gaps above, which Python pqx already solves and which port directly.
The cost is the rest of the UI (tabs, details pane, dialogs, formatting, themes),
roughly a release of work, in the order listed above.

## Test data

- **Real files (READ-ONLY, never write there):** `/sdf/data/rubin/user/mjuric/shutter-timing-ssp/rerun/2026-10-06/run/delivery/`
  - `SSSource.parquet`: 8.07M rows × 184 columns, 8 row groups of about 1M rows, ZSTD compression, no page index.
  - `mpc_orbits.parquet`: 1.58M × 53, 13 row groups.
  - `SSObject.parquet`: 298k × 80, 1 row group.
  - Smaller: `current_identifications`, `NearbySSO`, `numbered_identifications`.
  - These are on a network filesystem, so first reads are cold-cache.
- **Synthetic files:**
  - `python -m pqx.demo FILE --rows N` writes an LSST-like file.
  - `docs/screenshots/make_trips.py` writes the taxi-trip file used in the README.
  - The test fixtures in `tests/` build odd cases: many row groups, case-duplicate names, nested types, wide decimals, and hostile strings for the security tests.
- **Scale.** The user's typical files have tens to hundreds of millions of rows and hundreds of columns. Benchmark both many row groups and large row groups, and wide schemas (300+ columns), not just the demo file.

## Constraints and house rules

- **Workflow.** Read `CLAUDE.md` in the repo root.
  - Plan and get the user's approval before starting.
  - For big features: an integration branch with a draft PR and a design doc here.
  - Subagents each work in their own worktree, and every PR is reviewed.
  - Ask the user before merging anything into `master`.
- **Project map.** The user's global instructions keep a project map in `.project-map/` (gitignored), drawn by the `project-map` subagent. It is published at https://claude.ai/artifact/7PsPphtPmFwxwNJiCjRLmH; republish it after each update. Pass any question for the user to project-map, so it lands in `.project-map/decisions.md`.
- **Releases.**
  - 0.2.0rc1–rc3 are on PyPI. Master (with PR #26) is ahead of rc3.
  - 0.2.0 final, from Python, is due before **2026-10-19**, when GitHub moves `ubuntu-latest` to Ubuntu 26.
  - Release by `gh release create vX --target master …`; `publish.yml` uploads to PyPI by trusted publishing.
  - Yanking 0.1.0 is an open decision, to be left until 0.2.0 final is out.
- **Writing style.** The user likes plain, specific wording, in docs, PRs and messages alike.
- **Disk space.** `/lscratch` on this machine is nearly full. Keep build caches and benchmark files small and delete them when done.
