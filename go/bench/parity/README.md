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
| `--strict-colours` | off | colour differences fail too (else they are only reported) |
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
`<VER>`, but its length decides what fits on a narrow screen. Build Go with
`make build VERSION=<python pqx's version>` for the narrow-title scenario.

The full suite (58 scenarios, most at 2 sizes, 2 apps) takes about 8 minutes with `-j 5`.

## Reading the report

`run.py` writes `<scratch>/report/`:

- `report.txt`: one line per scenario and size, then the details of each that isn't a
  clean pass;
- `summary.json`: the status and counts per scenario and size;
- `screens.json`: every captured screen (normalized text, clipboard) of every run, by
  `scenario@WxH:app`; `--only` writes `screens-APP.txt` and `screens-APP.json`.

| status | meaning |
|---|---|
| PASS | every checkpoint has the same text, the same cell styles and the same clipboard |
| DIFF | a checkpoint's text (or clipboard, or an exported file) differs |
| STYLE | the text is the same, but bold, faint, reverse, underline or italic differ somewhere |
| ERROR | the compared app failed a step: a `wait` never matched, a notification never showed, it exited |
| ERROR-REF | the reference failed a step: the scenario is wrong, or the reference is flaky |
| XFAIL | not a pass, and listed in `expected_failures.yaml` |
| XPASS | listed in `expected_failures.yaml` but passes: remove it from the list |

The exit status is 0 when every result is PASS or XFAIL.

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
0.7 s and drops the cells that changed. Colours are named for the 16 ANSI colours
(`brightblack`; a 256-colour index below 16 counts as the same name) and hex otherwise.

### Normalization

Before comparing, and before deciding a screen has settled, each line goes through
(see `NORMALIZE` in `run.py`):

- the version after `pqx ` → `<VER>`;
- timings (`0.12 s`, `120 ms`, `35µs`) → `<T>`, and a running count's `·  00:03 elapsed` is dropped;
- a spinner frame before a word → `*`;
- the run's output directory → `<OUT>`, the fixture directory → `<FIX>`, the slot
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
args: ["-w", "band = 'g'"]    # more arguments (before the file); {fixture} {fixtures} {out} expand
sizes: [[120, 40]]            # default: 120x40 and 200x50
ready: "✓ [\\d,]+ rows"        # startup regex (default: the fixture's, in fixtures.yaml)
threads: 1                    # --threads for this scenario (1 makes DuckDB aggregates repeatable)
quiet: 1.0                    # seconds of an unchanged screen that count as settled (default 0.5)
config: "columns:\n  ra: .2f\n"   # a formats.yaml to start with
file: false                   # don't open a file (for CLI-only scenarios)
steps:
  - keys: right right right   # named keys or single characters, space-separated (or a list)
  - keys: s
    check: asc                # capture a checkpoint named asc (after the screen settles)
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
```

A step can hold several of these; they run in this order: `keys`, `text`, a wait for
the screen to change (`change`, default up to 1.5 s), `sleep`, `wait`, `wait_gone`,
`toast`, settle (`settle: false` skips it, a number sets the quiet time), `check`,
`expect` / `expect_not` (regexes the screen must or must not show, reported as errors),
`clipboard`, `file`, `exit` (seconds within which the app must exit). After a `toast`
step the runner waits until the notification is gone (`toast_gone: false` skips that,
for a message that also stays in the status line).

Key names (`../pty/ptydrive.py`): `up down left right home end pgup pgdn ctrl+home
ctrl+end ctrl+left ctrl+right tab shift+tab enter esc backspace delete space ctrl+x
ctrl+u ctrl+a ctrl+e ctrl+c`, any single character, `click:X,Y` (an SGR mouse click at
1-based cell X,Y), `text:...` and `raw:\x1b[...` (with Python escapes). A lone Esc is
followed by a 0.2 s gap so it isn't read as Alt+key.

Writing scenarios that are stable:

- after a key that opens something, `wait` for text that only the new state shows; the
  quiet-time settle alone can end before a slow app has drawn;
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
`types` (one column per Arrow type, wide decimals, CJK), `units` (felis units,
RAJ2000/DEJ2000), `wide` (60 columns). They can be replaced by go/testdata/fixtures
once those exist, with `--fixtures`.

## Expected failures

`expected_failures.yaml` lists the scenarios the Go version is known to fail, with a
reason: `NAME: reason`, or `NAME@WxH: reason` for one size. They apply only to Python
vs Go runs. A listed scenario that fails is XFAIL (doesn't fail the run); one that
passes is XPASS, and should be removed from the list. `--write-xfail` rewrites the
list from the current run.

## Security test

`security.py` is the port of `tests/test_security.py::test_pty_terminal_never_receives_file_escapes`.
It writes a file whose values, column names, units, descriptions, key-value metadata
and file name hold OSC title, OSC 52, OSC 8, CSI, C1 and bidi sequences, drives the app
through the grid, detail pane, copy, `=`, every tab, the column picker, the export
dialog, completion, an error that quotes a file value and the help, and checks every
byte written: none of the file's sequences raw, no C1 control, OSC 52 payloads without
controls, no OSC kinds other than the app's own, window titles without controls, the
SQL in the file never ran, and the hostile text drawn as visible stand-ins (␛).

```sh
python security.py --app both      # or --app go; --keep keeps the raw output
```

## Benchmarks

`../pty/bench.py` times the targets of go-port.md ("Performance targets") in a 200x50
pty: first screen, PgDn, `g` to the middle row, Ctrl+End, Esc on a slow count, and Right
arrow on a 300-column screen (key until the screen changes: an upper bound on a frame).

```sh
python ../pty/bench.py --targets /scratch/dir --apps python,go --runs 3 --json bench.json
rm /scratch/dir/wide300.parquet /scratch/dir/rg2000.parquet   # ~1 GB
```

`--targets DIR` runs SSSource and mpc_orbits from the delivery directory (read-only)
and wide300 and rg2000, written into DIR if missing; it prints a table of medians
against the targets and against the Go prototype's numbers (+20% is a regression).
`esc.sh` and `ptytime.py` are the prototype's scripts, kept for comparison.

## Python behaviour the scenarios work around

Found while making Python pqx pass against itself; worth knowing when a Go difference
looks odd:

- After a dialog with a text input closes (go to row, format, export), the key bar can
  keep showing the filter box's keys until focus changes again.
- The Stats histogram is sometimes drawn at a stale width when Stats opens (seen at
  120x40, from `i` and from `3`), and stays so until it is redrawn; the scenarios press
  `l l` before checking it.
- Stats quantiles (`approx_quantile`) and sampled statistics vary between runs with
  more than one DuckDB thread.
- Two quick Ctrl+← from Schema once ended on Data instead of Metadata: a key sent
  while a tab switch is still in progress can be lost. The scenarios settle between
  them.
