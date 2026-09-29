"""Modal dialogs: help, go-to-row, column picker, export."""
from __future__ import annotations

import os

from textual import on
from textual.app import ComposeResult
from textual.binding import Binding
from textual.containers import Horizontal, Vertical, VerticalScroll
from textual.screen import ModalScreen
from textual.widgets import (Button, Checkbox, Input, Label, Markdown, OptionList, RadioButton, RadioSet,
                             SelectionList)
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
**→** accepts a column-name completion. **x** clears the filter, and
**Ctrl+X** does too, even while you are typing in the filter box.
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
| Home / End | first / last column. **‹ ›** beside the header mean more columns that way (click to page); the panel's bottom edge reads e.g. *‹ 11 · columns 12–21 of 64 · 43 ›* |
| **f** | toggle smart / raw number formatting |
| **y** | copy cell value to the clipboard |
| **i** | open statistics for the cursor column |

## Everywhere

| key | action |
|---|---|
| **1**–**5** · **Ctrl+← →** | go to a tab · previous / next tab. The tab strip in the panel border shows each tab's number and ends with the ^← ^→ reminder; a click on a number or name switches too |
| **e** | export the current view (filter + sort) to Parquet / CSV / JSON |
| **m** | toggle sampling for stats and plots on large files |
| **Esc** | cancel running queries |
| **Ctrl+P** | command palette |
| **?** | this help · **q** quit |

## Stats and Plot tabs

Stats: **l** toggles log-scale counts, **L** log-scale values, **[ ]** fewer / more bins.
Plot: click a field of the settings line (mode, columns, centre, colour), or
move to it with **tab** and press **enter**, to open a drop-down; type to
narrow long column lists. **← →** steps the current field; **r** rotates the sky map centre
between RA 0° and 180°. The colormaps are drawn in exact 256-colour values;
*terminal* uses only your terminal's palette.

## Look

pqx uses your terminal's own background and 16 colours. Choose the focus
colour with `--accent` (blue, cyan, magenta, green, yellow), how secondary
text is dimmed with `--dim` (faint, or bright-black for terminals without the
faint attribute) and the unfocused border colour with `--border`; or set
`PQX_ACCENT`, `PQX_DIM` and `PQX_BORDER` in your shell.
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


class FieldDropdown(ModalScreen[str | None]):
    """A drop-down list under a settings field: type to narrow, ↑↓/enter or click to pick."""

    BINDINGS = [Binding("escape", "cancel", "Cancel"),
                Binding("up", "move(-1)", "Up", show=False, priority=True),
                Binding("down", "move(1)", "Down", show=False, priority=True),
                Binding("pageup", "move(-10)", "Page up", show=False, priority=True),
                Binding("pagedown", "move(10)", "Page down", show=False, priority=True)]

    def __init__(self, title: str, options: list[str], current: str, x: int, y: int, max_height: int = 16):
        super().__init__()
        self.title_text = title
        self.all = list(options)
        self.current = current
        self.at = (x, y)
        self.max_height = max_height
        self.width = max([len(o) for o in options] + [len(title) + 12, 16]) + 4
        self.searchable = len(options) > 8

    def compose(self) -> ComposeResult:
        from .widgets import CursorList

        with Vertical(id="dropdown"):
            if self.searchable:
                yield Input(placeholder="type to filter…", id="dropdown-filter")
            yield CursorList(id="dropdown-list")

    def on_mount(self) -> None:
        self.app.call_after_refresh(self.app._render_keys)
        box = self.query_one("#dropdown")
        w = min(self.width, max(20, self.app.size.width - 2))
        h = min(len(self.all), self.max_height) + (1 if self.searchable else 0) + 2
        x = max(0, min(self.at[0], self.app.size.width - w))
        y = self.at[1]
        if y + h > self.app.size.height:  # no room below: open upwards
            y = max(0, self.at[1] - h - 1)
        self.box_w = w
        box.styles.width = w
        box.styles.height = h
        box.styles.offset = (x, y)
        self._fill("")
        if self.searchable:
            self.query_one("#dropdown-filter", Input).focus()
        else:
            self.query_one("#dropdown-list").focus()

    def _fill(self, flt: str) -> None:
        from rich.text import Text

        lst = self.query_one("#dropdown-list")
        f = flt.lower()
        shown = [o for o in self.all if f in o.lower()]
        lst.set_items([(o, Text(o, style="bold" if o == self.current else "")) for o in shown],
                      width=self.box_w - 4)
        if shown:
            lst.highlighted = shown.index(self.current) if self.current in shown else 0
        box = self.query_one("#dropdown")
        if getattr(self, "box_w", None):
            box.styles.height = max(1, min(len(shown), self.max_height)) + (1 if self.searchable else 0) + 2
        n = f"{len(shown)} of {len(self.all)}" if flt else f"{len(self.all)}"
        box.border_title = f"[dim]{self.title_text}  {n}[/]"

    @on(Input.Changed, "#dropdown-filter")
    def refilter(self, event: Input.Changed) -> None:
        self._fill(event.value)

    @on(Input.Submitted, "#dropdown-filter")
    def submit(self) -> None:
        lst = self.query_one("#dropdown-list")
        if lst.highlighted is not None and lst.option_count:
            self.dismiss(lst.get_option_at_index(lst.highlighted).id)

    @on(OptionList.OptionSelected, "#dropdown-list")
    def picked(self, event: OptionList.OptionSelected) -> None:
        self.dismiss(event.option.id)

    def action_move(self, d: int) -> None:
        lst = self.query_one("#dropdown-list")
        if not lst.option_count:
            return
        cur = lst.highlighted if lst.highlighted is not None else 0
        lst.highlighted = max(0, min(lst.option_count - 1, cur + d))

    def action_cancel(self) -> None:
        self.dismiss(None)

    def on_unmount(self) -> None:
        self.app.call_after_refresh(self.app._render_keys)

    def on_click(self, event) -> None:
        # a click outside the list closes it, like a native drop-down
        if self.get_widget_at(event.screen_x, event.screen_y)[0] is self:
            self.dismiss(None)
