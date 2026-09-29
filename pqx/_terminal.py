"""Work-arounds for terminals and multiplexers that speak older mouse protocols.

pqx asks for SGR mouse reporting (mode 1006), like every Textual app. Some
multiplexers (GNU screen 4.x in particular) don't support SGR and hand the app
X10-encoded events instead -- ``ESC [ M`` followed by three raw bytes -- or
urxvt-encoded ones (``ESC [ b;x;y M``). Textual (as of 8.x) has two problems
with that:

* it only parses SGR mouse events, so X10/urxvt clicks are silently dropped;
* it decodes terminal input as strict UTF-8, and an X10 coordinate above 95
  is a byte > 127 that is not valid UTF-8: the input thread dies with
  ``UnicodeDecodeError`` while the app keeps drawing -- it looks frozen.

:func:`install` makes input decoding lenient (an invalid byte becomes the
character with that code, which is exactly what an X10 coordinate means) and
translates X10/urxvt mouse events into SGR before Textual parses them.
"""
from __future__ import annotations

import codecs
import functools
import re

_ERRORS = "pqx-bytes"
_X10 = re.compile(r"\x1b\[M(.)(.)(.)\Z", re.S)
_URXVT = re.compile(r"\x1b\[(\d+);(\d+);(\d+)M\Z")
_installed = False


def _bytes_as_chars(err: UnicodeDecodeError):
    return "".join(chr(b) for b in err.object[err.start:err.end]), err.end


def x10_to_sgr(code: str, last_button: int = 0) -> tuple[str, int] | None:
    """Translate an X10 or urxvt mouse sequence to SGR. Returns (sgr, last_button)."""
    m = _X10.match(code)
    if m:
        b, x, y = (ord(c) - 32 for c in m.groups())
    else:
        m = _URXVT.match(code)
        if not m:
            return None
        b, x, y = int(m.group(1)) - 32, int(m.group(2)), int(m.group(3))
    if x < 1 or y < 1:
        return None
    if b & 64 or b & 32:  # wheel, or motion (with or without a button held)
        return f"\x1b[<{b};{x};{y}M", last_button
    if b & 3 == 3:  # X10 release doesn't say which button: use the one pressed last
        return f"\x1b[<{last_button | (b & ~3)};{x};{y}m", last_button
    return f"\x1b[<{b};{x};{y}M", b & 3


def install() -> None:
    """Patch Textual's input path (idempotent)."""
    global _installed
    if _installed:
        return
    _installed = True
    try:
        codecs.lookup_error(_ERRORS)
    except LookupError:
        codecs.register_error(_ERRORS, _bytes_as_chars)

    try:
        from textual.drivers import linux_driver

        real = codecs.getincrementaldecoder

        def lenient(encoding: str):
            return functools.partial(real(encoding), errors=_ERRORS)

        linux_driver.getincrementaldecoder = lenient
    except Exception:  # pragma: no cover - other platforms / Textual versions
        pass

    try:
        from textual._xterm_parser import XTermParser

        original = XTermParser.parse_mouse_code

        @functools.wraps(original)
        def parse_mouse_code(self, code: str):
            translated = x10_to_sgr(code, getattr(self, "_pqx_last_button", 0))
            if translated is not None:
                code, self._pqx_last_button = translated
            return original(self, code)

        XTermParser.parse_mouse_code = parse_mouse_code
    except Exception:  # pragma: no cover
        pass
