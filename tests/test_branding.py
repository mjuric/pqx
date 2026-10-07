"""Filter-box hint from the file's own data."""
import pyarrow as pa
import pyarrow.parquet as pq
from textual.widgets import Input

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

