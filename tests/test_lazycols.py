"""The grid and Details pane fetch only the columns they need (design section E)."""
import threading

import duckdb
import numpy as np
import pyarrow as pa
import pyarrow.parquet as pq
import pytest
from textual.coordinate import Coordinate

import pqx.app as pqx_app
from pqx import fmt as F
from pqx.app import GridTable, PqxApp
from pqx.cells import FAILED_MARK, MISSING, PLACEHOLDER, text_width
from pqx.widgets import DetailList

from test_app import SIZE, settle

NCOLS = 120
NROWS = 3000


@pytest.fixture(scope="module")
def lazy_path(tmp_path_factory):
    """120 columns whose every value tells its row and column: c{j} = row * 1000 + j,
    except every 10th column, a float (row + j / 1000, made wide for some rows)."""
    rows = np.arange(NROWS)
    cols = {}
    for j in range(NCOLS):
        if j % 10 == 5:
            v = rows + j / 1000
            v[::97] *= -1e5  # a few much wider values
            cols[f"f{j:03d}"] = v
        else:
            cols[f"c{j:03d}"] = rows * 1000 + j
    p = tmp_path_factory.mktemp("data") / "lazy.parquet"
    pq.write_table(pa.table(cols), p, row_group_size=1000)
    return str(p)


def truth(name: str, row: int):
    j = int(name[1:])
    if name.startswith("c"):
        return row * 1000 + j
    v = row + j / 1000
    return v * -1e5 if row % 97 == 0 else v


@pytest.fixture
def lazy(monkeypatch):
    """Load plain views lazily however cheap the file (small test files otherwise load whole)."""
    monkeypatch.setattr(pqx_app, "LAZY_MIN_SAVING_MS", float("-inf"))


def record(app):
    """Count the columns each fetch and fetch_columns call asks for."""
    calls = []
    fetch, fetch_columns = app.ds.fetch, app.ds.fetch_columns

    def f(view, offset, limit, columns=None):
        calls.append(("fetch", list(columns or app.ds.column_names)))
        return fetch(view, offset, limit, columns)

    def fc(rows, columns):
        calls.append(("columns", list(columns)))
        return fetch_columns(rows, columns)
    app.ds.fetch, app.ds.fetch_columns = f, fc
    return calls


def check_page(app):
    """Every loaded value of the page and every grid cell is right; unloaded ones are placeholders."""
    page, g = app.page, app.query_one(GridTable)
    assert g.row_count == len(page.rows)
    for r in range(0, len(page.rows), 37):
        rn = page.row_numbers[r]
        for c, name in enumerate(page.columns):
            v = page.rows[r][c]
            text = str(g.get_cell_at(Coordinate(r, c)))
            if name in page.missing:
                assert v is MISSING and text == PLACEHOLDER
            else:
                assert v == truth(name, rn), (name, rn)
                fm = app.formatters[name]
                assert text == fm.plain(truth(name, rn), app.raw)


async def test_loads_only_the_columns_near_the_view(lazy_path, lazy):
    app = PqxApp(lazy_path)
    calls = record(app)
    async with app.run_test(size=SIZE) as pilot:
        await settle(pilot, app)
        g = app.query_one(GridTable)
        kind, first = calls[0]
        assert kind == "fetch" and 10 < len(first) < NCOLS / 2
        assert first == app.cols_shown[:len(first)]  # from the left edge, in order
        fetched = set(first)
        for kind, cols in calls[1:]:  # anything later is only what the first didn't have
            assert kind == "columns" and not fetched & set(cols)
            fetched |= set(cols)
        page = app.page
        assert page.missing and page.missing == set(app.cols_shown) - fetched
        last_visible = g.column_window()[1]
        assert all(n not in page.missing for n in app.cols_shown[:last_visible + 1])
        check_page(app)


