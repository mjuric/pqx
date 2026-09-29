"""The pqx Textual application."""
from __future__ import annotations

import datetime as dt
import math
import os
import re
import time

import duckdb
import pyarrow as pa
from rich.console import Group
from rich.table import Table
from rich.text import Text
from textual import on, work
from textual.app import App, ComposeResult
from textual.binding import Binding
from textual.containers import Horizontal, Vertical, VerticalScroll
from textual.css.query import NoMatches
from textual.message import Message
from textual.suggester import Suggester
from textual.widgets import (DataTable, Footer, Input, Label, OptionList, Select, Static, TabbedContent,
                             TabPane)
from textual.widgets.option_list import Option

from . import fmt as F
from . import plots
from .data import ColumnStats, ParquetDataset, View, guess_sky_columns, is_sql_query, parse_row_spec, quote_ident
from .screens import ColumnPicker, ExportScreen, GotoScreen, HelpScreen

SPINNER = "⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏"
SAMPLE_ROWS = 2_000_000       # rows used for stats/plots when sampling
AUTO_SAMPLE_ROWS = 200_000_000        # sampling defaults on above this many rows…
AUTO_SAMPLE_BYTES = 8 * 1024 ** 3     # …or this file size

SQL_WORDS = ["and", "or", "not", "is", "null", "between", "in", "like", "ilike", "select", "from", "where",
             "group by", "order by", "limit", "count(*)", "avg(", "min(", "max(", "sum(", "distinct",
             "regexp_matches(", "abs(", "isnan(", "desc", "asc", "having"]


def _epoch_label(v: float) -> str:
    return dt.datetime.fromtimestamp(v, dt.timezone.utc).strftime("%Y-%m-%d %H:%M")


class ColumnSuggester(Suggester):
    """Completes the identifier under the cursor with a column name or keyword."""

    def __init__(self, words: list[str]):
        super().__init__(use_cache=False, case_sensitive=True)
        self.words = words

    async def get_suggestion(self, value: str) -> str | None:
        m = re.search(r"([A-Za-z_][A-Za-z0-9_]*)$", value)
        if not m or len(m.group(1)) < 1:
            return None
        tok = m.group(1)
        low = tok.lower()
        for w in self.words:
            if w.lower().startswith(low) and len(w) > len(tok):
                return value[: m.start()] + w
        return None


class GridTable(DataTable):
    """A DataTable over a sliding window of a much larger result.

    It holds only ``row_count`` rows starting at absolute position ``offset``.
    Moving past either edge posts :class:`Seek`; the app loads a new window
    around the target and puts the cursor back on it."""

    BINDINGS = [
        Binding("s", "app.sort", "Sort"),
        Binding("equals_sign", "app.filter_value", "= filter", show=False),
        Binding("d", "app.toggle_detail", "Detail"),
        Binding("c", "app.pick_columns", "Columns"),
        Binding("minus", "app.hide_column", "Hide col", show=False),
        Binding("p", "app.pin_columns", "Pin", show=False),
        Binding("g", "app.goto", "Go to"),
        Binding("f", "app.toggle_raw", "Raw/smart", show=False),
        Binding("y", "app.copy_cell", "Copy", show=False),
        Binding("i", "app.inspect_column", "Col stats", show=False),
    ]

    class Seek(Message):
        def __init__(self, row: int) -> None:
            super().__init__()
            self.row = row

    offset: int = 0
    total: int | None = None  # rows in the whole view, when known
    window: int = 300

    @property
    def abs_row(self) -> int:
        return self.offset + self.cursor_row

    def more_below(self) -> bool:
        if self.total is None:
            return self.row_count >= self.window
        return self.offset + self.row_count < self.total

    def _page_rows(self) -> int:
        return max(1, self.scrollable_content_region.height - self.header_height - 1)

    def action_cursor_down(self) -> None:
        if self.cursor_row >= self.row_count - 1 and self.more_below():
            self.post_message(self.Seek(self.abs_row + 1))
            return
        super().action_cursor_down()

    def action_cursor_up(self) -> None:
        if self.cursor_row <= 0 and self.offset > 0:
            self.post_message(self.Seek(self.abs_row - 1))
            return
        super().action_cursor_up()

    def action_page_down(self) -> None:
        page = self._page_rows()
        if self.cursor_row + page >= self.row_count and self.more_below():
            self.post_message(self.Seek(self.abs_row + page))
            return
        super().action_page_down()

    def action_page_up(self) -> None:
        page = self._page_rows()
        if self.cursor_row - page < 0 and self.offset > 0:
            self.post_message(self.Seek(max(0, self.abs_row - page)))
            return
        super().action_page_up()

    def action_scroll_top(self) -> None:
        if self.offset > 0:
            self.post_message(self.Seek(0))
            return
        super().action_scroll_top()

    def action_scroll_bottom(self) -> None:
        if self.more_below():
            if self.total is not None:
                self.post_message(self.Seek(self.total - 1))
            else:
                self.app.notify("Still counting rows…", timeout=2)
            return
        super().action_scroll_bottom()

    def on_mouse_scroll_down(self, event) -> None:
        if self.scroll_y >= self.max_scroll_y and self.more_below():
            self.post_message(self.Seek(self.offset + self.row_count))

    def on_mouse_scroll_up(self, event) -> None:
        if self.scroll_y <= 0 and self.offset > 0:
            self.post_message(self.Seek(max(0, self.offset - 1)))


