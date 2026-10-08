"""Compare Python pqx and the Go prototype in a pty: startup, PgDn, g to a middle row, Ctrl+End.

    PQX_PY=/path/to/python/pqx PQX_GO=go/bin/pqx PTY_PYTHON=python-with-pyte \
        python bench.py [--runs N] [--dump] FILE[:COLUMN] ...

Needs pyarrow (for the markers) here, and pyte in PTY_PYTHON. COLUMN is a column whose
value at the target row both apps print recognisably (a string or integer column is
safest; floats are matched on their first digits). Prints the seconds of each step
per run, then all results as JSON.
"""
import json
import os
import re
import subprocess
import sys

import pyarrow.parquet as pq

HERE = os.path.dirname(os.path.abspath(__file__))
APPS = {
    "python": [os.environ.get("PQX_PY", "pqx")],
    "go": [os.environ.get("PQX_GO", os.path.join(HERE, "..", "..", "bin", "pqx"))],
}
PY = os.environ.get("PTY_PYTHON", sys.executable)


def locate(pf, row):
    md = pf.metadata
    start = 0
    for i in range(md.num_row_groups):
        n = md.row_group(i).num_rows
        if row < start + n:
            return i, None, row - start
        start += n
    raise IndexError(row)


def marker_for(pf, col, row):
    rg, _, off = locate(pf, row)
    v = pf.read_row_group(rg, columns=[col]).column(0)[off].as_py()
    if isinstance(v, float):
        s = f"{v:.12g}"
        m = re.match(r"(-?\d+\.\d{0,3})", s)
        val = re.escape(m.group(1) if m else s[:5])
    elif isinstance(v, int):
        val = rf"(?<![\d.,]){v}(?![\d.,])"
    else:
        val = re.escape(str(v)[:10])
    lab = f"{row:,}".replace(",", ",?")
    return rf"(^|[^\d,]){lab}\s.*{val}"


def main():
    args = sys.argv[1:]
    runs = 3
    if "--runs" in args:
        runs = int(args.pop(args.index("--runs") + 1))
        args.remove("--runs")
    dump = "--dump" in args
    if dump:
        args.remove("--dump")
    results = {}
    for spec in args:
        path, _, col = spec.partition(":")
        pf = pq.ParquetFile(path)
        col = col or pf.schema_arrow.names[1]
        n = pf.metadata.num_rows
        mid = n // 2 + 12345
        steps = [
            "=>" + marker_for(pf, col, 0),
            r"\x1b[6~=>" + marker_for(pf, col, 60),
            "g=>(?i)go to row",
            f"{mid}\\r=>" + marker_for(pf, col, mid),
            r"\x1b[1;5F=>" + marker_for(pf, col, n - 1),
        ]
        for app, cmd in APPS.items():
            for r in range(runs):
                argv = [PY, os.path.join(HERE, "ptytime.py"), "--timeout", "60"] + (["--dump"] if dump else []) + ["--", *cmd, path]
                for st in steps:
                    argv += [":::", st]
                p = subprocess.run(argv, capture_output=True, text=True)
                if dump or "TIMEOUT" in p.stderr:
                    sys.stderr.write(p.stderr)
                res = json.loads(p.stdout.strip().splitlines()[-1])
                key = f"{path.rsplit('/', 1)[-1]} {app}"
                results.setdefault(key, []).append([res[f"step{i}"] for i in range(len(steps)) if i != 2])
                print(key, res, flush=True)
    print(json.dumps(results))


main()
