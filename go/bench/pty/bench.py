"""Benchmarks in a pty against the performance targets of docs/design/go-port.md.

    bench.py [--apps python,go] [--runs 3] [--threads 8] [--dump] FILE[:COLUMN] ...
    bench.py --esc SSSource.parquet ...           # also time Esc on a slow count
    bench.py --frame wide300.parquet ...          # also time Right on a wide screen
    bench.py --gen DIR                            # write wide300 and rg2000 into DIR
    bench.py --targets DIR                        # the go-port.md files and targets

For each FILE and app, `--runs` times (after one unrecorded warm-up): start the app
in a 200x50 pty and time until row 0 is on the screen (first screen), then PgDn, `g`
to the middle row and Ctrl+End, each until the expected row is on the screen. A row
is recognised by its label and its COLUMN value, read from the file with pyarrow
(default: the first string or integer column). `--esc` applies a filter whose count
takes seconds, waits for the count to show, presses Esc and times until the screen
says it was cancelled. `--frame` times Right arrow presses on a wide file, from the key
until the app has written the frame, on the raw pty bytes.

PgDn, `g` and Ctrl+End are timed until the expected row is on the emulated screen, so
they include pyte's processing of the redraw (a few ms at 200x50); the first screen also
includes process start.

Prints each run as it goes, then a table of medians against the targets (and the Go
prototype's numbers, where a regression of more than 20% is a bug), and writes all
results as JSON (--json). Needs pyte and pyarrow. The apps are PQX_PY and PQX_GO.
"""
from __future__ import annotations

import argparse
import json
import os
import re
import statistics
import sys
import time

import pyarrow.parquet as pq

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, HERE)
from ptydrive import Session  # noqa: E402

APPS = {
    "python": os.environ.get("PQX_PY", "/root/parquet-explorer/.venv/bin/pqx"),
    "go": os.environ.get("PQX_GO", os.path.join(HERE, "..", "..", "bin", "pqx")),
}
W, H = 200, 50
DELIVERY = "/sdf/data/rubin/user/mjuric/shutter-timing-ssp/rerun/2026-10-06/run/delivery"
#: the filter esc.sh uses: its count on SSSource takes about 7 s
SLOW_WHERE = "levenshtein(repeat(obsid,4), repeat(trksub,4)) > 5"

#: go-port.md "Performance targets", in seconds: (metric, file stem or None for all, limit)
TARGETS = [
    ("first", "SSSource", 0.25),
    ("first", "rg2000", 0.4),
    ("pgdn", None, 0.050),
    ("g_middle", None, 0.150),
    ("ctrl_end", None, 0.150),
    ("esc", None, 0.100),
    ("frame", None, 0.005),
]
#: the Go prototype's slowest run per metric (native-port.md, ms); +20% is a regression
PROTOTYPE = {
    "SSSource": {"first": 174, "pgdn": 44, "g_middle": 66, "ctrl_end": 65, "esc": 66},
    "mpc_orbits": {"first": 213, "pgdn": 41, "g_middle": 140, "ctrl_end": 57},
    "wide300": {"first": 182, "pgdn": 46, "g_middle": 69, "ctrl_end": 66},
    "rg2000": {"first": 319, "pgdn": 48, "g_middle": 41, "ctrl_end": 41},
}


# ------------------------------------------------------------------ markers
def locate(pf, row):
    start = 0
    for i in range(pf.metadata.num_row_groups):
        n = pf.metadata.row_group(i).num_rows
        if row < start + n:
            return i, row - start
        start += n
    raise IndexError(row)


def pick_column(pf):
    for f in pf.schema_arrow:
        t = str(f.type)
        if t in ("string", "large_string") or t.startswith(("int", "uint")):
            return f.name
    return pf.schema_arrow.names[0]


def marker_for(pf, col, row):
    """A regex for the screen line of `row`: its label (with or without commas), then the value."""
    rg, off = locate(pf, row)
    v = pf.read_row_group(rg, columns=[col]).column(0)[off].as_py()
    if isinstance(v, float):
        m = re.match(r"(-?\d+\.\d{0,3})", f"{v:.12g}")
        val = re.escape(m.group(1) if m else f"{v:.12g}"[:5])
    elif isinstance(v, int):
        val = rf"(?<![\d.,]){v}(?![\d.,])"
    else:
        val = re.escape(str(v)[:10])
    lab = f"{row:,}".replace(",", ",?")
    return rf"(^|[^\d,]){lab}\s.*{val}"


ROW_LABEL = re.compile(r"^(\d[\d,]*)\s")


def last_row_label(lines):
    """The largest row label at the start of a grid line on the screen."""
    best = -1
    for ln in lines:
        m = ROW_LABEL.match(ln.lstrip("│ "))
        if m:
            best = max(best, int(m.group(1).replace(",", "")))
    return best


