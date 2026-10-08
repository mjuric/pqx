"""The pqx Textual application.

Visual language (after acid's CLI design spec): the terminal's own background
and 16-colour palette, thin panels, colour used only where it carries meaning
(focus, object names, ✓ success / ! warning / ✗ error), secondary text dimmed,
compact numbers, and terse status lines.
"""
from __future__ import annotations

import datetime as dt
import math
import os
import re
import threading
import time
from bisect import bisect_left, bisect_right
from contextlib import contextmanager
from itertools import accumulate

import duckdb
import pyarrow as pa
from rich.console import Group
from rich.padding import Padding
from rich.segment import Segment
from rich.style import Style
from rich.table import Table
from rich.text import Text
from textual import on, work
from textual.app import App, ComposeResult
from textual.binding import Binding
from textual.color import Color
from textual.containers import Horizontal, Vertical, VerticalScroll
from textual.coordinate import Coordinate
from textual.css.query import NoMatches
from textual.geometry import Region
from textual.message import Message
from textual.renderables.styled import Styled
from textual.suggester import Suggester
from textual.theme import Theme
from textual.widgets import DataTable, Input, Label, OptionList, Static, TabbedContent, TabPane
from textual.widgets._data_table import RowRenderables, default_cell_formatter
from textual.widgets.data_table import ColumnKey, Row, RowKey
from textual.worker import get_current_worker

from . import __version__, _terminal
from . import config
from . import fmt as F
from . import plots
from .cells import (FAILED_MARK, MISSING, PLACEHOLDER, UNAVAILABLE, CellRow, ColumnCells, one_cell_per_char, RowCells, RowLayout, text_width,
                    widest_candidates)
from .data import (ColumnStats, Page, ParquetDataset, Stopped, View, guess_sky_columns, is_plain_ident, is_sql_query,
                   parse_row_spec, quote_str, sql_column_ref, sql_ident, sql_text_literal)
from .screens import (FILTER_EXAMPLE, ColumnPicker, ExportScreen, FieldDropdown, FormatScreen, GotoScreen,
                      HelpScreen)
from .widgets import CursorList, DetailList

_terminal.install()  # X10/urxvt mouse (GNU screen) + lenient input decoding; see _terminal.py

SPINNER = "⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏"
SAMPLE_ROWS = 2_000_000       # rows used for stats/plots when sampling
AUTO_SAMPLE_ROWS = 200_000_000        # sampling defaults on above this many rows…
AUTO_SAMPLE_BYTES = 8 * 1024 ** 3     # …or this file size
FOOTER_WAIT = 2.0                     # s: Schema/Metadata wait at most this long for the grid's first page
ROWGROUP_BATCH = 250                  # rows added to Metadata's row-group table per event-loop turn
WIDTH_SAMPLE_ROWS = 16        # rows spread through a window that size its non-numeric columns
LAZY_TAGS = ("cols", "detail")  # query tags of the column fetches for a page (and "cell:<column>"; see load_window)
LAZY_MIN_SAVING_MS = 20  # load a plain view's window lazily only if that's estimated to save this much
DETAIL_FETCH_DELAY = 0.1  # s the Details pane waits (for the cursor to settle) before loading a page's columns
#: a typical value per formatting kind, to size a column not loaded yet that has no statistics
KIND_SAMPLES = {"float": -1.2345678901234567, "float32": -1.2345678, "flux": -1234.5678901, "err": 0.012345678,
                "mag": 21.123456, "angle": 123.4567891, "mjd": 60000.123456789, "bool": True,
                "time": dt.datetime(2026, 1, 2, 3, 4, 5, 678901)}

ACCENTS = ("blue", "cyan", "magenta", "green", "yellow")
DIM_MODES = ("faint", "bright-black")
TABS = [("tab-data", "Data"), ("tab-schema", "Schema"), ("tab-stats", "Stats"), ("tab-plot", "Plot"),
        ("tab-meta", "Meta")]

SQL_WORDS = ["and", "or", "not", "is", "null", "between", "in", "like", "ilike", "select", "from", "where",
             "group by", "order by", "limit", "count(*)", "avg(", "min(", "max(", "sum(", "distinct",
             "regexp_matches(", "abs(", "isnan(", "desc", "asc", "having"]

KEYS = {
    "tab-data": [("/", "filter"), ("x", "clear filter"), ("1-5", "tabs"), ("?", "help"), ("q", "quit"),
                 ("s", "sort"), ("=", "match cell"), ("d", "detail"), ("tab", "into detail"), ("c", "columns"),
                 ("g", "go to"), ("e", "export"), ("< > F", "format")],
    "tab-schema": [("↑↓", "column"), ("enter", "stats"), ("/", "filter"), ("1-5", "tabs"), ("?", "help"),
                   ("q", "quit")],
    "tab-stats": [("↑↓", "column"), ("l", "log counts"), ("L", "log values"), ("[ ]", "bins"),
                  ("m", "sampling"), ("1-5", "tabs"), ("?", "help"), ("q", "quit")],
    "tab-plot": [("enter/click", "pick"), ("tab", "next field"), ("← →", "change"), ("r", "rotate"), ("m", "sampling"), ("e", "export"),
                 ("1-5", "tabs"), ("?", "help"), ("q", "quit")],
    "tab-meta": [("↑↓", "scroll"), ("tab", "next panel"), ("1-5", "tabs"), ("?", "help"), ("q", "quit")],
    "detail": [("↑↓", "column"), ("=", "match"), ("y", "copy"), ("i", "stats"), ("esc", "close"), ("?", "help"),
               ("q", "quit"), ("tab", "grid")],
    "dropdown": [("type", "to filter"), ("↑↓", "move"), ("enter/click", "pick"), ("esc", "close")],
    "filter": [("enter", "apply"), ("esc", "back"), ("ctrl+x", "clear"), ("↑↓", "history"), ("→", "complete"),
               ("select … from t", "full query")],
}


def _ui(fn):
    """Mark a main-thread UI update fed by a worker: if the app is tearing down
    (quit while a query was running), its widgets are gone; skip silently."""
    import functools

    @functools.wraps(fn)
    def wrapper(self, *a, **kw):
        try:
            return fn(self, *a, **kw)
        except NoMatches:
            return None
    return wrapper


def _epoch_label(v: float) -> str:
    return dt.datetime.fromtimestamp(v, dt.timezone.utc).strftime("%Y-%m-%d %H:%M")


PLACEHOLDER_STR_MAX = 20


def _short_number(v) -> str | None:
    """``v`` as a short SQL number literal for an example (None if it has none)."""
    if isinstance(v, int):
        return str(v) if len(str(v)) <= 12 else None
    if not math.isfinite(v):
        return None
    s = f"{v:.3g}"
    if "e" in s and 1 <= abs(v) < 1e12:
        s = str(round(v))
    return s


def filter_placeholder(columns: list[str], types: list[pa.DataType], row: tuple | None) -> str:
    """The filter box's hint, with an example drawn from the file's own first row: the first
    numeric column with a plain name (``> its value``) and the first string column (``= its
    value``). Either part is left out if no column fits it; ``FILTER_EXAMPLE`` stands in only
    when neither does.

    ``columns`` are names as SQL calls them in ``t``."""
    num = text = None
    for name, typ, v in zip(columns, types, row or ()):
        if v is None or v is MISSING or not is_plain_ident(name):
            continue
        if num is None and (pa.types.is_integer(typ) or pa.types.is_floating(typ)) \
                and isinstance(v, (int, float)) and not isinstance(v, bool):
            lit = _short_number(v)
            if lit is not None:
                num = f"{name} > {lit}"
        elif text is None and (pa.types.is_string(typ) or pa.types.is_large_string(typ)) and isinstance(v, str):
            val = F.sanitize(v.strip())[:PLACEHOLDER_STR_MAX].strip()
            if val:
                text = f"{name} = {quote_str(val)}"
        if num and text:
            break
    example = " and ".join(p for p in (num, text) if p) or FILTER_EXAMPLE
    return f"SQL WHERE expression, e.g. {example} — or a full query: select … from t"


def _with_placeholders(page: Page, columns: list[str], types: dict[str, pa.DataType]) -> Page:
    """``page`` (holding some of ``columns``) as a page of all ``columns``, with
    ``MISSING`` for the values of those it doesn't hold (listed in ``missing``)."""
    have = {n: i for i, n in enumerate(page.columns)}
    n = len(page.rows)
    held = list(zip(*page.rows)) if page.rows else [()] * len(page.columns)
    absent = (MISSING,) * n
    data = [held[have[c]] if c in have else absent for c in columns]
    rows = list(zip(*data)) if data else [() for _ in range(n)]
    return Page(page.offset, list(columns), rows, page.row_numbers,
                [page.types[have[c]] if c in have else types.get(c, pa.null()) for c in columns],
                missing={c for c in columns if c not in have})


def pqx_theme(accent: str, border: str) -> Theme:
    """A theme that is nothing but the terminal's own colours (Textual's ANSI mode)."""
    a = f"ansi_{accent}"
    return Theme(
        name=f"pqx-{accent}", ansi=True, dark=True,
        primary=a, secondary="ansi_cyan", accent=a, warning="ansi_yellow", error="ansi_red",
        success="ansi_green", foreground="ansi_default", background="ansi_default",
        surface="ansi_default", panel="ansi_default", boost="ansi_default",
        variables={
            "pqx-border": border, "border": a, "border-blurred": border,
            "ansi-background": "ansi_black", "ansi-foreground": "ansi_white",
            "block-cursor-foreground": "ansi_default", "block-cursor-background": "ansi_default",
            "block-cursor-text-style": "reverse", "block-cursor-blurred-text-style": "reverse",
            "block-cursor-blurred-background": "ansi_default", "block-cursor-blurred-foreground": "ansi_default",
            "block-hover-background": "ansi_default",
            "input-cursor-background": "ansi_default", "input-cursor-foreground": "ansi_default",
            "input-cursor-text-style": "reverse", "input-selection-background": a,
            "screen-selection-background": a, "scrollbar": border, "scrollbar-hover": a,
            "scrollbar-active": a, "scrollbar-background": "ansi_default",
            "scrollbar-background-hover": "ansi_default", "scrollbar-background-active": "ansi_default",
            "scrollbar-corner-color": "ansi_default", "footer-background": "ansi_default",
            "button-color-foreground": "ansi_default",
        },
    )


class ColumnSuggester(Suggester):
    """Completes the identifier under the cursor with a column name or keyword.

    A column name goes in as SQL would need it: bare if it's a plain identifier
    and not a keyword, else quoted (a name is the file's, and must not become SQL
    of its own). Names with control characters aren't offered (no input box should
    hold those; ``=`` can still filter on such a column)."""

    def __init__(self, columns: list[str], words: list[str] = ()):
        super().__init__(use_cache=False, case_sensitive=True)
        self.columns = columns  # a list, or a function giving one (called on first use)
        self.keywords = list(words)
        self._words: list[tuple[str, str]] | None = None  # (what matches, what goes in), made on first use

    @property
    def words(self) -> list[tuple[str, str]]:
        if self._words is None:
            columns = self.columns() if callable(self.columns) else self.columns
            self._words = ([(c, sql_ident(c)) for c in columns if not F.has_controls(c)]
                           + [(w, w) for w in self.keywords])
        return self._words

    async def get_suggestion(self, value: str) -> str | None:
        m = re.search(r"([A-Za-z_][A-Za-z0-9_]*)$", value)
        if not m or len(m.group(1)) < 1:
            return None
        tok = m.group(1)
        low = tok.lower()
        for w, text in self.words:
            if w.lower().startswith(low) and len(w) > len(tok):
                return value[: m.start()] + text
        return None


