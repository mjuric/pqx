"""Headless UI tests driven through Textual's pilot."""
import os

import pyarrow.parquet as pq
import pytest
from rich.styled import Styled
from textual.worker import WorkerState
from textual.widgets import DataTable, Input, OptionList, Static, TabbedContent
from textual.widgets.data_table import ColumnKey

from pqx.app import GridTable, PlotControls, PqxApp
from pqx.screens import ColumnPicker, ExportScreen, FormatScreen, GotoScreen, HelpScreen

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


@pytest.fixture(scope="module")
def wide_path(tmp_path_factory):
    """200 numeric columns: stepping through them with arrows is hopeless."""
    import numpy as np
    import pyarrow as pa

    rng = np.random.default_rng(3)
    cols = {"ra": rng.uniform(0, 360, 2000), "dec": rng.uniform(-60, 20, 2000)}
    for i in range(198):
        cols[f"flux_{i:03d}"] = rng.normal(size=2000)
    p = tmp_path_factory.mktemp("data") / "wide.parquet"
    pq.write_table(pa.table(cols), p)
    return str(p)


async def test_plot_field_dropdown(wide_path):
    from pqx.screens import FieldDropdown

    app = PqxApp(wide_path)
    async with app.run_test(size=SIZE) as pilot:
        await settle(pilot, app)
        await pilot.press("4")
        await settle(pilot, app)
        pc = app.query_one(PlotControls)
        assert (pc.value("x"), pc.value("y")) == ("ra", "dec")

        # keyboard: tab to the lat field, enter opens the list, typing narrows it
        await pilot.press("tab", "tab", "enter")
        await pilot.pause(0.2)
        assert isinstance(app.screen, FieldDropdown)
        await pilot.press(*"flux_17")
        await pilot.pause(0.2)
        lst = app.screen.query_one("#dropdown-list")
        assert lst.option_count == 10  # flux_170 … flux_179
        await pilot.press("down", "enter")
        await settle(pilot, app)
        assert not isinstance(app.screen, FieldDropdown)
        assert pc.value("y") == "flux_171"

        # mouse: clicking the lon field's ‹ value › opens it; esc leaves it unchanged
        key, a, b = next(s for s in pc._spans if s[0] == "x")
        await pilot.click(PlotControls, offset=(a + 2, 0))
        await pilot.pause(0.2)
        assert isinstance(app.screen, FieldDropdown)
        await pilot.press("escape")
        await pilot.pause(0.1)
        assert not isinstance(app.screen, FieldDropdown) and pc.value("x") == "ra"

        # a short list (colour) opens without a filter box and picks by click
        key, a, b = next(s for s in pc._spans if s[0] == "colour")
        await pilot.click(PlotControls, offset=(a + 2, 0))
        await pilot.pause(0.2)
        assert not app.screen.query("#dropdown-filter")
        await pilot.press("down", "enter")
        await pilot.pause(0.4)  # the replot is debounced
        await settle(pilot, app)
        assert pc.value("colour") == "viridis"


async def test_hidden_column_hints(wide_path):
    app = PqxApp(wide_path)
    async with app.run_test(size=SIZE) as pilot:
        await settle(pilot, app)
        await pilot.pause(0.2)
        g = app.query_one(GridTable)
        panel = app.query_one("#data-panel")
        left, right = app.query_one("#more-left"), app.query_one("#more-right")
        first, last, hl, hr = g.column_window()
        assert first == 0 and hl == 0 and hr == 200 - (last + 1)
        sub = str(panel.border_subtitle)
        assert f"columns 1–{last + 1} of 200" in sub and f"{hr}" in sub and "‹" not in sub
        assert str(right.render()) == "›" and str(left.render()).strip() == ""

        await pilot.press("end")  # last column
        await pilot.pause(0.3)
        first, last, hl, hr = g.column_window()
        assert last == 199 and hr == 0 and hl == first
        assert str(left.render()) == "‹" and str(right.render()).strip() == ""
        assert "columns" in str(panel.border_subtitle) and "›" not in str(panel.border_subtitle)

        await pilot.click("#more-left")  # clicking a marker pages that way
        await pilot.pause(0.3)
        assert g.column_window()[3] > 0

    narrow = PqxApp(wide_path)
    async with narrow.run_test(size=SIZE) as pilot:  # everything fits: no hints at all
        await settle(pilot, narrow)
        narrow.cols_shown = ["ra", "dec"]
        narrow._rebuild_columns()
        narrow.load_window(0, 0)
        await settle(pilot, narrow)
        await pilot.pause(0.2)
        assert str(narrow.query_one("#data-panel").border_subtitle) == ""
        assert str(narrow.query_one("#more-right").render()).strip() == ""