async def test_scrolling_right_fetches_and_shows_columns(lazy_path, lazy):
    app = PqxApp(lazy_path)
    calls = record(app)
    async with app.run_test(size=SIZE) as pilot:
        await settle(pilot, app)
        g = app.query_one(GridTable)
        calls.clear()
        await pilot.press("end")  # last column
        await settle(pilot, app)
        assert g.cursor_column == NCOLS - 1
        assert calls and all(k == "columns" for k, _ in calls)
        first, last, _, _ = g.column_window()
        assert not {app.cols_shown[i] for i in range(first, last + 1)} & app.page.missing
        check_page(app)

        # stepping back left a column at a time fetches about once a screen, not per column
        calls.clear()
        for _ in range(60):
            await pilot.press("left")
        await settle(pilot, app)
        assert 1 <= len(calls) <= 4
        check_page(app)

        # a new window starts lazy again, around where the view is
        calls.clear()
        await pilot.press("g")
        await pilot.press(*"2500", "enter")
        await settle(pilot, app)
        assert calls[0][0] == "fetch" and len(calls[0][1]) < NCOLS
        assert app.cols_shown[g.cursor_column] in calls[0][1]
        check_page(app)


async def test_pinned_columns_always_load(lazy_path, lazy):
    app = PqxApp(lazy_path)
    calls = record(app)
    async with app.run_test(size=SIZE) as pilot:
        await settle(pilot, app)
        g = app.query_one(GridTable)
        await pilot.press("right", "right", "p")  # pin the first three
        await pilot.press("end")
        await settle(pilot, app)
        calls.clear()
        await pilot.press("g")
        await pilot.press(*"1500", "enter")
        await settle(pilot, app)
        assert g.fixed_columns == 3
        assert app.cols_shown[:3] == calls[0][1][:3]
        assert app.cols_shown[-1] in calls[0][1]
        check_page(app)


async def test_stale_column_fetch_is_discarded(lazy_path, lazy):
    """A column fetch overtaken by a new window must not write its rows into it."""
    app = PqxApp(lazy_path)
    async with app.run_test(size=SIZE) as pilot:
        await settle(pilot, app)
        g = app.query_one(GridTable)
        real = app.ds.fetch_columns
        started, release = threading.Event(), threading.Event()
        slow = []

        def fetch_columns(rows, columns):
            if not slow:  # the first call: read now, return late (as if interrupting came too late)
                slow.append(columns)
                out = real(rows, columns)
                started.set()
                release.wait(10)
                return out
            return real(rows, columns)
        app.ds.fetch_columns = fetch_columns
        await pilot.press("end")
        for _ in range(200):
            if started.is_set():
                break
            await pilot.pause(0.02)
        assert started.is_set()
        old = app.page
        # a new window (the last rows) around the first column: the slow fetch's columns are missing from it
        g.move_cursor(column=0)
        await pilot.press("ctrl+end")
        for _ in range(200):
            if app.page is not old:
                break
            await pilot.pause(0.02)
        assert app.page is not old and app.page.offset > old.offset
        release.set()  # now the old window's columns arrive
        await settle(pilot, app)
        assert g.cursor_column == 0 and set(slow[0]) <= app.page.missing  # not merged into the new page
        check_page(app)  # every value is the new rows', not the old ones'
        assert set(slow[0]) <= old.missing  # nor into the old one, which is gone


async def test_detail_pane_shows_columns_the_grid_has_not_loaded(lazy_path, lazy):
    app = PqxApp(lazy_path)
    calls = record(app)
    async with app.run_test(size=SIZE) as pilot:
        await settle(pilot, app)
        assert app.page.missing
        far = app.cols_shown[-1]
        assert far in app.page.missing
        calls.clear()
        await pilot.press("d")
        await pilot.pause(0.3)
        await settle(pilot, app)
        assert not app.page.missing  # the pane loaded the rest of the page, once
        assert len(calls) == 1 and calls[0][0] == "columns"
        lst = app.query_one(DetailList)
        items = dict((n, v.plain) for n, v in lst._items)
        g = app.query_one(GridTable)
        assert items[far] == str(truth(far, g.abs_row))
        check_page(app)

        calls.clear()  # moving the cursor fetches nothing more
        for _ in range(5):
            await pilot.press("down")
        await pilot.pause(0.3)
        await settle(pilot, app)
        assert calls == []
        items = dict((n, v.plain) for n, v in lst._items)
        assert items[far] == str(truth(far, g.abs_row))


async def test_detail_pane_placeholder_while_loading(lazy_path, lazy):
    app = PqxApp(lazy_path)
    async with app.run_test(size=SIZE) as pilot:
        await settle(pilot, app)
        real = app.ds.fetch_columns
        release = threading.Event()

        def fetch_columns(rows, columns):
            release.wait(10)
            return real(rows, columns)
        app.ds.fetch_columns = fetch_columns
        await pilot.press("d")
        await pilot.pause(0.3)
        items = dict((n, v.plain) for n, v in app.query_one(DetailList)._items)
        assert items[app.cols_shown[-1]] == PLACEHOLDER and items[app.cols_shown[0]] == "0"
        release.set()
        await settle(pilot, app)
        items = dict((n, v.plain) for n, v in app.query_one(DetailList)._items)
        assert items[app.cols_shown[-1]] == str(truth(app.cols_shown[-1], 0))


