"""Data access layer: a single Parquet file behind DuckDB + PyArrow.

Everything is lazy. The file is never loaded in full; the grid pulls small
windows of rows, statistics and plots push aggregation down into DuckDB.

Access paths:

* **Unfiltered, unsorted** windows are read straight from the row groups that
  hold them with pyarrow (see ``_fetch_direct``) when that is cheaper than
  DuckDB: DuckDB sets up a scan of every row group (from its cached footer) on
  each query, so its cost grows with the number of row groups in the file.
  Otherwise they seek with DuckDB's ``file_row_number`` filter, which skips
  rows inside a large row group faster than pyarrow can decode them.
* **Filtered / sorted / SQL** windows run the query with ``LIMIT/OFFSET``.

The user-visible table is registered as the view ``t`` so filters are plain
SQL ``WHERE`` expressions and free-form queries can say ``SELECT ... FROM t``.
"""
from __future__ import annotations

import atexit
import bisect
import math
import os
import re
import threading
import uuid
import weakref
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
    #: columns not fetched yet (the app's placeholders stand in for their values; see fetch_columns)
    missing: set[str] = field(default_factory=set)


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
        self._lock = threading.Lock()
        self._tls = threading.local()
        self._tagged: dict[str, duckdb.DuckDBPyConnection] = {}
        self._types_cache: dict[str, pa.DataType | None] | None = None
        self._types_done = threading.Event()
        self._types_thread: threading.Thread | None = None
        self._schema_known = threading.Event()  # pyarrow has parsed the footer (or failed to)
        self._ready = threading.Event()  # DuckDB's views exist (or failed to): see ``con``
        self._setup_error: Exception | None = None
        self._footer_lock = threading.Lock()
        self._footer: tuple[list[dict], list[int]] | None = None
        self._encodings: dict[str, set[str]] = {}
        self._io_errors = 0
        self._bad_rgs: set[int] = set()  # row groups pyarrow failed on in a way we couldn't pin down
        # Database-wide config: every cursor shows timestamps in UTC rather than local time, and the
        # footer is parsed once rather than on every query (~1 s each for 2000 row groups x 300
        # columns); DuckDB's cache checks the file's modification time.
        self._con = duckdb.connect(":memory:", config={"TimeZone": "UTC", "parquet_metadata_cache": True})
        if threads:
            self._con.execute(f"SET threads={int(threads)}")
        # DuckDB parses the footer on a background thread (see _bind_types) while pyarrow parses it
        # here: on huge footers each takes most of a second.
        self._opened = False  # the schema below is complete: the setup thread may create the views
        self._start_types()
        try:
            self.pf = pq.ParquetFile(self.path)
            self.meta = self.pf.metadata
            self.arrow_schema: pa.Schema = self.pf.schema_arrow
            self.num_rows: int = self.meta.num_rows
            self.file_size = os.path.getsize(self.path)
            self.columns = [self._column_info(f) for f in self.arrow_schema]
            self._by_name = {c.name: c for c in self.columns}
            self._wide_decimals = {f.name for f in self.arrow_schema
                                   if pa.types.is_decimal(f.type) and f.type.precision > 38}
            # file_row_number collides if the file already has such a column; then
            # fall back to OFFSET-based seeking everywhere.
            self._has_rownum = "file_row_number" not in self._by_name
            self._opened = True
        except BaseException:
            self._schema_known.set()
            self._stop_types()  # it gives up at once (not _opened): nothing left running
            raise
        self._schema_known.set()

    @property
    def con(self) -> duckdb.DuckDBPyConnection:
        """The DuckDB connection, once its views over the file exist: waits for
        the background setup, and raises what that failed with, if it did."""
        if not self._ready.is_set():
            self._start_types()
            self._ready.wait()
        if self._setup_error is not None:
            raise self._setup_error
        return self._con

    @property
    def setup_error(self) -> Exception | None:
        """Why DuckDB can't read the file (its views couldn't be created), once
        that's known; ``None`` if it can, or while it's still being set up.
        Every query raises this exception; the footer-based metadata still works."""
        return self._setup_error if self._ready.is_set() else None

    def _create_views(self) -> None:
        src = f"read_parquet({quote_str(self.path)}, file_row_number=true)"
        if self._has_rownum:
            self._con.execute(
                f"CREATE VIEW __pqx_src AS SELECT * RENAME (file_row_number AS {ROWNUM}) FROM {src}"
            )
            self._con.execute(f"CREATE VIEW {TABLE} AS SELECT * EXCLUDE ({ROWNUM}) FROM __pqx_src")
        else:
            self._con.execute(
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
        return self._register(self.con.cursor())

    def _register(self, c):
        """Register ``c`` (anything with ``interrupt()``) under the current tag."""
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
        """Per-column storage + statistics aggregated over all row groups (leaf
        columns, by path; encodings come from ``column_encodings``).

        One pass over every column chunk in the footer, shared with
        ``row_groups`` and computed once: ~2.5 s per million chunks, so call it
        off the UI thread."""
        return self._footer_scan()[0]

    def row_groups(self) -> list[dict]:
        rows = []
        start = 0
        comp = self._footer_scan()[1]
        for i in range(self.meta.num_row_groups):
            g = self.meta.row_group(i)
            rows.append(dict(index=i, start=start, rows=g.num_rows, compressed=comp[i],
                             uncompressed=g.total_byte_size))
            start += g.num_rows
        return rows

    def column_encodings(self, path: str) -> set[str]:
        """Encodings used by leaf column ``path`` in any row group (cheap: one column)."""
        if path not in self._encodings:
            md = self.meta
            idx = [i for i in range(md.num_columns) if md.schema.column(i).path == path]
            out: set[str] = set()
            for rg in range(md.num_row_groups):
                g = md.row_group(rg)
                for i in idx:
                    if i < g.num_columns:
                        out.update(g.column(i).encodings)
            self._encodings[path] = out
        return self._encodings[path]

    def _footer_scan(self) -> tuple[list[dict], list[int]]:
        """(``column_chunk_summary``, compressed bytes per row group), cached.

        The per-chunk cost is pyarrow's Python objects, so this touches as few
        as it can: plain booleans, ints and floats compare their statistics as
        stored (``min_raw``, which is what ``min`` converts them to anyway) and
        encodings are left to ``column_encodings``."""
        with self._footer_lock:
            if self._footer is None:
                self._footer = self._scan_footer()
            return self._footer

    def _scan_footer(self) -> tuple[list[dict], list[int]]:
        md = self.meta
        n = md.num_columns
        schema = [md.schema.column(i) for i in range(n)]
        paths = [sc.path for sc in schema]
        first: dict[str, int] = {}
        slot = [first.setdefault(p, i) for i, p in enumerate(paths)]  # leaves sharing a path add up
        raw = [sc.physical_type in ("BOOLEAN", "INT32", "INT64", "FLOAT", "DOUBLE")
               and sc.logical_type.type == "NONE" and sc.converted_type == "NONE" for sc in schema]
        comp, unc, nulls = [0] * n, [0] * n, [0] * n
        lo: list[Any] = [None] * n
        hi: list[Any] = [None] * n
        has = [True] * n
        info: dict[int, tuple] = {}  # slot -> (physical type, compression) of its first chunk
        rg_comp = []
        for rg in range(md.num_row_groups):
            g = md.row_group(rg)
            total = 0
            for i in range(g.num_columns):
                c = g.column(i)
                k = slot[i]
                if k not in info:
                    info[k] = (c.physical_type, c.compression)
                size = c.total_compressed_size
                total += size
                comp[k] += size
                unc[k] += c.total_uncompressed_size
                st = c.statistics
                if st is None:
                    has[k] = False
                    continue
                if st.has_min_max:
                    if raw[i]:
                        a, b = st.min_raw, st.max_raw
                    else:
                        a, b = st.min, st.max
                    try:
                        m = lo[k]
                        lo[k] = a if m is None else min(m, a)
                        m = hi[k]
                        hi[k] = b if m is None else max(m, b)
                    except TypeError:
                        pass
                    if st.has_null_count:
                        nulls[k] += st.null_count
                elif st.has_null_count:
                    nulls[k] += st.null_count
                else:
                    has[k] = False
            rg_comp.append(total)
        logical = {sc.path: str(sc.logical_type) if sc.logical_type else "" for sc in schema}
        out = [dict(path=paths[k], physical=phys, compression=codec, compressed=comp[k],
                    uncompressed=unc[k], min=lo[k], max=hi[k], nulls=nulls[k], has_stats=has[k],
                    logical=logical[paths[k]])
               for k, (phys, codec) in sorted(info.items())]
        return out, rg_comp

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
        rows = [self.meta.row_group(i).num_rows for i in range(self.meta.num_row_groups)]
        starts = [0]
        for r in rows:
            starts.append(starts[-1] + r)
        k = max(1, min(slices, len(rows)))
        chunk = max(1, -(-sample // k))
        picks = sorted({round(i * (len(rows) - 1) / max(k - 1, 1)) for i in range(k)})
        ranges = [(starts[i], starts[i] + min(chunk, rows[i])) for i in picks]
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
        token = None
        if view.is_trivial:
            page, token = self._fetch_direct(offset, limit, columns)
            if page is not None:
                return page
        cur = self._handover(token, self.con.cursor())
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
        if not is_sql:
            tbl = self._fix_wide_decimals(tbl, rn)
        return _page(offset, tbl, rn)

    def has_row_ids(self, view: View) -> bool:
        """Whether ``fetch(view, ...)`` pages carry file row numbers (needed by
        ``fetch_columns``): not for SQL results, nor for filtered or sorted views
        of files with their own ``file_row_number`` column (DuckDB can't number
        those rows). For such pages fetch every column with ``fetch`` instead."""
        return not view.sql.strip() and (view.is_trivial or self._has_rownum)

    def _fix_wide_decimals(self, tbl: pa.Table, rn: list) -> pa.Table:
        """Replace DuckDB's values of decimals wider than 38 digits with correct ones.

        DuckDB 1.5 reads such Parquet decimals as wrong doubles (-1.50 as
        -24998051.85), so pages it serves get these columns re-read directly for
        the same file rows, and a column shows the same, correct values however
        it's scrolled. Filters and statistics still see DuckDB's values. Drop
        this once ``test_decimal256_values_are_right`` says DuckDB is fixed."""
        wide = [n for n in tbl.column_names if n in self._wide_decimals]
        if not wide or not rn or any(r is None for r in rn):
            return tbl
        fixed, _ = self._read_rows(rn, wide, force=True)
        if fixed is None:
            return tbl
        for n in wide:
            tbl = tbl.set_column(tbl.column_names.index(n), n, fixed.column(n))
        return tbl

    # ------------------------------------------------- direct row-group reads
    def _fetch_direct(self, offset: int, limit: int, columns: list[str] | None
                      ) -> tuple[Page | None, _Cancel | None]:
        """A trivial view's window read straight from the row groups holding it.

        Returns ``(page, None)``, or ``(None, token)`` to have DuckDB serve the
        window: when DuckDB should be faster (see ``_direct_estimate``), or for
        anything pyarrow might read differently. ``token`` is the cancellation
        token this read registered under the current tag (or ``None``); hand it
        to ``_handover`` so a superseded fetch doesn't start a DuckDB query."""
        names = list(columns or self.column_names)
        end = min(offset + limit, self.num_rows)
        if end <= offset:
            types = self._duck_types()
            if types is None or len(set(names)) != len(names) or any(types.get(n) is None for n in names):
                return None, None
            empty = pa.table({n: pa.array([], types[n]) for n in names})
            return _page(offset, empty, []), None
        tbl, token = self._read_rows(range(offset, end), names)
        if tbl is None:
            return None, token
        return _page(offset, tbl, list(range(offset, end))), None

    def fetch_columns(self, file_rows: list[int], columns: list[str]) -> Page:
        """Values of ``columns`` for the given file rows, in that order.

        For filling in columns of an already loaded page (``page.row_numbers``)
        as they scroll into view, whatever view the page came from: reads only
        the row groups holding those rows when that's cheap, else one DuckDB
        query. ``Page.offset`` is 0 and ``Page.row_numbers`` is ``file_rows``.
        Runs under the caller's tag like ``fetch``. Raises ``ValueError`` for
        rows without a number (``None``): see ``has_row_ids``."""
        if any(r is None for r in file_rows):
            raise ValueError("these rows have no file row numbers (a SQL result, or a filtered/sorted view "
                             "of a file with its own file_row_number column): use fetch() instead")
        rows = [int(r) for r in file_rows]
        names = list(columns)
        if any(r < 0 or r >= self.num_rows for r in rows):
            raise IndexError("file row out of range")
        if rows:
            contiguous = rows == list(range(rows[0], rows[0] + len(rows)))  # a plain view's window: no take
            tbl, token = self._read_rows(range(rows[0], rows[0] + len(rows)) if contiguous else rows, names)
            if tbl is not None:
                return _page(0, tbl, rows)
        else:
            token = None
        cur = self._handover(token, self.con.cursor())
        cols = ", ".join(quote_ident(c) for c in names)
        uniq = sorted(set(rows))
        if self._has_rownum or not uniq:
            sql = f"SELECT {cols} FROM {TABLE} LIMIT 0"
            if uniq:
                sql = (f"SELECT {ROWNUM}, {cols} FROM __pqx_src WHERE {ROWNUM} IN "
                       f"({', '.join(map(str, uniq))}) ORDER BY {ROWNUM}")
            tbl = _arrow(cur.execute(sql))
            if uniq:
                tbl = tbl.drop_columns([ROWNUM])
        else:  # no row-number column to select by: contiguous runs with LIMIT/OFFSET
            tbl = pa.concat_tables([_arrow(cur.execute(f"SELECT {cols} FROM {TABLE} LIMIT {b - a} OFFSET {a}"))
                                    for a, b in _runs(uniq)])
        if uniq and uniq != rows:
            pos = {r: i for i, r in enumerate(uniq)}
            tbl = _take(tbl, [pos[r] for r in rows])
        return _page(0, self._fix_wide_decimals(tbl, rows), rows)

    def _handover(self, token: _Cancel | None, c):
        """Register DuckDB cursor ``c`` in place of a direct read's ``token``.

        Raises ``InterruptException`` if that read was superseded meanwhile, so an
        old fetch falling back to DuckDB can't interrupt (and outlive) a newer one."""
        if token is None:
            return self._register(c)
        tag = getattr(self._tls, "tag", None)
        with self._lock:
            if token.cancelled or (tag and self._tagged.get(tag) is not token):
                raise duckdb.InterruptException("INTERRUPT Error: superseded")
            if tag:
                self._tagged[tag] = c
        return c

    def _read_rows(self, rows, names: list[str], force: bool = False) -> tuple[pa.Table | None, _Cancel | None]:
        """File ``rows`` (any order; ``range`` for a window) of columns ``names``,
        read with pyarrow and converted to DuckDB's types, or ``(None, token)``.
        ``force``: whatever it costs (waiting for the types if need be)."""
        types = self._duck_types(wait=force)
        starts = self._rg_starts()
        if types is None or starts is None or len(set(names)) != len(names):
            return None, None
        if any(types.get(n) is None for n in names):
            return None, None
        uniq = rows if isinstance(rows, range) else sorted(set(rows))
        need: dict[int, list[int]] = {}  # row group -> sorted local rows
        if isinstance(uniq, range):
            r = bisect.bisect_right(starts, uniq.start) - 1
            while r < len(starts) - 1 and starts[r] < uniq.stop:
                lo, hi = max(uniq.start, starts[r]), min(uniq.stop, starts[r + 1])
                if hi > lo:
                    need[r] = range(lo - starts[r], hi - starts[r])
                r += 1
        else:
            for f in uniq:
                r = bisect.bisect_right(starts, f) - 1
                need.setdefault(r, []).append(f - starts[r])
        if self._bad_rgs.intersection(need):
            return None, None
        cheaper, pre_buffer = self._direct_estimate({r: loc[-1] + 1 for r, loc in need.items()}, names)
        if not (cheaper or force):
            return None, None
        token = self._register(_Cancel())
        try:
            parts = [self._read_rg(r, loc, names, pre_buffer, token) for r, loc in need.items()]
            if any(p is None for p in parts) or token.cancelled:
                return self._bail(token)
            tbl = pa.concat_tables(parts) if len(parts) > 1 else parts[0]
            if not isinstance(rows, range):
                pos = {f: i for i, f in enumerate(uniq)}
                tbl = _take(tbl, [pos[f] for f in rows])
        except duckdb.InterruptException:
            raise
        except OSError:  # I/O trouble (maybe transient, e.g. a network FS): DuckDB this time
            self._io_errors += 1
            if self._io_errors >= 3:
                self._exclude_unreadable(need, names, token)
            return self._bail(token)
        except Exception:  # noqa: BLE001 - pyarrow can't read some column: find it, leave it to DuckDB
            self._exclude_unreadable(need, names, token)
            return self._bail(token)
        self._io_errors = 0
        out = []
        for name in names:
            try:
                out.append(_convert(tbl.column(name), types[name]))
            except Exception:  # noqa: BLE001 - e.g. a lossy cast: this column goes to DuckDB from now on
                types[name] = None
                return self._bail(token)
        if token.cancelled:
            return self._bail(token)
        return pa.Table.from_arrays(out, names=names), token

    @staticmethod
    def _bail(token: _Cancel) -> tuple[None, _Cancel]:
        if token.cancelled:
            raise duckdb.InterruptException("INTERRUPT Error: superseded")
        return None, token

    def _read_rg(self, rg: int, local, names: list[str], pre_buffer: bool, token: _Cancel) -> pa.Table | None:
        """Rows ``local`` (sorted) of row group ``rg``, as pyarrow reads them."""
        # A fresh reader per call: nothing shared between threads, and reopening
        # with the parsed footer costs < 1 ms. Without pre-buffering, a bounded
        # read buffer: with buffer_size=0 parquet-cpp reads each whole column
        # chunk up front. INT96 timestamps as µs, like DuckDB (as ns they
        # silently overflow outside 1677-2262).
        pf = pq.ParquetFile(self.path, metadata=self.meta, pre_buffer=pre_buffer,
                            buffer_size=0 if pre_buffer else _READ_BUFFER,
                            coerce_int96_timestamp_unit="us")
        lo, hi = local[0], local[-1] + 1
        n = hi - lo
        skip = lo
        batches = []
        got = 0
        it = pf.iter_batches(batch_size=max(1024, min(n, 65_536)), row_groups=[rg], columns=names,
                             use_threads=False)
        try:
            for b in it:
                if token.cancelled:
                    raise duckdb.InterruptException("INTERRUPT Error: superseded")
                if skip >= b.num_rows:
                    skip -= b.num_rows
                    continue
                b = b.slice(skip, n - got)
                skip = 0
                batches.append(b)
                got += b.num_rows
                if got >= n:
                    break
        finally:
            close = getattr(it, "close", None)
            if close is not None:
                close()
        if got != n:
            return None
        tbl = pa.Table.from_batches(batches)
        if not set(names) <= set(tbl.column_names) or len(set(tbl.column_names)) != tbl.num_columns:
            return None
        tbl = tbl.select(names)
        if not isinstance(local, range):
            tbl = _take(tbl, [x - lo for x in local])
        return tbl

    def _exclude_unreadable(self, need: dict, names: list[str], token: _Cancel) -> None:
        """After a failed direct read, find the columns pyarrow can't read (one
        row each) and leave them to DuckDB from now on; the others keep the
        fast path. I/O errors don't count. If no column fails on its own, the
        trouble is further in: leave the row groups involved to DuckDB."""
        types = self._duck_types()
        if types is None:
            return
        found = False
        for name in names:
            try:
                self._read_rg(next(iter(need)), range(0, 1), [name], False, token)
            except duckdb.InterruptException:
                raise
            except OSError:
                pass
            except Exception:  # noqa: BLE001
                types[name] = None
                found = True
        if not found:
            self._bad_rgs.update(need)

    def _duck_types(self, wait: bool = False) -> dict[str, pa.DataType | None] | None:
        """Arrow type of each column as DuckDB returns it, or ``None`` where the
        direct path can't reproduce it. Bound once on a background thread
        (started with the dataset); until that's done this returns ``None``
        (fetches use DuckDB), or with ``wait`` blocks for it. It also blocks
        while DuckDB is still being set up: a DuckDB fetch would wait for that
        anyway, and the types follow within milliseconds (the footer is cached)."""
        while not self._types_done.is_set():
            self._start_types()
            if not wait and self._ready.is_set():
                return None
            self._types_done.wait(0.1)
        return self._types_cache

    def _start_types(self) -> None:
        with self._lock:
            if self._types_done.is_set() or (self._types_thread is not None and self._types_thread.is_alive()):
                return
            self._types_thread = threading.Thread(target=self._bind_types, name="pqx-types", daemon=True)
            self._types_thread.start()
            _BINDING.add(self)

    def _stop_types(self) -> None:
        """Wait for the background setup and bind (DuckDB aborts the process if a
        query is still running on a daemon thread when Python exits). Binding
        can't be interrupted (DuckDB checks only once it's bound), and it's one
        footer parse: ~1 s for 2000 row groups x 300 columns."""
        t = self._types_thread
        if t is not None and t is not threading.current_thread():
            t.join(30)

    def _setup(self) -> None:
        """Have DuckDB parse the footer (into its metadata cache) while pyarrow
        parses it in ``__init__``, then create the views once the schema says
        how. Sets ``_ready`` whatever happens; ``con`` raises a failure."""
        try:
            try:
                self._con.execute(f"SELECT * FROM read_parquet({quote_str(self.path)}) LIMIT 0")
            except Exception:  # noqa: BLE001 - creating the views says what's wrong
                pass
            self._schema_known.wait()
            if not self._opened:  # __init__ failed and raises
                raise RuntimeError("the dataset failed to open")
            self._create_views()
        except Exception as e:  # noqa: BLE001
            self._setup_error = e
        finally:
            self._ready.set()

    def _bind_types(self) -> None:
        # Untagged: ds.interrupt() couldn't shorten it, only throw the result away.
        if not self._ready.is_set():
            self._setup()
        if self._setup_error is not None:
            self._types_cache = None
            self._types_done.set()
            return
        cur = self._con.cursor()
        try:
            tbl = cur.execute(f"SELECT * FROM {TABLE} LIMIT 0").arrow()
            if isinstance(tbl, pa.RecordBatchReader):
                tbl = tbl.read_all()
            names = self.arrow_schema.names
            out: dict[str, pa.DataType | None] | None = None
            if len(set(names)) == len(names) and tbl.schema.names == names:
                out = {f.name: tbl.schema.field(f.name).type if _castable(f.type, tbl.schema.field(f.name).type)
                       else None for f in self.arrow_schema}
            self._types_cache = out
            self._types_done.set()
        except Exception:  # noqa: BLE001
            self._types_cache = None
            self._types_done.set()

    def _rg_starts(self) -> list[int] | None:
        """First file row of each row group, plus the row count at the end."""
        cached = getattr(self, "_rg_starts_cache", False)
        if cached is not False:
            return cached
        starts = [0]
        for i in range(self.meta.num_row_groups):
            starts.append(starts[-1] + self.meta.row_group(i).num_rows)
        ok = self.meta.num_row_groups > 0 and starts[-1] == self.num_rows
        self._rg_starts_cache = starts if ok else None
        return self._rg_starts_cache

    def _leaves(self) -> dict[str, list[int]]:
        """Parquet leaf column indices of each top-level column (for size estimates)."""
        cached = getattr(self, "_leaves_cache", None)
        if cached is not None:
            return cached
        out: dict[str, list[int]] = {}
        counts = [_nleaves(f.type) for f in self.arrow_schema]
        if sum(counts) == self.meta.num_columns:  # leaves come in schema order
            i = 0
            for f, k in zip(self.arrow_schema, counts):
                out[f.name] = list(range(i, i + k))
                i += k
        else:  # by path; ambiguous only for a flat "a.b" next to a struct "a"
            top = set(self.arrow_schema.names)
            for i in range(self.meta.num_columns):
                parts = self.meta.schema.column(i).path.split(".")
                for k in range(len(parts), 0, -1):
                    name = ".".join(parts[:k])
                    if name in top:
                        out.setdefault(name, []).append(i)
                        break
        self._leaves_cache = out
        return out

    def _direct_estimate(self, need: dict[int, int], names: list[str]) -> tuple[bool, bool]:
        """(is pyarrow expected to beat DuckDB, should pyarrow pre-buffer), for
        reading rows ``[0, need[rg])`` of each row group ``rg`` in ``need``.

        pyarrow decodes every row from the start of a row group up to the last
        one needed; DuckDB skips rows inside a row group faster, but each query
        sets up every row group in the file. Both pay
        about the same to decode the first page of each column (with a bounded
        read buffer), so that cancels out. Constants are from 300-column
        synthetic files and real 184/53-column ones (PR #15, refitted in #16)."""
        leaves = self._leaves()
        md = self.meta
        decoded = 0.0
        compressed = 0
        for r, upto in need.items():
            g = md.row_group(r)
            if g.num_rows <= 0:
                continue
            frac = min(1.0, upto / g.num_rows)
            for name in names:
                for i in leaves.get(name, ()):
                    c = g.column(i)
                    decoded += c.total_uncompressed_size * frac
                    compressed += c.total_compressed_size
        pa_ms = _PA_NS_PER_BYTE * 1e-6 * decoded
        duck_ms = (_DUCK_MS_BASE + _DUCK_MS_PER_COL * len(names)
                   + md.num_row_groups * (_DUCK_MS_PER_RG + _DUCK_MS_PER_RG_COL * len(names))
                   + _DUCK_SKIP_RATIO * pa_ms)
        return pa_ms <= duck_ms, compressed <= _PRE_BUFFER_MAX

    def window_cost(self, offset: int, limit: int, columns: list[str]) -> float | None:
        """Rough milliseconds to fetch rows ``[offset, offset + limit)`` of ``columns``
        in a plain (unfiltered, unsorted) view, by the cheaper of pyarrow and DuckDB,
        or ``None`` if unknown. For weighing fetching columns up front against later.

        As ``_direct_estimate``, plus the first page of each column chunk, which
        either reader decodes whatever the rows: the cost of a column even near
        the start of a row group (~1 ms each on SSSource's 1M-row row groups)."""
        starts = self._rg_starts()
        if starts is None:
            return None
        end = min(offset + limit, self.num_rows)
        need: dict[int, int] = {}
        r = max(0, bisect.bisect_right(starts, offset) - 1)
        while r < len(starts) - 1 and starts[r] < end:
            if min(end, starts[r + 1]) > max(offset, starts[r]):
                need[r] = min(end, starts[r + 1]) - starts[r]
            r += 1
        leaves = self._leaves()
        md = self.meta
        decoded = 0.0
        for r, upto in need.items():
            g = md.row_group(r)
            if g.num_rows <= 0:
                continue
            frac = min(1.0, upto / g.num_rows)
            for name in columns:
                for i in leaves.get(name, ()):
                    size = g.column(i).total_uncompressed_size
                    decoded += max(size * frac, min(size, _FIRST_PAGE_BYTES))
        pa_ms = _PA_NS_PER_BYTE * 1e-6 * decoded
        duck_ms = (_DUCK_MS_BASE + _DUCK_MS_PER_COL * len(columns)
                   + md.num_row_groups * (_DUCK_MS_PER_RG + _DUCK_MS_PER_RG_COL * len(columns))
                   + _DUCK_SKIP_RATIO * pa_ms)
        return min(pa_ms, duck_ms)

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


_BINDING: weakref.WeakSet = weakref.WeakSet()  # datasets that started a background bind


@atexit.register
def _stop_binds() -> None:
    for ds in list(_BINDING):
        ds._stop_types()


class _Cancel:
    """Stands in for a DuckDB cursor in the tag registry while pyarrow reads."""

    def __init__(self) -> None:
        self.cancelled = False

    def interrupt(self) -> None:
        self.cancelled = True


def _page(offset: int, tbl: pa.Table, rn: list) -> Page:
    cols = [_pylist(tbl.column(i)) for i in range(tbl.num_columns)]
    rows = list(zip(*cols)) if cols else [() for _ in range(tbl.num_rows)]
    return Page(offset, tbl.column_names, rows, rn, [f.type for f in tbl.schema])


def _pylist(col) -> list:
    """``col.to_pylist()``; values Python can't hold (e.g. timestamps past year
    9999, which DuckDB uses for infinity) become ``None`` instead of raising."""
    try:
        return col.to_pylist()
    except (OverflowError, ValueError, pa.ArrowException):
        out = []
        for chunk in getattr(col, "chunks", [col]):
            for i in range(len(chunk)):
                try:
                    out.append(chunk[i].as_py())
                except (OverflowError, ValueError, pa.ArrowException):
                    out.append(None)
        return out


def _take(tbl: pa.Table, idx: list[int]) -> pa.Table:
    """``tbl.take(idx)``, also for string/binary views (pyarrow has no take for them)."""
    try:
        return tbl.take(pa.array(idx, pa.int64()))
    except pa.ArrowNotImplementedError:
        tbl = pa.Table.from_arrays([c.cast(_unview(c.type)) for c in tbl.columns], names=tbl.column_names)
        return tbl.take(pa.array(idx, pa.int64()))


def _unview(t: pa.DataType) -> pa.DataType:
    """``t`` with string/binary views (also nested) as plain strings/binaries."""
    if pa.types.is_string_view(t):
        return pa.large_string()
    if pa.types.is_binary_view(t):
        return pa.large_binary()
    if pa.types.is_struct(t):
        return pa.struct([t.field(i).with_type(_unview(t.field(i).type)) for i in range(t.num_fields)])
    if pa.types.is_map(t):
        return pa.map_(_unview(t.key_type), _unview(t.item_type))
    if pa.types.is_large_list(t):
        return pa.large_list(t.value_field.with_type(_unview(t.value_type)))
    if pa.types.is_list(t):
        return pa.list_(t.value_field.with_type(_unview(t.value_type)))
    if pa.types.is_fixed_size_list(t):
        return pa.list_(t.value_field.with_type(_unview(t.value_type)), t.list_size)
    return t


def _arrow(cur) -> pa.Table:
    tbl = cur.arrow()
    return tbl.read_all() if isinstance(tbl, pa.RecordBatchReader) else tbl


def _runs(rows: list[int]) -> list[tuple[int, int]]:
    """Sorted unique ints as half-open contiguous runs."""
    out: list[tuple[int, int]] = []
    for r in rows:
        if out and out[-1][1] == r:
            out[-1] = (out[-1][0], r + 1)
        else:
            out.append((r, r + 1))
    return out


# Cost model for ``ParquetDataset._direct_estimate`` (milliseconds / bytes). DuckDB's
# constants are with its parquet_metadata_cache on (the footer parsed once): fitted to
# 150-row windows of 1, 20 and all columns on 300-column files with 5-2000 row groups,
# SSSource and mpc_orbits (median model/measured 1.0; 10-90%: 0.5-1.2).
_PA_NS_PER_BYTE = 3.0        # pyarrow decode time per uncompressed byte
_DUCK_SKIP_RATIO = 0.3       # DuckDB's cost to skip a byte, relative to pyarrow decoding it
_DUCK_MS_BASE = 4.0          # DuckDB per-query overhead
_DUCK_MS_PER_COL = 0.1       # ... per selected column
_DUCK_MS_PER_RG = 0.16       # ... per row group in the file (scan setup)
_DUCK_MS_PER_RG_COL = 0.001  # ... per row group and selected column
_PRE_BUFFER_MAX = 32 << 20   # pre-buffer (coalesce reads) only when the chunks are this small
_READ_BUFFER = 1 << 20       # otherwise read through a buffer this size
_FIRST_PAGE_BYTES = 256 << 10  # window_cost: what decoding a column chunk's first page costs, in bytes

_STRINGS = (pa.types.is_string, pa.types.is_large_string, pa.types.is_string_view)
_BINARIES = (pa.types.is_binary, pa.types.is_large_binary, pa.types.is_fixed_size_binary,
             pa.types.is_binary_view)
_LISTS = (pa.types.is_list, pa.types.is_large_list, pa.types.is_fixed_size_list)


def _castable(s: pa.DataType, d: pa.DataType, top: bool = True) -> bool:
    """True when ``_convert`` turns pyarrow's type ``s`` into DuckDB's ``d`` with
    DuckDB's values (lossy cases are then caught by a safe cast)."""
    t = pa.types
    if isinstance(s, pa.BaseExtensionType):  # DuckDB reads JSON and (top-level) UUIDs as text
        if s.extension_name == "arrow.uuid":
            return top and t.is_string(d)
        return s.extension_name == "arrow.json" and _castable(s.storage_type, d, top)
    if t.is_dictionary(s):
        return _castable(s.value_type, d, top)
    if isinstance(d, pa.BaseExtensionType) or t.is_dictionary(d):
        return False
    if t.is_null(s):
        return True
    if t.is_struct(s) or t.is_struct(d):
        return (t.is_struct(s) and t.is_struct(d) and s.num_fields == d.num_fields
                and all(s.field(i).name == d.field(i).name and _castable(s.field(i).type, d.field(i).type, False)
                        for i in range(s.num_fields)))
    if t.is_map(s) or t.is_map(d):
        return (t.is_map(s) and t.is_map(d) and _castable(s.key_type, d.key_type, False)
                and _castable(s.item_type, d.item_type, False))
    if any(f(s) for f in _LISTS):
        return any(f(d) for f in _LISTS) and _castable(s.value_type, d.value_type, False)
    if s == d:
        return True
    if t.is_floating(s):
        return t.is_floating(d) and s.bit_width <= d.bit_width
    if any(f(s) for f in _STRINGS):
        return any(f(d) for f in _STRINGS)
    if any(f(s) for f in _BINARIES):
        return any(f(d) for f in _BINARIES)
    if t.is_timestamp(s):
        return t.is_timestamp(d) and (s.tz is None) == (d.tz is None)
    if t.is_duration(s):  # DuckDB reads the stored int64 count
        return d == pa.int64()
    if t.is_decimal(s):  # DuckDB reads precision > 38 as DOUBLE
        return t.is_decimal(d) or (top and d == pa.float64())
    for kind in (t.is_date, t.is_time):
        if kind(s):
            return kind(d)
    return False


_UNIT_NS = {"s": 10**9, "ms": 10**6, "us": 10**3, "ns": 1}


def _convert(col: pa.ChunkedArray, d: pa.DataType) -> pa.ChunkedArray | pa.Array:
    """Column ``col`` as DuckDB's type ``d`` (``_castable(col.type, d)`` holds)."""
    s = col.type
    t = pa.types
    if isinstance(s, pa.BaseExtensionType) and s.extension_name == "arrow.uuid":
        raw = [b for c in col.chunks for b in c.storage.to_pylist()]
        return pa.array([None if b is None else str(uuid.UUID(bytes=b)) for b in raw], d)
    if (t.is_timestamp(s) and t.is_timestamp(d)) or (t.is_time(s) and t.is_time(d)):
        # coarser unit: DuckDB truncates sub-µs parts (checked to match, negatives included)
        return col.cast(d, safe=_UNIT_NS[s.unit] >= _UNIT_NS[d.unit])
    if t.is_decimal(s) and t.is_floating(d):  # via Python: Arrow's cast isn't correctly rounded
        return pa.array([None if v is None else float(v) for v in col.to_pylist()], d)
    return col.cast(d, safe=True)


def _nleaves(t: pa.DataType) -> int:
    """Number of Parquet leaf columns an Arrow type is stored in."""
    if isinstance(t, pa.BaseExtensionType):
        t = t.storage_type
    if pa.types.is_dictionary(t):
        return 1
    if pa.types.is_struct(t):
        return sum(_nleaves(t.field(i).type) for i in range(t.num_fields))
    if pa.types.is_map(t):
        return _nleaves(t.key_type) + _nleaves(t.item_type)
    if any(f(t) for f in _LISTS):
        return _nleaves(t.value_type)
    return 1


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
