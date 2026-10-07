
import duckdb
import pyarrow.parquet as pq
import pytest

from pqx.data import ParquetDataset, View, guess_sky_columns, is_sql_query, parse_row_spec


@pytest.fixture(scope="module")
def ds(demo_path):
    return ParquetDataset(demo_path)


@pytest.fixture(scope="module")
def truth(demo_path):
    return pq.read_table(demo_path).to_pandas()


def test_open_and_schema(ds):
    assert ds.num_rows == 20_000
    assert ds.column_names[:3] == ["diaSourceId", "ssObjectId", "ra"]
    assert ds.column("ra").unit == "deg"
    assert ds.column("ra").description.startswith("Right ascension")
    assert ds.meta.num_row_groups == 8


@pytest.mark.parametrize("offset", [0, 2_499, 2_500, 12_345, 19_990])
def test_seek_windows_match_file(ds, truth, offset):
    page = ds.fetch(View(), offset, 50)
    n = min(50, 20_000 - offset)
    assert len(page.rows) == n
    assert page.row_numbers == list(range(offset, offset + n))
    ids = [r[0] for r in page.rows]
    assert ids == truth["diaSourceId"].iloc[offset:offset + n].tolist()


def test_filter_sort_count(ds, truth):
    v = View(where="mag < 20 and band = 'r'", order_by=[("mag", True)])
    exp = truth[(truth.mag < 20) & (truth.band == "r")].sort_values("mag", ascending=False)
    assert ds.count(v) == len(exp)
    page = ds.fetch(v, 0, 10, ["diaSourceId", "mag"])
    assert [r[0] for r in page.rows] == exp.diaSourceId.iloc[:10].tolist()
    # row numbers point back into the file
    assert page.row_numbers[0] == int(exp.index[0])
    assert ds.find_row(v, page.row_numbers[3]) == 3


def test_find_row(ds, truth):
    v = View(where="band = 'r'")  # unsorted: a count of the matching rows before it
    exp = list(truth.index[truth.band == "r"])
    for pos in (0, 1, 999, 1000, len(exp) - 1):
        assert ds.find_row(v, exp[pos]) == pos
    assert ds.find_row(v, int(truth.index[truth.band != "r"][5])) is None  # not in the view
    v = View(where="band = 'r'", order_by=[("mag", False)])  # sorted: numbered
    exp = list(truth[truth.band == "r"].sort_values("mag", kind="stable").index)
    for pos in (0, 1234, len(exp) - 1):
        assert ds.find_row(v, exp[pos]) == pos
    assert ds.find_row(View(), 777) == 777


def test_sql_mode(ds, truth):
    v = View(sql="select band, count(*) as n from t group by band order by band")
    schema = ds.validate(v)
    assert [n for n, _ in schema] == ["band", "n"]
    page = ds.fetch(v, 0, 100)
    assert dict(page.rows) == truth.band.value_counts().to_dict()
    assert page.row_numbers == [None] * 6
    assert ds.count(v) == 6
    assert ds.fetch(v, 0, 10, ["n"]).columns == ["n"]


def test_bad_filter_raises(ds):
    with pytest.raises(duckdb.Error):
        ds.validate(View(where="nosuchcolumn > 3"))
    with pytest.raises(duckdb.Error):
        ds.validate(View(where="mag <"))


def test_column_stats(ds, truth):
    st = ds.column_stats(View(), "psfFlux")
    col = truth.psfFlux
    assert st.count == 20_000
    assert st.nans == int(col.isna().sum())  # NaNs (pandas reads NaN as NA for float)
    assert st.min == pytest.approx(col.min(), rel=1e-6)
    assert st.max == pytest.approx(col.max(), rel=1e-6)
    assert st.mean == pytest.approx(col.mean(), rel=1e-4)
    assert 0.25 in st.quantiles

    st = ds.column_stats(View(), "ssObjectId")
    assert st.nulls == int(truth.ssObjectId.isna().sum())

    st = ds.column_stats(View(), "band")
    assert st.distinct == 6 and len(st.top) == 6

    st = ds.column_stats(View(where="band = 'g'"), "band")
    assert st.top == [("g", int((truth.band == "g").sum()))]