async def test_actions_get_values_of_unloaded_columns(lazy_path, lazy):
    """y, = and the format dialog's sample load the cursor's column first if need be."""
    app = PqxApp(lazy_path)
    async with app.run_test(size=SIZE) as pilot:
        await settle(pilot, app)
        g = app.query_one(GridTable)
        real = app.ds.fetch_columns
        release = threading.Event()
        blocked = []

        def fetch_columns(rows, columns):
            if not blocked:  # the scroll's own fetch hangs; the action's goes through
                blocked.append(columns)
                release.wait(10)
            return real(rows, columns)
        app.ds.fetch_columns = fetch_columns
        await pilot.press("end", "down", "down")
        await pilot.pause(0.2)
        name = app.cols_shown[g.cursor_column]
        assert app._cursor_value() == (name, MISSING)
        await pilot.press("y")
        for _ in range(100):
            if app.clipboard:
                break
            await pilot.pause(0.02)
        assert app.clipboard == str(truth(name, 2))
        release.set()
        await settle(pilot, app)

        g.move_cursor(row=3)
        await pilot.press("equals_sign")
        await settle(pilot, app)
        assert app.view.where == f"{name} = {truth(name, 3)}" and app.total == 1


async def test_views_without_row_ids_fetch_every_column(lazy_path, odd_path, lazy):
    app = PqxApp(lazy_path)
    calls = record(app)
    async with app.run_test(size=SIZE) as pilot:
        await settle(pilot, app)
        await pilot.press("slash")
        await pilot.press(*"select * from t where c000 > 5000", "enter")
        await settle(pilot, app)
        assert app.view.sql and calls[-1] == ("fetch", app.cols_shown) and not app.page.missing
        g = app.query_one(GridTable)
        await pilot.press("end")
        await settle(pilot, app)
        assert calls[-1][0] == "fetch"  # nothing fetched column-wise
        assert str(g.get_cell_at(Coordinate(0, NCOLS - 1))) == str(truth(app.cols_shown[-1], 6))

    # a filtered view of a file with its own file_row_number column has no row numbers either
    app = PqxApp(odd_path, where="x > 0")
    calls = record(app)
    async with app.run_test(size=(60, 30)) as pilot:
        await settle(pilot, app)
        assert not app.ds.has_row_ids(app.view)
        assert calls[-1] == ("fetch", app.cols_shown) and not app.page.missing


async def test_filtered_views_load_lazily(lazy_path):
    """A filter or sort reads only what it selects, so these are lazy even on a small file."""
    app = PqxApp(lazy_path, where="c000 % 3 = 0")
    calls = record(app)
    async with app.run_test(size=SIZE) as pilot:
        await settle(pilot, app)
        assert app.page.missing and len(calls[0][1]) < NCOLS
        await pilot.press("end")
        await settle(pilot, app)
        check_page(app)
        assert all(rn % 3 == 0 for rn in app.page.row_numbers)


async def test_small_plain_files_load_whole(lazy_path):
    """Without the override, a window this cheap is read whole: nothing to fetch later."""
    app = PqxApp(lazy_path)
    calls = record(app)
    async with app.run_test(size=SIZE) as pilot:
        await settle(pilot, app)
        assert calls == [("fetch", app.cols_shown)] and not app.page.missing
    ds = app.ds
    assert ds.window_cost(0, 150, ds.column_names) > ds.window_cost(0, 150, ds.column_names[:10]) > 0


