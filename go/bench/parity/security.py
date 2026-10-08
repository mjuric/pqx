"""Pty security test: no escape sequence from a file may reach the terminal.

    security.py [--app python|go|both] [--keep] [--size 200x50]

A port of tests/test_security.py::test_pty_terminal_never_receives_file_escapes,
runnable against either app (PQX_PY, PQX_GO as for run.py). It writes a file whose
strings, column names, units, descriptions and key-value metadata hold ESC/CSI/OSC/C1
and bidi sequences (and whose own name does), opens it in a pty, goes through the grid,
the detail pane, copy, `=`, Schema, Stats, Plot, Metadata, the column picker, the export
dialog and an error message that quotes a file value, and then checks every byte the
app wrote:

- none of the file's sequences appears raw (OSC title/hyperlink/clipboard, CSI, C1);
- no C1 control at all (pqx itself never writes one);
- every OSC 52 clipboard write (pqx's own, from `y`) decodes to text without controls;
- no OSC other than the app's own kinds (52 clipboard, 22 pointer shape, 10/11 colour
  queries, 0/2 window title, whose text must hold no control characters) is written;
- the SQL the file tried to run did not run (no files appeared);
- the hostile text was drawn, as visible stand-ins (␛).

Exit status 0 when every check passes for every app run.
"""
from __future__ import annotations

import argparse
import base64
import os
import re
import shutil
import sys
import tempfile

import pyarrow as pa
import pyarrow.parquet as pq

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, os.path.join(os.path.dirname(HERE), "pty"))
from ptydrive import Session  # noqa: E402

APPS = {
    "python": os.environ.get("PQX_PY", "/root/parquet-explorer/.venv/bin/pqx"),
    "go": os.environ.get("PQX_GO", os.path.join(os.path.dirname(os.path.dirname(HERE)), "bin", "pqx")),
}

ESC = "\x1b"
OSC_TITLE = ESC + "]0;PWNED-TITLE\x07"
OSC52 = ESC + "]52;c;" + base64.b64encode(b"echo PWNED\n").decode() + ESC + "\\"
OSC8 = ESC + "]8;;https://evil.example/" + ESC + "\\click" + ESC + "]8;;" + ESC + "\\"
C1_CSI = "\x9b"
MARKUP = "[bold red]MARKUP[/] [@click=app.quit]clickme[/] [link=https://evil.example]lnk[/link]"
BIDI = "abc‮dcba⁦x⁩​﻿"


def controls(s: str) -> list[str]:
    """The C0 (other than tab and newline), DEL and C1 characters in `s`."""
    return [c for c in s if (ord(c) < 0x20 and c not in "\t\n") or 0x7F <= ord(c) < 0xA0]


def evil_values(pwn: str) -> list[str]:
    return [
        "it's \"quoted\" \\ back'slash",
        f"x' ); COPY (SELECT 1) TO '{pwn}'; --",
        OSC_TITLE + "TITLE-VAL",
        OSC52 + "CLIP-VAL",
        OSC8,
        MARKUP,
        "[/]",
        ESC + "[2J" + ESC + "[31mRED",
        C1_CSI + "31mC1" + "\x9d0;C1-TITLE\x07",
        "tab\there\nnew line",
        BIDI,
    ]


def evil_completion_name(pwn: str) -> str:
    return (f"random() > -1)); COPY (SELECT 'echo PWNED') TO '{pwn}' (HEADER false, QUOTE ''); "
            "SELECT * FROM (SELECT 1 AS x WHERE (1")


def make_evil(d: str, name: str):
    """The test's file (as test_security.make_evil, plus a bidi value). Returns (path, pwn files)."""
    pwn_completion = os.path.join(d, "pwn_completion.txt")
    pwn_value = os.path.join(d, "pwn_value.txt")
    vals = evil_values(pwn_value)
    n = len(vals)
    cols = {
        "a": pa.array(range(n)),
        "s": pa.array(vals),
        "select": pa.array(vals),
        "[bold]mk[/] [@click=app.quit]x[/]": pa.array(vals),
        "esc" + OSC_TITLE + "name": pa.array(range(n)),
        "c1" + C1_CSI + "2Jname": pa.array(range(n)),
        evil_completion_name(pwn_completion): pa.array(range(n)),
    }
    fields = []
    for k, v in cols.items():
        md = {b"description": ("desc " + OSC_TITLE + C1_CSI + "5m [bold]M[/] [@click=app.quit]q[/]").encode(),
              b"unit": ("u" + ESC + "[5m").encode()}
        fields.append(pa.field(k, v.type, metadata=md))
    schema = pa.schema(fields, metadata={
        ("k" + OSC_TITLE).encode(): ("v " + OSC52 + " [@click=app.quit]x[/]").encode(),
        b"json": b'{"a": "\\u001b]0;JSONTITLE\\u0007", "b": "\\u009b31mJSONC1", "c": "\\u007f"}',
    })
    path = os.path.join(d, name)
    pq.write_table(pa.table(list(cols.values()), schema=schema), path)
    return path, (pwn_completion, pwn_value)


