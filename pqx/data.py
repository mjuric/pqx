"""Data access layer: a single Parquet file behind DuckDB + PyArrow.

Everything is lazy. The file is never loaded in full; the grid pulls small
windows of rows, statistics and plots push aggregation down into DuckDB.

Two access paths:

* **Unfiltered, unsorted** windows seek straight to a row range with DuckDB's
  ``file_row_number`` pushdown, which prunes row groups (a 200-row window deep
  into a multi-GB file costs ~20 ms).
* **Filtered / sorted / SQL** windows run the query with ``LIMIT/OFFSET``.

The user-visible table is registered as the view ``t`` so filters are plain
SQL ``WHERE`` expressions and free-form queries can say ``SELECT ... FROM t``.
"""
from __future__ import annotations

import math
import os
import re
import threading
from contextlib import contextmanager
from dataclasses import dataclass, field
from typing import Any, Iterable

import duckdb
import pyarrow as pa
import pyarrow.parquet as pq

ROWNUM = "__pqx_row"
TABLE = "t"

_SQL_START = re.compile(r"^\s*(select|with|from|pivot|unpivot|describe|summarize)\b", re.I)


def quote_ident(name: str) -> str:
    return '"' + name.replace('"', '""') + '"'


def quote_str(s: str) -> str:
    return "'" + s.replace("'", "''") + "'"


def is_sql_query(text: str) -> bool:
    """True when ``text`` is a full query rather than a WHERE expression."""
    return bool(_SQL_START.match(text or ""))


@dataclass
class ColumnInfo:
    name: str
    arrow_type: pa.DataType
    nullable: bool = True
    description: str = ""
    unit: str = ""

    @property
    def is_numeric(self) -> bool:
        t = self.arrow_type
        return pa.types.is_integer(t) or pa.types.is_floating(t) or pa.types.is_decimal(t)

    @property
    def is_float(self) -> bool:
        return pa.types.is_floating(self.arrow_type)

    @property
    def is_temporal(self) -> bool:
        t = self.arrow_type
        return pa.types.is_timestamp(t) or pa.types.is_date(t) or pa.types.is_time(t)

    @property
    def is_nested(self) -> bool:
        return pa.types.is_nested(self.arrow_type)


@dataclass
class View:
    """What the grid is showing: a filter, a sort, or a full SQL query."""

    where: str = ""
    order_by: list[tuple[str, bool]] = field(default_factory=list)  # (col, descending)
    sql: str = ""  # full query over ``t``; overrides where

    @property
    def is_trivial(self) -> bool:
        return not (self.where.strip() or self.order_by or self.sql.strip())


@dataclass
class Page:
    offset: int
    columns: list[str]
    rows: list[tuple]
    row_numbers: list[int | None]  # file row number per row (None for SQL results)
    types: list[pa.DataType]


@dataclass
class ColumnStats:
    name: str
    count: int = 0
    nulls: int = 0
    nans: int | None = None
    distinct: int | None = None
    min: Any = None
    max: Any = None
    mean: float | None = None
    std: float | None = None
    quantiles: dict[float, float] = field(default_factory=dict)
    top: list[tuple[Any, int]] = field(default_factory=list)
    sampled: bool = False


