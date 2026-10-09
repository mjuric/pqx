"""Drive a terminal app in a real pty with an emulated screen (pyte).

Shared by the parity runner (go/bench/parity/run.py), the pty security test
(go/bench/parity/security.py) and the benchmarks (bench.py):

    s = Session(["pqx", "file.parquet"], cols=120, rows=40)
    s.wait(r"rows")                # until the screen text matches
    s.keys("down", "s", "pgdn")    # named keys or literal text
    s.settle()                     # until the screen stops changing
    print(s.text())
    s.close()

Everything the app writes is kept in ``s.raw``, so OSC 52 clipboard writes and any
stray escape sequences can be checked afterwards.
"""
from __future__ import annotations

import base64
import fcntl
import os
import re
import select
import signal
import struct
import termios
import time

import pyte

#: named keys, as an xterm sends them (Textual and Bubble Tea both decode these)
KEYS = {
    "up": "\x1b[A", "down": "\x1b[B", "right": "\x1b[C", "left": "\x1b[D",
    "home": "\x1b[H", "end": "\x1b[F", "pgup": "\x1b[5~", "pgdn": "\x1b[6~",
    "ctrl+home": "\x1b[1;5H", "ctrl+end": "\x1b[1;5F",
    "ctrl+left": "\x1b[1;5D", "ctrl+right": "\x1b[1;5C",
    "shift+tab": "\x1b[Z", "tab": "\t", "enter": "\r", "esc": "\x1b",
    "backspace": "\x7f", "space": " ", "ctrl+x": "\x18", "ctrl+c": "\x03",
    "ctrl+u": "\x15", "ctrl+a": "\x01", "ctrl+e": "\x05", "delete": "\x1b[3~",
}

#: a lone ESC has to stand alone, or the next key reads as Alt+key
ESC_GAP = 0.2

#: braille spinner frames, and the other spinners Textual and Bubbles draw
SPINNER = "⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏⣾⣽⣻⢿⡿⣟⣯⣷◐◓◑◒"


def key_bytes(k: str) -> bytes:
    """A named key (`down`, `ctrl+end`), `text:...` for literal text, or a single character."""
    if k in KEYS:
        return KEYS[k].encode()
    if k.startswith("text:"):
        return k[5:].encode()
    if k.startswith("raw:"):
        return k[4:].encode().decode("unicode_escape").encode("latin-1")
    if k.startswith("click:") or k.startswith("rclick:"):
        # click:X,Y (1-based screen cell) as an SGR mouse press and release
        x, y = (int(v) for v in k.split(":", 1)[1].split(","))
        b = 2 if k.startswith("r") else 0
        return f"\x1b[<{b};{x};{y}M\x1b[<{b};{x};{y}m".encode()
    if len(k) == 1:
        return k.encode()
    raise ValueError(f"unknown key {k!r}")


# ------------------------------------------------------------------ the screen
_SGR_COLOUR_256 = {i: n for i, n in enumerate(
    ["black", "red", "green", "brown", "blue", "magenta", "cyan", "white",
     "brightblack", "brightred", "brightgreen", "brightbrown", "brightblue", "brightmagenta",
     "brightcyan", "brightwhite"])}


