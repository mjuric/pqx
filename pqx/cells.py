"""Grid cells that format themselves when first drawn.

A window of a wide file holds tens of thousands of cells, of which a few hundred
are on screen. Each :class:`Cell` keeps its raw value and formats it (into Rich
``Text``, via the column's :class:`~pqx.fmt.CellFormatter`) only when it is
rendered or read, then caches the result. The cells of one grid column share a
:class:`ColumnCells`: bumping its ``gen`` (raw toggle, format override) makes
every cell of the column re-format on its next draw, without touching the cells.

Rows are :class:`CellRow` mappings that make a cell only when it is first
read, and DataTable renders a row from a :class:`RowCells` view of it: a window
of 150 x 300 values then allocates cells for what is drawn, not all 45,000.

A column never narrows here: when a cell turns out wider than its column, the
column grows (see :meth:`ColumnCells.fit`).
"""
from __future__ import annotations

import heapq
from bisect import bisect_left, bisect_right
from collections.abc import Sequence
from math import isfinite
from typing import Any, Callable

from rich.cells import cell_len
from rich.text import Text

from .fmt import CellFormatter


class ColumnCells:
    """What the cells of one grid column share: formatter, raw mode, a generation, the column.

    ``column`` is the DataTable ``Column`` whose ``content_width`` the cells widen;
    ``on_grow`` is called (with no arguments) after any widening, so the table can
    redraw with the new widths."""

    __slots__ = ("fmt", "raw", "gen", "column", "on_grow")

    def __init__(self, fmt: CellFormatter, raw: bool, column: Any = None,
                 on_grow: Callable[[], None] | None = None):
        self.fmt = fmt
        self.raw = raw
        self.gen = 0
        self.column = column
        self.on_grow = on_grow

    def invalidate(self, raw: bool | None = None) -> None:
        """Make every cell of the column format again when next drawn or read."""
        if raw is not None:
            self.raw = raw
        self.gen += 1

    def fit_values(self, values) -> None:
        """Grow the column to fit ``values`` formatted as its cells would be."""
        plain, raw = self.fmt.plain, self.raw
        self.fit(max((text_width(plain(v, raw)) for v in values), default=0))

    def fit(self, width: int) -> None:
        """Grow the column to ``width`` cells, if it is narrower."""
        col = self.column
        if col is not None and width > col.content_width:
            col.content_width = width
            if self.on_grow is not None:
                self.on_grow()


#: Kinds whose widest cell :func:`widest_candidates` can pick out.
GUESSABLE_KINDS = frozenset(("int", "float", "float32", "mjd", "angle", "mag", "flux", "err", "str"))


def widest_candidates(values, kind: str, raw: bool = False, k: int = 3) -> list | None:
    """The values of a column whose formatting is likely the widest, or None if
    this can't be guessed (then sample rows instead).

    Numbers: the formatted width follows from sign and magnitude, so the largest
    and smallest values (most integer digits) and the negative and positive values
    nearest zero (most leading zeros, or the scientific form) — ``k`` of each, as
    one may lose trailing zeros. Shown raw, a number is its ``repr``: the longest
    one. Strings: the ``k`` longest. NaN, ±∞ and NULL format short."""
    if kind not in GUESSABLE_KINDS:
        return None
    try:
        if kind == "str":
            return heapq.nlargest(k, (v for v in values if v is not None), key=lambda v: len(str(v)))
        vals = [v for v in values if v is not None and isfinite(v)]
        if raw:
            return [max(vals, key=lambda v: len(repr(v)))] if vals else []
        vals.sort()
    except (TypeError, ValueError, ArithmeticError):  # not plain numbers after all
        return None
    if len(vals) <= 4 * k:
        return vals
    neg, pos = bisect_left(vals, 0), bisect_right(vals, 0)
    return vals[:k] + vals[max(0, neg - k):neg] + vals[pos:pos + k] + vals[-k:]


def text_width(t: Text | str) -> int:
    """Width a text needs on one line: its widest line (as DataTable measures it)."""
    s = t if isinstance(t, str) else t.plain
    if s.isascii() and s.isprintable():  # the usual case: one cell per character
        return len(s)
    if "\n" not in s:
        return cell_len(s)
    return max(cell_len(line) for line in s.split("\n"))


class Cell:
    """One grid cell: a raw value, formatted on first use and cached until the column changes."""

    __slots__ = ("value", "col", "_text", "_gen")

    def __init__(self, value: Any, col: ColumnCells):
        self.value = value
        self.col = col
        self._gen = -1
        self._text: Text | None = None

    @property
    def text(self) -> Text:
        col = self.col
        if self._gen != col.gen:
            t = col.fmt(self.value, col.raw)
            self._text = t
            self._gen = col.gen
            col.fit(text_width(t))
        return self._text

    @property
    def formatted(self) -> bool:
        """Whether the cell is formatted for its column's current state."""
        return self._gen == self.col.gen

    def __rich__(self) -> Text:
        return self.text

    def __str__(self) -> str:
        return self.text.plain

    def __repr__(self) -> str:
        return f"Cell({self.value!r})"


class RowLayout:
    """The columns of a window's rows: keys in order, their ColumnCells, and each key's position."""

    __slots__ = ("keys", "ccols", "index")

    def __init__(self, keys: list, ccols: list[ColumnCells]):
        self.keys = keys[: len(ccols)]
        self.ccols = ccols
        self.index = {k: i for i, k in enumerate(self.keys)}


class CellRow(dict):
    """One grid row as DataTable stores it (cells by column key), each cell made on first access.

    An unknown key raises KeyError, as a plain row dict would; a row shorter
    than its columns reads as NULLs, as DataTable's ``add_row`` pads it."""

    __slots__ = ("values", "layout")

    def __init__(self, values: tuple, layout: RowLayout):
        super().__init__()
        self.values = values
        self.layout = layout

    def __missing__(self, key) -> Cell:
        i = self.layout.index[key]
        cell = self[key] = Cell(self.values[i] if i < len(self.values) else None, self.layout.ccols[i])
        return cell

    def cell_at(self, i: int) -> Cell:
        """The cell of the ``i``-th column."""
        key = self.layout.keys[i]
        cell = dict.get(self, key)
        return self[key] if cell is None else cell


class RowCells(Sequence):
    """A row's cells by column position, made as they're read: what DataTable
    renders a row from (``RowRenderables.cells``), without making all of them."""

    __slots__ = ("row",)

    def __init__(self, row: CellRow):
        self.row = row

    def __len__(self) -> int:
        return len(self.row.layout.keys)

    def __getitem__(self, i):
        if isinstance(i, slice):
            return [self.row.cell_at(j) for j in range(len(self))[i]]
        if i < 0:
            i += len(self)
        if not 0 <= i < len(self):
            raise IndexError(i)
        return self.row.cell_at(i)
