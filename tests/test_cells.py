"""Lazily formatted grid cells: laziness, column widths, raw toggle and format changes."""
import numpy as np
import pyarrow as pa
import pyarrow.parquet as pq
import pytest
from rich.text import Text

from pqx import fmt as F
from pqx.app import GridTable, PqxApp
from pqx.cells import Cell, CellRow, ColumnCells, text_width, widest_candidates

from test_app import SIZE, settle


class FakeColumn:
    content_width = 3


def test_cell_formats_once_and_again_after_invalidate():
    calls = []

    class Counting(F.CellFormatter):
        def __call__(self, v, raw=False):
            calls.append(v)
            return super().__call__(v, raw)

    grown = []
    cc = ColumnCells(Counting("x", pa.float64()), False, FakeColumn(), lambda: grown.append(1))
    c = Cell(1.0 / 3, cc)
    assert not calls and not c.formatted       # nothing happens until the cell is used
    assert str(c) == "0.333333333" and isinstance(c.__rich__(), Text)
    assert c.text.justify == "right"
    assert len(calls) == 1 and c.formatted     # formatted once, then cached
    assert cc.column.content_width == 11 and grown == [1]  # and the column grew to fit
    cc.invalidate(raw=True)
    assert not c.formatted and len(calls) == 1
    assert str(c) == repr(1.0 / 3) and len(calls) == 2
    assert cc.column.content_width == len(repr(1.0 / 3))
    cc.invalidate(raw=False)
    assert str(c) == "0.333333333"
    assert cc.column.content_width == len(repr(1.0 / 3))  # columns never narrow by themselves


def test_cell_row_makes_cells_on_first_read():
    ccs = [ColumnCells(F.CellFormatter(n, t), False) for n, t in (("a", pa.int64()), ("b", pa.string()))]
    row = CellRow((7,), (["a", "b"], ccs))  # a short row reads as NULLs, as DataTable's add_row pads it
    assert len(row) == 0
    assert str(row["b"]) == F.NULL and len(row) == 2  # one read makes the whole row
    assert row["a"].value == 7 and row["a"] is row["a"]
    with pytest.raises(KeyError):
        row["c"]


def test_widest_candidates_find_the_widest_number():
    rng = np.random.default_rng(3)
    exact = total = 0
    for kind, name, typ in (("float", "x", pa.float64()), ("mag", "mag", pa.float64()), ("int", "n", pa.int64()),
                            ("float32", "y", pa.float32())):
        fm = F.CellFormatter(name, typ)
        assert fm.kind == kind
        for scale in (1e-4, 1e-2, 1, 1e3, 1e8):
            for raw in (False, True):
                for _ in range(10):
                    v = rng.normal(scale=scale, size=300)
                    vals = [int(x) for x in v] if kind == "int" else list(map(float, v))
                    vals[5], vals[7] = None, (float("nan") if kind != "int" else None)
                    widest = max(text_width(fm(x, raw)) for x in vals)
                    got = max(text_width(fm(x, raw)) for x in widest_candidates(vals, kind, raw))
                    # a guess can only miss by trailing zeros it lost (fit_visible catches those)
                    assert widest - 2 <= got <= widest, (kind, scale, raw)
                    exact += got == widest
                    total += 1
    assert exact / total > 0.98
    assert widest_candidates(["a", "abcd", None, "ab"], "str") == ["abcd", "ab", "a"]
    assert widest_candidates([b"x"], "binary") is None  # not guessable: rows get sampled
    assert widest_candidates([None, float("nan")], "float") == []


@pytest.fixture
def wide_path(tmp_path):
    rng = np.random.default_rng(5)
    n, cols = 2000, 120
    data = {"id": np.arange(n, dtype=np.int64)}
    for i in range(cols - 1):
        data[f"c{i:03d}"] = rng.normal(scale=10 ** (i % 7 - 2), size=n)
    p = tmp_path / "wide.parquet"
    pq.write_table(pa.table(data), p)
    return str(p)


async def test_window_load_formats_only_what_is_drawn(wide_path, monkeypatch):
    calls = [0]
    orig = F.format_value

    def counting(*a, **kw):
        calls[0] += 1
        return orig(*a, **kw)

    monkeypatch.setattr(F, "format_value", counting)
    app = PqxApp(wide_path)
    async with app.run_test(size=SIZE) as pilot:
        await settle(pilot, app)
        g = app.query_one(GridTable)
        ncols = len(g.columns)
        cells = g.row_count * ncols
        h = g.scrollable_content_region.height
        # what's drawn (at most every column of the rows on screen) plus a dozen or so candidates per column
        budget = (h + 16) * ncols
        assert budget < cells / 2
        assert 0 < calls[0] <= budget

        for key in ("ctrl+end", "f", "f", "pagedown"):
            calls[0] = 0
            await pilot.press(key)
            await settle(pilot, app)
            assert calls[0] <= budget, key


