"""Write cli.json: Python pqx's --help, --version and command-line errors, the
reference for go/internal/opts and go/cmd/pqx.

Run from the repository root:
    PYTHONPATH=. python go/internal/opts/testdata/make_golden.py
The version is replaced by {VERSION}. No file is opened: every case that
parses names a file that doesn't exist.
"""
import contextlib
import io
import json
import os
import sys

from pqx import __version__, cli

WIDTHS = [12, 16, 20, 25, 30, 40, 50, 60, 72, 80, 100, 120, 200]
NOFILE = "/nonexistent/x.parquet"
ESC = "\x1b"
CASES = [
    [], ["a", "b"], ["--bogus", NOFILE], [NOFILE, "--bogus=1", "y"],
    ["--accent", "red", NOFILE], ["--acc", "red", NOFILE], ["--accent", "cyan", NOFILE],
    ["--dim", "bold", NOFILE], ["--dim", "bright-black", NOFILE],
    ["--th", "3", NOFILE], ["--th=3", NOFILE], ["--threads", "x", NOFILE], ["--threads", "1.5", NOFILE],
    ["--threads", " 7 ", NOFILE], ["--threads", "1_000", NOFILE], ["--threads=-1", NOFILE],
    ["--sample", "--no-sample", NOFILE], ["--no-sample", "--sample", NOFILE], ["--sample", "--sample", NOFILE],
    ["--s", NOFILE], ["--no", NOFILE], ["--sample=1", NOFILE],
    ["-w"], ["-w", "--bogus", NOFILE], ["-w", "-1", NOFILE], ["-w", "-x > 0", NOFILE], ["-wfoo", NOFILE],
    ["-w=foo", NOFILE], ["--where=", NOFILE], ["--wh", "x", NOFILE], [NOFILE, "-w", "x"],
    ["-h=1"], ["--help=1"], ["-he", NOFILE], ["--version=1"], ["--vers"], [NOFILE, "--version"],
    ["--accent", "red", "--version"], ["--version", "--accent", "red"], ["--help", "--bogus"],
    ["--", "-x"], ["--", "--version"], ["-"], ["-1"], ["-x y"],
    ["--format", "x", NOFILE], ["--format", "=1", NOFILE], ["--format", "a=", NOFILE],
    ["--format", "a = 3", NOFILE], ["--format=a=1", NOFILE], ["--format", "a" + ESC + "]0;T\x07=1", NOFILE],
    ["--format", "a'b", NOFILE], ["--format", "a\"b'", NOFILE], ["--format", "é​", NOFILE],
    ["--format", "a=.2q", NOFILE], ["--format", "a=99", NOFILE], ["--format", "a=" + ESC + "[31m", NOFILE],
    ["--theme", "nord", NOFILE], ["--border", "white", NOFILE], ["--accent"],
    ["nope" + ESC + "]0;T\x07.parquet"],
    # from the review of the Go port
    [NOFILE, "-w", "--th"], ["--format", "--=x=faint", "--version", NOFILE], ["-hw"], ["-hhw"], ["-hw", "--"],
    ["-hwfoo", NOFILE], ["-he", NOFILE], ["-hh=1"], [NOFILE, "--"], ["--", NOFILE, "--"], [NOFILE, "-w", "x", "--"],
    ["--threads", "\u0663", NOFILE], ["--threads", "\U0001d7d9", NOFILE], ["--threads", "99999999999999999999", NOFILE],
    ["--threads", "\u00a07\u2003", NOFILE], ["-="], ["--="], ["-x="], ["-w=", NOFILE], ["-w", "--", NOFILE],
    [NOFILE, "--threads="], ["-٣", NOFILE], ["-1.5"], ["-.5", NOFILE],
]


def run(args, columns=80):
    out, err = io.StringIO(), io.StringIO()
    os.environ["COLUMNS"] = str(columns)
    code = 0
    with contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
        try:
            code = cli.main(args)
        except SystemExit as e:
            code = e.code or 0
    norm = lambda s: s.replace(__version__, "{VERSION}")
    return {"args": args, "code": code, "stdout": norm(out.getvalue()), "stderr": norm(err.getvalue())}


golden = {
    "help": {str(w): run(["--help"], w)["stdout"] for w in WIDTHS},
    "version": run(["--version"])["stdout"],
    "cases": [run(a) for a in CASES],
}
here = os.path.dirname(os.path.abspath(__file__))
with open(os.path.join(here, "cli.json"), "w") as f:
    json.dump(golden, f, indent=1, ensure_ascii=False)
    f.write("\n")
print(f"wrote {len(golden['help'])} help texts and {len(golden['cases'])} cases", file=sys.stderr)