# ------------------------------------------------------------------ one run
def run_file(cmd, path, col, threads, dump, timeout=60):
    pf = pq.ParquetFile(path)
    n = pf.metadata.num_rows
    mid = n // 2 + 12345 if n > 30000 else n // 2
    argv = [cmd, path] + (["--threads", str(threads)] if threads else [])
    out = {}
    s = Session(argv, W, H)
    try:
        ok = s.wait(marker_for(pf, col, 0), timeout)
        out["first"] = round(time.perf_counter() - s.t0, 4) if ok else None
        s.settle(0.5, 10)
        if dump:
            print(s.text(), file=sys.stderr)
        last = last_row_label(s.lines())
        steps = [("pgdn", ["pgdn"], None, marker_for(pf, col, last + 1) if 0 <= last < n - 1 else None),
                 ("g_middle", ["g"], str(mid), marker_for(pf, col, mid)),
                 ("ctrl_end", ["ctrl+end"], None, marker_for(pf, col, n - 1))]
        for name, keys, text, rx in steps:
            if rx is None:
                out[name] = None
                continue
            if text is not None:  # the go-to dialog: open it and type, then time Enter
                s.keys(*keys)
                if not s.wait(r"(?i)go to row", 10):
                    out[name] = None
                    continue
                s.type(text)
                s.drain(0.2)
                keys = ["enter"]
            start = time.perf_counter()
            s.keys(*keys)
            ok = s.wait(rx, timeout)
            out[name] = round(time.perf_counter() - start, 4) if ok else None
            if dump or not ok:
                print(f"--- {name} {'ok' if ok else 'TIMEOUT'}\n{s.text()}", file=sys.stderr)
            s.settle(0.5, 10)
    finally:
        s.close()
    return out


def run_esc(cmd, path, threads, dump, where=SLOW_WHERE):
    """Esc on a slow count: seconds from Esc until the screen says it was cancelled."""
    argv = [cmd, path] + (["--threads", str(threads)] if threads else [])
    s = Session(argv, W, H)
    try:
        if not s.wait(r"\d", 60):
            return None
        s.settle(1.0, 30)
        s.keys("/")
        s.drain(0.4)
        s.type(where)
        s.keys("enter")
        if not s.wait(r"(?i)counting", 20):
            print(f"--- esc: no 'counting' shown\n{s.text()}", file=sys.stderr)
            return None
        s.drain(1.0)
        start = time.perf_counter()
        s.keys("esc")
        ok = s.wait(r"(?i)cancelled", 20)
        t = round(time.perf_counter() - start, 4) if ok else None
        if dump or not ok:
            print(f"--- esc {'ok' if ok else 'TIMEOUT'}\n{s.text()}", file=sys.stderr)
        return t
    finally:
        s.close()


def run_frame(cmd, path, threads, presses=20, quiet=0.03):
    """Right arrow on a wide screen: median seconds from the key until the app has written
    the frame. Timed on the raw pty bytes (no screen emulation in the loop): the frame
    ends at the end of a synchronized update (CSI ? 2026 l) or, without one, at the last
    byte before `quiet` s of silence (that silence is not counted)."""
    import select as _select
    argv = [cmd, path] + (["--threads", str(threads)] if threads else [])
    s = Session(argv, W, H)
    times = []
    try:
        if not s.wait(r"\d", 60):
            return None
        s.settle(1.0, 30)
        for _ in range(presses):
            s.drain(0.05)
            start = time.perf_counter()
            s.keys("right")
            last, buf = None, bytearray()
            while True:
                r, _, _ = _select.select([s.fd], [], [], quiet if last else 5.0)
                if not r:
                    break
                data = os.read(s.fd, 1 << 16)
                if not data:
                    break
                last = time.perf_counter()
                buf += data
                if buf.endswith(b"\x1b[?2026l"):
                    break
            if last:
                times.append(last - start)
            s.raw += buf
            s.stream.feed(s.filter.feed(bytes(buf)))
            s.settle(0.2, 5)
    finally:
        s.close()
    return round(statistics.median(times), 4) if times else None


# ------------------------------------------------------------------ generated files
def generate(d):
    """wide300 (200k x 300, 4 row groups) and rg2000 (2M x 20, 2,000 row groups), as measured
    for the prototype. About 600 MB and 370 MB: delete them when done."""
    import numpy as np
    import pyarrow as pa
    os.makedirs(d, exist_ok=True)
    rng = np.random.default_rng(1)
    p = os.path.join(d, "wide300.parquet")
    if not os.path.exists(p):
        n = 200_000
        data = {"id": np.arange(n, dtype=np.int64)}
        for i in range(299):
            data[f"c{i:03d}"] = rng.normal(scale=10 ** (i % 7 - 2), size=n)
        pq.write_table(pa.table(data), p, row_group_size=50_000)
        print(f"wrote {p}", file=sys.stderr)
    p = os.path.join(d, "rg2000.parquet")
    if not os.path.exists(p):
        n = 2_000_000
        data = {"id": np.arange(n, dtype=np.int64)}
        for i in range(19):
            data[f"c{i:02d}"] = rng.normal(size=n)
        pq.write_table(pa.table(data), p, row_group_size=1_000)
        print(f"wrote {p}", file=sys.stderr)


