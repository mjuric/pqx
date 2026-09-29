"""Headless UI tests driven through Textual's pilot."""
import os

import pyarrow.parquet as pq
from textual.worker import WorkerState
from textual.widgets import DataTable, Input, OptionList, Static, TabbedContent

from pqx.app import GridTable, PlotControls, PqxApp
from pqx.screens import ColumnPicker, ExportScreen, GotoScreen, HelpScreen

SIZE = (150, 42)


async def settle(pilot, app, timeout=10.0):
    """Wait until no background query is running."""
    await pilot.pause(0.05)
    t = 0.0
    while app._busy or any(w.state in (WorkerState.PENDING, WorkerState.RUNNING) for w in app.workers):
        await pilot.pause(0.05)
        t += 0.05
        if t > timeout:
            raise TimeoutError(f"still busy: {app._busy}")
    await pilot.pause(0.05)


def plain(widget) -> str:
    r = widget.render()
    return getattr(r, "plain", str(r))


async def test_startup_and_navigation(demo_path):
    app = PqxApp(demo_path)
    async with app.run_test(size=SIZE) as pilot:
        await settle(pilot, app)
        g = app.query_one(GridTable)
        assert app.focused is g
        assert g.row_count == g.window and g.offset == 0
        assert app.total == 20_000

        await pilot.press("ctrl+end")
        await settle(pilot, app)
        assert g.abs_row == 19_999 and g.offset + g.row_count == 20_000
        await pilot.press("down")  # at the very end: stays put
        await settle(pilot, app)
        assert g.abs_row == 19_999

        await pilot.press("ctrl+home")
        await settle(pilot, app)
        assert g.abs_row == 0 and g.offset == 0

        g.move_cursor(row=g.row_count - 1)
        await pilot.press("down")  # crosses the window edge seamlessly
        await settle(pilot, app)
        assert g.abs_row == g.window and g.offset > 0
        # the cell under the cursor is the right file row
        assert str(g.get_row_at(g.cursor_row)[0]) == str(170_000_000_000_000_000 + g.window)

        await pilot.press("g")
        assert isinstance(app.screen, GotoScreen)
        await pilot.press(*"12345", "enter")
        await settle(pilot, app)
        assert g.abs_row == 12_345


async def test_filter_sql_and_errors(demo_path):
    truth = pq.read_table(demo_path).to_pandas()
    app = PqxApp(demo_path)
    async with app.run_test(size=SIZE) as pilot:
        await settle(pilot, app)
        g = app.query_one(GridTable)
        await pilot.press("slash")
        inp = app.query_one("#filter", Input)
        assert app.focused is inp
        await pilot.press(*"mag < 19 and band = 'g'", "enter")  # 'q' etc. must not trigger actions
        await settle(pilot, app)
        exp = int(((truth.mag < 19) & (truth.band == "g")).sum())
        assert app.total == exp
        assert app.focused is g  # back to the grid after a successful filter
        status = plain(app.query_one("#status", Static))
        assert status.startswith("✓") and "% of 20k" in status

        # sort cycle on the cursor column (mag)
        g.move_cursor(column=app.cols_shown.index("mag"))
        await pilot.press("s")
        await settle(pilot, app)
        assert app.view.order_by == [("mag", False)]
        mags = [app.page.rows[i][app.cols_shown.index("mag")] for i in range(min(5, len(app.page.rows)))]
        assert mags == sorted(mags)
        await pilot.press("s")
        await settle(pilot, app)
        assert app.view.order_by == [("mag", True)]
        await pilot.press("s")
        await settle(pilot, app)
        assert app.view.order_by == []

        # broken filter: error shown, previous view kept
        inp.value = "mag <"
        await pilot.press("slash", "enter")
        await settle(pilot, app)
        assert app.query_one("#filterbox").has_class("error") and app._last_error
        assert plain(app.query_one("#status", Static)).startswith("✗ Query failed")
        assert app.total == exp

        # full SQL query (focus stayed in the input after the error, so "/" would be text)
        assert app.focused is inp
        inp.value = "select band, count(*) as n from t group by band order by n desc"
        await pilot.press("enter")
        await settle(pilot, app)
        assert app.total == 6 and app.cols_shown == ["band", "n"]
        assert app.query_one("#filter-mode").has_class("sql")

        # clear filter: back to the whole file
        await pilot.press("escape", "x")
        await settle(pilot, app)
        assert app.view.is_trivial and app.total == 20_000
        assert app.cols_shown == app.ds.column_names


async def test_quit_key_is_text_in_filter(demo_path):
    app = PqxApp(demo_path)
    async with app.run_test(size=SIZE) as pilot:
        await settle(pilot, app)
        await pilot.press("slash", "q", "e", "m", "x", "1", "?")
        await pilot.pause(0.2)
        assert app.is_running
        assert app.query_one("#filter", Input).value == "qemx1?"
        assert isinstance(app.screen, type(app.screen)) and not isinstance(app.screen, HelpScreen)


async def test_value_filter_and_keep_position(demo_path):
    app = PqxApp(demo_path)
    async with app.run_test(size=SIZE) as pilot:
        await settle(pilot, app)
        g = app.query_one(GridTable)
        g.move_cursor(row=3, column=app.cols_shown.index("band"))
        band = app.page.rows[3][app.cols_shown.index("band")]
        await pilot.press("equals_sign")
        await settle(pilot, app)
        assert app.view.where == f"band = '{band}'"
        assert all(r[app.cols_shown.index("band")] == band for r in app.page.rows)
        g.move_cursor(row=10)
        file_row = app.page.row_numbers[10]
        await pilot.press("x")  # clearing keeps you on the same file row
        await settle(pilot, app)
        assert g.abs_row == file_row


