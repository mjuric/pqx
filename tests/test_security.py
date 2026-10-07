"""Hostile files: names, values and metadata that try to reach the terminal, the SQL or the markup.

Each file is made here, in tmp_path. The pty test runs the real CLI and scans every
byte it writes to the terminal; the pilot tests drive the app headless.
"""
import base64
import os
import re
import sys

import pyarrow as pa
import pyarrow.parquet as pq
import pytest
from textual.coordinate import Coordinate
from textual.widgets import Input, Label
from textual.worker import WorkerState

from pqx.app import GridTable, PqxApp
from pqx.data import ParquetDataset, View

SIZE = (200, 50)
ESC = "\x1b"
OSC_TITLE = ESC + "]0;PWNED-TITLE\x07"
OSC52 = ESC + "]52;c;" + base64.b64encode(b"echo PWNED\n").decode() + ESC + "\\"
OSC8 = ESC + "]8;;https://evil.example/" + ESC + "\\click" + ESC + "]8;;" + ESC + "\\"
C1_CSI = "\x9b"
MARKUP = "[bold red]MARKUP[/] [@click=app.quit]clickme[/] [link=https://evil.example]lnk[/link]"


def controls(s: str) -> list[str]:
    """The C0 (other than tab and newline), DEL and C1 characters in ``s``."""
    return [c for c in s if (ord(c) < 0x20 and c not in "\t\n") or 0x7F <= ord(c) < 0xA0]


def any_controls(s: str) -> bool:
    return any(ord(c) < 0x20 or 0x7F <= ord(c) < 0xA0 for c in s)


def evil_values(pwn: str) -> list[str]:
    return [
        "it's \"quoted\" \\ back'slash",
        f"x' ); COPY (SELECT 1) TO '{pwn}'; --",
        OSC_TITLE + "TITLE-VAL",
        OSC52 + "CLIP-VAL",
        OSC8,
        MARKUP,
        "[/]",
        ESC + "[2J" + ESC + "[31mRED",
        C1_CSI + "31mC1" + "\x9d0;C1-TITLE\x07",
        "tab\there\nnew line",
    ]


def evil_completion_name(pwn: str) -> str:
    # what the column-name completion used to insert as is: SQL of its own
    return (f"random() > -1)); COPY (SELECT 'echo PWNED') TO '{pwn}' (HEADER false, QUOTE ''); "
            "SELECT * FROM (SELECT 1 AS x WHERE (1")


def make_evil(tmp_path, name: str = "evil.parquet"):
    """A file whose every displayed string is hostile. Returns (path, values, pwn files)."""
    pwn_completion = str(tmp_path / "pwn_completion.txt")
    pwn_value = str(tmp_path / "pwn_value.txt")
    vals = evil_values(pwn_value)
    n = len(vals)
    cols = {
        "a": pa.array(range(n)),
        "s": pa.array(vals),
        "select": pa.array(vals),  # a keyword
        "[bold]mk[/] [@click=app.quit]x[/]": pa.array(vals),
        "esc" + OSC_TITLE + "name": pa.array(range(n)),
        "c1" + C1_CSI + "2Jname": pa.array(range(n)),
        evil_completion_name(pwn_completion): pa.array(range(n)),
    }
    fields = []
    for k, v in cols.items():
        md = {b"description": ("desc " + OSC_TITLE + C1_CSI + "5m [bold]M[/] [@click=app.quit]q[/]").encode(),
              b"unit": ("u" + ESC + "[5m").encode()}
        fields.append(pa.field(k, v.type, metadata=md))
    schema = pa.schema(fields, metadata={
        ("k" + OSC_TITLE).encode(): ("v " + OSC52 + " [@click=app.quit]x[/]").encode(),
        b"json": b'{"a": "\\u001b]0;JSONTITLE\\u0007", "b": "\\u009b31mJSONC1", "c": "\\u007f"}',
    })
    path = tmp_path / name
    pq.write_table(pa.table(list(cols.values()), schema=schema), path)
    return str(path), vals, (pwn_completion, pwn_value)


async def settle(pilot, app, timeout=20.0):
    await pilot.pause(0.05)
    t = 0.0
    while app._busy or any(w.state in (WorkerState.PENDING, WorkerState.RUNNING) for w in app.workers):
        await pilot.pause(0.05)
        t += 0.05
        if t > timeout:
            raise TimeoutError(f"still busy: {app._busy}")
    await pilot.pause(0.1)