class TitleBar(Static):
    """The one-line bar at the top; what it shows depends on its width (``PqxApp._render_titlebar``)."""

    def on_resize(self, event) -> None:
        render = getattr(self.app, "_render_titlebar", None)
        if render is not None:
            render(event.size.width)


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
        Binding("less_than_sign", "app.step_digits(-1)", "Fewer digits", show=False),
        Binding("greater_than_sign", "app.step_digits(1)", "More digits", show=False),
        Binding("F", "app.set_format", "Format", show=False),
        Binding("y", "app.copy_cell", "Copy", show=False),
        Binding("i", "app.inspect_column", "Col stats", show=False),
    ]

    class Seek(Message):
        def __init__(self, row: int) -> None:
            super().__init__()
            self.row = row

    class HScroll(Message):
        """The set of horizontally visible columns may have changed."""

    # Rendering. DataTable builds every line from all columns and then crops it
    # to the viewport, which on a 300-column file is ~20x the work that shows.
    # We render only the columns that overlap the crop and stand in blanks of
    # the same width for the rest, so the crop lines up exactly as before.
    render_all_columns = False  # True: render like DataTable (for tests and comparisons)

    def __init__(self, *args, **kwargs) -> None:
        super().__init__(*args, **kwargs)
        # Room for a large terminal's worth of cells and lines (plus their
        # cursor/hover variants), so a full repaint doesn't evict itself.
        self._cell_render_cache.grow(20_000)
        self._row_render_cache.grow(4_000)
        self._line_cache.grow(4_000)
        self._geometry: tuple | None = None  # per-frame column positions; see _column_geometry
        self._widths: tuple = ()  # (column render widths, row label width, row labels shown)
        self._widths_gen = 0  # bumped whenever any of those change
        #: per column, in column order: what its cells share (see pqx.cells); set by set_rows
        self.cell_columns: list[ColumnCells] = []
        self._fast_cell_styles: dict = {}  # see _cell_styles

    @property
    def ordered_columns(self) -> list:
        """DataTable's, cached: it rebuilds the list (and `_render_line` asks for it on every line)."""
        key = (self._column_locations, len(self.columns), self._update_count)
        cached = getattr(self, "_ordered_columns", None)
        if cached is None or cached[0][0] is not key[0] or cached[0][1:] != key[1:]:
            cached = self._ordered_columns = (key, DataTable.ordered_columns.fget(self))
        return cached[1]

    def render_lines(self, crop):
        self._geometry = None  # widths, scroll and size may all have changed since the last frame
        gen = self._widths_gen
        self._column_geometry()
        if gen != self._widths_gen:
            # DataTable re-measures columns on idle without bumping _update_count, and its cell
            # and line caches don't key on width: drop them before this frame serves a stale line.
            self._clear_caches()
        return super().render_lines(crop)

    def _column_geometry(self) -> tuple:
        """``(columns, starts, ends, fixed_width, scrollable_width, widths_gen)`` of the ordered
        columns, positions within DataTable's scrollable line (which also holds the fixed ones)."""
        if self._geometry is None:
            cols = self.ordered_columns
            widths = tuple(c.get_render_width(self) for c in cols)
            sig = (widths, self._row_label_column_width, self._labelled_row_exists)
            if sig != self._widths:
                self._widths = sig
                self._widths_gen += 1
            starts = list(accumulate(widths, initial=0))
            fixed = min(self.fixed_columns, len(cols))
            self._geometry = (cols, starts[:-1], starts[1:], starts[fixed], starts[-1] - starts[fixed],
                              self._widths_gen)
        return self._geometry

    def _render_line_in_row(self, row_key, line_no: int, base_style: Style, cursor_location: Coordinate,
                            hover_location: Coordinate) -> tuple[list, list]:
        """DataTable's, but only for the columns `_render_line` will keep (see the class comment).

        The cache key holds the visible column range, and the cursor and hover
        only for rows they touch, so a cursor move re-renders just its two rows."""
        if self.render_all_columns:
            return super()._render_line_in_row(row_key, line_no, base_style, cursor_location, hover_location)
        cols, starts, ends, fixed_width, table_width, gen = self._column_geometry()
        # the span _render_line crops the scrollable line to
        x1 = self.scroll_offset.x + fixed_width
        x2 = self.scroll_offset.x + self.size.width
        lo = bisect_right(ends, x1)
        hi = max(lo, bisect_left(starts, x2))
        if ends and ends[-1] <= 3 * self.size.width:
            lo, hi = 0, len(cols)  # a narrow table: render it all, so lines stay cached while scrolling sideways

        row_index = self._row_locations.get(row_key) if row_key in self._row_locations else -1
        cursor_type = self.cursor_type
        if cursor_type == "column":
            cur_key, hov_key = cursor_location.column, hover_location.column
        elif cursor_type in ("cell", "row"):
            cur_key = cursor_location if cursor_location.row == row_index else None
            hov_key = hover_location if hover_location.row == row_index else None
        else:
            cur_key = hov_key = None
        cache_key = (row_key, line_no, base_style, cur_key, hov_key, cursor_type, self.show_cursor,
                     self._show_hover_cursor, self._update_count, self._pseudo_class_state, lo, hi, gen,
                     self.size.width)
        if cache_key in self._row_render_cache:
            return self._row_render_cache[cache_key]

        should_highlight = self._should_highlight
        render_cell = self._render_cell
        header_style = self.get_component_styles("datatable--header").rich_style

        fixed_row = []
        if self._labelled_row_exists and self.show_row_labels:
            loc = Coordinate(row_index, -1)
            fixed_row.append(render_cell(
                row_index, -1, header_style, width=self._row_label_column_width,
                cursor=should_highlight(cursor_location, loc, cursor_type),
                hover=should_highlight(hover_location, loc, cursor_type))[line_no])
        if self.fixed_columns:
            if row_key is self._header_row_key:
                fixed_style = header_style
            else:
                fixed_style = self.get_component_styles("datatable--fixed").rich_style
                fixed_style += Style.from_meta({"fixed": True})
            for column_index, column in enumerate(cols[: self.fixed_columns]):
                loc = Coordinate(row_index, column_index)
                fixed_row.append(render_cell(
                    row_index, column_index, fixed_style, column.get_render_width(self),
                    cursor=should_highlight(cursor_location, loc, cursor_type),
                    hover=should_highlight(hover_location, loc, cursor_type))[line_no])

        row_style = self._get_row_style(row_index, base_style)
        scrollable_row = []
        if lo:  # columns left of the view (all of them, when pinned ones push x1 past the end)
            scrollable_row.append([Segment(" " * (starts[lo] if lo < len(cols) else ends[-1]))])
        for column_index in range(lo, hi):
            loc = Coordinate(row_index, column_index)
            scrollable_row.append(render_cell(
                row_index, column_index, row_style, ends[column_index] - starts[column_index],
                cursor=should_highlight(cursor_location, loc, cursor_type),
                hover=should_highlight(hover_location, loc, cursor_type))[line_no])
        if hi < len(cols):
            scrollable_row.append([Segment(" " * (ends[-1] - starts[hi]))])  # and right of it

        # Extend the row's styling to fill the widget, as DataTable does.
        remaining_space = max(0, self.size.width - (table_width + self._row_label_column_width))
        if cursor_type == "row":
            extend_style, _ = self._get_styles_to_render_cell(
                row_index == -1, False, False,
                should_highlight(hover_location, Coordinate(row_index or 0, 0), cursor_type),
                row_index == cursor_location.row, self.show_cursor, self._show_hover_cursor, False, False)
            extend_style = row_style + extend_style
        elif row_style.bgcolor is not None:
            faded = Color.from_rich_color(row_style.bgcolor).blend(self.background_colors[1], factor=0.25)
            extend_style = Style.from_color(color=row_style.color, bgcolor=faded.rich_color)
        else:
            extend_style = Style.from_color(row_style.color, row_style.bgcolor)
        extend_style += Style.from_meta({"row": row_index, "column": 0, "out_of_bounds": True})
        scrollable_row.append([Segment(" " * remaining_space, extend_style)])

        row_pair = (fixed_row, scrollable_row)
        self._row_render_cache[cache_key] = row_pair
        return row_pair

    def watch_scroll_x(self, old_value: float, new_value: float) -> None:
        super().watch_scroll_x(old_value, new_value)
        self.fit_visible()
        self.post_message(self.HScroll())

    def watch_scroll_y(self, old_value: float, new_value: float) -> None:
        super().watch_scroll_y(old_value, new_value)
        self.fit_visible()

    def on_resize(self, event) -> None:
        self.fit_visible()
        self.post_message(self.HScroll())

    def column_window(self) -> tuple[int, int, int, int]:
        """``(first, last, hidden_left, hidden_right)`` over the scrollable columns.

        ``first``/``last`` are 0-based indices of the fully visible columns (a
        column cut by an edge counts as hidden on that side); pinned columns
        are excluded. ``(0, -1, 0, 0)`` when there are no scrollable columns."""
        cols = self.ordered_columns
        fixed = min(self.fixed_columns, len(cols))
        widths = [c.get_render_width(self) for c in cols]
        x0 = self._row_label_column_width + sum(widths[:fixed])
        left = self.scroll_x + x0
        right = self.scroll_x + self.scrollable_content_region.width
        x = x0
        vis, hl, hr = [], 0, 0
        for i in range(fixed, len(cols)):
            a, b = x, x + widths[i]
            if a < left:
                hl += 1
            elif b > right:
                hr += 1
            else:
                vis.append(i)
            x = b
        if not vis:
            return (0, -1, hl, hr)
        return (vis[0], vis[-1], hl, hr)

    def columns_near(self, column: int | None = None, screens: float = 1.0, width: int = 0,
                     left: int | None = None) -> list[int]:
        """Indices of the pinned columns and of the scrollable ones within ``screens``
        screens of the view, or of where scrolling the cursor to ``column`` would
        bring the view. ``width``: the widget's width, if it isn't laid out yet.
        ``left``: a scrollable column the view will start at (instead of where it is now)."""
        cols = self.ordered_columns
        n = len(cols)
        fixed = min(self.fixed_columns, n)
        starts = list(accumulate((c.get_render_width(self) for c in cols), initial=0))
        view = max(1, (self.size.width or width) - self._row_label_column_width - starts[fixed])
        x1 = starts[left] if left is not None and fixed <= left < n else self.scroll_x + starts[fixed]
        x2 = x1 + view
        if column is not None and fixed <= column < n:
            if starts[column] < x1:  # the view will scroll left until the column is its first
                x1, x2 = starts[column], starts[column] + view
            elif starts[column + 1] > x2:  # ... or right until it's its last
                x1, x2 = starts[column + 1] - view, starts[column + 1]
        lo, hi = x1 - screens * view, x2 + screens * view
        return list(range(fixed)) + [i for i in range(fixed, n) if starts[i + 1] > lo and starts[i] < hi]

    def scroll_x_for(self, column: int) -> float:
        """The scroll_x that shows scrollable column ``column`` leftmost (the current one for a pinned one)."""
        cols = self.ordered_columns
        fixed = min(self.fixed_columns, len(cols))
        if not fixed <= column < len(cols):
            return self.scroll_x
        return sum(c.get_render_width(self) for c in cols[fixed:column])

    def screen_row(self) -> int:
        """The cursor's row on screen (0: the top row below the header)."""
        return self.cursor_row - int(self.scroll_y)

    def show_cursor_at(self, screen_row: int) -> None:
        """Scroll so the cursor row is ``screen_row`` rows from the top, or as near as the rows
        above and below it allow (never past the first row or leaving space after the last)."""
        r = self.cursor_row
        self.scroll_to(y=r - max(0, min(screen_row, r)), animate=False, force=True, immediate=True)

    def visible_rows(self) -> int:
        """How many rows the screen shows."""
        return max(1, self.scrollable_content_region.height - (self.header_height if self.show_header else 0))

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

    # ------------------------------------------------------- lazy cells
    # Cells format on first draw (pqx.cells) and widen their column if they don't fit.
    # Growth found while fitting cells for a draw (fit_visible, or a caller's sizing()
    # block) is drawn by that same draw; growth found anywhere else (a renderer
    # formatting a cell nobody fitted) schedules one relayout for the batch.
    _sizing: bool = False  # inside sizing(): growth needn't schedule a relayout
    _growth: int = 0  # how many times a column has widened (fit_visible compares before/after)
    _relayout_pending: bool = False
    FIT_PASSES = 4  # fit_visible: widening a column can bring others into view; this many rounds at most

    def clear(self, columns: bool = False):
        self.cell_columns = []
        return super().clear(columns)

    def column_cells(self, formatters: list[F.CellFormatter], raw: bool) -> list[ColumnCells]:
        """Fresh per-column cell state for the current columns, wired to widen them."""
        return [ColumnCells(fm, raw, col, self._widened) for fm, col in zip(formatters, self.ordered_columns)]

    def set_rows(self, rows: list[tuple], labels: list[Text], cell_columns: list[ColumnCells]) -> None:
        """Replace the (cleared) table's rows with ``rows`` of raw values, all at once.

        Each row becomes a ``CellRow``, which makes its cells when they're first
        asked for. Unlike ``add_row`` this measures no cells: column widths are
        the caller's business (``ColumnCells`` widen their column as cells get
        formatted). Row labels are measured here, since they're few."""
        self.cell_columns = cell_columns
        layout = RowLayout([c.key for c in self.ordered_columns], cell_columns)
        locations, data, rows_meta = self._row_locations, self._data, self.rows
        for i, (values, label) in enumerate(zip(rows, labels)):
            key = RowKey()
            locations[key] = i
            data[key] = CellRow(values, layout)
            rows_meta[key] = Row(key, 1, label)
        if labels:
            self._labelled_row_exists = True
            self._label_column.content_width = max(self._label_column.content_width,
                                                   max(text_width(t) for t in labels))
        self._require_update_dimensions = True
        self._update_count += 1
        self.cursor_coordinate = self.cursor_coordinate
        if self.row_count and self.columns and self.show_cursor and self.cursor_type != "none":
            self._highlight_cursor()
        self.refresh()
        self.check_idle()

    def _compute_row_renderables(self, row_index: int) -> RowRenderables:
        """DataTable's, but a window row's cells are handed over as a lazy ``RowCells``:
        DataTable would otherwise make (and check) a renderable for every column of
        every row it draws, though only the columns in view are rendered."""
        if row_index >= 0:
            row_key = self._row_locations.get_key(row_index)
            row = self._data.get(row_key)
            meta = self.rows.get(row_key)
            if isinstance(row, CellRow) and meta is not None:
                label = None
                if self._should_render_row_labels and meta.label:
                    label = default_cell_formatter(meta.label, wrap=meta.height != 1, height=meta.height)
                return RowRenderables(label, RowCells(row))
        return super()._compute_row_renderables(row_index)

    def _render_cell(self, row_index: int, column_index: int, base_style: Style, width: int,
                     cursor: bool = False, hover: bool = False):
        """DataTable's, with a fast path for the usual window cell: one line of plain Text
        (no markup spans, every character one cell wide). Its segments — padding, the
        justified and cropped text, padding — are built directly, with the styles Rich
        gives them (learnt once per style combination from a one-character sample, see
        _cell_styles); anything else goes through Rich as before."""
        if row_index < 0 or column_index < 0 or self.cell_padding < 1 or self.render_all_columns:
            return super()._render_cell(row_index, column_index, base_style, width, cursor, hover)
        row_key = self._row_locations.get_key(row_index)
        column_key = self._column_locations.get_key(column_index)
        cache_key = (row_key, column_key, base_style, cursor, hover, self._show_hover_cursor, self._update_count,
                     self._pseudo_class_state)
        lines = self._cell_render_cache.get(cache_key)
        if lines is not None:
            return lines
        row, meta = self._data.get(row_key), self.rows.get(row_key)
        text = row.cell_at(column_index).text if isinstance(row, CellRow) and meta and meta.height == 1 else None
        pad = self.cell_padding
        inner = width - 2 * pad
        s = text.plain if text is not None else ""
        if not s or not one_cell_per_char(s):  # (tabs, wide or combining characters: Rich's job)
            return super()._render_cell(row_index, column_index, base_style, width, cursor, hover)
        if text.justify != "left":
            s = s.rstrip()  # as Rich's right/center justification does
        if (not s or inner < 1 or text._spans or text.justify not in ("left", "right", "center")
                or text.overflow not in (None, "fold")):
            return super()._render_cell(row_index, column_index, base_style, width, cursor, hover)
        fixed = row_index < self.fixed_rows or column_index < self.fixed_columns
        component, post = self._get_styles_to_render_cell(
            False, False, fixed, hover, cursor, self.show_cursor, self._show_hover_cursor,
            self.cursor_foreground_priority == "css", self.cursor_background_priority == "css")
        styles = self._cell_styles(base_style, component, post, text.style)
        if styles is None:
            return super()._render_cell(row_index, column_index, base_style, width, cursor, hover)
        n = len(s)
        if n >= inner:
            body = s[:inner]
        elif text.justify == "right":
            body = " " * (inner - n) + s
        elif text.justify == "left":
            body = s + " " * (inner - n)
        else:
            left = (inner - n) // 2
            body = " " * left + s + " " * (inner - n - left)
        where = Style.from_meta({"row": row_index, "column": column_index})
        pad_style, body_style = styles[0] + where, styles[1] + where
        edge = Segment(" " * pad, pad_style)
        lines = [[edge, Segment(body, body_style), edge]]
        self._cell_render_cache[cache_key] = lines
        return lines

    def _cell_styles(self, base_style: Style, component: Style, post: Style, text_style) -> tuple | None:
        """The (padding, text) styles Rich gives a one-line Text cell, without the cell's
        row/column meta: learnt by rendering a one-character sample the way DataTable
        renders a cell. None if the sample doesn't come out as padding, text, padding."""
        key = (base_style, component, post, text_style, self.cell_padding)
        cache = self._fast_cell_styles
        if key not in cache:
            pad = self.cell_padding
            sample = Text("x", style=text_style, justify="left")
            options = self.app.console_options.update_dimensions(2 * pad + 1, 1).update(no_wrap=True)
            lines = self.app.console.render_lines(
                Styled(Padding(sample, (0, pad)), pre_style=base_style + component, post_style=post), options)
            segs = lines[0] if len(lines) == 1 else []
            ok = [seg.text for seg in segs] == [" " * pad, "x", " " * pad] and segs[0].style == segs[2].style
            if len(cache) > 256:
                cache.clear()
            cache[key] = (segs[0].style, segs[1].style) if ok else None
        return cache[key]

    def add_column(self, *a, **kw):
        if self.row_count and self.cell_columns:
            # set_rows' CellRows share one column layout: a column added under them would read as missing.
            raise NotImplementedError("add GridTable columns before set_rows (clear(columns=True) first)")
        return super().add_column(*a, **kw)

    def update_cell(self, *a, **kw):
        # set_rows' CellRows hold Cells made from the window's values: a plain value put in
        # their place would be drawn unformatted and never fitted. The app never edits cells.
        raise NotImplementedError("GridTable cells come from set_rows; reload the window instead")

    def remove_column(self, *a, **kw):
        # A CellRow's layout is shared by the whole window; removing a column would desync it.
        raise NotImplementedError("GridTable columns are rebuilt with clear(columns=True)")

    def update_dimensions_now(self) -> None:
        """Settle the virtual size now rather than on idle, so the cursor can be
        scrolled into view at once (DataTable otherwise draws the top of the
        table first, then scrolls after the refresh)."""
        self._require_update_dimensions = False
        new_rows = self._new_rows.copy()
        self._new_rows.clear()
        self._update_dimensions(new_rows)

    def _widened(self) -> None:
        """A cell outgrew its column (ColumnCells.on_grow)."""
        self._growth += 1
        if not self._sizing and not self._relayout_pending:
            self._relayout_pending = True
            self.call_later(self._relayout)

    def _relayout(self) -> None:
        self._relayout_pending = False
        self.invalidate_cells()

    @contextmanager
    def sizing(self):
        """Columns widened inside this block are drawn by the caller's own redraw: no extra one."""
        outer, self._sizing = self._sizing, True
        try:
            yield
        finally:
            self._sizing = outer

    def visible_cells(self) -> tuple[range, list[int]]:
        """Rows and columns (indices) on screen at the current scroll position, partly visible ones included."""
        cols = self.ordered_columns
        fixed = min(self.fixed_columns, len(cols))
        width = self.scrollable_content_region.width
        x = self._row_label_column_width
        out = []
        for i in range(fixed):
            out.append(i)
            x += cols[i].get_render_width(self)
        left, right = self.scroll_x + x, self.scroll_x + width
        for i in range(fixed, len(cols)):
            w = cols[i].get_render_width(self)
            if x + w > left:
                out.append(i)
            x += w
            if x >= right:
                break
        top = int(self.scroll_y)
        h = max(0, self.scrollable_content_region.height - (self.header_height if self.show_header else 0))
        return range(top, min(self.row_count, top + h + 1)), out

    def fit_visible(self) -> None:
        """Format the cells about to be drawn and widen any column they outgrow,
        before the draw: a number is never shown cut off, even for a frame.

        If that pushes the cursor cell (on screen until now) off it, as when a move
        scrolls to a column that then widens, scroll it back into view."""
        if not self.cell_columns or not self.row_count or self._sizing:
            return
        start = self._growth
        cursor_was_in_view = self.cursor_cell_in_view()
        for _ in range(self.FIT_PASSES):
            before = self._growth
            with self.sizing():
                rows, cols = self.visible_cells()
                self.fit_columns(rows, cols)
            if self._growth == before:
                break
            self._update_count += 1  # every DataTable render cache is keyed on it
            self.update_dimensions_now()
            self.refresh()
        if self._growth != start and cursor_was_in_view and not self.cursor_cell_in_view():
            self._scroll_cursor_into_view()

    def _scroll_cursor_into_view(self, animate: bool = False) -> None:
        """DataTable's, except that a cursor in a pinned column scrolls only vertically: DataTable
        takes the pinned cell's region at its table position, left of the scrolled view, and
        scrolls all the way left to show a cell that's on screen anyway."""
        if self.cursor_type == "cell" and self.cursor_column < self.fixed_columns:
            spacing = self._get_fixed_offset()
            row = self._get_row_region(self.cursor_row)
            region = Region(int(self.scroll_x) + spacing.left, row.y, 1, row.height)
            self.scroll_to_region(region, animate=animate, spacing=spacing, force=True)
            return
        super()._scroll_cursor_into_view(animate)

    def cursor_cell_in_view(self) -> bool:
        """Whether the cursor cell is wholly on screen horizontally (a pinned one always is)."""
        if self.cursor_type != "cell" or self.cursor_column < self.fixed_columns:
            return True
        region = self._get_cell_region(self.cursor_coordinate)
        left = self._get_fixed_offset().left
        return (self.scroll_x + left <= region.x
                and region.right <= self.scroll_x + self.scrollable_content_region.width)

    def scroll_cursor_fitted(self) -> None:
        """Fit the cells on screen, then scroll the cursor cell into view (for after a
        re-format, which can widen columns left of the cursor). The scroll's own fitting
        keeps it in view if its column then widens (see fit_visible)."""
        self.fit_visible()
        if self.row_count:
            self._scroll_cursor_into_view()

    def watch_fixed_columns(self) -> None:
        super().watch_fixed_columns()
        self.fit_visible()  # pinning brings columns into view without scrolling

    def invalidate_cells(self) -> None:
        """Cells' text or column widths changed: drop rendered cells and lines, re-measure, redraw."""
        self._update_count += 1  # every DataTable render cache is keyed on it
        self.update_dimensions_now()
        self.refresh()

    def invalidate_columns(self, keys) -> None:
        """The cells of columns ``keys`` changed, not any width: drop just those rendered cells
        (and the lines, which are cheap to rebuild from the others), and redraw. Unlike
        ``invalidate_cells`` this keeps every other cell's rendering cached."""
        keys = set(keys)
        cache = self._cell_render_cache
        for k in [k for k in cache.keys() if k[1] in keys]:
            cache.discard(k)
        self._row_render_cache.clear()
        self._row_renderable_cache.clear()
        self._line_cache.clear()
        self.refresh()

    def fit_columns(self, rows: list[int] | None = None, columns: list[int] | None = None) -> None:
        """Format the cells in ``rows`` × ``columns`` (default: all), growing columns to fit them."""
        if rows is None:
            rows = range(self.row_count)
        data = self._data
        keys = [c.key for c in self.ordered_columns]
        if columns is not None:
            keys = [keys[i] for i in columns if i < len(keys)]
        row_keys = [self._row_locations.get_key(r) for r in rows]
        for rk in row_keys:
            row = data.get(rk)
            if isinstance(row, CellRow):
                for k in keys:
                    row[k].text  # noqa: B018  (formats and widens the column)

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


