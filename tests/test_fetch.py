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


def _ready(ds):
    assert ds._duck_types(wait=True) is not None
    return ds


def _open(path):
    return _ready(ParquetDataset(path))


def _duck(ds, view, offset, limit, columns=None):
    orig = ds._fetch_direct
    ds._fetch_direct = lambda *a: (None, None)
    try:
        return ds.fetch(view, offset, limit, columns)
    finally:
        ds._fetch_direct = orig


def _force(ds):
    """Make the cost model always pick the direct path (keeping its pre-buffer choice)."""
    est = type(ds)._direct_estimate
    ds._direct_estimate = lambda need, names: (True, est(ds, need, names)[1])


def _direct(ds, offset, limit, columns=None):
    """The direct path, forced on regardless of the cost model; None if it declined."""
    _ready(ds)
    had = "_direct_estimate" in vars(ds)
    if not had:
        _force(ds)
    try:
        return ds._fetch_direct(offset, limit, columns)[0]
    finally:
        if not had:
            del ds._direct_estimate


def _same(a, b):
    assert a.offset == b.offset
    assert a.columns == b.columns
    assert a.row_numbers == b.row_numbers
    assert [str(t) for t in a.types] == [str(t) for t in b.types]
    assert a.types == b.types
    assert len(a.rows) == len(b.rows)
    for ra, rb in zip(a.rows, b.rows):
        assert _norm(ra) == _norm(rb)


# all but dec256 (DuckDB 1.5 reads it wrong, see below)
DIRECT_ZOO = [n for n in _zoo().column_names if n != "dec256"]


@pytest.mark.parametrize("offset,limit", [(0, 60), (0, 1), (5, 10), (6, 1), (7, 7), (13, 30), (59, 5), (60, 5),
                                          (1000, 5), (3, 0)])
def test_zoo_direct_equals_duckdb(zoo_path, offset, limit):
    ds = _open(zoo_path)
    page = _direct(ds, offset, limit, DIRECT_ZOO)
    assert page is not None
    _same(page, _duck(ds, View(), offset, limit, DIRECT_ZOO))


def test_zoo_whole_fetch_equals_duckdb(zoo_path):
    """All columns, through fetch() (the direct path and DuckDB serving whatever it declines)."""
    ds = _open(zoo_path)
    _force(ds)
    cols = [n for n in ds.column_names if n != "dec256"]
    for offset, limit in [(0, 60), (10, 20), (55, 10)]:
        _same(ds.fetch(View(), offset, limit, cols), _duck(ds, View(), offset, limit, cols))
    assert all(t is not None for n, t in ds._duck_types().items())


def test_decimal256_values_are_right(zoo_path):
    """DuckDB 1.5 reads decimals wider than 38 digits as garbage doubles (e.g. 0.01 as 0.6553...);
    the direct path gives the correct double, with DuckDB's type."""
    ds = _open(zoo_path)
    page = _direct(ds, 0, 60, ["dec256"])
    assert str(page.types[0]) == "double"
    assert [r[0] for r in page.rows] == [float(decimal.Decimal(x) / 100) for x in range(60)]
    raw = [r[0] for r in ds.con.cursor().execute('SELECT dec256 FROM t').fetchall()]
    if raw == [r[0] for r in page.rows]:
        pytest.fail("DuckDB reads decimal256 right now: drop _fix_wide_decimals and its special cases")
    # pages DuckDB serves (filtered, sorted, or whenever the cost model says so) get the same values
    want = {i: float(decimal.Decimal(i) / 100) for i in range(60)}
    duck = _duck(ds, View(), 0, 60, ["i8", "dec256"])
    assert [r[1] for r in duck.rows] == [want[r[0]] for r in duck.rows]
    for v in (View(where="i8 > 10"), View(order_by=[("i8", True)]), View(where="i8 % 3 = 0", order_by=[("f32", False)])):
        pg = ds.fetch(v, 2, 20, ["i8", "dec256"])
        assert pg.rows and [r[1] for r in pg.rows] == [want[r[0]] for r in pg.rows]
    assert [r[0] for r in ds.fetch_columns([5, 3, 59], ["dec256"]).rows] == [want[5], want[3], want[59]]
    ds._read_rows = lambda rows, names, force=False: (None, None) if not force else type(ds)._read_rows(
        ds, rows, names, force)
    assert [r[0] for r in ds.fetch_columns([5, 3, 59], ["dec256"]).rows] == [want[5], want[3], want[59]]