async def test_widths_do_not_jump_when_columns_arrive(lazy_path, lazy):
    app = PqxApp(lazy_path)
    async with app.run_test(size=SIZE) as pilot:
        await settle(pilot, app)
        g = app.query_one(GridTable)
        real = app.ds.fetch_columns
        release = threading.Event()

        def fetch_columns(rows, columns):
            release.wait(10)
            return real(rows, columns)
        app.ds.fetch_columns = fetch_columns
        await pilot.press("end")
        await pilot.pause(0.2)
        assert PLACEHOLDER in g.render_line(3).text  # drawn while loading
        missing = set(app.page.missing)
        before = {c.key.value: c.content_width for c in g.ordered_columns}
        x_before = g.scroll_x
        release.set()
        await settle(pilot, app)
        after = {c.key.value: c.content_width for c in g.ordered_columns}
        page = app.page
        for c, name in enumerate(page.columns):
            assert after[name] >= before[name]  # widths only grow
            if name in missing and name.startswith("c"):
                assert after[name] == before[name]  # ints: the statistics give their exact width
            if name not in page.missing:  # and no number is cut
                widest = max(text_width(app.formatters[name].plain(r[c], app.raw)) for r in page.rows)
                assert after[name] >= widest
        assert g.scroll_x == x_before and g.cursor_column == NCOLS - 1 and g.cursor_cell_in_view()
        await pilot.pause(0.1)
        for y in range(2, 8):  # and redrawn with the values (no stale rendering of the placeholders)
            line = g.render_line(y).text
            assert PLACEHOLDER not in line and line.rstrip().endswith(str(truth(app.cols_shown[-1], y - 2)))


async def test_raw_and_formats_on_a_lazy_page(lazy_path, lazy):
    app = PqxApp(lazy_path)
    async with app.run_test(size=SIZE) as pilot:
        await settle(pilot, app)
        await pilot.press("f")  # raw while the far columns are missing
        await settle(pilot, app)
        await pilot.press("end")
        await settle(pilot, app)
        assert app.raw
        check_page(app)
        g = app.query_one(GridTable)
        name = app.cols_shown[115]  # a float: raw is its repr
        assert str(g.get_cell_at(Coordinate(0, 115))) == repr(truth(name, 0)) != F.format_value(truth(name, 0), "float")


# ------------------------------------------------------------ review of PR #17
def visible_missing(app):
    g = app.query_one(GridTable)
    first, last, _, _ = g.column_window()
    return [app.cols_shown[i] for i in range(first, last + 1) if app.cols_shown[i] in app.page.missing]


async def wait_for(pilot, cond, n=300):
    for _ in range(n):
        if cond():
            return True
        await pilot.pause(0.02)
    return False


async def test_columns_load_after_esc_cancels_their_fetch(lazy_path, lazy):
    """Esc while the scrolled-to columns load: the next cursor move loads them."""
    app = PqxApp(lazy_path)
    async with app.run_test(size=SIZE) as pilot:
        await settle(pilot, app)
        real = app.ds.fetch_columns
        started, release = threading.Event(), threading.Event()
        first = []

        def fetch_columns(rows, columns):
            if not first:
                first.append(1)
                started.set()
                release.wait(10)
                raise duckdb.InterruptException("INTERRUPT Error: cancelled")  # as Esc makes it
            return real(rows, columns)
        app.ds.fetch_columns = fetch_columns
        await pilot.press("end")
        assert await wait_for(pilot, started.is_set)
        await pilot.press("escape")
        release.set()
        await settle(pilot, app)
        assert visible_missing(app)  # cancelled: still loading, not retried by itself
        await pilot.press("left", "down")
        await settle(pilot, app)
        assert not visible_missing(app)
        assert PLACEHOLDER not in app.query_one(GridTable).render_line(3).text
        check_page(app)


async def test_columns_load_after_a_cancelled_page_load(lazy_path, lazy):
    """A window load cancelled with Esc keeps the old page, and its columns load again."""
    app = PqxApp(lazy_path)
    async with app.run_test(size=SIZE) as pilot:
        await settle(pilot, app)
        real_fc, real_fetch = app.ds.fetch_columns, app.ds.fetch
        fc_started, fc_release = threading.Event(), threading.Event()
        f_started, f_release = threading.Event(), threading.Event()

        def fetch_columns(rows, columns):
            if not fc_started.is_set():
                fc_started.set()
                fc_release.wait(10)
                raise duckdb.InterruptException("INTERRUPT Error: superseded")  # load_window interrupts it
            return real_fc(rows, columns)

        def fetch(view, offset, limit, columns=None):
            if not f_started.is_set():
                f_started.set()
                f_release.wait(10)
                raise duckdb.InterruptException("INTERRUPT Error: cancelled")  # Esc
            return real_fetch(view, offset, limit, columns)
        app.ds.fetch_columns, app.ds.fetch = fetch_columns, fetch
        await pilot.press("end")
        assert await wait_for(pilot, fc_started.is_set)
        old = app.page
        await pilot.press("ctrl+end")  # a new window
        assert await wait_for(pilot, f_started.is_set)
        fc_release.set()
        await pilot.press("escape")
        f_release.set()
        await settle(pilot, app)
        assert app.page is old and not visible_missing(app)
        check_page(app)