async def test_schema_unit_column(demo_path):
    app = PqxApp(demo_path)
    async with app.run_test(size=SIZE) as pilot:
        await settle(pilot, app)
        t = app.query_one("#schema-table", DataTable)
        assert str(t.get_cell("ra", "unit")) == "deg"          # the demo file has units: column shown
        assert str(t.get_cell("ssObjectId", "unit")) == "–"
        n = str(t.get_cell("ssObjectId", "nulls"))
        assert n.replace(",", "").isdigit() and str(t.get_cell("ssObjectId", "null %")).endswith("%")


async def test_click_tab_names_in_border(demo_path):
    from textual.content import Content

    app = PqxApp(demo_path)
    async with app.run_test(size=SIZE) as pilot:
        await settle(pilot, app)
        tc = app.query_one(TabbedContent)
        title = Content.from_markup(str(app.query_one("#data-panel").border_title)).plain
        assert title.startswith("1 Data ─ 2 Schema ─ 3 Stats ─ 4 Plot ─ 5 Meta") and title.endswith("^← ^→")

        def at(tab, part="name"):
            """Panel-relative x of a tab's number or name (the title starts 3 cells in)."""
            a, b = next((a, b) for t, a, b in app._tab_spans if t == tab and (b - a > 2) == (part == "name"))
            return 3 + a

        await pilot.click("#data-panel", offset=(at("tab-schema"), 0))
        await settle(pilot, app)
        assert tc.active == "tab-schema"
        await pilot.click("#schema-panel", offset=(at("tab-plot", "number"), 0))  # the number works too
        await settle(pilot, app)
        assert tc.active == "tab-plot"
        sep = at("tab-schema", "number") - 2  # the '─' between Data and Schema
        await pilot.click("#plot-panel", offset=(sep, 0))
        await pilot.pause(0.2)
        assert tc.active == "tab-plot"

        # hovering a name underlines it; moving off clears it
        def underlined():
            c = Content.from_markup(str(app.query_one("#plot-panel").border_title))
            return [c.plain[sp.start:sp.end] for sp in c.spans if "underline" in str(sp.style)]

        await pilot.hover("#plot-panel", offset=(at("tab-meta"), 0))
        await pilot.pause(0.1)
        assert underlined() == ["Meta"]
        await pilot.hover("#plot-panel", offset=(10, 5))
        await pilot.pause(0.1)
        assert underlined() == []


async def test_schema_all_null_column(odd_path):
    app = PqxApp(odd_path)
    async with app.run_test(size=SIZE) as pilot:
        await settle(pilot, app)
        t = app.query_one("#schema-table", DataTable)

        def cell(col):
            return str(t.get_cell("allnull", col))

        assert (cell("nulls"), cell("null %")) == ("1,000", "100%")   # no "1e+02%"
        assert cell("min") == cell("max") == "–"                      # no min/max for an all-null column
        assert cell("unit") == "–"   # no unit: a dash, not a blank that makes the null count look like one
        for k in "2345":                        # every tab copes with it
            await pilot.press(k)
            await settle(pilot, app)
        assert not app._last_error


async def test_ctrl_keys_tabs_and_clear(demo_path):
    app = PqxApp(demo_path)
    async with app.run_test(size=SIZE) as pilot:
        await settle(pilot, app)
        tc = app.query_one(TabbedContent)
        seen = []
        for _ in range(5):
            await pilot.press("ctrl+right")
            await settle(pilot, app)
            seen.append(tc.active)
        assert seen == ["tab-schema", "tab-stats", "tab-plot", "tab-meta", "tab-data"]  # wraps around
        await pilot.press("ctrl+left")
        await settle(pilot, app)
        assert tc.active == "tab-meta"
        await pilot.press("1")
        await settle(pilot, app)
        assert "x clear filter" in plain(app.query_one("#keys", Static))

        # apply a filter, then clear it with ctrl+x from inside the filter box
        await pilot.press("slash", *"band = 'g'", "enter")
        await settle(pilot, app)
        assert not app.view.is_trivial
        await pilot.press("slash")
        inp = app.query_one("#filter", Input)
        await pilot.press("ctrl+left")  # inside the input: word jump, not a tab switch
        assert tc.active == "tab-data" and app.focused is inp
        await pilot.press("ctrl+x")
        await settle(pilot, app)
        assert app.view.is_trivial and inp.value == "" and app.total == 20_000

        # typed but never applied: ctrl+x just empties the box
        await pilot.press("slash", *"mag <")
        await pilot.press("ctrl+x")
        await pilot.pause(0.1)
        assert inp.value == "" and app.view.is_trivial