def test_types_are_duckdbs(zoo_path):
    ds = _open(zoo_path)
    page = _direct(ds, 0, 3, ["dict", "lstr", "list", "ts_ms", "ts_off", "f16", "fsl", "null", "uuid"])
    assert [str(t) for t in page.types] == ["string", "string", "list<l: int64>", "timestamp[us]",
                                            "timestamp[us, tz=UTC]", "float", "list<l: int32>", "int32", "string"]
    assert page.rows[0][4].utcoffset().total_seconds() == 0
    assert page.rows[1][8] == "00000000-0000-0000-0000-000000000001"


def test_demo_windows(demo_path):
    ds = _open(demo_path)
    for offset in (0, 2_499, 2_500, 2_450, 12_345, 19_990, 19_999, 20_000, 25_000):
        for limit in (1, 50, 3_000):
            page = _direct(ds, offset, limit)
            assert page is not None
            _same(page, _duck(ds, View(), offset, limit))
    cols = ["mag", "diaSourceId", "band"]
    _same(_direct(ds, 4_990, 20, cols), _duck(ds, View(), 4_990, 20, cols))


def test_odd_file(odd_path):
    ds = _open(odd_path)
    assert not ds._has_rownum
    for offset, limit in ((0, 1_000), (290, 20), (950, 100), (999, 1)):
        page = _direct(ds, offset, limit)
        assert page is not None
        _same(page, _duck(ds, View(), offset, limit))
    assert ds.fetch(View(), 950, 100).row_numbers[0] == 950


def test_many_row_groups(many_path):
    ds = _open(many_path)
    assert ds.meta.num_row_groups == 7 and ds.num_rows == 1_000
    for offset in (0, 99, 100, 136, 137, 138, 386, 387, 388, 486, 487, 999, 1_000):
        for limit in (1, 2, 50, 400, 2_000):
            _same(_direct(ds, offset, limit), _duck(ds, View(), offset, limit))
    _same(_direct(ds, 120, 30, ["s", "id"]), _duck(ds, View(), 120, 30, ["s", "id"]))


def _spy_reads(monkeypatch):
    calls = []
    orig = pq.ParquetFile.iter_batches

    def spy(self, *a, **k):
        calls.append((k.get("row_groups"), k.get("columns"), k.get("use_threads")))
        return orig(self, *a, **k)

    monkeypatch.setattr(pq.ParquetFile, "iter_batches", spy)
    return calls


def test_reads_only_needed_row_groups(many_path, monkeypatch):
    ds = _open(many_path)
    calls = _spy_reads(monkeypatch)
    _force(ds)
    page = ds.fetch(View(), 130, 10, ["x"])  # row groups: 0..99, 100..136, (empty), 137..386
    assert page.row_numbers == list(range(130, 140))
    assert calls == [([1], ["x"], False), ([3], ["x"], False)]  # not the empty one
    calls.clear()
    ds.fetch(View(), 0, 100, ["id", "s"])
    assert calls == [([0], ["id", "s"], False)]
    calls.clear()
    ds.fetch(View(where="id > 3"), 0, 10)  # filtered: DuckDB
    assert calls == []


def test_bounded_read_buffer(many_path, monkeypatch):
    """Without pre-buffering, read through a bounded buffer (buffer_size=0 reads whole column chunks)."""
    ds = _open(many_path)
    seen = []
    orig = pq.ParquetFile.__init__

    def init(self, *a, **k):
        seen.append((k.get("pre_buffer"), k.get("buffer_size")))
        orig(self, *a, **k)

    monkeypatch.setattr(pq.ParquetFile, "__init__", init)
    monkeypatch.setattr(ds, "_direct_estimate", lambda need, names: (True, False))
    ds.fetch(View(), 0, 10)
    monkeypatch.setattr(ds, "_direct_estimate", lambda need, names: (True, True))
    ds.fetch(View(), 0, 10)
    assert seen == [(False, 1 << 20), (True, 0)]


