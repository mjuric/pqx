import numpy as np
import pyarrow as pa
import pyarrow.parquet as pq
import pytest

from pqx.demo import make_table


@pytest.fixture(scope="session")
def demo_path(tmp_path_factory):
    """20k LSST-like rows in 2,500-row row groups (so windows cross row groups)."""
    p = tmp_path_factory.mktemp("data") / "demo.parquet"
    pq.write_table(make_table(20_000, seed=7), p, row_group_size=2_500, compression="zstd")
    return str(p)


@pytest.fixture(scope="session")
def odd_path(tmp_path_factory):
    """Awkward schema: nested types, NaN/inf, quoted names, a file_row_number column."""
    p = tmp_path_factory.mktemp("data") / "odd.parquet"
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
    pq.write_table(tbl, p, row_group_size=300)
    return str(p)