class EdgeMarker(Static):
    """A one-cell column beside the grid showing ‹ / › when columns are hidden that way."""

    def __init__(self, side: int, **kw):
        super().__init__(**kw)
        self.side = side  # -1 left, +1 right

    def set_hidden(self, n: int, header_height: int) -> None:
        glyph = ("‹" if self.side < 0 else "›") if n else " "
        self.update(Text(glyph, style="bold") if n else Text(" "))
        self.tooltip = f"{n} more column{'s' if n != 1 else ''} {'left' if self.side < 0 else 'right'}" if n else None

    def on_click(self) -> None:
        grid = self.app.query_one(GridTable)
        (grid.action_page_left if self.side < 0 else grid.action_page_right)()


class PlotControls(Static, can_focus=True):
    """One line of ``label value ▾`` fields: tab moves between them, ← → steps one, enter or a click opens a drop-down."""

    BINDINGS = [
        Binding("left", "step(-1)", "previous value", show=False),
        Binding("right", "step(1)", "next value", show=False),
        Binding("tab", "field(1)", "next field", show=False),
        Binding("shift+tab", "field(-1)", "previous field", show=False),
        Binding("enter,space", "open", "pick", show=False),
    ]

    class Changed(Message):
        pass

    def __init__(self, **kw):
        super().__init__(**kw)
        self.values: dict[str, str] = {"mode": "sky", "x": "", "y": "", "centre": "0°",
                                       "colour": plots.DEFAULT_CMAP}
        self.options: dict[str, list[str]] = {"mode": ["sky", "xy"], "x": [], "y": [], "centre": ["0°", "180°"],
                                              "colour": list(plots.COLORMAPS)}
        self.cur = 0
        self._spans: list[tuple[str, int, int]] = []  # (field, first cell, last cell) of each "value ▾"

    def fields(self) -> list[str]:
        return ["mode", "x", "y", "centre", "colour"] if self.values["mode"] == "sky" else ["mode", "x", "y",
                                                                                           "colour"]

    def value(self, key: str) -> str:
        return self.values[key]

    def set_value(self, key: str, value: str, notify: bool = True) -> None:
        self.values[key] = value
        self.cur = min(self.cur, len(self.fields()) - 1)
        self.refresh()
        if notify:
            self.post_message(self.Changed())

    def set_columns(self, numeric: list[str]) -> None:
        self.options["x"] = list(numeric)
        self.options["y"] = list(numeric)

    def action_step(self, d: int) -> None:
        key = self.fields()[self.cur]
        opts = self.options[key]
        if not opts:
            return
        i = opts.index(self.values[key]) if self.values[key] in opts else 0
        self.set_value(key, opts[(i + d) % len(opts)])

    def action_field(self, d: int) -> None:
        self.cur = (self.cur + d) % len(self.fields())
        self.refresh()

    def _labels(self) -> dict[str, str]:
        sky = self.values["mode"] == "sky"
        return {"mode": "mode", "x": "lon" if sky else "x", "y": "lat" if sky else "y", "centre": "centre",
                "colour": "colour"}

    def action_open(self) -> None:
        """Open a drop-down for the current field, right under its value."""
        key = self.fields()[self.cur]
        opts = self.options[key]
        if not opts:
            return
        start = next((a for k, a, _ in self._spans if k == key), 0)
        x = self.region.x + start
        y = self.region.y + 1

        def done(value):
            if value is not None and value != self.values[key]:
                self.set_value(key, value)
        self.app.push_screen(FieldDropdown(self._labels()[key], opts, self.values[key], x, y), done)

    def on_click(self, event) -> None:
        for i, (key, a, b) in enumerate(self._spans):
            if a <= event.x <= b:
                self.focus()
                self.cur = self.fields().index(key)
                self.refresh()
                self.action_open()
                return

    def render(self) -> Text:
        app = self.app
        dim = app.dim_style if isinstance(app, PqxApp) else Style(dim=True)
        labels = self._labels()
        t = Text(no_wrap=True, overflow="ellipsis")
        spans = []
        for i, key in enumerate(self.fields()):
            if i:
                t.append("    ")
            t.append(labels[key] + " ", dim)
            obj = key in ("x", "y")
            st = Style(bold=True, color="cyan" if obj else None)
            if self.has_focus and i == self.cur:
                st = st + Style(reverse=True)
            a = t.cell_len
            t.append(f"{F.sanitize(self.values[key]) or '—'} ▾", st)
            spans.append((key, a, t.cell_len - 1))
        self._spans = spans
        return t


