"""Pty parity runner: drive Python pqx and Go pqx through the same key scripts and compare screens.

    run.py [options] [SCENARIO ...]          # Python vs Go, every scenario, 120x40 and 200x50
    run.py --a python --b python             # Python against itself (normalization check)
    run.py --only go cursor-keys             # just run one app and dump its screens

SCENARIO is a scenario name or a glob over names (scenarios/*.yaml). The apps are
PQX_PY (default /root/parquet-explorer/.venv/bin/pqx) and PQX_GO (default go/bin/pqx).
Needs pyte and pyyaml (and pyarrow for exported-file checks). See README.md for the
scenario format and how to read the report.
"""
from __future__ import annotations

import argparse
import concurrent.futures as cf
import fnmatch
import glob
import json
import os
import re
import shutil
import subprocess
import sys
import tempfile
import time

import yaml

HERE = os.path.dirname(os.path.abspath(__file__))
GO_ROOT = os.path.dirname(os.path.dirname(HERE))
sys.path.insert(0, os.path.join(GO_ROOT, "bench", "pty"))
import pyte  # noqa: E402
from ptydrive import SPINNER, Session, key_bytes, styles_changed  # noqa: E402

SCRATCH_DEFAULT = os.environ.get("PQX_PARITY_SCRATCH", os.path.join(tempfile.gettempdir(), "pqx-parity"))
APPS = {
    "python": os.environ.get("PQX_PY", "/root/parquet-explorer/.venv/bin/pqx"),
    "go": os.environ.get("PQX_GO", os.path.join(GO_ROOT, "bin", "pqx")),
}
#: the Python that writes the fixtures (needs pyarrow, numpy and pqx.demo)
FIXTURE_PYTHON = os.environ.get("PQX_FIXTURE_PYTHON", "/root/parquet-explorer/.venv/bin/python")
DEFAULT_SIZES = [(120, 40), (200, 50)]
#: seconds of an unchanged screen that count as settled (a scenario or step can set `quiet`)
QUIET = 0.5
#: after sending keys, wait up to this long for the screen to change before settling
CHANGE = 1.5
#: a checkpoint samples the styles over this long, to find blinking cells (a cursor
#: blinking at 1 Hz or faster is caught: three samples span more than half a period)
BLINK = 0.7

# ------------------------------------------------------------------ normalization
#: (pattern, replacement), applied to each screen line in order, before comparing and
#: before deciding the screen has settled. Each covers something that differs between
#: runs of the same app, not between apps.
NORMALIZE = [
    # the version after pqx's name ("pqx 0.1.0", "pqx v0.2.0rc3-28-gb4da3e8"): title, help, --version
    (r"(?<=pqx )v?\d+\.\d+(?:\.\d+)?(?:[-+.]?[0-9A-Za-z]+)*(?:\.dirty|-dirty)?", "<VER>"),
    # a timing between pqx's "·" separators, as the status line, the Stats and Plot headers
    # and the export notice print it ("·  0.12 s  ·", "· 120 ms"); nowhere else
    (r"(?<=·)(\s+)\d+(?:\.\d+)?\s?(?:ms|µs|us|s)\b", r"\1<T>"),
    # the elapsed time of a running count ("·  00:03 elapsed"), shown only after a while
    # (blanked, not removed, so what is drawn to its right stays in place)
    (r"\s+·\s+\d+:\d\d(?::\d\d)?\s?elapsed\b", lambda m: " " * len(m.group(0))),
    # a spinner frame at the start of a status line ("│ ⠹ Counting rows")
    (rf"(?:(?<=^)|(?<=^ )|(?<=│ )|(?<=│  ))[{SPINNER}](?= \S)", "*"),
]


def make_normalizer(extra=(), subs=()):
    rules = [(re.compile(p), r) for p, r in NORMALIZE] + [(re.compile(p), r) for p, r in extra]

    def norm_line(s: str) -> str:
        for old, new in subs:
            if old:
                s = s.replace(old, new)
        for rx, rep in rules:
            s = rx.sub(rep, s)
        return s.rstrip()

    def norm(text: str) -> str:
        return "\n".join(norm_line(ln) for ln in text.split("\n"))

    return norm_line, norm


# ------------------------------------------------------------------ scenarios
def load_scenarios(paths=None):
    out = {}
    for f in sorted(paths or glob.glob(os.path.join(HERE, "scenarios", "*.yaml"))):
        with open(f) as fh:
            sc = yaml.safe_load(fh)
        sc.setdefault("name", os.path.splitext(os.path.basename(f))[0])
        sc["_file"] = f
        for k in ("fixture", "steps", "description"):
            if k not in sc:
                raise SystemExit(f"{f}: missing {k!r}")
        out[sc["name"]] = sc
    return out


def fixture_facts():
    with open(os.path.join(HERE, "fixtures.yaml")) as fh:
        return yaml.safe_load(fh)


def ensure_fixtures(fdir, names):
    missing = [n for n in names if not os.path.exists(os.path.join(fdir, f"{n}.parquet"))]
    if missing:
        subprocess.run([FIXTURE_PYTHON, os.path.join(HERE, "make_fixtures.py"), fdir, "--only", *missing],
                       check=True, env=dict(os.environ, PYTHONPATH=os.environ.get("PQX_FIXTURE_PYTHONPATH", "")))


# ------------------------------------------------------------------ running one app
class StepError(Exception):
    pass


def as_keys(v):
    if isinstance(v, list):
        return [str(k) for k in v]
    return str(v).split()


