"""GridTable renders only the columns in view: guards for speed, Textual drift and look."""
import hashlib
import inspect

import numpy as np
import pyarrow as pa
import pyarrow.parquet as pq
import pytest
from textual.coordinate import Coordinate
from textual.geometry import Region
from textual.widgets import DataTable

from pqx.app import GridTable, PqxApp
from test_app import settle

SIZE = (150, 42)
NCOLS = 300


@pytest.fixture(scope="module")
def wide300_path(tmp_path_factory):
    rng = np.random.default_rng(5)
    cols = {"id": np.arange(500)}
    for i in range(NCOLS - 1):
        cols[f"c{i:03d}"] = rng.normal(scale=10 ** (i % 7 - 2), size=500)
    p = tmp_path_factory.mktemp("data") / "wide300.parquet"
    pq.write_table(pa.table(cols), p)
    return str(p)


def test_textual_render_hooks_unchanged():
    """GridTable._render_line_in_row replaces DataTable's and leans on its internals:
    if Textual changes them, re-check the override against the new implementation."""
    sig = inspect.signature(DataTable._render_line_in_row)
    assert list(sig.parameters) == ["self", "row_key", "line_no", "base_style", "cursor_location",
                                    "hover_location"]
    assert list(inspect.signature(DataTable._render_cell).parameters) == [
        "self", "row_index", "column_index", "base_style", "width", "cursor", "hover"]
    assert list(inspect.signature(DataTable._render_line).parameters) == ["self", "y", "x1", "x2", "base_style"]
    assert list(inspect.signature(DataTable._get_styles_to_render_cell).parameters) == [
        "self", "is_header_cell", "is_row_label_cell", "is_fixed_style_cell", "hover", "cursor", "show_cursor",
        "show_hover_cursor", "has_css_foreground_priority", "has_css_background_priority"]
    src = inspect.getsource(DataTable._render_line)
    # _render_line crops the scrollable part to [x1 + fixed width, x2): our blanks must fall outside it
    assert "line_crop(scrollable_line, x1 + fixed_width, x2, width)" in src
    assert "self._render_line_in_row(" in src
    t = DataTable()
    for cache in ("_row_render_cache", "_cell_render_cache", "_line_cache"):
        assert hasattr(t, cache)


# Hashes of the DataTable code GridTable copies or relies on, as of Textual 8.2.8.
UPSTREAM_SOURCE = {
    "_render_line_in_row": "669cc46a7ca119d2",
    "_render_line": "a32c6991165cea90",
    "render_line": "dfc24fd4f26a530e",
    "render_lines": "4e0ae4f0a9bd61c0",
    "ordered_columns": "aa1124cb7bcaa0ce",
    # Lazy cells (GridTable.set_rows / _compute_row_renderables / fit_visible, pqx/cells.py) stand in
    # for add_row and its idle measuring, hand rows over as CellRow/RowCells, settle dimensions
    # themselves and scroll the cursor with DataTable's own helpers.
    "add_row": "9404928b4fdd9dab",
    "_compute_row_renderables": "6906170428250b3c",
    "_get_row_renderables": "4766e26ce80d09dd",
    "get_row": "7f4134ffa6f7b3b9",
    "_on_idle": "d94152378c36b0c2",
    "_update_dimensions": "c62ef3216f7c354b",
    "_render_cell": "56c87899a3bfc4da",  # also: GridTable._render_cell's fast path mirrors it
    "_get_styles_to_render_cell": "058dea3ae068f137",
    "clear": "6af902cb1b84743d",
    "move_cursor": "f312ced43e2c45fd",
    "watch_cursor_coordinate": "a4f113b27d09127b",
    "watch_fixed_columns": "af846fa34a6922c8",
    "_scroll_cursor_into_view": "9e4419fb40c5be82",
}


@pytest.mark.parametrize("name", UPSTREAM_SOURCE)
def test_textual_render_source_unchanged(name):
    obj = getattr(DataTable, name)
    src = inspect.getsource(obj.fget if isinstance(obj, property) else obj)
    assert hashlib.sha256(src.encode()).hexdigest()[:16] == UPSTREAM_SOURCE[name], (
        f"Textual changed DataTable.{name}. Diff it against the version GridTable was written for "
        f"(Textual 8.2.8), port any change into GridTable (pqx/app.py: the rendering overrides, or the "
        f"lazy-cell ones: set_rows, _compute_row_renderables, fit_visible, scroll_cursor_fitted) and "
        f"pqx/cells.py, run tests/test_render.py and tests/test_cells.py, then update this hash.")


def _strips(g: GridTable) -> list:
    """The grid's rendered lines, as (text, style) segments (styles carry the click/hover metadata)."""
    g._styles_cache.clear()
    g._line_cache.clear()
    g._geometry = None
    w, h = g.size
    return [[(s.text, s.style) for s in strip] for strip in g.render_lines(Region(0, 0, w, h))]


