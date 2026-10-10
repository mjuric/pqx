# Test data for the Go port

- `fixtures/`: small Parquet files, checked in. Written by `make_fixtures.py`.
- `golden/`: JSON outputs of Python pqx (the reference) on fixed inputs and on these
  fixtures. Written by `golden/make_golden.py`; see `golden/README.md`.

## Regenerating the fixtures

From the repository root, with a Python that has pqx's dependencies (PyArrow 25 was used):

```
PYTHONPATH=$PWD python go/testdata/make_fixtures.py
```

The script is seeded and fixed throughout. With the same PyArrow version two runs write
byte-identical files (checked with PyArrow 25.0.1). Another PyArrow version changes the
footer's `created_by` and may change the encoding, but not the data. After regenerating,
regenerate the golden files too.

The repository's `.gitignore` ignores `*.parquet`; `fixtures/.gitignore` lets these in.

## The fixtures

Row numbers are file rows, from 0. "NULL" is a Parquet null; "NaN" is a float NaN
(not null). Parquet has no seconds unit: PyArrow stores `timestamp[s]` and `time32[s]` as
milliseconds, so those columns read back as `ms`.

| file | rows | row groups | compression | size |
|---|---|---|---|---|
| `demo.parquet` | 20,000 | 8 × 2,500 | ZSTD | 1.4 MB |
| `odd.parquet` | 1,000 | 300, 300, 300, 100 | Snappy | 45 kB |
| `types.parquet` | 60 | 8 × 7, then 4 | Snappy | 125 kB |
| `hostile.parquet` | 20 | 1 | Snappy | 80 kB |
| `units.parquet` | 500 | 200, 200, 100 | Snappy | 157 kB |
| `casedup.parquet` | 3 | 1 | Snappy | 1 kB |
| `rowcol.parquet` | 2 | 1 | Snappy | 1 kB |
| `nulname.parquet` | 2 | 1 | Snappy | 1 kB |

### demo.parquet

`pqx.demo.make_table(20_000, seed=7)`, as `tests/conftest.py`'s `demo_path`: a synthetic
LSST DiaSource table. Field metadata `description` holds the felis form `"[unit] text"`
(e.g. `ra`: unit `deg`, description `Right ascension coordinate of …`). File key-value
metadata: `table` = `DiaSource`, `description`, `generator` = `pqx.demo`, plus `ARROW:schema`.

| column | type | facts |
|---|---|---|
| `diaSourceId` | int64 | 170000000000000000 + row: unique, sorted |
| `ssObjectId` | int64 | 13,934 NULLs; others ≥ 1,000,000 |
| `ra` | double | 0.031 … 359.948 |
| `dec` | double | −88.95 … 38.36 |
| `raErr`, `decErr` | float | 1e-6 … 3e-5 |
| `midpointMjdTai` | double | 60800 … 61200, sorted ascending |
| `band` | dictionary<int32, string> | `u g r i z y` |
| `psfFlux` | float | 36 NaN (first at rows 424, 647, 1676, 1781, 1943); no NULLs |
| `psfFluxErr` | float | |
| `mag` | float | 17.28 … 26.0 |
| `snr` | float | 5 … 100 |
| `trailLength` | double | 11,921 NULLs (first at rows 4, 5, 7, 8, 10); no NaN |
| `isDipole` | bool | |
| `detector` | int16 | 0 … 188 |
| `ingestTime` | timestamp[ms, tz=UTC] | |

### odd.parquet

Exactly `tests/conftest.py`'s `odd_path`.

| column | type | facts |
|---|---|---|
| `file_row_number` | int64 | row × 10. Its name collides with DuckDB's `file_row_number`, so pqx can't number rows in filtered or sorted views (`has_file_row_number` is false in the golden files) |
| `weird name` | int64 | 0, 1 or 2 (a name with a space) |
| `x` | double | NaN at rows 0, 50, 100, … (20 NaN); +inf at row 1 |
| `tags` | list<string> | `[]`, `["a"]`, `["a","b"]` by row % 3 |
| `pos` | struct<ra: double, dec: double> | ra = row, dec = −row/20 |
| `blob` | binary | byte (row % 256) repeated row % 20 times |
| `day` | date32 | 2026-01-01 + row days |
| `allnull` | int64 | all NULL |

### types.parquet