def plain(widget) -> str:
    r = widget.render()
    return str(getattr(r, "plain", r))


def rendered(widget) -> str:
    """The text of what a Static was last updated with (a Rich renderable), as Rich lays it out."""
    from rich.console import Console

    with open(os.devnull, "w") as null:
        console = Console(width=200, record=True, color_system=None, file=null)
        console.print(widget.content)
        return console.export_text()


# --------------------------------------------------------------- 1. terminal escapes
def test_sanitize_shows_controls_visibly():
    from pqx.fmt import has_controls, sanitize

    s = "abc"
    assert sanitize(s) is s  # the usual string: untouched, not copied
    assert sanitize(ESC + "]0;x\x07") == "␛]0;x␇"
    assert sanitize("a\x9bb\x7f\x00") == "a\\x9bb␡␀"
    assert sanitize("a\tb\nc") == "a␉b␊c"
    assert sanitize("a\tb\nc\x1b", keep_ws=True) == "a\tb\nc␛"
    assert sanitize("é ✓ 漢字  ") == "é ✓ 漢字  "  # not printable to Python, but no controls
    for v in evil_values("/x"):
        assert not controls(sanitize(v, keep_ws=True)) and not any_controls(sanitize(v))
    assert has_controls(ESC) and not has_controls("a\tb\n", keep_ws=True) and has_controls("a\tb")
    # bidi controls reorder what's around them, zero-width characters hide: both are shown
    assert sanitize("abc‮dcba") == "abc⟨U+202E⟩dcba"
    for c in "‪‫‬‭‮⁦⁧⁨⁩‎‏؜​‌‍⁠﻿":
        assert sanitize("x" + c, keep_ws=True) == f"x⟨U+{ord(c):04X}⟩" and has_controls(c)


def test_format_value_sanitizes_strings_and_nested_values():
    import pyarrow as pa

    from pqx import fmt as F

    for v in evil_values("/x"):
        for raw in (False, True):
            assert not controls(F.format_value(v, "str", raw=raw, width=0))
        assert not controls(F.format_value(v, "str", override=">30"))
    nested = {"k" + ESC + "]0;K": [ESC + "[2J", {"x" + C1_CSI: "y\x9d"}]}
    for raw in (False, True):
        assert not controls(F.format_value(nested, "nested", raw=raw, width=0))
    assert ESC in F.format_value(ESC + "x", "str", safe=False)
    t = pa.struct([pa.field("f" + ESC + "]0;T", pa.int32())])
    assert not any_controls(F.short_type(t))
    assert not controls(F.CellFormatter("s", pa.string())(OSC52).plain)