# ------------------------------------------------------------------ report
def stem(path):
    return os.path.basename(path).rsplit(".", 1)[0]


def report(results):
    """Medians per file, metric and app, against the targets and the prototype."""
    lines = [f"{'file':12} {'metric':9} {'app':7} {'median ms':>10} {'runs ms':28} {'target':>8} "
             f"{'proto+20%':>10}  verdict"]
    for key in sorted(results):
        f, app = key.rsplit(" ", 1)
        for metric, runs in results[key].items():
            vals = [r for r in runs if r is not None]
            med = statistics.median(vals) if vals else None
            target = next((t for m, fs, t in TARGETS if m == metric and (fs is None or fs == f)), None)
            proto = PROTOTYPE.get(f, {}).get(metric) if app == "go" else None
            verdict = []
            if med is None:
                verdict.append("NO DATA")
            else:
                if target is not None:
                    verdict.append("meets target" if med <= target else "MISSES target")
                if proto is not None and med * 1000 > proto * 1.2:
                    verdict.append("REGRESSION vs prototype")
            runs_s = ", ".join("—" if r is None else f"{r * 1000:.0f}" for r in runs)
            med_s = "—" if med is None else f"{med * 1000:.0f}"
            lines.append(f"{f:12} {metric:9} {app:7} {med_s:>10} {runs_s:28} "
                         f"{'' if target is None else f'{target * 1000:.0f}':>8} "
                         f"{'' if proto is None else f'{proto * 1.2:.0f}':>10}  {'; '.join(verdict)}")
    return "\n".join(lines)


def main(argv=None):
    p = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    p.add_argument("files", nargs="*", help="FILE[:COLUMN]")
    p.add_argument("--apps", default="python,go")
    p.add_argument("--runs", type=int, default=3)
    p.add_argument("--warmup", type=int, default=1, help="unrecorded runs per file and app first")
    p.add_argument("--threads", type=int, default=8, help="--threads for the apps (0: their default)")
    p.add_argument("--esc", action="append", default=[], help="time Esc on a slow count on this file")
    p.add_argument("--esc-where", default=SLOW_WHERE, help="the slow filter (default: %(default)s, for SSSource)")
    p.add_argument("--frame", action="append", default=[], help="time Right arrow on this (wide) file")
    p.add_argument("--gen", metavar="DIR", help="write wide300.parquet and rg2000.parquet into DIR")
    p.add_argument("--targets", metavar="DIR", help="the go-port.md set: SSSource and mpc_orbits from the "
                   "delivery, wide300 and rg2000 from DIR (written there if missing), Esc on SSSource, "
                   "frame on wide300")
    p.add_argument("--dump", action="store_true", help="print the screens")
    p.add_argument("--json", help="write all results here")
    a = p.parse_args(argv)

    if a.gen:
        generate(a.gen)
    files, esc, frame = list(a.files), list(a.esc), list(a.frame)
    if a.targets:
        generate(a.targets)
        files += [f"{DELIVERY}/SSSource.parquet", f"{DELIVERY}/mpc_orbits.parquet",
                  os.path.join(a.targets, "wide300.parquet"), os.path.join(a.targets, "rg2000.parquet")]
        esc += [f"{DELIVERY}/SSSource.parquet"]
        frame += [os.path.join(a.targets, "wide300.parquet")]
    if not (files or esc or frame):
        if a.gen:
            return 0
        p.error("nothing to run")
    apps = a.apps.split(",")
    results = {}
    for spec in files:
        path, _, col = spec.partition(":")
        col = col or pick_column(pq.ParquetFile(path))
        for app in apps:
            key = f"{stem(path)} {app}"
            for r in range(a.warmup + a.runs):
                res = run_file(APPS[app], path, col, a.threads, a.dump)
                if r < a.warmup:
                    continue
                print(key, res, flush=True)
                for k, v in res.items():
                    results.setdefault(key, {}).setdefault(k, []).append(v)
    for path in esc:
        for app in apps:
            key = f"{stem(path)} {app}"
            for _ in range(a.runs):
                t = run_esc(APPS[app], path, a.threads, a.dump, a.esc_where)
                print(key, "esc", t, flush=True)
                results.setdefault(key, {}).setdefault("esc", []).append(t)
    for path in frame:
        for app in apps:
            key = f"{stem(path)} {app}"
            for _ in range(a.runs):
                t = run_frame(APPS[app], path, a.threads)
                print(key, "frame", t, flush=True)
                results.setdefault(key, {}).setdefault("frame", []).append(t)
    print()
    print(report(results))
    if a.json:
        with open(a.json, "w") as fh:
            json.dump(results, fh, indent=1)
    return 0


if __name__ == "__main__":
    sys.exit(main())