A zoo of types, widened from `tests/test_fetch.py`'s `_zoo`. **Every column except `null`
is NULL at rows where `row % 11 == 10`** (rows 10, 21, 32, 43, 54). Row groups of 7 rows,
so most windows cross row groups.

| column | type | values (where not NULL) |
|---|---|---|
| `i8` `i16` `i32` `i64` | signed ints | row 0 = the type's minimum, row 1 = its maximum, then `scale × (row − 30)` (scales 1, 1000, 7e7, 3e17) |
| `u8` `u16` `u32` `u64` | unsigned ints | row 0 = 0, row 1 = the maximum (u64: 18446744073709551615), then `scale × row` (u64: 2^63 + row) |
| `f16` | halffloat | float16(row / 7) |
| `f32` | float | row / 3, except rows 0, 9, 18, … (row % 9 == 0) cycling NaN, +inf, −inf |
| `f64` | double | cycles by row % 10: NaN, +inf, −inf, 1.5, −0.0, 0.1, 1e300, −1e-300, 5e-324, 123456789.12345679 |
| `bool` | bool | row odd |
| `str` | string | `""` when row % 4 == 0, else `s<row>` |
| `lstr` | large_string | `é<row>漢` |
| `dict` | dictionary<int32, string> | `a b c` by row % 3 |
| `bin` | binary | byte row repeated row % 5 times (`""` at row 0) |
| `fbin` | fixed_size_binary[4] | `[row, 255−row, 0, 7]` |
| `uuid` | extension<arrow.uuid> | UUID(int = row × 0x0123456789ABCDEF0123456789ABCDEF mod 2^128) |
| `date` | date32 | 2020-01-01 + 37 × row days |
| `ts_s` | timestamp[s] stored as [ms] | −2e9 s + 97 days × row (starts 1906-08-16) |
| `ts_ms` `ts_us` `ts_ns` | timestamp[ms/us/ns] | around 2020-09-13T12:26:40; `ts_ns` has nanoseconds (.123456789 + row ns) |
| `ts_s_utc` `ts_ms_utc` `ts_us_utc` `ts_ns_utc` | timestamp with tz UTC | `ts_s_utc` is stored as ms; `ts_ns_utc` ends in 001 ns |
| `ts_tz_ny` | timestamp[ns, tz=America/New_York] | instants; DuckDB shows them in UTC |
| `ts_off` | timestamp[ms, tz=+02:00] | 2026-01-01T10:00Z + row minutes |
| `t32_s` | time32[s] stored as [ms] | 1439 s × row |
| `t32_ms` `t64_us` `t64_ns` | time | row seconds + 7 ms / 5 µs / .123456789 |
| `dur_s` `dur_ms` `dur_us` `dur_ns` | duration | DuckDB reads durations as int64 counts of the unit |
| `dec9_2` | decimal128(9, 2) | (row − 30) / 100 |
| `dec10_0` | decimal128(10, 0) | row × 123456789 |
| `dec18_6` | decimal128(18, 6) | ± row × 1234.567891, sign alternating |
| `dec38_3` | decimal128(38, 3) | ±(1e37 + row) / 1000, sign alternating: 38 digits |
| `dec38_38` | decimal128(38, 38) | row × 1e-38 |
| `dec50_2` | decimal256(50, 2) | (row − 30) / 100 |
| `dec76_10` | decimal256(76, 10) | ±(1e64 × row + row) / 1e10 |
| `list` | list<int64> | `[row]` repeated row % 3 times |
| `llist` | large_list<double> | `[]`, `[row]`, `[row, NULL]` by row % 3 |
| `fsl` | fixed_size_list<int32>[2] | `[row, row+1]`. **PyArrow 25 can't read this column back** (its NULLs: "Expected all lists to be of size=2"); DuckDB can. pqx falls back to DuckDB for it |
| `lstr_list` | list<string> | prefixes of `["a\x1bb", "", NULL]` (an ESC inside a list) |
| `struct` | struct<a: int64, b: string, c: list<double>> | `{a: row, b: "x<row>", c: [1.0, row]}`; all fields NULL when row % 5 == 0 (the struct itself is not NULL there) |
| `lstruct` | list<struct<k: string, v: int16>> | one element `{k: "k<row>", v: row or NULL for odd rows}` |
| `map` | map<string, int64> | `{"k<row>": row}`, plus `"z": NULL` on odd rows |
| `null` | null | all NULL |
| `json` | extension<arrow.json> | `{"a": <row>}` |
| `a.b` | int64 | row (a dot in the name) |
| `q"uote` | int64 | 2 × row (a double quote in the name) |