def test_empty_file(tmp_path):
    p = tmp_path / "empty.parquet"
    pq.write_table(pa.table({"a": pa.array([], pa.int64()), "s": pa.array([], pa.string())}), p)
    ds = _open(str(p))
    page = ds.fetch(View(), 0, 100)
    assert page.rows == [] and page.columns == ["a", "s"]
    _same(page, _duck(ds, View(), 0, 100))


def test_cost_model_prefers_duckdb_deep_in_big_row_groups(tmp_path):
    p = tmp_path / "big.parquet"
    n = 1_000_000
    pq.write_table(pa.table({"x": np.arange(n, dtype=np.float64), "y": np.arange(n) * 2}), p, row_group_size=n)
    ds = _open(str(p))
    assert ds._direct_estimate({0: 150}, ["x", "y"])[0]  # the start of a row group: pyarrow
    assert not ds._direct_estimate({0: n - 10}, ["x", "y"])[0]  # its end: DuckDB skips faster
    page = ds.fetch(View(), n - 10, 20)
    assert page.row_numbers == list(range(n - 10, n)) and page.rows[-1] == (n - 1.0, 2 * (n - 1))


def test_many_row_groups_prefer_direct(tmp_path):
    p = tmp_path / "small_rgs.parquet"
    pq.write_table(pa.table({"x": np.arange(50_000, dtype=np.float64)}), p, row_group_size=100)
    ds = _open(str(p))
    assert ds._direct_estimate({250: 100}, ["x"])[0]


def test_unusual_metadata_falls_back(demo_path, monkeypatch):
    ds = _open(demo_path)
    monkeypatch.setattr(ds, "_rg_starts_cache", None, raising=False)  # row counts don't add up, say
    assert ds._fetch_direct(0, 10, None) == (None, None)
    _same(ds.fetch(View(), 0, 10), _duck(ds, View(), 0, 10))
    ds2 = _open(demo_path)
    assert ds2._fetch_direct(0, 10, ["mag", "mag"]) == (None, None)  # duplicate names
    monkeypatch.setattr(ds2, "_types_cache", None)  # DuckDB's schema didn't line up
    assert ds2._fetch_direct(0, 10, None) == (None, None)


def test_fetches_use_duckdb_until_types_are_bound(demo_path, monkeypatch):
    ds = ParquetDataset(demo_path)
    ds._types_done.wait(10)
    ds._types_done.clear()  # as if the background bind were still running
    monkeypatch.setattr(ds, "_start_types", lambda: None)
    assert ds._fetch_direct(0, 10, None) == (None, None)
    assert ds.fetch(View(), 0, 10).row_numbers == list(range(10))


def _served_by_duckdb(ds, monkeypatch):
    log = []
    orig = type(ds)._handover

    def handover(self, token, c):
        log.append(token is not None)
        return orig(self, token, c)

    monkeypatch.setattr(type(ds), "_handover", handover)
    return log


def test_io_errors_fall_back_without_excluding(demo_path, monkeypatch):
    ds = _open(demo_path)
    _force(ds)
    log = _served_by_duckdb(ds, monkeypatch)

    def boom(*a, **k):
        raise OSError("network filesystem hiccup")

    monkeypatch.setattr(pq.ParquetFile, "iter_batches", boom)
    page = ds.fetch(View(), 100, 10, ["mag", "band"])
    assert page.row_numbers == list(range(100, 110))
    assert log == [True]  # tried direct, then DuckDB served it
    assert ds._duck_types()["mag"] is not None and ds._duck_types()["band"] is not None
    monkeypatch.undo()
    _force(ds)
    assert _direct(ds, 100, 10, ["mag", "band"]) is not None


