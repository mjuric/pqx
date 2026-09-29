"""Small shared widgets."""
from __future__ import annotations

from rich.text import Text
from textual import on
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
