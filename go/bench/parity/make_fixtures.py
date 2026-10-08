"""Write the parity scenarios' fixture files, deterministically, into a directory.

    python make_fixtures.py OUTDIR [--only NAME ...]

Run it with a Python that has pyarrow and numpy (the reference pqx venv has both;
`demo` and `slow` also use `pqx.demo` when it imports, else a copy of its generator
isn't attempted and those two are skipped with a message). Files already present are
kept unless --force is given. The facts the scenarios rely on are listed per fixture
below and in README.md.
"""
from __future__ import annotations

import argparse
import datetime as dt
import decimal
import os
import sys

import numpy as np
import pyarrow as pa
import pyarrow.parquet as pq


def demo(path):
    """20,000 LSST-like rows (pqx.demo, seed 7) in 2,500-row row groups: the tests' demo_path."""
    from pqx.demo import make_table
    pq.write_table(make_table(20_000, seed=7), path, row_group_size=2_500, compression="zstd")


def slow(path):
    """2,000,000 LSST-like rows (pqx.demo, seed 3) in 8 row groups: big enough for a slow count."""
    from pqx.demo import make_table
    pq.write_table(make_table(2_000_000, seed=3), path, row_group_size=250_000, compression="zstd")


def odd(path):
    """The tests' odd_path: nested types, NaN/inf, a quoted name, a file_row_number column."""
    n = 1_000
    rng = np.random.default_rng(1)
    x = rng.normal(size=n)
    x[::50] = np.nan
    x[1] = np.inf
    tbl = pa.table({
        "file_row_number": np.arange(n) * 10,
        "weird name": rng.integers(0, 3, n),
        "x": x,
        "tags": pa.array([["a", "b"][: i % 3] for i in range(n)], type=pa.list_(pa.string())),
        "pos": pa.array([{"ra": float(i), "dec": -float(i) / 20} for i in range(n)]),
        "blob": pa.array([bytes([i % 256]) * (i % 20) for i in range(n)], type=pa.binary()),
        "day": pa.array(np.datetime64("2026-01-01") + np.arange(n).astype("timedelta64[D]")),
        "allnull": pa.nulls(n, pa.int64()),
    })
    pq.write_table(tbl, path, row_group_size=300)


def types(path):
    """One column per Arrow type pqx formats differently, 200 rows, every 7th row null."""
    n = 200
    rng = np.random.default_rng(5)
    mask = np.arange(n) % 7 == 3
    t0 = dt.datetime(2026, 1, 1, tzinfo=dt.timezone.utc)
    cols = {
        "id": pa.array(np.arange(n, dtype=np.int64)),
        "i8": pa.array(rng.integers(-128, 128, n).astype(np.int8), mask=mask),
        "u32": pa.array(rng.integers(0, 2**32, n, dtype=np.uint64).astype(np.uint32), mask=mask),
        "u64": pa.array((np.arange(n, dtype=np.uint64) * np.uint64(92233720368547758)), mask=mask),
        "f32": pa.array(rng.normal(size=n).astype(np.float32), mask=mask),
        "f64": pa.array(rng.lognormal(3, 4, n), mask=mask),
        "flag": pa.array(rng.random(n) < 0.5, mask=mask),
        "name": pa.array([f"name-{i:03d}-{'αβγ漢字'[i % 5]}" for i in range(n)], mask=mask),
        "bin": pa.array([bytes(range(i % 6)) for i in range(n)], type=pa.binary(), mask=mask),
        "day": pa.array([dt.date(2026, 1, 1) + dt.timedelta(days=i) for i in range(n)], mask=mask),
        "ts": pa.array([t0 + dt.timedelta(seconds=37 * i, microseconds=123 * i) for i in range(n)],
                       type=pa.timestamp("us", tz="UTC"), mask=mask),
        "dur": pa.array([dt.timedelta(seconds=i * 61) for i in range(n)], type=pa.duration("s"), mask=mask),
        "dec": pa.array([decimal.Decimal(f"{i * 3}.{i % 100:02d}") for i in range(n)], type=pa.decimal128(12, 2),
                        mask=mask),
        "wide": pa.array([decimal.Decimal("1" + "0" * 45 + str(i)) for i in range(n)], type=pa.decimal256(50, 0)),
        "lst": pa.array([list(range(i % 4)) for i in range(n)], type=pa.list_(pa.int32()), mask=mask),
        "st": pa.array([{"a": i, "b": f"s{i}"} for i in range(n)], mask=mask),
        "mp": pa.array([[("k", i)] for i in range(n)], type=pa.map_(pa.string(), pa.int64()), mask=mask),
        "mjd": pa.array(60000.0 + np.arange(n) * 0.123456789),
        "raDeg": pa.array(rng.uniform(0, 360, n)),
        "decDeg": pa.array(rng.uniform(-90, 90, n)),
    }
    units = {"mjd": "[d] Time, MJD.", "raDeg": "[deg] Right ascension.", "decDeg": "[deg] Declination.",
             "f64": "[nJy] A flux."}
    tbl = pa.table(cols)
    schema = pa.schema([f.with_metadata({"description": units[f.name]}) if f.name in units else f
                        for f in tbl.schema], metadata={"fixture": "types"})
    pq.write_table(tbl.cast(schema), path, row_group_size=64)