def test_pty_terminal_never_receives_file_escapes(tmp_path):
    """Run the real CLI in a pty on a hostile file (hostile name too) through every tab, the
    detail pane, copy and = filtering; no escape sequence from the file may reach the terminal."""
    if not sys.platform.startswith(("linux", "darwin")):
        pytest.skip("needs a pty")
    import fcntl
    import pty
    import select
    import signal
    import struct
    import termios
    import time

    path, _, pwns = make_evil(tmp_path, "evil" + ESC + "]2;FNAME-TITLE\x07" + C1_CSI + ".parquet")
    pid, fd = pty.fork()
    if pid == 0:
        env = dict(os.environ, TERM="xterm-256color")
        os.execve(sys.executable, [sys.executable, "-m", "pqx", path], env)
    fcntl.ioctl(fd, termios.TIOCSWINSZ, struct.pack("HHHH", 50, 200, 0, 0))
    out = bytearray()

    def pump(t, until=None):
        end = time.time() + t
        while time.time() < end:
            if select.select([fd], [], [], 0.05)[0]:
                try:
                    d = os.read(fd, 65536)
                except OSError:
                    return
                if not d:
                    return
                out.extend(d)
            if until is not None and until in out:
                return

    try:
        pump(30, until="rows".encode())
        pump(1.5)
        for keys, wait in [("\x1b[B", 0.4), ("\x1b[B", 0.4), ("d", 1.5), ("\x1b[C", 0.4), ("y", 1.0),
                           ("\x1b[B", 0.4), ("\x1b[B", 0.4), ("\x1b[B", 0.4), ("\x1b[B", 0.4), ("y", 1.0),
                           ("=", 2.5), ("2", 2.0), ("\x1b[B", 0.4), ("\x1b[B", 0.4), ("\x1b[B", 0.4),
                           ("\x1b[B", 0.4), ("5", 2.0), ("3", 3.0), ("1", 1.0), ("c", 1.5), ("\x1b", 0.5),
                           ("e", 1.5), ("\x1b", 0.5)]:
            os.write(fd, keys.encode())
            pump(wait)
    finally:
        os.kill(pid, signal.SIGKILL)
        os.waitpid(pid, 0)
    b = bytes(out)
    assert b"rows" in b, "the app never drew its first frame"
    for needle in [b"\x1b]0;", b"\x1b]2;", b"\x1b]8;", b"\x1b[2J\x1b[31mRED", b"\x1b[5m", b"PWNED-TITLE\x07"]:
        assert needle not in b, needle
    # C1 controls (UTF-8 encoded): pqx itself never writes any
    assert not re.search(rb"\xc2[\x80-\x9f]", b)
    # the only clipboard writes are pqx's own (y), and they carry no control characters either
    for payload in re.findall(rb"\x1b\]52;c;([A-Za-z0-9+/=]*)", b):
        assert not controls(base64.b64decode(payload).decode())
    assert not any(os.path.exists(p) for p in pwns)
    assert "␛".encode() in b  # (and the hostile text was drawn: as visible stand-ins)


async def test_copy_sanitizes_and_says_so(tmp_path):
    path, vals, _ = make_evil(tmp_path)
    app = PqxApp(path)
    async with app.run_test(size=SIZE) as pilot:
        await settle(pilot, app)
        g = app.query_one(GridTable)
        g.move_cursor(row=vals.index(OSC52 + "CLIP-VAL"), column=app.cols_shown.index("s"))
        await pilot.press("y")
        await settle(pilot, app)
        assert app.clipboard and ESC not in app.clipboard and app.clipboard.startswith("␛]52;c;")
        assert "characters copied as visible symbols" in str(list(app._notifications)[-1].message)
        g.move_cursor(row=vals.index("tab\there\nnew line"))
        await pilot.press("y")
        await settle(pilot, app)
        assert app.clipboard == "tab\there\nnew line"  # tab and newline are text, copied as they are
        assert "copied as visible symbols" not in str(list(app._notifications)[-1].message)


async def test_widgets_show_no_control_characters(tmp_path):
    """Everything pqx draws from the file shows control characters visibly."""
    path, vals, _ = make_evil(tmp_path, "f" + ESC + "]0;FN\x07.parquet")
    app = PqxApp(path)
    async with app.run_test(size=SIZE) as pilot:
        await settle(pilot, app)
        title = rendered(app.query_one("#titlebar"))
        assert "␛" in title and not controls(title) and not any_controls(app.title)
        g = app.query_one(GridTable)
        for col in g.ordered_columns:
            assert not controls(col.label.plain)
        for row in range(len(vals)):
            for c in range(len(app.cols_shown)):
                assert not controls(g.get_cell_at(Coordinate(row, c)).text.plain)
        app.query_one(GridTable).move_cursor(row=2, column=app.cols_shown.index("s"))
        await pilot.press("d")
        await settle(pilot, app)
        for name, value in app.query_one("#detail-list")._items:
            assert not controls(value.plain)
        for tab in ["2", "5", "3"]:
            await pilot.press(tab)
            await settle(pilot, app)
            await pilot.press("down")
            await settle(pilot, app)
        for sel in ("#schema-desc", "#meta-overview", "#stats-head", "#stats-summary", "#meta-kv"):
            text = rendered(app.query_one(sel))
            assert not controls(text), (sel, text)
        assert "\\x9b" in rendered(app.query_one("#meta-kv")) and "␛" in rendered(app.query_one("#schema-desc"))
        stats = app.query_one("#stats-cols")
        assert not any(controls(stats.get_option_at_index(i).prompt.plain) for i in range(stats.option_count))