async def test_column_formats(demo_path, config_home):
    from pqx import config

    app = PqxApp(demo_path)
    async with app.run_test(size=SIZE) as pilot:
        await settle(pilot, app)
        g = app.query_one(GridTable)
        g.move_cursor(column=app.cols_shown.index("ra"))
        ra = app.page.rows[g.cursor_row][g.cursor_column]

        def cell():
            return str(g.get_cell_at(g.cursor_coordinate))

        assert cell() == f"{ra:.6f}"
        await pilot.press("less_than_sign", "less_than_sign")
        await pilot.pause(0.1)
        assert cell() == f"{ra:.4f}"
        assert config.load_formats() == {"ra": 4}
        assert ".4f" in g.columns[ColumnKey("ra")].label.plain

        await pilot.press("F")
        assert isinstance(app.screen, FormatScreen)
        inp = app.screen.query_one(Input)
        assert inp.value == "4"
        inp.value = ",d"  # doesn't fit a float: rejected in the dialog
        await pilot.press("enter")
        assert isinstance(app.screen, FormatScreen)
        inp.value = ".2e"
        await pilot.press("enter")
        await pilot.pause(0.1)
        assert cell() == f"{ra:.2e}"
        await pilot.press("greater_than_sign")
        await pilot.pause(0.1)
        assert cell() == f"{ra:.3e}" and config.load_formats() == {"ra": ".3e"}

        g.move_cursor(column=app.cols_shown.index("band"))
        await pilot.press("greater_than_sign")  # nothing to step on a string column
        assert config.load_formats() == {"ra": ".3e"}

    # remembered by the next session; --format wins for that session only
    app = PqxApp(demo_path, formats={"dec": 2})
    async with app.run_test(size=SIZE) as pilot:
        await settle(pilot, app)
        g = app.query_one(GridTable)
        row = app.page.rows[0]
        ra_i, dec_i = app.cols_shown.index("ra"), app.cols_shown.index("dec")
        assert str(g.get_cell_at((0, ra_i))) == f"{row[ra_i]:.3e}"
        assert str(g.get_cell_at((0, dec_i))) == f"{row[dec_i]:.2f}"
        g.move_cursor(column=ra_i)
        await pilot.press("F")
        app.screen.query_one(Input).value = ""  # empty: back to automatic
        await pilot.press("enter")
        await pilot.pause(0.1)
        assert str(g.get_cell_at((0, ra_i))) == f"{row[ra_i]:.6f}"
        assert config.load_formats() == {}


async def test_corrupt_formats_file(demo_path, config_home):
    from pqx import config

    p = config.formats_path()
    p.parent.mkdir(parents=True)
    p.write_text("columns: [oops\n")
    app = PqxApp(demo_path)
    async with app.run_test(size=SIZE) as pilot:
        await settle(pilot, app)
        assert app._config_error and app.col_formats == {}
        g = app.query_one(GridTable)
        g.move_cursor(column=app.cols_shown.index("ra"))
        await pilot.press("less_than_sign")
        await pilot.pause(0.1)
        assert app.formatters["ra"].override == 5  # applies for the session...
    assert p.read_text() == "columns: [oops\n"  # ...but never clobbers the file


async def test_format_dialog_markup_and_kinds(demo_path, config_home):
    from pqx import config

    app = PqxApp(demo_path)
    async with app.run_test(size=SIZE) as pilot:
        await settle(pilot, app)
        g = app.query_one(GridTable)
        g.move_cursor(column=app.cols_shown.index("ra"))
        await pilot.press("F")
        inp = app.screen.query_one(Input)
        for bad in ("[/b]", "²", "100000000"):  # markup in the error, unicode digit, absurd digit count
            inp.value = bad
            await pilot.press("enter")
            await pilot.pause(0.05)
            assert isinstance(app.screen, FormatScreen)
        await pilot.press("escape")

        g.move_cursor(column=app.cols_shown.index("detector"))  # int: digits don't apply, specs do
        await pilot.press("F")
        inp = app.screen.query_one(Input)
        inp.value = "4"
        await pilot.press("enter")
        assert isinstance(app.screen, FormatScreen)
        inp.value = ">5"
        await pilot.press("enter")
        await pilot.pause(0.1)
        assert config.load_formats() == {"detector": ">5"}

        g.move_cursor(column=app.cols_shown.index("ingestTime"))
        await pilot.press("F")
        inp = app.screen.query_one(Input)
        inp.value = ".2f"
        await pilot.press("enter")
        assert isinstance(app.screen, FormatScreen)
        inp.value = "%Y-%m-%d"
        await pilot.press("enter")
        await pilot.pause(0.1)
        assert len(str(g.get_cell_at(g.cursor_coordinate))) == 10


