"""Small shared widgets."""
from __future__ import annotations

from rich.cells import cell_len
from rich.segment import Segment
from rich.style import Style
from rich.styled import Styled
from rich.table import Table
from rich.text import Text
from textual import on
from textual.binding import Binding
from textual.visual import RichVisual, Visual
from textual.widgets import OptionList
from textual.widgets.option_list import Option


class CursorList(OptionList):
    """An OptionList whose highlight is a real reverse-video attribute.

    Textual emulates reverse by swapping foreground and background colours,
    which does nothing when both are the terminal's defaults (pqx's look). So
    the highlighted row's prompt is re-rendered with ``reverse`` instead."""

    def set_items(self, items: list[tuple[str, Text]], width: int = 0) -> None:
        """Replace the options with ``(id, prompt)`` pairs, padded to ``width``."""
        self._items = [(i, t) for i, t in items]
        self._width = width
        self._hl: int | None = None
        self.clear_options()
        self.add_options([Option(self._prompt(k, False), id=i) for k, (i, _) in enumerate(self._items)])

    def _prompt(self, k: int, highlighted: bool) -> Text:
        t = self._items[k][1].copy()
        if self._width and t.cell_len < self._width:
            t.append(" " * (self._width - t.cell_len))
        if highlighted:
            t.stylize("reverse")
        return t

    @on(OptionList.OptionHighlighted)
    def _mark(self, event: OptionList.OptionHighlighted) -> None:
        if event.option_list is not self or not getattr(self, "_items", None):
            return
        k = event.option_index
        prev = self._hl
        if prev is not None and prev != k and prev < len(self._items):
            self.replace_option_prompt_at_index(prev, self._prompt(prev, False))
        if k is not None and k < len(self._items):
            self.replace_option_prompt_at_index(k, self._prompt(k, True))
        self._hl = k


class EntryGrid:
    """A ``name  value`` Details entry, laid out as the Rich grid
    ``Table.grid(padding=(0, 2), expand=True)`` with a fixed-width, no-wrap name
    column and a folding value column would lay it out, but without Table's
    measuring and padding passes (several times cheaper; a wide file draws
    dozens of these per row change). Too narrow for that layout, it is the
    Table itself."""

    def __init__(self, name: Text, value: Text, name_width: int) -> None:
        self.name = name
        self.value = value
        self.name_width = name_width

    def table(self) -> Table:
        tbl = Table.grid(padding=(0, 2), expand=True)
        tbl.add_column(width=self.name_width, no_wrap=True)
        tbl.add_column(ratio=1, overflow="fold")  # long tokens (ids, blobs) break rather than lose their end
        tbl.add_row(self.name, self.value)
        return tbl

    def __rich_console__(self, console, options):
        nw = self.name_width
        vw = options.max_width - nw - 2
        if nw < 1 or vw < 1:
            yield self.table()
            return
        cell = options.update(justify="left", height=None, highlight=False)
        names = console.render_lines(self.name, cell.update(width=nw, no_wrap=True, overflow="ellipsis"))
        values = console.render_lines(self.value, cell.update(width=vw, no_wrap=False, overflow="fold"))
        null = Style()  # what the grid pads with
        gap, blank_name, blank_value = Segment("  ", null), Segment(" " * nw, null), Segment(" " * vw, null)
        nl = Segment.line()
        for i in range(max(len(names), len(values))):
            yield from names[i] if i < len(names) else (blank_name,)
            yield gap
            yield from values[i] if i < len(values) else (blank_value,)
            yield nl


class _LazyEntry(Visual):
    """A Details entry that builds its grid only when it is drawn.

    A wide file has hundreds of entries but only a screenful is ever on view;
    OptionList asks every entry for its height on each change, and only the
    visible ones for their lines. A one-line value that fits the value column
    is one line high without building anything; anything else (wrapping, a
    derived reading, tabs) is measured by rendering the grid, so heights match
    the rendering exactly."""

    def __init__(self, lst: DetailList, name: str, value: Text, name_width: int) -> None:
        self._list = lst
        self._name = name
        self._value = value
        self._name_width = name_width
        self._visual: RichVisual | None = None

    def _rich(self) -> RichVisual:
        if self._visual is None:
            self._visual = RichVisual(self._list, self._list._grid(self._name, self._value, False))
        return self._visual

    @staticmethod
    def one_line(value: Text, name_width: int, width: int) -> bool:
        """Whether ``value`` surely fits on the entry's first line (no newline, tab or control character)."""
        plain = value.plain
        vw = width - name_width - 2
        return name_width >= 1 and vw >= 1 and plain.isprintable() and cell_len(plain) <= vw

    def get_height(self, rules, width: int) -> int:
        if self.one_line(self._value, self._name_width, width):
            return 1
        return self._rich().get_height(rules, width)

    def get_optimal_width(self, rules, container_width: int) -> int:
        return self._rich().get_optimal_width(rules, container_width)

    def render_strips(self, width, height, style, options):
        return self._rich().render_strips(width, height, style, options)


