"""The grid's cell keys (=, y, i, F, < >) in the Details pane, and = keeping the cursor's record."""
import threading

import pyarrow.parquet as pq
import pytest
from textual.widgets import Input, TabbedContent

from pqx.app import GridTable, PqxApp
from pqx.cells import MISSING
from pqx.data import View
from pqx.screens import FormatScreen
from pqx.widgets import DetailList

from test_app import SIZE, settle
from test_lazycols import lazy, lazy_path, truth as lazy_truth  # noqa: F401  (fixtures)


@pytest.fixture(scope="module")
def demo(demo_path):
    return pq.read_table(demo_path).to_pandas()


async def into_pane(pilot, app, row: int, column: str):
    """Grid cursor on (``row``, ``column``), the pane open and focused on that field."""
    g = app.query_one(GridTable)
    await pilot.press("g", *str(row), "enter")
    await settle(pilot, app)
    g.move_cursor(column=app.cols_shown.index(column))
    await pilot.pause(0.05)
    if not app.query_one("#detail").display:
        await pilot.press("d")
    await pilot.press("tab")
    await pilot.pause(0.1)
    lst = app.query_one(DetailList)
    assert app.focused is lst and lst.selected == column
    return g, lst


def record(app):
    """The file row of the record under the grid cursor."""
    g = app.query_one(GridTable)
    return app.page.row_numbers[g.cursor_row]


async def test_equals_from_pane_narrows_in_two_keystrokes(demo_path, demo):
    app = PqxApp(demo_path)
    async with app.run_test(size=SIZE) as pilot:
        await settle(pilot, app)
        g, lst = await into_pane(pilot, app, 15_000, "band")  # (well past the filtered view's first page)
        band, det = demo.band[15_000], int(demo.detector[15_000])
        await pilot.press("equals_sign")
        await settle(pilot, app)
        assert app.view.where == f"band = '{band}'"
        assert app.focused is lst and lst.selected == "band"  # focus stays, same field
        assert record(app) == 15_000 and app.total == int((demo.band == band).sum())
        assert g.abs_row == int((demo.band[:15_000] == band).sum())  # its place in the filtered view
        assert "file row 15,000" in str(app.query_one("#detail").border_title)

        while lst.selected != "detector":
            await pilot.press("down")
        await pilot.pause(0.05)
        await pilot.press("equals_sign")
        await settle(pilot, app)
        assert app.view.where == f"band = '{band}' and detector = {det}"
        assert app.focused is lst and lst.selected == "detector"
        assert record(app) == 15_000
        assert app.total == int(((demo.band == band) & (demo.detector == det)).sum())
        assert "match" in str(app.query_one("#keys").render())

        await pilot.press("escape")  # back to the grid, on that field
        await pilot.pause(0.05)
        assert app.focused is g and app.cols_shown[g.cursor_column] == "detector"


async def test_equals_from_pane_on_null_and_sorted(demo_path, demo):
    app = PqxApp(demo_path)
    async with app.run_test(size=SIZE) as pilot:
        await settle(pilot, app)
        g = app.query_one(GridTable)
        g.move_cursor(column=app.cols_shown.index("mag"))
        await pilot.press("s")  # sorted by mag
        await settle(pilot, app)
        # a NULL ssObjectId well down the sorted view
        rows = [i for i, r in enumerate(app.page.rows) if r[app.cols_shown.index("ssObjectId")] is None]
        pos = [i for i in rows if i > 600][0]
        fr = app.page.row_numbers[pos]
        g, lst = await into_pane(pilot, app, pos, "ssObjectId")
        assert record(app) == fr
        await pilot.press("equals_sign")
        await settle(pilot, app)
        assert app.view.where == "ssObjectId IS NULL" and app.view.order_by == [("mag", False)]
        assert app.focused is lst and lst.selected == "ssObjectId"
        assert record(app) == fr  # found by position in the filtered, sorted view
        exp = demo[demo.ssObjectId.isna()].sort_values("mag", kind="stable")
        assert g.abs_row == list(exp.index).index(fr)


async def test_grid_equals_keeps_the_record(demo_path, demo):
    app = PqxApp(demo_path)
    async with app.run_test(size=SIZE) as pilot:
        await settle(pilot, app)
        g = app.query_one(GridTable)
        for fr, col in ((12_345, "band"), (40, "detector")):  # the lookup, then the first page
            await pilot.press("g", *str(fr), "enter")
            await settle(pilot, app)
            g.move_cursor(column=app.cols_shown.index(col))
            before = record(app)
            await pilot.press("equals_sign")
            await settle(pilot, app)
            assert app.focused is g and record(app) == before and app.cols_shown[g.cursor_column] == col
            await pilot.press("x")
            await settle(pilot, app)
            assert g.abs_row == before


async def test_lookup_does_not_yank_a_cursor_moved_meanwhile(demo_path):
    app = PqxApp(demo_path)
    async with app.run_test(size=SIZE) as pilot:
        await settle(pilot, app)
        g = app.query_one(GridTable)
        real = app.ds.find_row
        release, started = threading.Event(), threading.Event()

        def find_row(view, file_row):
            started.set()
            release.wait(10)
            return real(view, file_row)
        app.ds.find_row = find_row
        await pilot.press("g", *"15000", "enter")
        await settle(pilot, app)
        g.move_cursor(column=app.cols_shown.index("band"))
        await pilot.press("equals_sign")
        for _ in range(200):  # the first page shows (at the top) while the lookup runs
            if started.is_set() and app.page is not None and app.view.where and app.page.offset == 0:
                break
            await pilot.pause(0.02)
        assert started.is_set() and g.abs_row == 0 and app._busy
        await pilot.press("down", "down")
        release.set()
        await settle(pilot, app)
        assert g.abs_row == 2


