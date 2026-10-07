# Fast rendering of wide tables

Status: approved 2026-10-06 · built 2026-10-07 (PRs #12–#17 into `wide-tables`, #11 into `master`).
See [As built](#as-built) for what was implemented and the results.

## Problem

On a 300-column file the grid takes 0.6 s to move one column sideways, 1.5 s to
page down and 2.8 s to load a new window of rows. `bench/grid_bench.py` measures
this (CPU ms per operation, 200×50 terminal, 300 float columns × 5000 rows; the
16-column row is the regression guard):

| operation | 300 cols now | 300 cols target | 16 cols now (≤10 % slower allowed) |
|---|---|---|---|
| startup | 2197 | ≤ 1000 | 764 |
| ↓ | 42 | ≤ 25 | 12 |
| → | 582 | ≤ 50 | 11 |
| PgDn | 1567 | ≤ 100 | 74 |
| Ctrl+End (new window) | 2827 | ≤ 400 | 503 |
| Ctrl+Home | 1874 | ≤ 400 | 419 |
| End / Home | 979 / 1014 | ≤ 100 | 12 / 36 |
| `f` (raw toggle) | 1774 | ≤ 150 | 362 |
| ↓ with Details open | 137 | ≤ 50 | 28 |

## Causes (profiled)

1. **Off-screen columns are rendered.** Textual's
   `DataTable._render_line_in_row` builds each row line from every column (300),
   though about 15 fit on screen; `_render_line` then crops to the viewport. Its
   row-line cache key includes the cursor and hover positions, so every move
   rebuilds every visible line, and its cell cache is too small for 300-wide rows,
   so cells miss too. This is 70–95 % of the time in every slow operation.
2. **Every cell is formatted up front.** `_apply_page` formats the whole window,
   150 rows × 300 columns = 45,000 Rich `Text`s, and DataTable measures each for
   auto-width. `f` and format changes redo all of it. Only ~700 cells are ever on
   screen at once.
3. **The Details pane renders all entries.** On each row change it rebuilds and
   re-renders a Rich table for every column (300), not just the ones in view.

## Design

### A. Render only visible columns (`GridTable`)
- Override `_render_line_in_row` in `GridTable`. Scrollable columns entirely
  outside `[scroll_x, scroll_x + width)` contribute blank segments of their width;
  visible and partly visible ones render as before. Row labels and pinned
  (fixed) columns always render. `_render_line`'s crop is unchanged.
- Cache keys must stay correct: include the visible column range in the
  row-line cache key (or clear on horizontal scroll and resize). Cursor, hover,
  header row, sort arrows, focus and blur must look exactly as today.
- Size Textual's cell and line caches for the visible area, so they don't thrash.
- Pin `textual>=8.2,<9` (done on the integration branch). Add a test that fails
  loudly if Textual's `_render_line_in_row` signature changes.
- Add a deterministic guard test: count `_render_cell` calls for one sideways
  and one down move on a wide table, and assert they scale with visible columns,
  not total columns.

### B. Format cells lazily (page load path)
- Store cells so that formatting happens when a cell is first drawn, then
  cached: for example a light cell object whose Rich rendering formats on demand.
  `get_cell_at` (and `str()` of it) must still give the formatted text; tests
  rely on it.
- Column widths come from the header plus a sample of the window's values, not
  from measuring every cell. **Numbers must never be cut off**: if a later cell
  is wider than its column, the column grows. Widths only grow, as today, so
  nothing jumps.
- `f` (raw), format overrides (`<`, `>`, `F`) and pages that only change rows
  must invalidate formatting and widths cheaply, not rebuild 45,000 cells.
- Profile the rest of the window-load path and fix what dominates it.

### C. Details pane: render only what's in view
- On a row change, avoid re-rendering entries that aren't in view. For example,
  update the prompts of the visible entries now and the others when they scroll
  into view, or render lazily in `DetailList`. Keep the scroll position, the
  selection and the focus look exactly as today.

### D. Fetch a window in time that doesn't grow with the file (added during integration)
Found while reviewing B: on a 300-column file with 1000-row row groups, fetching a
150-row window costs 40 ms at 5k rows but 280 ms wall / 1.4 s CPU at 200k rows,
whatever the window size or offset. DuckDB sets up a scan across every column
chunk of every row group per query; the `file_row_number` filter doesn't prune
row groups.
- For trivial views (no filter or sort), map the window's offset to row groups
  from the footer and read only those, e.g. with pyarrow
  `read_row_groups(..., use_threads=False)` (~5 ms). pyarrow's default threading
  took 287 ms wall and 20 s CPU for one row group on a 128-core node, so keep it
  off or bounded.
- Filtered or sorted views still use DuckDB; keep their behaviour.
- Results must be identical to today's: values, types (timestamps, decimals,
  nested, dictionary-encoded strings), `file_row_number` labels and NULLs.

### E. Fetch only the columns in view (added during integration)
Real LSST files (e.g. SSSource: 8M rows × 184 columns in ~1M-row row groups, no
page index) are dominated by decoding inside big row groups, and that cost
follows the bytes of the columns requested: on SSSource, 150 rows in the middle
of a row group take 481 ms with all 184 columns and 55 ms with 20.
- The grid loads pinned columns plus the visible ones ± one screen. Missing
  columns are fetched when scrolling reaches them, with
  `ds.fetch_columns(page.row_numbers, missing)` under their own tag, and merged
  into the current page.
- **Staleness:** merge only if a page-generation counter still matches. A new
  page load cancels outstanding column fetches; different tags don't cancel each
  other, so cancel explicitly.
- **Pages without row ids** (`ds.has_row_ids(view)` is False: SQL results, and
  filtered/sorted views of files with their own `file_row_number`) keep fetching
  all columns.
- **Details pane:** it shows every column, so fetch its missing columns for the
  whole page once (debounced, own tag) and cache them; never per cursor row.
- Column widths for not-yet-loaded columns must not jump when data arrives:
  reserve them from metadata/statistics or the header. Copy (`y`), `=`, export,
  stats and the Details pane need a column's data before using it.
- Don't inherit the race fixed in #15: a superseded column fetch must never
  overwrite newer data.

### F. Startup independent of row-group count (added during integration)
On a 2000-row-group × 300-column file, startup takes 13.9 s before the grid
responds: `column_chunk_summary` walks 600k column chunks in Python (8.9 s) and is
called twice (Schema and Metadata tabs) on the UI thread in `on_mount`; opening
the dataset takes another 3.1 s.
- Compute the summary once, cached, without per-chunk Python overhead where
  possible, and build the Schema/Metadata tabs off the critical path (a worker,
  or lazily on first view) so the grid is usable first.
- Profile `ParquetDataset.__init__` and fix what dominates it.
- Target: the grid is interactive in ≤ 1 s on that file, and ≤ 1 s on SSSource;
  the Schema and Metadata tabs fill in as soon as their data is ready.

### Not changing
Behaviour and look: everything must work as now, including cursor, hover,
pinning, sorting, header clicks, hidden-column markers, the format header
markers, the Details pane link and the linked tabs. The existing tests must
pass unchanged, unless a test reaches into DataTable internals that the change
replaces.

## Plan

| Branch (from `wide-tables`) | Owner | Area |
|---|---|---|
| `wide-tables` | integrator | this document, `bench/grid_bench.py`, Textual pin |
| `wide-tables-render` | subagent A | section A: `GridTable` rendering, caches, guard tests |
| `wide-tables-load` | subagent B | section B: `_apply_page`, cell storage, widths, raw/format invalidation |
| `wide-tables-detail` | subagent C | section C: `DetailList` / `_update_detail` |
| `wide-tables-fetch` | subagent D | section D: `ParquetDataset.fetch` for trivial views |
| `wide-tables-lazycols` | subagent E | section E: column-lazy fetching in the grid and Details pane |
| `wide-tables-startup` | subagent F | section F: startup cost (`column_chunk_summary`, tab building, dataset open) |

- **Subagents:** each runs the benchmark before and after their work, puts the
  numbers in their PR, commits and pushes per logical unit, keeps `pytest` and
  `ruff` green, and opens a PR into `wide-tables`.
- **Integrator:** has each PR reviewed independently (the reviewer re-runs the
  benchmark), sends fixes back, merges, and re-runs the suite and benchmark after
  each merge.
- **Release:** the user approves merging into `master`.

## As built

### Results

`bench/grid_bench.py`, 300 float columns × 5000 rows, CPU ms per operation (the
16-column file is unchanged within noise):

| operation | before | after | target |
|---|---|---|---|
| startup | 2197 | 550 | ≤ 1000 |
| ↓ | 42 | 11 | ≤ 25 |
| → | 582 | 26 | ≤ 50 |
| PgDn | 1567 | 60 | ≤ 100 |
| Ctrl+End | 2827 | 153 | ≤ 400 |
| Ctrl+Home | 1874 | 95 | ≤ 400 |
| End / Home | 979 / 1014 | 46 / 49 | ≤ 100 |
| `f` | 1774 | 57 | ≤ 150 |
| ↓ with Details | 137 | 36 | ≤ 50 |

Real and large files (wall time, `bench/startup_bench.py` and ad-hoc timing):

- SSSource (8M × 184, 1M-row row groups): grid usable in ~0.36 s; a page in
  the middle of a row group 359 → 65 ms; jump to a far window ~920 → ~380 ms.
- 2000 row groups × 300 columns: grid usable 10.4 → 1.2 s, Schema/Metadata
  filled at ~3.4 s with no UI stall over ~100 ms. **Misses the 1 s target**: the
  rest is DuckDB's own footer parse, needed for the result types; avoiding it
  would mean predicting DuckDB's types without asking it.
- Small row groups: a new window 250 ms–2 s → ~11 ms.

Trade-offs: End on a fresh SSSource page costs ~+125 ms (far columns load on
demand); the Details pane on a fresh mid-file SSSource page shows `…` for ~1 s
while the page's other columns load; startup CPU on small files is ~60 ms
higher (threads running in parallel), wall time is not.

### A. Rendering (`GridTable` in `pqx/app.py`)

`GridTable` keeps Textual's `DataTable` and overrides private methods:

- `_render_line_in_row` bisects cached column start/end positions
  (`_column_geometry`) for the columns overlapping the crop span
  `[scroll_x + fixed_width, scroll_x + width)`, renders only those and stands in
  one blank `Segment` of exact width on each side, so `_render_line`'s crop is
  unchanged. Row labels and pinned columns always render; tables under three
  screens wide render whole so their lines stay cached while scrolling sideways.
- Its row-line cache key holds the cursor/hover only for the rows they touch,
  plus the visible column range, a widths generation and the widget width: a ↓
  re-renders two rows. Cell, row and line LRU caches are grown to a large
  terminal's worth.
- `render_lines` recomputes the geometry each frame and clears the caches when
  any width changed (DataTable re-measures on idle without bumping
  `_update_count`). `ordered_columns` is memoized.
- `_render_cell` has a fast path for one-line plain `Text` (no spans, one
  terminal cell per character): pad/body/pad segments are built directly, with
  styles learnt once per style combination from a one-character Rich render
  (`_cell_styles`). Anything else goes through Rich.
- Guards: `tests/test_render.py` pins a SHA-256 of each overridden upstream
  method (`UPSTREAM_SOURCE`); `GridTable.render_all_columns = True` restores the
  upstream path and tests compare output line by line against it (cursor, hover,
  pinning, header, focus/blur); `_render_cell` call counts must scale with
  visible columns. `textual>=8.2,<9` is pinned: each Textual major needs a
  deliberate port.

### B. Cells formatted on first draw (`pqx/cells.py`)

- `ColumnCells`: per column, the formatter, raw flag, a generation `gen` and
  DataTable's `Column`. `Cell(value, col)` formats in `.text` on first access,
  caches against `col.gen`, and calls `col.fit(width)`, which only grows
  `content_width`. `__rich__`/`__str__` delegate to `.text`.
- `CellRow(dict)` is what DataTable stores per row; `__missing__` makes a `Cell`
  on first read. `RowCells` is a lazy positional view handed to DataTable by the
  `_compute_row_renderables` override.
- `GridTable.set_rows()` replaces `add_row`: bulk insert into DataTable's
  `_row_locations`/`_data`/`rows`, no measuring.
- `f`, `<`, `>`, `F` call `ColumnCells.invalidate()` (`gen += 1`): every cell of
  the column re-formats when next drawn; no cell objects are touched.
- Widths: `_fit_columns` fits each column up front to `widest_candidates` (the
  values likely to format widest) or a sample of rows. `fit_visible()` runs on
  every scroll and formats the cells about to be drawn before the draw,
  re-measuring if any column grew and scrolling the cursor back into view if
  that pushed it off screen — so a number is never shown cut off.

### C. Details pane (`pqx/widgets.py`)

Entries are `_LazyEntry` Visuals: a one-line value that fits reports height 1
without building anything; others are measured by rendering. `EntryGrid` lays
out `name  value` as `Table.grid(padding=(0, 2), expand=True)` would, without
Table's measuring passes (falls back to a `Table` when too narrow).

### D. Window fetch (`ParquetDataset.fetch` in `pqx/data.py`)

- Trivial views try `_fetch_direct` → `_read_rows`: `_rg_starts()` + `bisect`
  map the window to row groups and local ranges; `_read_rg` opens a fresh
  `pq.ParquetFile` with the parsed footer (< 1 ms, nothing shared between
  threads) and streams `iter_batches(row_groups=[rg], columns=…,
  use_threads=False)`, skipping to the window and stopping when it has enough.
  A 1 MB `buffer_size` bounds reads; small chunks use `pre_buffer`.
- Identical results: a background thread binds DuckDB's Arrow types once
  (`_bind_types`); pyarrow's result is cast to them only where `_castable` says
  it's lossless. A column pyarrow can't read or cast goes to DuckDB from then on
  (`_exclude_unreadable`); failing row groups go to `_bad_rgs`. INT96 is read as
  µs, like DuckDB. `_fix_wide_decimals` re-reads decimals wider than 38 digits
  directly, since DuckDB 1.5 misreads them.
- Cost model `_direct_estimate`: pyarrow ≈ uncompressed bytes up to the last
  needed row × `_PA_NS_PER_BYTE`; DuckDB ≈ base + per column + per row group in
  the file (scan setup) + skipping at `_DUCK_SKIP_RATIO` of pyarrow's decode.
  Fitted on synthetic and real files, refitted in #16 with DuckDB's metadata
  cache on.
- Cancellation: direct reads register a `_Cancel` token under the caller's tag
  (checked between batches); `_handover` swaps it for a DuckDB cursor atomically
  and raises if the read was superseded, so a stale fetch can't outlive a newer
  one.
- New API: `fetch_columns(file_rows, columns)` (contiguous rows read as a range,
  scattered via `take`), `has_row_ids(view)`, `window_cost(offset, limit,
  columns)`.

### E. Lazy columns (`pqx/app.py`)

- `Page.missing` lists unfetched columns; their values are `MISSING` (dim `…`)
  or, after a failed fetch, `UNAVAILABLE` (red `✗`).
- `load_window` bumps `_page_gen`, cancels every column fetch (tags don't
  interrupt each other), and, if the view has row ids, fetches only
  `grid.columns_near(target)` (pinned + one screen each side). **Deviation:**
  plain views go lazy only if `window_cost` says that saves ≥
  `LAZY_MIN_SAVING_MS` (20 ms); always-lazy made small files slower. Filtered
  and sorted views are always lazy.
- `_apply_page` drops a page whose `gen` isn't current. `_ensure_columns` (on
  `HScroll`, page apply, fetch completion, pinning, page failure) fetches the
  missing columns within two screens when any within one is missing, under tag
  `cols`; `_inflight[tag] = (seq, gen, names)` tracks fetches, and columns the
  Details fetch will bring are skipped.