async def test_format_change_does_not_resurrect_old_stats(demo_path):
    from pqx.data import View

    app = PqxApp(demo_path)
    async with app.run_test(size=SIZE) as pilot:
        await settle(pilot, app)
        g = app.query_one(GridTable)
        g.move_cursor(column=app.cols_shown.index("psfFlux"))
        await pilot.press("i")  # profile psfFlux
        await settle(pilot, app)
        assert app._stats_rendered and app._stats_rendered[1] == "psfFlux"
        await pilot.press("1")  # back to the grid, cursor still on psfFlux
        await pilot.pause(0.1)
        g.focus()
        calls = []
        app._render_stats = lambda *a: calls.append(a)
        await pilot.press("less_than_sign")
        await pilot.pause(0.1)
        assert len(calls) == 1  # same view: reformatted in place
        app.view = View(where="psfFlux < 0")  # a new view whose profile never arrived (e.g. cancelled)
        await pilot.press("less_than_sign")
        await pilot.pause(0.1)
        assert len(calls) == 1  # old numbers are not redrawn under the new filter


async def test_move_grid_to_column(demo_path):
    app = PqxApp(demo_path)
    async with app.run_test(size=SIZE) as pilot:
        await settle(pilot, app)
        g = app.query_one(GridTable)
        g.move_cursor(row=5)
        assert app._move_grid_to_column("ingestTime")
        await pilot.pause(0.05)
        assert app.cols_shown[g.cursor_column] == "ingestTime" and g.cursor_row == 5
        app.set_current_column("ra", "grid")
        assert app.current_column == "ra"
        app.cols_shown = [c for c in app.cols_shown if c != "dec"]
        assert not app._move_grid_to_column("dec")


async def _press(pilot, app, *keys):
    await pilot.press(*keys)
    await settle(pilot, app)
    await pilot.pause(0.1)


def _count_stats(app) -> list[str]:
    calls = []
    compute = app.compute_stats
    app.compute_stats = lambda name: (calls.append(name), compute(name))
    return calls


async def test_linked_columns_across_tabs(demo_path):
    """Data, Schema and Stats stay on one current column, by number keys and ctrl+arrows."""
    app = PqxApp(demo_path)
    async with app.run_test(size=SIZE) as pilot:
        await settle(pilot, app)
        names = app.ds.column_names
        g = app.query_one(GridTable)
        st = app.query_one("#schema-table", DataTable)
        ol = app.query_one("#stats-cols", OptionList)
        tc = app.query_one(TabbedContent)
        assert app.current_column == names[0]
        calls = _count_stats(app)

        g.move_cursor(row=5, column=names.index("mag"))
        await pilot.pause(0.1)
        assert app.current_column == "mag"

        await _press(pilot, app, "2")  # Data -> Schema
        assert tc.active == "tab-schema" and st.cursor_row == names.index("mag")
        assert plain(app.query_one("#schema-desc", Static)).startswith("mag")
        await _press(pilot, app, "down")
        assert app.current_column == "snr"

        await _press(pilot, app, "3")  # Schema -> Stats: highlighted and profiled, once
        assert ol.highlighted == names.index("snr") and app._stats_shown == "snr" and calls == ["snr"]
        await pilot.press("down")
        await pilot.pause(0.3)
        await settle(pilot, app)
        assert app.current_column == "trailLength" and app._stats_shown == "trailLength"

        await _press(pilot, app, "1")  # Stats -> Data: same row
        assert names[g.cursor_column] == "trailLength" and g.cursor_row == 5

        await _press(pilot, app, "3")  # Data -> Stats: already profiled, not again
        assert ol.highlighted == names.index("trailLength") and calls == ["snr", "trailLength"]
        await _press(pilot, app, "up")
        await _press(pilot, app, "2")  # Stats -> Schema
        assert st.cursor_row == names.index("snr")
        await _press(pilot, app, "up")
        await _press(pilot, app, "1")  # Schema -> Data
        assert names[g.cursor_column] == "mag" and g.cursor_row == 5

        await _press(pilot, app, "right")
        await _press(pilot, app, "ctrl+right")  # Data -> Schema
        assert tc.active == "tab-schema" and st.cursor_row == names.index("snr")
        await _press(pilot, app, "down")
        await _press(pilot, app, "ctrl+right")  # Schema -> Stats
        assert tc.active == "tab-stats" and app._stats_shown == "trailLength"
        await pilot.press("down")
        await pilot.pause(0.3)
        await _press(pilot, app, "ctrl+left")  # Stats -> Schema
        assert tc.active == "tab-schema" and st.cursor_row == names.index("isDipole")
        await _press(pilot, app, "down")
        await _press(pilot, app, "ctrl+left")  # Schema -> Data
        assert tc.active == "tab-data" and names[g.cursor_column] == "detector"
        await _press(pilot, app, "left")
        await _press(pilot, app, "ctrl+left", "ctrl+left", "ctrl+left")  # Data -> Meta -> Plot (not linked) -> Stats
        assert tc.active == "tab-stats" and ol.highlighted == names.index("isDipole")
        assert app.current_column == "isDipole"
        await pilot.press("up")
        await pilot.pause(0.3)
        await _press(pilot, app, "ctrl+right", "ctrl+right", "ctrl+right")  # Stats -> Plot -> Meta -> Data
        assert tc.active == "tab-data" and names[g.cursor_column] == "trailLength" and g.cursor_row == 5