async def test_a_superseded_page_is_not_applied(lazy_path, lazy):
    """An older window's _apply_page landing after a newer one's is dropped (and doesn't
    stop the page on screen from loading its columns)."""
    app = PqxApp(lazy_path)
    async with app.run_test(size=SIZE) as pilot:
        await settle(pilot, app)
        old, old_gen = app.page, app._shown_gen
        await pilot.press("ctrl+end")
        await settle(pilot, app)
        new = app.page
        app._apply_page(old, 0, None, old_gen)  # what a late call_from_thread would deliver
        await settle(pilot, app)
        assert app.page is new
        await pilot.press("end")
        await settle(pilot, app)
        assert not visible_missing(app)
        check_page(app)


async def test_columns_that_fail_show_so_and_are_not_retried(lazy_path, lazy):
    app = PqxApp(lazy_path)
    async with app.run_test(size=SIZE) as pilot:
        await settle(pilot, app)
        g = app.query_one(GridTable)
        calls = []

        def fetch_columns(rows, columns):
            calls.append(list(columns))
            raise RuntimeError("boom")
        app.ds.fetch_columns = fetch_columns
        await pilot.press("d")
        await pilot.pause(0.3)
        await settle(pilot, app)
        assert len(calls) == 1  # the pane's fetch
        far = app.cols_shown[-1]
        items = dict((n, v.plain) for n, v in app.query_one(DetailList)._items)
        assert items[far].startswith(FAILED_MARK)
        for _ in range(4):  # moving doesn't retry them (nor toast each time)
            await pilot.press("down")
            await pilot.pause(0.25)
            await settle(pilot, app)
        await pilot.press("end")
        await settle(pilot, app)
        assert len(calls) == 1
        assert str(g.get_cell_at(Coordinate(0, NCOLS - 1))) == FAILED_MARK
        assert FAILED_MARK in g.render_line(3).text and PLACEHOLDER not in g.render_line(3).text
        await pilot.press("y")  # says so, rather than copying nothing
        await pilot.pause(0.1)
        assert not app.clipboard
        assert any("couldn't be loaded" in n.message for n in app._notifications)


async def test_actions_on_a_loading_column_all_happen(lazy_path, lazy):
    """y then = on a column still loading: both wait for one fetch, neither is dropped."""
    app = PqxApp(lazy_path)
    async with app.run_test(size=SIZE) as pilot:
        await settle(pilot, app)
        g = app.query_one(GridTable)
        real = app.ds.fetch_columns
        release = threading.Event()
        calls = []

        def fetch_columns(rows, columns):
            calls.append(list(columns))
            release.wait(10)
            return real(rows, columns)
        app.ds.fetch_columns = fetch_columns
        await pilot.press("end")
        await pilot.pause(0.2)
        name = app.cols_shown[g.cursor_column]
        assert app._cursor_value() == (name, MISSING)
        await pilot.press("y", "equals_sign")
        await pilot.pause(0.2)
        release.set()
        await settle(pilot, app)
        assert app.clipboard == str(truth(name, 0))
        assert app.view.where == f"{name} = {truth(name, 0)}"
        assert sum(1 for c in calls if c == [name]) == 1


async def test_detail_and_grid_fetches_do_not_overlap(lazy_path, lazy):
    """With the pane open, scrolling doesn't fetch what the pane's fetch is bringing."""
    app = PqxApp(lazy_path)
    async with app.run_test(size=SIZE) as pilot:
        await settle(pilot, app)
        real = app.ds.fetch_columns
        release = threading.Event()
        calls = []

        def fetch_columns(rows, columns):
            calls.append(list(columns))
            release.wait(10)
            return real(rows, columns)
        app.ds.fetch_columns = fetch_columns
        await pilot.press("d")
        await pilot.pause(0.3)
        assert len(calls) == 1
        await pilot.press("end")
        await pilot.pause(0.2)
        release.set()
        await settle(pilot, app)
        seen = [n for c in calls for n in c]
        assert len(seen) == len(set(seen))
        check_page(app)
