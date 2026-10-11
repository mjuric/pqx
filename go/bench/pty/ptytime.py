"""Time a TUI in a real pty: spawn, then (keys, wait-for-regex) steps on an emulated 200x50 screen.

    ptytime.py [--dump] [--timeout S] -- CMD ARGS... ::: STEP ::: STEP ...

A STEP is `KEYS=>REGEX`: send KEYS (Python escapes allowed, e.g. \\x1b[6~), then wait until
the screen text matches REGEX. The first step usually has empty KEYS (startup). Prints the
elapsed seconds of each step as JSON. --dump prints the screen after each step.
"""
import json
import os
import pty
import re
import select
import signal
import sys
import time

import pyte

W, H = 200, 50


def main():
    argv = sys.argv[1:]
    dump = "--dump" in argv
    timeout = 60.0
    if "--timeout" in argv:
        timeout = float(argv[argv.index("--timeout") + 1])
    argv = argv[argv.index("--") + 1:]
    parts, cur = [], []
    for a in argv:
        if a == ":::":
            parts.append(cur)
            cur = []
        else:
            cur.append(a)
    parts.append(cur)
    cmd, steps = parts[0], [" ".join(p) for p in parts[1:]]

    screen = pyte.Screen(W, H)
    stream = pyte.ByteStream(screen)
    t0 = time.perf_counter()
    pid, fd = pty.fork()
    if pid == 0:
        os.environ["TERM"] = "xterm-256color"
        os.environ["COLUMNS"], os.environ["LINES"] = str(W), str(H)
        os.execvp(cmd[0], cmd)
    import fcntl
    import struct
    import termios
    fcntl.ioctl(fd, termios.TIOCSWINSZ, struct.pack("HHHH", H, W, 0, 0))

    def pump(deadline):
        r, _, _ = select.select([fd], [], [], max(0.0, deadline - time.perf_counter()))
        if r:
            try:
                data = os.read(fd, 1 << 16)
            except OSError:
                return False
            if not data:
                return False
            stream.feed(data)
            # answer cursor-position / device-attribute queries some TUIs send at startup
            if b"\x1b[6n" in data:
                os.write(fd, f"\x1b[{screen.cursor.y + 1};{screen.cursor.x + 1}R".encode())
            if b"\x1b[c" in data or b"\x1b[0c" in data:
                os.write(fd, b"\x1b[?62;22c")
        return True

    out = {}
    start = t0
    for i, step in enumerate(steps):
        keys, _, rx = step.partition("=>")
        keys = keys.encode().decode("unicode_escape").encode("latin-1")
        start = time.perf_counter()
        if keys:
            os.write(fd, keys)
        if rx.strip().startswith("SLEEP:"):
            end = start + float(rx.strip()[6:])
            while time.perf_counter() < end:
                pump(end)
            out[f"step{i}"] = None
            continue
        rx = re.compile(rx.strip(), re.M)
        ref = t0 if i == 0 else start
        deadline = start + timeout
        ok = False
        while time.perf_counter() < deadline:
            if rx.search("\n".join(screen.display)):
                ok = True
                break
            if not pump(min(deadline, time.perf_counter() + 0.002)):
                break
        out[f"step{i}"] = round(time.perf_counter() - ref, 4) if ok else None
        if dump or not ok:
            print(f"--- after step {i} ({'ok' if ok else 'TIMEOUT'}): {step!r}", file=sys.stderr)
            print("\n".join(line.rstrip() for line in screen.display), file=sys.stderr)
    os.kill(pid, signal.SIGKILL)
    os.waitpid(pid, 0)
    print(json.dumps(out))


main()