async def test_linked_columns_jumps(demo_path):
    """i and Schema Enter jump to Stats on that column, profile it once, and the others follow."""
    app = PqxApp(demo_path)
    async with app.run_test(size=SIZE) as pilot:
        await settle(pilot, app)
        names = app.ds.column_names
        g = app.query_one(GridTable)
        st = app.query_one("#schema-table", DataTable)
        tc = app.query_one(TabbedContent)
        calls = _count_stats(app)

        g.move_cursor(column=names.index("band"))
        await _press(pilot, app, "i")
        assert tc.active == "tab-stats" and app._stats_shown == "band" and calls == ["band"]
        await _press(pilot, app, "2")
        assert st.cursor_row == names.index("band")
        st.move_cursor(row=names.index("dec"))
        await _press(pilot, app, "enter")
        assert tc.active == "tab-stats" and app._stats_shown == "dec" and calls == ["band", "dec"]
        assert app.current_column == "dec"
        await _press(pilot, app, "1")
        assert names[g.cursor_column] == "dec"


async def test_linked_columns_hidden(demo_path):
    """A hidden current column leaves the grid put, with a status hint until the cursor moves."""
    app = PqxApp(demo_path)
    async with app.run_test(size=SIZE) as pilot:
        await settle(pilot, app)
        names = app.ds.column_names
        g = app.query_one(GridTable)
        st = app.query_one("#schema-table", DataTable)
        status = app.query_one("#status", Static)
        g.move_cursor(row=7, column=names.index("mag"))
        await _press(pilot, app, "minus")  # hide mag: the cursor lands on snr, same row
        assert app.current_column == "snr" and g.abs_row == 7

        await _press(pilot, app, "2")
        st.move_cursor(row=names.index("mag"))
        await _press(pilot, app, "1")
        assert app.cols_shown[g.cursor_column] == "snr" and app.current_column == "mag"
        s = plain(status)  # appended to the usual status
        assert s.startswith("✓ 20,000 rows") and s.endswith("! mag is hidden · c to show")
        await _press(pilot, app, "2")  # nothing moved: Schema is still on mag
        assert st.cursor_row == names.index("mag") and app.current_column == "mag"
        assert "hidden" not in plain(status)
        await _press(pilot, app, "1")
        assert "mag is hidden" in plain(status)
        await _press(pilot, app, "right")  # moving clears the hint
        assert "hidden" not in plain(status) and app.current_column == app.cols_shown[g.cursor_column]

        await _press(pilot, app, "2")
        st.move_cursor(row=names.index("mag"))
        await _press(pilot, app, "1")
        assert "mag is hidden" in plain(status)
        await pilot.press("c")  # bringing it back lands on it, same row
        assert isinstance(app.screen, ColumnPicker)
        await pilot.press("ctrl+a")
        await pilot.click("#apply")
        await settle(pilot, app)
        assert app.cols_shown[g.cursor_column] == "mag" and "hidden" not in plain(status)
        assert g.abs_row == 7

        # a filter clears the hint, and it never hides "No matching rows"
        await _press(pilot, app, "minus")
        await _press(pilot, app, "2")
        st.move_cursor(row=names.index("mag"))
        await _press(pilot, app, "1")
        assert "mag is hidden" in plain(status)
        await pilot.press("slash", *"mag > 99", "enter")
        await settle(pilot, app)
        assert app.total == 0 and plain(status).startswith("! No matching rows") and "hidden" not in plain(status)


