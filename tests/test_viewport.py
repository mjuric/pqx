"""A new view of the same columns keeps the viewport: the leftmost column, and the screen row
of the record the cursor stays on."""
import pyarrow.parquet as pq
import pytest

from pqx.app import GridTable, PqxApp
from pqx.widgets import DetailList

from test_app import settle
from test_detail_keys import hold_find_row, record, wait_until

SIZE = (100, 40)  # narrow: the demo's 16 columns need scrolling (27 rows on screen)


@pytest.fixture(scope="module")
def demo(demo_path):
    return pq.read_table(demo_path).to_pandas()


def leftmost(app) -> str:
    """The leftmost wholly visible scrollable column."""
    first, last, _, _ = app.query_one(GridTable).column_window()
    assert first <= last
    return app.cols_shown[first]


async def place(pilot, app, file_row: int, column: str, left: str, screen_row: int) -> GridTable:
    """Cursor on (``file_row``, ``column``) of the view, ``left`` the leftmost column and the
    cursor ``screen_row`` rows down the screen."""
    g = app.query_one(GridTable)
    await pilot.press("g", *str(file_row), "enter")
    await settle(pilot, app)
    g.move_cursor(column=app.cols_shown.index(column), animate=False)
    g.scroll_to(x=g.scroll_x_for(app.cols_shown.index(left)), animate=False, immediate=True)
    g.show_cursor_at(screen_row)
    await pilot.pause(0.05)
    assert leftmost(app) == left and g.cursor_cell_in_view()
    assert g.screen_row() == screen_row and record(app) == file_row
    return g


def position(mask, file_row: int) -> int:
    """``file_row``'s position in the view of the rows ``mask`` selects."""
    return int(mask[:file_row].sum())


async def test_equals_keeps_the_leftmost_column_and_screen_row(demo_path, demo):
    app = PqxApp(demo_path)
    async with app.run_test(size=SIZE) as pilot:
        await settle(pilot, app)
        g = await place(pilot, app, 15_000, "detector", "psfFlux", 15)
        det = int(demo.detector[15_000])
        await pilot.press("equals_sign")
        await settle(pilot, app)
        assert app.view.where == f"detector = {det}"
        assert g.abs_row == position(demo.detector == det, 15_000) >= 15
        assert record(app) == 15_000 and app.cols_shown[g.cursor_column] == "detector"
        assert leftmost(app) == "psfFlux" and g.screen_row() == 15

        await pilot.press("x")  # back to the plain view: still there, on the same record
        await settle(pilot, app)
        assert app.view.is_trivial and g.abs_row == 15_000
        assert leftmost(app) == "psfFlux" and g.screen_row() == 15


async def test_equals_from_the_pane_keeps_the_viewport(demo_path, demo):
    app = PqxApp(demo_path)
    async with app.run_test(size=SIZE) as pilot:
        await settle(pilot, app)
        g = await place(pilot, app, 15_000, "detector", "mag", 15)
        await pilot.press("d", "tab")
        await pilot.pause(0.1)
        lst = app.query_one(DetailList)
        assert app.focused is lst and lst.selected == "detector"
        # (the pane narrows the grid)
        before = app.cols_shown[app.cols_shown.index("detector") - 1]  # (fits beside the pane at any pane width)
        g.scroll_to(x=g.scroll_x_for(app.cols_shown.index(before)), animate=False, immediate=True)
        await pilot.pause(0.05)
        left, row = leftmost(app), g.screen_row()
        assert left == before and g.cursor_cell_in_view() and row == 15
        await pilot.press("equals_sign")
        await settle(pilot, app)
        assert app.view.where == f"detector = {int(demo.detector[15_000])}"
        assert record(app) == 15_000 and app.focused is lst and lst.selected == "detector"
        assert leftmost(app) == left and g.screen_row() == row


async def test_a_record_near_the_top_of_the_view_shows_at_its_position(demo_path, demo):
    app = PqxApp(demo_path)
    async with app.run_test(size=SIZE) as pilot:
        await settle(pilot, app)
        det = int(demo.detector[15_000])
        fr = int(demo.index[demo.detector == det][5])  # the 6th record of its view
        g = await place(pilot, app, fr, "detector", "psfFlux", 15)
        await pilot.press("equals_sign")
        await settle(pilot, app)
        assert record(app) == fr and g.abs_row == 5
        assert g.screen_row() == 5 and g.scroll_y == 0 and leftmost(app) == "psfFlux"


