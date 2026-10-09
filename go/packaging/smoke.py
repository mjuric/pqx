"""Headless smoke test of a built pqx: open a Parquet file in a terminal, check the screen, quit.

    python smoke.py PQX FILE.parquet [--expect TEXT ...] [--timeout S]

Runs PQX FILE in a 200x50 pseudo-terminal (pty on Linux and macOS, ConPTY through
pywinpty on Windows), waits until every --expect text appears in the output (with
escape sequences removed), sends `q` and checks that pqx exits with status 0.
Standard library only, apart from pywinpty on Windows. Exit status 0 on success;
on failure it prints the output seen so far.
"""
import argparse
import os
import re
import sys
import time

W, H = 200, 50
# CSI, OSC (BEL- or ST-terminated) and other two-byte escapes
ESC_RE = re.compile(r"\x1b\[[0-?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)|\x1b[@-Z\\-_]")


class Term:
    """A child process on a pseudo-terminal: read(), write(), wait()."""

    def __init__(self, argv):
        env = dict(os.environ, TERM="xterm-256color", COLUMNS=str(W), LINES=str(H))
        if os.name == "nt":
            from winpty import PtyProcess  # pywinpty

            self.p = PtyProcess.spawn(argv, dimensions=(H, W), env=env)
            self.win = True
            return
        import fcntl
        import pty
        import struct
        import termios

        self.win = False
        self.pid, self.fd = pty.fork()
        if self.pid == 0:
            os.environ.update(env)
            try:
                os.execvp(argv[0], argv)
            finally:
                os._exit(127)
        fcntl.ioctl(self.fd, termios.TIOCSWINSZ, struct.pack("HHHH", H, W, 0, 0))

    def read(self, wait):
        """Return what the child wrote within `wait` seconds ('' if nothing); None at EOF."""
        if self.win:
            if not self.p.isalive():
                return None
            time.sleep(wait)
            try:
                return self.p.read(1 << 16)
            except EOFError:
                return None
        import select

        r, _, _ = select.select([self.fd], [], [], wait)
        if not r:
            return ""
        try:
            data = os.read(self.fd, 1 << 16)
        except OSError:  # EIO: the child closed the pty
            return None
        return data.decode("utf-8", "replace") if data else None

    def write(self, s):
        if self.win:
            self.p.write(s)
        else:
            os.write(self.fd, s.encode())

    def wait(self, timeout):
        """The exit status, or None if the child is still running after `timeout` seconds."""
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            if self.win:
                if not self.p.isalive():
                    return self.p.exitstatus
            else:
                pid, status = os.waitpid(self.pid, os.WNOHANG)
                if pid:
                    return os.waitstatus_to_exitcode(status)
            if self.read(0.05) is None:  # keep draining the output so the child never blocks
                time.sleep(0.05)
        return None

    def kill(self):
        if self.win:
            self.p.terminate(force=True)
        else:
            import signal

            os.kill(self.pid, signal.SIGKILL)
            os.waitpid(self.pid, 0)


def main():
    ap = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    ap.add_argument("pqx")
    ap.add_argument("file")
    ap.add_argument("--expect", action="append", default=[], help="text that must appear (repeatable)")
    ap.add_argument("--timeout", type=float, default=60.0)
    a = ap.parse_args()
    expect = a.expect or [os.path.basename(a.file)]

    t0 = time.monotonic()
    term = Term([a.pqx, "--threads", "2", a.file])
    raw = ""
    ok = False
    while time.monotonic() - t0 < a.timeout:
        data = term.read(0.05)
        if data is None:
            break
        raw += data
        # answer the cursor-position and device-attribute queries a TUI may send at startup
        if "\x1b[6n" in data:
            term.write("\x1b[1;1R")
        if "\x1b[c" in data or "\x1b[0c" in data:
            term.write("\x1b[?62;22c")
        text = ESC_RE.sub("", raw)
        if all(e in text for e in expect):
            ok = True
            break
    elapsed = time.monotonic() - t0
    if not ok:
        print(f"FAIL: {expect} not seen within {elapsed:.1f} s; output so far:", file=sys.stderr)
        print(ESC_RE.sub("", raw)[-4000:], file=sys.stderr)
        term.kill()
        return 1
    print(f"opened {a.file} in {elapsed:.2f} s; saw {expect}")

    term.write("q")
    status = term.wait(15)
    if status is None:
        print("FAIL: pqx still running 15 s after q", file=sys.stderr)
        term.kill()
        return 1
    if status != 0:
        print(f"FAIL: pqx exited with status {status}", file=sys.stderr)
        return 1
    print("quit with q: exit status 0")
    return 0


if __name__ == "__main__":
    sys.exit(main())
