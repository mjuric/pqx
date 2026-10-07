"""Startup: the footer summary is computed once, off the UI thread, and the grid doesn't wait for it."""
import math
import threading

import numpy as np
import pyarrow as pa
import pyarrow.parquet as pq
import pytest
from textual.widgets import DataTable, Static, TabbedContent
from textual.worker import WorkerState

from pqx.app import ROWGROUP_BATCH, GridTable, PqxApp
from pqx.data import ParquetDataset, Stopped
from test_fetch import _zoo

SIZE = (150, 42)


@pytest.fixture(scope="module")
def zoo_path(tmp_path_factory):
    p = tmp_path_factory.mktemp("zoo") / "zoo.parquet"
    pq.write_table(_zoo(), p, row_group_size=7)
    return str(p)


def _reference_summary(md) -> list[dict]:
    """The summary as computed before it was optimized: every chunk's statistics converted with ``min``/``max``."""
    out: dict[str, dict] = {}
    paths = [md.schema.column(i).path for i in range(md.num_columns)]
    for rg in range(md.num_row_groups):
        g = md.row_group(rg)
        for i in range(g.num_columns):
            c = g.column(i)
            d = out.setdefault(paths[i], dict(path=paths[i], physical=c.physical_type, compression=c.compression,
                                              encodings=set(), compressed=0, uncompressed=0, min=None, max=None,
                                              nulls=0, has_stats=True))
            d["compressed"] += c.total_compressed_size
            d["uncompressed"] += c.total_uncompressed_size
            d["encodings"].update(c.encodings)
            st = c.statistics
            if st is None or not st.has_min_max:
                d["has_stats"] = d["has_stats"] and st is not None and st.has_null_count
            else:
                try:
                    d["min"] = st.min if d["min"] is None else min(d["min"], st.min)
                    d["max"] = st.max if d["max"] is None else max(d["max"], st.max)
                except TypeError:
                    pass
            if st is not None and st.has_null_count:
                d["nulls"] += st.null_count
    for i in range(md.num_columns):
        sc = md.schema.column(i)
        if sc.path in out:
            out[sc.path]["logical"] = str(sc.logical_type) if sc.logical_type else ""
    return list(out.values())


def _same(a, b) -> bool:
    if isinstance(a, float) and isinstance(b, float) and math.isnan(a) and math.isnan(b):
        return True
    return type(a) is type(b) and a == b


@pytest.mark.parametrize("fixture", ["demo_path", "odd_path", "zoo_path"])
def test_summary_matches_reference(fixture, request):
    """Same values and Python types as converting every chunk's statistics (raw ones for plain columns)."""
    ds = ParquetDataset(request.getfixturevalue(fixture))
    ref = _reference_summary(ds.meta)
    got = ds.column_chunk_summary()
    assert [d["path"] for d in got] == [d["path"] for d in ref]
    for r, g in zip(ref, got):
        assert ds.column_encodings(r["path"]) == r.pop("encodings"), r["path"]
        assert g.keys() == r.keys() and all(_same(g[k], r[k]) for k in r), (r, g)
    rgs = ds.row_groups()
    md = ds.meta
    assert [r["compressed"] for r in rgs] == [
        sum(md.row_group(i).column(j).total_compressed_size for j in range(md.row_group(i).num_columns))
        for i in range(md.num_row_groups)]
    assert [r["start"] for r in rgs][:2] == [0, md.row_group(0).num_rows]


def test_summary_computed_once(demo_path, monkeypatch):
    calls = []
    orig = ParquetDataset._scan_footer
    gate = threading.Event()

    def scan(self, stop=None):
        calls.append(threading.current_thread().name)
        gate.wait(5)
        return orig(self, stop)

    monkeypatch.setattr(ParquetDataset, "_scan_footer", scan)
    ds = ParquetDataset(demo_path)
    ts = [threading.Thread(target=f) for f in (ds.column_chunk_summary, ds.row_groups, ds.column_chunk_summary)]
    for t in ts:
        t.start()
    gate.set()
    for t in ts:
        t.join()
    ds.column_chunk_summary()
    ds.row_groups()
    assert len(calls) == 1


def test_sampling_does_not_scan_the_footer(demo_path, monkeypatch):
    ds = ParquetDataset(demo_path)
    monkeypatch.setattr(ParquetDataset, "_scan_footer", lambda self: pytest.fail("scanned"))
    cond = ds.sample_condition(3000, slices=4)
    assert cond.count("OR") == 3 and ds.cursor().execute(f"SELECT count(*) FROM __pqx_src WHERE {cond}").fetchone()[0]