def run_unit(sc, slot, app, cmd, size, fixtures, scratch, threads, timeout_scale=1.0, keep_raw=None):
    """Run scenario `sc` on one app at one size. Returns the captures and any errors.

    Each run works in <scratch>/runs/<slot>/<scenario>-<WxH>/, with `slot` a or b, so the
    paths the two apps show have the same length (an input box showing the end of a path
    then shows the same characters) and normalize to the same text."""
    cols, rows = size
    work = os.path.join(scratch, slot, f"{sc['name']}-{cols}x{rows}")
    shutil.rmtree(work, ignore_errors=True)
    out_dir = os.path.join(work, "out")
    os.makedirs(out_dir)
    os.makedirs(os.path.join(work, "config"))
    subs = {"fixtures": fixtures, "out": out_dir, "fixture": os.path.join(fixtures, f"{sc['fixture']}.parquet"),
            "config": os.path.join(work, "config")}

    def fill(s):  # {fixture}, {fixtures}, {out}, {config}; other braces (regexes) stay
        if not isinstance(s, str):
            return s
        for k, v in subs.items():
            s = s.replace("{" + k + "}", v)
        return s

    subs_norm = [(out_dir, "<OUT>"), (subs["config"], "<CONFIG>"), (fixtures, "<FIX>"),
                 (f"/{slot}/{sc['name']}", f"/_/{sc['name']}")]
    # the slot in a path cut by wrapping or by an input box's edge
    extra = [(r"(?<=runs/)[ab](?=/)", "_")] + sc.get("normalize", [])
    norm_line, norm = make_normalizer(extra, subs_norm)
    argv = [cmd, *[fill(a) for a in sc.get("args", [])]]
    if sc.get("file", True):
        argv.append(subs["fixture"])
    threads = sc.get("threads", threads)  # a scenario can ask for 1 (deterministic aggregates)
    if threads:
        argv += ["--threads", str(threads)]
    env = dict(os.environ, XDG_CONFIG_HOME=os.path.join(work, "config"))
    for k in ("PQX_ACCENT", "PQX_DIM", "PQX_BORDER"):  # the user's own settings
        env.pop(k, None)
    env.update({k: fill(str(v)) for k, v in (sc.get("env") or {}).items()})
    if sc.get("config"):  # a formats.yaml to start from
        os.makedirs(os.path.join(work, "config", "pqx"))
        with open(os.path.join(work, "config", "pqx", "formats.yaml"), "w") as fh:
            fh.write(sc["config"])
    res = {"app": app, "slot": slot, "size": f"{cols}x{rows}", "checks": [], "errors": [], "argv": argv}
    t0 = time.perf_counter()
    s = Session(argv, cols, rows, env=env, cwd=out_dir)
    T = timeout_scale

    def capture(name):
        """The screen text, and its styles sampled over BLINK s: cells whose style changes
        in that time (a blinking cursor) are left out of the style comparison."""
        lines = [norm_line(ln) for ln in s.lines()]
        styles = s.styles()
        blink = set()
        for _ in range(2):  # samples 0, BLINK/2 and BLINK s apart: one pair straddles a blink
            s.drain(BLINK / 2)
            later = s.styles()
            blink |= {(y, x) for y in range(s.rows) for x in range(s.cols) if styles[y][x] != later[y][x]}
        blink = sorted([y, x] for y, x in blink)
        clip = s.osc52()
        res["checks"].append({"name": name, "lines": lines, "styles": styles, "blink": blink,
                              "plain": plain_spaces(s.screen.buffer, s.rows, s.cols),
                              "clipboard": [norm_line(c) for c in clip[-1:]], "exited": s.exited})

    def changed_since(text, styles):
        return s.text() != text or styles_changed(styles, s.styles())

    try:
        ready = sc.get("ready", fixture_facts().get(sc["fixture"], {}).get("ready"))
        if ready is not None and not s.wait(ready, 30 * T, norm=norm):
            res["errors"].append(f"startup: never matched {ready!r}")  # carry on: the diffs say more
        if not s.settle(sc.get("startup_quiet", 0.8), 20 * T, norm=norm):
            res["errors"].append("startup: screen never settled")
        for i, st in enumerate(sc["steps"]):
            if isinstance(st, str):
                st = {"keys": st}
            # a key ending in @WxH applies only at that size ("expect@120x40": ...)
            here = f"@{cols}x{rows}"
            st = {k.split("@")[0]: v for k, v in st.items() if "@" not in k or k.endswith(here)}
            if not st:
                continue
            where = f"step {i + 1} {json.dumps(st, ensure_ascii=False)[:80]}"
            quiet = st.get("quiet", sc.get("quiet", QUIET))
            sends = "keys" in st or "text" in st or "click" in st or "resize" in st
            raw_mark = len(s.raw)
            if sends:
                before = (s.text(), s.styles())
            if "keys" in st:
                s.keys(*as_keys(st["keys"]), gap=st.get("gap", 0.05))
            if "text" in st:
                s.type(fill(st["text"]), gap=st.get("gap", 0.0))
            if "click" in st:
                # a mouse click on the first place the screen matches a regex (+dx, dy cells)
                pos = s.find(fill(st["click"]))
                if pos is None:
                    raise StepError(f"{where}: nothing on the screen matches {st['click']!r} to click")
                x, y = pos[0] + st.get("dx", 0), pos[1] + st.get("dy", 0)
                s.write(key_bytes(f"click:{x},{y}"))
            if "resize" in st:
                s.resize(*st["resize"])
            if sends and st.get("change", CHANGE) and st.get("settle", True) is not False:
                # the app may take a moment to react: wait for a change before
                # waiting for quiet (a key that changes nothing costs CHANGE seconds)
                end = time.perf_counter() + float(st.get("change", CHANGE)) * T
                while time.perf_counter() < end and not changed_since(*before):
                    s.pump(0.01)
            if "sleep" in st:
                s.drain(float(st["sleep"]))
            if "wait" in st:
                if not s.wait(fill(st["wait"]), st.get("timeout", 20) * T, norm=norm):
                    raise StepError(f"{where}: never matched {st['wait']!r}")
            if "wait_gone" in st:
                if not s.wait_gone(fill(st["wait_gone"]), st.get("timeout", 20) * T):
                    raise StepError(f"{where}: still showing {st['wait_gone']!r}")
            if "toast" in st:
                # a notification: wait for it, check it while it shows, then let it go.
                # One that came and went before the screen was looked at counts as shown
                # if its text went through the terminal (the checkpoint then lacks it).
                if not s.wait(fill(st["toast"]), st.get("timeout", 10) * T, norm=norm) and not re.search(
                        fill(st["toast"]), plain_text(bytes(s.raw[raw_mark:]))):
                    res["errors"].append(f"{where}: notification {st['toast']!r} never shown")
                quiet = min(quiet, 0.5)  # (pqx's notifications last 2 s and more)
            if st.get("settle", True) is not False and (sends or "wait" in st or "check" in st or "toast" in st):
                q = st["settle"] if not isinstance(st.get("settle", True), bool) else quiet
                if not s.settle(float(q), 15 * T, norm=norm):
                    res["errors"].append(f"{where}: screen never settled")
            if "check" in st:
                capture(str(st["check"]))
            # expectations are checked on the checkpoint's own screen when the step took
            # one (a notification may be gone by now), else on the screen as it is
            seen = "\n".join(res["checks"][-1]["lines"]) if "check" in st else norm(s.text())
            for key, want in (("expect", True), ("expect_not", False)):
                if key in st:
                    pats = st[key] if isinstance(st[key], list) else [st[key]]
                    for p in pats:
                        found = re.search(fill(p), seen, re.M) is not None
                        if found != want:
                            res["errors"].append(f"{where}: {'missing' if want else 'unexpected'} {p!r}")
            if "clipboard" in st:
                clip = s.osc52()
                if not clip or not re.search(st["clipboard"], clip[-1]):
                    res["errors"].append(f"{where}: clipboard {clip[-1:]!r} does not match {st['clipboard']!r}")
            if "file" in st:
                res["checks"].append({"name": f"file:{st['file']}", "file": describe_file(fill(st["file"]))})
            if "exit" in st:
                end = time.perf_counter() + float(st["exit"]) * T
                while s.exited is None and time.perf_counter() < end:
                    s.pump(0.05)
                if s.exited is None:
                    res["errors"].append(f"{where}: app did not exit")
            if "toast" in st and st.get("toast_gone", True):
                if not s.wait_gone(fill(st["toast"]), st.get("toast_timeout", 15) * T):
                    res["errors"].append(f"{where}: notification {st['toast']!r} never went away")
                s.settle(quiet, 15 * T, norm=norm)
    except StepError as e:
        res["errors"].append(str(e))
        capture("<at error>")
    except Exception as e:  # noqa: BLE001 - report, don't stop the run
        res["errors"].append(f"harness: {type(e).__name__}: {e}")
    finally:
        if s.exited is not None and not any("exit" in (st if isinstance(st, dict) else {}) for st in sc["steps"]):
            res["errors"].append(f"app exited with status {s.exited}")
        if keep_raw:
            with open(os.path.join(keep_raw, f"{sc['name']}.{res['size']}.{app}.raw"), "wb") as fh:
                fh.write(bytes(s.raw))
        s.close()
        res["seconds"] = round(time.perf_counter() - t0, 2)
        if not res["errors"]:
            shutil.rmtree(work, ignore_errors=True)
    return res