def test_read_errors_exclude_only_the_bad_column(demo_path, monkeypatch):
    ds = _open(demo_path)
    _force(ds)
    log = _served_by_duckdb(ds, monkeypatch)
    orig = pq.ParquetFile.iter_batches

    def picky(self, *a, **k):
        if "band" in k.get("columns", ()):
            raise ValueError("pyarrow can't decode this")
        return orig(self, *a, **k)

    monkeypatch.setattr(pq.ParquetFile, "iter_batches", picky)
    page = ds.fetch(View(), 100, 10, ["mag", "band"])
    assert page.row_numbers == list(range(100, 110)) and log == [True]
    types = ds._duck_types()
    assert types["band"] is None and types["mag"] is not None
    assert _direct(ds, 100, 10, ["mag"]) is not None


def test_cast_failure_after_row_zero_excludes_column(tmp_path):
    """A struct holding zoned ns timestamps: DuckDB truncates nested values to µs, the safe cast
    refuses when a value has sub-µs digits; here only rows >= 50 do."""
    p = tmp_path / "late.parquet"
    n = 100
    ns = np.arange(n, dtype=np.int64) * 1000 + 1_600_000_000 * 10**9
    ns[50:] += 7
    ts = pa.array(ns, pa.timestamp("ns", "Europe/Berlin"))
    pq.write_table(pa.table({"id": np.arange(n), "s": pa.StructArray.from_arrays([ts], ["t"])}), p,
                   row_group_size=10)
    ds = _open(str(p))
    _force(ds)
    if ds._duck_types()["s"] is None:
        pytest.skip("DuckDB's type for the struct isn't castable here")
    _same(ds.fetch(View(), 0, 10), _duck(ds, View(), 0, 10))
    assert ds._duck_types()["s"] is not None
    _same(ds.fetch(View(), 45, 10), _duck(ds, View(), 45, 10))
    assert ds._duck_types()["s"] is None and ds._duck_types()["id"] is not None
    assert _direct(ds, 60, 10, ["id"]) is not None


def _blocking_reads(monkeypatch):
    """iter_batches that waits on ``release`` after yielding its first batch."""
    entered, release = threading.Event(), threading.Event()
    orig = pq.ParquetFile.iter_batches

    def slow(self, *a, **k):
        for b in orig(self, *a, **k):
            entered.set()
            release.wait(5)
            yield b

    monkeypatch.setattr(pq.ParquetFile, "iter_batches", slow)
    return entered, release, orig


def _in_thread(ds, offset, limit, result):
    def run():
        try:
            with ds.tagged("page"):
                result["page"] = ds.fetch(View(), offset, limit)
        except duckdb.InterruptException as e:
            result["error"] = e

    t = threading.Thread(target=run)
    t.start()
    return t


def test_superseded_fetch_is_interrupted(many_path, monkeypatch):
    """A newer fetch under the same tag cancels an older direct read, like a DuckDB query."""
    ds = _open(many_path)
    _force(ds)
    entered, release, orig = _blocking_reads(monkeypatch)
    result = {}
    t = _in_thread(ds, 0, 500, result)
    assert entered.wait(5)
    monkeypatch.setattr(pq.ParquetFile, "iter_batches", orig)
    with ds.tagged("page"):
        newer = ds.fetch(View(), 10, 5)
    release.set()
    t.join(5)
    assert newer.row_numbers == list(range(10, 15))
    assert "error" in result and "page" not in result
    # ds.interrupt() also stops one
    entered, release, orig = _blocking_reads(monkeypatch)
    result.clear()
    t = _in_thread(ds, 0, 500, result)
    assert entered.wait(5)
    ds.interrupt()
    release.set()
    t.join(5)
    assert "error" in result