class PqxApp(App):
    CSS_PATH = "app.tcss"
    TITLE = "pqx"
    ENABLE_COMMAND_PALETTE = True
    BINDINGS = [
        Binding("slash", "focus_filter", "Filter"),
        Binding("x", "clear_filter", "Clear filter"),
        # works while typing in the filter box too (priority beats the input's own ctrl+x = cut)
        Binding("ctrl+x", "clear_filter_anywhere", "Clear filter", show=False, priority=True),
        Binding("ctrl+right", "tab_step(1)", "Next tab", show=False),
        Binding("ctrl+left", "tab_step(-1)", "Previous tab", show=False),
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
                 sample: bool | None = None, threads: int | None = None,
                 accent: str | None = None, dim: str | None = None, border: str | None = None,
                 formats: dict[str, int | str] | None = None):
        super().__init__()
        self.ds = ParquetDataset(path, threads=threads)
        self.view = View(where=where) if where and not is_sql_query(where) else View(sql=where if where else "")
        self._initial_filter = where
        self._placeholder_set = False  # the filter box's hint is drawn from the first page (_apply_page)
        self.cols_shown: list[str] = self.ds.column_names
        self.result_schema: list[tuple[str, pa.DataType]] = [(c.name, c.arrow_type) for c in self.ds.columns]
        self.formatters: dict[str, F.CellFormatter] = {}
        # per-column overrides: saved ones (config.formats_path()), then this session's --format on top
        self._config_error = ""
        try:
            self.col_formats: dict[str, int | str] = config.load_formats()
        except config.ConfigError as e:
            self.col_formats, self._config_error = {}, str(e)
        self.col_formats.update(formats or {})
        self.raw = False
        self.page = None
        # Lazy columns (see load_window): each load_window bumps _page_gen; _shown_gen is the
        # generation of the page on screen. Column fetches for a page merge only while both match.
        self._page_gen = 0
        self._shown_gen = 0
        self._fetch_seq = 0
        self._inflight: dict[str, tuple[int, int, frozenset]] = {}  # tag -> (seq, page gen, columns)
        self._cols_failed: set[str] = set()  # columns that failed to load for the page on screen
        self._cols_cancelled = False  # a fetch for the page on screen was cancelled (see cell_highlighted)
        self._cell_waiters: dict[str, list] = {}  # cell fetch tag -> actions waiting for that column
        self._chunk_stats: dict[tuple[int, int], tuple] = {}  # (row group, leaf) -> (min, max) or ()
        #: (view, file row): the record the cursor is to stay on in a new view, until it's there
        #: (see _set_view and _keep_record); meanwhile cell keys wait in _keep_queue, the record's
        #: values (by column) are _keep_values, and _keep_phase says what's being waited for:
        #: "first" (the view's first page), "locating" (find_row) or "seeking" (the record's page)
        self._keep: tuple[View, int] | None = None
        self._keep_phase = ""
        self._keep_values: dict[str, object] = {}
        self._keep_queue: list[tuple[str, tuple, str, str]] = []  # (action, args, column, key)
        self._locate_seq = 0
        #: the viewport a new view of the same columns keeps (see _rebuild_columns): the column to
        #: show leftmost when its first page is shown, and the screen row (from the grid's top) to
        #: show the kept record on once it's there (_land_keep; _apply_page in a plain view)
        self._anchor_left: str | None = None
        self._anchor_row: int | None = None
        #: (leftmost column, scroll_x) an empty result's header was shown with: kept for the next
        #: view unless the header is scrolled meanwhile
        self._empty_left: tuple[str, float] | None = None
        self.total: int | None = self.ds.num_rows if self.view.is_trivial else None
        big = self.ds.num_rows > AUTO_SAMPLE_ROWS or self.ds.file_size > AUTO_SAMPLE_BYTES
        self.sampling = big if sample is None else sample
        self.history: list[str] = []
        self._hist_pos = 0
        self._busy: dict[str, tuple[str, float]] = {}
        self._spin = 0
        self._stats_col: str | None = None
        self.current_column: str | None = None  # shared by Data, Details, Schema and Stats
        self._hidden_hint: str | None = None  # current column hidden in the grid, until the cursor moves
        self._hist_bins = 60
        self._hist_log_y = False
        self._hist_log_x = False
        self._last_error = ""
        self._stats_stale = True
        self._stats_shown: str | None = None
        self._stats_rendered: tuple | None = None  # (view, *last _render_stats arguments), to reformat in place
        self._count_secs: float | None = None
        # look: accent (focus) colour, how secondary text is dimmed, unfocused border colour
        self.accent = (accent or os.environ.get("PQX_ACCENT") or "blue").lower()
        if self.accent not in ACCENTS:
            self.accent = "blue"
        self.dim_mode = (dim or os.environ.get("PQX_DIM") or "faint").lower()
        if self.dim_mode not in DIM_MODES:
            self.dim_mode = "faint"
        self.dim = "dim" if self.dim_mode == "faint" else "bright_black"
        self.dim_style = Style.parse(self.dim)
        F.DIM = self.dim
        border = border or os.environ.get("PQX_BORDER") or "ansi_bright_black"
        if not border.startswith(("ansi_", "#")):
            border = "ansi_" + border
        self.register_theme(pqx_theme(self.accent, border))
        self.theme = theme or f"pqx-{self.accent}"

    def get_theme_variable_defaults(self) -> dict[str, str]:
        # pqx's own variables, for themes that aren't pqx's (``--theme``): Textual's themes
        # don't define them, and CSS that uses an undefined variable fails to parse.
        try:
            primary = self.current_theme.primary
        except Exception:  # noqa: BLE001 - no theme yet
            primary = "#808080"
        return {"pqx-border": f"{primary} 45%"}

    # ------------------------------------------------------------------ layout
    def compose(self) -> ComposeResult:
        yield TitleBar(id="titlebar")
        with Horizontal(id="filterbox", classes="panel"):
            yield Label("›", id="filter-mode")
            yield Input(value=self._initial_filter,
                        placeholder=filter_placeholder([], [], None),
                        id="filter",
                        suggester=ColumnSuggester(lambda: [self.ds.sql_name(c) for c in self.ds.column_names], SQL_WORDS))
        with TabbedContent(id="tabs", initial="tab-data"):
            with TabPane("Data", id="tab-data"):
                with Horizontal():
                    with Vertical(id="data-panel", classes="panel tabbed"):
                        with Horizontal(id="grid-row"):
                            yield EdgeMarker(-1, id="more-left", classes="edge")
                            yield GridTable(id="grid", header_height=2, cursor_type="cell")
                            yield EdgeMarker(1, id="more-right", classes="edge")
                        yield Static(id="status")
                    with Vertical(id="detail", classes="panel"):
                        yield DetailList(id="detail-list")
            with TabPane("Schema", id="tab-schema"):
                with Vertical():
                    with Vertical(id="schema-panel", classes="panel tabbed"):
                        yield DataTable(id="schema-table", cursor_type="row")
                    yield Static(id="schema-desc", classes="panel")
            with TabPane("Stats", id="tab-stats"):
                with Horizontal():
                    with VerticalScroll(id="stats-body", classes="panel tabbed"):
                        yield Static(id="stats-head")
                        yield Static(id="stats-summary")
                        yield Static(id="stats-plot")
                    with Vertical(id="stats-cols-panel", classes="panel"):
                        yield CursorList(id="stats-cols")
            with TabPane("Plot", id="tab-plot"):
                with Vertical(id="plot-panel", classes="panel tabbed"):
                    yield PlotControls(id="plot-controls")
                    with VerticalScroll(id="plot-area"):
                        yield Static(id="plot-body")
                    yield Static(id="plot-status")
            with TabPane("Metadata", id="tab-meta"):
                with Horizontal():
                    with VerticalScroll(id="meta-file", classes="panel tabbed"):
                        yield Static(id="meta-overview")
                        yield Static(id="meta-kv")
                    with Vertical(id="meta-rg-panel", classes="panel"):
                        yield DataTable(id="rowgroups", cursor_type="row")
                        yield Static(id="meta-status")
        yield Static(id="keys")

    def on_mount(self) -> None:
        self.title = f"pqx — {F.sanitize(os.path.basename(self.ds.path))}"
        if self.dim_mode == "bright-black":
            self.screen.add_class("dim-bright-black")
        self._render_titlebar()
        self._render_tab_titles("tab-data")
        self.query_one("#filterbox").border_title = self._dim_markup("filter")
        self._setup_formatters(self.result_schema)
        if self._config_error:
            self.notify(f"Ignoring saved column formats: {F.sanitize(self._config_error)}", title="! Config", markup=False,
                        severity="warning", timeout=8)
        self.query_one("#detail").display = False
        grid = self.query_one(GridTable)
        grid.window = max(150, min(1000, 40_000 // max(1, len(self.cols_shown))))
        grid.total = self.total
        self._init_schema_tab()
        self._init_meta_tab()
        self._build_stats_list()
        self._init_plot_controls()
        self.set_interval(0.1, self._tick)
        if self._initial_filter:
            self.apply_filter(self._initial_filter)
        else:
            self._rebuild_columns()
            self.load_window(0, 0)
        self.read_footer()  # after the first page's fetch: Schema and Metadata fill in when it's done
        grid.focus()
        self._render_keys()

    # ------------------------------------------------------------ chrome text
    def _dim_markup(self, s: str) -> str:
        colour = "dim" if self.dim_mode == "faint" else "ansi_bright_black"
        return f"[{colour}]{s}[/]"

    TAB_HINT = "^← ^→"

    def _tab_strip(self, active: str, hover: str | None = None) -> tuple[str, list[tuple[str, int, int]]]:
        """The tab strip for the panel borders: ``1 Data ─ 2 Schema ─ …   ^← ^→``.

        Returns its markup and the ``(tab, start, end)`` cells each tab's number
        and name occupy, which clicks and hovers are matched against (Textual
        doesn't route mouse events from border titles itself)."""
        dim = "dim" if self.dim_mode == "faint" else "ansi_bright_black"
        markup: list[str] = []
        spans: list[tuple[str, int, int]] = []
        x = 0

        def add(text: str, style: str, tab: str | None = None) -> None:
            nonlocal x
            markup.append(f"[{style}]{text}[/]" if style else text)
            if tab:
                spans.append((tab, x, x + len(text)))
            x += len(text)

        for i, (tab, name) in enumerate(TABS):
            if i:
                add(" ─ ", dim)
            add(f"{i + 1} ", dim, tab)  # the number is the key: 1-5 go straight to a tab
            style = f"bold ansi_{self.accent}" if tab == active else dim
            add(name, style + (" underline" if tab == hover else ""), tab)
        add("   ", "")
        add(self.TAB_HINT, dim)  # ctrl+← / ctrl+→ step through them
        return "".join(markup), spans

    def _render_tab_titles(self, active: str | None = None) -> None:
        if active is not None:
            self._tab_active = active
        title, self._tab_spans = self._tab_strip(self._tab_active, getattr(self, "_tab_hover", None))
        for panel in self.query(".tabbed"):
            panel.border_title = title

    def _tab_at(self, event) -> str | None:
        """The tab whose number or name is under a mouse event on a panel's top border.

        A left-aligned border title starts three cells in: corner, rule, space."""
        w = event.widget
        if w is None or not w.has_class("tabbed") or event.y != 0:
            return None
        x = event.x - 3
        return next((tab for tab, a, b in getattr(self, "_tab_spans", []) if a <= x < b), None)

    def _render_titlebar(self, width: int | None = None) -> None:
        """``pqx <version> · file · rows · …`` on one line, cut with an ellipsis when it doesn't
        fit. The file name comes first: if it doesn't fit after the version, the version goes
        (``?`` and ``--version`` show it too). Redrawn on resize."""
        ds, d = self.ds, self.dim
        bar = self.query_one("#titlebar", Static)
        if width is None:
            width = bar.content_size.width or max(0, self.size.width - 4)  # (screen padding 1, bar margin 1)
        name = F.sanitize(os.path.basename(ds.path))
        sep = "  ·  "
        version = f" {__version__}" if len(f"pqx {__version__}{sep}{name}") <= width else ""
        t = Text.assemble(
            ("pqx", "bold"), (version, d), (sep, d), (name, "bold cyan"),
            (f"{sep}{ds.num_rows:,} rows{sep}{len(ds.columns)} columns{sep}{F.human_bytes(ds.file_size)}"
             f"{sep}{ds.meta.num_row_groups:,} row groups", d),
            no_wrap=True, overflow="ellipsis",
        )
        bar.update(t)

    def _render_keys(self) -> None:
        try:
            if isinstance(self.screen, FieldDropdown):
                ctx = "dropdown"
            elif isinstance(self.focused, Input):
                ctx = "filter"
            elif isinstance(self.focused, DetailList):
                ctx = "detail"
            else:
                ctx = self.query_one(TabbedContent).active
        except NoMatches:  # another modal (help, export) is up
            return
        t = Text(no_wrap=True, overflow="ellipsis")
        keys = KEYS.get(ctx, [])
        if ctx == "tab-data" and not self.screen_stack[0].query_one("#detail").display:
            keys = [kl for kl in keys if kl != ("tab", "into detail")]  # Tab goes to the filter then
        for i, (k, label) in enumerate(keys):
            if i:
                t.append("   ")
            t.append(k, "bold")
            t.append(" " + label, self.dim)
        self.screen_stack[0].query_one("#keys", Static).update(t)

    def on_descendant_focus(self, event) -> None:
        self._render_keys()

    def on_click(self, event) -> None:
        """A click on a tab's number or name in a panel's top border switches to it."""
        tab = self._tab_at(event)
        if tab is not None:
            event.stop()
            self.action_tab(tab)

    def on_mouse_move(self, event) -> None:
        """Underline the tab name under the pointer, so it reads as clickable."""
        tab = self._tab_at(event)
        if tab != getattr(self, "_tab_hover", None):
            self._tab_hover = tab
            self._render_tab_titles()

    def on_leave(self, event) -> None:
        # the pointer left a panel (or the window) straight from its top border:
        # no further move arrives there, so drop the hover underline here
        if getattr(self, "_tab_hover", None) and getattr(event, "node", None) is not None \
                and event.node.has_class("tabbed"):
            self._tab_hover = None
            self._render_tab_titles()

    def on_screen_resume(self, event) -> None:
        self.call_after_refresh(self._render_keys)

    def on_descendant_blur(self, event) -> None:
        self.call_after_refresh(self._render_keys)

    # ------------------------------------------------------------ status line
    def _tick(self) -> None:
        if self._busy:
            self._spin = (self._spin + 1) % len(SPINNER)
            self._render_status()

    @_ui
    def _set_busy(self, key: str, label: str | None) -> None:
        if label is None:
            self._busy.pop(key, None)
        else:
            self._busy[key] = (label, time.time())
        self._render_status()

    def _render_status(self) -> None:
        try:
            grid = self.query_one(GridTable)
            out = self.query_one("#status", Static)
        except NoMatches:
            return
        d = self.dim
        t = Text(no_wrap=True, overflow="ellipsis")
        tot = self.total
        if self._last_error and self.ds.setup_error is not None:
            t.append("✗", "red")
            t.append(" DuckDB can't read this file", "bold")
            t.append("   reason: ", d)
            t.append(self._last_error)
            t.append("   → Schema (2) and Metadata (5) still work", d)
        elif self._last_error:
            t.append("✗", "red")
            t.append(" Query failed", "bold")
            t.append("   reason: ", d)
            t.append(self._last_error)
            t.append(f"   → {getattr(self, '_error_hint', 'edit with /')}  ·  previous view kept", d)
        elif self._busy:
            label, t0 = next(iter(self._busy.values()))
            t.append(SPINNER[self._spin], self.accent)
            t.append(F.sanitize(" " + label[:1].upper() + label[1:]))  # (labels can name columns)
            extra = []
            if grid.row_count:
                extra.append(f"first {grid.row_count:,} shown" if tot is None else f"{tot:,} rows")
            el = time.time() - t0
            if el >= 1:
                extra.append(f"{int(el // 60):02d}:{int(el % 60):02d} elapsed")
            if extra:
                t.append("   " + "  ·  ".join(extra), d)
        else:
            if tot == 0:
                t.append("!", "yellow")
                t.append(" No matching rows", "bold")
                t.append("   → x clears the filter", d)
            else:
                t.append("✓", "green")
                t.append(f" {tot:,} rows" if tot is not None else " rows")
                bits = []
                if not self.view.is_trivial and tot is not None and not self.view.sql:
                    bits.append(f"{F.percent(tot, self.ds.num_rows)} of {F.human_count(self.ds.num_rows)}")
                if self.view.sql:
                    bits.append("SQL result")
                if self.view.order_by:
                    c, desc = self.view.order_by[0]
                    bits.append(f"sorted {F.sanitize(c)} {'↓' if desc else '↑'}")
                if self._count_secs is not None and not self.view.is_trivial:
                    bits.append(f"{self._count_secs:.2f} s")
                if self.raw:
                    bits.append("raw values")
                if grid.row_count:
                    bits.append(f"row {grid.abs_row:,}")
                if bits:
                    t.append("  ·  " + "  ·  ".join(bits), d)
                if self._hidden_hint and grid.row_count:
                    t.append("   !", "yellow")
                    t.append(f" {F.sanitize(self._hidden_hint)} is hidden", "bold")
                    t.append(" · c to show", d)
        out.update(t)

    # --------------------------------------------------------------- the grid
    def _setup_formatters(self, schema: list[tuple[str, pa.DataType]]) -> None:
        self.formatters = {}
        for name, typ in schema:
            unit = ""
            if name in self.ds._by_name:
                unit = self.ds.column(name).unit
            self.formatters[name] = F.CellFormatter(name, typ, unit, self.col_formats.get(name))

    def _rebuild_columns(self, follow: bool = False) -> None:
        """Rebuild the grid's columns for ``cols_shown``. If they're the columns shown, the view
        keeps its horizontal place: the leftmost wholly visible scrollable column is remembered
        (by name: widths are refit for the new rows) and shown leftmost again by the next page
        (_apply_page). Clearing resets the scroll, so it's taken now. ``follow``: the columns
        were picked or hidden: keep the place even so, at the first column from the leftmost
        one rightwards that's still shown."""
        grid = self.query_one(GridTable)
        self._anchor_left, self._anchor_row = None, None
        empty, self._empty_left = self._empty_left, None
        old = [c.key.value for c in grid.ordered_columns]
        if old and (follow or old == list(self.cols_shown)):  # (an empty result's header counts too)
            first, last, hidden_left, _ = grid.column_window()
            shown = set(self.cols_shown)
            if empty is not None and not grid.row_count and grid.scroll_x == empty[1] and empty[0] in shown:
                self._anchor_left = empty[0]  # (the header of an empty result may not scroll that far)
            elif hidden_left and first <= last:
                self._anchor_left = next((n for n in old[first:] if n in shown), None)
        grid.clear(columns=True)
        for name in self.cols_shown:
            grid.add_column(self._column_label(name), key=name)

    def _column_label(self, name: str) -> Text:
        """Two-line header: name and sort arrow; type, unit and any format override."""
        typ = dict(self.result_schema).get(name, pa.string())
        unit = self.ds.column(name).unit if name in self.ds._by_name else ""
        order = dict(self.view.order_by)
        arrow = (" ↓" if order[name] else " ↑") if name in order else ""
        fm = self.formatters.get(name) or F.CellFormatter(name, typ)
        override = F.describe_override(fm.override, fm.kind)
        return Text.assemble((F.sanitize(name), "bold"), (arrow, "bold"), "\n",
                             (F.short_type(typ) + (f"·{F.sanitize(unit)}" if unit else ""), self.dim),
                             (f"·{override}" if override else "", self.dim),
                             justify="right" if fm.right else "left")

    def load_window(self, offset: int, cursor_abs: int, column: int | None = None, *,
                    keep: bool = False, around: tuple[int, int] | None = None) -> None:
        """Load the window of rows at ``offset`` (in a worker), cursor on row ``cursor_abs``.

        Decoding cost follows the columns read, so when the view's rows have file
        row numbers only the pinned columns and those within a screen of the view
        are fetched; the others show placeholders until they come near the view
        (``_ensure_columns``) or the Details pane needs them. A plain view's window
        is read whole if that is cheap anyway (``ParquetDataset.window_cost``).
        A new window cancels any column fetch still running for the old one.

        ``keep``: this load is on the way to a kept record (``_keep``); any other drops it (the
        user went elsewhere). ``around``: (file row, position) of a row the window holds, for
        views ``ParquetDataset.fetch_around`` serves (found by file row: no scan up to it)."""
        if self._keep is not None and not keep:
            self._drop_keep("the view moved before the record was found")
        if self.ds.setup_error is not None:  # no query can run: say why (once), don't try
            self._show_error(self.ds.setup_error)
            return
        self._page_gen += 1
        self._cancel_column_fetches()
        shown = list(self.cols_shown)
        cols = None
        grid = self.query_one(GridTable)
        if self.ds.has_row_ids(self.view) and len(grid.ordered_columns) == len(shown):
            target = grid.cursor_column if column is None else column
            left = shown.index(self._anchor_left) if self._anchor_left in shown else None
            near = grid.columns_near(min(target, len(shown) - 1), width=self.size.width, left=left)
            if len(near) < len(shown):
                cols = [shown[i] for i in near]
            if cols and self.view.is_trivial:  # (a filter or sort scans whatever columns it reads: always worth it)
                full = self.ds.window_cost(max(0, offset), grid.window, shown)
                part = self.ds.window_cost(max(0, offset), grid.window, cols)
                if full is None or part is None or full - part < LAZY_MIN_SAVING_MS:
                    cols = None  # cheap enough to read whole: no placeholders, no fetches while scrolling
        self._load_window(offset, cursor_abs, column, shown, cols, self._page_gen, around)

    @work(thread=True, exclusive=True, group="page")
    def _load_window(self, offset: int, cursor_abs: int, column: int | None, shown: list[str],
                     cols: list[str] | None, gen: int, around: tuple[int, int] | None = None) -> None:
        grid = self.query_one(GridTable)
        view = self.view
        offset = max(0, offset)
        self.call_from_thread(self._set_busy, "page", "loading rows")
        try:
            with self.ds.tagged("page"):
                if around is not None and self.ds.can_fetch_around(view):
                    page = self.ds.fetch_around(view, *around, offset, grid.window, cols or shown)
                else:
                    page = self.ds.fetch(view, offset, grid.window, cols or shown)
        except duckdb.InterruptException:
            self.call_from_thread(self._page_failed, gen)
            return
        except Exception as e:  # noqa: BLE001
            self.call_from_thread(self._show_error, e)
            self.call_from_thread(self._page_failed, gen)
            return
        finally:
            self.call_from_thread(self._set_busy, "page", None)
        if view is not self.view:
            self.call_from_thread(self._page_failed, gen)
            return
        if cols is not None:
            page = _with_placeholders(page, shown, dict(self.result_schema))
        self.call_from_thread(self._apply_page, page, cursor_abs, column, gen)

    @_ui
    def _page_failed(self, gen: int) -> None:
        """A window load came to nothing (failed, cancelled, or its view changed meanwhile). If it
        was the newest, the page on screen stays, and its columns load as before."""
        if gen == self._page_gen:
            self._anchor_left, self._anchor_row = None, None
            if self._keep is not None:
                self._drop_keep("its page didn't load")  # (the page on the way to it didn't come)
            self._page_gen += 1
            self._shown_gen = self._page_gen
            self._ensure_columns()  # load_window cancelled the page's column fetches: start again
            self._update_detail()  # (re-arms the pane's fetch, if it's shown)

    @_ui
    def _apply_page(self, page, cursor_abs: int, column: int | None, gen: int | None = None) -> None:
        if gen is not None and gen != self._page_gen:
            return  # a newer window is on its way (or already shown): this one is superseded
        grid = self.query_one(GridTable)
        if list(page.columns) != [c.key.value for c in grid.ordered_columns]:
            # not the columns the grid was built for (validate's schema): its rows would read cells
            # of columns they don't have. Say so and keep what's shown.
            self._show_error(ValueError(f"the query returned other columns than expected "
                                        f"({len(page.columns)}, not {len(grid.ordered_columns)})"))
            self._page_failed(gen if gen is not None else self._page_gen)
            return
        col = grid.cursor_column if column is None else column
        scroll_x = grid.scroll_x
        # a new view of the same columns: back to its leftmost column, and its record to its screen
        # row (a record that's looked up goes there when it's found: _land_keep)
        left, self._anchor_left = self._anchor_left, None
        row_at = None
        if self._keep is None:
            row_at, self._anchor_row = self._anchor_row, None
        grid.clear()
        grid.offset = page.offset
        fm = [self.formatters.get(n) or F.CellFormatter(n, t) for n, t in zip(page.columns, page.types)]
        # cells format themselves when first drawn (pqx.cells); widths come from a sample, below
        dim, off = self.dim, page.offset
        labels = [Text(f"{off + i if n is None else n:,}", style=dim) for i, n in enumerate(page.row_numbers)]
        grid.set_rows(page.rows, labels, grid.column_cells(fm, self.raw))
        if page is not self.page:
            self._cols_failed = set()
        self.page = page
        self._shown_gen = self._page_gen if gen is None else gen
        self._fit_columns()
        self._reserve_widths()
        grid.update_dimensions_now()  # so the cursor scrolls into view now, not after a first draw at the top
        if page.rows:
            r = max(0, min(len(page.rows) - 1, cursor_abs - page.offset))
            if left is not None and left in page.columns:
                scroll_x = grid.scroll_x_for(page.columns.index(left))
            with grid.sizing():  # fit what's on screen once, where the scrolling ends
                # first: the cursor must end up in view, wherever this was (if its column doesn't
                # fit beside the leftmost one, the view scrolls just enough to show it). No scroll
                # from move_cursor itself: it would first scroll to the cleared cursor's cell (0, 0),
                # losing the place, before scrolling to the new one.
                grid.scroll_x = scroll_x
                grid.move_cursor(row=r, column=min(col, max(0, len(self.cols_shown) - 1)), animate=False,
                                 scroll=False)
            grid.scroll_cursor_fitted()
            if row_at is not None:
                grid.show_cursor_at(row_at)
        else:  # (no rows: the header keeps the place, for the next view)
            if left is not None and left in page.columns:
                scroll_x = grid.scroll_x_for(page.columns.index(left))
            grid.scroll_x = scroll_x
            grid.fit_visible()
            if left is not None:
                self._empty_left = (left, grid.scroll_x)
        if self.total is None and len(page.rows) < grid.window:
            self.total = page.offset + len(page.rows)  # hit the end: we now know the size
            grid.total = self.total
        if not self._placeholder_set and page.rows and not self.view.sql.strip():
            self._placeholder_set = True  # once, from the file's first page (not a query's)
            self._set_filter_placeholder(page)
        self._render_status()
        self._update_detail()
        self.call_after_refresh(self._render_hscroll)
        self._ensure_columns()
        if self._keep is not None and self._keep[0] is self.view:
            self._keep_record(page)

    def _set_filter_placeholder(self, page) -> None:
        # Only names DuckDB calls by the same name: one that differs from another column only by
        # case is renamed (ParquetDataset.sql_name), which isn't known (without waiting on DuckDB's
        # setup, on this thread) until the views exist. Such a name makes a poor example anyway.
        lower: dict[str, int] = {}
        for c in self.ds.column_names:
            lower[c.lower()] = lower.get(c.lower(), 0) + 1
        cols = [(c, t, v) for c, t, v in zip(page.columns, page.types, page.rows[0])
                if lower.get(c.lower()) == 1 and c in self.ds._by_name]
        names, types, row = (list(x) for x in zip(*cols)) if cols else ([], [], [])
        self.query_one("#filter", Input).placeholder = filter_placeholder(names, types, tuple(row))

    def _keep_record(self, page) -> None:
        """A page of a view with a kept record (``_keep``) is shown: the view's first page (at its
        top), or the page the record was found on. Put the cursor on the record if the page holds
        it; else look it up (after the first page: it's shown at once, whatever the lookup takes)."""
        view, file_row = self._keep
        try:
            r = page.row_numbers.index(file_row)
        except ValueError:
            r = None
        if r is not None and (self._keep_phase == "seeking" or self._room_around(r)):
            self._land_keep(r)
        elif r is not None:  # on the page, but too near its edge to show it where it was on screen
            self._seek_record(page.offset + r)
        elif self._keep_phase == "first" and len(page.rows) >= self.query_one(GridTable).window:
            self._keep_phase = "locating"
            self._locate_seq += 1
            self._locate_record(view, file_row, self._locate_seq)
            self._update_detail()
        else:
            self._drop_keep("the record isn't in the view")

    def _land_keep(self, r: int) -> None:
        """The kept record is on the page, at row ``r``: the cursor goes there, then the keys
        pressed while it was on its way act on it."""
        self._keep, self._keep_phase, self._keep_values = None, "", {}
        queue, self._keep_queue = self._keep_queue, []
        grid = self.query_one(GridTable)
        grid.move_cursor(row=r, animate=False)
        if self._anchor_row is not None:  # on the screen row it was on (or as near as the view allows)
            grid.show_cursor_at(self._anchor_row)
            self._anchor_row = None
        self._update_detail()
        for item in queue:
            if self._keep is not None:  # an = among them is making a new view: the rest wait for it
                self._keep_queue.append(item)
                continue
            action, args, name, _ = item
            if self._move_grid_to_column(name):
                getattr(self, f"action_{action}")(*args)

    def _drop_keep(self, why: str, moved: bool = False) -> None:
        """Give up on the kept record (the user went elsewhere, or it wasn't found or loaded):
        the cursor stays where it is, and keys waiting for the record aren't applied (``why``
        says so). ``moved``: the user moved the cursor, so the record's page, if it's on its way,
        must not take it back."""
        phase, queue = self._keep_phase, self._keep_queue
        self._keep, self._keep_phase, self._keep_values, self._keep_queue = None, "", {}, []
        self._anchor_row = None
        if phase == "locating":
            self.ds.interrupt("locate")
        elif phase == "seeking" and moved:
            self._page_failed(self._page_gen)  # (the page on screen stays)
        if queue:
            keys = " ".join(k for *_, k in queue)
            self.notify(f"{keys} not applied: {why}", severity="warning", timeout=3, markup=False)
        self._update_detail()

    def _record_values(self, file_row: int) -> dict:
        """The values (by column) of file row ``file_row`` on the page shown, if it's there."""
        page = self.page
        if page is None or file_row not in page.row_numbers:
            return {}
        return dict(zip(page.columns, page.rows[page.row_numbers.index(file_row)]))

    def _queue_for_keep(self, action: str, key: str, *args) -> bool:
        """While the cursor is on its way to a kept record, a cell key waits to act on that record
        (not on the row the cursor is on meanwhile). True if it was queued."""
        if self._keep is None:
            return False
        grid = self.query_one(GridTable)
        if grid.cursor_column < len(self.cols_shown):
            self._keep_queue.append((action, args, self.cols_shown[grid.cursor_column], key))
        return True

    @work(thread=True, exclusive=True, group="locate")
    def _locate_record(self, view: View, file_row: int, seq: int) -> None:
        """Find file row ``file_row``'s position in ``view`` (a scan of the file: off the UI
        thread, after the first page, and cancelled by Esc or a newer view)."""
        self.call_from_thread(self._set_busy, "locate", "finding record")
        pos, why = None, "the record isn't in the view"
        try:
            with self.ds.tagged("locate"):
                pos = self.ds.find_row(view, file_row)
        except duckdb.InterruptException:
            why = "finding the record was cancelled"
        except Exception:  # noqa: BLE001  (failed: the cursor stays where it is)
            why = "finding the record failed"
        self.call_from_thread(self._record_located, view, seq, pos, why)

    @_ui
    def _record_located(self, view: View, seq: int, pos: int | None, why: str = "") -> None:
        if seq != self._locate_seq:
            return  # a newer lookup is running (and owns the busy flag)
        self._set_busy("locate", None)
        if self._keep is None or self._keep[0] is not view or view is not self.view:
            return  # given up on meanwhile
        if pos is None:
            self._drop_keep(why)
            return
        grid = self.query_one(GridTable)
        if grid.offset <= pos < grid.offset + grid.row_count and self._room_around(pos - grid.offset):
            self._land_keep(pos - grid.offset)
            return
        self._seek_record(pos)

    def _room_around(self, r: int) -> bool:
        """Whether the window holds the rows to show its row ``r`` (the kept record) on its screen
        row (``_anchor_row``): those above it, and a screen's worth below unless the view ends."""
        grid = self.query_one(GridTable)
        want = self._anchor_row
        if want is None:
            return True
        above = r >= want or grid.offset == 0
        below = grid.row_count - r >= grid.visible_rows() - want or not grid.more_below()
        return above and below

    def _seek_record(self, pos: int) -> None:
        """Load the window around the kept record, at position ``pos`` of the view."""
        grid = self.query_one(GridTable)
        self._keep_phase = "seeking"
        offset = max(0, pos - grid.window // 2)
        if self.total is not None:
            offset = max(0, min(offset, self.total - grid.window))
        self.load_window(offset, pos, None, keep=True, around=(self._keep[1], pos))

    def _fit_columns(self, columns: list[int] | None = None) -> None:
        """Size the grid's columns (default: all) for the window, formatting as few cells as possible.

        A number or string column fits its likely widest values
        (``widest_candidates``); any other column fits a sample of rows spread
        through the window. The cells on screen are fitted before each draw
        (``GridTable.fit_visible``), so a cell wider than this guess widens its
        column before it is shown. Columns not loaded yet are left alone."""
        grid = self.query_one(GridTable)
        n = grid.row_count
        page = self.page
        if not n or page is None:
            return
        rows = page.rows
        missing = page.missing
        sampled = []
        with grid.sizing():
            for i in range(len(grid.cell_columns)) if columns is None else columns:
                if i >= len(grid.cell_columns) or (i < len(page.columns) and page.columns[i] in missing):
                    continue
                cc = grid.cell_columns[i]
                guess = widest_candidates([r[i] for r in rows], cc.fmt.kind, cc.raw) if i < len(page.columns) else None
                if guess is None:
                    sampled.append(i)
                else:
                    cc.fit_values(guess)
            if sampled:
                grid.fit_columns(range(0, n, max(1, n // WIDTH_SAMPLE_ROWS)), sampled)

    # ------------------------------------------------------------ lazy columns
    def _cancel_column_fetches(self) -> None:
        """Forget and interrupt the column fetches for the page on screen (a new one is coming).
        Tags don't interrupt each other, so each is interrupted here."""
        tags = set(LAZY_TAGS) | set(self._inflight)
        self._inflight.clear()
        self._cell_waiters.clear()
        for tag in tags:
            self.ds.interrupt(tag)

    def _lazy_ready(self) -> bool:
        """Whether the page on screen has columns to load and no newer page is on its way."""
        page = self.page
        return page is not None and bool(page.missing) and self._shown_gen == self._page_gen

    def _ensure_columns(self) -> None:
        """Fetch the columns near the view that the page on screen doesn't have yet.

        When a column within a screen of the view is missing, the missing ones
        within two screens are fetched. One fetch at a time: while one runs,
        columns it brings that are still near the view are left to it (and the
        rest fetched when it's done); if none are, a fresh fetch replaces it (the
        same tag interrupts the old one)."""
        if not self._lazy_ready():
            return
        page = self.page
        grid = self.query_one(GridTable)
        if len(grid.ordered_columns) != len(page.columns):
            return

        coming = set()  # what the Details pane's fetch will bring anyway
        detail = self._inflight.get("detail")
        if detail is not None and detail[1] == self._page_gen:
            coming = detail[2]

        def missing(screens):
            return [n for n in (page.columns[i] for i in grid.columns_near(screens=screens))
                    if n in page.missing and n not in self._cols_failed and n not in coming]
        need = missing(1)
        if not need:
            return
        cur = self._inflight.get("cols")
        if cur is not None and cur[1] == self._page_gen and cur[2] & set(need):
            return  # its completion checks again
        # a screen further than needed, so scrolling sideways fetches about once a screen, not every column
        self._fetch_columns("cols", missing(2))

    def _fetch_columns(self, tag: str, names: list[str], then=None) -> None:
        """Fetch ``names`` for the page on screen under ``tag`` and merge them into it;
        then call ``then()`` (if the page is still on screen)."""
        self._fetch_seq += 1
        self._inflight[tag] = (self._fetch_seq, self._page_gen, frozenset(names))
        self._columns_worker(tag, self._fetch_seq, self._page_gen, self.page, list(names), then)

    @work(thread=True, group="columns")
    def _columns_worker(self, tag: str, seq: int, gen: int, page, names: list[str], then) -> None:
        self.call_from_thread(self._set_busy, f"columns-{tag}", "loading columns")
        got, err = None, None
        try:
            with self.ds.tagged(tag):
                got = self.ds.fetch_columns(page.row_numbers, names)
        except duckdb.InterruptException:
            pass
        except Exception as e:  # noqa: BLE001
            err = e
        finally:
            self.call_from_thread(self._set_busy, f"columns-{tag}", None)
        self.call_from_thread(self._columns_done, tag, seq, gen, page, names, got, err, then)

    @_ui
    def _columns_done(self, tag: str, seq: int, gen: int, page, names: list[str], got, err, then) -> None:
        cur = self._inflight.get(tag)
        latest = cur is not None and cur[0] == seq
        if latest:
            del self._inflight[tag]
        current = gen == self._page_gen and page is self.page
        if latest and (got is None or not current):
            self._cell_waiters.pop(tag, None)  # the actions waiting on it are dropped with it
        if latest and current and got is None and err is None:
            self._cols_cancelled = True  # not retried by itself (Esc means stop), but on the next move
        if err is not None and current:
            self._cols_failed.update(names)
            self._mark_unavailable(page, names)
            self.notify(f"Couldn't load {len(names)} column{'s' if len(names) != 1 else ''}: "
                        f"{F.sanitize((str(err).splitlines() or [''])[0][:200])}", title="✗ Columns",
                        severity="error", timeout=6, markup=False)
        if got is None or not current:
            return  # cancelled (Esc, a newer fetch, a new page) or superseded: nothing to merge
        self._merge_columns(page, got)
        if then is not None:
            then()
        self._ensure_columns()

    def _mark_unavailable(self, page, names: list[str]) -> None:
        """Show the columns ``names`` that failed to load for ``page`` as such (✗), not as loading.
        They stay missing (and aren't fetched again for this page)."""
        idx = {n: i for i, n in enumerate(page.columns)}
        cols = [idx[n] for n in names if n in idx and n in page.missing]
        if not cols:
            return
        rows = page.rows
        for r in range(len(rows)):
            vals = list(rows[r])
            for i in cols:
                if vals[i] is MISSING:
                    vals[i] = UNAVAILABLE
            rows[r] = tuple(vals)
        grid = self.query_one(GridTable)
        if grid.row_count == len(rows) and len(grid.cell_columns) == len(page.columns):
            keys = [ColumnKey(page.columns[i]) for i in cols]
            data, locations = grid._data, grid._row_locations
            for r in range(len(rows)):
                row = data.get(locations.get_key(r))
                if isinstance(row, CellRow):
                    row.set_values(rows[r], keys)
            grid.invalidate_columns(keys)
        self._update_detail()

    def _merge_columns(self, page, got) -> None:
        """Put the fetched columns ``got`` (for ``page``'s rows) into the page and the grid.
        Only columns still missing are filled: loaded values are never replaced."""
        idx = {n: i for i, n in enumerate(page.columns)}
        pairs = [(idx[n], j) for j, n in enumerate(got.columns) if n in page.missing and n in idx]
        if not pairs or len(got.rows) != len(page.rows):
            return
        got_cols = list(zip(*got.rows))
        rows = page.rows
        for r in range(len(rows)):
            vals = list(rows[r])
            for i, j in pairs:
                vals[i] = got_cols[j][r]
            rows[r] = tuple(vals)
        for i, j in pairs:
            page.types[i] = got.types[j]
        names = [page.columns[i] for i, _ in pairs]
        page.missing.difference_update(names)
        grid = self.query_one(GridTable)
        if grid.row_count == len(rows) and len(grid.cell_columns) == len(page.columns):
            keys = [ColumnKey(n) for n in names]
            data, locations = grid._data, grid._row_locations
            for r in range(len(rows)):
                row = data.get(locations.get_key(r))
                if isinstance(row, CellRow):
                    row.set_values(rows[r], keys)
            was_in_view = grid.cursor_cell_in_view()
            growth = grid._growth
            self._fit_columns([i for i, _ in pairs])
            if grid._growth != growth:
                grid.invalidate_cells()  # widths changed: everything moves
            else:
                grid.invalidate_columns(keys)  # (the usual case, with reserved widths)
            grid.fit_visible()
            if was_in_view and not grid.cursor_cell_in_view():
                grid._scroll_cursor_into_view()  # a column left of the cursor outgrew its reserve
            self.call_after_refresh(self._render_hscroll)
        self._update_detail()

    def _reserve_widths(self, columns: list[int] | None = None) -> None:
        """Make the not-yet-loaded columns (of ``columns``, default all) as wide as their
        values will likely be, so the grid doesn't shift when they arrive: from the
        min/max statistics of the row groups the page comes from, else from the kind."""
        page, grid = self.page, self.query_one(GridTable)
        if page is None or not page.missing or len(grid.cell_columns) != len(page.columns):
            return
        rgs = None
        with grid.sizing():
            for i in range(len(page.columns)) if columns is None else columns:
                name = page.columns[i]
                if name not in page.missing:
                    continue
                if rgs is None:
                    rgs = self._page_row_groups(page)
                cc = grid.cell_columns[i]
                cc.fit(self._reserved_width(name, cc.fmt, cc.raw, rgs))

    def _page_row_groups(self, page, limit: int = 8) -> list[int]:
        """Row groups holding the page's rows (at most ``limit``, spread out)."""
        starts = self.ds._rg_starts()
        rns = [r for r in page.row_numbers if r is not None]
        if not starts or not rns:
            return []
        rgs = sorted({bisect_right(starts, r) - 1 for r in (rns if len(rns) <= 4096 else rns[::len(rns) // 4096])})
        if len(rgs) > limit:
            rgs = [rgs[round(k * (len(rgs) - 1) / (limit - 1))] for k in range(limit)]
        return rgs

    def _chunk_min_max(self, rg: int, leaf: int) -> tuple:
        key = (rg, leaf)
        hit = self._chunk_stats.get(key)
        if hit is None:
            hit = ()
            try:
                st = self.ds.meta.row_group(rg).column(leaf).statistics
                if st is not None and st.has_min_max:
                    hit = (st.min, st.max)
            except Exception:  # noqa: BLE001 - statistics pyarrow can't decode: none
                pass
            self._chunk_stats[key] = hit
        return hit

    def _reserved_width(self, name: str, fm: F.CellFormatter, raw: bool, rgs: list[int]) -> int:
        """How wide ``name``'s values likely format: its row groups' min and max (for a
        float kind with all its significant digits, as a real value has), else a typical
        value of its kind. Capped at the width a long string is cut to."""
        leaves = self.ds._leaves().get(name, [])
        vals: list = []
        if len(leaves) == 1:
            for rg in rgs:
                vals.extend(self._chunk_min_max(rg, leaves[0]))
        if fm.kind in F.SIG_DIGITS:
            floats = [v for v in vals if isinstance(v, float) and math.isfinite(v)]
            vals = [math.copysign(1.2345678901234567, v) * 10.0 ** math.floor(math.log10(abs(v)))
                    if isinstance(v, float) and math.isfinite(v) and v else v for v in vals]
            if floats and min(floats) < 0 < max(floats):  # values near zero, with leading zeros
                vals += [-0.012345678901234567, 0.012345678901234567]
        if not vals and fm.kind in KIND_SAMPLES:
            vals = [KIND_SAMPLES[fm.kind]]
        width = 0
        for v in vals:
            try:
                width = max(width, text_width(fm.plain(v, raw)))
            except Exception:  # noqa: BLE001 - a statistic of an unexpected type: no guess from it
                pass
        return min(width, 40)

    @on(GridTable.HScroll)
    def _hscroll(self) -> None:
        self._debounced("hscroll", 0.03, self._render_hscroll)
        self._ensure_columns()

    @_ui
    def _render_hscroll(self) -> None:
        """Column-position readout in the data panel's bottom border + edge markers."""
        grid = self.query_one(GridTable)
        n = len(grid.ordered_columns)
        first, last, hl, hr = grid.column_window()
        self.query_one("#more-left", EdgeMarker).set_hidden(hl, grid.header_height)
        self.query_one("#more-right", EdgeMarker).set_hidden(hr, grid.header_height)
        panel = self.query_one("#data-panel")
        if not (hl or hr):
            panel.border_subtitle = ""
            return
        pinned = min(grid.fixed_columns, n)
        parts = []
        if hl:
            parts.append(f"[bold]‹[/] {hl}")
        rng = f"columns {first + 1}–{last + 1} of {n}" if last >= first else f"{n} columns"
        if pinned:
            rng += f" · {pinned} pinned"
        parts.append(self._dim_markup(rng))
        if hr:
            parts.append(f"{hr} [bold]›[/]")
        panel.border_subtitle = self._dim_markup("  ·  ").join(parts)

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
        # the cursor waits for a kept record at the top of the new view: moving off it is the
        # user's (the move to the record clears _keep first)
        if self._keep is not None and self.query_one(GridTable).abs_row != 0:
            self._drop_keep("the cursor moved before the record was found", moved=True)
        # only the Data tab's own moves count: page loads behind another tab don't
        if self._tab_is("tab-data") and self.page is not None:
            grid = self.query_one(GridTable)
            if grid.cursor_column < len(self.page.columns):
                self.set_current_column(self.page.columns[grid.cursor_column], "grid")
        self._hidden_hint = None
        self._render_status()
        self._update_detail()
        if self._cols_cancelled:  # columns left loading by a cancelled fetch (Esc): load them now
            self._cols_cancelled = False
            self._ensure_columns()

    @on(DataTable.CellSelected, "#grid")
    def cell_selected(self) -> None:
        self.action_toggle_detail()

    @on(DataTable.HeaderSelected, "#grid")
    def header_selected(self, event: DataTable.HeaderSelected) -> None:
        self._sort_by(str(event.column_key.value))

    def _cursor_value(self):
        """``(column, value)`` under the grid cursor; the value is ``MISSING`` if the column isn't loaded yet."""
        grid = self.query_one(GridTable)
        if self.page is None or not self.page.rows:
            return None, None
        r = grid.cursor_row
        c = grid.cursor_column
        if r >= len(self.page.rows) or c >= len(self.page.columns):
            return None, None
        return self.page.columns[c], self.page.rows[r][c]

    def _with_cursor_value(self, fn) -> None:
        """Call ``fn(column, value)`` with the value under the cursor, loading its column first if need be."""
        name, v = self._cursor_value()
        if name is None:
            return
        if v is UNAVAILABLE:
            self.notify(f"{F.sanitize(name)} couldn't be loaded for these rows", severity="error", timeout=4,
                        markup=False)
            return
        if v is not MISSING:
            fn(name, v)
            return
        page, r, c = self.page, self.query_one(GridTable).cursor_row, self.query_one(GridTable).cursor_column

        def then():
            if self.page is page and page.rows[r][c] is not MISSING and page.rows[r][c] is not UNAVAILABLE:
                fn(name, page.rows[r][c])
        # one fetch per column: a second action on it while it loads waits for the same fetch
        tag = f"cell:{name}"
        waiting = self._cell_waiters.setdefault(tag, [])
        waiting.append(then)
        cur = self._inflight.get(tag)
        if cur is not None and cur[1] == self._page_gen:
            return
        self._fetch_columns(tag, [name], lambda: [f() for f in self._cell_waiters.pop(tag, [])])

    # ---------------------------------------------------------- current column
    def set_current_column(self, name: str | None, source: str) -> None:
        """Record the column the user is on. ``source`` is the view it came from:
        "grid", "detail", "schema" or "stats". Each view moves to it when shown."""
        if name:
            self.current_column = name

    def _move_grid_to_column(self, name: str) -> bool:
        """Put the grid cursor on column ``name``, same row; False if it isn't shown."""
        if name not in self.cols_shown:
            return False
        grid = self.query_one(GridTable)
        col = self.cols_shown.index(name)
        if grid.cursor_column != col:
            grid.move_cursor(column=col, animate=False)
        return True

    # ------------------------------------------------------------ detail pane
    def action_toggle_detail(self) -> None:
        d = self.query_one("#detail")
        d.display = not d.display
        if not d.display and self.query_one(TabbedContent).active == "tab-data":
            self.query_one(GridTable).focus()
        self._update_detail()
        self._render_keys()

    def _busy_with_user_work(self) -> bool:
        """Is something running that the user asked for (a lookup, stats, a plot, an
        export), rather than pqx's own loading of rows, columns or the row count?"""
        return any(k not in ("page", "count") and not k.startswith("columns-") for k in self._busy)

    def action_detail_to_grid(self, close: bool = False) -> None:
        """Enter or Tab in the pane: back to the grid, on the selected column. Esc
        (``close``) does the same and closes the pane, unless something the user
        asked for is running: then, as everywhere else, it only cancels that."""
        if close and self._busy_with_user_work():
            self.action_escape()
            return
        name = self.query_one(DetailList).selected
        if name:
            self._move_grid_to_column(name)
        if close:
            self.query_one("#detail").display = False
            self._update_detail()
            self._render_keys()
        self.query_one(GridTable).focus()

    def action_detail_key(self, action: str, *args) -> None:
        """A grid key pressed in the Details pane (=, y, i, F, < >): the same action on the
        pane's field of the record it shows, i.e. the grid's cell there. Focus stays in the
        pane (the format dialog hands it back when it closes; i goes to Stats)."""
        name = self.query_one(DetailList).selected
        if not name or not self._move_grid_to_column(name):
            return
        getattr(self, f"action_{action}")(*args)

    @on(OptionList.OptionHighlighted, "#detail-list")
    def detail_highlighted(self, event: OptionList.OptionHighlighted) -> None:
        # only the user's own moves in the pane drive the grid. The pane
        # following the grid must not echo back, even if its event arrives
        # after the pane got focus: by then it is stale or names the grid's
        # own column.
        lst = self.query_one(DetailList)
        name = event.option.id
        grid = self.query_one(GridTable)
        on_grid = self.cols_shown[grid.cursor_column] if grid.cursor_column < len(self.cols_shown) else None
        if self.focused is not lst or not name or event.option_index != lst.highlighted or name == on_grid:
            return
        self.set_current_column(name, "detail")
        self._move_grid_to_column(name)

    def _update_detail(self) -> None:
        d = self.query_one("#detail")
        if not d.display or self.page is None:
            return
        if not self.page.rows:
            d.border_title = self._dim_markup("no rows")
            self.query_one(DetailList).set_entries([], 0)
            return
        grid = self.query_one(GridTable)
        r = min(grid.cursor_row, len(self.page.rows) - 1)
        row = self.page.rows[r]
        rn = self.page.row_numbers[r]
        cur_col = self.page.columns[grid.cursor_column] if grid.cursor_column < len(self.page.columns) else None
        if self._keep is not None:  # the record the cursor is on its way to, not the row it's on meanwhile
            rn = self._keep[1]
            row = tuple(self._keep_values.get(n, MISSING) for n in self.page.columns)
        entries = []
        for name, typ, v in zip(self.page.columns, self.page.types, row):
            if v is MISSING:  # loading (_fetch_detail_columns, below)
                entries.append((name, Text(PLACEHOLDER, style=self.dim)))
                continue
            if v is UNAVAILABLE:
                entries.append((name, Text.assemble((FAILED_MARK, "red"), (" couldn't load", self.dim))))
                continue
            fmt = self.formatters.get(name) or F.CellFormatter(name, typ)
            full = F.format_value(F.shortest(v, typ), fmt.kind, raw=True, width=0)
            if len(full) > 300:  # keep one huge JSON/blob from burying every other column
                full = full[:300] + f"… ({len(full):,} chars; y copies it all)"
            cell = Text(full, style=self.dim if v is None else "")
            info = self.ds._by_name.get(name)
            if info is not None and info.unit and v is not None:
                cell.append(f"  {F.sanitize(info.unit)}", self.dim)
            extra = F.derived(name, fmt.kind, v, info.unit if info is not None else "")
            if extra:
                cell.append("\n· " + extra, self.dim)
            entries.append((name, cell))
        if self._keep is not None:
            d.border_title = self._dim_markup(f"finding record… · file row {rn:,}")
        else:
            d.border_title = self._dim_markup(f"row {grid.abs_row:,}" + (f" · file row {rn:,}" if rn is not None else ""))
        lst = self.query_one(DetailList)
        lst.set_entries(entries, min(22, max((len(n) for n, _ in entries), default=0)))
        lst.select(cur_col)
        if self.page.missing - self._cols_failed and self._keep is None:
            self._debounced("detail-columns", DETAIL_FETCH_DELAY, self._fetch_detail_columns)

    def _fetch_detail_columns(self) -> None:
        """The pane shows every column: fetch all the page's missing ones, once per page
        (they're merged into the page, so the grid has them too, and every row of it)."""
        if not self._lazy_ready() or not self.query_one("#detail").display:
            return
        cur = self._inflight.get("detail")
        if cur is not None and cur[1] == self._page_gen:
            return
        page = self.page
        names = [n for n in page.columns if n in page.missing and n not in self._cols_failed]
        if names:
            self._fetch_columns("detail", names)

    # --------------------------------------------------------------- filtering
    def action_focus_filter(self) -> None:
        self.query_one("#filter", Input).focus()

    @on(Input.Changed, "#filter")
    def filter_changed(self, event: Input.Changed) -> None:
        lab = self.query_one("#filter-mode", Label)
        sql = is_sql_query(event.value)
        lab.update("sql ›" if sql else "›")
        lab.set_class(sql, "sql")
        self.query_one("#filterbox").remove_class("error")

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
        if self.ds.setup_error is not None:  # no query can run: say why (once), don't try
            self.call_from_thread(self._show_error, self.ds.setup_error)
            return
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

    @_ui
    def _set_view(self, view: View, schema, keep_file_row: int | None = None) -> None:
        inp = self.query_one("#filter", Input)
        self.query_one("#filterbox").remove_class("error")
        if self.focused is inp and self.query_one(TabbedContent).active == "tab-data":
            self.query_one(GridTable).focus()
        self._last_error = ""
        self._count_secs = None
        self._hidden_hint = None
        was_sql = bool(self.view.sql)
        self.view = view
        grid = self.query_one(GridTable)
        # the record's screen row (of a record still on its way, where it's to go)
        row_at = self._anchor_row if self._keep is not None and self._keep[1] == keep_file_row else None
        if row_at is None and keep_file_row is not None and grid.row_count:
            row_at = grid.screen_row()
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
        # the cursor stays on the same record: in a plain view, the file row is its position; in a
        # filtered or sorted one, it's looked up once the first page is shown (_keep_record)
        target = 0
        # keys waiting for a record still on its way wait on if this view keeps the same record
        queue, values = [], {}
        if self._keep is not None:
            if self._keep[1] == keep_file_row:
                queue, values, self._keep_queue = self._keep_queue, self._keep_values, []
            self._drop_keep("the view changed before the record was found")  # (a record kept for the old view)
        if keep_file_row is not None and not values:
            values = self._record_values(keep_file_row)
        if keep_file_row is not None:
            if view.is_trivial:
                target = keep_file_row
                self._anchor_row = row_at
            elif self.ds.has_row_ids(view):
                self._keep, self._keep_phase = (view, keep_file_row), "first"
                self._keep_queue, self._keep_values = queue, values
                self._anchor_row = row_at
                queue = []
        if queue:
            self.notify(f"{' '.join(k for *_, k in queue)} not applied: the record isn't kept in this view",
                        severity="warning", timeout=3, markup=False)
        # stay on the current column if the new view shows it
        col = self.cols_shown.index(self.current_column) if self.current_column in self.cols_shown else 0
        self.load_window(max(0, target - grid.window // 2), target, col, keep=True)
        if not view.is_trivial:
            self.count_rows()
        self._refresh_analysis()
        self._render_status()

    @work(thread=True, exclusive=True, group="count")
    def count_rows(self) -> None:
        view = self.view
        self.call_from_thread(self._set_busy, "count", "counting rows")
        t0 = time.time()
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
            self.call_from_thread(self._set_total, n, time.time() - t0)

    @_ui
    def _set_total(self, n: int, secs: float | None = None) -> None:
        self.total = n
        self._count_secs = secs
        self.query_one(GridTable).total = n
        self._render_status()

    def action_clear_filter(self) -> None:
        inp = self.query_one("#filter", Input)
        if not inp.value and self.view.is_trivial:
            return
        fr = None
        if self._keep is not None:  # (on its way to a record: that one)
            fr = self._keep[1]
        elif self.page and self.page.rows:
            fr = self.page.row_numbers[min(self.query_one(GridTable).cursor_row, len(self.page.rows) - 1)]
        inp.value = ""
        self.view = View(sql=self.view.sql, where=self.view.where)  # sort is dropped with the filter
        self.apply_filter("", fr)
        # focusing the grid from another tab would switch to Data (TabbedContent follows focus)
        self.query_one(GridTable).focus() if self._tab_is("tab-data") else self.set_focus(None)

    def action_clear_filter_anywhere(self) -> None:
        """ctrl+x: clear the filter even while typing in the filter box."""
        inp = self.query_one("#filter", Input)
        if isinstance(self.focused, Input) and self.focused is inp and inp.value and self.view.is_trivial:
            inp.value = ""  # only typed, never applied: just empty the box
            return
        self.action_clear_filter()

    def action_filter_value(self) -> None:
        if self._queue_for_keep("filter_value", "="):
            return
        self._with_cursor_value(self._filter_value)

    def _filter_value(self, name: str, v) -> None:
        if self.view.sql:
            self.notify("= filtering works on the table, not on SQL results", severity="warning")
            return
        # The condition goes into the filter box: a keyword or odd name is quoted (plain ones read
        # better bare), and no control character of the name or value is put there as is (sql_*).
        q = sql_column_ref(self.ds.sql_name(name))  # (DuckDB's name, if it renamed it)
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
            cond = f"{q} = {sql_text_literal(v)}"
        else:
            self.notify("Can't filter on this value type", severity="warning")
            return
        inp = self.query_one("#filter", Input)
        cur = inp.value.strip()
        inp.value = f"({cur}) and {cond}" if cur and " or " in cur.lower() else (f"{cur} and {cond}" if cur else cond)
        self.history.append(inp.value)
        # the record filtered on matches the new filter: the cursor stays on it
        grid = self.query_one(GridTable)
        fr = None
        if self.page and self.page.rows:
            fr = self.page.row_numbers[min(grid.cursor_row, len(self.page.rows) - 1)]
        self.apply_filter(inp.value, fr)

    @_ui
    def _show_error(self, e: Exception, mark_input: bool = False) -> None:
        # DuckDB's messages quote names and values from the file: shown as text, never as markup
        msg = F.sanitize(str(e).strip(), keep_ws=True)
        if e is self.ds.setup_error:  # not this query's fault: no query can run on this file
            reason = re.sub(r"^[A-Za-z ]+ Error:\s*", "", msg.split("\n")[0])
            reason = re.sub(r"^Failed to read Parquet file '.*?':\s*", "", reason)
            self._last_error = F.sanitize(reason[:160])
            self._error_hint = ""
            if not self.__dict__.get("_unreadable_notified"):  # once: stats, plots etc. fail the same way
                self._unreadable_notified = True
                self.notify(f"{msg[:600]}\n\nSchema and Metadata (from the footer) still work.",
                            title="✗ DuckDB can't read this file", severity="error", timeout=12, markup=False)
            self._render_status()
            return
        first = msg.split("\n")[0]
        first = re.sub(r"^(Binder|Parser|Catalog|Conversion|Invalid Input|Out of Range) Error:\s*", "", first)
        first = re.sub(r'Referenced column ("[^"]+") not found in FROM clause!?', r"unknown column \1", first)
        first = first.rstrip("!")
        m = re.search(r'Candidate bindings: "(?:[^".]+\.)?([^"]+)"', msg)
        self._error_hint = f'did you mean "{m.group(1)}"?' if m else "edit with /"
        self._last_error = F.sanitize(first[:160])
        if mark_input:
            self.query_one("#filterbox").add_class("error")
        self.notify(msg[:600], title="✗ Query failed", severity="error", timeout=8, markup=False)
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
        if self._keep is not None:
            self._drop_keep("the sort changed before the record was found")
        grid = self.query_one(GridTable)
        col = grid.cursor_column
        self.total = self.ds.num_rows if self.view.is_trivial else self.total
        grid.total = self.total
        self._rebuild_columns()
        self.load_window(0, 0, col)
        if self.total is None:
            self.count_rows()

    # ------------------------------------------------------------ grid actions
    def action_pick_columns(self) -> None:
        cols = [(n, F.short_type(t)) for n, t in self.result_schema]

        def done(result):
            if result:
                grid = self.query_one(GridTable)
                offset, row = grid.offset, grid.abs_row  # before the rebuild resets the cursor
                self.cols_shown = result
                self._rebuild_columns(follow=True)
                # land on the current column (also the one a hidden-column hint was about)
                col = result.index(self.current_column) if self.current_column in result else 0
                self._hidden_hint = None
                self.load_window(offset, row, col)
        self.push_screen(ColumnPicker(cols, self.cols_shown), done)

    def action_hide_column(self) -> None:
        grid = self.query_one(GridTable)
        if len(self.cols_shown) <= 1:
            return
        name = self.cols_shown[grid.cursor_column]
        self.cols_shown = [c for c in self.cols_shown if c != name]
        col = min(grid.cursor_column, len(self.cols_shown) - 1)
        offset, row = grid.offset, grid.abs_row  # before the rebuild resets the cursor
        self._rebuild_columns(follow=True)
        self.load_window(offset, row, col)
        self.notify(f"Hid {F.sanitize(name)} · c brings it back", timeout=2, markup=False)

    def action_pin_columns(self) -> None:
        grid = self.query_one(GridTable)
        grid.fixed_columns = 0 if grid.fixed_columns else grid.cursor_column + 1
        self.call_after_refresh(self._render_hscroll)
        self.call_after_refresh(self._ensure_columns)  # pinning brings columns into view without scrolling

    def action_toggle_raw(self) -> None:
        self.raw = not self.raw
        grid = self.query_one(GridTable)
        for cc in grid.cell_columns:  # cells re-format when next drawn
            cc.invalidate(self.raw)
        self._fit_columns()
        self._reserve_widths()  # raw values are wider: so are the ones still loading
        grid.invalidate_cells()
        grid.scroll_cursor_fitted()  # columns left of the cursor may have widened
        self._render_status()

    def _cursor_formatter(self) -> F.CellFormatter | None:
        grid = self.query_one(GridTable)
        if not self.cols_shown or grid.cursor_column >= len(self.cols_shown):
            return None
        name = self.cols_shown[grid.cursor_column]
        if name not in self.formatters:
            unit = self.ds.column(name).unit if name in self.ds._by_name else ""
            self.formatters[name] = F.CellFormatter(name, dict(self.result_schema).get(name, pa.string()), unit,
                                                    self.col_formats.get(name))
        return self.formatters[name]

    def action_step_digits(self, delta: int) -> None:
        if self._queue_for_keep("step_digits", "<" if delta < 0 else ">", delta):
            return
        fm = self._cursor_formatter()
        if fm is None:
            return
        new = F.step_override(fm.override, fm.kind, delta)
        if new is None:
            self.notify(f"{F.sanitize(fm.name)} has no digits to change · F sets a format spec",
                        severity="warning", timeout=3, markup=False)
            return
        # (the Details pane always shows full precision: from there, say where the change shows)
        self._set_format(fm, new, " (grid)" if isinstance(self.focused, DetailList) else "")

    def action_set_format(self) -> None:
        if self._queue_for_keep("set_format", "F"):
            return
        fm = self._cursor_formatter()
        if fm is None:
            return

        def done(text):
            if text is not None:
                self._set_format(fm, config.parse_override(text))
        self._with_cursor_value(lambda _, sample: self.push_screen(FormatScreen(fm.name, fm.kind, fm.override,
                                                                                  sample), done))

    def _set_format(self, fm: F.CellFormatter, value: int | str | None, where: str = "") -> None:
        """Apply a column's format override (None = automatic), redraw, and remember it.
        ``where`` is appended to the notification (e.g. " (grid)")."""
        fm.override = value
        if value is None:
            self.col_formats.pop(fm.name, None)
        else:
            self.col_formats[fm.name] = value
        grid = self.query_one(GridTable)
        col = grid.columns.get(ColumnKey(fm.name))
        if col is not None:
            col.label = self._column_label(fm.name)
            col.content_width = max(line.cell_len for line in col.label.split())  # let it shrink to fit
        for i, cc in enumerate(grid.cell_columns):
            if cc.fmt.name == fm.name:  # re-format the column: all of it, so its width is exact
                cc.fmt = fm
                cc.invalidate()
                with grid.sizing():
                    grid.fit_columns(columns=[i])
                self._reserve_widths([i])  # (if it's still loading)
        grid.invalidate_cells()
        grid.scroll_cursor_fitted()
        if (self._stats_rendered and self._stats_rendered[0] is self.view and self._stats_rendered[1] == fm.name
                and not self._stats_stale):
            self._render_stats(*self._stats_rendered[1:])  # reformat what's shown; no need to re-profile
        name, shown = F.sanitize(fm.name), F.sanitize(F.describe_override(value, fm.kind) or "automatic")
        try:
            config.save_format(fm.name, value)
        except (config.ConfigError, OSError) as e:
            self.notify(f"{name}: {shown}{where}, for this session only — not saved: {F.sanitize(str(e))}",
                        severity="warning", timeout=6, markup=False)
            return
        self.notify(f"✓ {name}: {shown}{where}", timeout=2, markup=False)

    def action_copy_cell(self) -> None:
        if self._queue_for_keep("copy_cell", "y"):
            return
        self._with_cursor_value(self._copy_value)

    def _copy_value(self, name: str, v) -> None:
        typ = dict(self.result_schema).get(name, pa.null())
        # what's copied is what the detail pane shows: control characters (an ESC pasted into a
        # terminal could end a bracketed paste and run what follows) as their visible stand-ins
        raw = F.format_value(F.shortest(v, typ), "float", raw=True, width=0, safe=False) if v is not None else ""
        s = F.sanitize(raw, keep_ws=True)
        self.copy_to_clipboard(s)
        note = "  (control and invisible characters copied as visible symbols such as ␛)" if s != raw else ""
        self.notify(f"✓ Copied {F.sanitize(name)} = {s[:60]}{note}", timeout=2 if not note else 5, markup=False)

    def action_goto(self) -> None:
        def done(spec):
            if not spec:
                return
            try:
                total = self.total if self.total is not None else 1 << 62
                target = parse_row_spec(spec, total)
            except ValueError:
                self.notify(f"Not a row number: {F.sanitize(spec)}", severity="error", markup=False)
                return
            self._seek_to(target)
        self.push_screen(GotoScreen(self.total), done)

    def action_inspect_column(self) -> None:
        if self._queue_for_keep("inspect_column", "i"):
            return
        grid = self.query_one(GridTable)
        if not self.cols_shown:
            return
        self._show_stats_for(self.cols_shown[grid.cursor_column])

    # --------------------------------------------------------------- app-wide
    def action_tab_step(self, d: int) -> None:
        ids = [t for t, _ in TABS]
        cur = self.query_one(TabbedContent).active
        self.action_tab(ids[(ids.index(cur) + d) % len(ids)] if cur in ids else ids[0])

    def action_tab(self, tab: str) -> None:
        # Drop focus first: TabbedContent re-activates whichever pane holds the
        # focused widget, which would otherwise snap us back to the old tab.
        self.set_focus(None)
        self.query_one(TabbedContent).active = tab

    def action_help(self) -> None:
        self.push_screen(HelpScreen())

    def action_escape(self) -> None:
        # the detail pane closes first, unless the user is waiting on something to cancel;
        # pqx's own row/column/count loading carries on (the pane's columns are wanted later)
        if isinstance(self.focused, GridTable) and self.query_one("#detail").display \
                and not self._busy_with_user_work():
            self.action_toggle_detail()
            return
        if self._busy:
            self.ds.interrupt()
            self.notify("Cancelled running queries", timeout=2)
            return
        if isinstance(self.focused, Input):
            self.query_one(GridTable).focus() if self.query_one(TabbedContent).active == "tab-data" \
                else self.set_focus(None)
        elif isinstance(self.focused, GridTable) and self.query_one("#detail").display:
            self.action_toggle_detail()  # (reached when the user's work had just finished)

    def action_toggle_sample(self) -> None:
        self.sampling = not self.sampling
        self.notify(("! Sampling on · stats and plots use ~" + F.human_count(SAMPLE_ROWS) + " rows")
                    if self.sampling else "✓ Sampling off · stats and plots scan every row", timeout=3)
        self._render_status()
        self._refresh_analysis()

    def _sample(self) -> int | None:
        return SAMPLE_ROWS if self.sampling else None

    def check_action(self, action: str, parameters) -> bool | None:
        # single-letter app keys must not fire while typing in an input
        if isinstance(self.focused, Input) and action in (
                "clear_filter", "export", "toggle_sample", "help", "quit", "tab", "tab_step", "hist_log_y",
                "hist_log_x", "bins", "rotate_sky", "focus_filter"):
            return False
        return True

    def action_export(self) -> None:
        stem = F.sanitize(os.path.splitext(os.path.basename(self.ds.path))[0])  # (no control characters in the box)
        default = os.path.join(os.getcwd(), f"{stem}.subset.parquet")
        desc = "all rows" if self.view.is_trivial else (
            "SQL result" if self.view.sql else f"where {F.sanitize(self.view.where)}" if self.view.where else "")
        if self.view.order_by:
            desc += f" · sorted by {F.sanitize(self.view.order_by[0][0])}"
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
        # (notify is safe during teardown)
        self.call_from_thread(self.notify, f"✓ Wrote {F.human_count(n)} rows · {size} · {time.time() - t0:.1f} s"
                                           f"\n→ {F.sanitize(opts['path'])}", timeout=8, markup=False)

    # ------------------------------------------------------------ schema tab
    # Schema and Metadata need a pass over every column chunk in the footer (sizes,
    # statistics): seconds for thousands of row groups x hundreds of columns. So
    # they're set up empty with the grid, read_footer does the pass on a thread,
    # and they fill in when it's done.
    def _init_schema_tab(self) -> None:
        t = self.query_one("#schema-table", DataTable)
        # A missing unit is a dim "–", never a blank: blank unit cells next to the
        # right-aligned null counts made those read as units. The null share has its
        # own column so the counts stay narrow and line up under their header.
        cols = [("#", True), ("column", False), ("type", False), ("unit", False), ("nulls", True),
                ("null %", True), ("min", True), ("max", True), ("size", True), ("ratio", True)]
        for label, right in cols:
            t.add_column(Text(label, style="bold", justify="right" if right else "left"), key=label)
        self._schema_info: dict[str, tuple] = {}
        self._schema_skip = 0  # highlights to take as the table being built, not as the user moving
        md = self.ds.meta
        self.query_one("#schema-desc", Static).update(Text.assemble(
            "Reading sizes and statistics from the footer …",
            (f"   {md.num_row_groups:,} row groups × {md.num_columns:,} columns", self.dim)))

    @work(thread=True, exclusive=True, group="footer")
    def read_footer(self) -> None:
        # The pass is Python (it holds the GIL) and converting timestamp statistics imports pandas
        # (~0.3 s): let the grid's first page in first, unless that's taking a while.
        worker = get_current_worker()
        deadline = time.monotonic() + FOOTER_WAIT
        while self.page is None and self.ds.setup_error is None and time.monotonic() < deadline:
            if worker.is_cancelled:  # quitting
                return
            time.sleep(0.01)
        if self.page is not None:  # and on screen
            painted = threading.Event()
            try:
                self.call_from_thread(self.call_after_refresh, painted.set)
            except RuntimeError:  # the app has stopped
                return
            deadline = time.monotonic() + FOOTER_WAIT
            while not painted.wait(0.01) and time.monotonic() < deadline:
                if worker.is_cancelled:
                    return
        stop = lambda: worker.is_cancelled  # noqa: E731 - quitting needn't wait for the whole pass
        try:
            summ = self.ds.column_chunk_summary(stop)
            rgs = self.ds.row_groups(stop)
            rg_rows = self._rowgroup_rows(rgs)
        except Stopped:
            return
        except Exception as e:  # noqa: BLE001 - say so in both tabs; the rest of pqx works without it
            self.call_from_thread(self._footer_failed, e)
            return
        self.call_from_thread(self._build_footer_tabs, summ, rgs, rg_rows)

    @_ui
    def _build_footer_tabs(self, summ: list[dict], rgs: list[dict], rg_rows: list[tuple]) -> None:
        self._build_schema_tab(summ)
        self._build_meta_footer(summ, rgs, rg_rows)

    @_ui
    def _footer_failed(self, e: Exception) -> None:
        msg = Text.assemble(("✗", "red"), " Couldn't read the footer's statistics", (f"   {F.sanitize(str(e))}", self.dim))
        self.query_one("#schema-desc", Static).update(msg)
        self.query_one("#meta-status", Static).update(msg)

    def _build_schema_tab(self, summ_list: list[dict]) -> None:
        t = self.query_one("#schema-table", DataTable)
        d = self.dim
        summ = {s["path"]: s for s in summ_list}
        info = {}
        rows = []
        for i, c in enumerate(self.ds.columns):
            s = summ.get(c.name)
            fm = F.CellFormatter(c.name, c.arrow_type, c.unit)
            if s is None:  # nested: aggregate the leaves
                leaves = [v for k, v in summ.items() if k.split(".")[0] == c.name]
                size = sum(v["compressed"] for v in leaves)
                usize = sum(v["uncompressed"] for v in leaves)
                mn = mx = nulls = None
                comp = leaves[0]["compression"] if leaves else ""
            else:
                size, usize = s["compressed"], s["uncompressed"]
                mn, mx = s["min"], s["max"]
                nulls = s["nulls"] if s["has_stats"] else None
                comp = s["compression"]
            ratio = f"{usize / size:.1f}×" if size else ""
            info[c.name] = (i, size, ratio, str(comp).lower(), s is not None)
            if nulls is None:
                null_n, null_p = Text("–", style=d, justify="right"), Text("")
            elif nulls:
                null_n = Text(f"{nulls:,}", justify="right")
                null_p = Text(F.percent(nulls, self.ds.num_rows), justify="right")
            else:
                null_n, null_p = Text("0", style=d, justify="right"), Text("")
            cells = [Text(str(i), style=d, justify="right"), Text(F.sanitize(c.name), style="bold"),
                     Text(F.short_type(c.arrow_type), style=d), Text(F.sanitize(c.unit)) if c.unit else Text("–", style=d)]
            cells += [null_n, null_p,
                      fm(mn) if mn is not None else Text("–", style=d, justify="right"),
                      fm(mx) if mx is not None else Text("–", style=d, justify="right"),
                      Text(F.human_bytes(size), justify="right"), Text(ratio, style=d, justify="right")]
            rows.append((cells, c.name))
        self._schema_info = info
        # adding the first row highlights it: that's not the user moving
        self._schema_skip = int(bool(rows) and t.show_cursor and t.cursor_type != "none")
        for cells, key in rows:
            t.add_row(*cells, key=key)
        if self._tab_is("tab-schema"):  # built while shown: go to the current column now
            self._sync_schema()

    def _sync_schema(self) -> None:
        """Put the Schema cursor on the current column (its highlight event updates the description)."""
        t = self.query_one("#schema-table", DataTable)
        # SQL-result columns may not be in the file: then Schema keeps its own cursor
        if self.current_column in self._schema_info:
            row = t.get_row_index(self.current_column)
            if t.cursor_row != row:
                t.move_cursor(row=row, animate=False)

    @on(DataTable.RowHighlighted, "#schema-table")
    def schema_row(self, event: DataTable.RowHighlighted) -> None:
        name = str(event.row_key.value) if event.row_key else None
        building = self._schema_skip > 0
        if building:
            self._schema_skip -= 1
        if name is None or name not in self._schema_info:
            return
        if self._tab_is("tab-schema") and not building:
            self.set_current_column(name, "schema")
        c = self.ds.column(name)
        i, size, ratio, comp, leaf = self._schema_info[name]
        enc = ""
        if leaf:
            enc = ", ".join(sorted(e.replace("RLE_DICTIONARY", "dict").lower()
                                   for e in self.ds.column_encodings(name)))
        d = self.dim
        S = F.sanitize  # (name, type, unit and description are all the file's)
        t = Text.assemble((S(c.name), "bold cyan"), f"   {S(str(c.arrow_type))}", (f"   [{S(c.unit)}]" if c.unit else ""),
                          (f"   {'nullable' if c.nullable else 'not null'}  ·  {F.human_bytes(size)}"
                           + (f"  ·  {ratio} {comp}" if ratio else "") + (f"  ·  {enc}" if enc else ""), d), "\n",
                          (S(c.description, keep_ws=True) or "no description in the file's field metadata",
                           "" if c.description else d),
                          ("\n→ enter opens statistics  ·  i from the data grid", d))
        desc = self.query_one("#schema-desc", Static)
        desc.border_title = self._dim_markup(f"column {i}")
        desc.update(t)

    @on(DataTable.RowSelected, "#schema-table")
    def schema_selected(self, event: DataTable.RowSelected) -> None:
        self._show_stats_for(str(event.row_key.value))

    # ---------------------------------------------------------- metadata tab
    def _init_meta_tab(self) -> None:
        ds, md, d = self.ds, self.ds.meta, self.dim
        self._render_meta_overview(None)
        t = self.query_one("#rowgroups", DataTable)
        for label, right in [("#", True), ("first row", True), ("rows", True), ("compressed", True), ("", False),
                             ("ratio", True)]:
            t.add_column(Text(label, style="bold", justify="right" if right else "left"))
        self.query_one("#meta-rg-panel").border_title = self._dim_markup(f"row groups  {md.num_row_groups:,}")
        self.query_one("#meta-status", Static).update(Text.assemble(
            "Reading the footer …", (f"   {F.human_bytes(md.serialized_size)}  ·  {md.num_row_groups:,} row groups", d)))
        kv = ds.key_value_metadata()
        parts = [Text(""), Text("key-value metadata", style="bold")]
        from rich.json import JSON
        S = F.sanitize  # keys and values are the file's: their control characters are shown, not sent
        for k, v in kv.items():
            if k == "ARROW:schema":
                parts.append(Text.assemble((k, "cyan"), (f"   {F.human_bytes(len(v))} · decoded in Schema", d)))
                continue
            try:
                parts.append(Text(S(k), style="cyan"))
                if len(v) >= 4000:  # pretty-printed it would be longer still: cut, as below
                    raise ValueError("too long to pretty-print")
                js = JSON(v, indent=2, highlight=False)  # (its text keeps C1 controls and DEL as they are)
                js.text = Text(S(js.text.plain, keep_ws=True))  # (no highlighting: no spans to keep)
                parts.append(js)
            except Exception:
                parts[-1] = Text.assemble((S(k), "cyan"), "   ", S(v if len(v) < 4000 else v[:4000] + " …",
                                                                   keep_ws=True))
        if len(parts) == 2:
            parts.append(Text("none", style=d))
        self.query_one("#meta-kv", Static).update(Group(*parts))

    def _render_meta_overview(self, rgs: list[dict] | None) -> None:
        """The file overview; ``rgs`` (``ds.row_groups()``) fills in the per-row-group figures."""
        ds, md, d = self.ds, self.ds.meta, self.dim
        ov = Table.grid(padding=(0, 2))
        ov.add_column(style=d, no_wrap=True)
        ov.add_column()
        if rgs is None:
            groups = Text(f"{md.num_row_groups:,}")
            data = Text("…", style=d)
        else:
            avg_rg = (sum(r["rows"] for r in rgs) / len(rgs)) if rgs else 0
            comp = sum(r["compressed"] for r in rgs)
            unc = sum(r["uncompressed"] for r in rgs)
            groups = Text.assemble(f"{md.num_row_groups:,}", (f"   ~{avg_rg:,.0f} rows each", d))
            data = Text.assemble(F.human_bytes(comp), (f"   compressed  ·  {F.human_bytes(unc)} raw"
                                                       + (f"  ·  {unc / comp:.2f}×" if comp else ""), d))
        rows = [
            ("path", Text.assemble((F.sanitize(os.path.basename(ds.path)), "cyan"),
                                   (f"   {F.sanitize(os.path.dirname(ds.path))}/", d))),
            ("file size", Text.assemble(F.human_bytes(ds.file_size), (f"   {ds.file_size:,} bytes", d))),
            ("rows", Text(f"{ds.num_rows:,}")),
            ("columns", Text.assemble(f"{len(ds.columns)}", (f"   {md.num_columns} leaf", d))),
            ("row groups", groups),
            ("data", data),
            ("format", Text(str(md.format_version))),
            ("created by", Text(F.sanitize(str(md.created_by or "?")))),
            ("footer", Text(F.human_bytes(md.serialized_size))),
        ]
        for k, v in rows:
            ov.add_row(k, v)
        self.query_one("#meta-overview", Static).update(ov)

    def _rowgroup_rows(self, rgs: list[dict]) -> list[tuple]:
        """Cells of the Metadata tab's row-group table (the first 5000); no widgets, so any thread."""
        d = self.dim
        cmax = max((r["compressed"] for r in rgs), default=1) or 1
        rows = []
        for r in rgs[:5000]:
            n = round(10 * r["compressed"] / cmax)
            bar = Text.assemble("▰" * n, ("▱" * (10 - n), d))
            rows.append((Text(str(r["index"]), style=d, justify="right"), Text(f"{r['start']:,}", justify="right"),
                         Text(f"{r['rows']:,}", justify="right"),
                         Text(F.human_bytes(r["compressed"]), justify="right"),
                         bar, Text(f"{r['uncompressed'] / r['compressed']:.2f}×" if r["compressed"] else "", style=d,
                                   justify="right")))
        return rows

    @_ui
    def _add_rowgroup_rows(self, rows: list[tuple]) -> None:
        """Add ``rows`` to the row-group table a few hundred per event-loop turn: each
        batch costs the DataTable a measuring pass, which shouldn't hold up the UI."""
        self.query_one("#rowgroups", DataTable).add_rows(rows[:ROWGROUP_BATCH])
        if len(rows) > ROWGROUP_BATCH:
            self.set_timer(0.01, lambda: self._add_rowgroup_rows(rows[ROWGROUP_BATCH:]))

    def _build_meta_footer(self, summ: list[dict], rgs: list[dict], rg_rows: list[tuple]) -> None:
        md, d = self.ds.meta, self.dim
        self._render_meta_overview(rgs)
        self._add_rowgroup_rows(rg_rows)
        self.query_one("#meta-rg-panel").border_title = self._dim_markup(f"row groups  {len(rgs):,}")
        n_stats = sum(1 for s in summ if s["has_stats"])
        st = Text.assemble(("✓", "green"), " Footer read",
                           (f"   {F.human_bytes(md.serialized_size)}  ·  {md.num_row_groups:,} row groups  ·  "
                            f"stats on {n_stats}/{md.num_columns} columns", d))
        self.query_one("#meta-status", Static).update(st)

    # -------------------------------------------------------------- stats tab
    def _build_stats_list(self) -> None:
        ol = self.query_one("#stats-cols", CursorList)
        ol.set_items([(name, Text.assemble((F.sanitize(name).ljust(18), "bold"), "  ", (F.short_type(typ), self.dim)))
                      for name, typ in self.result_schema], width=32)
        self.query_one("#stats-cols-panel").border_title = self._dim_markup(f"columns  {len(self.result_schema)}")
        if self._stats_col not in dict(self.result_schema):
            self._stats_col = None
        # (the highlight comes back with _sync_stats, when Stats is shown or the view changes on it)

    def _show_stats_for(self, name: str) -> None:
        self.set_current_column(name, "stats")
        if self._tab_is("tab-stats"):
            self._sync_stats()
        else:
            self.action_tab("tab-stats")  # tab_activated highlights and profiles it, once

    def _sync_stats(self) -> None:
        """Highlight the current column in the Stats list and profile it, unless it's already shown."""
        ol = self.query_one("#stats-cols", OptionList)
        names = [n for n, _ in self.result_schema]
        if self.current_column in names:
            self._stats_col = self.current_column
        elif self._stats_col is None and names:
            self._stats_col = names[0]
        if self._stats_col:
            # _stats_col is set first, so the highlight event below isn't taken for a move
            ol.highlighted = names.index(self._stats_col)
            if self._stats_stale or self._stats_shown != self._stats_col:
                timer = self.__dict__.get("_pqx_timers", {}).pop("stats", None)
                if timer is not None:  # a highlight's debounced profile is superseded by this one
                    timer.stop()
                self.compute_stats(self._stats_col)

    @on(OptionList.OptionHighlighted, "#stats-cols")
    def stats_col_highlighted(self, event: OptionList.OptionHighlighted) -> None:
        name = event.option.id
        if name and name != self._stats_col:  # syncing sets _stats_col first, so it never lands here
            self._stats_col = name
            if self._tab_is("tab-stats"):
                self.set_current_column(name, "stats")
                self._debounced("stats", 0.15, lambda: self._tab_is("tab-stats") and self.compute_stats(name))

    def _debounced(self, key: str, delay: float, fn) -> None:
        timers = self.__dict__.setdefault("_pqx_timers", {})
        if key in timers:
            timers[key].stop()
        timers[key] = self.set_timer(delay, fn)

    @on(TabbedContent.TabActivated)
    def tab_activated(self, event: TabbedContent.TabActivated) -> None:
        pane = event.pane.id
        self._render_tab_titles(pane)
        # each linked view moves to the current column (Plot has its own pickers)
        self._hidden_hint = None
        if pane == "tab-stats":
            self._sync_stats()
            self.query_one("#stats-cols", OptionList).focus()
        elif pane == "tab-plot":
            self.query_one(PlotControls).focus()
            self.call_after_refresh(self.replot)  # after layout, so the plot fills the pane
        elif pane == "tab-schema":
            self._sync_schema()  # (if it's still being read, it syncs once built)
            self.query_one("#schema-table", DataTable).focus()
        elif pane == "tab-data":
            name = self.current_column
            if name and not self._move_grid_to_column(name) and name in dict(self.result_schema):
                self._hidden_hint = name
            self.query_one(GridTable).focus()
        elif pane == "tab-meta":
            self.query_one("#meta-file").focus()
        self._render_status()
        self._render_keys()

    def _refresh_analysis(self) -> None:
        active = self.query_one(TabbedContent).active
        if active == "tab-stats":
            self._stats_stale = True
            self._sync_stats()
        elif active == "tab-plot":
            self.replot()
        else:  # recompute lazily when those tabs are next opened
            self.query_one("#stats-summary", Static).update("")
            self._stats_stale = True

    def _scope(self, view: View) -> str:
        return "all rows" if view.is_trivial else ("SQL result" if view.sql else f"where {F.sanitize(view.where)}")

    @work(thread=True, exclusive=True, group="stats")
    def compute_stats(self, name: str) -> None:
        view = self.view
        types = dict(self.result_schema)
        typ = types.get(name)
        if typ is None:
            return
        sample = self._sample()
        self.call_from_thread(self._set_busy, "stats", f"profiling {F.sanitize(name)}")
        self.call_from_thread(self._stats_running, name)
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

    def _stats_head(self, name: str, status: Text) -> Group:
        info = self.ds._by_name.get(name)
        typ = dict(self.result_schema).get(name)
        S = F.sanitize
        line = Text.assemble((S(name), "bold cyan"), (f"   {F.short_type(typ)}" if typ is not None else "", self.dim),
                             (f"   [{S(info.unit)}]" if info and info.unit else ""),
                             (f"   {S(info.description, keep_ws=True)}" if info and info.description else "", self.dim))
        return Group(line, status)

    @_ui
    def _stats_running(self, name: str) -> None:
        st = Text.assemble((SPINNER[3], self.accent), f" Profiling {F.sanitize(name)}",
                           (f"   {self._scope(self.view)}", self.dim))
        self.query_one("#stats-head", Static).update(self._stats_head(name, st))

    @_ui
    def _render_stats(self, name: str, typ: pa.DataType, st: ColumnStats, hist, elapsed: float) -> None:
        self._stats_rendered = (self.view, name, typ, st, hist, elapsed)
        self._stats_stale = False
        self._stats_shown = name
        d = self.dim
        fm = self.formatters.get(name) or F.CellFormatter(name, typ)
        if st.sampled:
            status = Text.assemble(("!", "yellow"), f" Profiled {F.sanitize(name)}, sampled",
                                   (f"   {F.human_count(st.count)} rows  ·  {elapsed:.2f} s  ·  {self._scope(self.view)}"
                                    "  ·  m scans everything", d))
        else:
            status = Text.assemble(("✓", "green"), f" Profiled {F.sanitize(name)}",
                                   (f"   {st.count:,} rows  ·  {elapsed:.2f} s  ·  {self._scope(self.view)}", d))
        self.query_one("#stats-head", Static).update(self._stats_head(name, status))

        def pct(n):
            return F.percent(n, st.count)

        is_int = pa.types.is_integer(typ)

        def num(v):
            if isinstance(v, (int, float)) and not isinstance(v, bool):
                if is_int:
                    return Text(f"{int(round(v)):,}" if abs(v) < 1e15 else str(int(round(v))), justify="right")
                return fm(float(v))
            return Text(F.sanitize(str(v)))

        left = [("rows", Text(f"{st.count:,}", justify="right"), "sampled" if st.sampled else ""),
                ("nulls", Text(f"{st.nulls:,}", justify="right"), pct(st.nulls) if st.nulls else "")]
        if st.nans is not None:
            left.append(("NaN", Text(f"{st.nans:,}", justify="right"), pct(st.nans) if st.nans else ""))
        if st.distinct is not None:
            exact = bool(st.top) and len(st.top) < 10
            left.append(("distinct", Text(f"{st.distinct:,}" if exact else f"≈{st.distinct:,}", justify="right"), ""))
        if st.min is not None:
            info = self.ds._by_name.get(name)
            unit = info.unit if info is not None else ""
            left.append(("min", fm(st.min), F.derived(name, fm.kind, st.min, unit)))
            left.append(("max", fm(st.max), F.derived(name, fm.kind, st.max, unit)))
        if st.mean is not None:
            left.append(("mean", Text(F._fmt_float(float(st.mean), 15), justify="right") if is_int
                         else fm(float(st.mean)), ""))
        if st.std is not None:
            left.append(("std", Text(F.format_value(float(st.std), "err"), justify="right"), ""))
        right = [(f"p{q * 100:g}", num(v), "median" if q == 0.5 else "") for q, v in st.quantiles.items()]
        g = Table.grid(padding=(0, 2))
        for style, just in [(d, "right"), ("", "right"), (d, "left"), (d, "right"), ("", "right"), (d, "left")]:
            g.add_column(style=style, justify=just, no_wrap=True)
        for i in range(max(len(left), len(right))):
            a = left[i] if i < len(left) else ("", "", "")
            b = right[i] if i < len(right) else ("", "", "")
            g.add_row(a[0], a[1], a[2], b[0], b[1], b[2])
        blocks = [Text(""), g]
        if st.top and (hist is None):
            blocks.append(Text("\nmost frequent", style="bold"))
            tt = Table.grid(padding=(0, 2))
            tt.add_column(justify="right", max_width=40, no_wrap=True)
            tt.add_column()
            tt.add_column(justify="right", no_wrap=True)
            tt.add_column(justify="right", style=d, no_wrap=True)
            m = max(n for _, n in st.top) or 1
            barw = 30
            for v, n in st.top:
                w = n / m * barw
                bar = "█" * int(w) + (" ▏▎▍▌▋▊▉"[int((w - int(w)) * 8)] if w < barw else "")
                tt.add_row(fm(v), Text(bar.rstrip()), f"{n:,}", pct(n))
            blocks.append(tt)
        self.query_one("#stats-summary", Static).update(Group(*blocks))
        plot = Text("")
        if hist is not None:
            edges, counts = hist
            width = max(30, self.query_one("#stats-body").size.width - 6)
            temporal = pa.types.is_timestamp(typ) or pa.types.is_date(typ)
            plot = plots.render_histogram(edges, counts, width=width, height=10, color=None,
                                          log_y=self._hist_log_y, log_x=self._hist_log_x and not temporal,
                                          xlabel=F.sanitize(name) + ("  (UTC)" if temporal else ""),
                                          xfmt=_epoch_label if temporal else None, dim=self.dim_style)
            plot = Group(Text("\ndistribution", style="bold"), plot,
                         Text(f"{len(counts)} bins" + ("  ·  log counts" if self._hist_log_y else "")
                              + ("  ·  log values" if self._hist_log_x and not temporal else ""), style=d))
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
        pc = self.query_one(PlotControls)
        old = (pc.value("x"), pc.value("y"))
        pc.set_columns(numeric)
        lon, lat = guess_sky_columns(numeric)
        if keep and old[0] in numeric and old[1] in numeric:
            return
        if lon and lat:
            pc.values.update(mode="sky", x=lon, y=lat)
        elif len(numeric) >= 2:
            pc.values.update(mode="xy", x=numeric[0], y=numeric[1])
        else:
            pc.values.update(mode="xy", x="", y="")
        pc.refresh()

    @on(PlotControls.Changed)
    def plot_control_changed(self) -> None:
        self._debounced("plot", 0.2, self.replot)

    def action_rotate_sky(self) -> None:
        if self._tab_is("tab-plot"):
            pc = self.query_one(PlotControls)
            pc.set_value("centre", "180°" if pc.value("centre") == "0°" else "0°")

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
        pc = self.query_one(PlotControls)
        x, y = pc.value("x"), pc.value("y")
        if not x or not y:
            self.query_one("#plot-body", Static).update(Text("no numeric columns to plot", style=self.dim))
            return
        area = self.query_one("#plot-area")
        w, h = max(20, area.size.width - 1), max(8, area.size.height)
        self.plot_worker(pc.value("mode"), x, y, pc.value("colour"), 180.0 if pc.value("centre") == "180°" else 0.0,
                         w, h)

    @work(thread=True, exclusive=True, group="plot")
    def plot_worker(self, mode: str, x: str, y: str, cmap: str, center: float, w: int, h: int) -> None:
        view = self.view
        sample = self._sample()
        d = self.dim
        self.call_from_thread(self._set_busy, "plot", f"binning {F.sanitize(x)} × {F.sanitize(y)}")
        self.call_from_thread(self._plot_show, Text.assemble((SPINNER[3], self.accent), f" Binning {F.sanitize(x)} × {F.sanitize(y)}",
                                                             (f"   {self._scope(view)}", d)), None)
        t0 = time.time()
        try:
            with self.ds.tagged("plot"):
                if mode == "sky":
                    grid = self.ds.sky_counts(view, x, y, res_deg=0.5, sample=sample)
                    n = int(grid.sum())
                    mw, _ = plots.sky_shape(w, h - 3)
                    frac = (grid > 0).sum() / grid.size
                    out = plots.render_skymap(grid, width=mw, height=h - 3, cmap=cmap, center=center,
                                              accent=self.accent, dim=self.dim_style)
                    detail = f"0.5° cells  ·  {F.percent(frac, 1.0)} of sky"
                else:
                    pw, ph = max(10, w - 12), max(4, h - 5)
                    grid, xl, yl = self.ds.xy_counts(view, x, y, 2 * pw, 2 * ph, sample=sample)
                    n = int(grid.sum())
                    out = plots.render_density(grid, xl, yl, cmap=cmap, xlabel=F.sanitize(x), ylabel=F.sanitize(y), accent=self.accent,
                                               dim=self.dim_style)
                    detail = f"{2 * pw}×{2 * ph} bins"
        except duckdb.InterruptException:
            return
        except Exception as e:  # noqa: BLE001
            self.call_from_thread(self._show_error, e)
            return
        finally:
            self.call_from_thread(self._set_busy, "plot", None)
        if view is not self.view:
            return
        rest = f"   {F.human_count(n)} rows  ·  {detail}  ·  {time.time() - t0:.2f} s  ·  {self._scope(view)}"
        if sample:
            status = Text.assemble(("!", "yellow"), f" Binned {F.sanitize(x)} × {F.sanitize(y)}, sampled", (rest + "  ·  m scans everything", d))
        else:
            status = Text.assemble(("✓", "green"), f" Binned {F.sanitize(x)} × {F.sanitize(y)}", (rest, d))
        self.call_from_thread(self._plot_show, status, out)

    @_ui
    def _plot_show(self, status: Text, body) -> None:
        self.query_one("#plot-status", Static).update(status)
        if body is not None:
            self.query_one("#plot-body", Static).update(body)
