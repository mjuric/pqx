"""Mouse input from terminals/multiplexers that don't speak SGR (e.g. GNU screen)."""
import pqx.app  # noqa: F401  (installs the work-arounds)
from pqx._terminal import x10_to_sgr


def x10(b, x, y):
    return "\x1b[M" + chr(32 + b) + chr(32 + x) + chr(32 + y)


def test_translation():
    assert x10_to_sgr(x10(0, 51, 13)) == ("\x1b[<0;51;13M", 0)
    assert x10_to_sgr(x10(2, 51, 13)) == ("\x1b[<2;51;13M", 2)          # right button
    assert x10_to_sgr(x10(3, 51, 13), 2) == ("\x1b[<2;51;13m", 2)       # release of the last button
    assert x10_to_sgr(x10(35, 51, 13))[0] == "\x1b[<35;51;13M"          # motion
    assert x10_to_sgr(x10(65, 51, 13))[0] == "\x1b[<65;51;13M"          # wheel down
    assert x10_to_sgr("\x1b[32;51;13M") == ("\x1b[<0;51;13M", 0)        # urxvt
    assert x10_to_sgr("\x1b[<0;51;13M") is None                         # already SGR
    assert x10_to_sgr("\x1b[A") is None


def test_parser_understands_x10_and_urxvt():
    from textual._xterm_parser import XTermParser

    p = XTermParser()
    msgs = list(p.feed(x10(0, 121, 13) + x10(3, 121, 13) + "\x1b[32;5;6M")) + list(p.tick())
    kinds = [(type(m).__name__, int(m.x), int(m.y)) for m in msgs]
    assert kinds == [("MouseDown", 120, 12), ("MouseUp", 120, 12), ("MouseDown", 4, 5)]


def test_input_decoding_survives_invalid_utf8():
    from textual.drivers import linux_driver

    dec = linux_driver.getincrementaldecoder("utf-8")()
    # an X10 click at column 120: 0x98 is not valid UTF-8 and used to kill the input thread
    assert dec.decode(b"\x1b[M \x98-") == "\x1b[M \x98-"
    assert dec.decode("é✓".encode()) == "é✓"   # real UTF-8 is unaffected


def test_pixel_mouse_mode_is_never_enabled():
    from textual.drivers.linux_driver import LinuxDriver

    class FakeDriver:
        _mouse = True
        _mouse_pixels = False
        written = ""

        def write(self, s):
            self.written += s

    d = FakeDriver()
    LinuxDriver._enable_mouse_pixels(d)
    assert "1016" not in d.written and not d._mouse_pixels


def test_iterm_over_ssh_handshake_keeps_cell_coordinates(demo_path):
    """Replay the handshake from an iTerm2-over-ssh keys.log in a real pty.

    iTerm answers the mode-2048 query 'supported', but never sends in-band
    size reports. pqx must not switch the mouse to pixel coordinates."""
    import os
    import select
    import signal
    import sys
    import time

    import pytest

    if not sys.platform.startswith(("linux", "darwin")):
        pytest.skip("needs a pty")
    import fcntl
    import pty
    import struct
    import termios

    pid, fd = pty.fork()
    if pid == 0:  # child: the real pqx CLI
        env = {k: v for k, v in os.environ.items() if k not in ("TERM_PROGRAM", "LC_TERMINAL")}
        env["TERM"] = "xterm-256color"
        os.execve(sys.executable, [sys.executable, "-m", "pqx", demo_path], env)
    fcntl.ioctl(fd, termios.TIOCSWINSZ, struct.pack("HHHH", 40, 120, 0, 0))
    out, answered = b"", False
    try:
        end = time.time() + 20
        while time.time() < end:
            r, _, _ = select.select([fd], [], [], 0.1)
            if r:
                try:
                    out += os.read(fd, 65536)
                except OSError:
                    break
            if not answered and b"\x1b[?2048$p" in out:
                os.write(fd, b"\x1b[?2026;2$y\x1b[?2048;2$y")  # exactly what iTerm replied
                answered, end = True, time.time() + 3
        assert answered, "pqx never queried mode 2048 (did the app start?)"
        assert b"\x1b[?1006h" in out  # SGR mouse is on…
        assert b"\x1b[?1016h" not in out  # …but never in pixel coordinates
    finally:
        os.kill(pid, signal.SIGKILL)
        os.waitpid(pid, 0)


def test_quit_clears_screen_before_leaving_alt_screen(demo_path):
    """If the terminal ignores the alternate screen, quitting must not leave pqx's
    last frame behind: the screen is cleared before mode 1049 is switched off."""
    import os
    import select
    import signal
    import sys
    import time

    import pytest

    if not sys.platform.startswith(("linux", "darwin")):
        pytest.skip("needs a pty")
    import fcntl
    import pty
    import struct
    import termios

    pid, fd = pty.fork()
    if pid == 0:
        env = dict(os.environ, TERM="xterm-256color")
        os.execve(sys.executable, [sys.executable, "-m", "pqx", demo_path], env)
    fcntl.ioctl(fd, termios.TIOCSWINSZ, struct.pack("HHHH", 40, 120, 0, 0))
    out = b""

    def pump(t, until=None):
        nonlocal out
        end = time.time() + t
        while time.time() < end:
            if select.select([fd], [], [], 0.1)[0]:
                try:
                    out += os.read(fd, 65536)
                except OSError:
                    return
            if until and until in out:
                return

    try:
        pump(20, until=b"rows")  # the app has drawn its first frame
        pump(1)
        n = len(out)
        os.write(fd, b"q")
        pump(8)
        tail = out[n:]
        clear, leave = tail.find(b"\x1b[H\x1b[2J"), tail.find(b"\x1b[?1049l")
        assert leave >= 0, "pqx did not leave the alternate screen"
        assert 0 <= clear < leave
    finally:
        try:
            os.kill(pid, signal.SIGKILL)
        except ProcessLookupError:
            pass
        os.waitpid(pid, 0)
