"""Pty security test: no escape sequence from a file may reach the terminal.

    security.py [--app python|go|both] [--keep] [--size 200x50]
    security.py --selftest

A port of tests/test_security.py::test_pty_terminal_never_receives_file_escapes,
runnable against either app (PQX_PY, PQX_GO as for run.py). It writes a file whose
strings, column names, units, descriptions, key-value metadata and file name hold
ESC/CSI/OSC/C1/bidi/zero-width sequences, binary values and invalid UTF-8, and drives
the app through SCRIPT: `=` on the SQL-injection value, the detail pane, copy, every
tab, the column picker, the export dialog, the completion of a column name that is SQL,
an error quoting a file value, the help. Each step that opens something must show its
marker (or the test fails: it didn't reach that screen). Then check_bytes() parses
every byte the app wrote; see README.md for the rules. Exit status 0 when every check
passes for every app run.
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
        # every byte value, and strings that aren't valid UTF-8 (built without validation)
        "bin": pa.array([bytes(range(i * 23 % 256, 256))[:40] for i in range(n)], type=pa.binary()),
        "badutf8": pa.array([b"\x9b31mRAW-C1 \xff\xfe \xc2 \x1b[5m" + bytes([0x80 + i]) for i in range(n)],
                            type=pa.binary()).view(pa.string()),
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


#: (keys, marker, what they reach). After the keys the screen must show the marker (a
#: regex, from what Python pqx shows) within MARKER_WAIT seconds, or the test fails: it
#: proves the hostile text was really drawn there. None: no marker, just settle.
#: Row 1 of column `s` holds the SQL injection value; the column whose name starts with
#: `random()` is the injection through completion.
SCRIPT = [
    ("right", None, "grid: the string column"),
    ("down", None, "row 1: the SQL injection value"),
    ("=", r"COPY \(SELECT 1\)", "= on the injection value: it is quoted into the filter"),
    ("x", r"SQL WHERE expression", "clear"),
    ("down", None, "row 2: OSC title"),
    ("d", r"file row", "detail pane"),
    ("y", r"Copied", "copy (OSC 52) from the grid"),
    ("down down", None, "rows with OSC 52, OSC 8"),
    ("y", r"Copied .*CLIP-VAL|Copied", "copy a value holding OSC 52"),
    ("tab down down", None, "into the detail pane"),
    ("y", r"Copied", "copy from the detail pane"),
    ("esc", None, "close the pane"),
    ("down down down down down down", None, "the ESC[2J, C1, tab and bidi rows"),
    ("end", None, "last columns: hostile names, binary, invalid UTF-8"),
    ("home", None, "back"),
    ("2", r"null %", "Schema: hostile names, units, descriptions"),
    ("down down down down down", None, "schema rows"),
    ("3", r"Profil", "Stats on a hostile column"),
    ("4", r"mode ", "Plot"),
    ("5", r"row groups", "Metadata: hostile key-value metadata"),
    ("pgdn", None, "more metadata"),
    ("1", None, "back to the grid"),
    ("c", r"Visible columns", "column picker: hostile names"),
    ("esc", None, "close it"),
    ("e", r"Export current view", "export dialog"),
    ("esc", None, "close it"),
    ("/", None, "filter box"),
    ("text:rand", r"COPY \(SELECT 'echo PWNED'\)", "completion offers the hostile name"),
    ("right", None, "accept the completion"),
    ("enter", None, "apply it: the name must be quoted, not run"),
    ("esc", None, "leave the box"),
    ("x", None, "clear"),
    ("/", None, "filter box"),
    ("ctrl+u", None, "empty it"),
    ("text:select cast(s as integer) as v from t where a = 7", None, "a query whose error quotes a file value"),
    ("enter", r"Query failed", "the error"),
    ("esc", None, "leave the box"),
    ("x", None, "clear"),
    ("?", r"Parquet explorer", "help"),
    ("esc", None, "close help"),
]
MARKER_WAIT = 10.0

# ------------------------------------------------------------------ the byte checks
#: CSI sequences a terminal app writes itself, by final byte (with the parameters each
#: may have): cursor moves, erasing, modes, scrolling regions, SGR, reports and queries,
#: cursor shape, keyboard-protocol settings
CSI_FINALS = set("ABCDEFGHJKSTXZLMP@`dfhlmnprsuctq") | {"~"}
CSI_RE = re.compile(rb"\x1b\[([0-?]*)([ -/]*)([@-~])")
OSC_RE = re.compile(rb"\x1b\]([^\x07\x1b]*)(\x07|\x1b\\)")
STRING_RE = re.compile(rb"\x1b([P_^X])([^\x1b\x07]*)(\x1b\\|\x07)")
ESC2_OK = set(b"78=>McDEH") | {ord("(")}
#: code points that reorder or hide text; pqx shows them as ⟨U+XXXX⟩
HIDDEN = {0x200B, 0x200C, 0x200D, 0x200E, 0x200F, 0x202A, 0x202B, 0x202C, 0x202D, 0x202E,
          0x2066, 0x2067, 0x2068, 0x2069, 0xFEFF, 0x061C, 0x2060}
B64 = re.compile(rb"[A-Za-z0-9+/=]*")


def check_bytes(b: bytes, pwns=()) -> list[str]:
    """Everything wrong with what an app wrote. Every escape sequence must be one an app
    writes itself; the text between them must be valid UTF-8 without C1 controls, other
    C0 controls than CR, LF, tab, backspace and BEL, or hidden/bidi code points."""
    fails = []

    def fail(msg, at):
        if len(fails) < 30:
            fails.append(f"{msg}: …{b[max(0, at - 30):at + 50]!r}")

    text = bytearray()
    i, n = 0, len(b)
    while i < n:
        c = b[i]
        if c != 0x1B:
            text.append(c)
            i += 1
            continue
        nxt = b[i + 1:i + 2]
        if nxt == b"[":
            m = CSI_RE.match(b, i)
            if not m:
                fail("unterminated or malformed CSI", i)
                i += 2
                continue
            params, inter, final = m.group(1), m.group(2), chr(m.group(3)[0])
            if final not in CSI_FINALS or len(params) > 64 or inter not in (b"", b" ", b"$", b"!", b'"'):
                fail(f"a CSI no app writes (final {final!r})", i)
            i = m.end()
        elif nxt == b"]":
            m = OSC_RE.match(b, i)
            if not m:
                fail("unterminated OSC", i)
                i += 2
                continue
            body = m.group(1)
            code, _, payload = body.partition(b";")
            if code in (b"0", b"1", b"2"):
                if not payload.startswith(b"pqx") or re.search(rb"[\x00-\x1f\x7f]|\xc2[\x80-\x9f]", payload):
                    fail("a window title that isn't the app's own", i)
            elif code == b"52":
                sel, _, data = payload.partition(b";")
                if not B64.fullmatch(data):
                    fail("an OSC 52 payload that isn't base64", i)
                else:
                    try:
                        if controls(base64.b64decode(data).decode("utf-8", "replace")):
                            fail("an OSC 52 copy carrying control characters", i)
                    except ValueError:
                        fail("an OSC 52 payload that isn't base64", i)
            elif code in (b"10", b"11", b"12") and payload == b"?":
                pass  # colour queries
            elif code == b"22" and re.fullmatch(rb"[a-z-]*", payload):
                pass  # pointer shape
            else:
                fail(f"an OSC no app writes (OSC {code.decode('latin-1')!r})", i)
            i = m.end()
        elif nxt in (b"P", b"_", b"^", b"X"):
            m = STRING_RE.match(b, i)
            if not m:
                fail("unterminated DCS/APC/PM/SOS", i)
                i += 2
                continue
            if not (nxt == b"P" and re.fullmatch(rb"(\+q|\$q)[0-9A-Za-z;]*", m.group(2))):
                fail("a DCS/APC/PM/SOS string no app writes", i)  # capability queries only
            i = m.end()
        elif nxt and nxt[0] in ESC2_OK:
            i += 3 if nxt in (b"(",) else 2
        else:
            fail(f"a stray ESC {nxt!r}", i)
            i += 2
    try:
        s = text.decode("utf-8")
    except UnicodeDecodeError as e:
        bad = bytes(text[max(0, e.start - 30):e.end + 30])
        fails.append(f"bytes that aren't UTF-8 (raw C1 or broken text): {bad!r}")
        s = text.decode("utf-8", "replace")
    for j, ch in enumerate(s):
        o = ord(ch)
        if 0x80 <= o < 0xA0:
            fails.append(f"a C1 control U+{o:04X}: …{s[max(0, j - 30):j + 30]!r}")
        elif (o < 0x20 and ch not in "\r\n\t\b\x07") or o == 0x7F:
            fails.append(f"a control character U+{o:04X}: …{s[max(0, j - 30):j + 30]!r}")
        elif o in HIDDEN:
            fails.append(f"a hidden or bidi code point U+{o:04X}: …{s[max(0, j - 30):j + 30]!r}")
        if len(fails) >= 40:
            break
    for p in pwns:
        if os.path.exists(p):
            fails.append(f"SQL from the file ran: {p} exists")
    if "␛" not in s:
        fails.append("the hostile text was never drawn (no ␛ stand-in)")
    return fails


def selftest() -> int:
    """The checks against streams with one fault each (and a clean one)."""
    ok = (b"\x1b[?1049h\x1b[1;1H\x1b[1;2;38;5;12mpqx \xe2\x90\x9b]0;x\x1b[0m\r\n\x1b]52;c;"
          + base64.b64encode("✓ fine".encode()) + b"\x07\x1b]0;pqx demo.parquet\x07\x1b]22;default\x1b\\"
          + b"\x1bP+q544e\x1b\\\x1b[>1u\x1b[?2026$p\x1b7\x1b8\x1b(B")
    cases = {
        "clean": (ok, False),
        "raw 0x9B": (ok + b"\x9b31m", True),
        "C1 as UTF-8": (ok + b"\xc2\x9b31m", True),
        "DCS from a file": (ok + b"\x1bPq#0;2;0;0;0\x1b\\", True),
        "APC": (ok + b"\x1b_Gf=24\x1b\\", True),
        "U+200B": (ok + "a​b".encode(), True),
        "U+202E": (ok + "a‮b".encode(), True),
        "bare CSI final": (ok + b"\x1b[31;1y", True),
        "cut-off OSC": (ok + b"\x1b]0;pqx title", True),
        "OSC 8 link": (ok + b"\x1b]8;;https://x\x1b\\", True),
        "title from a file": (ok + b"\x1b]0;PWNED\x07", True),
        "OSC 52 with ESC inside": (ok + b"\x1b]52;c;" + base64.b64encode(b"a\x1b]0;x") + b"\x07", True),
        "SO": (ok + b"\x0e", True),
        "stray ESC": (ok + b"\x1bZ", True),
    }
    bad = 0
    for name, (data, want_fail) in cases.items():
        got = bool(check_bytes(data))
        print(f"{'ok  ' if got == want_fail else 'FAIL'} {name}: {'flagged' if got else 'clean'}")
        bad += got != want_fail
    return 1 if bad else 0


def run_app(app: str, cmd: str, size, keep: bool) -> tuple[list[str], str]:
    d = tempfile.mkdtemp(prefix=f"pqx-sec-{app}-", dir=os.environ.get("PQX_PARITY_SCRATCH"))
    try:
        path, pwns = make_evil(d, "evil" + ESC + "]2;FNAME-TITLE\x07" + C1_CSI + ".parquet")
        env = dict(os.environ, XDG_CONFIG_HOME=os.path.join(d, "config"))
        s = Session([cmd, path], size[0], size[1], env=env, cwd=d)
        fails = []
        try:
            if not s.wait(r"\S", 30):
                return ["the app drew nothing"], d
            s.settle(0.8, 20)
            for keys, marker, what in SCRIPT:
                if keys.startswith("text:"):
                    s.type(keys[5:])
                else:
                    s.keys(*keys.split(), gap=0.05)
                s.drain(0.3)
                if marker and not s.wait(marker, MARKER_WAIT):
                    fails.append(f"never reached: {what} (after {keys!r}, no {marker!r} on the screen)")
                s.settle(0.4, 8)
                if s.exited is not None:
                    fails.append(f"the app exited (status {s.exited}) after {keys!r} ({what})")
                    break
            b = bytes(s.raw)
        finally:
            s.close()
        if keep:
            with open(os.path.join(d, "terminal.raw"), "wb") as fh:
                fh.write(b)
        return fails + check_bytes(b, pwns), d
    finally:
        if not keep:
            shutil.rmtree(d, ignore_errors=True)


def main(argv=None):
    p = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    p.add_argument("--app", default="both", choices=["python", "go", "both"])
    p.add_argument("--size", default="200x50")
    p.add_argument("--keep", action="store_true", help="keep the temporary directory and the raw output")
    p.add_argument("--selftest", action="store_true", help="check the byte checks on made-up streams")
    a = p.parse_args(argv)
    if a.selftest:
        return selftest()
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
