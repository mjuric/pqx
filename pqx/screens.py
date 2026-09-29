"""Modal dialogs: help, go-to-row, column picker, export."""
from __future__ import annotations

import os

from textual import on
from textual.app import ComposeResult
from textual.binding import Binding
from textual.containers import Horizontal, Vertical, VerticalScroll
from textual.screen import ModalScreen
from textual.widgets import Button, Checkbox, Input, Label, Markdown, RadioButton, RadioSet, SelectionList
from textual.widgets.selection_list import Selection

HELP = """\
# pqx — Parquet explorer

Browse, filter, profile and plot Parquet files of any size. Nothing is loaded
in full: the grid pulls small windows of rows and all aggregates are pushed
down into DuckDB.

## Filtering and queries

Press **/** and type either

* a SQL **WHERE** expression — `mag < 21 and band = 'r'`,
  `ssObjectId is not null`, `ra between 10 and 20`, `regexp_matches(name, '^20')`
* or a full **query** over the table `t` —
  `select band, count(*), avg(mag) from t group by 1 order by 1`

**Enter** applies, **Esc** returns to the grid, **↑/↓** browse history and
**→** accepts a column-name completion. **x** clears the filter.
The filter applies everywhere: grid, stats, plots and export.

## Data grid

| key | action |
|---|---|
| arrows, PgUp/PgDn | move (the window of rows slides seamlessly) |
| Ctrl+Home / Ctrl+End | first / last row |
| **g** | go to row — `1234`, `1.5M`, `50%`, `-1` |
| **s** | sort by the cursor column: ascending → descending → off |
| **=** | narrow the filter to rows equal to the cursor cell |
| **d** or Enter | toggle the row detail panel (full precision, sexagesimal, UTC dates) |
| **c** | choose visible columns; **-** hides the cursor column |
| **p** | pin columns up to the cursor (stay visible when scrolling right) |
| **f** | toggle smart / raw number formatting |
| **y** | copy cell value to the clipboard |
| **i** | open statistics for the cursor column |

## Everywhere

| key | action |
|---|---|
| **1**–**5** | Data / Schema / Stats / Plot / Metadata tabs |
| **e** | export the current view (filter + sort) to Parquet / CSV / JSON |
| **m** | toggle sampling for stats and plots on large files |
| **Esc** | cancel running queries |
| **Ctrl+P** | command palette (themes, …) |
| **?** | this help · **q** quit |

## Stats and Plot tabs

Stats: **l** toggles log-scale counts, **L** log-scale values, **[ ]** fewer / more bins.
Plot: choose *Sky (Mollweide)* or *Scatter*, the columns and the colormap;
**r** rotates the sky map centre between RA 0° and 180°.
"""


class HelpScreen(ModalScreen[None]):
    BINDINGS = [Binding("escape,q,question_mark", "dismiss", "Close")]

    def compose(self) -> ComposeResult:
        with Vertical(id="help-box", classes="dialog"):
            with VerticalScroll():
                yield Markdown(HELP)
            yield Label("[dim]Esc to close[/dim]", id="help-foot")

    def action_dismiss(self, result=None) -> None:  # type: ignore[override]
        self.dismiss(None)


class GotoScreen(ModalScreen[str | None]):
    BINDINGS = [Binding("escape", "cancel", "Cancel")]

    def __init__(self, total: int | None):
        super().__init__()
        self.total = total

    def compose(self) -> ComposeResult:
        with Vertical(classes="dialog small"):
            yield Label("[b]Go to row[/b]")
            hint = f"of {self.total:,}" if self.total is not None else "(row count still being computed)"
            yield Label(f"[dim]1234 · 1.5M · 50% · -1 (last)   {hint}[/dim]")
            yield Input(placeholder="row", id="goto-input")

    def on_mount(self) -> None:
        self.query_one(Input).focus()

    @on(Input.Submitted)
    def submitted(self, event: Input.Submitted) -> None:
        self.dismiss(event.value.strip() or None)

    def action_cancel(self) -> None:
        self.dismiss(None)