async def test_a_record_near_the_end_of_the_view_fills_the_screen(demo_path, demo):
    app = PqxApp(demo_path)
    async with app.run_test(size=SIZE) as pilot:
        await settle(pilot, app)
        band = demo.band[15_000]
        rows = demo.index[demo.band == band]
        assert len(rows) > 2 * app.query_one(GridTable).window  # (a lookup, then the record's page)
        fr = int(rows[-3])  # the third from the end of its view
        g = await place(pilot, app, fr, "band", "ra", 15)
        await pilot.press("equals_sign")
        await settle(pilot, app)
        assert record(app) == fr and g.abs_row == len(rows) - 3 == app.total - 3
        assert g.screen_row() == g.visible_rows() - 3  # not row 15: the screen ends at the view's end
        assert leftmost(app) == "ra"


async def test_a_record_near_the_first_pages_end_is_shown_at_its_screen_row(demo_path, demo):
    """The record is on the view's first page but too near its end to show 15 rows down with
    a screen of rows below it: its page is loaded around it."""
    app = PqxApp(demo_path)
    async with app.run_test(size=SIZE) as pilot:
        await settle(pilot, app)
        band = demo.band[15_000]
        p = app.query_one(GridTable).window - 5
        fr = int(demo.index[demo.band == band][p])
        g = await place(pilot, app, fr, "band", "ra", 15)
        await pilot.press("equals_sign")
        await settle(pilot, app)
        assert record(app) == fr and g.abs_row == p and g.offset > 0
        assert g.screen_row() == 15 and leftmost(app) == "ra"


async def test_sort_and_clear_keep_the_leftmost_column(demo_path):
    app = PqxApp(demo_path)
    async with app.run_test(size=SIZE) as pilot:
        await settle(pilot, app)
        g = await place(pilot, app, 15_000, "detector", "psfFlux", 15)
        await pilot.press("s")
        await settle(pilot, app)
        assert app.view.order_by == [("detector", False)] and g.abs_row == 0
        assert leftmost(app) == "psfFlux" and app.cols_shown[g.cursor_column] == "detector"
        g.move_cursor(row=40, animate=False)
        g.show_cursor_at(12)
        await pilot.pause(0.05)
        fr = record(app)
        await pilot.press("x")  # the plain view, on the same record and screen row
        await settle(pilot, app)
        assert app.view.is_trivial and g.abs_row == fr
        assert leftmost(app) == "psfFlux" and g.screen_row() == 12

        await pilot.press("slash", *"band = 'r'", "enter")  # a typed filter: its top, same columns
        await settle(pilot, app)
        assert app.view.where == "band = 'r'" and g.abs_row == 0 and leftmost(app) == "psfFlux"


async def test_a_query_of_other_columns_starts_at_the_left(demo_path):
    app = PqxApp(demo_path)
    async with app.run_test(size=SIZE) as pilot:
        await settle(pilot, app)
        g = await place(pilot, app, 15_000, "detector", "psfFlux", 15)
        app.apply_filter("select ssObjectId, ra, dec, raErr, decErr, midpointMjdTai, band, psfFlux, "
                         "psfFluxErr, mag, snr, trailLength, isDipole, detector, ingestTime from t")
        await settle(pilot, app)
        assert app.view.sql and app.cols_shown[0] == "ssObjectId"
        # not kept: the view scrolls from the left only as far as the cursor's column needs
        assert app.cols_shown[g.cursor_column] == "detector" and g.column_window()[1] == g.cursor_column
        assert leftmost(app) != "psfFlux"


async def test_paging_keeps_the_leftmost_column(demo_path):
    app = PqxApp(demo_path)
    async with app.run_test(size=SIZE) as pilot:
        await settle(pilot, app)
        g = await place(pilot, app, 100, "mag", "psfFlux", 15)
        assert g.column_window()[1] > g.cursor_column  # (the cursor isn't at the right edge)
        await pilot.press("g", *"15000", "enter")  # another window
        await settle(pilot, app)
        assert g.abs_row == 15_000 and g.offset > 0 and leftmost(app) == "psfFlux"


async def test_a_record_found_later_gets_its_screen_row(demo_path, demo):
    app = PqxApp(demo_path)
    async with app.run_test(size=SIZE) as pilot:
        await settle(pilot, app)
        holds = hold_find_row(app)
        g = await place(pilot, app, 15_000, "band", "ra", 15)
        band = demo.band[15_000]
        await pilot.press("equals_sign")
        await wait_until(pilot, lambda: holds and app.view.where and app.page.offset == 0)
        assert g.abs_row == 0 and leftmost(app) == "ra"  # the first page, while the lookup runs
        holds[0].set()
        await settle(pilot, app)
        assert record(app) == 15_000
        assert g.abs_row == position(demo.band == band, 15_000) > g.window
        assert g.screen_row() == 15 and leftmost(app) == "ra"