def _widths(g):
    return [c.content_width for c in g.ordered_columns]


def _drawn_cells_fit(app, g):
    """Every cell on screen fits its column (computed without touching the cells)."""
    rows, cols = g.visible_cells()
    assert len(rows) > 10 and len(cols) > 3
    for c in cols:
        name, col = app.page.columns[c], g.ordered_columns[c]
        fm = app.formatters[name]
        need = max(text_width(fm(app.page.rows[r][c], app.raw)) for r in rows)
        assert col.content_width >= need, (name, app.raw)


async def test_drawn_cells_are_never_cut_off(demo_path, odd_path, wide_path):
    for path in (demo_path, odd_path, wide_path):
        app = PqxApp(path)
        async with app.run_test(size=SIZE) as pilot:
            await settle(pilot, app)
            g = app.query_one(GridTable)
            for keys in ([], ["f"], ["pagedown"] * 3, ["f"], ["ctrl+end"], ["pageup"] * 2, ["end"], ["f"],
                         ["ctrl+home"]):
                await pilot.press(*keys)
                await settle(pilot, app)
                _drawn_cells_fit(app, g)


async def test_column_grows_when_a_wider_cell_is_drawn(tmp_path):
    n = 1000
    blobs = [b""] * n
    blobs[503] = bytes(range(40))  # far from the sampled rows and the first screen
    p = tmp_path / "blob.parquet"
    pq.write_table(pa.table({"id": np.arange(n), "blob": pa.array(blobs, type=pa.binary())}), p)
    app = PqxApp(str(p))
    async with app.run_test(size=SIZE) as pilot:
        await settle(pilot, app)
        g = app.query_one(GridTable)
        blob = g.ordered_columns[1]
        long = F.format_value(blobs[503], "binary")
        before = _widths(g)
        assert blob.content_width < len(long)
        g.move_cursor(row=503, animate=False)
        await settle(pilot, app)
        assert blob.content_width == len(long)  # grown on first draw...
        assert all(b >= a for a, b in zip(before, _widths(g)))
        y = 503 - int(g.scroll_y) + g.header_height
        assert long in g.render_line(y).text  # ...and drawn whole
        g.move_cursor(row=0, animate=False)
        await settle(pilot, app)
        assert blob.content_width == len(long)  # and it never narrows again


async def test_raw_toggle_and_format_change_redraw(demo_path):
    app = PqxApp(demo_path)
    async with app.run_test(size=SIZE) as pilot:
        await settle(pilot, app)
        g = app.query_one(GridTable)
        ra_i = app.cols_shown.index("ra")
        g.move_cursor(column=ra_i)
        await settle(pilot, app)
        ra = app.page.rows[g.cursor_row][ra_i]
        y = g.cursor_row - int(g.scroll_y) + g.header_height

        def line():
            return g.render_line(y).text

        assert f"{ra:.6f}" in line()
        w0 = _widths(g)
        await pilot.press("f")
        await settle(pilot, app)
        assert str(g.get_cell_at(g.cursor_coordinate)) == repr(ra) and repr(ra) in line()
        w_raw = _widths(g)
        assert all(b >= a for a, b in zip(w0, w_raw))
        await pilot.press("f")
        await settle(pilot, app)
        assert str(g.get_cell_at(g.cursor_coordinate)) == f"{ra:.6f}" and f"{ra:.6f}" in line()
        assert _widths(g) == w_raw  # widths only grow, so nothing jumps back and forth

        await pilot.press("greater_than_sign", "greater_than_sign")
        await settle(pilot, app)
        assert f"{ra:.8f}" in line()
        wide = g.ordered_columns[ra_i].content_width
        for _ in range(8):
            await pilot.press("less_than_sign")
        await settle(pilot, app)
        assert f"{ra:.0f}" == str(g.get_cell_at(g.cursor_coordinate))
        narrow = g.ordered_columns[ra_i].content_width
        assert narrow < wide  # a format change lets its column shrink to fit
        header = g.ordered_columns[ra_i].label
        need = max(max(text_width(F.CellFormatter("ra", pa.float64(), "", 0)(r[ra_i])) for r in app.page.rows),
                   max(t.cell_len for t in header.split()))
        assert narrow == need