class Screen(pyte.Screen):
    """pyte's screen, plus the faint attribute (SGR 2, kept in the `blink` slot, since
    nothing in pqx blinks), 256-colour indices 0–15 kept as names (so `38;5;8` equals
    `90`), and device queries answered."""

    def __init__(self, cols, rows, reply):
        super().__init__(cols, rows)
        self._reply = reply

    def resize(self, lines=None, columns=None):
        """As pyte's, but a smaller screen loses its bottom lines, not its top ones: what
        xterm and VTE do on the alternate screen when the cursor isn't below the new last
        line (pyte drops the top, which an app that redraws only the lines it changed,
        like Textual, then never repaints)."""
        lines = lines or self.lines
        if lines < self.lines:
            for y in range(lines, self.lines):
                self.buffer.pop(y, None)
            self.lines = lines  # the base resize then has no lines to drop
            self.cursor.y = min(self.cursor.y, lines - 1)
        super().resize(lines, columns)

    def write_process_input(self, data):
        self._reply(data.encode())

    def select_graphic_rendition(self, *attrs, private=False):
        if private:  # CSI > ... m (xterm modifyOtherKeys etc.): not SGR
            return
        out, dim = [], None
        a = list(attrs)
        i = 0
        while i < len(a):
            v = a[i]
            if v in (38, 48) and i + 1 < len(a):
                if a[i + 1] == 5 and i + 2 < len(a):
                    n = a[i + 2]
                    if n < 16:
                        base = (30 if v == 38 else 40) if n < 8 else (90 if v == 38 else 100)
                        out.append(base + n % 8)
                    else:
                        out += a[i:i + 3]
                    i += 3
                    continue
                if a[i + 1] == 2:
                    out += a[i:i + 5]
                    i += 5
                    continue
            if v == 2:
                dim = True
            elif v == 22:
                dim = False
                out.append(22)
            elif v in (5, 6, 25):
                pass
            elif v == 0:
                dim = False
                out.append(0)
            else:
                out.append(v)
            i += 1
        if out:
            super().select_graphic_rendition(*out)
        elif not attrs:
            super().select_graphic_rendition()
        if dim is not None:
            self.cursor.attrs = self.cursor.attrs._replace(blink=dim)


_CSI_PRIVATE = re.compile(rb"\x1b\[[<=>][0-9;:]*[ -/]*[@-~]")
_STRING_SEQ = re.compile(rb"\x1b[P_^X].*?(?:\x1b\\|\x07)", re.S)
_SGR_COLON = re.compile(rb"\x1b\[([0-9;]*:[0-9;:]*)m")


def _fix_sgr(m) -> bytes:
    """SGR with colon sub-parameters (`4:3`, `38:2::r:g:b`), which pyte can't parse."""
    out = []
    for part in m.group(1).split(b";"):
        if b":" not in part:
            out.append(part)
            continue
        sub = part.split(b":")
        if sub[0] in (b"38", b"48") and len(sub) > 2:
            if sub[1] == b"5":
                out += [sub[0], b"5", sub[-1]]
            elif sub[1] == b"2" and len(sub) >= 5:
                out += [sub[0], b"2"] + sub[-3:]
        elif sub[0] == b"4":
            out.append(b"24" if sub[1] == b"0" else b"4")
        # 58 (underline colour) and anything else: dropped
    return b"\x1b[" + b";".join(out) + b"m"


class TermFilter:
    """What pyte would misread, removed before it sees it: CSI sequences with a `<`, `=`
    or `>` prefix (keyboard-protocol and modifyOtherKeys settings, which pyte reads as
    SGR), DCS/APC/PM/SOS strings (capability queries, which it would draw), and colon
    sub-parameters in SGR. Incomplete sequences at the end of a read are held back."""

    def __init__(self):
        self.held = b""

    def feed(self, data: bytes) -> bytes:
        data = self.held + data
        self.held = b""
        i = data.rfind(b"\x1b")
        if i >= 0 and not self._complete(data[i:]):
            data, self.held = data[:i], data[i:]
            if len(self.held) > 4096:  # not a sequence after all
                data, self.held = data + self.held, b""
        data = _STRING_SEQ.sub(b"", data)
        data = _CSI_PRIVATE.sub(b"", data)
        return _SGR_COLON.sub(_fix_sgr, data)

    @staticmethod
    def _complete(seq: bytes) -> bool:
        if len(seq) < 2:
            return False
        c = seq[1:2]
        if c == b"[":
            return re.match(rb"\x1b\[[0-?]*[ -/]*[@-~]", seq) is not None
        if c in (b"]", b"P", b"_", b"^", b"X"):
            return seq.endswith(b"\x07") or b"\x1b\\" in seq[2:] or b"\x07" in seq
        return True