class ParquetDataset:
    """One Parquet file opened for exploration."""

    def __init__(self, path: str, *, threads: int | None = None):
        self.path = os.path.abspath(os.path.expanduser(path))
        if not os.path.exists(self.path):
            raise FileNotFoundError(self.path)
        self.pf = pq.ParquetFile(self.path)
        self.meta = self.pf.metadata
        self.arrow_schema: pa.Schema = self.pf.schema_arrow
        self.num_rows: int = self.meta.num_rows
        self.file_size = os.path.getsize(self.path)
        self.columns = [self._column_info(f) for f in self.arrow_schema]
        self._by_name = {c.name: c for c in self.columns}
        self._lock = threading.Lock()
        self._tls = threading.local()
        self._tagged: dict[str, duckdb.DuckDBPyConnection] = {}
        self.con = duckdb.connect(":memory:")
        if threads:
            self.con.execute(f"SET threads={int(threads)}")
        src = f"read_parquet({quote_str(self.path)}, file_row_number=true)"
        # file_row_number collides if the file already has such a column; then
        # fall back to OFFSET-based seeking everywhere.
        self._has_rownum = "file_row_number" not in self._by_name
        if self._has_rownum:
            self.con.execute(
                f"CREATE VIEW __pqx_src AS SELECT * RENAME (file_row_number AS {ROWNUM}) FROM {src}"
            )
            self.con.execute(f"CREATE VIEW {TABLE} AS SELECT * EXCLUDE ({ROWNUM}) FROM __pqx_src")
        else:
            self.con.execute(
                f"CREATE VIEW {TABLE} AS SELECT * FROM read_parquet({quote_str(self.path)})"
            )

    # ------------------------------------------------------------------ schema
    @staticmethod
    def _column_info(f: pa.Field) -> ColumnInfo:
        md = {k.decode(errors="replace"): v.decode(errors="replace") for k, v in (f.metadata or {}).items()}
        desc = md.get("description") or md.get("doc") or md.get("comment") or ""
        unit = md.get("unit") or md.get("units") or ""
        m = re.match(r"^\s*\[([^\]]*)\]\s*(.*)$", desc)  # "[unit] description" (felis style)
        if m and not unit:
            unit, desc = m.group(1), m.group(2)
        return ColumnInfo(f.name, f.type, f.nullable, desc, unit)

    def column(self, name: str) -> ColumnInfo:
        return self._by_name[name]

    @property
    def column_names(self) -> list[str]:
        return [c.name for c in self.columns]

    def cursor(self) -> duckdb.DuckDBPyConnection:
        """A fresh cursor (DuckDB connections are not thread-safe).

        Inside ``with ds.tagged(name):`` the cursor is registered under
        ``name`` and any older query under the same tag is interrupted, so a
        superseded count/stats/plot never keeps burning CPU in the background."""
        c = self.con.cursor()
        tag = getattr(self._tls, "tag", None)
        if tag:
            with self._lock:
                old = self._tagged.get(tag)
                self._tagged[tag] = c
            if old is not None and old is not c:
                _interrupt(old)
        return c

    @contextmanager
    def tagged(self, tag: str):
        prev = getattr(self._tls, "tag", None)
        self._tls.tag = tag
        try:
            yield
        finally:
            self._tls.tag = prev

    def interrupt(self, tag: str | None = None) -> None:
        """Interrupt running queries (all tags, or one)."""
        with self._lock:
            curs = [self._tagged.get(tag)] if tag else list(self._tagged.values())
        for c in curs:
            if c is not None:
                _interrupt(c)

    # --------------------------------------------------------------- metadata
    def column_chunk_summary(self) -> list[dict]:
        """Per-column storage + statistics aggregated over all row groups."""
        md = self.meta
        out: dict[str, dict] = {}
        paths = [md.schema.column(i).path for i in range(md.num_columns)]
        for rg in range(md.num_row_groups):
            g = md.row_group(rg)
            for i in range(g.num_columns):
                c = g.column(i)
                d = out.setdefault(
                    paths[i],
                    dict(path=paths[i], physical=c.physical_type, compression=c.compression,
                         encodings=set(), compressed=0, uncompressed=0, min=None, max=None,
                         nulls=0, has_stats=True),
                )
                d["compressed"] += c.total_compressed_size
                d["uncompressed"] += c.total_uncompressed_size
                d["encodings"].update(c.encodings)
                st = c.statistics
                if st is None or not st.has_min_max:
                    d["has_stats"] = d["has_stats"] and st is not None and st.has_null_count
                else:
                    try:
                        d["min"] = st.min if d["min"] is None else min(d["min"], st.min)
                        d["max"] = st.max if d["max"] is None else max(d["max"], st.max)
                    except TypeError:
                        pass
                if st is not None and st.has_null_count:
                    d["nulls"] += st.null_count
        for i in range(md.num_columns):
            sc = md.schema.column(i)
            if sc.path in out:
                out[sc.path]["logical"] = str(sc.logical_type) if sc.logical_type else ""
        return list(out.values())

    def row_groups(self) -> list[dict]:
        if getattr(self, "_rg_cache", None) is not None:
            return self._rg_cache
        rows = []
        start = 0
        for i in range(self.meta.num_row_groups):
            g = self.meta.row_group(i)
            comp = sum(g.column(j).total_compressed_size for j in range(g.num_columns))
            rows.append(dict(index=i, start=start, rows=g.num_rows, compressed=comp,
                             uncompressed=g.total_byte_size))
            start += g.num_rows
        self._rg_cache = rows
        return rows

    def key_value_metadata(self) -> dict[str, str]:
        kv = self.meta.metadata or {}
        return {k.decode(errors="replace"): v.decode(errors="replace") for k, v in kv.items()}

    # ------------------------------------------------------------ query build
    def _base_sql(self, view: View, columns: list[str] | None, with_rownum: bool) -> str:
        if view.sql.strip():
            return view.sql.strip().rstrip(";")
        cols = ", ".join(quote_ident(c) for c in (columns or self.column_names))
        if with_rownum and self._has_rownum:
            cols = f"{ROWNUM}, {cols}"
            src = "__pqx_src"
        else:
            src = TABLE
        sql = f"SELECT {cols} FROM {src}"
        if view.where.strip():
            sql += f" WHERE ({view.where})"
        if view.order_by:
            keys = [f"{quote_ident(c)} {'DESC' if d else 'ASC'} NULLS LAST" for c, d in view.order_by]
            if with_rownum and self._has_rownum:
                keys.append(ROWNUM)  # stable order
            sql += " ORDER BY " + ", ".join(keys)
        return sql

    def relation_sql(self, view: View, columns: list[str] | None = None, *, sample: int | None = None) -> str:
        """SQL for the current view's rows (no row number), for stats/plots/export.

        ``sample`` (a target row count) restricts a plain-file view to evenly
        spaced contiguous slices of the file, selected through the
        ``file_row_number`` pushdown so only those row groups are read."""
        if view.sql.strip():
            base = view.sql.strip().rstrip(";")
            if sample:
                base = f"SELECT * FROM ({base}) USING SAMPLE reservoir({int(sample)} ROWS) REPEATABLE (42)"
            if columns:
                cols = ", ".join(quote_ident(c) for c in columns)
                return f"SELECT {cols} FROM ({base})"
            return base
        cond = self.sample_condition(sample)
        if cond:
            cols = ", ".join(quote_ident(c) for c in (columns or self.column_names))
            sql = f"SELECT {cols} FROM __pqx_src WHERE {cond}"
            if view.where.strip():
                sql += f" AND ({view.where})"
            return sql
        v = View(where=view.where)  # order is irrelevant for aggregates
        return self._base_sql(v, columns, with_rownum=False)

    def sample_condition(self, sample: int | None, slices: int = 16) -> str:
        """``file_row_number`` predicate selecting ~``sample`` rows for sampling.

        Takes up to ``slices`` runs, each starting at the beginning of an evenly
        spaced row group, so DuckDB prunes (never reads) every other row group
        — the win on IO-bound storage where a full column scan is slow."""
        n = self.num_rows
        if not sample or not self._has_rownum or sample >= n:
            return ""
        rgs = self.row_groups()
        k = max(1, min(slices, len(rgs)))
        chunk = max(1, -(-sample // k))
        picks = sorted({round(i * (len(rgs) - 1) / max(k - 1, 1)) for i in range(k)})
        ranges = [(rgs[i]["start"], rgs[i]["start"] + min(chunk, rgs[i]["rows"])) for i in picks]
        return "(" + " OR ".join(f"({ROWNUM} >= {a} AND {ROWNUM} < {b})" for a, b in ranges) + ")"

    def validate(self, view: View) -> list[tuple[str, pa.DataType]]:
        """Bind the query without running it; returns its output schema.

        Raises ``duckdb.Error`` with a user-presentable message on failure."""
        cur = self.cursor()
        rel = cur.sql(f"SELECT * FROM ({self._base_sql(view, None, False)}) LIMIT 0")
        tbl = rel.arrow()
        if isinstance(tbl, pa.RecordBatchReader):
            tbl = tbl.read_all()
        return [(f.name, f.type) for f in tbl.schema]

    def count(self, view: View) -> int:
        if view.is_trivial:
            return self.num_rows
        if view.sql.strip():
            sql = f"SELECT count(*) FROM ({self._base_sql(view, None, False)})"
        else:
            sql = f"SELECT count(*) FROM {TABLE}"
            if view.where.strip():
                sql += f" WHERE ({view.where})"
        return int(self.cursor().execute(sql).fetchone()[0])

    def fetch(self, view: View, offset: int, limit: int, columns: list[str] | None = None) -> Page:
        """Fetch ``limit`` rows of the view starting at ``offset``."""
        offset = max(0, int(offset))
        limit = max(0, int(limit))
        cur = self.cursor()
        is_sql = bool(view.sql.strip())
        if view.is_trivial and self._has_rownum:
            cols = ", ".join(quote_ident(c) for c in (columns or self.column_names))
            sql = (f"SELECT {ROWNUM}, {cols} FROM __pqx_src WHERE {ROWNUM} >= {offset} "
                   f"AND {ROWNUM} < {offset + limit} ORDER BY {ROWNUM}")
        else:
            base = self._base_sql(view, columns, with_rownum=not is_sql)
            sel = ", ".join(quote_ident(c) for c in columns) if (is_sql and columns) else "*"
            sql = f"SELECT {sel} FROM ({base}) LIMIT {limit} OFFSET {offset}"
        tbl = cur.execute(sql).arrow()
        if isinstance(tbl, pa.RecordBatchReader):
            tbl = tbl.read_all()
        names = tbl.column_names
        if names and names[0] == ROWNUM:
            rn = tbl.column(0).to_pylist()
            tbl = tbl.drop_columns([ROWNUM])
        elif not is_sql and view.is_trivial:
            rn = list(range(offset, offset + tbl.num_rows))
        else:
            rn = [None] * tbl.num_rows
        cols = [tbl.column(i).to_pylist() for i in range(tbl.num_columns)]
        rows = list(zip(*cols)) if cols else [() for _ in range(tbl.num_rows)]
        return Page(offset, tbl.column_names, rows, rn, [f.type for f in tbl.schema])

    def find_row(self, view: View, file_row: int) -> int | None:
        """Position of file row ``file_row`` within a filtered/sorted view."""
        if view.is_trivial:
            return file_row
        if view.sql.strip() or not self._has_rownum:
            return None
        base = self._base_sql(view, [self.column_names[0]], with_rownum=True)
        sql = (f"SELECT pos FROM (SELECT {ROWNUM}, row_number() OVER () - 1 AS pos "
               f"FROM ({base})) WHERE {ROWNUM} = {int(file_row)}")
        r = self.cursor().execute(sql).fetchone()
        return None if r is None else int(r[0])

    # ------------------------------------------------------------------ stats
    def column_stats(self, view: View, name: str, *, sample: int | None = None, top_k: int = 10,
                     result_types: dict[str, pa.DataType] | None = None) -> ColumnStats:
        typ = (result_types or {}).get(name)
        info = self._by_name.get(name)
        if typ is None and info is not None:
            typ = info.arrow_type
        ci = ColumnInfo(name, typ if typ is not None else pa.string())
        q = quote_ident(name)
        rel = f"(SELECT {q} AS v FROM ({self.relation_sql(view, sample=sample)}))"
        cur = self.cursor()
        st = ColumnStats(name, sampled=bool(sample))
        numeric = ci.is_numeric and not pa.types.is_decimal(ci.arrow_type)
        # NaN poisons min/max/avg (and makes stddev raise), so aggregate a
        # NaN-free copy and count the NaNs separately.
        w = "CASE WHEN isnan(v) THEN NULL ELSE v END" if ci.is_float else "v"
        wf = "CASE WHEN isfinite(v) THEN v END" if ci.is_float else "v"  # ±inf breaks mean/std
        aggs = ["count(*)", "count(v)"]
        aggs += ["NULL", "NULL"] if ci.is_nested else [f"min({w})", f"max({w})"]
        if not ci.is_nested and not pa.types.is_boolean(ci.arrow_type):
            aggs.append("approx_count_distinct(v)")
        else:
            aggs.append("NULL")
        qs = (0.01, 0.05, 0.25, 0.5, 0.75, 0.95, 0.99)
        if numeric:
            aggs += [f"avg({wf})", f"stddev_samp({wf})"]
            aggs.append("count(*) FILTER (WHERE isnan(v))" if ci.is_float else "NULL")
            aggs.append(f"approx_quantile({wf}, [{', '.join(map(str, qs))}])")
        r = cur.execute(f"SELECT {', '.join(aggs)} FROM {rel}").fetchone()
        st.count, nonnull = int(r[0]), int(r[1])
        st.nulls = st.count - nonnull
        st.min, st.max = r[2], r[3]
        st.distinct = None if r[4] is None else min(int(r[4]), nonnull)
        if numeric:
            st.mean, st.std, st.nans = r[5], r[6], r[7]
            if r[8] is not None:
                st.quantiles = dict(zip(qs, r[8]))
        near_unique = (st.distinct or 0) > max(1000, 0.5 * nonnull)
        if not ci.is_nested and top_k and not (ci.is_float and (st.distinct or 0) > 1000) and not near_unique:
            st.top = [
                (v, int(n))
                for v, n in cur.execute(
                    f"SELECT v, count(*) AS n FROM {rel} GROUP BY v ORDER BY n DESC, v LIMIT {int(top_k)}"
                ).fetchall()
            ]
            non_null_top = [v for v, _ in st.top if v is not None]
            if len(st.top) < top_k:  # the list is the whole distribution: exact distinct count
                st.distinct = len(non_null_top)
        return st

    def histogram(self, view: View, name: str, bins: int = 40, *, sample: int | None = None,
                  lo: float | None = None, hi: float | None = None, log: bool = False,
                  temporal: bool = False) -> tuple[list[float], list[int]]:
        """Equal-width histogram of a numeric column. Returns (edges, counts).

        With ``temporal`` the column is binned on epoch seconds; with ``log``
        on log10 of its positive values."""
        q = quote_ident(name)
        vexpr = f"CAST(epoch({q}) AS DOUBLE)" if temporal else f"CAST({q} AS DOUBLE)"
        if log:
            vexpr = f"log10(CASE WHEN {q} > 0 THEN CAST({q} AS DOUBLE) END)"
        rel = (f"(SELECT {vexpr} AS v FROM ({self.relation_sql(view, sample=sample)})) "
               f"WHERE v IS NOT NULL AND isfinite(v)")
        cur = self.cursor()
        if lo is None or hi is None:
            a, b = cur.execute(f"SELECT min(v), max(v) FROM {rel}").fetchone()
            lo = a if lo is None else lo
            hi = b if hi is None else hi
        if lo is None or hi is None:
            return [], []
        lo, hi = float(lo), float(hi)
        if hi <= lo:
            hi = lo + 1.0
            lo = lo - 0.0
        w = (hi - lo) / bins
        res = cur.execute(
            f"SELECT least(greatest(floor((v - {lo!r}) / {w!r}), 0), {bins - 1})::INT AS b, count(*) "
            f"FROM {rel} AND v >= {lo!r} AND v <= {hi!r} GROUP BY b"
        ).fetchall()
        counts = [0] * bins
        for b, n in res:
            counts[int(b)] = int(n)
        edges = [lo + i * w for i in range(bins + 1)]
        return edges, counts

    def sky_counts(self, view: View, lon: str, lat: str, res_deg: float = 0.5, *,
                   sample: int | None = None):
        """Bin (lon, lat) in degrees onto a ``res_deg`` equirectangular grid.

        Returns a dense ``(nlat, nlon)`` int64 numpy array of counts."""
        import numpy as np

        nlon = int(round(360 / res_deg))
        nlat = int(round(180 / res_deg))
        a, d = quote_ident(lon), quote_ident(lat)
        rel = (f"(SELECT CAST({a} AS DOUBLE) AS a, CAST({d} AS DOUBLE) AS d FROM "
               f"({self.relation_sql(view, sample=sample)})) "
               f"WHERE isfinite(a) AND isfinite(d) AND d BETWEEN -90 AND 90")
        sql = (f"SELECT least(floor((((a % 360) + 360) % 360) / {res_deg!r}), {nlon - 1})::INT AS i, "
               f"least(floor((d + 90) / {res_deg!r}), {nlat - 1})::INT AS j, count(*) AS n "
               f"FROM {rel} GROUP BY i, j")
        tbl = self.cursor().execute(sql).arrow()
        if isinstance(tbl, pa.RecordBatchReader):
            tbl = tbl.read_all()
        grid = np.zeros((nlat, nlon), dtype=np.int64)
        if tbl.num_rows:
            i = tbl.column("i").to_numpy()
            j = tbl.column("j").to_numpy()
            grid[j, i] = tbl.column("n").to_numpy()
        return grid

    def xy_counts(self, view: View, x: str, y: str, nx: int, ny: int, *, sample: int | None = None,
                  xlim=None, ylim=None):
        """2-D histogram of two numeric columns. Returns (grid[ny, nx], (x0,x1), (y0,y1))."""
        import numpy as np

        qx, qy = quote_ident(x), quote_ident(y)
        rel = (f"(SELECT CAST({qx} AS DOUBLE) AS x, CAST({qy} AS DOUBLE) AS y FROM "
               f"({self.relation_sql(view, sample=sample)})) WHERE isfinite(x) AND isfinite(y)")
        cur = self.cursor()
        if xlim is None or ylim is None:
            r = cur.execute(
                f"SELECT approx_quantile(x, 0.001), approx_quantile(x, 0.999), "
                f"approx_quantile(y, 0.001), approx_quantile(y, 0.999), min(x), max(x), min(y), max(y) FROM {rel}"
            ).fetchone()
            if r[0] is None:
                return np.zeros((ny, nx), dtype=np.int64), (0.0, 1.0), (0.0, 1.0)
            # robust range: clip the extreme 0.1% tails unless that collapses the range
            x0, x1 = (r[0], r[1]) if r[1] > r[0] else (r[4], r[5])
            y0, y1 = (r[2], r[3]) if r[3] > r[2] else (r[6], r[7])
            xlim = xlim or (float(x0), float(x1) if x1 > x0 else float(x0) + 1)
            ylim = ylim or (float(y0), float(y1) if y1 > y0 else float(y0) + 1)
        (x0, x1), (y0, y1) = xlim, ylim
        wx, wy = (x1 - x0) / nx, (y1 - y0) / ny
        tbl = cur.execute(
            f"SELECT floor((x - {x0!r}) / {wx!r})::INT AS i, floor((y - {y0!r}) / {wy!r})::INT AS j, "
            f"count(*) AS n FROM {rel} AND x >= {x0!r} AND x < {x1!r} AND y >= {y0!r} AND y < {y1!r} "
            f"GROUP BY i, j"
        ).arrow()
        if isinstance(tbl, pa.RecordBatchReader):
            tbl = tbl.read_all()
        grid = np.zeros((ny, nx), dtype=np.int64)
        if tbl.num_rows:
            i = np.clip(tbl.column("i").to_numpy(), 0, nx - 1)
            j = np.clip(tbl.column("j").to_numpy(), 0, ny - 1)
            np.add.at(grid, (j, i), tbl.column("n").to_numpy())
        return grid, (x0, x1), (y0, y1)

    # ----------------------------------------------------------------- export
    def export(self, view: View, out_path: str, *, fmt: str = "parquet",
               columns: list[str] | None = None) -> int:
        """Write the view (filter + sort, optionally a column subset) to a file."""
        out_path = os.path.abspath(os.path.expanduser(out_path))
        if os.path.abspath(out_path) == self.path:
            raise ValueError("refusing to overwrite the file being explored")
        if view.sql.strip():
            base = self.relation_sql(view, columns)
        else:
            base = self._base_sql(View(where=view.where, order_by=view.order_by), columns, False)
        opts = {"parquet": "FORMAT parquet, COMPRESSION zstd",
                "csv": "FORMAT csv, HEADER true",
                "json": "FORMAT json"}[fmt]
        cur = self.cursor()
        cur.execute(f"COPY ({base}) TO {quote_str(out_path)} ({opts})")
        return self.count(View(where=view.where, sql=view.sql))


# --------------------------------------------------------------------- helpers
def _interrupt(c) -> None:
    try:
        c.interrupt()
    except Exception:
        pass


_RA_NAMES = ("ra", "raj2000", "ra_j2000", "radeg", "ra_deg", "alpha", "coord_ra", "lon", "elon", "glon",
             "lambda", "ecl_lon", "eclon")
_DEC_NAMES = ("dec", "decl", "dej2000", "decj2000", "dec_j2000", "decdeg", "dec_deg", "delta", "coord_dec",
              "lat", "elat", "glat", "beta", "ecl_lat", "eclat")


def guess_sky_columns(names: Iterable[str]) -> tuple[str | None, str | None]:
    """Pick the most plausible (longitude, latitude) column pair, or (None, None)."""
    names = list(names)
    low = {n.lower(): n for n in names}
    for r, d in zip(_RA_NAMES, _DEC_NAMES):
        if r in low and d in low:
            return low[r], low[d]
    ras = [n for n in names if re.search(r"(^|_)ra($|_)|^ra[A-Z]|Ra$", n) or n.lower() in _RA_NAMES]
    decs = [n for n in names if re.search(r"(^|_)dec($|_)|^dec[A-Z]|Dec$", n) or n.lower() in _DEC_NAMES]
    ras = [n for n in ras if not re.search(r"err|sigma|cov|rate|dot", n, re.I)]
    decs = [n for n in decs if not re.search(r"err|sigma|cov|rate|dot", n, re.I)]
    if ras and decs:
        return ras[0], decs[0]
    return None, None


def parse_row_spec(text: str, total: int) -> int:
    """Parse '1234', '1_000', '1.5M', '2k', '50%', '-10' (from end) into a row index."""
    s = text.strip().replace(",", "").replace("_", "")
    if not s:
        raise ValueError("empty row number")
    if s.endswith("%"):
        return max(0, min(total - 1, int(float(s[:-1]) / 100.0 * total)))
    mult = {"k": 1e3, "m": 1e6, "b": 1e9, "g": 1e9}
    m = 1.0
    if s[-1].lower() in mult:
        m = mult[s[-1].lower()]
        s = s[:-1]
    v = float(s) * m
    if not math.isfinite(v):
        raise ValueError(text)
    i = int(v)
    if i < 0:
        i = total + i
    return max(0, min(max(total - 1, 0), i))
