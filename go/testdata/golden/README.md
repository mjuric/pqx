# Golden files from Python pqx

Python pqx is the reference implementation of the Go port (design doc D7a). These JSON
files hold its outputs on fixed inputs and on the fixtures in `../fixtures/`; the Go tests
compare against them. `go/internal/golden` loads them.

## Regenerating

From the repository root, with a Python that has pqx's dependencies (the files here were
made with Python 3.12, PyArrow 25.0.1, DuckDB 1.5.6, NumPy 2.5, Rich 15.0, Textual 8.2):

```
PYTHONPATH=$PWD python go/testdata/golden/make_golden.py
```

`PYTHONPATH` makes it import this checkout's `pqx` (the script checks that it did). It
reads `../fixtures/`, so regenerate those first if they changed (`../make_fixtures.py`).
An optional argument writes the files to another directory. It takes about 10 s.

The script is deterministic: inputs are fixed or seeded and DuckDB runs with one thread
(`PQX_GOLDEN_THREADS` changes that), so two runs write the same bytes. The header of each
file records the versions used and `git describe` of the checkout (`pqx_git`).

## File layout

Each file is one JSON object: `"header"`, then one array per function or area
("section"), one record per line. Every record has an `id` (`"<section>/<n>"`), its
inputs under the parameter names of the Python function, and either the result in `out`
or, when Python raised, `"error": "<exception type>: <message>"`.

## Value encoding

Any cell or input value (Python `Any`) is a typed object:

| encoding | meaning |
|---|---|
| `{"t":"null"}` | NULL / `None` |
| `{"t":"int","v":"-12"}` | signed integer, as a decimal string |
| `{"t":"uint","v":"18446744073709551615"}` | unsigned integer (from an unsigned column, or above int64) |
| `{"t":"f64","v":"0.1"}` | float64: Python `repr`, or `nan`, `inf`, `-inf`; `-0.0` keeps its sign |
| `{"t":"f32","v":"0.10000000149011612"}` | a float32 (or float16) value, as the Python float it converts to |
| `{"t":"bool","v":true}` | |
| `{"t":"str","v":"..."}` | text (JSON escapes control characters) |
| `{"t":"bytes","v":"0102ff"}` | binary, hex |
| `{"t":"ts","v":"2026-01-02T03:04:05.123456000","unit":"us","tz":"UTC"}` | timestamp, 9 fractional digits; `tz` is `"UTC"` (an instant, shown in UTC) or `null` (naive); `unit` is the Arrow unit of the column it came from (`us` when unknown) |
| `{"t":"date","v":"2026-01-02"}` | date |
| `{"t":"time","v":"03:04:05.000000500"}` | time of day, 9 fractional digits |
| `{"t":"dur","v":"1500000000"}` | duration in nanoseconds |
| `{"t":"dec","v":"-30","scale":2,"precision":9}` | decimal: unscaled integer, scale, precision (−0.30) |
| `{"t":"uuid","v":"01234567-89ab-cdef-0123-456789abcdef"}` | UUID |
| `{"t":"list","v":[VALUE,...]}` | list |
| `{"t":"struct","v":[["field",VALUE],...]}` | struct (ordered fields) |
| `{"t":"map","v":[[KEY,VALUE],...]}` | map (ordered entries) |

What Python holds limits what is recorded: values that came through Python `datetime` or
`time` (`fetch` results, stats) stop at microseconds, so their last three digits are 0;
`file_table` reads integers and keeps nanoseconds. DuckDB returns durations as `int` counts
of the column's unit, UUIDs as `str`, and decimals wider than 38 digits as `f64`.

Rich `Text` (plots, `colorbar`) is `{"text": "...", "style": "<base style>", "spans":
[[start, end, "<style>"], ...]}`: character offsets into `text` (code points, not bytes),
styles as Rich's `str(Style)` (`"dim"`, `"color(201)"`, `"dim italic"`, `""` for none).
Adjacent spans of the same style are merged, so compare the style of each character, not
the span lists. Overrides (`override`) are plain JSON: `null`, an int, or a format-spec
string. Other floats that are always finite (plot inputs, `percent` inputs) are plain JSON
numbers; histogram edges and `xy_counts` limits use the `f64` string form.