async def test_columns_detail_raw(demo_path):
    app = PqxApp(demo_path)
    async with app.run_test(size=SIZE) as pilot:
        await settle(pilot, app)
        g = app.query_one(GridTable)
        n = len(app.cols_shown)
        await pilot.press("minus")
        await settle(pilot, app)
        assert len(app.cols_shown) == n - 1 and "diaSourceId" not in app.cols_shown
        assert len(g.columns) == n - 1

        await pilot.press("c")
        assert isinstance(app.screen, ColumnPicker)
        await pilot.press("ctrl+a")
        await pilot.click("#apply")
        await settle(pilot, app)
        assert app.cols_shown == app.ds.column_names

        await pilot.press("d")
        await pilot.pause(0.1)
        assert app.query_one("#detail").display
        await pilot.press("f")
        await pilot.pause(0.1)
        assert app.raw
        await pilot.press("question_mark")
        assert isinstance(app.screen, HelpScreen)
        await pilot.press("escape")
        assert not isinstance(app.screen, HelpScreen)


async def test_tabs_stats_plots(demo_path):
    app = PqxApp(demo_path)
    async with app.run_test(size=SIZE) as pilot:
        await settle(pilot, app)
        tc = app.query_one(TabbedContent)
        for key, tab in [("2", "tab-schema"), ("3", "tab-stats"), ("4", "tab-plot"), ("5", "tab-meta"),
                         ("3", "tab-stats"), ("5", "tab-meta"), ("1", "tab-data")]:
            await pilot.press(key)
            await settle(pilot, app)
            assert tc.active == tab, key

        # schema table lists every column; Enter jumps to its stats
        await pilot.press("2")
        st = app.query_one("#schema-table", DataTable)
        assert st.row_count == len(app.ds.columns)
        st.move_cursor(row=app.ds.column_names.index("mag"))
        await pilot.press("enter")
        await settle(pilot, app)
        assert tc.active == "tab-stats" and app._stats_shown == "mag"

        ol = app.query_one("#stats-cols", OptionList)
        ol.highlighted = ol.get_option_index("band")
        await pilot.pause(0.3)
        await settle(pilot, app)
        assert app._stats_shown == "band"

        await pilot.press("4")
        await settle(pilot, app)
        await pilot.pause(0.3)
        await settle(pilot, app)
        assert "Binned ra × dec" in plain(app.query_one("#plot-status", Static))
        pc = app.query_one(PlotControls)
        assert app.focused is pc and pc.value("colour") == "magma"
        await pilot.press("left")  # mode field: sky -> xy
        await pilot.pause(0.4)
        await settle(pilot, app)
        assert pc.value("mode") == "xy"
        assert plain(app.query_one("#plot-status", Static)).startswith("✓ Binned")
        await pilot.press("tab", "tab", "tab", "right")  # colour field: magma -> viridis
        await pilot.pause(0.4)
        await settle(pilot, app)
        assert pc.value("colour") == "viridis"

        await pilot.press("m")  # sampling toggle re-runs the plot
        await settle(pilot, app)
        assert app.sampling
        assert plain(app.query_one("#plot-status", Static)).startswith("! Binned")


async def test_export_dialog(demo_path, tmp_path, monkeypatch):
    monkeypatch.chdir(tmp_path)
    app = PqxApp(demo_path, where="band = 'y'")
    async with app.run_test(size=SIZE) as pilot:
        await settle(pilot, app)
        await pilot.press("e")
        assert isinstance(app.screen, ExportScreen)
        path = app.screen.query_one("#export-path", Input)
        path.value = str(tmp_path / "y.parquet")
        await pilot.click("#export")
        await settle(pilot, app)
        assert os.path.exists(tmp_path / "y.parquet")
        t = pq.read_table(tmp_path / "y.parquet")
        assert set(t.column("band").to_pylist()) == {"y"} and t.num_rows == app.total


async def test_error_hint_and_look(demo_path):
    app = PqxApp(demo_path, accent="magenta", dim="bright-black")
    async with app.run_test(size=SIZE) as pilot:
        await settle(pilot, app)
        assert app.theme == "pqx-magenta" and app.current_theme.ansi
        assert app.dim == "bright_black"
        app.query_one("#filter", Input).value = "magg < 21"
        await pilot.press("slash", "enter")
        await settle(pilot, app)
        status = plain(app.query_one("#status", Static))
        assert 'unknown column "magg"' in status and 'did you mean "mag"?' in status
        keys = plain(app.query_one("#keys", Static))
        assert keys.startswith("enter apply")  # the filter has focus: its keys are shown


async def test_odd_file_opens(odd_path):
    app = PqxApp(odd_path)
    async with app.run_test(size=SIZE) as pilot:
        await settle(pilot, app)
        assert app.query_one(GridTable).row_count == 1000
        await pilot.press("ctrl+end")
        await settle(pilot, app)
        assert app.query_one(GridTable).abs_row == 999
        for k in "2345":
            await pilot.press(k)
            await settle(pilot, app)
        assert not app._last_error