def plain_text(b: bytes) -> str:
    """Terminal output without its escape sequences (other than SGR, they become spaces)."""
    b = re.sub(rb"\x1b\[[0-?]*m", b"", b)
    b = re.sub(rb"\x1b\[[0-?]*[ -/]*[@-~]", b" ", b)
    b = re.sub(rb"\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)", b"", b)
    return b.decode("utf-8", "replace")


def describe_file(path):
    if not os.path.exists(path):
        return {"exists": False}
    d = {"exists": True}
    if path.endswith(".parquet"):
        try:
            import pyarrow.parquet as pq
            md = pq.ParquetFile(path).metadata
            d.update(rows=md.num_rows, columns=pq.ParquetFile(path).schema_arrow.names)
        except Exception as e:  # noqa: BLE001
            d["error"] = str(e)
    else:
        with open(path, "rb") as fh:
            d["head"] = fh.read(4000).decode("utf-8", "replace").splitlines()[:20]
    return d


# ------------------------------------------------------------------ comparing
STYLE_NAMES = ("fg", "bg", "bold", "dim", "reverse", "underline", "italic")


#: the grid's status line (pqx's symbols: ✓ done, * running, ! warning, ✗ error)
STATUS_LINE = re.compile(r"^ │ [✓*!✗] ")


def python_bug_for(sc, name, size, key="python_bug"):
    """The scenario's `python_bug` (or `intended`) entry that covers checkpoint `name` at
    `size`, if any."""
    for pb in sc.get(key) or []:
        if name in pb.get("checks", []) and (not pb.get("sizes") or list(size) in [list(z) for z in pb["sizes"]]):
            return pb
    return None


def rule_applies(pb, ref, other, key):
    """A python_bug (key "correct") or intended (key "other_shows") entry, if the
    reference shows `ref_shows` and the other app shows `pb[key]` in the entry's region."""
    if not pb:
        return None
    return python_bug_applies({**pb, "correct": pb.get(key, "(?!)")}, ref, other) and pb