async def test_linked_columns_filters(demo_path):
    """Applying a filter keeps the current column in the grid, and on Stats re-profiles it."""
    app = PqxApp(demo_path)
    async with app.run_test(size=SIZE) as pilot:
        await settle(pilot, app)
        names = app.ds.column_names
        g = app.query_one(GridTable)
        st = app.query_one("#schema-table", DataTable)
        ol = app.query_one("#stats-cols", OptionList)
        g.move_cursor(column=names.index("mag"))
        await pilot.press("slash", *"mag > 18", "enter")  # from Data
        await settle(pilot, app)
        assert not app.view.is_trivial and names[g.cursor_column] == "mag" and app.current_column == "mag"

        await _press(pilot, app, "2")
        await pilot.press("slash", *"mag > 19", "enter")  # from Schema
        await settle(pilot, app)
        assert st.cursor_row == names.index("mag") and app.current_column == "mag"
        await _press(pilot, app, "escape", "3")  # profiled under the new filter
        assert app._stats_shown == "mag" and app._stats_rendered[0] is app.view

        await pilot.press("slash", *"select ra, dec from t", "enter")  # on Stats, dropping mag
        await settle(pilot, app)
        assert app._stats_col == "ra" and ol.highlighted == 0 and app._stats_shown == "ra"
        assert app._stats_rendered[0] is app.view and app.current_column == "mag"
        await pilot.press("ctrl+x")  # cleared: back on mag
        await settle(pilot, app)
        await _press(pilot, app, "escape")  # out of the filter box
        assert app.query_one(TabbedContent).active == "tab-stats"
        assert app.view.is_trivial and ol.highlighted == names.index("mag") and app._stats_shown == "mag"
        assert app._stats_rendered[0] is app.view
        await _press(pilot, app, "1")
        assert names[g.cursor_column] == "mag"


async def test_linked_columns_sql_result(demo_path):
    """SQL-result columns that aren't in the file leave Schema on its own row; Stats follows."""
    app = PqxApp(demo_path)
    async with app.run_test(size=SIZE) as pilot:
        await settle(pilot, app)
        names = app.ds.column_names
        g = app.query_one(GridTable)
        st = app.query_one("#schema-table", DataTable)
        ol = app.query_one("#stats-cols", OptionList)
        status = app.query_one("#status", Static)
        await pilot.press("slash", *"select ra, dec, mag*2 as m2 from t", "enter")
        await settle(pilot, app)
        assert app.cols_shown == ["ra", "dec", "m2"]
        await _press(pilot, app, "end")
        assert app.current_column == "m2"

        row = st.cursor_row
        await _press(pilot, app, "2")  # m2 isn't in the file: Schema stays put
        assert st.cursor_row == row and app.current_column == "m2"
        await _press(pilot, app, "3")
        assert ol.highlighted == 2 and app._stats_shown == "m2"
        await _press(pilot, app, "2")
        st.move_cursor(row=names.index("dec"))
        await _press(pilot, app, "1")
        assert app.cols_shown[g.cursor_column] == "dec"

        await _press(pilot, app, "2")
        st.move_cursor(row=names.index("band"))  # not in the result
        await _press(pilot, app, "3")  # Stats keeps its column; that doesn't change the current one
        assert app._stats_col == "m2" and app.current_column == "band"
        await _press(pilot, app, "2")
        assert st.cursor_row == names.index("band")
        await _press(pilot, app, "1")  # can't be shown in this result: no hint, the grid stays put
        assert app.cols_shown[g.cursor_column] == "dec" and "hidden" not in plain(status)
        assert app.current_column == "band"


async def test_linked_columns_no_bounce(demo_path):
    """Views moved behind other tabs, or while being synced, never change the current column."""
    app = PqxApp(demo_path)
    async with app.run_test(size=SIZE) as pilot:
        await settle(pilot, app)
        names = app.ds.column_names
        g = app.query_one(GridTable)
        ol = app.query_one("#stats-cols", OptionList)
        g.move_cursor(column=names.index("ra"))
        await _press(pilot, app, "3")
        assert app.current_column == "ra"
        g.move_cursor(column=names.index("mag"), row=3)  # grid and Schema moved behind Stats: ignored
        app.query_one("#schema-table", DataTable).move_cursor(row=names.index("band"))
        await pilot.pause(0.1)
        assert app.current_column == "ra"
        ol.highlighted = names.index("snr")  # Stats' own move counts
        await pilot.pause(0.3)
        await settle(pilot, app)
        assert app.current_column == "snr"
        seen = []
        orig = app.set_current_column
        app.set_current_column = lambda name, source: (seen.append((name, source)), orig(name, source))
        for key in ["1", "2", "3", "1", "2", "1", "3"]:
            await _press(pilot, app, key)
            assert app.current_column == "snr", key
        # the grid and Schema were elsewhere: their syncs did report, with the same column
        assert {s for _, s in seen} >= {"grid", "schema"} and {n for n, _ in seen} == {"snr"}