# --------------------------------------------------------------- 2. SQL from names and values
async def test_column_completion_quotes_names(tmp_path):
    from pqx.app import ColumnSuggester

    path, _, (pwn_completion, _) = make_evil(tmp_path)
    app = PqxApp(path)
    async with app.run_test(size=SIZE) as pilot:
        await settle(pilot, app)
        await pilot.press("slash", "r", "a", "n")
        await pilot.pause(0.3)
        await pilot.press("right")
        completed = app.query_one("#filter", Input).value
        await pilot.press("enter")
        await settle(pilot, app)
        assert app.is_running
    assert not os.path.exists(pwn_completion)  # the name's COPY never ran
    assert completed.startswith('"random() > -1))')  # it went in quoted

    sug = ColumnSuggester(["random() > 1); drop", "select", "plain_name", "esc" + ESC + "x"], ["select"])
    assert await sug.get_suggestion("ran") == '"random() > 1); drop"'
    assert await sug.get_suggestion("x > 1 and pla") == "x > 1 and plain_name"
    assert await sug.get_suggestion("sel") == '"select"'
    assert await sug.get_suggestion("es") is None  # never puts a control character in the box


async def test_equals_filter_matches_hostile_values_exactly(tmp_path):
    path, vals, (_, pwn_value) = make_evil(tmp_path)
    app = PqxApp(path)
    async with app.run_test(size=SIZE) as pilot:
        await settle(pilot, app)
        inp = app.query_one("#filter", Input)
        g = app.query_one(GridTable)
        for name in ("s", "select", "[bold]mk[/] [@click=app.quit]x[/]", "esc" + OSC_TITLE + "name",
                     "c1" + C1_CSI + "2Jname"):
            for row in range(len(vals)):
                await pilot.press("ctrl+x")
                await settle(pilot, app)
                g.focus()
                g.move_cursor(row=row, column=app.cols_shown.index(name))
                await pilot.pause(0.05)
                await pilot.press("equals_sign")
                await settle(pilot, app)
                assert not any_controls(inp.value), (name, row, inp.value)
                assert app._last_error == "" and app.total == 1, (name, row, inp.value, app._last_error)
                assert app.page.rows[0][app.cols_shown.index("a")] == row
        assert app.is_running
    assert not os.path.exists(pwn_value)


def test_queries_must_be_one_select(tmp_path):
    import duckdb

    from pqx.data import check_select

    pq.write_table(pa.table({"a": [1, 2, 3], "b": ["x", "y", "x"]}), tmp_path / "t.parquet")
    ds = ParquetDataset(str(tmp_path / "t.parquet"))
    pwn = str(tmp_path / "pwn.txt")
    # Each query wraps the filter in its own parentheses: one of these closes them for each
    # (the others are mere syntax errors). Every one must be refused before it runs.
    bad = [f"a > 0{')' * k}; COPY (SELECT 1) TO '{pwn}'; {'SELECT * FROM (' * (k - 1)}SELECT 1 AS a WHERE (1"
           for k in range(1, 5)]
    calls = {"validate": lambda v: ds.validate(v), "count": lambda v: ds.count(v),
             "fetch": lambda v: ds.fetch(v, 0, 10), "stats": lambda v: ds.column_stats(v, "a"),
             "histogram": lambda v: ds.histogram(v, "a"), "xy": lambda v: ds.xy_counts(v, "a", "a", 4, 4),
             "sky": lambda v: ds.sky_counts(v, "a", "a"),
             "export": lambda v: ds.export(v, str(tmp_path / "out.parquet"))}
    for what, call in calls.items():
        refused = []
        for where in bad:
            with pytest.raises(duckdb.Error) as e:
                call(View(where=where))
            refused.append("only a single SELECT query is allowed here" in str(e.value))
        assert any(refused), what  # (the filter that fits this query's parentheses)
        assert not os.path.exists(pwn), what
    sql = View(sql=f"select * from t; COPY (SELECT 1) TO '{pwn}'")
    with pytest.raises(duckdb.Error):
        ds.validate(sql)
    with pytest.raises(duckdb.Error):  # ) TO ... -- would turn the export's COPY into one of its own
        ds.export(View(sql=f"select * from t) TO '{pwn}' (FORMAT csv) --"), str(tmp_path / "out.csv"), fmt="csv")
    assert not os.path.exists(pwn)
    with pytest.raises(duckdb.Error, match="single SELECT"):
        check_select("CREATE TYPE x AS ENUM ('a'); SELECT 1")
    # what's allowed still works: queries, CTEs, FROM-first, PIVOT (which adds a CREATE TYPE of its own)
    for q in ("select a, count(*) from t group by 1", "with x as (select * from t) select * from x",
              "from t", "pivot t on b using sum(a)", "summarize t"):
        assert ds.validate(View(sql=q)), q
    assert ds.count(View(where="b = 'x'")) == 2