def python_bug_applies(pb, ref, other):
    """`pb` if the reference shows the bug in its region at this checkpoint (its last line
    matches `ref_shows`) and the other app shows what is right there (`correct`); else
    None, and the checkpoint is compared in full."""
    if not pb or not ref.get("lines") or not other.get("lines"):
        return None
    if pb.get("region") == "keybar":
        r, o = ref["lines"][-1], other["lines"][-1]
    elif pb.get("region") in ("screen", "status", "pane"):
        r, o = "\n".join(ref["lines"]), "\n".join(other["lines"])
    else:
        return None
    if re.search(pb["ref_shows"], r, re.M) and re.search(pb["correct"], o, re.M):
        return pb
    return None


def mask_region(c, region):
    """A capture with a screen region blanked out: `keybar` is the last line, `screen` all
    of it (for a bug that changes the whole checkpoint)."""
    if region == "screen":
        return {**c, "lines": [], "styles": [], "plain": [], "clipboard": []}
    if region == "pane":  # the details pane: every column from its left border on
        x = next((ln.index("┌─ row") for ln in c["lines"] if "┌─ row" in ln), None)
        if x is None:
            return c
        c = dict(c)
        c["lines"] = [ln[:x] for ln in c["lines"]]
        c["styles"] = [st[:x] + [(None,) * 7] * (len(st) - x) for st in c["styles"]]
        if c.get("plain"):
            c["plain"] = [p[:x] + "1" * (len(p) - x) for p in c["plain"]]
        return c
    if region == "status":  # the grid's status line: "│ ✓ 20,000 rows  ·  row 0", "│ * Counting rows"
        c = dict(c)
        idx = [i for i, ln in enumerate(c["lines"]) if STATUS_LINE.match(ln)]
        c["lines"] = [("" if i in idx else ln) for i, ln in enumerate(c["lines"])]
        c["styles"] = [([(None,) * 7] * len(st) if i in idx else st) for i, st in enumerate(c["styles"])]
        return c
    if region != "keybar":
        raise ValueError(f"unknown python_bug region {region!r}")
    c = dict(c)
    c["lines"] = c["lines"][:-1] + [""]
    c["styles"] = c["styles"][:-1] + [[(None,) * 7] * len(c["styles"][-1])]
    if c.get("plain"):
        c["plain"] = c["plain"][:-1] + ["1" * len(c["plain"][-1])]
    return c


def plain_spaces(buf, rows, cols):
    """Per screen row, "1" for each cell that is a space without reverse, underline or
    strikethrough (its foreground colour can't be seen), else "0"."""
    return ["".join("1" if (c.data == " " and not c.reverse and not c.underscore and not c.strikethrough)
                    else "0" for c in (buf[y][x] for x in range(cols))) for y in range(rows)]


def selftest():
    """compare_check on made-up captures: what counts as a style or colour difference."""
    sp, x = " ", "x"

    def cap(chars, styles):
        cells = [[pyte.screens.Char(ch, fg, bg, reverse=rev, underscore=und)
                  for ch, (fg, bg, rev, und) in zip(chars, styles)]]
        return {"lines": ["".join(chars)], "blink": [],
                "styles": [[(c.fg, c.bg, c.bold, c.blink, c.reverse, c.underscore, c.italics) for c in cells[0]]],
                "plain": plain_spaces(cells, 1, len(chars))}

    d0 = ("default", "default", False, False)
    cases = [  # (name, chars, styles A, styles B, colour cells, style cells)
        ("space fg ignored", [sp], [d0], [("red", "default", False, False)], 0, 0),
        ("space bg counts", [sp], [d0], [("default", "blue", False, False)], 1, 0),
        ("reversed space fg counts", [sp], [("red", "default", True, False)], [("green", "default", True, False)],
         1, 0),
        ("underlined space fg counts", [sp], [("red", "default", False, True)], [("green", "default", False, True)],
         1, 0),
        ("space reverse counts", [sp], [d0], [("default", "default", True, False)], 0, 1),
        ("letter fg counts", [x], [d0], [("red", "default", False, False)], 1, 0),
    ]
    bad = 0
    pb = {"checks": ["c"], "region": "keybar", "ref_shows": "enter apply", "correct": "^ / filter"}

    def pair(ref_bar, other_bar):
        a_, b_ = cap([x, x], [d0, d0]), cap([x, x], [d0, d0])
        a_["lines"], b_["lines"] = ["grid", ref_bar], ["grid", other_bar]
        a_["styles"], b_["styles"] = a_["styles"] * 2, b_["styles"] * 2
        return a_, b_

    a_, b_ = pair(" x", " x")
    a_["lines"][0], b_["lines"][0] = "wide 1e+46", "wide 1000000000000000000000"
    it = {"checks": ["c"], "region": "screen", "ref_shows": r"1e\+46", "other_shows": "10{20}"}
    ok = bool(rule_applies(it, a_, b_, "other_shows")) and not rule_applies(it, b_, a_, "other_shows")
    print(f"{'ok  ' if ok else 'FAIL'} intended, region screen: applies only to the listed difference")
    bad += not ok
    pa_, pb_ = pair(" x", " x")
    pa_["lines"] = ["│ grid ┐ ┌─ row 0 ─┐", "│ 1    │ │ wide 1e+46"]
    pb_["lines"] = ["│ grid ┐ ┌─ row 0 ─┐", "│ 1    │ │ wide 1000000000000"]
    pc_ = {**pb_, "lines": ["│ grid ┐ ┌─ row 0 ─┐", "│ 2    │ │ wide 1000000000000"]}
    ok = (not compare_check(mask_region(pa_, "pane"), mask_region(pb_, "pane"))["text"]
          and compare_check(mask_region(pa_, "pane"), mask_region(pc_, "pane"))["text"])
    print(f"{'ok  ' if ok else 'FAIL'} region pane: the pane is left out, the grid beside it isn't")
    bad += not ok
    for name, ref_bar, other_bar, want_masked in [
            ("bug shown, other app right: left out", " enter apply   esc back", " / filter   x clear", True),
            ("bug shown, other app wrong: compared", " enter apply   esc back", " BOGUS KEYBAR", False),
            ("bug not shown: compared", " / filter   x clear", " BOGUS KEYBAR", False)]:
        a_, b_ = pair(ref_bar, other_bar)
        got = python_bug_applies(pb, a_, b_)
        d = compare_check(*(mask_region(c, "keybar") for c in (a_, b_))) if got else compare_check(a_, b_)
        ok = bool(got) == want_masked and bool(d["text"]) != want_masked
        print(f"{'ok  ' if ok else 'FAIL'} python_bug keybar, {name}")
        bad += not ok
    for name, chars, sa, sb, want_c, want_s in cases:
        d = compare_check(cap(chars, sa), cap(chars, sb))
        ok = (d["colour"], d["style"]) == (want_c, want_s)
        print(f"{'ok  ' if ok else 'FAIL'} {name}: colour {d['colour']}, style {d['style']}")
        bad += not ok
    return 1 if bad else 0