#: (keys, what they reach). Every step is followed by a settle; `text:` types literally.
SCRIPT = [
    ("down down", "grid: move onto hostile rows"),
    ("right", "grid: the string column"),
    ("d", "detail pane"),
    ("y", "copy (OSC 52) from the grid"),
    ("down down down down", "rows with OSC 52, OSC 8, markup"),
    ("y", "copy a value holding OSC 52"),
    ("tab down down", "into the detail pane"),
    ("y", "copy from the detail pane"),
    ("esc", "close the pane"),
    ("=", "filter to a hostile value"),
    ("x", "clear"),
    ("down down down down down down down", "the ESC[2J and C1 rows"),
    ("end", "last columns: hostile names"),
    ("home", "back"),
    ("2", "Schema: hostile names, units, descriptions"),
    ("down down down down down", "schema rows"),
    ("3", "Stats on a hostile column"),
    ("4", "Plot"),
    ("5", "Metadata: hostile key-value metadata"),
    ("pgdn", "more metadata"),
    ("1", "back to the grid"),
    ("c", "column picker: hostile names"),
    ("esc", "close it"),
    ("e", "export dialog"),
    ("esc", "close it"),
    ("/", "filter box"),
    ("text:sel", "completion of hostile names"),
    ("ctrl+x", "clear the box"),
    ("text:select cast(s as integer) as v from t where a = 7", "an error that quotes a file value"),
    ("enter", "run it"),
    ("esc", "leave the box"),
    ("x", "clear"),
    ("?", "help"),
    ("esc", "close help"),
]

#: sequences from the file that must never appear raw
NEEDLES = [b"\x1b]0;PWNED", b"\x1b]2;FNAME", b"\x1b]8;;https://evil", b"\x1b[2J\x1b[31mRED", b"\x1b[5m",
           b"PWNED-TITLE\x07", b"FNAME-TITLE\x07", b"C1-TITLE\x07", b"JSONTITLE\x07",
           OSC52.encode()[:20], "‮".encode(), "⁦".encode()]
#: the kinds of OSC an app may write itself
OSC_OK = {"52", "22", "10", "11", "12", "0", "1", "2", "133", "9", "777"}


def check_bytes(b: bytes, d: str, pwns) -> list[str]:
    fails = []
    for needle in NEEDLES:
        if needle in b:
            i = b.index(needle)
            fails.append(f"file text reached the terminal raw: {needle!r} … {b[max(0, i - 40):i + 60]!r}")
    m = re.search(rb"\xc2[\x80-\x9f]", b)
    if m:
        fails.append(f"a C1 control was written: {b[max(0, m.start() - 40):m.end() + 40]!r}")
    for payload in re.findall(rb"\x1b\]52;[a-z]*;([A-Za-z0-9+/=]*)", b):
        try:
            text = base64.b64decode(payload).decode("utf-8", "replace")
        except ValueError:
            fails.append(f"undecodable OSC 52 payload {payload[:60]!r}")
            continue
        if controls(text):
            fails.append(f"OSC 52 copy carries control characters: {text[:80]!r}")
    for m in re.finditer(rb"\x1b\](\d*)[;\x07]([^\x07\x1b]*)", b):
        code = m.group(1).decode()
        if code not in OSC_OK:
            fails.append(f"unexpected OSC {code}: {m.group(0)[:80]!r}")
        if code in ("0", "1", "2") and re.search(rb"[\x00-\x1f\x7f]|\xc2[\x80-\x9f]", m.group(2)):
            fails.append(f"window title carries control characters: {m.group(0)[:80]!r}")
    for p in pwns:
        if os.path.exists(p):
            fails.append(f"SQL from the file ran: {p} exists")
    if "␛".encode() not in b:
        fails.append("the hostile text was never drawn (no ␛ stand-in)")
    return fails


def run_app(app: str, cmd: str, size, keep: bool) -> tuple[list[str], str]:
    d = tempfile.mkdtemp(prefix=f"pqx-sec-{app}-", dir=os.environ.get("PQX_PARITY_SCRATCH"))
    try:
        path, pwns = make_evil(d, "evil" + ESC + "]2;FNAME-TITLE\x07" + C1_CSI + ".parquet")
        env = dict(os.environ, XDG_CONFIG_HOME=os.path.join(d, "config"))
        s = Session([cmd, path], size[0], size[1], env=env, cwd=d)
        log = []
        try:
            if not s.wait(r"\S", 30):
                return ["the app drew nothing"], d
            s.settle(0.8, 20)
            for keys, what in SCRIPT:
                if keys.startswith("text:"):
                    s.type(keys[5:])
                else:
                    s.keys(*keys.split(), gap=0.05)
                s.drain(0.3)
                s.settle(0.4, 8)
                log.append(f"{keys:58} {what}")
                if s.exited is not None:
                    return [f"the app exited (status {s.exited}) after {keys!r} ({what})"], d
            b = bytes(s.raw)
        finally:
            s.close()
        if keep:
            with open(os.path.join(d, "terminal.raw"), "wb") as fh:
                fh.write(b)
        return check_bytes(b, d, pwns), d
    finally:
        if not keep:
            shutil.rmtree(d, ignore_errors=True)


def main(argv=None):
    p = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    p.add_argument("--app", default="both", choices=["python", "go", "both"])
    p.add_argument("--size", default="200x50")
    p.add_argument("--keep", action="store_true", help="keep the temporary directory and the raw output")
    a = p.parse_args(argv)
    size = tuple(int(v) for v in a.size.split("x"))
    bad = 0
    for app in (["python", "go"] if a.app == "both" else [a.app]):
        fails, d = run_app(app, APPS[app], size, a.keep)
        print(f"{'PASS' if not fails else 'FAIL'} {app} ({APPS[app]})" + (f"  [kept {d}]" if a.keep else ""))
        for f in fails:
            print(f"   - {f}")
        bad += bool(fails)
    return 1 if bad else 0


if __name__ == "__main__":
    sys.exit(main())