# ------------------------------------------------------------------- the app
async def settle(pilot, app, timeout=10.0):
    await pilot.pause(0.05)
    t = 0.0
    while app._busy or any(w.state in (WorkerState.PENDING, WorkerState.RUNNING) for w in app.workers):
        await pilot.pause(0.05)
        t += 0.05
        if t > timeout:
            raise TimeoutError(f"still busy: {app._busy}")
    await pilot.pause(0.05)


def _held_summary(monkeypatch):
    """Make the footer pass wait for ``gate``; ``threads`` records where it ran."""
    gate = threading.Event()
    threads = []
    orig = ParquetDataset.column_chunk_summary

    def summary(self, stop=None):
        threads.append(threading.current_thread() is threading.main_thread())
        gate.wait(10)
        return orig(self, stop)

    monkeypatch.setattr(ParquetDataset, "column_chunk_summary", summary)
    return gate, threads


async def _until(pilot, cond, timeout=10.0):
    t = 0.0
    while not cond():
        await pilot.pause(0.02)
        t += 0.02
        assert t < timeout


def plain(widget) -> str:
    r = widget.render()
    return getattr(r, "plain", str(r))


async def test_grid_usable_before_tabs_built(demo_path, monkeypatch):
    gate, threads = _held_summary(monkeypatch)
    app = PqxApp(demo_path)
    async with app.run_test(size=SIZE) as pilot:
        g = app.query_one(GridTable)
        st = app.query_one("#schema-table", DataTable)
        await _until(pilot, lambda: g.row_count > 0)
        await pilot.press("down", "right")
        await pilot.pause(0.1)
        assert g.abs_row == 1 and g.cursor_column == 1
        assert st.row_count == 0 and threads == [False]  # still reading, on a worker thread
        assert plain(app.query_one("#schema-desc", Static)).startswith("Reading sizes and statistics")
        await pilot.press("5")
        await pilot.pause(0.1)
        assert plain(app.query_one("#meta-status", Static)).startswith("Reading the footer")
        assert app.query_one("#rowgroups", DataTable).row_count == 0
        gate.set()
        await settle(pilot, app)
        assert st.row_count == len(app.ds.columns) and app.query_one("#rowgroups", DataTable).row_count == 8
        assert plain(app.query_one("#meta-status", Static)).startswith("✓ Footer read")
        assert threads == [False]
        assert app.current_column == app.ds.column_names[1]  # building Schema didn't move it


async def test_schema_built_late_follows_current_column(demo_path, monkeypatch):
    """Switching to Schema while it's being read: once built, it lands on the current column."""
    gate, _ = _held_summary(monkeypatch)
    app = PqxApp(demo_path)
    async with app.run_test(size=SIZE) as pilot:
        names = app.ds.column_names
        g = app.query_one(GridTable)
        st = app.query_one("#schema-table", DataTable)
        await _until(pilot, lambda: g.row_count > 0)
        g.move_cursor(column=names.index("mag"))
        await pilot.pause(0.1)
        await pilot.press("2")
        await pilot.pause(0.1)
        assert app.query_one(TabbedContent).active == "tab-schema" and st.row_count == 0
        gate.set()
        await settle(pilot, app)
        assert st.cursor_row == names.index("mag") and app.current_column == "mag"
        assert plain(app.query_one("#schema-desc", Static)).startswith("mag")
        st.move_cursor(row=names.index("dec"))  # and it's linked as usual from then on
        await pilot.pause(0.1)
        assert app.current_column == "dec"
        await pilot.press("enter")
        await settle(pilot, app)
        assert app.query_one(TabbedContent).active == "tab-stats" and app._stats_shown == "dec"


async def test_schema_built_late_on_first_column_and_sql_result(demo_path, monkeypatch):
    """Built while shown with the current column not in the file: Schema keeps row 0, the current column stays."""
    gate, _ = _held_summary(monkeypatch)
    app = PqxApp(demo_path, where="select ra, mag*2 as m2 from t")
    async with app.run_test(size=SIZE) as pilot:
        g = app.query_one(GridTable)
        st = app.query_one("#schema-table", DataTable)
        await _until(pilot, lambda: g.row_count > 0)
        await pilot.press("end")
        await pilot.pause(0.1)
        assert app.current_column == "m2"
        await pilot.press("2")
        await pilot.pause(0.1)
        gate.set()
        await settle(pilot, app)
        assert st.row_count == len(app.ds.columns) and st.cursor_row == 0
        assert app.current_column == "m2"
        assert plain(app.query_one("#schema-desc", Static)).startswith(app.ds.column_names[0])