def compare_check(a, b):
    """Differences between two captures of the same checkpoint."""
    if "file" in a or "file" in b:
        return {"text": [] if a.get("file") == b.get("file") else [("file", a.get("file"), b.get("file"))],
                "style": 0, "colour": 0, "clipboard": None}
    la, lb = a["lines"], b["lines"]
    text = [(i, x, y) for i, (x, y) in enumerate(zip(la, lb)) if x != y]
    if len(la) != len(lb):
        text.append((min(len(la), len(lb)), f"<{len(la)} lines>", f"<{len(lb)} lines>"))
    style = colour = 0
    style_lines, colour_lines = {}, {}
    same = {i for i in range(min(len(la), len(lb)))} - {t[0] for t in text}
    blink = {tuple(c) for c in a.get("blink", []) + b.get("blink", [])}
    pa, pb = a.get("plain"), b.get("plain")
    for y in sorted(same):
        ra, rb = a["styles"][y], b["styles"][y]
        for x, (ca, cb) in enumerate(zip(ra, rb)):
            if ca == cb or (y, x) in blink:
                continue
            if (pa and pa[y][x] == "1") and (pb and pb[y][x] == "1"):
                # a space without reverse, underline or strike shows no foreground: its
                # foreground colour doesn't count (its background and attributes do)
                ca, cb = (None,) + tuple(ca[1:]), (None,) + tuple(cb[1:])
                if ca == cb:
                    continue
            if ca[2:] != cb[2:]:
                style += 1
                style_lines.setdefault(y, []).append((x, ca, cb))
            if ca[:2] != cb[:2]:
                colour += 1
                colour_lines.setdefault(y, []).append((x, ca, cb))
    clip = None if a.get("clipboard") == b.get("clipboard") else (a.get("clipboard"), b.get("clipboard"))
    return {"text": text, "style": style, "colour": colour, "style_lines": style_lines,
            "colour_lines": colour_lines, "clipboard": clip}


def describe_diff(d, la, lb):
    """The first difference of a checkpoint, in a line: where, and the two texts around it."""
    if d["text"]:
        i, x, y = d["text"][0]
        if i == "file":
            return "the exported file differs"
        j = next((k for k in range(max(len(x), len(y))) if x[k:k + 1] != y[k:k + 1]), 0)
        lo = max(0, j - 12)
        more = f" (+{len(d['text']) - 1} more lines)" if len(d["text"]) > 1 else ""
        return f"line {i}, column {j}: {la} {x[lo:lo + 40]!r} vs {lb} {y[lo:lo + 40]!r}{more}"
    if d["clipboard"]:
        return f"clipboard {d['clipboard'][0]!r} vs {d['clipboard'][1]!r}"
    for kind in ("style", "colour"):
        if d[kind]:
            y, cells = next(iter(d[f"{kind}_lines"].items()))
            a, b = cells[0][1], cells[0][2]
            names = [n for n, u, v in zip(STYLE_NAMES, a, b) if u != v]
            return (f"{kind} on row {y}, {len(cells)} cells from column {cells[0][0]}: "
                    f"{_fmt_style(a, names)} vs {_fmt_style(b, names)} ({d[kind]} cells in all)")
    return "differs"


def summarize_style_diffs(lines_map, which, limit=6):
    out = []
    for y, cells in list(lines_map.items())[:limit]:
        xs = [c[0] for c in cells]
        a, b = cells[0][1], cells[0][2]
        names = [n for n, u, v in zip(STYLE_NAMES, a, b) if u != v]
        out.append(f"      row {y}: {len(cells)} cells, cols {xs[0]}–{xs[-1]}: {', '.join(names)} "
                   f"({_fmt_style(a, names)} vs {_fmt_style(b, names)})")
    if len(lines_map) > limit:
        out.append(f"      … and {len(lines_map) - limit} more rows with {which} differences")
    return out


def _fmt_style(s, names):
    d = dict(zip(STYLE_NAMES, s))
    return " ".join(f"{n}={d[n]}" for n in names)


def caret(x, y):
    n = max(len(x), len(y))
    return "".join("^" if (x[i:i + 1] or " ") != (y[i:i + 1] or " ") else " " for i in range(n)).rstrip()