def styles_changed(a, b) -> bool:
    """Whether two style grids differ in more than one cell."""
    n = 0
    for ra, rb in zip(a, b):
        if ra != rb:
            n += sum(x != y for x, y in zip(ra, rb))
            if n > 1:
                return True
    return len(a) != len(b)


def cell_style(c) -> tuple:
    """The style of a pyte cell: (fg, bg, bold, dim, reverse, underline, italic)."""
    return (c.fg, c.bg, c.bold, c.blink, c.reverse, c.underscore, c.italics)


# ------------------------------------------------------------------ the session
class Session:
    def __init__(self, argv, cols=120, rows=40, env=None, cwd=None):
        self.cols, self.rows = cols, rows
        self.raw = bytearray()
        self.screen = Screen(cols, rows, self._answer)
        self.stream = pyte.ByteStream(self.screen)
        self.filter = TermFilter()
        self.parse_errors = []
        self.t0 = time.perf_counter()
        self.exited = None
        self._last_write = 0.0
        self._last_esc = False
        e = dict(os.environ if env is None else env)
        e["TERM"] = "xterm-256color"
        # no COLUMNS/LINES: they would override the terminal's size for good (Python's
        # shutil.get_terminal_size reads them first), and a resize would go unseen
        for k in ("COLUMNS", "LINES", "NO_COLOR"):
            e.pop(k, None)
        # the size is set on the pty before the app starts, so it never sees another
        master, slave = os.openpty()
        fcntl.ioctl(slave, termios.TIOCSWINSZ, struct.pack("HHHH", rows, cols, 0, 0))
        pid = os.fork()
        if pid == 0:
            try:
                os.close(master)
                os.setsid()
                fcntl.ioctl(slave, termios.TIOCSCTTY, 0)
                for fd in (0, 1, 2):
                    os.dup2(slave, fd)
                if slave > 2:
                    os.close(slave)
                if cwd:
                    os.chdir(cwd)
                os.execvpe(argv[0], argv, e)
            finally:
                os._exit(127)
        os.close(slave)
        self.pid, self.fd = pid, master

    # -- io
    def _answer(self, data: bytes):
        try:
            os.write(self.fd, data)
        except OSError:
            pass

    def pump(self, timeout: float) -> bool:
        """Read what the app wrote within `timeout`; False once the app has exited."""
        if self.exited is not None:
            time.sleep(max(0.0, timeout))
            return False
        r, _, _ = select.select([self.fd], [], [], max(0.0, timeout))
        if not r:
            return True
        try:
            data = os.read(self.fd, 1 << 16)
        except OSError:
            data = b""
        if not data:
            self._reap()
            return False
        self.raw += data
        try:
            self.stream.feed(self.filter.feed(data))
        except (TypeError, ValueError, IndexError, KeyError) as e:
            # a sequence pyte can't take (wrong parameter count, say): note it, start the
            # parser afresh, and carry on; the raw bytes are kept for the checks
            self.parse_errors.append(f"{type(e).__name__}: {e}")
            self.stream = pyte.ByteStream(self.screen)
        return True

    def _reap(self):
        if self.exited is None:
            try:
                _, st = os.waitpid(self.pid, 0)
                self.exited = os.waitstatus_to_exitcode(st)
            except ChildProcessError:
                self.exited = -1

    def write(self, data: bytes):
        if self._last_esc:  # let a lone ESC be read as Esc, not Alt+key
            gap = ESC_GAP - (time.perf_counter() - self._last_write)
            if gap > 0:
                self.drain(gap)
        os.write(self.fd, data)
        self._last_write = time.perf_counter()
        self._last_esc = data == b"\x1b"

    def keys(self, *names, gap: float = 0.0):
        """Send keys; `gap` seconds of reading in between."""
        for k in names:
            self.write(key_bytes(k))
            if gap:
                self.drain(gap)

    def type(self, text: str, gap: float = 0.0):
        if gap:
            for ch in text:
                self.write(ch.encode())
                self.drain(gap)
        else:
            self.write(text.encode())

    def drain(self, seconds: float):
        end = time.perf_counter() + seconds
        while (left := end - time.perf_counter()) > 0:
            self.pump(min(left, 0.05))

    # -- screen
    def lines(self) -> list[str]:
        return [ln.rstrip() for ln in self.screen.display]

    def text(self) -> str:
        return "\n".join(self.lines())

    def styles(self) -> list[list[tuple]]:
        buf = self.screen.buffer
        return [[cell_style(buf[y][x]) for x in range(self.cols)] for y in range(self.rows)]

    def wait(self, rx, timeout: float = 30.0, norm=None) -> bool:
        """Until the screen text matches `rx` (a regex, multiline)."""
        rx = re.compile(rx, re.M) if isinstance(rx, str) else rx
        end = time.perf_counter() + timeout
        while True:
            t = self.text()
            if rx.search(norm(t) if norm else t):
                return True
            if time.perf_counter() >= end:
                return False
            if not self.pump(min(0.01, end - time.perf_counter())) and self.exited is not None:
                t = self.text()
                return bool(rx.search(norm(t) if norm else t))

    def wait_gone(self, rx, timeout: float = 30.0) -> bool:
        rx = re.compile(rx, re.M) if isinstance(rx, str) else rx
        end = time.perf_counter() + timeout
        while rx.search(self.text()):
            if time.perf_counter() >= end:
                return False
            if not self.pump(0.01):
                break
        return True

    def settle(self, quiet: float = 0.4, timeout: float = 15.0, norm=None) -> bool:
        """Until neither the (normalized) screen text nor the cell styles have changed
        for `quiet` seconds. A style change of a single cell (a blinking text cursor)
        doesn't count; a cursor moving in a grid changes many."""
        end = time.perf_counter() + timeout
        last = last_styles = None
        since = time.perf_counter()
        while time.perf_counter() < end:
            t = self.text()
            t = norm(t) if norm else t
            st = self.styles()
            if t != last or last_styles is None or styles_changed(last_styles, st):
                last, last_styles, since = t, st, time.perf_counter()
            elif time.perf_counter() - since >= quiet:
                return True
            self.pump(0.02)
        return False

    def resize(self, cols: int, rows: int):
        """Change the terminal size, as a terminal window does (TIOCSWINSZ and SIGWINCH)."""
        self.cols, self.rows = cols, rows
        self.screen.resize(rows, cols)
        fcntl.ioctl(self.fd, termios.TIOCSWINSZ, struct.pack("HHHH", rows, cols, 0, 0))
        try:
            os.kill(self.pid, signal.SIGWINCH)
        except ProcessLookupError:
            pass

    def find(self, rx):
        """(x, y), 1-based, of the first match of `rx` on the screen, or None."""
        rx = re.compile(rx) if isinstance(rx, str) else rx
        for y, line in enumerate(self.screen.display):
            m = rx.search(line)
            if m:
                return m.start() + 1, y + 1
        return None

    def osc52(self) -> list[str]:
        """The clipboard writes (OSC 52) so far, decoded."""
        out = []
        for m in re.finditer(rb"\x1b\]52;[a-z]*;([A-Za-z0-9+/=]*)(?:\x07|\x1b\\)", bytes(self.raw)):
            try:
                out.append(base64.b64decode(m.group(1)).decode("utf-8", "replace"))
            except ValueError:
                out.append("<bad base64>")
        return out

    def close(self):
        if self.exited is None:
            try:
                os.kill(self.pid, signal.SIGKILL)
            except ProcessLookupError:
                pass
            self._reap()
        try:
            os.close(self.fd)
        except OSError:
            pass

    def __enter__(self):
        return self

    def __exit__(self, *a):
        self.close()