class DetailList(CursorList):
    """The Details pane: one entry per column of the current row.

    Entries are ``name  value`` grids (the value may wrap and carry a derived
    reading below it), all with the same name width so the values line up. The
    selected entry is reversed whole while the list has focus and only by its
    name otherwise, like the grid's own column cursor. The wheel just scrolls;
    the app links the selection to the grid cursor."""

    BINDINGS = [
        Binding("enter,tab", "app.detail_to_grid", "Back to grid", show=False),
        Binding("escape", "app.detail_to_grid(True)", "Back to grid", show=False),
        Binding("d", "app.toggle_detail", "Detail", show=False),
    ]

    def __init__(self, **kw) -> None:
        super().__init__(**kw)
        self._items: list[tuple[str, Text]] = []
        self._name_width = 0
        self._hl: int | None = None

    def set_entries(self, entries: list[tuple[str, Text]], name_width: int) -> None:
        """Show ``(column, value)`` entries. When the columns are the same as
        before, the prompts are replaced in place, which keeps the scroll
        position and the selection; when nothing changed (the grid only moved
        sideways), nothing is redrawn."""
        def key(items):
            return [(n, v.plain, str(v.style), v.spans) for n, v in items]
        if key(entries) == key(self._items) and name_width == self._name_width:
            return
        same = [n for n, _ in entries] == [n for n, _ in self._items]
        self._items = list(entries)
        self._name_width = name_width
        if same:
            # replace_option_prompt_at_index for each, but clearing the caches once, not per entry
            for k, option in enumerate(self.options):
                option._set_prompt(self._prompt(k, k == self._hl))
            self._clear_caches()
            return
        self._hl = None
        self.clear_options()
        self.add_options([Option(self._prompt(k, False), id=n) for k, (n, _) in enumerate(entries)])

    def replace_option_prompt_at_index(self, index: int, prompt) -> DetailList:
        """OptionList's, but when the entry keeps its height (a moving selection
        restyles two entries) only that entry is redrawn, not all in view."""
        option = self.get_option_at_index(index)
        height = self._line_cache.heights.get(index)
        option._set_prompt(prompt)
        region = self.scrollable_content_region
        if height is None or not region:
            self._clear_caches()
            return self
        width = region.width - self._get_left_gutter_width()
        width -= self.get_component_styles("option-list--option").padding.width
        if self._get_visual(option).get_height(self.styles, width) != height:
            self._clear_caches()
            return self
        cache = self._option_render_cache
        for key in [key for key in cache.keys() if key[0] is option]:
            cache.discard(key)
        self.refresh()
        return self

    def select(self, name: str | None) -> None:
        """Select the entry for column ``name`` (scrolling it into view if it moves)."""
        k = next((k for k, (n, _) in enumerate(self._items) if n == name), None)
        if k is not None and self.highlighted != k:
            self.highlighted = k

    @property
    def selected(self) -> str | None:
        k = self.highlighted
        return self._items[k][0] if k is not None and k < len(self._items) else None

    def _prompt(self, k: int, highlighted: bool, focused: bool | None = None) -> EntryGrid | Styled | _LazyEntry:
        name, value = self._items[k]
        if not highlighted:  # built when drawn: a row change costs what's in view, not every column
            return _LazyEntry(self, name, value, self._name_width)
        whole = self.has_focus if focused is None else focused
        grid = self._grid(name, value, not whole)
        return Styled(grid, "reverse") if whole else grid

    def _grid(self, name: str, value: Text, name_reversed: bool) -> EntryGrid:
        return EntryGrid(Text(name, style="bold reverse" if name_reversed else "bold"), value, self._name_width)

    def watch_has_focus(self, has_focus: bool) -> None:
        super().watch_has_focus(has_focus)
        # restyle the selection for the state being entered (on_focus runs too early)
        k = self._hl
        if k is not None and k < len(self._items):
            self.replace_option_prompt_at_index(k, self._prompt(k, True, has_focus))
