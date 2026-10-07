"""Small shared widgets."""
from __future__ import annotations

from rich.styled import Styled
from rich.table import Table
from rich.text import Text
from textual import on
from textual.binding import Binding
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

    def set_entries(self, entries: list[tuple[str, Text]], name_width: int) -> None:
        """Show ``(column, value)`` entries. When the columns are the same as
        before, the prompts are replaced in place, which keeps the scroll
        position and the selection; when nothing changed (the grid only moved
        sideways), nothing is redrawn."""
        def key(items):
            return [(n, v.plain, str(v.style), v.spans) for n, v in items]
        old = getattr(self, "_items", [])
        if key(entries) == key(old) and name_width == getattr(self, "_name_width", None):
            return
        same = [n for n, _ in entries] == [n for n, _ in old]
        self._items = list(entries)
        self._name_width = name_width
        if same:
            for k in range(len(entries)):
                self.replace_option_prompt_at_index(k, self._prompt(k, k == self._hl))
            return
        self._hl = None
        self.clear_options()
        self.add_options([Option(self._prompt(k, False), id=n) for k, (n, _) in enumerate(entries)])

    def select(self, name: str | None) -> None:
        """Select the entry for column ``name`` (scrolling it into view if it moves)."""
        k = next((k for k, (n, _) in enumerate(getattr(self, "_items", [])) if n == name), None)
        if k is not None and self.highlighted != k:
            self.highlighted = k

    @property
    def selected(self) -> str | None:
        k = self.highlighted
        return self._items[k][0] if k is not None and k < len(self._items) else None

    def _prompt(self, k: int, highlighted: bool) -> Table | Styled:
        name, value = self._items[k]
        focused = highlighted and self.has_focus
        tbl = Table.grid(padding=(0, 2), expand=True)
        tbl.add_column(width=self._name_width, no_wrap=True)
        tbl.add_column(ratio=1)
        tbl.add_row(Text(name, style="bold reverse" if highlighted and not focused else "bold"), value)
        return Styled(tbl, "reverse") if focused else tbl

    def _repaint(self) -> None:
        k = self._hl
        if k is not None and k < len(getattr(self, "_items", [])):
            self.replace_option_prompt_at_index(k, self._prompt(k, True))

    def on_focus(self) -> None:
        self._repaint()

    def on_blur(self) -> None:
        self._repaint()
