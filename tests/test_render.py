"""GridTable renders only the columns in view: guards for speed, Textual drift and look."""
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

        g.move_cursor(column=last)
        await settle(pilot, app)
        calls.clear()
        await pilot.press("right")  # scrolls one column into view
        await settle(pilot, app)
        assert g.column_window()[0] > first
        assert calls and len(calls) <= in_view * lines < NCOLS * 2
        assert {c for _, c in calls} <= {-1} | set(range(first, last + 4))  # -1: the row labels

        calls.clear()
        await pilot.press("down")
        await settle(pilot, app)
        assert calls and len(calls) <= 2 * in_view  # the two rows the cursor touched
        assert {r for r, _ in calls} <= {g.cursor_row - 1, g.cursor_row}