async def test_smuggled_statement_is_refused_in_the_app(tmp_path):
    path, _, _ = make_evil(tmp_path)
    pwn = tmp_path / "pwn_typed.txt"
    app = PqxApp(path)
    async with app.run_test(size=SIZE) as pilot:
        await settle(pilot, app)
        app.query_one("#filter", Input).value = (f"a > 0)); COPY (SELECT 1) TO '{pwn}'; "
                                                 "SELECT * FROM (SELECT 1 AS x WHERE (1")
        await pilot.press("slash", "enter")
        await settle(pilot, app)
        assert app.is_running and "only a single SELECT query is allowed here" in app._last_error
    assert not pwn.exists()


async def test_page_with_unexpected_columns_does_not_crash(tmp_path):
    from pqx.data import Page

    path, _, _ = make_evil(tmp_path)
    app = PqxApp(path)
    async with app.run_test(size=SIZE) as pilot:
        await settle(pilot, app)
        before = app.page
        app._apply_page(Page(0, ["x"], [(1,)], [None], [pa.int32()]), 0, None)
        await settle(pilot, app)
        assert app.is_running and app.page is before and "other columns" in app._last_error


# --------------------------------------------------------------- 3. markup
async def test_duckdb_error_with_markup_is_shown_as_text(tmp_path):
    p = tmp_path / "m.parquet"
    pq.write_table(pa.table({"s": ["[/]", "2"], "c": ["[@click=app.quit]CLICK ME[/]", "2"]}), p)
    app = PqxApp(str(p))
    async with app.run_test(size=(160, 40), notifications=True) as pilot:
        await settle(pilot, app)
        for q, text in [("s::int > 1", "'[/]'"), ("c::int > 1", "[@click=app.quit]CLICK ME[/]")]:
            app.query_one("#filter", Input).value = q
            await pilot.press("slash", "enter")
            await settle(pilot, app)
            assert app.is_running
            note = list(app._notifications)[-1]
            assert text in note.message and not note.markup
            for _ in range(40):
                toasts = list(app.screen.query("Toast"))
                if toasts:
                    break
                await pilot.pause(0.05)
            assert any(text in plain(t) for t in toasts)  # shown literally: not a link, not a crash
            await pilot.press("ctrl+x")
            await settle(pilot, app)


async def test_markup_in_names_and_values_is_shown_as_text(tmp_path):
    path, vals, _ = make_evil(tmp_path)
    p = tmp_path / "[x].parquet"
    pq.write_table(pa.table({"[/]": [1, 2], "b": [3, 4]}), p)
    app = PqxApp(str(p))
    async with app.run_test(size=SIZE) as pilot:  # a column picker with a [/] column
        await settle(pilot, app)
        await pilot.press("c")
        await settle(pilot, app)
        assert type(app.screen).__name__ == "ColumnPicker"
        sl = app.screen.query_one("SelectionList")
        assert sl.get_option_at_index(0).prompt.plain.startswith("[/]")
    app = PqxApp(path)
    name = "[bold]mk[/] [@click=app.quit]x[/]"
    async with app.run_test(size=SIZE) as pilot:  # export summary: sorted by it, filtered on [/]
        await settle(pilot, app)
        g = app.query_one(GridTable)
        g.move_cursor(row=vals.index("[/]"), column=app.cols_shown.index(name))
        await pilot.press("equals_sign")
        await settle(pilot, app)
        await pilot.press("s")
        await settle(pilot, app)
        await pilot.press("e")
        await settle(pilot, app)
        assert type(app.screen).__name__ == "ExportScreen"
        summary = " ".join(plain(lab) for lab in app.screen.query(Label))
        assert f"sorted by {name}" in summary and "'[/]'" in summary