## What each file covers

### fmt.json (`pqx/fmt.py`)

- `kind_for`: `name`, `type` (PyArrow's `str(type)`, e.g. `double`, `float`,
  `timestamp[ms, tz=UTC]`, `dictionary<values=string, indices=int32, ordered=0>`), `unit`
  → kind. Every fixture column (`source` names the file), then ~80 names × float64/float32 ×
  11 units, and each name × ~45 other types.
- `format_value`: one record per (value `v`, `kind`); `cases` are `[raw, width, override,
  output]`, output a string (or `{"error": ...}`). Every value × every kind × raw
  true/false × widths 0, 8, 40 without override, then 20 overrides (digits and Python
  specs, strftime) at width 40, some raw and at width 8. `safe` is true.
- `format_value_unsafe`: `safe=False` on hostile strings.
- `derived`: `name`, `kind` (= `kind_for(name, double, unit)`), `v`, `unit`.
- `mjd_to_iso`, `deg_to_hms`, `deg_to_dms` (`plus`): edge cases, rounding to 60, −0.
- `step_override` (`override`, `kind`, `delta`), `describe_override`, `override_error`
  (`value`, `kind` or null, `sample` or null; `out` is the message or null),
  `default_digits`, `percent`, `human_count`, `human_bytes`, `short_type`.
- `sanitize`: `s`, `keep_ws` → `out`, plus `has_controls`.
- `cell_formatter`: `CellFormatter(name, type, unit, override)` on each fixture's first 3
  rows: `kind`, `right` (right-justified), and per value and raw: `plain`, `style`
  (`""`, `"dim"`, `"bold"`), `justify` (`left`, `right`, `center`).

### cells.json (`pqx/cells.py`)

