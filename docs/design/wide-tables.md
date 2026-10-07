# Fast rendering of wide tables

Status: approved 2026-10-06 · integration branch `wide-tables`

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

- **Subagents:** each runs the benchmark before and after their work, puts the
  numbers in their PR, commits and pushes per logical unit, keeps `pytest` and
  `ruff` green, and opens a PR into `wide-tables`.
- **Integrator:** has each PR reviewed independently (the reviewer re-runs the
  benchmark), sends fixes back, merges, and re-runs the suite and benchmark after
  each merge.
- **Release:** the user approves merging into `master`.
