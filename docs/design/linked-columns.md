# Linked columns: one current column across Data, Schema, Stats and Details

Status: approved 2026-10-06 · integration branch `linked-columns`

## Goal

pqx shows the same columns in four places: the Data grid, the Details pane
beside it, the Schema table and the Stats column list. Today each keeps its own
cursor, so switching views loses your place. After this change they share one
**current column**: wherever you move to a column, every other view is on it
too when you look at it.

## Behaviour

### 1. Data, Schema and Stats follow the current column

- The app keeps `current_column`. It is set when:
  - the grid cursor moves to another column (Data);
  - the row cursor moves in the Schema table;
  - the highlight moves in the Stats column list;
  - the selection moves in the Details pane (section 2);
  - `i` (grid → Stats) or Enter (Schema → Stats) jumps to a column.
- When a tab is shown (keys `1`–`3`, Ctrl+←/→, a click on the tab strip), it
  moves to the current column:
  - **Data:** the grid cursor goes to that column, on the same row, scrolling
    sideways as needed.
  - **Schema:** the cursor goes to that column's row; the description panel
    updates.
  - **Stats:** the column is highlighted and profiled (as `i` does today).
- **Edge cases:**
  - Column hidden in the grid (`-` or the column picker): the grid cursor stays
    put, and the status line says `‹col› is hidden · c to show`.
  - SQL-query results: their columns may not exist in the file's schema. Schema
    then keeps its own cursor. Stats lists the result's columns, so it follows
    as usual.
  - Plot is not linked; it has its own column pickers.
  - Moving a view's cursor while it is being synced must not bounce back (the
    sync itself must not count as the user moving).

### 2. A focusable Details pane

- The Details pane (`d`) becomes a list with one entry per column of the current
  row. Each entry shows the same content as today: name, full-precision value,
  unit, derived reading.
- **Focus:**
  - Tab from the grid, or a click in the pane, focuses it; the panel border shows
    focus in the accent colour like other panels.
  - Tab, Esc or Enter returns to the grid. Enter also leaves the grid cursor on
    the selected column.
  - `d` still opens and closes the pane, and closing it returns focus to the grid.
- **Moving:**
  - ↑/↓, PgUp/PgDn, Home/End or a click selects a column. The grid jumps sideways
    to that column; the row does not change.
  - The **mouse wheel only scrolls the pane**. It does not change the selection
    and does not move the grid.
  - Moving the grid cursor still moves the pane's selection (as today's
    highlighted row does).
- The selected entry uses the same reverse-video highlight as the other lists
  (`CursorList` look). When the pane is not focused, it is shown without the
  accent.

### Shared API (on the integration branch)

```python
self.current_column: str | None          # the column the user is on

def set_current_column(self, name: str | None, source: str) -> None
    # source: "grid" | "detail" | "schema" | "stats"; records the column

def _move_grid_to_column(self, name: str) -> bool
    # moves the grid cursor to `name` on the same row; False if it's hidden
```

Both features call these instead of touching each other's widgets.

## Docs

Update the footer key hints (`KEYS`), the help screen (`pqx/screens.py` HELP)
and README's keys table for the new Details keys and the linked behaviour.

## Plan

| Branch (from `linked-columns`) | Owner | Content |
|---|---|---|
| `linked-columns` | integrator | this document, the shared API stub, repo `CLAUDE.md` |
| `linked-columns-tabs` | subagent A | set the current column from grid, Schema and Stats; sync on tab switch; hidden/SQL edge cases; tests |
| `linked-columns-detail` | subagent B | Details pane as a focusable list; two-way link with the grid; focus keys; wheel scrolls only; tests |

- **Subagents:** each commits and pushes after every logical unit, keeps
  `pytest` and `ruff` green, and opens a PR into `linked-columns` when done.
- **Integrator:** reviews each PR, with an independent review agent for each of
  these two; sends fixes back; merges; runs the full suite after each merge.
- **Release:** when both are in and CI is green on the integration PR, the user
  approves merging `linked-columns` into `master`.