def test_superseded_fetch_falling_back_doesnt_override_newer(demo_path, monkeypatch):
    """An old direct read that fails (here: a cast) after a newer fetch started must not
    fall back to DuckDB: that would interrupt the newer fetch and deliver a stale page."""
    import pqx.data as D

    ds = _open(demo_path)
    _force(ds)
    in_cast, go_on = threading.Event(), threading.Event()
    orig_convert = D._convert

    def convert(col, d):
        if threading.current_thread().name == "old":
            in_cast.set()
            go_on.wait(5)
            raise pa.ArrowInvalid("lossy")
        return orig_convert(col, d)

    monkeypatch.setattr(D, "_convert", convert)
    result = {}

    def old():
        try:
            with ds.tagged("page"):
                result["old"] = ds.fetch(View(), 10_000, 50, ["mag"])
        except duckdb.InterruptException:
            result["old"] = "interrupted"

    t = threading.Thread(target=old, name="old")
    t.start()
    assert in_cast.wait(5)
    with ds.tagged("page"):
        newer = ds.fetch(View(), 100, 50, ["mag"])
    go_on.set()
    t.join(5)
    assert newer.row_numbers[0] == 100
    assert result["old"] == "interrupted"


def test_concurrent_fetches(demo_path):
    ds = _open(demo_path)
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


def _rows_via_duckdb(ds, rows, columns):
    out = []
    for r in rows:
        out.append(_duck(ds, View(), r, 1, columns).rows[0])
    return out