async def test_export_path_with_brackets(tmp_path, monkeypatch):
    d = tmp_path / "d"
    d.mkdir()
    p = d / "x[x=a:b].parquet"
    pq.write_table(pa.table({"a": [1, 2]}), p)
    monkeypatch.chdir(d)
    app = PqxApp(str(p))
    async with app.run_test(size=SIZE, notifications=True) as pilot:
        await settle(pilot, app)
        await pilot.press("e")
        await settle(pilot, app)
        await pilot.press("enter")
        await settle(pilot, app)
        assert app.is_running
        out = d / "x[x=a:b].subset.parquet"
        assert out.exists() and pq.read_table(out).num_rows == 2
        assert any(str(out) in str(n.message) or "x\\[x=a:b]" in str(n.message) for n in app._notifications)
        await pilot.press("e")
        await settle(pilot, app)
        await pilot.press("enter")  # exists: the warning names it (with its brackets)
        await settle(pilot, app)
        assert app.is_running


# --------------------------------------------------------------- 4. odd files
def test_glob_characters_in_the_path_name_one_file(tmp_path):
    pq.write_table(pa.table({"a": [1, 2]}), tmp_path / "a*.parquet")
    pq.write_table(pa.table({"a": [100, 200, 300]}), tmp_path / "ab.parquet")
    pq.write_table(pa.table({"a": [5]}), tmp_path / "x[1].parquet")
    pq.write_table(pa.table({"a": [6, 6]}), tmp_path / "x1.parquet")
    pq.write_table(pa.table({"a": [7]}), tmp_path / "q?.parquet")
    pq.write_table(pa.table({"a": [8, 8]}), tmp_path / "qq.parquet")
    for name, want in [("a*.parquet", [1, 2]), ("x[1].parquet", [5]), ("q?.parquet", [7]), ("ab.parquet", [100, 200, 300])]:
        ds = ParquetDataset(str(tmp_path / name))
        view = View(where="a > 0")
        assert ds.count(view) == len(want), name
        assert [r[0] for r in ds.fetch(view, 0, 10).rows] == want, name


def test_row_number_column_names_do_not_collide(tmp_path):
    p = tmp_path / "rowcol.parquet"
    pq.write_table(pa.table({"__pqx_row": [7, 8], "__PQX_ROW_": [1, 1], "b": [1, 2]}), p)
    ds = ParquetDataset(str(p))
    assert ds.setup_error is None and ds.count(View(where="b > 0")) == 2
    page = ds.fetch(View(where="b > 0", order_by=[("b", True)]), 0, 10)
    assert page.columns == ["__pqx_row", "__PQX_ROW_", "b"] and page.rows == [(8, 1, 2), (7, 1, 1)]
    assert page.row_numbers == [1, 0]
    q = tmp_path / "frn.parquet"
    pq.write_table(pa.table({"FILE_ROW_NUMBER": [9, 9], "b": [1, 2]}), q)
    ds = ParquetDataset(str(q))
    assert ds.count(View(where="b > 1")) == 1 and ds.fetch(View(where="b > 1"), 0, 10).rows == [(9, 2)]


def test_nul_in_a_column_name_fails_with_a_clear_message(tmp_path):
    p = tmp_path / "nul.parquet"
    pq.write_table(pa.table({"a\x00b": [1, 2], "b": [1, 2]}), p)
    ds = ParquetDataset(str(p))
    with pytest.raises(ValueError, match="NUL character") as e:
        ds.fetch(View(where="b > 0"), 0, 10)
    assert "\x00" not in str(e.value)


def test_sql_helpers():
    import duckdb

    from pqx.data import sql_column_ref, sql_ident, sql_text_literal

    assert sql_ident("band") == "band" and sql_ident("select") == '"select"' and sql_ident("a b") == '"a b"'
    con = duckdb.connect()
    for s in evil_values("/x") + ["plain", "", "it''s"]:
        lit = sql_text_literal(s)
        assert not any_controls(lit) and con.execute(f"SELECT {lit}").fetchone()[0] == s
    con.execute(f"CREATE TABLE t AS SELECT 1 AS {sql_ident('esc' + ESC + 'x')}, 2 AS y")
    assert con.execute(f"SELECT y FROM t WHERE {sql_column_ref('esc' + ESC + 'x')} = 1").fetchall() == [(2,)]