# ------------------------------------------------------------------ main
def main(argv=None):
    p = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    p.add_argument("scenarios", nargs="*", help="names or globs (default: all)")
    p.add_argument("--a", default="python", help="reference app: python or go (default python)")
    p.add_argument("--b", default="go", help="app compared with it (default go)")
    p.add_argument("--only", metavar="APP", help="just run APP and dump its screens")
    p.add_argument("--size", action="append", help="WxH (repeatable; default 120x40 and 200x50, "
                                                    "or the scenario's own)")
    p.add_argument("--fixtures", default=os.environ.get("PQX_FIXTURES"),
                   help="fixture directory (default <scratch>/fixtures; made when missing)")
    p.add_argument("--scratch", default=SCRATCH_DEFAULT, help="where runs write (default %(default)s)")
    p.add_argument("--out", help="report directory (default <scratch>/report)")
    p.add_argument("--xfail", default=os.path.join(HERE, "expected_failures.yaml"))
    p.add_argument("--no-xfail", action="store_true", help="ignore the expected-failures file")
    p.add_argument("--write-xfail", action="store_true",
                   help="rewrite the expected-failures file with this run's failures (keeps old reasons)")
    p.add_argument("--jobs", "-j", type=int, default=4, help="runs at a time (default 4; more makes "
                                                                   "the apps slow to react and screens settle late)")
    p.add_argument("--threads", type=int, default=4, help="--threads passed to each app (0: none)")
    p.add_argument("--lenient-colours", action="store_true",
                   help="colour differences are reported but don't fail (they fail by default)")
    p.add_argument("--timeout-scale", type=float, default=1.0)
    p.add_argument("--keep-raw", action="store_true", help="save each run's raw terminal output")
    p.add_argument("--list", action="store_true", help="list the scenarios and exit")
    p.add_argument("--selftest", action="store_true", help="check the style comparison on made-up cells")
    a = p.parse_args(argv)
    if a.selftest:
        return selftest()

    scen = load_scenarios()
    if a.list:
        for n, sc in scen.items():
            print(f"{n:28} {sc['fixture']:6} {sc['description'].strip().splitlines()[0]}")
        return 0
    if a.scenarios:
        names = [n for n in scen if any(fnmatch.fnmatch(n, g) for g in a.scenarios)]
        if not names:
            raise SystemExit(f"no scenario matches {a.scenarios}")
        scen = {n: scen[n] for n in names}

    os.makedirs(a.scratch, exist_ok=True)
    fixtures = os.path.abspath(a.fixtures or os.path.join(a.scratch, "fixtures"))
    ensure_fixtures(fixtures, sorted({sc["fixture"] for sc in scen.values()}))
    out = os.path.abspath(a.out or os.path.join(a.scratch, "report"))
    os.makedirs(out, exist_ok=True)
    runs_dir = os.path.join(a.scratch, "runs")
    os.makedirs(runs_dir, exist_ok=True)
    raw_dir = os.path.join(out, "raw") if a.keep_raw else None
    if raw_dir:
        os.makedirs(raw_dir, exist_ok=True)

    apps = [a.only] if a.only else [a.a, a.b]
    labels = [a.only] if a.only else (["A:" + a.a, "B:" + a.b] if a.a == a.b else apps)
    for app in set(apps):
        if app not in APPS:
            raise SystemExit(f"unknown app {app!r} (python or go)")
        if not shutil.which(APPS[app]) and not os.access(APPS[app], os.X_OK):
            raise SystemExit(f"{app}: {APPS[app]} is not executable (set PQX_PY / PQX_GO)")
    versions = {app: version_of(APPS[app]) for app in set(apps)}

    sizes_cli = [tuple(int(v) for v in s.lower().split("x")) for s in (a.size or [])]
    units = []
    for n, sc in scen.items():
        sizes = sizes_cli or [tuple(s) for s in sc.get("sizes", [])] or DEFAULT_SIZES
        for size in sizes:
            for lab, app in zip(labels, apps):
                units.append((n, size, lab, app))

    print(f"{len(scen)} scenarios, {len(units)} runs, {a.jobs} at a time; "
          + ", ".join(f"{k} = {APPS[k]} ({versions[k]})" for k in sorted(set(apps))), file=sys.stderr)
    results = {}
    t0 = time.perf_counter()
    with cf.ThreadPoolExecutor(a.jobs) as ex:
        futs = {ex.submit(run_unit, scen[n], "ab"[labels.index(lab)], app, APPS[app], size, fixtures, runs_dir,
                          a.threads, a.timeout_scale, raw_dir): (n, size, lab) for n, size, lab, app in units}
        for f in cf.as_completed(futs):
            n, size, lab = futs[f]
            r = f.result()
            results[(n, size, lab)] = r
            print(f"  {n} {size[0]}x{size[1]} {lab}: {r['seconds']} s"
                  + (f"  ({len(r['errors'])} errors)" if r["errors"] else ""), file=sys.stderr)
    elapsed = time.perf_counter() - t0

    if a.only:
        return dump_only(scen, results, labels[0], out, versions)
    xfail = {} if (a.no_xfail or a.b != "go" or a.a == a.b) else load_xfail(a.xfail)
    return report(scen, results, labels, out, xfail, a, versions, elapsed)


def version_of(cmd):
    try:
        p = subprocess.run([cmd, "--version"], capture_output=True, text=True, timeout=30)
        return (p.stdout or p.stderr).strip().splitlines()[0]
    except Exception as e:  # noqa: BLE001
        return f"? ({e})"