- `text_width`: `s` → `width` (terminal cells, Rich's measure) and `one_cell_per_char`;
  ASCII, CJK, emoji, flags, ZWJ sequences, combining marks, Hangul jamo, tabs (expanded to
  8), newlines (widest line), controls, the hostile strings and their sanitized forms.
- `text_width_formatted`: `format_value(v, kind, raw)` → `text` and its `width`.
- `widest_candidates`: `values`, `kind`, `raw`, `k` → list of values, or null (can't
  guess).

### plots.json (`pqx/plots.py`)

- `cmap_color` (`cmap`, `frac`, `dark_bg` → `#rrggbb`), `xterm256` (`hex` → index),
  `density_style` (→ style string), `fmt_density`, `sparkline`, `axis_labels`,
  `colorbar` (→ Text), `sky_shape` (→ `[w, h]`).
- `render_histogram`: `edges`, `counts`, `width`, `height`, `color`, `log_y`, `xlabel`,
  `log_x` → Text. Two cases raise `OverflowError` (log x of edges near 1.6e9: Python
  computes `10 ** 1.6e9`).
- `render_density`: `grid` (rows of counts, row 0 = lowest y), `xlim`, `ylim`, `cmap`,
  `dark_bg`, `xlabel`, `ylabel` → Text.
- `skymap_grids`: named (nlat, nlon) count grids (row 0 = dec −90, column 0 = ra 0),
  including the demo file's `sky_counts` at 2° and 1°. `render_skymap` records refer to
  one by `grid` (its id) with `width`, `height`, `cmap`, `dark_bg`, `center`, `caption`.

All plot calls use the default `accent="blue"` and `dim=Style(dim=True)` unless the
record says otherwise.

### data_common.json (`pqx/data.py`, `pqx/app.py`)

`parse_row_spec` (`text`, `total`), `check_select` (`sql` → the same text, or the error),
`where_sql`, `is_sql_query`, `idents` (`quote_ident`, `is_plain_ident`, `sql_ident`,
`sql_column_ref` per name, each `{"out"}` or `{"error"}`), `sql_text_literal` (and
`quote_str`), `path_literal`, `guess_sky_columns`, `filter_placeholder` (`columns`,
`types`, `row`), and `keywords` (DuckDB's keyword list, which `is_plain_ident` uses).

### data_\<fixture\>.json (`ParquetDataset` on `../fixtures/<fixture>.parquet`)

One per fixture: demo, odd, types, hostile, units, casedup, rowcol, nulname. A `view` is
`{"where", "order_by": [[column, descending], ...], "sql"}`.

- `file`: rows, row groups, columns, leaf columns, `created_by`, `has_file_row_number`
  (false when the file has its own `file_row_number` column: odd).
- `columns`: `name`, `arrow_type`, `nullable`, `unit`, `description` (field metadata,
  felis `"[unit] desc"` split), `short_type`, `kind`, `is_*`, `sql_name` (DuckDB's name:
  `name_1` for a case duplicate), `duckdb_type` (`DESCRIBE t`).
- `validate` (→ `[[name, arrow type], ...]` of the result), `count`.
- `fetch`: `view`, `offset`, `limit`, `columns` (null = all) → page: `offset`,
  `columns`, `types` (PyArrow type strings of the result, DuckDB's types), `row_numbers`
  (file rows, null for SQL results and for filtered/sorted views of odd), `rows` (typed
  values). Windows at the start, across row-group boundaries, at the end and past it; for
  non-trivial views also the view's middle and last rows. types: also the whole file.
- `fetch_columns` (`file_rows`, `columns`), `find_row` (`view`, `file_row` → position or
  null), `fetch_around` (`file_row`, `pos`, `offset`, `limit`, `columns` → `offset`,
  `row_numbers`, `first` row).
- `column_stats`: `view`, `column`, `sample` → `count`, `nulls`, `nans`, `distinct`,
  `min`, `max`, `mean`, `std`, `quantiles` (`[[q, value]]`), `top` (`[[value, n]]`),
  `sampled`.
- `histogram` (`column` plus `bins`, `lo`, `hi`, `log`, `temporal` as given → `edges`,
  `counts`), `sky_counts` (`lon`, `lat`, `res_deg` → (nlat, nlon) grid), `xy_counts`
  (`x`, `y`, `nx`, `ny`, `xlim`, `ylim` → `grid`, `xlim`, `ylim`).
- `column_chunk_summary` (per leaf path: physical type, compression, sizes, min/max from
  the footer statistics, nulls, `has_stats`, logical type), `row_groups`,
  `column_encodings` (sorted), `key_value_metadata` (including `ARROW:schema`).
- `sample_condition` (`sample`, `slices` → SQL text), `filter_placeholder` (the filter
  box's hint as the app builds it from the first row), `guess_sky_columns`.
- `file_table` (types, hostile, casedup): every column as PyArrow reads it, exactly
  (timestamps, times and durations to the nanosecond, decimal256 exact). `fsl` in types
  has `error`: PyArrow 25 can't read a fixed-size list with NULLs back.

## Comparing: what is exact and what isn't

- **Approximate in DuckDB**: `column_stats` `distinct` (`approx_count_distinct`) and
  `quantiles` (`approx_quantile`), and `xy_counts` limits when not given (quantiles). They
  depend on DuckDB's version and thread count; compare with a tolerance, or the exact
  parts only.
- **Floating-point sums**: `mean` and `std` change in the last digits with the thread
  count; compare with a relative tolerance (1e-9 is enough).
- **Ties in sorts without a row number**: odd's sorts on `x` (20 NaN tie, and pqx can't
  add the file row as a tiebreak there) come out in a different order with other thread
  counts. Compare those windows as sets per tie.
- **Error messages** from DuckDB (`Binder Error: …`) and Python (`Unknown format code …`)
  are recorded as they are; the Go port decides per case whether to match the text or
  only that it fails.
- **Python's limits, not pqx behaviour**: ns timestamps and times through Python are cut
  to µs; wide decimals are doubles (the Go port keeps them exact: an intended difference).

## Known oddities in the reference (recorded as is)

- `column_stats` of `f64` in types raises `OutOfRangeException: STDDEV_SAMP is out of
  range` (the column holds 1e300).
- `std` of `diaSourceId` (1.7e17 + row) is wrong (DuckDB's `stddev_samp` loses precision
  on large offsets: 11546.9 rather than 5773.6; with 8 threads 5913.8).
- `render_histogram` raises `OverflowError` with `log_x` on edges that aren't log10
  values.