class PqxApp(App):
    CSS_PATH = "app.tcss"
    TITLE = "pqx"
    BINDINGS = [
        Binding("slash", "focus_filter", "Filter"),
        Binding("x", "clear_filter", "Clear filter"),
        Binding("e", "export", "Export"),
        Binding("m", "toggle_sample", "Sampling"),
        Binding("question_mark,f1", "help", "Help"),
        Binding("escape", "escape", "Cancel/Back", show=False),
        Binding("q", "quit", "Quit"),
        Binding("1", "tab('tab-data')", "Data", show=False),
        Binding("2", "tab('tab-schema')", "Schema", show=False),
        Binding("3", "tab('tab-stats')", "Stats", show=False),
        Binding("4", "tab('tab-plot')", "Plot", show=False),
        Binding("5", "tab('tab-meta')", "Metadata", show=False),
        Binding("l", "hist_log_y", "Log counts", show=False),
        Binding("L", "hist_log_x", "Log values", show=False),
        Binding("left_square_bracket", "bins(-1)", "Fewer bins", show=False),
        Binding("right_square_bracket", "bins(1)", "More bins", show=False),
        Binding("r", "rotate_sky", "Rotate sky", show=False),
    ]

    def __init__(self, path: str, *, where: str = "", theme: str | None = None,
                 sample: bool | None = None, threads: int | None = None):
        super().__init__()
        self.ds = ParquetDataset(path, threads=threads)
        self.view = View(where=where) if where and not is_sql_query(where) else View(sql=where if where else "")
        self._initial_filter = where
        self.cols_shown: list[str] = self.ds.column_names
        self.result_schema: list[tuple[str, pa.DataType]] = [(c.name, c.arrow_type) for c in self.ds.columns]
        self.formatters: dict[str, F.CellFormatter] = {}
        self.raw = False
        self.page = None
        self.total: int | None = self.ds.num_rows if self.view.is_trivial else None
        big = self.ds.num_rows > AUTO_SAMPLE_ROWS or self.ds.file_size > AUTO_SAMPLE_BYTES
        self.sampling = big if sample is None else sample
        self.history: list[str] = []
        self._hist_pos = 0
        self._busy: dict[str, str] = {}
        self._spin = 0
        self._stats_col: str | None = None
        self._hist_bins = 60
        self._hist_log_y = False
        self._hist_log_x = False
        self._sky_center = 0.0
        self._last_error = ""
        self._stats_stale = True
        self._stats_shown: str | None = None
        if theme:
            self.theme = theme
        else:
            self.theme = "tokyo-night"

    # ------------------------------------------------------------------ layout
    def compose(self) -> ComposeResult:
        yield Static(id="titlebar")
        with Horizontal(id="filterbar"):
            yield Label(" WHERE ", id="filter-mode")
            yield Input(value=self._initial_filter,
                        placeholder="filter: a SQL WHERE expression (mag < 21 and band = 'r'), "
                                    "or a full query: select … from t   — press / to focus",
                        id="filter",
                        suggester=ColumnSuggester(self.ds.column_names + SQL_WORDS))
        with TabbedContent(id="tabs", initial="tab-data"):
            with TabPane("Data", id="tab-data"):
                with Horizontal():
                    yield GridTable(id="grid", zebra_stripes=True, header_height=2, cursor_type="cell")
                    with VerticalScroll(id="detail"):
                        yield Static(id="detail-body")
            with TabPane("Schema", id="tab-schema"):
                with Vertical():
                    yield DataTable(id="schema-table", zebra_stripes=True, cursor_type="row")
                    yield Static(id="schema-desc")
            with TabPane("Stats", id="tab-stats"):
                with Horizontal():
                    yield OptionList(id="stats-cols")
                    with VerticalScroll(id="stats-body"):
                        yield Static(id="stats-head")
                        yield Static(id="stats-summary")
                        yield Static(id="stats-plot")
            with TabPane("Plot", id="tab-plot"):
                with Vertical():
                    with Horizontal(id="plot-controls"):
                        yield Select([("Sky (Mollweide)", "sky"), ("Scatter x vs y", "xy")],
                                     value="sky", allow_blank=False, id="plot-mode")
                        yield Select([], id="plot-x", prompt="x / lon")
                        yield Select([], id="plot-y", prompt="y / lat")
                        yield Select([(c, c) for c in plots.COLORMAPS], value=plots.DEFAULT_CMAP,
                                     allow_blank=False, id="plot-cmap")
                    yield Static(id="plot-head")
                    with VerticalScroll(id="plot-area"):
                        yield Static(id="plot-body")
            with TabPane("Metadata", id="tab-meta"):
                with VerticalScroll():
                    yield Static(id="meta-overview")
                    yield Label("[b]Row groups[/b]", classes="section")
                    yield DataTable(id="rowgroups", zebra_stripes=True, cursor_type="row")
                    yield Label("[b]Key-value metadata[/b]", classes="section")
                    yield Static(id="meta-kv")
        yield Static(id="status")
        yield Footer()

    def on_mount(self) -> None:
        self.title = f"pqx — {os.path.basename(self.ds.path)}"
        self._render_titlebar()
        self._setup_formatters(self.result_schema)
        self.query_one("#detail").display = False
        grid = self.query_one(GridTable)
        grid.window = max(150, min(1000, 40_000 // max(1, len(self.cols_shown))))
        grid.total = self.total
        self._build_schema_tab()
        self._build_meta_tab()
        self._build_stats_list()
        self._init_plot_controls()
        self.set_interval(0.1, self._tick)
        if self._initial_filter:
            self.apply_filter(self._initial_filter)
        else:
            self._rebuild_columns()
            self.load_window(0, 0)
        grid.focus()

    # ------------------------------------------------------------ title/status
    def _render_titlebar(self) -> None:
        ds = self.ds
        t = Text.assemble(
            (" ▦ pqx ", "bold reverse"), "  ",
            (os.path.basename(ds.path), "bold"),
            ("  ·  ", "dim"), (f"{ds.num_rows:,}", "bold"), (" rows", "dim"),
            ("  ·  ", "dim"), (f"{len(ds.columns)}", "bold"), (" columns", "dim"),
            ("  ·  ", "dim"), (F.human_bytes(ds.file_size), "bold"),
            ("  ·  ", "dim"), (f"{ds.meta.num_row_groups}", "bold"), (" row groups", "dim"),
        )
        self.query_one("#titlebar", Static).update(t)

    def _tick(self) -> None:
        if self._busy:
            self._spin = (self._spin + 1) % len(SPINNER)
            self._render_status()

    def _set_busy(self, key: str, label: str | None) -> None:
        if label is None:
            self._busy.pop(key, None)
        else:
            self._busy[key] = label
        self._render_status()

    def _render_status(self) -> None:
        grid = self.query_one(GridTable)
        parts: list = []
        if self._busy:
            parts.append((f" {SPINNER[self._spin]} " + " · ".join(self._busy.values()) + "  ", "bold $accent"))
        n = grid.row_count
        tot = self.total
        if n:
            a, b = grid.offset + 1, grid.offset + n
            parts += [("row ", "dim"), (f"{grid.abs_row:,}", "bold"), (" · ", "dim"),
                      (f"{tot:,}" if tot is not None else "…", "bold"), (" rows", "dim")]
            parts.append((f"   window {a - 1:,}–{b - 1:,}", "dim"))
        elif tot == 0:
            parts.append(("no matching rows", "bold $warning"))
        if not self.view.is_trivial and tot is not None and not self.view.sql:
            pct = 100.0 * tot / self.ds.num_rows if self.ds.num_rows else 0
            parts.append((f"   filter keeps {pct:.3g}% of {F.human_count(self.ds.num_rows)}", "dim"))
        if self.view.order_by:
            c, d = self.view.order_by[0]
            parts.append((f"   sorted by {c} {'↓' if d else '↑'}", "$secondary"))
        if self.view.sql:
            parts.append(("   SQL result", "$secondary"))
        if self.sampling:
            parts.append((f"   stats/plots sampled ({F.human_count(SAMPLE_ROWS)} rows)", "$warning"))
        if self.raw:
            parts.append(("   raw values", "$warning"))
        if self._last_error:
            parts = [(" ✖ " + self._last_error, "bold $error")] + [("   ", "")] + parts
        t = Text()
        for s, style in parts:
            t.append(s, self._style(style))
        self.query_one("#status", Static).update(t)

    def _style(self, style: str) -> str:
        if "$" not in style:
            return style
        tv = self.get_css_variables()
        return re.sub(r"\$([a-z-]+)", lambda m: tv.get(m.group(1), "white"), style)

    # --------------------------------------------------------------- the grid
    def _setup_formatters(self, schema: list[tuple[str, pa.DataType]]) -> None:
        self.formatters = {}
        for name, typ in schema:
            unit = ""
            if name in self.ds._by_name:
                unit = self.ds.column(name).unit
            self.formatters[name] = F.CellFormatter(name, typ, unit)

    def _rebuild_columns(self) -> None:
        grid = self.query_one(GridTable)
        grid.clear(columns=True)
        types = dict(self.result_schema)
        order = {c: d for c, d in self.view.order_by}
        for name in self.cols_shown:
            typ = types.get(name, pa.string())
            unit = self.ds.column(name).unit if name in self.ds._by_name else ""
            arrow = ""
            if name in order:
                arrow = " ▼" if order[name] else " ▲"
            label = Text.assemble((name, "bold"), (arrow, "bold"), "\n",
                                  (F.short_type(typ) + (f" · {unit}" if unit else ""), "dim italic"))
            grid.add_column(label, key=name)

    @work(thread=True, exclusive=True, group="page")
    def load_window(self, offset: int, cursor_abs: int, column: int | None = None) -> None:
        grid = self.query_one(GridTable)
        view = self.view
        offset = max(0, offset)
        self.call_from_thread(self._set_busy, "page", "loading rows")
        try:
            with self.ds.tagged("page"):
                page = self.ds.fetch(view, offset, grid.window, self.cols_shown)
        except duckdb.InterruptException:
            return
        except Exception as e:  # noqa: BLE001
            self.call_from_thread(self._show_error, e)
            return
        finally:
            self.call_from_thread(self._set_busy, "page", None)
        if view is not self.view:
            return
        self.call_from_thread(self._apply_page, page, cursor_abs, column)

    def _apply_page(self, page, cursor_abs: int, column: int | None) -> None:
        grid = self.query_one(GridTable)
        col = grid.cursor_column if column is None else column
        scroll_x = grid.scroll_x
        grid.clear()
        grid.offset = page.offset
        fm = [self.formatters.get(n) or F.CellFormatter(n, t) for n, t in zip(page.columns, page.types)]
        raw = self.raw
        rows = []
        for i, row in enumerate(page.rows):
            label_n = page.row_numbers[i] if page.row_numbers[i] is not None else page.offset + i
            rows.append(([f(v, raw) for f, v in zip(fm, row)], Text(f"{label_n:,}", style="dim")))
        for cells, label in rows:
            grid.add_row(*cells, label=label)
        self.page = page
        if page.rows:
            r = max(0, min(len(page.rows) - 1, cursor_abs - page.offset))
            grid.move_cursor(row=r, column=min(col, max(0, len(self.cols_shown) - 1)), animate=False)
            grid.scroll_x = scroll_x
        if self.total is None and len(page.rows) < grid.window:
            self.total = page.offset + len(page.rows)  # hit the end: we now know the size
            grid.total = self.total
        self._render_status()
        self._update_detail()

    @on(GridTable.Seek)
    def seek(self, event: GridTable.Seek) -> None:
        self._seek_to(event.row)

    def _seek_to(self, target: int, column: int | None = None) -> None:
        grid = self.query_one(GridTable)
        target = max(0, target)
        if self.total is not None:
            target = min(target, max(0, self.total - 1))
        w = grid.window
        if grid.row_count and grid.offset <= target < grid.offset + grid.row_count:
            grid.move_cursor(row=target - grid.offset, column=column, animate=False)
            return
        offset = max(0, target - w // 2)
        if self.total is not None:
            offset = max(0, min(offset, self.total - w))
        self.load_window(offset, target, column)

    @on(DataTable.CellHighlighted, "#grid")
    def cell_highlighted(self) -> None:
        self._render_status()
        self._update_detail()

    @on(DataTable.CellSelected, "#grid")
    def cell_selected(self) -> None:
        self.action_toggle_detail()

    @on(DataTable.HeaderSelected, "#grid")
    def header_selected(self, event: DataTable.HeaderSelected) -> None:
        self._sort_by(str(event.column_key.value))

    def _cursor_value(self):
        grid = self.query_one(GridTable)
        if self.page is None or not self.page.rows:
            return None, None
        r = grid.cursor_row
        c = grid.cursor_column
        if r >= len(self.page.rows) or c >= len(self.page.columns):
            return None, None
        return self.page.columns[c], self.page.rows[r][c]

    # ------------------------------------------------------------ detail pane
    def action_toggle_detail(self) -> None:
        d = self.query_one("#detail")
        d.display = not d.display
        self._update_detail()

    def _update_detail(self) -> None:
        d = self.query_one("#detail")
        if not d.display or self.page is None or not self.page.rows:
            return
        grid = self.query_one(GridTable)
        r = min(grid.cursor_row, len(self.page.rows) - 1)
        row = self.page.rows[r]
        rn = self.page.row_numbers[r]
        tbl = Table.grid(padding=(0, 1), expand=True)
        tbl.add_column(style="bold", no_wrap=True, max_width=24)
        tbl.add_column(ratio=1)
        cur_col = self.page.columns[grid.cursor_column] if grid.cursor_column < len(self.page.columns) else None
        for name, typ, v in zip(self.page.columns, self.page.types, row):
            fmt = self.formatters.get(name) or F.CellFormatter(name, typ)
            full = F.format_value(F.shortest(v, typ), fmt.kind, raw=True, width=0)
            if len(full) > 300:  # keep one huge JSON/blob from burying every other column
                full = full[:300] + f"… ({len(full):,} chars; y copies it all)"
            val = Text(full, style="dim italic" if v is None else "")
            extra = F.derived(name, fmt.kind, v)
            cell = Text.assemble(val)
            if extra:
                cell.append("\n" + extra, style="italic cyan")
            info = self.ds._by_name.get(name)
            if info is not None and (info.unit or info.description):
                cell.append("\n" + " ".join(s for s in (f"[{info.unit}]" if info.unit else "",
                                                        info.description) if s), style="dim")
            key = Text(name, style="bold reverse" if name == cur_col else "bold")
            tbl.add_row(key, cell)
        head = Text.assemble(("Row ", "dim"), (f"{grid.abs_row:,}", "bold"),
                             (f"   (file row {rn:,})" if rn is not None else "", "dim"), "\n")
        self.query_one("#detail-body", Static).update(Group(head, tbl))

    # --------------------------------------------------------------- filtering
    def action_focus_filter(self) -> None:
        self.query_one("#filter", Input).focus()

    @on(Input.Changed, "#filter")
    def filter_changed(self, event: Input.Changed) -> None:
        lab = self.query_one("#filter-mode", Label)
        lab.update(" SQL " if is_sql_query(event.value) else " WHERE ")
        lab.set_class(is_sql_query(event.value), "sql")
        event.input.remove_class("error")

    @on(Input.Submitted, "#filter")
    def filter_submitted(self, event: Input.Submitted) -> None:
        text = event.value.strip()
        if text and (not self.history or self.history[-1] != text):
            self.history.append(text)
        self._hist_pos = len(self.history)
        self.apply_filter(text)

    def on_key(self, event) -> None:
        inp = self.query_one("#filter", Input)
        if self.focused is inp and event.key in ("up", "down") and self.history:
            self._hist_pos = max(0, min(len(self.history), self._hist_pos + (-1 if event.key == "up" else 1)))
            inp.value = self.history[self._hist_pos] if self._hist_pos < len(self.history) else ""
            inp.cursor_position = len(inp.value)
            event.stop()

    @work(thread=True, exclusive=True, group="filter")
    def apply_filter(self, text: str, keep_file_row: int | None = None) -> None:
        if is_sql_query(text):
            view = View(sql=text)
        else:
            view = View(where=text, order_by=list(self.view.order_by) if not self.view.sql else [])
        try:
            with self.ds.tagged("validate"):
                schema = self.ds.validate(view)
        except Exception as e:  # noqa: BLE001
            self.call_from_thread(self._show_error, e, True)
            return
        self.call_from_thread(self._set_view, view, schema, keep_file_row)

    def _set_view(self, view: View, schema, keep_file_row: int | None = None) -> None:
        inp = self.query_one("#filter", Input)
        inp.remove_class("error")
        if self.focused is inp and self.query_one(TabbedContent).active == "tab-data":
            self.query_one(GridTable).focus()
        self._last_error = ""
        was_sql = bool(self.view.sql)
        self.view = view
        grid = self.query_one(GridTable)
        if view.sql or was_sql:
            self.result_schema = schema
            self._setup_formatters(schema)
            self.cols_shown = [n for n, _ in schema] if view.sql else self.ds.column_names
        self.total = self.ds.num_rows if view.is_trivial else None
        grid.total = self.total
        grid.offset = 0
        self._rebuild_columns()
        self._build_stats_list()
        self._init_plot_controls(keep=True)
        target = 0
        if keep_file_row is not None:
            target = keep_file_row if view.is_trivial else 0
        self.load_window(max(0, target - grid.window // 2), target)
        if not view.is_trivial:
            self.count_rows()
        self._refresh_analysis()
        self._render_status()

    @work(thread=True, exclusive=True, group="count")
    def count_rows(self) -> None:
        view = self.view
        self.call_from_thread(self._set_busy, "count", "counting rows")
        try:
            with self.ds.tagged("count"):
                n = self.ds.count(view)
        except duckdb.InterruptException:
            return
        except Exception as e:  # noqa: BLE001
            self.call_from_thread(self._show_error, e)
            return
        finally:
            self.call_from_thread(self._set_busy, "count", None)
        if view is self.view:
            self.call_from_thread(self._set_total, n)

    def _set_total(self, n: int) -> None:
        self.total = n
        self.query_one(GridTable).total = n
        self._render_status()

    def action_clear_filter(self) -> None:
        inp = self.query_one("#filter", Input)
        if not inp.value and self.view.is_trivial:
            return
        fr = None
        if self.page and self.page.rows:
            fr = self.page.row_numbers[min(self.query_one(GridTable).cursor_row, len(self.page.rows) - 1)]
        inp.value = ""
        self.view = View(sql=self.view.sql, where=self.view.where)  # sort is dropped with the filter
        self.apply_filter("", fr)
        self.query_one(GridTable).focus()

    def action_filter_value(self) -> None:
        name, v = self._cursor_value()
        if name is None:
            return
        if self.view.sql:
            self.notify("= filtering works on the table, not on SQL results", severity="warning")
            return
        q = quote_ident(name)
        if v is None:
            cond = f"{q} IS NULL"
        elif isinstance(v, bool):
            cond = f"{q} = {'true' if v else 'false'}"
        elif isinstance(v, float) and math.isnan(v):
            cond = f"isnan({q})"
        elif isinstance(v, (int, float)):
            cond = f"{q} = {v!r}"
        elif isinstance(v, dt.datetime):
            cond = f"{q} = TIMESTAMPTZ '{v.isoformat()}'" if v.tzinfo else f"{q} = TIMESTAMP '{v.isoformat()}'"
        elif isinstance(v, dt.date):
            cond = f"{q} = DATE '{v.isoformat()}'"
        elif isinstance(v, str):
            cond = f"{q} = '" + v.replace("'", "''") + "'"
        else:
            self.notify("Can't filter on this value type", severity="warning")
            return
        if re.fullmatch(r"[A-Za-z_][A-Za-z0-9_]*", name):
            cond = cond.replace(q, name, 1)  # plain identifiers read better unquoted
        inp = self.query_one("#filter", Input)
        cur = inp.value.strip()
        inp.value = f"({cur}) and {cond}" if cur and " or " in cur.lower() else (f"{cur} and {cond}" if cur else cond)
        self.history.append(inp.value)
        self.apply_filter(inp.value)

    def _show_error(self, e: Exception, mark_input: bool = False) -> None:
        msg = str(e).strip()
        first = msg.split("\n")[0]
        first = re.sub(r"^(Binder|Parser|Catalog|Conversion|Invalid Input|Out of Range) Error:\s*", "", first)
        self._last_error = first[:200]
        if mark_input:
            self.query_one("#filter", Input).add_class("error")
        self.notify(msg[:600], title="Query error", severity="error", timeout=8)
        self._render_status()

    # ----------------------------------------------------------------- sorting
    def action_sort(self) -> None:
        grid = self.query_one(GridTable)
        if not self.cols_shown:
            return
        self._sort_by(self.cols_shown[grid.cursor_column])

    def _sort_by(self, name: str) -> None:
        if self.view.sql:
            self.notify("Sort SQL results with ORDER BY in the query", severity="warning")
            return
        cur = dict(self.view.order_by)
        if name not in cur:
            order = [(name, False)]
        elif not cur[name]:
            order = [(name, True)]
        else:
            order = []
        self.view = View(where=self.view.where, order_by=order)
        grid = self.query_one(GridTable)
        col = grid.cursor_column
        self.total = self.ds.num_rows if self.view.is_trivial else self.total
        grid.total = self.total
        self._rebuild_columns()
        self.load_window(0, 0, col)
        if self.total is None:
            self.count_rows()
        what = "off" if not order else ("descending" if order[0][1] else "ascending")
        self.notify(f"Sort {name}: {what}", timeout=2)

    # ------------------------------------------------------------ grid actions
    def action_pick_columns(self) -> None:
        cols = [(n, F.short_type(t)) for n, t in self.result_schema]

        def done(result):
            if result:
                self.cols_shown = result
                self._rebuild_columns()
                grid = self.query_one(GridTable)
                self.load_window(grid.offset, grid.abs_row, 0)
        self.push_screen(ColumnPicker(cols, self.cols_shown), done)

    def action_hide_column(self) -> None:
        grid = self.query_one(GridTable)
        if len(self.cols_shown) <= 1:
            return
        name = self.cols_shown[grid.cursor_column]
        self.cols_shown = [c for c in self.cols_shown if c != name]
        col = min(grid.cursor_column, len(self.cols_shown) - 1)
        self._rebuild_columns()
        self.load_window(grid.offset, grid.abs_row, col)
        self.notify(f"Hid {name} — press c to bring it back", timeout=2)

    def action_pin_columns(self) -> None:
        grid = self.query_one(GridTable)
        grid.fixed_columns = 0 if grid.fixed_columns else grid.cursor_column + 1

    def action_toggle_raw(self) -> None:
        self.raw = not self.raw
        grid = self.query_one(GridTable)
        if self.page is not None:
            self._apply_page(self.page, grid.abs_row, grid.cursor_column)

    def action_copy_cell(self) -> None:
        name, v = self._cursor_value()
        if name is None:
            return
        typ = dict(self.result_schema).get(name, pa.null())
        s = F.format_value(F.shortest(v, typ), "float", raw=True, width=0) if v is not None else ""
        self.copy_to_clipboard(s)
        self.notify(f"Copied {name} = {s[:60]}", timeout=2)

    def action_goto(self) -> None:
        def done(spec):
            if not spec:
                return
            try:
                total = self.total if self.total is not None else 1 << 62
                target = parse_row_spec(spec, total)
            except ValueError:
                self.notify(f"Not a row number: {spec}", severity="error")
                return
            self._seek_to(target)
        self.push_screen(GotoScreen(self.total), done)

    def action_inspect_column(self) -> None:
        grid = self.query_one(GridTable)
        if not self.cols_shown:
            return
        self._show_stats_for(self.cols_shown[grid.cursor_column])

    # --------------------------------------------------------------- app-wide
    def action_tab(self, tab: str) -> None:
        # Drop focus first: TabbedContent re-activates whichever pane holds the
        # focused widget, which would otherwise snap us back to the old tab.
        self.set_focus(None)
        self.query_one(TabbedContent).active = tab

    def action_help(self) -> None:
        self.push_screen(HelpScreen())

    def action_escape(self) -> None:
        if self._busy:
            self.ds.interrupt()
            self.notify("Cancelled running queries", timeout=2)
            return
        if isinstance(self.focused, Input):
            self.query_one(GridTable).focus() if self.query_one(TabbedContent).active == "tab-data" \
                else self.set_focus(None)

    def action_toggle_sample(self) -> None:
        self.sampling = not self.sampling
        self.notify("Sampling " + ("on: stats and plots use ~" + F.human_count(SAMPLE_ROWS) + " rows"
                                   if self.sampling else "off: stats and plots scan every row"), timeout=3)
        self._render_status()
        self._refresh_analysis()

    def _sample(self) -> int | None:
        return SAMPLE_ROWS if self.sampling else None

    def check_action(self, action: str, parameters) -> bool | None:
        # single-letter app keys must not fire while typing in an input
        if isinstance(self.focused, Input) and action in (
                "clear_filter", "export", "toggle_sample", "help", "quit", "tab", "hist_log_y",
                "hist_log_x", "bins", "rotate_sky", "focus_filter"):
            return False
        return True

    def action_export(self) -> None:
        stem = os.path.splitext(os.path.basename(self.ds.path))[0]
        default = os.path.join(os.getcwd(), f"{stem}.subset.parquet")
        desc = "all rows" if self.view.is_trivial else (
            "SQL result" if self.view.sql else f"WHERE {self.view.where}" if self.view.where else "")
        if self.view.order_by:
            desc += f", sorted by {self.view.order_by[0][0]}"
        n = f"{self.total:,} rows" if self.total is not None else "row count pending"
        self.push_screen(ExportScreen(default, f"{desc} · {n} · {len(self.cols_shown)} visible columns"),
                         self._do_export)

    def _do_export(self, opts) -> None:
        if opts:
            self.export_worker(opts)

    @work(thread=True, group="export")
    def export_worker(self, opts: dict) -> None:
        self.call_from_thread(self._set_busy, "export", "exporting")
        t0 = time.time()
        try:
            cols = self.cols_shown if opts["visible_only"] else None
            with self.ds.tagged("export"):
                n = self.ds.export(self.view, opts["path"], fmt=opts["fmt"], columns=cols)
        except Exception as e:  # noqa: BLE001
            self.call_from_thread(self._show_error, e)
            return
        finally:
            self.call_from_thread(self._set_busy, "export", None)
        size = F.human_bytes(os.path.getsize(opts["path"])) if os.path.exists(opts["path"]) else "?"
        self.call_from_thread(self.notify, f"Wrote {n:,} rows ({size}) to {opts['path']} in {time.time() - t0:.1f}s",
                              title="Export complete", timeout=8)

    # ------------------------------------------------------------ schema tab
    def _build_schema_tab(self) -> None:
        t = self.query_one("#schema-table", DataTable)
        t.add_columns("#", "column", "type", "unit", "nulls", "min", "max", "size", "ratio", "encodings",
                      "description")
        summ = {d["path"]: d for d in self.ds.column_chunk_summary()}
        for i, c in enumerate(self.ds.columns):
            d = summ.get(c.name)
            fm = F.CellFormatter(c.name, c.arrow_type, c.unit)
            if d is None:  # nested: aggregate the leaves
                leaves = [v for k, v in summ.items() if k.split(".")[0] == c.name]
                size = sum(v["compressed"] for v in leaves)
                usize = sum(v["uncompressed"] for v in leaves)
                mn = mx = None
                nulls = None
                enc = ""
            else:
                size, usize = d["compressed"], d["uncompressed"]
                mn, mx = d["min"], d["max"]
                nulls = d["nulls"] if d["has_stats"] else None
                enc = ", ".join(sorted(e.replace("RLE_DICTIONARY", "DICT") for e in d["encodings"]))
            ratio = f"{usize / size:.1f}×" if size else ""
            null_txt = Text("–", style="dim") if nulls is None else Text(
                f"{nulls:,}" + (f" ({100 * nulls / self.ds.num_rows:.2g}%)" if nulls and self.ds.num_rows else ""),
                style="dim" if not nulls else "")
            t.add_row(Text(str(i), style="dim"), Text(c.name, style="bold"),
                      Text(F.short_type(c.arrow_type), style="italic"), c.unit, null_txt,
                      fm(mn) if mn is not None else Text("–", style="dim"),
                      fm(mx) if mx is not None else Text("–", style="dim"),
                      Text(F.human_bytes(size), justify="right"), Text(ratio, justify="right"), Text(enc, style="dim"),
                      Text(c.description[:60] + ("…" if len(c.description) > 60 else ""), style="dim"),
                      key=c.name)

    @on(DataTable.RowHighlighted, "#schema-table")
    def schema_row(self, event: DataTable.RowHighlighted) -> None:
        name = str(event.row_key.value) if event.row_key else None
        if name is None or name not in self.ds._by_name:
            return
        c = self.ds.column(name)
        t = Text.assemble((c.name, "bold"), "  ", (str(c.arrow_type), "italic"),
                          (f"  [{c.unit}]" if c.unit else "", "cyan"),
                          ("  nullable" if c.nullable else "  not null", "dim"), "\n",
                          (c.description or "(no description in the file's field metadata)",
                           "" if c.description else "dim italic"),
                          ("\nEnter: column statistics", "dim"))
        self.query_one("#schema-desc", Static).update(t)

    @on(DataTable.RowSelected, "#schema-table")
    def schema_selected(self, event: DataTable.RowSelected) -> None:
        self._show_stats_for(str(event.row_key.value))

    # ---------------------------------------------------------- metadata tab
    def _build_meta_tab(self) -> None:
        ds, md = self.ds, self.ds.meta
        ov = Table.grid(padding=(0, 2))
        ov.add_column(style="dim", justify="right")
        ov.add_column()
        rgs = ds.row_groups()
        avg_rg = (sum(r["rows"] for r in rgs) / len(rgs)) if rgs else 0
        comp = sum(r["compressed"] for r in rgs)
        unc = sum(r["uncompressed"] for r in rgs)
        for k, v in [
            ("path", ds.path), ("file size", f"{F.human_bytes(ds.file_size)} ({ds.file_size:,} bytes)"),
            ("rows", f"{ds.num_rows:,}"), ("columns", f"{len(ds.columns)} top-level · {md.num_columns} leaf"),
            ("row groups", f"{md.num_row_groups:,} · ~{avg_rg:,.0f} rows each"),
            ("data size", f"{F.human_bytes(comp)} compressed · {F.human_bytes(unc)} uncompressed"
                          + (f" · {unc / comp:.2f}× ratio" if comp else "")),
            ("format version", str(md.format_version)), ("created by", str(md.created_by or "?")),
            ("footer size", F.human_bytes(md.serialized_size)),
        ]:
            ov.add_row(k, v)
        self.query_one("#meta-overview", Static).update(Group(Text("File", style="bold"), ov))
        t = self.query_one("#rowgroups", DataTable)
        t.add_columns("#", "first row", "rows", "compressed", "uncompressed", "ratio")
        for r in rgs[:5000]:
            t.add_row(Text(str(r["index"]), style="dim"), Text(f"{r['start']:,}", justify="right"),
                      Text(f"{r['rows']:,}", justify="right"),
                      Text(F.human_bytes(r["compressed"]), justify="right"),
                      Text(F.human_bytes(r["uncompressed"]), justify="right"),
                      Text(f"{r['uncompressed'] / r['compressed']:.2f}×" if r["compressed"] else "", justify="right"))
        t.styles.max_height = min(len(rgs), 15) + 2
        kv = ds.key_value_metadata()
        parts = []
        from rich.json import JSON
        for k, v in kv.items():
            parts.append(Text(k, style="bold cyan"))
            if k == "ARROW:schema":
                parts.append(Text(f"  base64-encoded Arrow schema, {F.human_bytes(len(v))} (decoded in the Schema tab)\n",
                                  style="dim italic"))
                continue
            try:
                parts.append(JSON(v, indent=2))
                parts.append(Text(""))
            except Exception:
                parts.append(Text("  " + (v if len(v) < 4000 else v[:4000] + " …") + "\n"))
        if not parts:
            parts = [Text("(none)", style="dim italic")]
        self.query_one("#meta-kv", Static).update(Group(*parts))

    # -------------------------------------------------------------- stats tab
    def _build_stats_list(self) -> None:
        ol = self.query_one("#stats-cols", OptionList)
        ol.clear_options()
        for name, typ in self.result_schema:
            ol.add_option(Option(Text.assemble((name, "bold"), "  ", (F.short_type(typ), "dim italic")), id=name))
        if self._stats_col not in dict(self.result_schema):
            self._stats_col = None

    def _show_stats_for(self, name: str) -> None:
        self.action_tab("tab-stats")
        ol = self.query_one("#stats-cols", OptionList)
        try:
            ol.highlighted = ol.get_option_index(name)
        except Exception:
            pass
        self._stats_col = name
        ol.focus()
        self.compute_stats(name)

    @on(OptionList.OptionHighlighted, "#stats-cols")
    def stats_col_highlighted(self, event: OptionList.OptionHighlighted) -> None:
        name = event.option.id
        if name and name != self._stats_col:
            self._stats_col = name
            if self._tab_is("tab-stats"):
                self._debounced("stats", 0.15, lambda: self._tab_is("tab-stats") and self.compute_stats(name))

    def _debounced(self, key: str, delay: float, fn) -> None:
        timers = self.__dict__.setdefault("_pqx_timers", {})
        if key in timers:
            timers[key].stop()
        timers[key] = self.set_timer(delay, fn)

    @on(TabbedContent.TabActivated)
    def tab_activated(self, event: TabbedContent.TabActivated) -> None:
        pane = event.pane.id
        if pane == "tab-stats":
            if self._stats_col is None and self.result_schema:
                self._stats_col = self.result_schema[0][0]
                self.query_one("#stats-cols", OptionList).highlighted = 0
            if self._stats_col and (self._stats_stale or self._stats_shown != self._stats_col):
                self.compute_stats(self._stats_col)
            self.query_one("#stats-cols", OptionList).focus()
        elif pane == "tab-plot":
            self.query_one("#plot-area").focus()
            self.call_after_refresh(self.replot)  # after layout, so the plot fills the pane
        elif pane == "tab-schema":
            self.query_one("#schema-table", DataTable).focus()
        elif pane == "tab-data":
            self.query_one(GridTable).focus()

    def _refresh_analysis(self) -> None:
        active = self.query_one(TabbedContent).active
        if active == "tab-stats" and self._stats_col:
            self.compute_stats(self._stats_col)
        elif active == "tab-plot":
            self.replot()
        else:  # recompute lazily when those tabs are next opened
            self.query_one("#stats-summary", Static).update("")
            self._stats_stale = True

    @work(thread=True, exclusive=True, group="stats")
    def compute_stats(self, name: str) -> None:
        view = self.view
        types = dict(self.result_schema)
        typ = types.get(name)
        if typ is None:
            return
        sample = self._sample()
        self.call_from_thread(self._set_busy, "stats", f"profiling {name}")
        t0 = time.time()
        try:
            with self.ds.tagged("stats"):
                st = self.ds.column_stats(view, name, sample=sample, result_types=types)
                hist = None
                ci_numeric = pa.types.is_integer(typ) or pa.types.is_floating(typ)
                temporal = pa.types.is_timestamp(typ) or pa.types.is_date(typ)
                if (ci_numeric or temporal) and st.count - st.nulls > 0 and (st.distinct or 0) > 12:
                    hist = self.ds.histogram(view, name, bins=self._hist_bins, sample=sample,
                                             log=self._hist_log_x and not temporal, temporal=temporal)
        except duckdb.InterruptException:
            return
        except Exception as e:  # noqa: BLE001
            self.call_from_thread(self._show_error, e)
            return
        finally:
            self.call_from_thread(self._set_busy, "stats", None)
        if view is not self.view:
            return
        self.call_from_thread(self._render_stats, name, typ, st, hist, time.time() - t0)

    def _render_stats(self, name: str, typ: pa.DataType, st: ColumnStats, hist, elapsed: float) -> None:
        self._stats_stale = False
        self._stats_shown = name
        info = self.ds._by_name.get(name)
        fm = self.formatters.get(name) or F.CellFormatter(name, typ)
        head = Text.assemble((name, "bold"), "  ", (str(typ), "italic dim"),
                             (f"  [{info.unit}]" if info and info.unit else "", "cyan"))
        if info and info.description:
            head.append("\n" + info.description, style="dim")
        scope = "all rows" if self.view.is_trivial else ("SQL result" if self.view.sql else f"WHERE {self.view.where}")
        head.append(f"\n{scope}" + (" · sampled" if st.sampled else "") + f" · {elapsed:.2f}s", style="dim italic")
        self.query_one("#stats-head", Static).update(head)

        def pct(n):
            return f"{100 * n / st.count:.3g}%" if st.count else ""

        g = Table.grid(padding=(0, 2))
        g.add_column(style="dim", justify="right")
        g.add_column(justify="right")
        g.add_column(style="dim")
        g.add_row("rows", f"{st.count:,}", "sampled" if st.sampled else "")
        g.add_row("nulls", f"{st.nulls:,}", pct(st.nulls))
        if st.nans is not None:
            g.add_row("NaN", f"{st.nans:,}", pct(st.nans))
        if st.distinct is not None:
            exact = bool(st.top) and len(st.top) < 10
            g.add_row("distinct", f"{st.distinct:,}" if exact else f"≈{st.distinct:,}",
                      "" if exact else "HyperLogLog estimate")
        if st.min is not None:
            g.add_row("min", fm(st.min), F.derived(name, fm.kind, st.min))
            g.add_row("max", fm(st.max), F.derived(name, fm.kind, st.max))
        is_int = pa.types.is_integer(typ)

        def num(v):
            if isinstance(v, (int, float)) and not isinstance(v, bool):
                if is_int:
                    return Text(f"{int(round(v)):,}" if abs(v) < 1e15 else str(int(round(v))), justify="right")
                return fm(float(v))
            return Text(str(v))

        if st.mean is not None:
            g.add_row("mean", Text(F._fmt_float(float(st.mean), 15 if is_int else 9), justify="right")
                      if is_int else fm(float(st.mean)), "")
        if st.std is not None:
            g.add_row("std", F.format_value(float(st.std), "err"), "")
        if st.quantiles:
            for q, v in st.quantiles.items():
                g.add_row(f"p{q * 100:g}", num(v), "median" if q == 0.5 else "")
        blocks = [g]
        if st.top and (hist is None):
            blocks.append(Text("\nMost frequent values", style="bold"))
            tt = Table.grid(padding=(0, 1))
            tt.add_column(justify="right", max_width=40, no_wrap=True)
            tt.add_column()
            tt.add_column(justify="right", style="dim")
            m = max(n for _, n in st.top) or 1
            barw = 30
            col = self.get_css_variables().get("primary", "#5fafff")
            for v, n in st.top:
                w = n / m * barw
                bar = "█" * int(w) + (" ▏▎▍▌▋▊▉"[int((w - int(w)) * 8)] if w < barw else "")
                tt.add_row(fm(v), Text(bar.rstrip(), style=col), f"{n:,}  {pct(n)}")
            blocks.append(tt)
        self.query_one("#stats-summary", Static).update(Group(*blocks))
        plot = Text("")
        if hist is not None:
            edges, counts = hist
            width = max(30, self.query_one("#stats-body").size.width - 4)
            temporal = pa.types.is_timestamp(typ) or pa.types.is_date(typ)
            color = self.get_css_variables().get("primary", "#5fafff")
            plot = plots.render_histogram(edges, counts, width=width, height=12, color=color,
                                          log_y=self._hist_log_y, log_x=self._hist_log_x and not temporal,
                                          xlabel=name + ("  (UTC)" if temporal else ""),
                                          xfmt=_epoch_label if temporal else None)
            if temporal and edges:
                lo = dt.datetime.fromtimestamp(edges[0], dt.timezone.utc).isoformat(sep=" ")[:19]
                hi = dt.datetime.fromtimestamp(edges[-1], dt.timezone.utc).isoformat(sep=" ")[:19]
                plot.append(f"\n  range {lo} → {hi}", style="dim")
            plot = Group(Text("\nDistribution", style="bold"), plot,
                         Text(f"{len(counts)} bins · l: log counts · L: log values · [ ]: bins", style="dim italic"))
        self.query_one("#stats-plot", Static).update(plot)

    def action_hist_log_y(self) -> None:
        self._hist_log_y = not self._hist_log_y
        self._refresh_analysis()

    def action_hist_log_x(self) -> None:
        self._hist_log_x = not self._hist_log_x
        self._refresh_analysis()

    def action_bins(self, delta: int) -> None:
        self._hist_bins = max(5, min(400, int(self._hist_bins * (1.5 if delta > 0 else 1 / 1.5))))
        self._refresh_analysis()

    # --------------------------------------------------------------- plot tab
    def _init_plot_controls(self, keep: bool = False) -> None:
        numeric = [n for n, t in self.result_schema if pa.types.is_integer(t) or pa.types.is_floating(t)]
        sx, sy = self.query_one("#plot-x", Select), self.query_one("#plot-y", Select)
        old = (sx.value, sy.value)
        opts = [(n, n) for n in numeric]
        with self.prevent(Select.Changed):
            sx.set_options(opts)
            sy.set_options(opts)
            lon, lat = guess_sky_columns(numeric)
            if keep and old[0] in numeric and old[1] in numeric:
                sx.value, sy.value = old
            elif lon and lat:
                sx.value, sy.value = lon, lat
                self.query_one("#plot-mode", Select).value = "sky"
            elif len(numeric) >= 2:
                sx.value, sy.value = numeric[0], numeric[1]
                self.query_one("#plot-mode", Select).value = "xy"

    @on(Select.Changed)
    def plot_control_changed(self, event: Select.Changed) -> None:
        if event.select.id and event.select.id.startswith("plot-"):
            self._debounced("plot", 0.2, self.replot)

    def action_rotate_sky(self) -> None:
        if self.query_one(TabbedContent).active == "tab-plot":
            self._sky_center = 180.0 if self._sky_center == 0.0 else 0.0
            self.replot()

    def on_resize(self, event) -> None:
        if self._tab_is("tab-plot"):
            self._debounced("plot", 0.3, self.replot)

    def _tab_is(self, tab: str) -> bool:
        try:
            return self.query_one(TabbedContent).active == tab
        except NoMatches:  # shutting down
            return False

    def replot(self) -> None:
        if not self._tab_is("tab-plot"):
            return  # plots are computed lazily, when their tab is shown
        mode = self.query_one("#plot-mode", Select).value
        x = self.query_one("#plot-x", Select).value
        y = self.query_one("#plot-y", Select).value
        cmap = self.query_one("#plot-cmap", Select).value
        body = self.query_one("#plot-body", Static)
        if x is Select.BLANK or y is Select.BLANK or x is None or y is None:
            body.update(Text("Choose two numeric columns to plot.", style="dim italic"))
            return
        area = self.query_one("#plot-area")
        w, h = max(20, area.size.width - 2), max(8, area.size.height - 1)
        self.plot_worker(str(mode), str(x), str(y), str(cmap), w, h)

    @work(thread=True, exclusive=True, group="plot")
    def plot_worker(self, mode: str, x: str, y: str, cmap: str, w: int, h: int) -> None:
        view = self.view
        sample = self._sample()
        dark = self.current_theme.dark
        self.call_from_thread(self._set_busy, "plot", f"binning {x} × {y}")
        t0 = time.time()
        try:
            with self.ds.tagged("plot"):
                if mode == "sky":
                    grid = self.ds.sky_counts(view, x, y, res_deg=0.5, sample=sample)
                    n = int(grid.sum())
                    mw, _ = plots.sky_shape(w, h - 4)
                    frac = (grid > 0).sum() / grid.size
                    cap = f"{n:,} points in range · {100 * frac:.3g}% of 0.5° cells occupied"
                    out = plots.render_skymap(grid, width=mw, height=h - 4, cmap=cmap, dark_bg=dark,
                                              center=self._sky_center, caption=cap)
                else:
                    pw, ph = max(10, w - 12), max(4, h - 5)
                    grid, xl, yl = self.ds.xy_counts(view, x, y, 2 * pw, 2 * ph, sample=sample)
                    n = int(grid.sum())
                    out = plots.render_density(grid, xl, yl, cmap=cmap, dark_bg=dark, xlabel=x, ylabel=y)
        except duckdb.InterruptException:
            return
        except Exception as e:  # noqa: BLE001
            self.call_from_thread(self._show_error, e)
            return
        finally:
            self.call_from_thread(self._set_busy, "plot", None)
        if view is not self.view:
            return
        scope = "all rows" if view.is_trivial else ("SQL result" if view.sql else f"WHERE {view.where}")
        head = Text.assemble(("Sky map " if mode == "sky" else "Density ", "bold"),
                             (f"{x} × {y}", "bold cyan"), ("  ·  " + scope, "dim"),
                             ("  ·  sampled" if sample else "", "dim"),
                             (f"  ·  {n:,} rows plotted · {time.time() - t0:.2f}s", "dim"),
                             ("   r: rotate centre" if mode == "sky" else "", "dim italic"))
        self.call_from_thread(self.query_one("#plot-head", Static).update, head)
        self.call_from_thread(self.query_one("#plot-body", Static).update, out)
