"""Write the Parquet fixtures in go/testdata/fixtures/ (checked in).

    PYTHONPATH=<repo root> python go/testdata/make_fixtures.py [OUTDIR]

They mirror the fixtures of the Python tests (tests/conftest.py, tests/test_security.py,
tests/test_fetch.py) so the Go tests and the golden files (golden/make_golden.py) see the
same data. Everything is seeded or fixed: with the same PyArrow version, two runs write
byte-identical files (the footer's created_by names the PyArrow version, so another
version changes the bytes but not the data). See README.md for what each file holds.
"""
from __future__ import annotations

import base64
import datetime as dt
import decimal
import json
import math
import os
import sys
import uuid

import numpy as np
import pyarrow as pa
import pyarrow.parquet as pq

from pqx.demo import make_table

HERE = os.path.dirname(os.path.abspath(__file__))

# ------------------------------------------------------------------ hostile strings (test_security)
ESC = "\x1b"
OSC_TITLE = ESC + "]0;PWNED-TITLE\x07"
OSC52 = ESC + "]52;c;" + base64.b64encode(b"echo PWNED\n").decode() + ESC + "\\"
OSC8 = ESC + "]8;;https://evil.example/" + ESC + "\\click" + ESC + "]8;;" + ESC + "\\"
C1_CSI = "\x9b"
MARKUP = "[bold red]MARKUP[/] [@click=app.quit]clickme[/] [link=https://evil.example]lnk[/link]"
#: where the SQL-smuggling strings would write, if one ever ran (it must not exist afterwards)
PWN = "/tmp/pqx-fixture-pwned.txt"


def evil_values(pwn: str = PWN) -> list[str]:
    """test_security.evil_values, then more: NUL, bidi, zero-width, C1, DEL, wide characters."""
    return [
        "it's \"quoted\" \\ back'slash",
        f"x' ); COPY (SELECT 1) TO '{pwn}'; --",
        OSC_TITLE + "TITLE-VAL",
        OSC52 + "CLIP-VAL",
        OSC8,
        MARKUP,
        "[/]",
        ESC + "[2J" + ESC + "[31mRED",
        C1_CSI + "31mC1" + "\x9d0;C1-TITLE\x07",
        "tab\there\nnew line",
        # (rows 10 on: not in test_security's list)
        "nul\x00in the middle",
        "abc‮dcba",                      # RIGHT-TO-LEFT OVERRIDE
        "zero​width‍⁠﻿",  # ZWSP, ZWJ, WORD JOINER, BOM
        "c1 \x85 nel \x9d osc",
        "del\x7f",
        "⁦isolate⁩ ‎‏ ؜",
        "é ✓ 漢字 😀 é",
        "[bold]",
        "trailing backslash\\",
        "",
    ]


def evil_completion_name(pwn: str = PWN) -> str:
    return (f"random() > -1)); COPY (SELECT 'echo PWNED') TO '{pwn}' (HEADER false, QUOTE ''); "
            "SELECT * FROM (SELECT 1 AS x WHERE (1")


def hostile_table() -> pa.Table:
    """test_security.make_evil's file (with more strings): every displayed string is hostile."""
    vals = evil_values()
    n = len(vals)
    cols = {
        "a": pa.array(range(n)),
        "s": pa.array(vals),
        "select": pa.array(vals),  # a keyword
        "[bold]mk[/] [@click=app.quit]x[/]": pa.array(vals),
        "esc" + OSC_TITLE + "name": pa.array(range(n)),
        "c1" + C1_CSI + "2Jname": pa.array(range(n)),
        evil_completion_name(): pa.array(range(n)),
        "bidi‮name​": pa.array(range(n)),
        "dir\\": pa.array([i + 0.5 for i in range(n)]),
    }
    fields = []
    for k, v in cols.items():
        md = {b"description": ("desc " + OSC_TITLE + C1_CSI + "5m [bold]M[/] [@click=app.quit]q[/]").encode(),
              b"unit": ("u" + ESC + "[5m").encode()}
        fields.append(pa.field(k, v.type, metadata=md))
    schema = pa.schema(fields, metadata={
        ("k" + OSC_TITLE).encode(): ("v " + OSC52 + " [@click=app.quit]x[/]").encode(),
        b"json": b'{"a": "\\u001b]0;JSONTITLE\\u0007", "b": "\\u009b31mJSONC1", "c": "\\u007f"}',
        b"big": json.dumps({f"k{i}": "v" * 50 for i in range(500)}).encode(),
    })
    return pa.table(list(cols.values()), schema=schema)