async def test_lookup_cancelled_by_esc(demo_path):
    app = PqxApp(demo_path)
    async with app.run_test(size=SIZE) as pilot:
        await settle(pilot, app)
        g = app.query_one(GridTable)
        calls = []

        def find_row(view, file_row):  # a lookup that runs until interrupted
            calls.append(file_row)
            app.ds.cursor().execute("SELECT count(*) FROM range(1000000000000) a, range(1000) b").fetchone()
            return 5
        app.ds.find_row = find_row
        await pilot.press("g", *"15000", "enter")
        await settle(pilot, app)
        g.move_cursor(column=app.cols_shown.index("band"))
        await pilot.press("equals_sign")
        for _ in range(200):
            if calls and "locate" in app._busy:
                break
            await pilot.pause(0.02)
        assert "locate" in app._busy and calls == [15_000]
        await pilot.press("escape")
        await settle(pilot, app)
        assert g.abs_row == 0 and not app._busy and app.view.where


async def test_equals_without_row_ids_goes_to_the_top(odd_path):
    app = PqxApp(odd_path)
    async with app.run_test(size=SIZE) as pilot:
        await settle(pilot, app)
        g, lst = await into_pane(pilot, app, 600, "weird name")
        await pilot.press("equals_sign")
        await settle(pilot, app)
        assert not app.ds.has_row_ids(app.view)
        assert app.view.where.startswith('"weird name" = ') and g.abs_row == 0
        assert app.focused is lst and lst.selected == "weird name"


async def test_plain_view_keeps_the_record_after_clearing_from_pane(demo_path):
    app = PqxApp(demo_path, where="band = 'r'")
    async with app.run_test(size=SIZE) as pilot:
        await settle(pilot, app)
        g, lst = await into_pane(pilot, app, 700, "detector")
        fr = record(app)
        await pilot.press("equals_sign")
        await settle(pilot, app)
        assert record(app) == fr and app.focused is lst
        await pilot.press("x")  # (x in the pane clears the filter app-wide)
        await settle(pilot, app)
        assert app.view == View() or app.view.is_trivial
        assert g.abs_row == fr


async def test_equals_from_pane_on_unloaded_value(lazy_path, lazy):  # noqa: F811
    app = PqxApp(lazy_path)
    async with app.run_test(size=SIZE) as pilot:
        await settle(pilot, app)
        g = app.query_one(GridTable)
        lst = app.query_one(DetailList)
        real = app.ds.fetch_columns
        release = threading.Event()
        blocked = []

        def fetch_columns(rows, columns):
            if not blocked:  # the pane's own fetch hangs; the action's goes through
                blocked.append(columns)
                release.wait(10)
            return real(rows, columns)
        app.ds.fetch_columns = fetch_columns
        g.move_cursor(row=5)
        await pilot.press("d", "tab", "end")
        await pilot.pause(0.3)
        name = lst.selected
        assert name == app.cols_shown[-1] and app._cursor_value() == (name, MISSING)
        await pilot.press("equals_sign")
        for _ in range(200):
            if app.view.where:
                break
            await pilot.pause(0.02)
        release.set()
        await settle(pilot, app)
        assert app.view.where == f"{name} = {lazy_truth(name, 5)}" and app.total == 1
        assert app.focused is lst and lst.selected == name and record(app) == 5


async def test_copy_stats_format_from_pane(demo_path, config_home):
    from pqx import config

    app = PqxApp(demo_path)
    async with app.run_test(size=SIZE) as pilot:
        await settle(pilot, app)
        g, lst = await into_pane(pilot, app, 3, "ra")
        ra = app.page.rows[g.cursor_row][app.cols_shown.index("ra")]

        await pilot.press("y")
        await pilot.pause(0.05)
        assert app.clipboard == repr(ra)  # the full value
        assert app.focused is lst

        await pilot.press("less_than_sign")
        await pilot.pause(0.05)
        assert config.load_formats() == {"ra": 5}
        assert str(g.get_cell_at(g.cursor_coordinate)) == f"{ra:.5f}"
        note = str(list(app._notifications)[-1].message)
        assert note.startswith("✓ ra:") and note.endswith("(grid)")
        await pilot.press("greater_than_sign", "greater_than_sign")
        await pilot.pause(0.05)
        assert config.load_formats() == {"ra": 7}
        assert app.focused is lst

        await pilot.press("F")
        await pilot.pause(0.05)
        assert isinstance(app.screen, FormatScreen)
        app.screen.query_one(Input).value = ".2e"
        await pilot.press("enter")
        await pilot.pause(0.1)
        assert str(g.get_cell_at(g.cursor_coordinate)) == f"{ra:.2e}"
        assert app.focused is lst and lst.selected == "ra"  # back in the pane
        await pilot.press("F", "escape")
        await pilot.pause(0.1)
        assert app.focused is lst

        await pilot.press("down")  # dec
        await pilot.pause(0.05)
        await pilot.press("i")
        await settle(pilot, app)
        assert app.query_one(TabbedContent).active == "tab-stats"
        assert app.current_column == "dec" and app._stats_col == "dec"