async def test_detail_pane_focus_and_link(demo_path):
    from textual.events import MouseScrollDown

    from pqx.widgets import DetailList

    app = PqxApp(demo_path)
    async with app.run_test(size=(150, 24)) as pilot:  # short: the pane scrolls
        await settle(pilot, app)
        g = app.query_one(GridTable)
        lst = app.query_one(DetailList)
        g.move_cursor(row=7, column=2)
        await pilot.press("d")
        await pilot.pause(0.1)
        assert lst.option_count == len(app.cols_shown)
        assert lst.selected == app.cols_shown[2]

        await pilot.press("tab")  # into the pane, selection on the grid's column
        await pilot.pause(0.05)
        assert app.focused is lst and lst.selected == app.cols_shown[2]
        assert app.query_one("#detail").has_focus_within
        assert "back to grid" in plain(app.query_one("#keys"))
        assert isinstance(selected_prompt(lst), Styled)  # focused: the whole entry reversed
        await pilot.press("down", "down")  # the grid follows sideways, same row
        await pilot.pause(0.05)
        assert lst.selected == app.cols_shown[4]
        assert g.cursor_column == 4 and g.cursor_row == 7
        assert app.current_column == app.cols_shown[4]
        await pilot.press("end")
        await pilot.pause(0.05)
        assert g.cursor_column == len(app.cols_shown) - 1 and g.cursor_row == 7
        await pilot.press("home", "down")
        await pilot.press("enter")  # back to the grid, on the selected column
        await pilot.pause(0.05)
        assert app.focused is g and g.cursor_column == 1 and g.cursor_row == 7
        assert not isinstance(selected_prompt(lst), Styled)  # unfocused: just the name
        assert "into detail" in plain(app.query_one("#keys"))

        for key in ("escape", "tab"):
            await pilot.press("tab", "down", key)
            await pilot.pause(0.05)
            assert app.focused is g and g.cursor_column == 2 and g.cursor_row == 7
            g.move_cursor(column=1)
            await pilot.pause(0.05)

        # moving the grid moves the pane's selection, without the pane taking over
        g.move_cursor(column=5)
        await pilot.pause(0.05)
        assert lst.selected == app.cols_shown[5] and app.focused is g
        await pilot.press("down")  # a new row: same selection, new values
        await pilot.pause(0.05)
        assert lst.selected == app.cols_shown[5] and g.cursor_row == 8 and g.cursor_column == 5

        # the wheel only scrolls the pane
        lst.scroll_home(animate=False)
        await pilot.pause(0.05)
        for _ in range(5):  # routed by the screen, as a real wheel is
            await pilot._post_mouse_events([MouseScrollDown], lst, offset=(2, 2))
        await pilot.pause(0.1)
        assert lst.scroll_y > 0
        assert lst.selected == app.cols_shown[5] and g.cursor_column == 5 and app.focused is g
        y0 = lst.scroll_y
        await pilot.press("down")  # a new row keeps the pane where it was scrolled to
        await pilot.pause(0.05)
        assert lst.scroll_y == y0 and g.cursor_row == 9

        # a click on an entry focuses the pane and moves the grid there
        lst.scroll_home(animate=False)
        await pilot.pause(0.05)
        y = lst._index_to_line[3]
        await pilot.click(lst, offset=(2, y))
        await pilot.pause(0.05)
        assert app.focused is lst and lst.selected == app.cols_shown[3]
        assert g.cursor_column == 3 and g.cursor_row == 9

        await pilot.press("d")  # closing the pane hands focus back to the grid
        await pilot.pause(0.05)
        assert not app.query_one("#detail").display and app.focused is g
        assert "into detail" not in plain(app.query_one("#keys"))  # Tab goes to the filter now


def selected_prompt(lst):
    return lst.get_option_at_index(lst.highlighted).prompt


async def test_detail_pane_rows_and_views(demo_path):
    from pqx.widgets import DetailList

    app = PqxApp(demo_path)
    async with app.run_test(size=SIZE) as pilot:
        await settle(pilot, app)
        g = app.query_one(GridTable)
        lst = app.query_one(DetailList)
        d = app.query_one("#detail")
        g.move_cursor(column=3)
        await pilot.press("d")
        await pilot.pause(0.1)

        await pilot.press("ctrl+end")  # a new window of rows: the pane follows, same column
        await settle(pilot, app)
        assert g.abs_row == 19_999 and "row 19,999" in str(d.border_title)
        assert lst.selected == app.cols_shown[3]
        await pilot.press("tab", "down", "enter")
        await pilot.pause(0.05)
        assert g.abs_row == 19_999 and g.cursor_column == 4

        app.apply_filter("select band, ra, dec from t")  # other columns
        await settle(pilot, app)
        assert [o.id for o in lst.options] == ["band", "ra", "dec"]
        await pilot.press("tab", "down")
        await pilot.pause(0.05)
        assert app.cols_shown[g.cursor_column] == lst.selected
        await pilot.press("enter")

        app.apply_filter("ra < -1000")  # no rows: no stale entries to wander into
        await settle(pilot, app)
        assert lst.option_count == 0 and "no rows" in str(d.border_title)
        col = app.current_column
        await pilot.press("tab", "down")
        await pilot.pause(0.05)
        assert app.current_column == col