def _reference(g: GridTable) -> list:
    """The same, rendered by DataTable's own _render_line_in_row from cold caches."""
    g.render_all_columns = True
    try:
        g._clear_caches()
        return _strips(g)
    finally:
        g.render_all_columns = False
        g._clear_caches()


async def _check_look(pilot, app, g: GridTable, what: str) -> None:
    await pilot.pause(0.05)
    shot = app.export_screenshot()
    ours = _strips(g)  # warm row/cell caches, as in real use
    assert ours == _reference(g), what
    g.refresh()
    await pilot.pause(0.05)
    assert app.export_screenshot() == shot, what


async def test_render_matches_datatable(wide300_path):
    app = PqxApp(wide300_path)
    async with app.run_test(size=SIZE) as pilot:
        await settle(pilot, app)
        g = app.query_one(GridTable)
        await _check_look(pilot, app, g, "start")

        for _ in range(3):
            await pilot.press("right")
        await pilot.press("down", "down")
        await settle(pilot, app)
        await _check_look(pilot, app, g, "after moves")

        # scrolled so that columns are cut at both edges
        widths = [c.get_render_width(g) for c in g.ordered_columns]
        g.scroll_to(x=sum(widths[:40]) + widths[40] // 2, animate=False)
        await pilot.pause(0.1)
        assert g.column_window()[2] > 0 and g.column_window()[3] > 0
        x = g.scroll_offset.x
        assert any(a < x < a + w for a, w in zip(np.cumsum([0] + widths[:-1]), widths)), "no partly visible column"
        await _check_look(pilot, app, g, "cut columns")

        # the mouse over a cell in view: hover look
        g._set_hover_cursor(True)
        g.hover_coordinate = Coordinate(4, g.column_window()[0] + 1)
        await _check_look(pilot, app, g, "hover")
        g._set_hover_cursor(False)

        # pinned columns, scrolled far right
        g.move_cursor(column=2)
        await pilot.press("p")
        await settle(pilot, app)
        assert g.fixed_columns == 3
        await pilot.press("end")
        await settle(pilot, app)
        await _check_look(pilot, app, g, "pinned, at the end")
        await pilot.press("up", "left", "left")
        await settle(pilot, app)
        await _check_look(pilot, app, g, "pinned, moved")

        # sorted (header arrow) and blurred
        await pilot.press("s")
        await settle(pilot, app)
        await _check_look(pilot, app, g, "sorted")
        g.blur()
        await _check_look(pilot, app, g, "blurred")


async def test_render_matches_datatable_narrow(demo_path):
    """Tables up to three screens wide render every column (the cache then survives sideways moves)."""
    app = PqxApp(demo_path)
    async with app.run_test(size=SIZE) as pilot:
        await settle(pilot, app)
        g = app.query_one(GridTable)
        widths = [c.get_render_width(g) for c in g.ordered_columns]
        assert g.size.width < sum(widths) <= 3 * g.size.width
        await _check_look(pilot, app, g, "start")
        await pilot.press("end")
        await settle(pilot, app)
        assert g.column_window()[2] > 0
        await _check_look(pilot, app, g, "end")
        await pilot.press("down", "home")
        await settle(pilot, app)
        await _check_look(pilot, app, g, "home")


def _frame(g: GridTable) -> list:
    """The grid's lines as a real frame draws them: through every cache but the per-refresh one."""
    g._styles_cache.clear()
    w, h = g.size
    return [[(s.text, s.style) for s in strip] for strip in g.render_lines(Region(0, 0, w, h))]


async def test_render_after_remeasure(wide300_path):
    """DataTable re-measures column and row-label widths on idle without invalidating its caches:
    a frame drawn before that must not leave lines at the old widths behind. (The grid now
    settles its widths when it loads or re-formats, but a frame drawn at once must still
    match one drawn after idle.)"""
    app = PqxApp(wide300_path)
    async with app.run_test(size=SIZE) as pilot:
        await settle(pilot, app)
        g = app.query_one(GridTable)
        g.move_cursor(column=1)  # a float column: < and > change its digits
        await settle(pilot, app)
        reloads = {"page": lambda: app._apply_page(app.page, g.abs_row, g.cursor_column),
                   "raw": app.action_toggle_raw, "digits": lambda: app.action_step_digits(1)}
        for what, reload in reloads.items():
            reload()
            first = _frame(g)  # drawn at once, before any idle
            await settle(pilot, app)
            assert not g._require_update_dimensions, what
            assert _frame(g) == _reference(g) == first, what


async def test_render_after_width_change_behind_the_caches(wide300_path):
    """A column or row-label width that changes without an _update_count bump (as DataTable's
    idle re-measuring does) must not leave lines cached at the old widths."""
    app = PqxApp(wide300_path)
    async with app.run_test(size=SIZE) as pilot:
        await settle(pilot, app)
        g = app.query_one(GridTable)
        _frame(g)  # warm every cache
        count = g._update_count
        g.ordered_columns[1].content_width += 3
        assert g._update_count == count
        assert _frame(g) == _reference(g), "column"
        _frame(g)
        g._label_column.content_width += 2
        assert _frame(g) == _reference(g), "row labels"


async def test_render_work_scales_with_visible_columns(wide300_path):
    """One → or ↓ must render (in cells) a screenful at most, not every column of every line."""
    app = PqxApp(wide300_path)
    async with app.run_test(size=SIZE) as pilot:
        await settle(pilot, app)
        g = app.query_one(GridTable)
        assert len(g.columns) == NCOLS
        calls = []
        orig = g._render_cell

        def counting(row_index, column_index, *a, **kw):
            calls.append((row_index, column_index))
            return orig(row_index, column_index, *a, **kw)

        g._render_cell = counting
        first, last, _, _ = g.column_window()
        in_view = last - first + 4  # plus a partly visible column on each side and the row labels
        lines = g.size.height
        assert in_view < NCOLS // 4  # the fixture itself: most columns must be off screen

        g.move_cursor(column=last)
        await settle(pilot, app)
        calls.clear()
        await pilot.press("right")  # scrolls one column into view
        await settle(pilot, app)
        assert g.column_window()[0] > first
        assert calls and len(calls) <= in_view * lines
        assert {c for _, c in calls} <= {-1} | set(range(first, last + 4))  # -1: the row labels

        calls.clear()
        await pilot.press("down")
        await settle(pilot, app)
        assert calls and len(calls) <= 2 * in_view  # the two rows the cursor touched
        assert {r for r, _ in calls} <= {g.cursor_row - 1, g.cursor_row}


@pytest.fixture(scope="module")
def styled_path(tmp_path_factory):
    """Cells of every look: dim NULL/NaN/∞, bold ✓ and dim · booleans, left/right/center
    justification, empty and wide (CJK) strings, long text cut by its column, timestamps, blobs."""
    n = 200
    rng = np.random.default_rng(2)
    x = rng.normal(size=n)
    x[::7] = np.nan
    x[3] = np.inf
    strs = [None if i % 5 == 0 else ("" if i % 5 == 1 else ("日本語" if i % 5 == 2 else "w" * (i % 30)))
            for i in range(n)]
    tbl = pa.table({
        "id": np.arange(n),
        "x": pa.array([None if i % 11 == 0 else v for i, v in enumerate(x)]),
        "flag": pa.array([None if i % 4 == 0 else i % 3 == 0 for i in range(n)]),
        "s": pa.array(strs),
        "day": pa.array(np.datetime64("2026-01-01") + (np.arange(n) * 3_600_123).astype("timedelta64[ms]")),
        "blob": pa.array([bytes([i % 256]) * (i % 20) for i in range(n)], type=pa.binary()),
        "tags": pa.array([["a", "b"][: i % 3] for i in range(n)], type=pa.list_(pa.string())),
        **{f"c{i}": rng.normal(scale=10.0 ** i, size=n) for i in range(12)},
    })
    p = tmp_path_factory.mktemp("data") / "styled.parquet"
    pq.write_table(tbl, p)
    return str(p)


async def test_render_matches_datatable_styled_cells(styled_path):
    """GridTable._render_cell's fast path for one-line Text cells must draw exactly what
    DataTable's Rich rendering does, for every cell look, cursor, hover and pinned style."""
    app = PqxApp(styled_path)
    async with app.run_test(size=(120, 30)) as pilot:
        await settle(pilot, app)
        g = app.query_one(GridTable)
        await _check_look(pilot, app, g, "start")
        assert g._fast_cell_styles, "the fast path wasn't used"
        for col in range(1, 7):
            g.move_cursor(row=col + 3, column=col)
            await settle(pilot, app)
            await _check_look(pilot, app, g, f"cursor on column {col}")
        g._set_hover_cursor(True)
        g.hover_coordinate = Coordinate(5, 2)
        await _check_look(pilot, app, g, "hover")
        g._set_hover_cursor(False)
        g.move_cursor(column=3)
        await pilot.press("p")  # pin id..s: fixed-cell styles, and the cursor on a pinned cell
        await settle(pilot, app)
        await _check_look(pilot, app, g, "pinned")
        await pilot.press("f")
        await settle(pilot, app)
        await _check_look(pilot, app, g, "raw")
        await pilot.press("end", "pagedown")
        await settle(pilot, app)
        await _check_look(pilot, app, g, "pinned, scrolled")
        g.blur()
        await _check_look(pilot, app, g, "blurred")


@pytest.mark.parametrize("size,keys", [(SIZE, ["end", "p"]), ((80, 24), ["end", "left", "left", "p"])])
async def test_pin_while_scrolled_right(wide300_path, size, keys):
    """Pinning far right puts the scrollable part's left edge past the table's end: no crash,
    and still drawn like DataTable."""
    app = PqxApp(wide300_path)
    async with app.run_test(size=size) as pilot:
        await settle(pilot, app)
        g = app.query_one(GridTable)
        for k in keys:
            await pilot.press(k)
            await settle(pilot, app)
        assert g.fixed_columns > 200
        await _check_look(pilot, app, g, "pinned far right")
