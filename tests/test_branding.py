"""Filter-box hint from the file's own data; version, help and title-bar branding."""
import pyarrow as pa
import pyarrow.parquet as pq
import pytest
from textual.widgets import Input, Label, Static

from pqx import __version__, cli
from pqx.screens import HELP, HelpScreen
from pqx.app import FILTER_EXAMPLE, PqxApp, filter_placeholder
from pqx.cells import MISSING
from pqx.fmt import sanitize

from test_app import SIZE, settle

I, D, S = pa.int64(), pa.float64(), pa.string()


def test_placeholder_from_first_row():
    ph = filter_placeholder(["vendor", "trip_distance", "fare"], [S, D, D], ("VeriFone", 2.8213, 9.5))
    assert ph == ("SQL WHERE expression, e.g. trip_distance > 2.82 and vendor = 'VeriFone'"
                  " — or a full query: select … from t")


def test_placeholder_skips_nulls_odd_names_and_quotes():
    ph = filter_placeholder(
        ["a b", "x", "y", "select", "s", "t"],
        [D, D, D, S, S, S],
        (1.0, None, 1234567.8, "zz", None, "it's\ta very long value"))
    # nulls and names that aren't plain identifiers are passed over; strings sanitized, cut, quoted
    assert ph.split("e.g. ")[1].split(" — ")[0] == f"y > 1234568 and t = 'it''s{sanitize(chr(9))}a very long val'"


def test_placeholder_fallbacks():
    assert FILTER_EXAMPLE in filter_placeholder([], [], None)
    assert FILTER_EXAMPLE in filter_placeholder(["b"], [pa.bool_()], (True,))
    only_num = filter_placeholder(["n"], [I], (7,))
    assert "e.g. n > 7 —" in only_num
    only_str = filter_placeholder(["s", "n"], [S, D], ("x", float("nan")))
    assert "e.g. s = 'x' —" in only_str
    assert "e.g. s = 'x'" in filter_placeholder(["n", "s"], [D, S], (MISSING, "x"))


async def test_placeholder_set_from_first_page(tmp_path):
    p = tmp_path / "trips.parquet"
    pq.write_table(pa.table({"vendor": [None, "CMT"], "VendorID": [None, 2], "trip_distance": [2.82, 1.0],
                             "payment": ["card", "cash"]}), p)
    app = PqxApp(str(p))
    async with app.run_test(size=SIZE) as pilot:
        await settle(pilot, app)
        ph = app.query_one("#filter", Input).placeholder
        assert "e.g. trip_distance > 2.82 and payment = 'card'" in ph
        app.load_window(1, 1)  # later pages don't change it
        await settle(pilot, app)
        assert app.query_one("#filter", Input).placeholder == ph


def test_version_gnu_style(capsys):
    with pytest.raises(SystemExit) as e:
        cli.main(["--version"])
    assert e.value.code == 0
    out = capsys.readouterr().out
    assert out.splitlines() == [
        f"pqx {__version__}",
        "Copyright (C) 2026 Mario Juric",
        "License BSD-3-Clause: <https://opensource.org/license/bsd-3-clause>",
        "This is free software: you are free to change and redistribute it.",
        "There is NO WARRANTY, to the extent permitted by law.",
        "",
        "Written by Mario Juric.",
    ]


def test_help_epilog(capsys, monkeypatch):
    monkeypatch.setenv("COLUMNS", "80")
    with pytest.raises(SystemExit):
        cli.main(["--help"])
    out = capsys.readouterr().out
    assert out.startswith("usage: pqx ")
    assert "--format COL=SPEC" in out and "--version" in out
    assert out.rstrip().endswith(
        "Examples:\n"
        "  pqx trips.parquet\n"
        "  pqx trips.parquet -w \"payment_type = 'card'\"\n"
        "  pqx trips.parquet -w \"select vendor, count(*) from t group by 1\"\n"
        "\n"
        "Inside pqx, press ? for keys.\n"
        "\n"
        "Report bugs at: <https://github.com/mjuric/pqx/issues>\n"
        "pqx home page: <https://github.com/mjuric/pqx>\n"
        "Written by Mario Juric.")
    assert max(len(line) for line in out.splitlines()) <= 80   # description and options still wrap


async def test_titlebar_and_help_show_version(demo_path):
    app = PqxApp(demo_path)
    async with app.run_test(size=SIZE) as pilot:
        await settle(pilot, app)
        title = app.query_one("#titlebar", Static).render()
        assert title.plain.startswith(f"pqx {__version__}  ·  demo.parquet  ·  ")
        start = title.plain.index(__version__)
        assert any(s.start <= start and s.end >= start + len(__version__) and str(s.style) == app.dim
                   for s in title.spans)   # the version is dimmed
        await pilot.press("question_mark")
        assert isinstance(app.screen, HelpScreen)
        head = app.screen.query_one("#help-head", Label).render().plain
        assert f"pqx {__version__}" in head and "Written by Mario Juric" in head
        assert "https://github.com/mjuric/pqx" in head
        assert FILTER_EXAMPLE in HELP and "mag < 21" not in HELP   # a general example, not an astronomy one