def load_xfail(path):
    if not os.path.exists(path):
        return {}
    with open(path) as fh:
        d = yaml.safe_load(fh) or {}
    entries = d.get("xfail") or {}
    for key, e in entries.items():  # only entries that say what fails: no blanket ones
        if "@" not in key or not isinstance(e, dict) or "status" not in e or "checks" not in e:
            raise SystemExit(f"{path}: {key}: an entry needs NAME@WxH and status, checks, reason "
                             f"(regenerate with --write-xfail)")
    return entries


def write_screens_json(path, results):
    """Every capture, by "scenario@WxH:app", without the per-cell styles."""
    with open(path, "w") as fh:
        json.dump({f"{n}@{s[0]}x{s[1]}:{lab}": {"errors": r["errors"], "argv": r["argv"], "checks": [
            {k: v for k, v in c.items() if k not in ("styles", "plain")} for c in r["checks"]]}
            for (n, s, lab), r in sorted(results.items())}, fh, ensure_ascii=False, indent=0)


def dump_only(scen, results, lab, out, versions):
    path = os.path.join(out, f"screens-{lab}.txt")
    bad = 0
    with open(path, "w") as fh:
        fh.write(f"# {lab}: {versions[lab]}\n")
        for (n, size, _), r in sorted(results.items()):
            fh.write(f"\n{'=' * 100}\n{n} {size[0]}x{size[1]}  ({r['seconds']} s)\n")
            for e in r["errors"]:
                fh.write(f"  ERROR {e}\n")
            bad += bool(r["errors"])
            for c in r["checks"]:
                fh.write(f"--- {c['name']}\n")
                if "file" in c:
                    fh.write(json.dumps(c["file"], indent=1) + "\n")
                    continue
                fh.write("\n".join(c["lines"]) + "\n")
                if c.get("clipboard"):
                    fh.write(f"[clipboard] {c['clipboard'][-1]!r}\n")
    write_screens_json(os.path.join(out, f"screens-{lab}.json"), results)
    print(f"screens: {path}; {bad} of {len(results)} runs had errors", file=sys.stderr)
    for (n, size, _), r in sorted(results.items()):
        for e in r["errors"]:
            print(f"  {n} {size[0]}x{size[1]}: {e}", file=sys.stderr)
    return 1 if bad else 0