# ------------------------------------------------------------------ odd (conftest.odd_path)
def odd_table() -> pa.Table:
    n = 1_000
    rng = np.random.default_rng(1)
    x = rng.normal(size=n)
    x[::50] = np.nan
    x[1] = np.inf
    return pa.table({
        "file_row_number": np.arange(n) * 10,
        "weird name": rng.integers(0, 3, n),
        "x": x,
        "tags": pa.array([["a", "b"][: i % 3] for i in range(n)], type=pa.list_(pa.string())),
        "pos": pa.array([{"ra": float(i), "dec": -float(i) / 20} for i in range(n)]),
        "blob": pa.array([bytes([i % 256]) * (i % 20) for i in range(n)], type=pa.binary()),
        "day": pa.array(np.datetime64("2026-01-01") + np.arange(n).astype("timedelta64[D]")),
        "allnull": pa.nulls(n, pa.int64()),
    })


# ------------------------------------------------------------------ types (test_fetch._zoo, widened)
TYPES_ROWS = 60
TYPES_RG = 7  # row groups of 7 rows: 9 groups, the last of 4


def types_table(n: int = TYPES_ROWS) -> pa.Table:
    """A zoo of types. Every column but ``null`` is NULL where ``i % 11 == 10``."""
    i = np.arange(n)
    big = 1_600_000_000

    def col(values, typ=None):
        vals = [None if k % 11 == 10 else v for k, v in enumerate(values)]
        return pa.array(vals, typ)

    def ints(dtype, offset=0, scale=1):
        info = np.iinfo(dtype)
        out = []
        for k in range(n):
            if k == 0:
                out.append(int(info.min))
            elif k == 1:
                out.append(int(info.max))
            else:
                out.append(int(offset + scale * (k - 30)) if info.min < 0 else int(offset + scale * k))
        return out

    def d(unscaled: int, scale: int = 0) -> decimal.Decimal:
        """Exactly unscaled / 10**scale (Decimal arithmetic would round to 28 digits)."""
        return decimal.Decimal(f"{unscaled}E-{scale}")

    utc_off = dt.timezone(dt.timedelta(hours=2))
    cols = {
        "i8": col(ints(np.int8), pa.int8()), "i16": col(ints(np.int16, 0, 1000), pa.int16()),
        "i32": col(ints(np.int32, 0, 70_000_000), pa.int32()),
        "i64": col(ints(np.int64, 0, 300_000_000_000_000_000), pa.int64()),
        "u8": col(ints(np.uint8), pa.uint8()), "u16": col(ints(np.uint16, 0, 1000), pa.uint16()),
        "u32": col(ints(np.uint32, 0, 70_000_000), pa.uint32()), "u64": col(ints(np.uint64, 2**63), pa.uint64()),
    }
    f64_cycle = [math.nan, math.inf, -math.inf, 1.5, -0.0, 0.1, 1e300, -1e-300, 5e-324, 123456789.123456789]
    cols |= {
        "f16": col([float(np.float16(x / 7)) for x in i], pa.float16()),
        "f32": col([float(np.float32(x / 3)) if x % 9 else [math.nan, math.inf, -math.inf][(x // 9) % 3]
                    for x in i], pa.float32()),
        "f64": col([f64_cycle[x % len(f64_cycle)] for x in i], pa.float64()),
        "bool": col([bool(x % 2) for x in i], pa.bool_()),
        "str": col([f"s{x}" if x % 4 else "" for x in i], pa.string()),
        "lstr": col([f"é{x}漢" for x in i], pa.large_string()),
        "dict": pa.array([None if x % 11 == 10 else ["a", "b", "c"][x % 3] for x in i]).dictionary_encode(),
        "bin": col([bytes([x]) * (x % 5) for x in i], pa.binary()),
        "fbin": col([bytes([x, 255 - x, 0, 7]) for x in i], pa.binary(4)),
        "uuid": pa.ExtensionArray.from_storage(
            pa.uuid(), col([uuid.UUID(int=int(x) * 0x0123456789ABCDEF0123456789ABCDEF % 2**128).bytes for x in i],
                           pa.binary(16))),
        "date": col([dt.date(2020, 1, 1) + dt.timedelta(days=int(x) * 37) for x in i], pa.date32()),
        "ts_s": col([int(x) * 86_400 * 97 - 2_000_000_000 for x in i], pa.timestamp("s")),
        "ts_ms": col([int(x) * 1_000_003 + big * 10**3 for x in i], pa.timestamp("ms")),
        "ts_us": col([int(x) * 7 + big * 10**6 for x in i], pa.timestamp("us")),
        "ts_ns": col([int(x) + big * 10**9 + 123_456_789 for x in i], pa.timestamp("ns")),
        "ts_s_utc": col([int(x) * 3_600 + big for x in i], pa.timestamp("s", "UTC")),
        "ts_ms_utc": col([int(x) * 1_001 + big * 10**3 for x in i], pa.timestamp("ms", "UTC")),
        "ts_us_utc": col([int(x) * 10**6 + big * 10**6 for x in i], pa.timestamp("us", "UTC")),
        "ts_ns_utc": col([int(x) * 1_000 + 1 + big * 10**9 for x in i], pa.timestamp("ns", "UTC")),
        "ts_tz_ny": col([int(x) * 1000 + big * 10**9 for x in i], pa.timestamp("ns", "America/New_York")),
        "ts_off": col([dt.datetime(2026, 1, 1, 12, tzinfo=utc_off) + dt.timedelta(minutes=int(x)) for x in i],
                      pa.timestamp("ms", "+02:00")),
        "t32_s": col([int(x) * 1_439 for x in i], pa.time32("s")),
        "t32_ms": col([int(x) * 1000 + 7 for x in i], pa.time32("ms")),
        "t64_us": col([int(x) * 10**6 + 5 for x in i], pa.time64("us")),
        "t64_ns": col([int(x) * 10**9 + 123_456_789 for x in i], pa.time64("ns")),
        "dur_s": col([int(x) - 30 for x in i], pa.duration("s")),
        "dur_ms": col([int(x) * 1001 for x in i], pa.duration("ms")),
        "dur_us": col([int(x) * 10**6 + 1 for x in i], pa.duration("us")),
        "dur_ns": col([int(x) * 10**9 + 7 for x in i], pa.duration("ns")),
        "dec9_2": col([d(int(x) - 30, 2) for x in i], pa.decimal128(9, 2)),
        "dec10_0": col([d(int(x) * 123_456_789) for x in i], pa.decimal128(10, 0)),
        "dec18_6": col([d(int(x) * 1_234_567_891 * (-1) ** int(x), 6) for x in i], pa.decimal128(18, 6)),
        "dec38_3": col([d((10**37 + int(x)) * (-1) ** int(x), 3) for x in i], pa.decimal128(38, 3)),
        "dec38_38": col([d(int(x), 38) for x in i], pa.decimal128(38, 38)),
        "dec50_2": col([d(int(x) - 30, 2) for x in i], pa.decimal256(50, 2)),
        "dec76_10": col([d((10**64 * int(x) + int(x)) * (-1) ** int(x), 10) for x in i], pa.decimal256(76, 10)),
        "list": col([[int(x)] * (x % 3) for x in i], pa.list_(pa.int64())),
        "llist": col([[float(x), None][: x % 3] for x in i], pa.large_list(pa.float64())),
        "fsl": col([[int(x), int(x) + 1] for x in i], pa.list_(pa.int32(), 2)),
        "lstr_list": col([["a\x1bb", "", None][: x % 4] for x in i], pa.list_(pa.string())),
        "struct": col([{"a": int(x), "b": f"x{x}", "c": [1.0, float(x)]} if x % 5 else
                       {"a": None, "b": None, "c": None} for x in i],
                      pa.struct([("a", pa.int64()), ("b", pa.string()), ("c", pa.list_(pa.float64()))])),
        "lstruct": col([[{"k": f"k{x}", "v": None if x % 2 else int(x)}] for x in i],
                       pa.list_(pa.struct([("k", pa.string()), ("v", pa.int16())]))),
        "map": col([[(f"k{x}", int(x)), ("z", None)][: 1 + x % 2] for x in i], pa.map_(pa.string(), pa.int64())),
        "null": pa.nulls(n),
        "json": col([f'{{"a": {x}}}' for x in i], pa.json_(pa.string())),
        "a.b": col(list(i), pa.int64()),
        'q"uote': col([int(x) * 2 for x in i], pa.int64()),
    }
    return pa.table(cols)


# ------------------------------------------------------------------ units (field metadata, kind_for)
UNITS_ROWS = 500


def units_table(n: int = UNITS_ROWS) -> pa.Table:
    """LSST- and VizieR-like names with units and descriptions in each metadata form pqx reads."""
    rng = np.random.default_rng(11)
    ra = rng.uniform(0, 360, n)
    dec = np.degrees(np.arcsin(rng.uniform(-1, 1, n)))
    mjd = 60000 + rng.uniform(0, 1000, n)
    mag = rng.normal(21, 1.5, n)
    flux = 10 ** (-0.4 * (mag - 31.4))
    ra[0], dec[0] = 359.9999999999, -0.0000000001   # derived readings round to 00h / +00°
    ra[1], dec[1] = 0.0, -90.0
    mjd[2] = math.nan
    flux[3] = -5.0
    # (name, values, Arrow type, field metadata)
    specs = [
        ("objectId", np.arange(n, dtype=np.int64) + 10**15, pa.int64(), {"description": "[] Object id."}),
        ("ra", ra, pa.float64(), {"description": "[deg] Right ascension."}),
        ("dec", dec, pa.float64(), {"description": "[deg] Declination."}),
        ("raErr", rng.uniform(1e-7, 1e-5, n), pa.float32(), {"description": "[deg] Uncertainty of ra."}),
        ("decErr", rng.uniform(1e-7, 1e-5, n), pa.float32(), {"unit": "deg", "description": "Uncertainty of dec."}),
        ("ra_dec_Cov", rng.normal(0, 1e-12, n), pa.float32(), {"unit": "deg**2"}),
        ("coord_ra", ra, pa.float64(), {"units": "deg"}),
        ("coord_dec", dec, pa.float64(), {"units": "deg"}),
        ("RAJ2000", ra, pa.float64(), {"unit": "deg", "doc": "VizieR-style RA"}),
        ("DEJ2000", dec, pa.float64(), {"unit": "deg", "comment": "VizieR-style Dec"}),
        ("RA_ICRS", ra, pa.float64(), {}),
        ("DE_ICRS", dec, pa.float64(), {}),
        ("glon", (ra + 33) % 360, pa.float64(), {}),
        ("glat", dec / 2, pa.float64(), {}),
        ("lambda", rng.uniform(3000, 10000, n), pa.float64(), {"description": "[Angstrom] Wavelength."}),
        ("elon", ra - 180, pa.float64(), {"unit": "deg"}),
        ("midpointMjdTai", mjd, pa.float64(), {"description": "[d] Mid-exposure time, TAI MJD."}),
        ("jd", mjd + 2_400_000.5, pa.float64(), {}),
        ("epoch", mjd, pa.float64(), {"unit": "d"}),
        ("obsTime", mjd, pa.float64(), {"unit": "mjd"}),
        ("mag", mag, pa.float32(), {"description": "[mag] AB magnitude."}),
        ("gMag", mag + 0.3, pa.float32(), {}),
        ("magErr", rng.uniform(0.001, 0.2, n), pa.float32(), {"unit": "mag"}),
        ("psfFlux", flux, pa.float64(), {"description": "[nJy] PSF flux."}),
        ("psfFluxErr", flux * 0.05, pa.float32(), {"description": "[nJy] PSF flux uncertainty."}),
        ("value", flux, pa.float64(), {"unit": "nJy"}),
        ("snr", flux / (flux * 0.05), pa.float32(), {"description": "[] Signal to noise."}),
        ("radius", rng.uniform(0, 10, n), pa.float64(), {"description": "[arcsec] Not an RA."}),
        ("RATIO", rng.uniform(0, 1, n), pa.float64(), {}),
        ("x", rng.normal(size=n), pa.float64(), {"unit": "deg"}),
        ("decimalDeg", [decimal.Decimal(round(v, 4)).quantize(decimal.Decimal("0.0001")) for v in dec],
         pa.decimal128(8, 4), {"description": "[deg] Dec as a decimal."}),
        ("band", rng.choice(list("ugrizy"), n), pa.string(), {"description": "[] Filter band."}),
        ("nested_unit", rng.integers(0, 9, n), pa.int16(), {"description": "[ct] [not a unit] text"}),
        ("unit_and_desc", rng.normal(size=n), pa.float64(),
         {"unit": "km/s", "description": "[ignored] the unit key wins"}),
        ("empty_meta", rng.normal(size=n), pa.float64(), {"unit": "", "description": ""}),
        ("ingestTime", pa.array(np.datetime64("2026-01-01T00:00:00", "ms")
                                + np.arange(n).astype("timedelta64[s]")), pa.timestamp("ms", "UTC"),
         {"description": "[] When ingested."}),
    ]
    arrays, fields = [], []
    for name, vals, typ, md in specs:
        arr = vals if isinstance(vals, pa.Array) else pa.array(vals)
        arrays.append(arr.cast(typ))
        fields.append(pa.field(name, typ, metadata={k.encode(): v.encode() for k, v in md.items()} or None))
    return pa.table(arrays, schema=pa.schema(fields, metadata={"table": "Units", "generator": "make_fixtures.py"}))


# ------------------------------------------------------------------ small single-purpose files
def casedup_table() -> pa.Table:
    """test_security.case_dup_file: DuckDB calls the second ``name`` ``name_1``."""
    return pa.table({"Name": ["A", "B", "C"], "name": ["a", "b", "c"], "x": [1, 2, 3]})


def rowcol_table() -> pa.Table:
    """test_security.test_row_number_column_names_do_not_collide: pqx's own row-number names."""
    return pa.table({"__pqx_row": [7, 8], "__PQX_ROW_": [1, 1], "b": [1, 2]})


def nulname_table() -> pa.Table:
    """A NUL in a column name: SQL can't refer to it (quote_ident raises)."""
    return pa.table({"a\x00b": [1, 2], "b": [1, 2]})


def write(tbl: pa.Table, path: str, **kw) -> None:
    pq.write_table(tbl, path, **kw)


def main(argv: list[str]) -> None:
    out = argv[1] if len(argv) > 1 else os.path.join(HERE, "fixtures")
    os.makedirs(out, exist_ok=True)
    p = lambda name: os.path.join(out, name)  # noqa: E731
    write(make_table(20_000, seed=7), p("demo.parquet"), row_group_size=2_500, compression="zstd")
    write(odd_table(), p("odd.parquet"), row_group_size=300)
    write(hostile_table(), p("hostile.parquet"))
    write(types_table(), p("types.parquet"), row_group_size=TYPES_RG)
    write(units_table(), p("units.parquet"), row_group_size=200)
    write(casedup_table(), p("casedup.parquet"))
    write(rowcol_table(), p("rowcol.parquet"))
    write(nulname_table(), p("nulname.parquet"))
    for name in sorted(os.listdir(out)):
        if name.endswith(".parquet"):
            print(f"{os.path.getsize(p(name)):>10,}  {name}")


if __name__ == "__main__":
    main(sys.argv)