def test_histogram(ds, truth):
    edges, counts = ds.histogram(View(), "mag", bins=20)
    assert len(edges) == 21 and len(counts) == 20
    assert sum(counts) == truth.mag.notna().sum()
    edges, counts = ds.histogram(View(), "ingestTime", bins=10, temporal=True)
    assert sum(counts) == 20_000
    _, counts = ds.histogram(View(), "psfFlux", bins=10, log=True)
    assert sum(counts) == (truth.psfFlux > 0).sum()


def test_sky_and_xy_counts(ds):
    g = ds.sky_counts(View(), "ra", "dec", res_deg=1.0)
    assert g.shape == (180, 360) and g.sum() == 20_000
    g2 = ds.sky_counts(View(where="dec > 0"), "ra", "dec", res_deg=1.0)
    assert g2[:90].sum() == 0 and g2.sum() > 0
    grid, xl, yl = ds.xy_counts(View(), "mag", "snr", 40, 20)
    assert grid.shape == (20, 40) and 19_000 < grid.sum() <= 20_000


def test_sampling(ds):
    cond = ds.sample_condition(5_000)
    assert "__pqx_row" in cond
    st = ds.column_stats(View(), "mag", sample=5_000)
    assert st.sampled and 4_000 <= st.count <= 5_100
    assert ds.sample_condition(10**9) == ""


def test_export_roundtrip(ds, tmp_path, truth):
    v = View(where="band = 'i'", order_by=[("mag", False)])
    out = tmp_path / "sub.parquet"
    n = ds.export(v, str(out), columns=["diaSourceId", "mag"])
    t = pq.read_table(out).to_pandas()
    assert n == len(t) == int((truth.band == "i").sum())
    assert list(t.columns) == ["diaSourceId", "mag"]
    assert t.mag.is_monotonic_increasing
    ds.export(View(sql="select band, count(*) n from t group by 1"), str(tmp_path / "agg.csv"), fmt="csv")
    assert (tmp_path / "agg.csv").read_text().startswith("band,n")
    with pytest.raises(ValueError):
        ds.export(View(), ds.path)


def test_timestamps_in_utc(ds):
    page = ds.fetch(View(), 0, 1, ["ingestTime"])
    assert page.rows[0][0].utcoffset().total_seconds() == 0
    st = ds.column_stats(View(where="mag < 21"), "ingestTime")
    assert st.min.utcoffset().total_seconds() == 0


def test_metadata(ds):
    rgs = ds.row_groups()
    assert len(rgs) == 8 and rgs[1]["start"] == 2_500
    summ = {d["path"]: d for d in ds.column_chunk_summary()}
    assert summ["dec"]["min"] < -80
    assert ds.key_value_metadata()["table"] == "DiaSource"


def test_odd_file(odd_path):
    ds = ParquetDataset(odd_path)
    assert not ds._has_rownum  # file has its own file_row_number column → OFFSET fallback
    page = ds.fetch(View(), 950, 100)
    assert len(page.rows) == 50 and page.row_numbers[0] == 950
    assert page.rows[0][0] == 9_500
    assert ds.count(View(where='"weird name" = 1')) > 0
    st = ds.column_stats(View(), "x")
    assert st.nans == 20 and st.max == float("inf")
    for col in ("tags", "pos", "blob", "day"):
        ds.column_stats(View(), col)  # must not raise
    assert ds.fetch(View(where="len(tags) = 2"), 0, 5).rows[0][3] == ["a", "b"]


def test_helpers():
    assert guess_sky_columns(["id", "ra", "dec", "raErr"]) == ("ra", "dec")
    assert guess_sky_columns(["coord_ra", "coord_dec"]) == ("coord_ra", "coord_dec")
    assert guess_sky_columns(["x", "y"]) == (None, None)
    assert is_sql_query("SELECT 1") and is_sql_query("  with a as (select 1) select * from a")
    assert not is_sql_query("selected > 3")
    assert parse_row_spec("1.5M", 10**7) == 1_500_000
    assert parse_row_spec("50%", 1000) == 500
    assert parse_row_spec("-1", 1000) == 999
    assert parse_row_spec("1,234", 10**6) == 1234
    assert parse_row_spec("10k", 100) == 99
    with pytest.raises(ValueError):
        parse_row_spec("abc", 10)


def test_duckdb_setup_in_background(demo_path):
    """DuckDB parses the footer and creates its views on a thread; queries wait for that."""
    ds = ParquetDataset(demo_path)
    assert ds.cursor().execute("SELECT current_setting('parquet_metadata_cache')").fetchone()[0] is True
    assert ds.count(View(where="mag < 20")) > 0
    assert ds._duck_types(wait=True) is not None