def report(scen, results, labels, out, xfail, a, versions, elapsed):
    la, lb = labels
    rows, summary = [], {"a": la, "b": lb, "versions": versions, "seconds": round(elapsed, 1), "scenarios": {}}
    counts = {}
    for n, sc in scen.items():
        sizes = sorted({k[1] for k in results if k[0] == n})
        for size in sizes:
            ra, rb = results[(n, size, la)], results[(n, size, lb)]
            key = f"{n}@{size[0]}x{size[1]}"
            lines, status = [], "pass"
            errs = [f"{la}: {e}" for e in ra["errors"]] + [f"{lb}: {e}" for e in rb["errors"]]
            if ra["errors"]:
                status = "error-ref"  # the reference itself failed: a scenario bug
            elif rb["errors"]:
                status = "error"
            ca = {c["name"]: c for c in ra["checks"]}
            cb = {c["name"]: c for c in rb["checks"]}
            ndiff = nstyle = ncolour = 0
            failing = []  # the checkpoints that differ (or that the compared app never reached)
            what = {}  # checkpoint: what differs first, in a few words
            pybugs = []  # checkpoints where a known Python bug's region differed (and was ignored)
            for name in ca:
                if name not in cb:
                    lines.append(f"  [{name}] missing from {lb}")
                    ndiff += 1
                    failing.append(name)
                    what[name] = f"{lb} never reached it"
                    continue
                pyb = python_bug_applies(python_bug_for(sc, name, size), ca[name], cb[name])
                kind = "known Python bug"
                if not pyb:  # an intended difference, listed in the scenario
                    pyb = rule_applies(python_bug_for(sc, name, size, "intended"), ca[name], cb[name],
                                       "other_shows")
                    kind = "intended"
                if pyb:  # the known Python bug shows, and the other app is right there: leave it out
                    full = compare_check(ca[name], cb[name])
                    d = compare_check(*(mask_region(c, pyb["region"]) for c in (ca[name], cb[name])))
                    if full["text"] or full["style"] or full["colour"]:
                        if not (full["text"] == d["text"] and full["style"] == d["style"]
                                and full["colour"] == d["colour"]):
                            pybugs.append((kind, f"{name}: {pyb['region']} ignored ({pyb.get('note', '')})"))
                else:
                    d = compare_check(ca[name], cb[name])
                ndiff += bool(d["text"]) + bool(d["clipboard"])
                nstyle += d["style"]
                ncolour += d["colour"]
                if d["text"] or d["clipboard"] or d["style"] or (d["colour"] and not a.lenient_colours):
                    failing.append(name)
                    what[name] = describe_diff(d, la, lb)
                if not (d["text"] or d["style"] or d["colour"] or d["clipboard"]):
                    continue
                lines.append(f"  [{name}] {len(d['text'])} lines differ, {d['style']} cells differ in style, "
                             f"{d['colour']} in colour")
                for i, x, y in d["text"][:40]:
                    if i == "file":
                        lines.append(f"    {la}: {json.dumps(x)[:300]}")
                        lines.append(f"    {lb}: {json.dumps(y)[:300]}")
                        continue
                    lines.append(f"    {i:3d} {la[:6]:>6} |{x}")
                    lines.append(f"    {'':3} {lb[:6]:>6} |{y}")
                    lines.append(f"    {'':3} {'':>6} |{caret(x, y)}")
                if len(d["text"]) > 40:
                    lines.append(f"    … {len(d['text']) - 40} more lines")
                if d["clipboard"]:
                    lines.append(f"    clipboard {la}: {d['clipboard'][0]!r}")
                    lines.append(f"    clipboard {lb}: {d['clipboard'][1]!r}")
                if d["style"]:
                    lines += summarize_style_diffs(d["style_lines"], "style")
                if d["colour"]:
                    lines += summarize_style_diffs(d["colour_lines"], "colour")
            if status == "pass":
                if ndiff:
                    status = "diff"
                elif nstyle:
                    status = "style"
                elif ncolour and not a.lenient_colours:
                    status = "colour"
            if status == "pass" and pybugs:
                # passes once a known Python bug's region, or an intended difference, is left out
                status = "pybug" if any(k == "known Python bug" for k, _ in pybugs) else "intended"
                lines = [f"  {k}: {p}" for k, p in pybugs] + lines
            if sc.get("same_version") and len(set(versions.values())) > 1:
                status = "skip"  # what fits depends on the version's length
                lines = ["  skipped: the two apps report different versions"] + lines
            final, xf = status, None
            entry = xfail.get(key, xfail.get(n))
            if entry is not None:
                if isinstance(entry, str):  # a scenario-level reason: any failure is expected
                    entry = {"reason": entry}
                xf = entry.get("reason", "")
                if status in ("pass", "skip", "pybug", "intended"):
                    final = "xpass" if status == "pass" else status
                else:
                    new = sorted(set(failing) - set(entry.get("checks", failing)))
                    was = entry.get("status")
                    if new or (was and was != status):
                        final = "changed"
                        lines.insert(0, f"  changed since expected_failures.yaml: status {was} → {status}"
                                        + (f"; newly failing checkpoints {new}" if new else ""))
                    else:
                        final = "xfail"
            counts[final] = counts.get(final, 0) + 1
            summary["scenarios"][key] = {"status": final, "raw_status": status, "failing": failing, "what": what,
                                         "text_diffs": ndiff, "style_cells": nstyle, "colour_cells": ncolour,
                                         "errors": errs, "xfail_reason": xf,
                                         "description": sc["description"].strip()}
            rows.append((key, final, status, sc, errs, lines, xf))

    rep = os.path.join(out, "report.txt")
    with open(rep, "w") as fh:
        fh.write(f"pqx pty parity: {la} vs {lb}\n")
        for k, v in versions.items():
            fh.write(f"  {k}: {APPS[k]} ({v})\n")
        if len(set(versions.values())) > 1:
            fh.write("  note: the versions differ; the title bar's version is normalized but its length "
                     "changes what fits (build Go with VERSION=<python's>)\n")
        fh.write(f"  {elapsed:.0f} s;  " + ",  ".join(f"{k} {v}" for k, v in sorted(counts.items())) + "\n\n")
        for key, final, status, sc, errs, lines, xf in rows:
            fh.write(f"{final.upper():9} {key}" + (f"  ({status}; expected: {xf})" if xf else "") + "\n")
        for key, final, status, sc, errs, lines, xf in rows:
            if status == "pass" and not lines:
                continue
            fh.write(f"\n{'=' * 100}\n{final.upper()} {key}\n  {sc['description'].strip().splitlines()[0]}\n")
            for e in errs:
                fh.write(f"  ERROR {e}\n")
            fh.write("\n".join(lines) + "\n")
    summary["counts"] = counts
    with open(os.path.join(out, "summary.json"), "w") as fh:
        json.dump(summary, fh, indent=1, ensure_ascii=False)
    write_screens_json(os.path.join(out, "screens.json"), results)
    if a.write_xfail:
        write_xfail(a.xfail, summary, xfail)
    bad = sum(v for k, v in counts.items() if k not in ("pass", "xfail", "skip", "pybug", "intended"))
    print(f"\n{la} vs {lb}: " + ", ".join(f"{k} {v}" for k, v in sorted(counts.items()))
          + f"\nreport: {rep}\nsummary: {os.path.join(out, 'summary.json')}", file=sys.stderr)
    return 1 if bad else 0


def write_xfail(path, summary, old):
    """One entry per scenario and size that doesn't pass: its status, its failing
    checkpoints and a reason (kept from the old file when there is one)."""
    entries = {}
    for key, s in summary["scenarios"].items():
        if s["raw_status"] in ("pass", "skip", "pybug", "intended"):
            continue
        prev = old.get(key, old.get(key.split("@")[0]))
        why = prev.get("reason") if isinstance(prev, dict) else prev
        if not why:  # the first error past startup, or what differs in the first checkpoints
            mine = [e for e in s["errors"] if e.startswith(summary["b"] + ":")]
            errs = [e for e in mine if ": startup:" not in e] or mine
            diffs = [f"{c}: {s['what'][c]}" for c in s["failing"][:2] if c in s.get("what", {})]
            why = "; ".join(([errs[0][:160]] if errs else []) + diffs)[:400] or s["raw_status"]
        entries[key] = {"status": s["raw_status"], "checks": s["failing"], "reason": why}
    with open(path, "w") as fh:
        fh.write("# Known gaps of Go pqx against Python pqx, per scenario and size: the status, the\n"
                 "# checkpoints that fail, and why. A run fails on anything not listed here: a new\n"
                 "# failing checkpoint, a changed status, or a listed entry that now passes (XPASS).\n"
                 "# Regenerate with `run.py --write-xfail`; reasons already here are kept.\n")
        yaml.safe_dump({"xfail": dict(sorted(entries.items()))}, fh, allow_unicode=True, width=200,
                       sort_keys=False)

if __name__ == "__main__":
    sys.exit(main())