async def test_footer_failure_is_shown(demo_path, monkeypatch):
    def boom(self, stop=None):
        raise ValueError("bad statistics")

    monkeypatch.setattr(ParquetDataset, "column_chunk_summary", boom)
    app = PqxApp(demo_path)
    async with app.run_test(size=SIZE) as pilot:
        await settle(pilot, app)
        assert app.query_one(GridTable).row_count > 0
        assert "bad statistics" in plain(app.query_one("#meta-status", Static))
        await pilot.press("2")
        await settle(pilot, app)
        assert "bad statistics" in plain(app.query_one("#schema-desc", Static))


async def test_rowgroup_table_filled_in_batches(tmp_path):
    """Thousands of row groups go into Metadata's table a batch per event-loop turn, all of them in the end."""
    p = tmp_path / "many.parquet"
    pq.write_table(pa.table({"x": np.arange(700)}), p, row_group_size=1)
    assert ROWGROUP_BATCH < 700
    app = PqxApp(str(p))
    async with app.run_test(size=SIZE) as pilot:
        await settle(pilot, app)
        t = app.query_one("#rowgroups", DataTable)
        await _until(pilot, lambda: t.row_count == 700)
        assert [str(t.get_row_at(i)[1]) for i in (0, 699)] == ["0", "699"]


async def test_file_duckdb_cannot_read(demo_path, monkeypatch):
    """DuckDB failing on the file says so (not "Query failed … edit with /"); Schema and Metadata still fill."""
    import duckdb

    def boom(self):
        raise duckdb.InvalidInputException(f"Invalid Input Error: Failed to read Parquet file '{self.path}': "
                                           "Need at least one non-root column in the file")

    monkeypatch.setattr(ParquetDataset, "_create_views", boom)
    app = PqxApp(demo_path)
    async with app.run_test(size=SIZE) as pilot:
        await settle(pilot, app)
        s = plain(app.query_one("#status", Static))
        assert s.startswith("✗ DuckDB can't read this file") and "Need at least one non-root column" in s
        assert "edit with" not in s and "Query failed" not in s and demo_path not in s
        assert app.query_one("#schema-table", DataTable).row_count == len(app.ds.columns)
        assert plain(app.query_one("#meta-status", Static)).startswith("✓ Footer read")
        # keys don't keep trying (or keep notifying): one notification, whatever is pressed
        fetches = []
        monkeypatch.setattr(ParquetDataset, "fetch", lambda *a, **k: fetches.append(a))
        for key in ["down", "pagedown", "right", "ctrl+end", "end", "home", "d", "down"]:
            await pilot.press(key)
        await pilot.press("slash", *"mag > 1", "enter")
        await settle(pilot, app)
        assert len(app._notifications) == 1 and not fetches
        assert plain(app.query_one("#status", Static)).startswith("✗ DuckDB can't read this file")


async def test_zero_column_file(tmp_path):
    p = tmp_path / "nocols.parquet"
    pq.write_table(pa.table({}), p)
    app = PqxApp(str(p))
    async with app.run_test(size=SIZE) as pilot:
        await settle(pilot, app)
        assert plain(app.query_one("#status", Static)).startswith("✗ DuckDB can't read this file")


def test_footer_scan_stops_and_starts_over(demo_path):
    ds = ParquetDataset(demo_path)
    with pytest.raises(Stopped):
        ds.column_chunk_summary(lambda: True)
    assert ds._footer is None
    assert len(ds.column_chunk_summary()) == len(ds.columns)


async def test_quit_while_reading_footer(demo_path, monkeypatch):
    """q while the footer pass runs: the pass stops at its next row group, and nothing is reported."""
    orig = ParquetDataset._scan_footer
    entered = threading.Event()
    stopped = []

    def scan(self, stop=None):
        entered.set()
        while not stop():  # a pass that lasts until it's told to stop
            threading.Event().wait(0.01)
        try:
            return orig(self, stop)
        except Exception as e:  # noqa: BLE001
            stopped.append(type(e).__name__)
            raise

    monkeypatch.setattr(ParquetDataset, "_scan_footer", scan)
    errors = []
    app = PqxApp(demo_path)
    app._footer_failed = lambda e: errors.append(e)
    async with app.run_test(size=SIZE) as pilot:
        await _until(pilot, entered.is_set)
        await pilot.press("q")
    assert stopped == ["Stopped"] and not errors