def test_duckdb_setup_failure_is_reported_on_use(demo_path, monkeypatch):
    def boom(self):
        raise duckdb.IOException("IO Error: cannot read it")

    monkeypatch.setattr(ParquetDataset, "_create_views", boom)
    ds = ParquetDataset(demo_path)  # pyarrow reads it: opening works
    assert ds._duck_types(wait=True) is None
    with pytest.raises(duckdb.IOException, match="cannot read it"):
        ds.count(View(where="mag < 20"))
    with pytest.raises(duckdb.IOException):
        ds.fetch(View(), 0, 10)


def test_unreadable_file_raises(tmp_path):
    p = tmp_path / "bad.parquet"
    p.write_bytes(b"not a parquet file at all")
    with pytest.raises(Exception):
        ParquetDataset(str(p))


def test_init_failure_after_footer_parse_stops_setup_thread(demo_path, monkeypatch):
    """__init__ failing after pyarrow opened the file mustn't leave DuckDB's setup thread waiting (exit would stall)."""
    import time

    def boom(f):
        raise ValueError("bad field metadata")

    started = []
    orig = ParquetDataset._start_types

    def start(self):
        orig(self)
        started.append(self._types_thread)

    monkeypatch.setattr(ParquetDataset, "_column_info", staticmethod(boom))
    monkeypatch.setattr(ParquetDataset, "_start_types", start)
    t0 = time.perf_counter()
    with pytest.raises(ValueError, match="bad field metadata"):
        ParquetDataset(demo_path)
    assert time.perf_counter() - t0 < 5
    assert started and not started[0].is_alive()


@pytest.mark.parametrize("where, match", [("band = 'r'", lambda t: t.band == "r"),
                                          ("detector = 7", lambda t: t.detector == 7),  # (sparse)
                                          ("mag < 30", lambda t: t.mag < 30)])
def test_fetch_around_matches_fetch(ds, truth, where, match):
    v = View(where=where)
    rows = list(truth.index[match(truth)])
    for pos in sorted({min(p, len(rows) - 1) for p in (0, 3, 400, len(rows) // 2, len(rows) - 1)}):
        fr = int(rows[pos])
        for offset in (pos, max(0, pos - 150), max(0, pos - 299), max(0, pos - 999)):
            got = ds.fetch_around(v, fr, pos, offset, 300, ["diaSourceId", "mag"])
            exp = ds.fetch(v, offset, 300, ["diaSourceId", "mag"])
            assert got.offset == offset and got.row_numbers == exp.row_numbers and got.rows == exp.rows


@pytest.mark.parametrize("bad", ["detector = 3) OR (band = 'r'", "(detector = 3", "detector = 3)",
                                 "detector = 3)) OR ((band = 'r'"])
def test_filters_cannot_escape_their_parentheses(ds, truth, bad):
    """``x) OR (y`` parses on its own (WHERE (x) OR (y)), but next to pqx's own conditions it
    would mean something else: such filters are refused everywhere they'd be spliced in."""
    v = View(where=bad)
    with pytest.raises(duckdb.ParserException, match="unbalanced parentheses"):
        ds.validate(v)
    for call in (lambda: ds.count(v), lambda: ds.fetch(v, 0, 10), lambda: ds.find_row(v, 5),
                 lambda: ds.fetch_around(v, 5, 0, 0, 150), lambda: ds.relation_sql(v)):
        with pytest.raises(duckdb.ParserException, match="unbalanced parentheses"):
            call()


@pytest.mark.parametrize("where", ["band = ')' or detector = 3", "detector = 3 -- )", "detector = 3 /* ( */",
                                   "(detector = 3) or (band = 'r')", '"detector" = 3 or "band" = \'(\''])
def test_parentheses_in_literals_and_comments_are_fine(ds, where):
    v = View(where=where)
    ds.validate(v)
    n = ds.count(v)
    rows = ds.fetch(v, 0, n, ["diaSourceId"]).row_numbers
    for pos in sorted({0, len(rows) // 2, len(rows) - 1}):
        got = ds.fetch_around(v, rows[pos], pos, max(0, pos - 75), 150, ["diaSourceId"])
        assert len(got.rows) <= 150 and got.row_numbers == rows[got.offset:got.offset + 150]
        assert ds.find_row(v, rows[pos]) == pos