- `_columns_done` merges only if the result is its tag's latest, `gen` is
  current and `page is self.page`, and fills only columns still missing.
  `CellRow.set_values` drops just those cells; if no width changed,
  `GridTable.invalidate_columns` discards just their cached renderings (column
  key is element 1 of the cell-cache key, pinned by a test).
- Failures: an Esc-cancelled fetch sets `_cols_cancelled` and the next cursor
  move retries; a failed fetch marks the columns `UNAVAILABLE` until the next
  page; `_page_failed` restarts the fetches of the page still on screen.
- `_reserve_widths` sizes missing columns from the page's row-group min/max
  statistics (formatted) or a typical width for the kind.
- Details fetches all of a page's missing columns once (debounced, tag
  `detail`). `y`, `=`, `F` go through `_with_cursor_value`: a per-column
  `cell:<name>` fetch with queued actions (`_cell_waiters`).
- Views without row ids (SQL results; filtered/sorted views of files with their
  own `file_row_number`) fetch every column.

### F. Startup (`pqx/data.py`, `pqx/app.py`)

- `ParquetDataset.__init__` starts the `pqx-types` thread first: DuckDB parses
  the footer into its cache (`parquet_metadata_cache` in the connect config)
  while pyarrow parses it on the main thread; `_schema_known` then lets it
  create the views and bind types. `ds.con` is a property that waits on
  `_ready` and re-raises a setup error; `__init__` sets the event and joins the
  thread on any failure.
- `ds.setup_error`: a file DuckDB can't read opens with a clear status line and
  no queries; the pyarrow-based Schema/Metadata tabs still work.
  **Behaviour change**: it used to exit with "cannot open".
- One cached footer pass (`_footer_scan`) serves `column_chunk_summary()` and
  `row_groups()`, comparing `min_raw`/`max_raw` for plain bool/int/float and
  leaving encodings to `column_encodings`; `stop()` is checked per row group and
  raises `Stopped` without caching.
- Schema/Metadata start as "Reading …". The `read_footer` thread worker waits
  for the first page and one refresh, runs the pass (`stop=worker.is_cancelled`,
  so quitting is immediate), builds the row-group cells, and the UI adds them
  250 rows per event-loop turn.
