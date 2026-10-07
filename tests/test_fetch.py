"""Trivial-view windows read directly with pyarrow must equal DuckDB's."""
import datetime as dt
import decimal
import math
import threading
import uuid

import duckdb
import numpy as np
import pyarrow as pa
import pyarrow.parquet as pq
import pytest

from pqx.data import ParquetDataset, View


def _zoo(n: int = 60) -> pa.Table:
    i = np.arange(n)
    big = 1_600_000_000
    return pa.table({
        "i8": pa.array(i.astype("i1")), "u64": pa.array(i.astype("u8") + np.uint64(2**63)),
        "f16": pa.array(i.astype("f2")), "f32": pa.array(i.astype("f4") / 3),
        "f64": pa.array([math.nan, math.inf, -math.inf, None, 1.5, -0.0] * (n // 6)),
        "bool": pa.array([bool(x % 2) if x % 5 else None for x in i]),
        "str": pa.array([f"s{x}" if x % 4 else None for x in i]),
        "lstr": pa.array([f"é{x}" for x in i], pa.large_string()),
        "dict": pa.array([["a", "b", None][x % 3] for x in i]).dictionary_encode(),
        "bin": pa.array([bytes([x]) * (x % 5) for x in i], pa.binary()),
        "fbin": pa.array([bytes([x]) * 4 for x in i], pa.binary(4)),
        "date": pa.array([dt.date(2020, 1, 1) + dt.timedelta(days=int(x)) for x in i], pa.date32()),
        "ts_ms": pa.array(i * 10**3 + big * 10**3, pa.timestamp("ms")),
        "ts_us": pa.array(i * 7 + big * 10**6, pa.timestamp("us")),
        "ts_ns": pa.array(i + big * 10**9 + 123_456_789, pa.timestamp("ns")),
        "ts_utc": pa.array(i * 10**6 + big * 10**6, pa.timestamp("us", "UTC")),
        "ts_tz_ns": pa.array(i * 1000 + big * 10**9, pa.timestamp("ns", "America/New_York")),
        "ts_tz_odd": pa.array(i + big * 10**9 + 7, pa.timestamp("ns", "Europe/Berlin")),  # ns: DuckDB truncates
        "ts_off": pa.array(i + big * 10**3, pa.timestamp("ms", "+02:00")),
        "t32": pa.array((i * 1000).astype("i4"), pa.time32("ms")),
        "t64": pa.array((i * 10**6 + 5).astype("i8"), pa.time64("us")),
        "dec": pa.array([decimal.Decimal(int(x)) / 100 for x in i], pa.decimal128(10, 2)),
        "dec38": pa.array([decimal.Decimal(int(x)) / 1000 for x in i], pa.decimal128(38, 3)),
        "dec256": pa.array([decimal.Decimal(int(x)) / 100 for x in i], pa.decimal256(50, 2)),
        "list": pa.array([[int(x)] * (x % 3) if x % 6 else None for x in i], pa.list_(pa.int64())),
        "llist": pa.array([[float(x)] * (x % 3) for x in i], pa.large_list(pa.float64())),
        "fsl": pa.array([[int(x), int(x) + 1] for x in i], pa.list_(pa.int32(), 2)),
        "struct": pa.array([{"a": int(x), "b": f"x{x}", "c": [1.0, float(x)]} if x % 5 else None for x in i]),
        "lstruct": pa.array([[{"k": f"k{x}", "v": None}] for x in i],
                            pa.list_(pa.struct([("k", pa.string()), ("v", pa.int16())]))),
        "map": pa.array([[(f"k{x}", int(x))] for x in i], pa.map_(pa.string(), pa.int64())),
        "dur": pa.array(i, pa.duration("ms")),
        "null": pa.nulls(n),
        "nested_dict": pa.StructArray.from_arrays([pa.array([["p", "q"][x % 2] for x in i]).dictionary_encode()],
                                                  ["d"]),
        "uuid": pa.ExtensionArray.from_storage(pa.uuid(), pa.array([uuid.UUID(int=int(x)).bytes for x in i],
                                                                    pa.binary(16))),
        "json": pa.array([f'{{"a": {x}}}' for x in i], pa.json_(pa.string())),
        "a.b": pa.array(i),
        'q"uote': pa.array(i * 2),
    })


@pytest.fixture(scope="module")
def zoo_path(tmp_path_factory):
    p = tmp_path_factory.mktemp("zoo") / "zoo.parquet"
    pq.write_table(_zoo(), p, row_group_size=7)
    return str(p)


@pytest.fixture(scope="module")
def many_path(tmp_path_factory):
    """Many small row groups of uneven size, including an empty one."""
    p = tmp_path_factory.mktemp("many") / "many.parquet"
    rng = np.random.default_rng(3)
    schema = pa.schema([("id", pa.int64()), ("x", pa.float64()), ("s", pa.string())])
    with pq.ParquetWriter(p, schema) as w:
        start = 0
        for n in (100, 37, 0, 250, 1, 99, 513):
            w.write_table(pa.table({"id": np.arange(start, start + n), "x": rng.normal(size=n),
                                    "s": [f"r{k}" for k in range(start, start + n)]}, schema=schema),
                          row_group_size=max(n, 1))
            start += n
    return str(p)


def _norm(v):
    """Make NaN compare equal to itself, recursively; keep the Python type in the key."""
    if isinstance(v, float) and math.isnan(v):
        return ("nan",)
    if isinstance(v, (list, tuple)):
        return type(v), tuple(_norm(x) for x in v)
    if isinstance(v, dict):
        return dict, tuple((k, _norm(x)) for k, x in v.items())
    return type(v), v, (v.tzinfo if isinstance(v, (dt.datetime, dt.time)) else None)


def _duck(ds, view, offset, limit, columns=None):
    orig = ds._fetch_direct
    ds._fetch_direct = lambda *a: None
    try:
        return ds.fetch(view, offset, limit, columns)
    finally:
        ds._fetch_direct = orig


def _direct(ds, offset, limit, columns=None):
    """The direct path, forced on regardless of the cost model."""
    est = ds._direct_estimate
    ds._direct_estimate = lambda rgs, end, names: (True, est(rgs, end, names)[1])
    try:
        page = ds._fetch_direct(offset, limit, columns)
    finally:
        del ds._direct_estimate
    return page


def _same(a, b):
    assert a.offset == b.offset
    assert a.columns == b.columns
    assert a.row_numbers == b.row_numbers
    assert [str(t) for t in a.types] == [str(t) for t in b.types]
    assert a.types == b.types
    assert len(a.rows) == len(b.rows)
    for ra, rb in zip(a.rows, b.rows):
        assert _norm(ra) == _norm(rb)


DIRECT_ZOO = ["i8", "u64", "f16", "f32", "f64", "bool", "str", "lstr", "dict", "bin", "fbin", "date", "ts_ms",
              "ts_us", "ts_ns", "ts_utc", "ts_tz_ns", "ts_off", "t32", "t64", "dec", "dec38", "list", "llist", "fsl",
              "struct", "lstruct", "map", "dur", "null", "json", "a.b", 'q"uote']


@pytest.mark.parametrize("offset,limit", [(0, 60), (0, 1), (5, 10), (6, 1), (7, 7), (13, 30), (59, 5), (60, 5),
                                          (1000, 5), (3, 0)])
def test_zoo_direct_equals_duckdb(zoo_path, offset, limit):
    ds = ParquetDataset(zoo_path)
    page = _direct(ds, offset, limit, DIRECT_ZOO)
    assert page is not None
    _same(page, _duck(ds, View(), offset, limit, DIRECT_ZOO))


def test_zoo_whole_fetch_equals_duckdb(zoo_path):
    """All columns, including ones the direct path leaves to DuckDB."""
    ds = ParquetDataset(zoo_path)
    ds._direct_estimate = lambda rgs, end, names: (True, True)
    for offset, limit in [(0, 60), (10, 20), (55, 10)]:
        _same(ds.fetch(View(), offset, limit), _duck(ds, View(), offset, limit))
    # these differ between pyarrow and DuckDB (or pyarrow can't read them), so they go to DuckDB
    for col in ("ts_tz_odd", "dec256", "uuid", "nested_dict"):
        assert _direct(ds, 0, 10, ["i8", col]) is None, col
    assert _direct(ds, 0, 10, ["i8", "str"]) is not None  # the rest keeps the direct path


def test_types_are_duckdbs(zoo_path):
    ds = ParquetDataset(zoo_path)
    page = _direct(ds, 0, 3, ["dict", "lstr", "list", "ts_ms", "ts_off", "f16", "fsl", "null"])
    assert [str(t) for t in page.types] == ["string", "string", "list<l: int64>", "timestamp[us]",
                                            "timestamp[us, tz=UTC]", "float", "list<l: int32>", "int32"]
    assert page.rows[0][4].utcoffset().total_seconds() == 0


def test_demo_windows(demo_path):
    ds = ParquetDataset(demo_path)
    for offset in (0, 2_499, 2_500, 2_450, 12_345, 19_990, 19_999, 20_000, 25_000):
        for limit in (1, 50, 3_000):
            page = _direct(ds, offset, limit)
            assert page is not None
            _same(page, _duck(ds, View(), offset, limit))
    cols = ["mag", "diaSourceId", "band"]
    _same(_direct(ds, 4_990, 20, cols), _duck(ds, View(), 4_990, 20, cols))


def test_odd_file(odd_path):
    ds = ParquetDataset(odd_path)
    assert not ds._has_rownum
    for offset, limit in ((0, 1_000), (290, 20), (950, 100), (999, 1)):
        page = _direct(ds, offset, limit)
        assert page is not None
        _same(page, _duck(ds, View(), offset, limit))
    assert ds.fetch(View(), 950, 100).row_numbers[0] == 950


def test_many_row_groups(many_path):
    ds = ParquetDataset(many_path)
    assert ds.meta.num_row_groups == 7 and ds.num_rows == 1_000
    for offset in (0, 99, 100, 136, 137, 138, 386, 387, 388, 486, 487, 999, 1_000):
        for limit in (1, 2, 50, 400, 2_000):
            _same(_direct(ds, offset, limit), _duck(ds, View(), offset, limit))
    _same(_direct(ds, 120, 30, ["s", "id"]), _duck(ds, View(), 120, 30, ["s", "id"]))


def test_reads_only_needed_row_groups(many_path, monkeypatch):
    ds = ParquetDataset(many_path)
    calls = []
    orig = pq.ParquetFile.iter_batches

    def spy(self, *a, **k):
        calls.append((k.get("row_groups"), k.get("columns"), k.get("use_threads")))
        return orig(self, *a, **k)

    monkeypatch.setattr(pq.ParquetFile, "iter_batches", spy)
    monkeypatch.setattr(ds, "_direct_estimate", lambda rgs, end, names: (True, True))
    page = ds.fetch(View(), 130, 10, ["x"])  # row groups: 0..99, 100..136, (empty), 137..386
    assert page.row_numbers == list(range(130, 140))
    assert calls == [([1, 3], ["x"], False)]  # not the empty one
    calls.clear()
    ds.fetch(View(), 0, 100, ["id", "s"])
    assert calls == [([0], ["id", "s"], False)]
    calls.clear()
    ds.fetch(View(where="id > 3"), 0, 10)  # filtered: DuckDB
    assert calls == []


def test_empty_file(tmp_path):
    p = tmp_path / "empty.parquet"
    pq.write_table(pa.table({"a": pa.array([], pa.int64()), "s": pa.array([], pa.string())}), p)
    ds = ParquetDataset(str(p))
    page = ds.fetch(View(), 0, 100)
    assert page.rows == [] and page.columns == ["a", "s"]
    _same(page, _duck(ds, View(), 0, 100))


def test_cost_model_prefers_duckdb_deep_in_big_row_groups(tmp_path):
    p = tmp_path / "big.parquet"
    n = 1_000_000
    pq.write_table(pa.table({"x": np.arange(n, dtype=np.float64), "y": np.arange(n) * 2}), p, row_group_size=n)
    ds = ParquetDataset(str(p))
    assert ds._direct_estimate([0], 150, ["x", "y"])[0]  # the start of a row group: pyarrow
    assert not ds._direct_estimate([0], n - 10, ["x", "y"])[0]  # its end: DuckDB skips faster
    page = ds.fetch(View(), n - 10, 20)
    assert page.row_numbers == list(range(n - 10, n)) and page.rows[-1] == (n - 1.0, 2 * (n - 1))


def test_many_row_groups_prefer_direct(tmp_path):
    p = tmp_path / "small_rgs.parquet"
    pq.write_table(pa.table({"x": np.arange(50_000, dtype=np.float64)}), p, row_group_size=100)
    ds = ParquetDataset(str(p))
    assert ds._direct_estimate([250], 25_150, ["x"])[0]


def test_unusual_metadata_falls_back(demo_path, monkeypatch):
    ds = ParquetDataset(demo_path)
    monkeypatch.setattr(ds, "_rg_starts_cache", None, raising=False)  # row counts don't add up, say
    assert ds._fetch_direct(0, 10, None) is None
    _same(ds.fetch(View(), 0, 10), _duck(ds, View(), 0, 10))
    ds2 = ParquetDataset(demo_path)
    monkeypatch.setattr(ds2, "_duck_types_cache", None, raising=False)
    assert ds2._fetch_direct(0, 10, None) is None
    assert ds2._fetch_direct(0, 10, ["mag", "mag"]) is None  # duplicate names


def test_read_errors_fall_back(demo_path, monkeypatch):
    ds = ParquetDataset(demo_path)
    monkeypatch.setattr(ds, "_direct_estimate", lambda rgs, end, names: (True, True))

    def boom(*a, **k):
        raise OSError("disk on fire")

    monkeypatch.setattr(pq.ParquetFile, "iter_batches", boom)
    page = ds.fetch(View(), 100, 10, ["mag"])
    assert page.row_numbers == list(range(100, 110))  # served by DuckDB


def test_superseded_fetch_is_interrupted(many_path, monkeypatch):
    """A newer fetch under the same tag cancels an older direct read, like a DuckDB query."""
    ds = ParquetDataset(many_path)
    monkeypatch.setattr(ds, "_direct_estimate", lambda rgs, end, names: (True, True))
    entered, release = threading.Event(), threading.Event()
    orig = pq.ParquetFile.iter_batches

    def slow(self, *a, **k):
        for b in orig(self, *a, **k):
            entered.set()
            release.wait(5)
            yield b

    monkeypatch.setattr(pq.ParquetFile, "iter_batches", slow)
    result = {}

    def old():
        try:
            with ds.tagged("page"):
                result["page"] = ds.fetch(View(), 0, 500)
        except duckdb.InterruptException as e:
            result["error"] = e

    t = threading.Thread(target=old)
    t.start()
    assert entered.wait(5)
    monkeypatch.setattr(pq.ParquetFile, "iter_batches", orig)
    with ds.tagged("page"):
        newer = ds.fetch(View(), 10, 5)
    release.set()
    t.join(5)
    assert newer.row_numbers == list(range(10, 15))
    assert "error" in result and "page" not in result
    # ds.interrupt() also stops one
    entered.clear()
    release.clear()
    monkeypatch.setattr(pq.ParquetFile, "iter_batches", slow)
    result.clear()
    t = threading.Thread(target=old)
    t.start()
    assert entered.wait(5)
    ds.interrupt()
    release.set()
    t.join(5)
    assert "error" in result


def test_concurrent_fetches(demo_path):
    ds = ParquetDataset(demo_path)
    want = {off: _duck(ds, View(), off, 120).rows for off in (0, 2_400, 9_999, 17_000)}
    errors = []

    def worker(off):
        try:
            for _ in range(5):
                rows = ds.fetch(View(), off, 120).rows
                if [_norm(r) for r in rows] != [_norm(r) for r in want[off]]:
                    errors.append(off)
        except Exception as e:  # noqa: BLE001
            errors.append(e)

    ts = [threading.Thread(target=worker, args=(off,)) for off in want for _ in range(2)]
    for t in ts:
        t.start()
    for t in ts:
        t.join()
    assert errors == []