@pytest.fixture(scope="module")
def tall_detail_path(tmp_path_factory):
    """150 integer columns whose values say their row and column, and a note
    that wraps on odd rows: a Details pane far longer than the screen."""
    import numpy as np
    import pyarrow as pa

    n = 50
    ints = [(f"c{j:03d}", np.arange(n) * 1000 + j) for j in range(150)]
    note = ("note", [("odd " * 40 if i % 2 else "even") + f"#{i}" for i in range(n)])
    p = tmp_path_factory.mktemp("data") / "tall.parquet"
    pq.write_table(pa.table(dict(ints[:60] + [note] + ints[60:])), p)
    return str(p)


def detail_text(lst):
    """The pane as drawn: {entry index: its lines' text, joined}."""
    out = {}
    for y in range(lst.scrollable_content_region.height):
        line = lst.scroll_offset.y + y
        if line < len(lst._lines):
            out.setdefault(lst._lines[line][0], []).append(lst.render_line(y).text)
    return {k: " ".join(v) for k, v in out.items()}


async def test_detail_pane_renders_only_what_is_in_view(tall_detail_path):
    from pqx.widgets import DetailList

    app = PqxApp(tall_detail_path)
    async with app.run_test(size=(150, 30)) as pilot:
        await settle(pilot, app)
        g = app.query_one(GridTable)
        lst = app.query_one(DetailList)
        g.move_cursor(row=2, column=0)
        await pilot.press("d")
        await pilot.pause(0.1)
        assert lst.option_count == 151 and lst.selected == "c000"
        built = []
        orig = lst._grid
        lst._grid = lambda name, *a: (built.append(name), orig(name, *a))[1]

        await pilot.press("down")  # a new row: only the entries in view are built
        await pilot.pause(0.1)
        assert g.cursor_row == 3
        in_view = lst.scrollable_content_region.height
        assert 0 < len(built) <= in_view + 1, built  # (+1: the selection, built whole)
        shown = detail_text(lst)
        assert shown[5].split()[:2] == ["c005", "3005"]

        # entries that were out of view show the new row when scrolled to, at the right heights
        note = [o.id for o in lst.options].index("note")
        assert lst._heights[note] > 1  # the odd row's long note wraps
        for k in (150, note):
            lst.scroll_to(y=lst._index_to_line[k], animate=False)
            await pilot.pause(0.05)
            shown = detail_text(lst)
            assert k in shown
            for i, text in shown.items():
                name = lst.options[i].id
                if name == "note":
                    assert "".join(text.split()).endswith("#3") and text.count("odd") == 40
                else:
                    assert text.split()[:2] == [name, str(3000 + int(name[1:]))]
        assert len(built) < 151

        y0 = lst.scroll_y
        await pilot.press("up")  # an even row: the note is one line again; the pane stays put
        await pilot.pause(0.1)
        assert lst._heights[note] == 1 and lst.scroll_y == y0
        for i, text in detail_text(lst).items():
            name = lst.options[i].id
            assert text.split()[:2] == ([name, "even#2"] if name == "note" else [name, str(2000 + int(name[1:]))])


def test_detail_entry_layout_matches_rich_grid():
    """EntryGrid lays an entry out as Rich's Table.grid does, cell for cell,
    and a lazy entry's quick one-line height agrees with the rendering."""
    import random

    from rich.console import Console
    from rich.segment import Segment
    from rich.text import Text

    from pqx.widgets import EntryGrid, _LazyEntry

    console = Console(width=200)
    rnd = random.Random(5)
    chars = "abcxyz0123456789.-e+ 日本é\t"

    def rand(n):
        return "".join(rnd.choice(chars) for _ in range(n))

    def lines(renderable, width, base):
        segs = console.render(Styled(renderable, base) if base else renderable,
                              console.options.update_width(width).update(highlight=False))
        return [list(Segment.simplify(ln)) for ln in Segment.split_and_crop_lines(segs, width, pad=False)]

    quick = 0
    for _ in range(1500):
        width, nw = rnd.randint(1, 70), rnd.randint(0, 23)
        name = Text(rand(rnd.randint(1, 30)).replace("\t", "_"), style=rnd.choice(["bold", "bold reverse"]))
        value = Text(rand(rnd.choice([0, 1, 5, 10, 30, 80])), style=rnd.choice(["", "dim"]))
        if rnd.random() < .5:
            value.append("  " + rand(3), "dim")
        if rnd.random() < .3:
            value.append("\n· " + rand(rnd.randint(1, 60)), "dim")
        base = rnd.choice([None, "reverse", "on blue"])
        grid = EntryGrid(name, value, nw)
        assert lines(grid, width, base) == lines(grid.table(), width, base), (width, nw, name, value)
        if _LazyEntry.one_line(value, nw, width):  # the quick answer
            assert len(lines(grid.table(), width, None)) == 1, (width, nw, value)
            quick += 1
    assert quick > 100
