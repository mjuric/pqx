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