async def pinned_place(pilot, app) -> GridTable:
    """Three columns pinned, the cursor on a pinned one (ssObjectId), mag the leftmost scrollable."""
    g = app.query_one(GridTable)
    await pilot.press("g", *"15004", "enter")
    await settle(pilot, app)
    g.move_cursor(column=app.cols_shown.index("ra"), animate=False)
    await pilot.press("p")
    await pilot.pause(0.05)
    assert g.fixed_columns == 3
    g.move_cursor(column=app.cols_shown.index("ssObjectId"), animate=False)
    await pilot.pause(0.05)
    g.scroll_to(x=g.scroll_x_for(app.cols_shown.index("mag")), animate=False, immediate=True)
    await pilot.pause(0.05)
    assert leftmost(app) == "mag" and g.scroll_x > 0
    return g


async def test_a_cursor_in_a_pinned_column_keeps_the_leftmost_column(demo_path):
    app = PqxApp(demo_path)
    async with app.run_test(size=SIZE) as pilot:
        await settle(pilot, app)
        g = await pinned_place(pilot, app)
        for keys in (["down"], ["pagedown"], ["g", *"2000", "enter"], ["up"]):  # moves, and a page load
            await pilot.press(*keys)
            await settle(pilot, app)
            assert leftmost(app) == "mag", keys
            assert app.cols_shown[g.cursor_column] == "ssObjectId"
        fr = record(app)
        await pilot.press("equals_sign")
        await settle(pilot, app)
        assert app.view.where.startswith("ssObjectId") and record(app) == fr and leftmost(app) == "mag"
        await pilot.press("x")
        await settle(pilot, app)
        assert app.view.is_trivial and g.abs_row == fr and leftmost(app) == "mag"
        await pilot.press("s")
        await settle(pilot, app)
        assert app.view.order_by == [("ssObjectId", False)] and leftmost(app) == "mag"


async def test_a_failed_page_forgets_the_screen_row(demo_path, demo):
    app = PqxApp(demo_path)
    async with app.run_test(size=SIZE) as pilot:
        await settle(pilot, app)
        g = await place(pilot, app, 15_000, "detector", "psfFlux", 15)
        await pilot.press("equals_sign")
        await settle(pilot, app)
        assert g.screen_row() == 15
        real = app.ds.fetch

        def fetch(view, *a, **kw):
            if view.is_trivial:
                raise RuntimeError("no page")
            return real(view, *a, **kw)
        app.ds.fetch = fetch
        await pilot.press("x")  # the plain view's page fails
        await settle(pilot, app)
        assert app._anchor_row is None and app._anchor_left is None
        app.ds.fetch = real
        await pilot.press("g", *"5000", "enter")
        await settle(pilot, app)
        assert g.abs_row == 5000 and g.screen_row() == g.visible_rows() - 1  # scrolled to, not to row 15


async def test_an_empty_result_keeps_the_leftmost_column(demo_path):
    app = PqxApp(demo_path)
    async with app.run_test(size=SIZE) as pilot:
        await settle(pilot, app)
        g = await place(pilot, app, 15_000, "detector", "psfFlux", 15)
        await pilot.press("slash", *"band = 'nope'", "enter")
        await settle(pilot, app)
        assert app.total == 0 and g.row_count == 0 and g.scroll_x > 0  # (as far as the header goes)
        await pilot.press("slash", *["backspace"] * 20, *"band = 'r'", "enter")
        await settle(pilot, app)
        assert app.view.where == "band = 'r'" and g.row_count and leftmost(app) == "psfFlux"


async def test_hiding_a_column_keeps_the_leftmost_column(demo_path):
    app = PqxApp(demo_path)
    async with app.run_test(size=SIZE) as pilot:
        await settle(pilot, app)
        g = await place(pilot, app, 15_000, "mag", "psfFlux", 15)
        await pilot.press("minus")  # mag: psfFlux stays leftmost
        await settle(pilot, app)
        assert "mag" not in app.cols_shown and leftmost(app) == "psfFlux"
        g.move_cursor(column=app.cols_shown.index("psfFlux"), animate=False)
        await pilot.pause(0.05)
        await pilot.press("minus")  # the leftmost one: the next one right of it is
        await settle(pilot, app)
        assert "psfFlux" not in app.cols_shown and leftmost(app) == "psfFluxErr"