def test_cli_prints_no_control_characters(tmp_path, capsys):
    from pqx.cli import main

    assert main([str(tmp_path / ("nope" + ESC + "]0;T\x07.parquet"))]) == 2
    bad = tmp_path / ("bad" + ESC + "]0;T\x07" + C1_CSI + ".parquet")
    bad.write_bytes(b"not a parquet file")
    assert main([str(bad)]) == 1
    err = capsys.readouterr().err
    assert "␛]0;T" in err and not controls(err)


# --------------------------------------------------------------- review follow-ups
def case_dup_file(tmp_path):
    """Columns whose names differ only in case: one name to DuckDB, which calls the second name_1."""
    p = tmp_path / "case.parquet"
    pq.write_table(pa.table({"Name": ["A", "B", "C"], "name": ["a", "b", "c"], "x": [1, 2, 3]}), p)
    return str(p)


def test_case_duplicate_columns_each_show_their_own_data(tmp_path):
    ds = ParquetDataset(case_dup_file(tmp_path))
    assert ds.sql_name("name") == "name_1" and ds.sql_name("Name") == "Name"
    for view in (View(), View(where="x > 1"), View(order_by=[("name", True)])):
        page = ds.fetch(view, 0, 10)
        assert page.columns == ["Name", "name", "x"], view
        assert all(r[0].lower() == r[1] for r in page.rows), (view, page.rows)
        assert [n for n, _ in ds.validate(view)] == ["Name", "name", "x"]
    assert ds.fetch(View(order_by=[("name", True)]), 0, 10).rows[0] == ("C", "c", 3)
    assert ds.fetch_columns([2, 0], ["name"]).rows == [("c",), ("a",)]
    st = ds.column_stats(View(where="x > 0"), "name")
    assert (st.min, st.max) == ("a", "c")


async def test_case_duplicate_columns_in_the_app(tmp_path):
    app = PqxApp(case_dup_file(tmp_path))
    async with app.run_test(size=SIZE) as pilot:
        await settle(pilot, app)
        g = app.query_one(GridTable)
        assert g.row_count == 3 and app._last_error == ""
        assert [(r[0], r[1]) for r in app.page.rows] == [("A", "a"), ("B", "b"), ("C", "c")]
        g.move_cursor(row=1, column=1)
        await pilot.press("equals_sign")  # filters on name (DuckDB's name_1), not on Name
        await settle(pilot, app)
        assert app.query_one("#filter", Input).value == "name_1 = 'b'"
        assert app.total == 1 and app.page.rows[0][:2] == ("B", "b") and app._last_error == ""
        await pilot.press("ctrl+x", "s")  # sort on it
        await settle(pilot, app)
        assert app._last_error == "" and [r[1] for r in app.page.rows] == ["a", "b", "c"]


async def test_trailing_backslash_in_names_is_not_markup(tmp_path):
    p = tmp_path / "bs.parquet"
    name = "dir\\"
    pq.write_table(pa.table({name: [1.5, 2.5], "b": [1, 2]}), p)
    app = PqxApp(str(p))
    async with app.run_test(size=SIZE) as pilot:
        await settle(pilot, app)
        await pilot.press("F")
        await settle(pilot, app)
        assert type(app.screen).__name__ == "FormatScreen"
        labels = [plain(lab) for lab in app.screen.query(Label)]
        assert "Format of dir\\" in labels  # one backslash, and no [/cyan] leaking out
        await pilot.press("escape")
        await settle(pilot, app)
        await pilot.press("minus")
        await settle(pilot, app)
        note = list(app._notifications)[-1]
        assert note.message == "Hid dir\\ · c brings it back" and not note.markup


async def test_long_json_metadata_is_cut(tmp_path):
    import json

    p = tmp_path / "kv.parquet"
    big = json.dumps({f"k{i}": "v" * 50 for i in range(500)})
    pq.write_table(pa.table({"a": [1]}).replace_schema_metadata({"big": big}), p)
    app = PqxApp(str(p))
    async with app.run_test(size=SIZE) as pilot:
        await settle(pilot, app)
        text = rendered(app.query_one("#meta-kv"))
        assert len(text) < 6000 and "…" in text