def units(path):
    """Felis-style units and descriptions, VizieR-style RAJ2000/DEJ2000, 1,000 rows."""
    n = 1_000
    rng = np.random.default_rng(11)
    cols = {
        "objectId": pa.array(np.arange(n, dtype=np.int64) + 1_000_000),
        "RAJ2000": rng.uniform(0, 360, n),
        "DEJ2000": rng.uniform(-60, 30, n),
        "elon": rng.uniform(0, 360, n),
        "mjd": 60000 + np.sort(rng.uniform(0, 500, n)),
        "psfFlux": rng.lognormal(8, 1, n),
        "raErr": rng.uniform(1e-6, 3e-5, n),
        "gmag": rng.normal(21, 1, n),
    }
    desc = {
        "objectId": "[] Object id.",
        "RAJ2000": "[deg] Right ascension (J2000).",
        "DEJ2000": "[deg] Declination (J2000).",
        "elon": "[deg] Ecliptic longitude.",
        "mjd": "[d] Observation time, MJD (TAI).",
        "psfFlux": "[nJy] PSF flux.",
        "raErr": "[deg] Uncertainty of RAJ2000.",
        "gmag": "[mag] g-band magnitude.",
    }
    tbl = pa.table(cols)
    schema = pa.schema([f.with_metadata({"description": desc[f.name]}) for f in tbl.schema],
                       metadata={"table": "Units", "description": "pqx parity fixture"})
    pq.write_table(tbl.cast(schema), path, row_group_size=250)


def wide(path):
    """60 columns × 3,000 rows (c00..c58 floats of growing scale), for column paging."""
    n = 3_000
    rng = np.random.default_rng(2)
    data = {"id": np.arange(n, dtype=np.int64)}
    for i in range(59):
        data[f"c{i:02d}"] = np.round(rng.normal(scale=10 ** (i % 7 - 2), size=n), 6)
    pq.write_table(pa.table(data), path, row_group_size=1_000)


FIXTURES = {"demo": demo, "slow": slow, "odd": odd, "types": types, "units": units, "wide": wide}


def main(argv=None):
    p = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    p.add_argument("outdir")
    p.add_argument("--only", nargs="*")
    p.add_argument("--force", action="store_true")
    a = p.parse_args(argv)
    os.makedirs(a.outdir, exist_ok=True)
    for name, fn in FIXTURES.items():
        if a.only and name not in a.only:
            continue
        path = os.path.join(a.outdir, f"{name}.parquet")
        if os.path.exists(path) and not a.force:
            continue
        tmp = path + ".tmp"
        try:
            fn(tmp)
        except ImportError as e:
            print(f"skipping {name}: {e}", file=sys.stderr)
            continue
        os.replace(tmp, path)
        print(f"wrote {path}", file=sys.stderr)


if __name__ == "__main__":
    main()
