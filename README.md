# pqx — a terminal explorer for Parquet files

`pqx` is an interactive, keyboard-driven terminal UI for looking inside
Parquet files: browse rows, filter with SQL, profile columns, draw sky maps and
density plots, and export subsets. It is built for large LSST catalogs
(SSSource, SSObject, DiaSource, …) but works with any single Parquet file.

Nothing is loaded in full. The grid pulls small windows of rows, and every
aggregate (counts, statistics, histograms, sky maps) runs inside
[DuckDB](https://duckdb.org). A multi-GB file opens instantly, and jumping to
row 3,000,000,000 costs one row-group read.

```
pip install -e .          # or: pip install -e ".[dev]" for tests
pqx catalog.parquet
pqx sssource.parquet --where "ssObjectId = 9000123"
python -m pqx.demo demo.parquet --rows 1000000   # a synthetic LSST-like file to play with
```

## What you get

| Tab | |
|---|---|
| **Data** | A fast, scrollable grid over the entire file. Headers show type and unit, and values use astronomy-aware formatting. **d** opens a detail panel with every column of the current row at full precision, plus derived readings: MJD → UTC date, RA/Dec → sexagesimal, errors in mas, flux → AB mag. |
| **Schema** | Every column with its type, unit, description (from Parquet field metadata, including Felis-style `"[unit] description"`), null count, min/max from row-group statistics, compressed size, compression ratio and encodings. |
| **Stats** | Pick a column to see count, nulls, NaNs, distinct values, min/max, mean/std and quantiles, with a histogram for numeric and time columns or a top-values bar chart for categorical ones. |
| **Plot** | **Sky (Mollweide)** maps of any lon/lat pair, with RA/Dec auto-detected, and **density scatter** plots of any two numeric columns, both drawn in text with sub-character resolution and log-density colour. |
| **Metadata** | File overview, row-group table and key-value metadata, with JSON shown pretty-printed. |

The **filter bar** (press `/`) takes either

* a SQL `WHERE` expression: `mag < 21 and band = 'r'`, `ssObjectId is not null`,
  `ra between 10 and 20`; or
* a full query over the table `t`: `select band, count(*), avg(mag) from t group by 1`.

The filter applies everywhere: grid, stats, plots and export. Column names
auto-complete (→ accepts), ↑/↓ recall history, and errors appear inline
without losing the current view.

## Keys

| key | action |
|---|---|
| `/` · `x` / `Ctrl+X` | edit filter · clear filter (`Ctrl+X` also works while typing in the filter box) |
| arrows, PgUp/PgDn, Ctrl+Home/End | move; the row window slides seamlessly |
| `g` | go to row: `1234`, `1.5M`, `50%`, `-1` |
| `s` | sort by the cursor column (asc → desc → off); clicking a header does the same |
| `=` | narrow the filter to rows equal to the cursor cell |
| `d` / Enter | row detail panel |
| `c` · `-` · `p` | choose columns · hide column · pin columns |
| Home / End | first / last column; `‹` `›` beside the header mark hidden columns (click to page) |
| `f` · `y` · `i` | raw/smart formatting · copy cell · stats for column |
| `1`–`5` · `Ctrl+←` `Ctrl+→` | go to a tab · previous / next tab (or click a tab name in a panel border) |
| `e` | export the current view (filter + sort + visible columns) to Parquet/CSV/JSON |
| `m` | toggle sampling for stats and plots |
| `l` `L` `[` `]` | Stats: log counts, log values, fewer/more bins |
| click · `enter` | Plot: open a settings field's drop-down (type to narrow the column list) |
| `tab` · `← →` | Plot: move between settings fields · step the field's value |
| `r` | Plot: rotate the sky-map centre between RA 0° and 180° |
| Esc | cancel running queries / leave the filter bar |
| `?` · `q` | help · quit |

## Look

pqx draws with your terminal's own background and 16-colour palette, so it
matches whatever scheme you use, and it looks the same with or without 24-bit
colour. Panels are thin boxes, the focused one in the accent colour. Colour is
kept for things that mean something: the file name and other object names are
cyan, and status lines use ✓ (green) for done, ! (yellow) for warnings such as
sampling, ✗ (red) for errors, and ⠸ while running. The style follows acid's CLI
design language.

| option | env | |
|---|---|---|
| `--accent blue\|cyan\|magenta\|green\|yellow` | `PQX_ACCENT` | focus colour (default blue) |
| `--dim faint\|bright-black` | `PQX_DIM` | secondary text: the faint attribute (default), or ANSI bright black for terminals that ignore faint |
| `--border NAME` | `PQX_BORDER` | unfocused panel border, an ANSI colour name (default `bright_black`) |
| `--theme NAME` | | use a Textual theme instead of the terminal's colours |

Sky maps and density plots default to **magma**; the colormaps (magma,
viridis, inferno, plasma, gray) are emitted as exact xterm-256 colours, and
`terminal` draws density with your palette alone (faint, accent, bold).

## Large files

* **Seeking.** When no filter or sort is active, a window is fetched with DuckDB's
  `file_row_number` pushdown. Only the row group(s) holding the window are read,
  so paging is O(1) in file size (about 20 ms per window on a 30M-row / 850 MB file).
* **Filtered and sorted views** use `LIMIT/OFFSET` over the query. The total
  row count is computed in the background, and the grid is usable before it
  arrives.
* **Cancellation.** A new filter, stats request or plot interrupts the
  now-stale DuckDB query instead of letting it run to completion. Esc cancels
  everything that's running.
* **Sampling** (`m`, `--sample`) makes stats and plots read about 2M rows from
  up to 16 evenly spaced row groups and skip the rest. It turns on
  automatically for files over 200M rows or 8 GiB, and is worth enabling
  whenever the storage is slow (network file systems, cold caches). Sampled
  results are marked as such.
* `--threads N` caps DuckDB's parallelism on shared machines.

## Sky maps

The Mollweide renderer is a port of the one in
[acid](https://github.com/mjuric/acid) (`acid/io/skymap_art.py`), adapted to
work from an equirectangular count grid, which DuckDB bins in a single
`GROUP BY`, instead of a HEALPix map:

* each character cell is inverse-projected, so rendering cost doesn't depend on row count;
* **area** is carried by glyph shape: 2×2 quadrant sub-cells light up when at
  least half of a sub-cell is covered, so partial coverage draws as partially
  filled glyphs;
* **density** is carried by colour: log surface density in deg⁻² through a
  selectable colormap (magma by default; viridis, inferno, plasma, gray, or
  `terminal`), with a colorbar legend;
* the limb and graticule are a braille outline, and RA increases to the left.

## Development

```
python -m venv .venv && .venv/bin/pip install -e ".[dev]"
.venv/bin/pytest          # data layer, formatting/plots, and headless UI tests (Textual pilot)
```

Layout: `pqx/data.py` (DuckDB/PyArrow access layer), `pqx/fmt.py`
(astronomy-aware formatting), `pqx/plots.py` (text plots), `pqx/app.py` +
`pqx/screens.py` + `pqx/app.tcss` (the Textual UI), `pqx/demo.py` (synthetic data).