@pytest.mark.parametrize("direct", [True, False])
def test_fetch_columns(many_path, demo_path, odd_path, direct):
    for path, cols in ((many_path, ["s", "x"]), (demo_path, ["band", "mag", "diaSourceId"]),
                       (odd_path, ["tags", "file_row_number", "pos"])):
        ds = _open(path)
        if direct:
            _force(ds)
        else:
            ds._read_rows = lambda rows, names: (None, None)
        n = ds.num_rows
        for rows in ([5, 3, 3, n - 1, 0], list(range(90, 140)), [n // 2], [], list(range(n - 7, n))[::-1]):
            page = ds.fetch_columns(rows, cols)
            assert page.row_numbers == rows and page.columns == cols
            assert [_norm(r) for r in page.rows] == [_norm(r) for r in _rows_via_duckdb(ds, rows, cols)]
            assert [str(t) for t in page.types] == [str(t) for t in _duck(ds, View(), 0, 1, cols).types]
    with pytest.raises(IndexError):
        ds.fetch_columns([n], cols)


def test_fetch_columns_for_a_sorted_page(demo_path, monkeypatch):
    ds = _open(demo_path)
    v = View(where="mag < 21", order_by=[("mag", True)])
    page = ds.fetch(v, 30, 40, ["mag"])
    calls = _spy_reads(monkeypatch)
    _force(ds)
    more = ds.fetch_columns(page.row_numbers, ["diaSourceId", "mag"])
    assert [r[1] for r in more.rows] == [r[0] for r in page.rows]
    assert {tuple(c[0]) for c in calls} <= {(i,) for i in range(ds.meta.num_row_groups)}
    assert all(c[1] == ["diaSourceId", "mag"] for c in calls)


def test_leaves_with_flat_dotted_name(tmp_path):
    p = tmp_path / "dots.parquet"
    pq.write_table(pa.table({"a": pa.array([{"b": 1, "c": 2.0}]), "a.b": pa.array([3]),
                             "m": pa.array([[("k", 1)]], pa.map_(pa.string(), pa.int64()))}), p)
    ds = _open(str(p))
    assert ds._leaves() == {"a": [0, 1], "a.b": [2], "m": [3, 4]}
    page = _direct(ds, 0, 1, ["a.b", "a"])
    _same(page, _duck(ds, View(), 0, 1, ["a.b", "a"]))


def test_fetch_columns_needs_row_ids(odd_path, demo_path):
    odd = _open(odd_path)
    v = View(order_by=[("x", True)])
    page = odd.fetch(v, 0, 10, ["x"])
    assert page.row_numbers == [None] * 10 and not odd.has_row_ids(v)
    with pytest.raises(ValueError):
        odd.fetch_columns(page.row_numbers, ["tags"])
    assert odd.has_row_ids(View())
    demo = _open(demo_path)
    assert demo.has_row_ids(View()) and demo.has_row_ids(v.__class__(where="mag < 20"))
    assert not demo.has_row_ids(View(sql="select * from t"))


def test_fetch_columns_with_view_types(tmp_path, monkeypatch):
    """pyarrow has no take kernel for string/binary views; scattered rows must still go direct."""
    p = tmp_path / "views.parquet"
    n = 3_000
    pq.write_table(pa.table({"id": np.arange(n), "s": pa.array([f"s{i}" for i in range(n)], pa.string_view()),
                             "b": pa.array([b"%d" % i for i in range(n)], pa.binary_view()),
                             "l": pa.array([[f"x{i}"] for i in range(n)], pa.list_(pa.string_view()))}),
                   p, row_group_size=500)
    ds = _open(str(p))
    _force(ds)
    log = _served_by_duckdb(ds, monkeypatch)
    rows = [2_999, 5, 1_200, 5, 777]
    page = ds.fetch_columns(rows, ["s", "b", "l", "id"])
    assert log == [] and [r[3] for r in page.rows] == rows
    assert [r[0] for r in page.rows] == [f"s{i}" for i in rows] and page.rows[0][2] == ["x2999"]
    assert [str(t) for t in page.types] == [str(t) for t in _duck(ds, View(), 0, 1, ["s", "b", "l", "id"]).types]


def test_unpinned_read_error_remembers_row_groups(many_path, monkeypatch):
    """A read error the one-row probe can't reproduce leaves those row groups to DuckDB for good."""
    ds = _open(many_path)
    _force(ds)
    orig = type(ds)._read_rg
    calls = []

    def flaky(self, rg, local, names, pre_buffer, token):
        calls.append(rg)
        if rg == 3 and local != range(0, 1):
            raise ValueError("bad page deep in row group 3")
        return orig(self, rg, local, names, pre_buffer, token)

    monkeypatch.setattr(type(ds), "_read_rg", flaky)
    _same(ds.fetch(View(), 300, 20), _duck(ds, View(), 300, 20))
    assert ds._bad_rgs == {3} and all(t is not None for t in ds._duck_types().values())
    calls.clear()
    ds.fetch(View(), 300, 20)
    assert calls == []  # straight to DuckDB, no failed read and probe every time
    ds.fetch(View(), 0, 20)
    assert calls == [0]  # other row groups keep the direct path


def test_io_error_count_resets(demo_path, monkeypatch):
    ds = _open(demo_path)
    _force(ds)
    orig = pq.ParquetFile.iter_batches
    fail = {"on": True}

    def sometimes(self, *a, **k):
        if fail["on"]:
            raise OSError("hiccup")
        return orig(self, *a, **k)

    monkeypatch.setattr(pq.ParquetFile, "iter_batches", sometimes)
    ds.fetch(View(), 0, 10)
    ds.fetch(View(), 0, 10)
    assert ds._io_errors == 2
    fail["on"] = False
    ds.fetch(View(), 0, 10)
    assert ds._io_errors == 0


def test_extreme_timestamps_dont_raise(tmp_path):
    p = tmp_path / "extreme.parquet"
    v = np.array([0, 9_223_372_036_854_775_000, -9_223_372_036_854_775_000, 1], dtype="i8")
    pq.write_table(pa.table({"t": pa.array(v, pa.timestamp("ns", "UTC")), "i": np.arange(4)}), p)
    ds = _open(str(p))
    for page in (_direct(ds, 0, 4), _duck(ds, View(), 0, 4)):
        assert len(page.rows) == 4 and page.rows[0][0].year == 1970 and page.rows[3][1] == 3


def test_bind_is_not_registered_for_interrupts(demo_path):
    ds = ParquetDataset(demo_path)
    with ds._lock:
        assert not ds._tagged  # ds.interrupt() can't shorten a bind, only throw it away
    ds.interrupt()
    assert ds._duck_types(wait=True) is not None