class ColumnPicker(ModalScreen[list[str] | None]):
    BINDINGS = [Binding("escape", "cancel", "Cancel"), Binding("ctrl+a", "all", "All", priority=True),
                Binding("ctrl+n", "none", "None", priority=True)]

    def __init__(self, columns: list[tuple[str, str]], visible: list[str]):
        super().__init__()
        self.columns = columns
        self.chosen = set(visible)

    def compose(self) -> ComposeResult:
        with Vertical(classes="dialog", id="picker-box"):
            yield Label("[b]Visible columns[/b]  [dim]space toggles · Ctrl+A all · Ctrl+N none[/dim]")
            yield Input(placeholder="type to filter columns…", id="picker-filter")
            yield SelectionList[str](*self._selections(""), id="picker-list")
            with Horizontal(classes="buttons"):
                yield Button("Apply", variant="primary", id="apply")
                yield Button("Cancel", id="cancel")

    def _selections(self, flt: str):
        f = flt.lower()
        return [Selection(f"{n}  [dim]{t}[/dim]", n, n in self.chosen)
                for n, t in self.columns if f in n.lower()]

    @on(Input.Changed, "#picker-filter")
    def refilter(self, event: Input.Changed) -> None:
        sl = self.query_one(SelectionList)
        self._sync()
        sl.clear_options()
        sl.add_options(self._selections(event.value))

    @on(Input.Submitted, "#picker-filter")
    def to_list(self) -> None:
        self.query_one(SelectionList).focus()

    def _sync(self) -> None:
        sl = self.query_one(SelectionList)
        shown = {sl.get_option_at_index(i).value for i in range(sl.option_count)}
        self.chosen = (self.chosen - shown) | set(sl.selected)

    def action_all(self) -> None:
        self.query_one(SelectionList).select_all()

    def action_none(self) -> None:
        self.query_one(SelectionList).deselect_all()

    @on(Button.Pressed, "#apply")
    def apply(self) -> None:
        self._sync()
        cols = [n for n, _ in self.columns if n in self.chosen]
        if not cols:
            self.notify("Select at least one column", severity="warning")
            return
        self.dismiss(cols)

    @on(Button.Pressed, "#cancel")
    def action_cancel(self) -> None:
        self.dismiss(None)


class ExportScreen(ModalScreen[dict | None]):
    BINDINGS = [Binding("escape", "cancel", "Cancel")]

    def __init__(self, default_path: str, summary: str):
        super().__init__()
        self.default_path = default_path
        self.summary = summary

    def compose(self) -> ComposeResult:
        with Vertical(classes="dialog", id="export-box"):
            yield Label("[b]Export current view[/b]")
            yield Label(f"[dim]{self.summary}[/dim]")
            yield Input(value=self.default_path, id="export-path")
            with RadioSet(id="export-format"):
                yield RadioButton("Parquet (zstd)", value=True, id="fmt-parquet")
                yield RadioButton("CSV", id="fmt-csv")
                yield RadioButton("JSON (newline-delimited)", id="fmt-json")
            yield Checkbox("Only the visible columns", value=True, id="export-visible")
            yield Checkbox("Overwrite if the file exists", value=False, id="export-overwrite")
            with Horizontal(classes="buttons"):
                yield Button("Export", variant="primary", id="export")
                yield Button("Cancel", id="cancel")

    def on_mount(self) -> None:
        self.query_one("#export-path", Input).focus()

    @on(RadioSet.Changed)
    def fmt_changed(self, event: RadioSet.Changed) -> None:
        inp = self.query_one("#export-path", Input)
        fmt = self._fmt()
        base, ext = os.path.splitext(inp.value)
        if ext.lower() in (".parquet", ".csv", ".json", ".ndjson", ".pq"):
            inp.value = base + {"parquet": ".parquet", "csv": ".csv", "json": ".json"}[fmt]

    def _fmt(self) -> str:
        pressed = self.query_one(RadioSet).pressed_button
        return {"fmt-csv": "csv", "fmt-json": "json"}.get(pressed.id if pressed else "", "parquet")

    @on(Input.Submitted, "#export-path")
    @on(Button.Pressed, "#export")
    def do_export(self) -> None:
        path = self.query_one("#export-path", Input).value.strip()
        if not path:
            self.notify("Enter a file name", severity="warning")
            return
        full = os.path.abspath(os.path.expanduser(path))
        if os.path.exists(full) and not self.query_one("#export-overwrite", Checkbox).value:
            self.notify(f"{path} exists — tick 'Overwrite' to replace it", severity="warning")
            return
        self.dismiss(dict(path=full, fmt=self._fmt(),
                          visible_only=self.query_one("#export-visible", Checkbox).value))

    @on(Button.Pressed, "#cancel")
    def action_cancel(self) -> None:
        self.dismiss(None)
