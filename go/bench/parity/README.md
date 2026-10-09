# pty parity harness

Runs Python pqx (the reference) and Go pqx on the same key scripts in a real pty,
emulates the terminal with [pyte](https://github.com/selectel/pyte), and compares
the screens at checkpoints: the text exactly (after normalizing what differs between
runs), and the attributes of each cell (bold, faint, reverse, underline, italic,
colours) as a summary. This is the parity check D7(b) of
[`docs/design/go-port.md`](../../../docs/design/go-port.md); the scenarios are written
from the README, the `?` help and native-port.md, not from either implementation.

| file | |
|---|---|
| `run.py` | the runner |
| `scenarios/*.yaml` | the key scripts, one per file |
| `fixtures.yaml` | facts about the fixtures, and the startup regex for each |
| `make_fixtures.py` | writes the fixtures (deterministic) |
| `expected_failures.yaml` | known gaps of the Go version, reported as XFAIL |
| `security.py` | the pty security test (no escape sequence from a file reaches the terminal) |
| `../pty/ptydrive.py` | the pty and screen driver the three tools share |
| `../pty/bench.py` | the benchmarks against the performance targets |

## Running

You need a Python with `pyte`, `pyyaml` and `pyarrow`, and the reference pqx venv
(for the fixtures, which use `pqx.demo`):

```sh
python3 -m venv /tmp/hv && /tmp/hv/bin/pip install pyte pyyaml pyarrow
cd go && make build                      # the Go binary, go/bin/pqx
cd bench/parity
/tmp/hv/bin/python run.py                # Python vs Go: every scenario at 120x40 and 200x50
/tmp/hv/bin/python run.py 'filter-*' detail-pane --size 120x40
/tmp/hv/bin/python run.py --a python --b python     # Python against itself
/tmp/hv/bin/python run.py --only go startup         # just run Go and dump its screens
/tmp/hv/bin/python security.py --app both
```

| variable / option | default | |
|---|---|---|
| `PQX_PY` | `/root/parquet-explorer/.venv/bin/pqx` | the Python pqx command |
| `PQX_GO` | `go/bin/pqx` | the Go binary |
| `PQX_FIXTURE_PYTHON` | `/root/parquet-explorer/.venv/bin/python` | writes the fixtures (needs `pqx.demo`) |
| `--scratch DIR` / `PQX_PARITY_SCRATCH` | `$TMPDIR/pqx-parity` | fixtures, runs and the report go here |
| `--fixtures DIR` / `PQX_FIXTURES` | `<scratch>/fixtures` | made on first use |
| `-j N` | 4 | runs at a time. More makes the apps slow to react, and screens can settle before the app has answered |
| `--threads N` | 4 | passed to both apps as `--threads` (a scenario can ask for 1) |
| `--size WxH` | the scenario's `sizes`, else 120x40 and 200x50 | repeatable |
| `--lenient-colours` | off | colour differences are reported but don't fail (by default they fail) |
| `--keep-raw` | off | save every run's raw terminal output under `<report>/raw/` |
| `--timeout-scale X` | 1 | multiply every wait, for slow machines |
| `--write-xfail` | | rewrite `expected_failures.yaml` from this run (keeps the reasons already there) |
| `--list` | | list the scenarios |

Each run works in `<scratch>/runs/a|b/<scenario>-<WxH>/` (`a` is the reference),
with its own `XDG_CONFIG_HOME` (so saved column formats never leak between runs or
into your `~/.config`) and its own `out/` directory as the working directory (exports
land there). The two apps' paths have the same length, so an input box that shows the
end of a path shows the same characters; the slot letter is normalized. A run's
directory is deleted unless it had errors. Two runner invocations must not share a
scratch directory at the same time.

Both apps should report the same version: the title bar's version is normalized to
`<VER>`, but its length decides what fits on a narrow screen. Scenarios marked
`same_version: true` (narrow-title) are reported as SKIP when the two `--version`
outputs differ; build Go with `make build VERSION=<python pqx's version>` to run them.

The full suite (80 scenarios, most at 2 sizes, 2 apps) takes about 15 minutes with
`-j 6` for Python against itself, and much longer against an incomplete Go version
(every missed wait runs to its timeout). Keep `-j` at 8 or below on shared machines.

## Reading the report

`run.py` writes `<scratch>/report/`:

- `report.txt`: one line per scenario and size, then the details of each that isn't a
  clean pass;
- `summary.json`: the status, the failing checkpoints and the counts per scenario and size;
- `screens.json`: every captured screen (normalized text, clipboard) of every run, by
  `scenario@WxH:app`; `--only` writes `screens-APP.txt` and `screens-APP.json`.

| status | meaning |
|---|---|
| PASS | every checkpoint has the same text, the same cell styles and the same clipboard |
| DIFF | a checkpoint's text (or clipboard, or an exported file) differs |
| STYLE | the text is the same, but bold, faint, reverse, underline or italic differ somewhere |
| COLOUR | the text and styles are the same, but colours differ (`--lenient-colours` lets this pass) |
| ERROR | the compared app failed a step: a `wait` never matched, a notification never showed, it exited |
| ERROR-REF | the reference failed a step: the scenario is wrong, or the reference is flaky |
| XFAIL | fails exactly as `expected_failures.yaml` says (the same status, no other checkpoints) |
| CHANGED | listed, but fails differently: another status, or checkpoints not listed |
| XPASS | listed in `expected_failures.yaml` but passes: remove it from the list |
| SKIP | `same_version` scenario, and the two apps report different versions |
| PYBUG | passes once a known Python bug's region is left out (see below) |

The exit status is 0 when every result is PASS, XFAIL, SKIP or PYBUG.

A text difference is shown as the reference's line, the other app's line, and a line
of `^` under the characters that differ:

```
     21 python | │   0    170000000000000000     1000133  333.152881   -4.229770 …
            go | │  0  170000000000000000     1000133  333.153  -4.22977 …
               |   ^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^ …
```

Style and colour differences are summarized per screen row: how many cells, which
columns, which attributes, and the first cell's values (`reverse=True vs
reverse=False`). They are compared only on rows whose text is the same, and cells that
blink (a text cursor) are left out: each checkpoint samples the styles three times over
0.7 s and drops the cells that changed. The foreground colour of a plain space (no
reverse, underline or strikethrough) is ignored, as it can't be seen; its background,
reverse, underline and strikethrough still count (`run.py --selftest` checks these rules). Colours are named for the 16 ANSI colours
(`brightblack`; a 256-colour index below 16 counts as the same name) and hex otherwise.

### Normalization

Before comparing, and before deciding a screen has settled, each line goes through
(see `NORMALIZE` in `run.py`):

- the version after `pqx ` → `<VER>`;
- a timing between pqx's `·` separators (`·  0.12 s  ·`, `· 120 ms`: the status line,
  the Stats and Plot headers, the export notice) → `<T>`; timings anywhere else are
  compared as they are;
- a running count's `·  00:03 elapsed` is blanked (same width, so what follows stays put);
- a spinner frame at the start of a status line (`│ ⠹ Counting`) → `*`;
- the run's output directory → `<OUT>`, its config directory → `<CONFIG>`, the fixture
  directory → `<FIX>`, the slot
  letter in `runs/a/…` → `_`;
- trailing spaces.

A scenario can add its own rules (`normalize:`). Python run against itself must show
no differences (`run.py --a python --b python`, three times in a row) before a change
to the rules or the scenarios is committed.

## Scenarios

One YAML file per scenario in `scenarios/`:

```yaml
description: |
  `s` sorts by the cursor column: ascending, descending, then off.
covers:                       # the README / help / design lines this checks
  - "README All keys: s | sort by the cursor column (asc → desc → off)"
fixture: demo                 # <fixtures>/demo.parquet, opened as the last argument
args: ["-w", "band = 'g'"]    # more arguments (before the file); {fixture} {fixtures} {out} {config} expand
sizes: [[120, 40]]            # default: 120x40 and 200x50
ready: "✓ [\\d,]+ rows"        # startup regex (default: the fixture's, in fixtures.yaml)
threads: 1                    # --threads for this scenario (1 makes DuckDB aggregates repeatable)
quiet: 1.0                    # seconds of an unchanged screen that count as settled (default 0.5)
config: "columns:\n  ra: .2f\n"   # a formats.yaml to start with
file: false                   # don't open a file (for CLI-only scenarios)
same_version: true            # SKIP unless both apps report the same version
steps:
  - keys: right right right   # named keys or single characters, space-separated (or a list)
  - keys: s
    wait: 'sorted ra ↑'       # a marker only the key's effect produces
    check: asc                # capture a checkpoint named asc (after the screen settles)
    expect: 'ra ↑'            # and the reference must show what the scenario claims
    expect@120x40: 'row 0'    # a key ending in @WxH applies only at that size
  - text: "band = 'r'"        # type literally ({out} etc. expand)
  - keys: enter
    wait: "✓ [\\d,]+ rows"     # then wait for this regex on the (normalized) screen
  - keys: y
    clipboard: "^r$"          # the last OSC 52 write, decoded, must match
    toast: "Copied"           # wait for this notification, check while it shows, then wait it out
    check: copied
  - keys: e
    wait: "Export current view"
  - file: "{out}/view.csv"    # compare the exported file (CSV/JSON head, Parquet rows and columns)
  - click: '(?<= )ra(?= )'    # an SGR mouse click on the first match on the screen (dx/dy to shift)
  - resize: [100, 30]         # resize the terminal (TIOCSWINSZ and SIGWINCH)
```

A step can hold several of these; they run in this order: `keys`, `text`, `click`,
`resize`, a wait for
the screen to change (`change`, default up to 1.5 s), `sleep`, `wait`, `wait_gone`,
`toast`, settle (`settle: false` skips it, a number sets the quiet time), `check`,
`expect` / `expect_not` (regexes the screen must or must not show, reported as errors:
every scenario asserts with them what it claims, on the reference too),
`clipboard`, `file`, `exit` (seconds within which the app must exit). After a `toast`
step the runner waits until the notification is gone (`toast_gone: false` skips that,
for a message that also stays in the status line).

Key names (`../pty/ptydrive.py`): `up down left right home end pgup pgdn ctrl+home
ctrl+end ctrl+left ctrl+right tab shift+tab enter esc backspace delete space ctrl+x
ctrl+u ctrl+a ctrl+e ctrl+c`, any single character, `click:X,Y` (an SGR mouse click at
1-based cell X,Y), `text:...` and `raw:\x1b[...` (with Python escapes). A lone Esc is
followed by a 0.2 s gap so it isn't read as Alt+key.

Writing scenarios that are stable:

- after a key that opens something, `wait` for text that only the new state shows (the
  status line's `row 1,234`, a dialog's title), never for text that may already be on
  the screen (the key bar, the text just typed); the quiet-time settle alone can end
  before a slow app has drawn. Settling watches the text and the cell styles (a single
  blinking cell doesn't count);
- the key bar can update a moment after the rest of the screen: wait for it too when
  it matters (`wait: "Format of[\\s\\S]*enter apply"`);
- stats and plots use approximate quantiles: give those scenarios `threads: 1`;
- notifications disappear after a few seconds: use `toast:` so the checkpoint is taken
  while it shows and the next one after it has gone;
- sort on a column with distinct values, or the order of ties (and an export) varies;
- check the result with `--a python --b python` before adding it.

The fixtures (`make_fixtures.py`, facts in `fixtures.yaml`): `demo` (20,000 LSST-like
rows from `pqx.demo`, the tests' `demo_path`), `slow` (2,000,000 rows, for a count slow
enough to cancel), `odd` (the tests' `odd_path`: nested types, NaN/inf, a quoted name),
`types` (one column per Arrow type, wide decimals, CJK, a long string), `units` (felis
units, RAJ2000/DEJ2000), `wide` (60 columns), `notparquet` (a text file).

## Expected failures

`expected_failures.yaml` lists, per scenario and size, how the Go version is known to
fail: the status (`error`, `diff`, `style`, `colour`), the checkpoints that differ (or
that it never reached) and a reason:

```yaml
xfail:
  sort-cycle@120x40:
    status: error
    checks: [asc, desc, off]
    reason: 'go: step 2 {"keys": "s", "wait": "sorted dec ↑"}: never matched'
```

Every entry needs the size, status and checks (a blanket `NAME: reason` is refused), and
the reason generated says what differs first, for example `first: line 48: python
'columns 1–8 of 16' vs go '…'` or `style on row 7, 112 cells from column 2: bold=True vs
bold=False`. It applies only to Python vs Go runs. A run fails (CHANGED) when a listed entry fails
differently: another status, or a checkpoint not in its list; it fails (XPASS) when a
listed entry passes. `--write-xfail` rewrites the file from the current run, so the
next run on the same binary is green; reasons already there are kept.

## Security test

`security.py` is the port of `tests/test_security.py::test_pty_terminal_never_receives_file_escapes`.
It writes a file whose values, column names, units, descriptions, key-value metadata
and file name hold OSC title, OSC 52, OSC 8, CSI, C1, bidi and zero-width sequences,
binary values and strings that aren't valid UTF-8, and drives the app through `=` on
the SQL-injection value, the detail pane, copy (grid and pane), every tab, the column
picker, the export dialog, the completion of a column name that is SQL, an error that
quotes a file value and the help. After each step that opens something it waits for a
marker of that screen (from Python pqx's text) and fails if it never appears, so a pass
means the hostile text was really drawn there.

Then it checks every byte the app wrote. First, none of the fixture's own sequences may
appear as bytes (its OSC title, OSC 52, OSC 8, `ESC[2J ESC[31m`, the units' `ESC[5m`, its
C1 strings, …): a parser alone would accept those, as they are well-formed. Then it
parses every escape sequence, and each must be one an app writes itself: CSI with the usual finals (cursor, erase, modes, SGR, reports, cursor shape,
keyboard protocol; not `t`, whose reports type text back), OSC 0/1/2 only with the app's
own title (`pqx`, or `pqx` and the file's name) and no controls, OSC 52 only with a
base64 payload whose text has no controls and none of the file's injected text,
two-byte escapes only `ESC 7 8 = > M D E H` and `ESC ( B` (no reset, no line drawing), OSC 10/11/12 queries, OSC 22,
DCS only as capability queries (`+q`, `$q`); no APC/PM/SOS, no unterminated sequence.
The text between them must be valid UTF-8 (a raw 0x80–0x9F byte isn't), with no C1
control, no C0 control other than CR, LF, tab, backspace and BEL, and no bidi or
zero-width code point (U+061C, U+200B–U+200F, U+202A–U+202E, U+2060, U+2066–U+2069,
U+FEFF). The files the SQL in the fixture would write must not exist, and ␛ must have
been drawn.

```sh
python security.py --app both      # or --app go; --keep keeps the raw output
python security.py --selftest      # the byte checks against made-up streams, one fault each (27)
```

The checks were also run against a wrapper that injects each fault into Python pqx's
output (raw 0x9B, a DCS string, U+200B, a CSI with an unknown final, a cut-off OSC):
each is reported.

## Benchmarks

`../pty/bench.py` times the targets of go-port.md ("Performance targets") in a 200x50
pty: first screen, PgDn, `g` to the middle row, Ctrl+End, Esc on a slow count, and Right
arrow on a 300-column screen. PgDn, `g` and Ctrl+End are timed until the expected row
is on the emulated screen, so they include pyte's processing of the redraw (a few ms at
200x50). The Right-arrow time is taken on the raw pty bytes, without the emulator: from
the key until the app has written the frame (the end of a synchronized update, or the
last byte before 30 ms of silence). That includes the app's input handling, its work
and the pty, so it is an upper bound on go-port.md's "frame" (5 ms, measured inside the
app), not the same measurement.

```sh
python ../pty/bench.py --targets /scratch/dir --apps python,go --runs 3 --json bench.json
rm /scratch/dir/wide300.parquet /scratch/dir/rg2000.parquet   # ~1 GB
```

`--targets DIR` runs SSSource and mpc_orbits from the delivery directory (read-only)
and wide300 and rg2000, written into DIR if missing; it prints a table of medians
against the targets and against the Go prototype's numbers (+20% is a regression).
`esc.sh` and `ptytime.py` are the prototype's scripts, kept for comparison.

## Known Python bugs (PYBUG)

A scenario can mark a region of some checkpoints as a known Python pqx bug, so that a
correct Go screen isn't reported as a difference:

```yaml
python_bug:
  - checks: [row-1234, half, last]   # these checkpoints
    region: keybar                   # the last screen line
    ref_shows: 'enter apply   esc back'      # the bug, as the reference shows it
    correct: '^ / filter   x clear filter'   # what the compared app must show there
    sizes: [[120, 40]]               # optional: only at these sizes
    note: "Python pqx's key bar can keep the filter box's keys after a dialog with an input closes (racy)"
```

The region is left out of the text, style and colour comparison at a listed checkpoint
only when the reference really shows the bug there (its key bar matches `ref_shows`)
and the compared app shows what is right (its key bar matches `correct`); otherwise the
checkpoint is compared in full, so a wrong key bar in the compared app is a DIFF. A
scenario that passes only because of the left-out region is reported as PYBUG (not a
failure), with the note; everything else on those screens is still compared.

| scenario | checkpoints | bug |
|---|---|---|
| goto-row, goto-suffix | after each jump | the key bar keeps the filter box's keys after the go-to dialog closes |
| format-F, formats-saved, detail-format | after the format dialog closes | the same, after the format dialog |
| dialog-no-shift | after | the same, after the go-to and format dialogs |
| export-csv, export-parquet, export-json | done | the same, after the export dialog |
| columns-picker | applied, cancelled | the same, after the column picker |

## Python behaviour the scenarios work around

Found while making Python pqx pass against itself; worth knowing when a Go difference
looks odd:

- The key bar can lag: after a dialog with a text input closes (go to row, format,
  export) it can keep showing the filter box's keys, and when such a dialog opens it can
  show the grid's keys for a moment; waits for these dialogs include the key bar.
- The column readout under the grid (`columns 1–11 of 16 · 5 ›`) isn't always redrawn
  when the grid widens (the detail panel closing, a tab switch); the scenarios move the
  cursor right and back first.
- The Stats histogram is sometimes drawn at a stale width when Stats opens (seen at
  120x40, from `i` and from `3`), and stays so until it is redrawn; the scenarios press
  `l l` before checking it.
- Stats quantiles (`approx_quantile`) and sampled statistics vary between runs with
  more than one DuckDB thread.
- A filter that keeps the record under the cursor doesn't keep it, or the leftmost
  column, when the grid is scrolled right (`keep-viewport-filter`: row 30 → row 0,
  leftmost c05 → c06), against native-port.md's "Keep the viewport". After `=` and `x`
  the record and rows are kept (`keep-viewport-equals`).
- The detail panel shows a decimal256(50, 0) value as `1e+46`, not at full precision.
- An unknown `--theme` exits with Textual's own message ("Theme 'x' has not been
  registered. Call 'App.register_theme' …").
- Two quick Ctrl+← from Schema once ended on Data instead of Metadata: a key sent
  while a tab switch is still in progress can be lost. The scenarios settle between
  them.
