package dialogs

import "strings"

// FilterExample is the filter box's example when the file has nothing
// better to show, and the help's (Python's FILTER_EXAMPLE).
const FilterExample = "price > 100 and city = 'Paris'"

// Help is the help text in Markdown, Python's HELP (screens.py); the help
// dialog renders it.
var Help = strings.NewReplacer("‵", "`", "{example}", FilterExample).Replace(helpSource)

// helpSource is Help with ‵ for the backquote (a Go raw string can't hold
// one).
const helpSource = `# pqx — Parquet explorer

Browse, filter, profile and plot Parquet files of any size. Nothing is loaded
in full: the grid pulls small windows of rows and all aggregates are pushed
down into DuckDB.

## Filtering and queries

Press **/** and type either

* a SQL **WHERE** expression — ‵{example}‵,
  ‵email is not null‵, ‵price between 10 and 20‵, ‵regexp_matches(name, '^20')‵
* or a full **query** over the table ‵t‵ —
  ‵select city, count(*), avg(price) from t group by 1 order by 1‵

**Enter** applies, **Esc** returns to the grid, **↑/↓** browse history and
**→** accepts a column-name completion. **x** clears the filter, and
**Ctrl+X** does too, even while you are typing in the filter box.
The filter applies everywhere: grid, stats, plots and export.

## Data grid

| key | action |
|---|---|
| arrows, PgUp/PgDn | move (the window of rows slides seamlessly) |
| Ctrl+Home / Ctrl+End | first / last row |
| **g** | go to row — ‵1234‵, ‵1.5M‵, ‵50%‵, ‵-1‵ |
| **s** | sort by the cursor column: ascending → descending → off |
| **=** | narrow the filter to rows equal to the cursor cell; the cursor stays on the same record |
| **d** or Enter | toggle the row detail panel (full precision, sexagesimal, UTC dates); **Esc** closes it |
| Tab or a click | into the detail panel: **↑/↓**, PgUp/PgDn, Home/End or a click pick a column and the grid follows on the same row (the wheel only scrolls); **Enter** or **Tab** return to the grid on that column; **Esc** also closes the panel |
| in the detail panel | **=**, **y**, **i**, **F**, **<** / **>** act on the selected field as on that grid cell; after **=** you stay in the panel on the same field and record, so **=** on one field and then another narrows in two keystrokes |
| **c** | choose visible columns; **-** hides the cursor column |
| **p** | pin columns up to the cursor (stay visible when scrolling right) |
| Home / End | first / last column. **‹ ›** beside the header mean more columns that way (click to page); the panel's bottom edge reads e.g. *‹ 11 · columns 12–21 of 64 · 43 ›* |
| **f** | toggle smart / raw number formatting |
| **<** / **>** | one digit fewer / more for the cursor column (decimals for MJD, angles and magnitudes, significant digits otherwise) |
| **F** | set the cursor column's format: a Python spec (‵.2f‵, ‵.3e‵, ‵,d‵) or a number of digits; empty resets it |
| **y** | copy cell value to the clipboard |
| **i** | open statistics for the cursor column |

Column formats set with **<**, **>** and **F** are remembered by column name
for every file, in ‵~/.config/pqx/formats.yaml‵ (under ‵$XDG_CONFIG_HOME‵ if set).
They change only the grid and stats; the detail panel and **y** keep full precision.

## Everywhere

| key | action |
|---|---|
| **1**–**5** · **Ctrl+← →** | go to a tab · previous / next tab. The tab strip in the panel border shows each tab's number and ends with the ^← ^→ reminder; a click on a number or name switches too |
| **e** | export the current view (filter + sort) to Parquet / CSV / JSON |
| **m** | toggle sampling for stats and plots on large files |
| **Esc** | cancel running queries; when nothing is running, leave the filter box or close the detail panel |
| **?** | this help · **q** quit |

Data, Schema and Stats stay on the same column: move to a column in one and
the others are on it when you switch tabs (the grid keeps its row). If the
column is hidden in the grid, the grid stays put and the status line says
so; **c** brings it back. Plot keeps its own columns.

## Stats and Plot tabs

Stats: **l** toggles log-scale counts, **L** log-scale values, **[ ]** fewer / more bins.
Plot: click a field of the settings line (mode, columns, centre, colour), or
move to it with **tab** and press **enter**, to open a drop-down; type to
narrow long column lists. **← →** steps the current field; **r** rotates the sky map centre
between RA 0° and 180°. The colormaps are drawn in exact 256-colour values;
*terminal* uses only your terminal's palette.

## Look

pqx uses your terminal's own background and 16 colours. Choose the focus
colour with ‵--accent‵ (blue, cyan, magenta, green, yellow), how secondary
text is dimmed with ‵--dim‵ (faint, or bright-black for terminals without the
faint attribute) and the unfocused border colour with ‵--border‵; or set
‵PQX_ACCENT‵, ‵PQX_DIM‵ and ‵PQX_BORDER‵ in your shell.
`