DuckDB reads `decimal256` wider than 38 digits (`dec50_2`, `dec76_10`) as wrong doubles;
Python pqx re-reads them with PyArrow and shows them as (correct) doubles. The Go port
keeps them exact (`Decimal`), which is an intended difference.

### hostile.parquet

`tests/test_security.py`'s `make_evil` file, with more strings. Column `a` is the row
number; `s`, `select` (a keyword) and `[bold]mk[/] [@click=app.quit]x[/]` (markup) hold
the same 20 strings:

| row | string |
|---|---|
| 0 | `it's "quoted" \ back'slash` |
| 1 | `x' ); COPY (SELECT 1) TO '/tmp/pqx-fixture-pwned.txt'; --` |
| 2 | OSC 0 window title (`ESC ]0;PWNED-TITLE BEL`) + `TITLE-VAL` |
| 3 | OSC 52 clipboard write + `CLIP-VAL` |
| 4 | OSC 8 hyperlink |
| 5 | Rich markup with `@click` and `link` |
| 6 | `[/]` |
| 7 | `ESC[2J ESC[31m RED` (clear screen, red) |
| 8 | C1 CSI (U+009B) and C1 OSC (U+009D) |
| 9 | `tab\there\nnew line` |
| 10 | a NUL in the middle |
| 11 | RIGHT-TO-LEFT OVERRIDE (U+202E) |
| 12 | zero-width space, ZWJ, word joiner, BOM |
| 13 | C1 NEL (U+0085) and U+009D |
| 14 | DEL |
| 15 | bidi isolates, LRM, RLM, ALM |
| 16 | `é ✓ 漢字 😀` and a combining acute accent |
| 17 | `[bold]` |
| 18 | `trailing backslash\` |
| 19 | `""` (empty) |

Other columns (int64 = row unless noted): `esc␛]0;PWNED-TITLE␇name` (ESC and BEL in the
name), `c1\x9b2Jname` (C1 CSI in the name), a name that is SQL (`random() > -1)); COPY …`),
`bidi‮name​` (RLO and ZWSP in the name), `dir\` (double, row + 0.5; a trailing backslash).
Every column's field metadata has a hostile `description` (OSC title, C1, markup) and
`unit` (`u ESC[5m`). File key-value metadata: a key with an OSC title whose value holds
OSC 52 and markup; `json` (escaped controls inside JSON text); `big` (a 31 kB JSON object,
500 keys, for "long values are cut").

### units.parquet

500 rows of astronomy-like columns for `kind_for`, units, descriptions and the derived
readings. Units come from field metadata `unit` or `units`, else from a felis description
`"[unit] text"`; descriptions from `description`, `doc` or `comment`.

Notable: `ra` row 0 = 359.9999999999 (reads 00h00m00.000s), `dec` row 0 = −1e-10 (reads
+00°00′00.00″); `ra` row 1 = 0, `dec` row 1 = −90; `midpointMjdTai`, `jd`, `epoch`,
`obsTime` are NaN at row 2; `psfFlux` and `value` are −5 at row 3 (no AB magnitude).
`decimalDeg` is decimal128(8, 4) with unit deg (kind `angle`). `nested_unit` has
description `"[ct] [not a unit] text"` (unit `ct`); `unit_and_desc` has both a `unit` key
(`km/s`, which wins) and a `"[ignored] …"` description; `empty_meta` has empty strings.
`RAJ2000`/`DEJ2000` (unit in `unit`, text in `doc`/`comment`), `RA_ICRS`/`DE_ICRS`,
`glon`/`glat`, `elon`, `lambda` (Angstrom, but `angle` by name) cover the VizieR and
longitude names. File key-value metadata: `table` = `Units`, `generator`.

### casedup.parquet, rowcol.parquet, nulname.parquet

- `casedup`: `Name` (`A B C`), `name` (`a b c`), `x` (1 2 3). DuckDB calls `name` `name_1`.
- `rowcol`: `__pqx_row` (7, 8), `__PQX_ROW_` (1, 1), `b` (1, 2): pqx's own row-number
  column names, which it must avoid.
- `nulname`: `a\x00b` and `b` (1, 2). SQL can't name the first column: pqx raises
  "has a NUL character in its name" for any query that would.
