"""Astronomy-aware value formatting.

A column gets a *formatter* chosen once from its name, Arrow type and unit;
the grid then calls it per cell. Heuristics (by name / unit, case-insensitive):

* MJD / JD times      → 7 decimals (~10 ms); detail view adds the UTC calendar date
* RA / Dec / lon/lat  → 6 decimals (~4 mas); detail view adds sexagesimal
* magnitudes          → 3 decimals
* fluxes              → 4 significant digits
* errors / sigmas     → 3 significant digits
* other floats        → up to 7 (float32) / 10 (float64) significant digits
* ids / integers      → verbatim (no thousands separators — ids are copied around)

A column can carry an *override*: an int (decimals for the fixed-point kinds,
significant digits for the others) or a Python format spec such as ``.2e``.
"""
from __future__ import annotations

import datetime as dt
import decimal
import math
import re
from typing import Any

import pyarrow as pa
from rich.text import Text

NULL = "∅"
#: Rich style for secondary text: "dim" (SGR 2 faint) or "bright_black"; set by the app.
DIM = "dim"

_MJD = re.compile(r"mjd|(^|_)jd($|_)|^jd|epoch|tai$|utc$", re.I)
_ANGLE = re.compile(r"(^|_)(ra|dec|decl|lon|lat|glon|glat|elon|elat|lambda|beta)($|_)", re.I)
_ANGLE_CAMEL = re.compile(r"^(ra|dec|decl)($|[A-Z0-9_])|(Ra|Dec|RA|DEC)$")  # raJ2000, decl, coordRa
_ERR = re.compile(r"err|sigma|unc|std|rms|cov", re.I)
_MAG = re.compile(r"mag($|[A-Z_])|^mag|Mag", re.I)
_FLUX = re.compile(r"flux", re.I)


def kind_for(name: str, typ: pa.DataType, unit: str = "") -> str:
    """Classify a column into a formatting kind."""
    u = (unit or "").strip().lower()
    if pa.types.is_floating(typ) or pa.types.is_decimal(typ):
        if _ERR.search(name):
            return "err"
        if u in ("d", "day", "days", "mjd") or _MJD.search(name):
            return "mjd"
        if u in ("deg", "degree", "degrees") or _ANGLE_CAMEL.search(name) or _ANGLE.search(name):
            return "angle"
        if u in ("mag", "mag(ab)", "abmag") or _MAG.search(name):
            return "mag"
        if u in ("njy", "jy", "mjy", "ujy") or _FLUX.search(name):
            return "flux"
        return "float32" if pa.types.is_float32(typ) or pa.types.is_float16(typ) else "float"
    if pa.types.is_integer(typ):
        return "int"
    if pa.types.is_boolean(typ):
        return "bool"
    if pa.types.is_timestamp(typ) or pa.types.is_date(typ) or pa.types.is_time(typ):
        return "time"
    if pa.types.is_binary(typ) or pa.types.is_large_binary(typ) or pa.types.is_fixed_size_binary(typ):
        return "binary"
    if pa.types.is_nested(typ):
        return "nested"
    return "str"


def _fmt_float(v: float, sig: int) -> str:
    """``sig`` significant digits, fixed-point for 1e-3 <= |v| < 1e9, else scientific."""
    if v == 0:
        return "0"
    a = abs(v)
    if 1e-3 <= a < 1e9:
        mag = math.floor(math.log10(a))
        dec = max(0, sig - 1 - mag)
        s = f"{round(v, dec) if dec else round(v, sig - 1 - mag):.{dec}f}"
        if "." in s:
            s = s.rstrip("0").rstrip(".")
        return s
    m, e = f"{v:.{max(sig - 1, 1)}e}".split("e")
    if "." in m:
        m = m.rstrip("0").rstrip(".")
    return f"{m}e{e}"


def _fixed(v: float, decimals: int) -> str:
    return f"{v:.{decimals}f}"


def shortest(v: Any, typ: pa.DataType) -> Any:
    """For float32 columns, the shortest decimal that round-trips at float32 precision."""
    if isinstance(v, float) and (pa.types.is_float32(typ) or pa.types.is_float16(typ)) and math.isfinite(v):
        import numpy as np

        return float(repr(np.float32(v)).replace("np.float32(", "").rstrip(")"))
    return v


#: Default digits per float kind: decimals for the fixed-point kinds, significant digits otherwise.
FIXED_DIGITS = {"mjd": 7, "angle": 6, "mag": 3}
SIG_DIGITS = {"flux": 4, "err": 3, "float32": 7, "float": 9}
MAX_DIGITS = 17
#: Largest width or precision a format spec may ask for (a typo like .1000000f would hang the grid).
MAX_SPEC_NUMBER = 64
_SPEC_PRECISION = re.compile(r"\.(\d+)")
# a standard format spec: [[fill]align][sign][z][#][0][width][grouping][.precision][type]
_STD_SPEC = re.compile(r"(?:.?[<>=^])?[-+ ]?z?#?0?(?P<width>\d*)[,_]?(?:\.(?P<prec>\d+))?[a-zA-Z%]?", re.S)
#: Kinds a format spec doesn't apply to: they keep their automatic rendering.
NO_SPEC_KINDS = ("bool", "binary", "nested")
_SAMPLES = {"int": 1, "str": "abc", "time": dt.datetime(2026, 1, 2, 3, 4, 5)}


def default_digits(kind: str) -> int | None:
    """The digits a float kind shows without an override; None for non-float kinds."""
    return FIXED_DIGITS.get(kind, SIG_DIGITS.get(kind))


def step_override(override: int | str | None, kind: str, delta: int) -> int | str | None:
    """One digit more (``delta`` > 0) or fewer than ``override``; None if the column has no digits to step.

    A spec with a precision (``.3e``) has that precision stepped; any other spec, or
    no override, starts from what the kind shows by default."""
    if isinstance(override, str):
        m = _SPEC_PRECISION.search(override)
        if m:
            n = max(0, min(MAX_DIGITS, int(m.group(1)) + delta))
            return override[: m.start(1)] + str(n) + override[m.end(1):]
        override = None
    cur = override if override is not None else default_digits(kind)
    if cur is None:
        return None
    return max(0 if kind in FIXED_DIGITS else 1, min(MAX_DIGITS, cur + delta))


def describe_override(override: int | str | None, kind: str) -> str:
    """Short label for a header: ``.4f`` for fixed digits, ``4 sig`` for significant ones, or the spec."""
    if override is None:
        return ""
    if isinstance(override, int):
        return f".{override}f" if kind in FIXED_DIGITS else f"{override} sig"
    return override


def override_error(value: int | str, kind: str | None = None, sample: Any = None) -> str | None:
    """Why ``value`` can't be a column's override, or None if it can.

    ``kind`` and ``sample`` (a value from the column) narrow the check; without them
    a spec only has to suit some column (a float, an int, a string or a timestamp)."""
    if isinstance(value, int):
        if kind is not None and default_digits(kind) is None:
            return "a digit count applies only to float columns; use a format spec such as ,d"
        if value > MAX_DIGITS:
            return f"at most {MAX_DIGITS} digits"
        return None
    m = _STD_SPEC.fullmatch(value)  # anything else (strftime) can't ask for huge output
    if m and any(int(n or 0) > MAX_SPEC_NUMBER for n in (m["width"], m["prec"])):
        return f"widths and precisions are limited to {MAX_SPEC_NUMBER}"
    if kind in NO_SPEC_KINDS:
        return f"{kind} columns can't take a format spec"
    if sample is not None:
        samples = [sample]
    elif kind is not None:
        samples = [_SAMPLES.get(kind, 1.5)]
    else:
        samples = [1.5, *_SAMPLES.values()]
    errors = []
    for v in samples:
        if isinstance(v, (dt.date, dt.time)) and "%" not in value:
            errors.append("timestamps take strftime codes, e.g. %Y-%m-%d %H:%M")
            continue
        try:
            format(v, value)
            return None
        except (ValueError, TypeError) as e:
            errors.append(str(e))
    return errors[0]  # the first sample is the column's own value, or a float


# ------------------------------------------------------------ control characters
# Text from a file (values, column names, metadata, the file's own name) must never reach the
# terminal with its control characters: ESC, the C1 CSI (U+009B) and friends start escape
# sequences that can retitle the window, write the clipboard (OSC 52) or redraw the screen.
# They are shown as visible stand-ins instead: C0 as the Unicode control pictures (ESC is ␛),
# DEL as ␡, C1 as a \x9b-style escape.
def _control_repr(c: int) -> str:
    if c < 0x20:
        return chr(0x2400 + c)
    if c == 0x7F:
        return "␡"
    return f"\\x{c:02x}"


CONTROL_CHARS = frozenset([*range(0x20), *range(0x7F, 0xA0)])
# Bidi controls (embeddings, overrides, isolates, marks) reorder what's shown around them, and
# zero-width characters hide in it: shown as ⟨U+202E⟩ and the like. (All are format characters,
# so str.isprintable() is False for them too: the fast path below still catches every one.)
INVISIBLE_CHARS = frozenset([*range(0x202A, 0x202F), *range(0x2066, 0x206A), 0x200E, 0x200F, 0x061C,
                             *range(0x200B, 0x200E), 0x2060, 0xFEFF])
_CONTROLS = {c: _control_repr(c) for c in CONTROL_CHARS} | {c: f"⟨U+{c:04X}⟩" for c in INVISIBLE_CHARS}
_CONTROLS_BUT_WS = {c: r for c, r in _CONTROLS.items() if c not in (0x09, 0x0A)}


def sanitize(s: str, keep_ws: bool = False) -> str:
    """``s`` with its control characters (C0, DEL, C1) replaced by visible stand-ins.

    ``keep_ws`` keeps tab and newline, which Rich lays out itself (for values shown
    on several lines). Cheap for the usual string: one C-level check, no copy."""
    if s.isprintable():
        return s
    return s.translate(_CONTROLS_BUT_WS if keep_ws else _CONTROLS)


def has_controls(s: str, keep_ws: bool = False) -> bool:
    """Whether ``sanitize(s, keep_ws)`` would change ``s``."""
    return not s.isprintable() and s != s.translate(_CONTROLS_BUT_WS if keep_ws else _CONTROLS)


def format_value(v: Any, kind: str, *, raw: bool = False, width: int = 40,
                 override: int | str | None = None, safe: bool = True) -> str:
    """Plain-text rendering of one value.

    Text comes out ``sanitize``-d (tab and newline kept), so it is safe to show;
    ``safe=False`` leaves its control characters in."""
    if v is None:
        return NULL
    if isinstance(v, float):
        if math.isnan(v):
            return "NaN"
        if math.isinf(v):
            return "∞" if v > 0 else "-∞"
        if raw:
            return repr(v)
    if isinstance(override, str) and not raw and kind not in NO_SPEC_KINDS:
        try:
            s = format(v, override)
        except (ValueError, TypeError):
            pass  # a spec that doesn't fit this value: fall back to the automatic format
        else:
            if safe and not s.isprintable():
                s = sanitize(s, keep_ws=True)
            if width and len(s) > width and kind not in ("int", "float", "float32", *FIXED_DIGITS, *SIG_DIGITS):
                s = s[: width - 1] + "…"
            return s
    if isinstance(v, decimal.Decimal) and isinstance(override, int) and not raw and v.is_finite():
        v = float(v)  # digits for a decimal column: render it like any other float
    if isinstance(v, float):
        digits = min(override, MAX_DIGITS) if isinstance(override, int) else None
        if kind in FIXED_DIGITS:
            return _fixed(v, FIXED_DIGITS[kind] if digits is None else digits)
        return _fmt_float(v, SIG_DIGITS.get(kind, 9) if digits is None else max(1, digits))
    if isinstance(v, bool):
        return "✓" if v else "·" if not raw else str(v)
    if isinstance(v, int):
        return str(v)
    if isinstance(v, dt.datetime):
        s = v.isoformat(sep=" ")
        return s.replace("+00:00", "Z")
    if isinstance(v, (dt.date, dt.time)):
        return v.isoformat()
    if isinstance(v, (bytes, bytearray, memoryview)):
        b = bytes(v)
        s = b[:16].hex()
        return f"0x{s}{'…' if len(b) > 16 else ''} ({len(b)} B)"
    if isinstance(v, dict):
        s = "{" + ", ".join(f"{k}: {format_value(x, _guess_kind(x), raw=raw, safe=safe)}"
                            for k, x in v.items()) + "}"  # (keys are sanitized with the whole, below)
    elif isinstance(v, (list, tuple)):
        suffix = f" ({len(v)})" if len(v) > 3 else ""
        budget = (width - len(suffix) - 3) if (width and not raw) else 10**9
        items, used = [], 0
        for x in v:
            it = format_value(x, _guess_kind(x), raw=raw, width=0, safe=safe)
            if used + len(it) + 2 > budget:
                items.append("…")
                break
            items.append(it)
            used += len(it) + 2
        return f"[{', '.join(items)}]{suffix}"
    else:
        s = str(v)
    if safe and not s.isprintable():  # (the usual string is printable: one C-level check)
        s = sanitize(s, keep_ws=True)
    if not raw and width and len(s) > width:
        s = s[: width - 1] + "…"
    return s


def _guess_kind(v: Any) -> str:
    if isinstance(v, float):
        return "float"
    return "str"


class CellFormatter:
    """Per-column formatter producing Rich ``Text`` for the grid."""

    def __init__(self, name: str, typ: pa.DataType, unit: str = "", override: int | str | None = None):
        self.name = name
        self.type = typ
        self.kind = kind_for(name, typ, unit)
        self.right = self.kind in ("mjd", "angle", "mag", "flux", "err", "float", "float32", "int")
        self.override = override

    def plain(self, v: Any, raw: bool = False) -> str:
        """The cell's text, without styling."""
        return format_value(v, self.kind, raw=raw, override=self.override)

    def __call__(self, v: Any, raw: bool = False) -> Text:
        s = format_value(v, self.kind, raw=raw, override=self.override)
        justify = "right" if self.right else "left"
        if v is None:
            return Text(s, style=DIM, justify=justify)
        if isinstance(v, float) and (math.isnan(v) or math.isinf(v)):
            return Text(s, style=DIM, justify=justify)
        if self.kind == "bool":
            return Text(s, style="bold" if v else DIM, justify="center")
        return Text(s, justify=justify)


# ------------------------------------------------------------ derived values
MJD_EPOCH = dt.datetime(1858, 11, 17, tzinfo=dt.timezone.utc)


def mjd_to_iso(mjd: float) -> str:
    try:
        t = MJD_EPOCH + dt.timedelta(days=float(mjd))
    except (OverflowError, ValueError):
        return ""
    return t.strftime("%Y-%m-%d %H:%M:%S.") + f"{t.microsecond // 1000:03d}"


def deg_to_hms(deg: float) -> str:
    h = (float(deg) % 360.0) / 15.0
    hh = int(h)
    m = (h - hh) * 60
    mm = int(m)
    ss = (m - mm) * 60
    if ss >= 59.9995:
        ss, mm = 0.0, mm + 1
    if mm >= 60:
        mm, hh = 0, (hh + 1) % 24
    return f"{hh:02d}h{mm:02d}m{ss:06.3f}s"


def deg_to_dms(deg: float) -> str:
    sign = "-" if deg < 0 else "+"
    a = abs(float(deg))
    d = int(a)
    m = (a - d) * 60
    mm = int(m)
    ss = (m - mm) * 60
    if ss >= 59.995:
        ss, mm = 0.0, mm + 1
    if mm >= 60:
        mm, d = 0, d + 1
    return f"{sign}{d:02d}°{mm:02d}′{ss:05.2f}″"


def derived(name: str, kind: str, v: Any) -> str:
    """Extra human reading of a value for the detail panel ('' when none)."""
    if v is None or not isinstance(v, (int, float)) or isinstance(v, bool):
        return ""
    if isinstance(v, float) and not math.isfinite(v):
        return ""
    low = name.lower()
    if kind == "mjd":
        if "jd" in low and "mjd" not in low and v > 2_400_000:
            return mjd_to_iso(v - 2_400_000.5) + " (from JD)"
        if 0 < v < 200_000:
            return mjd_to_iso(v)
    if kind == "angle":
        if re.search(r"(^|_)ra|^ra|Ra$|lon", name) and not re.search(r"dec", low):
            return deg_to_hms(v)
        if -90 <= v <= 90:
            return deg_to_dms(v)
    if kind == "err" and re.search(r"(ra|dec)", low) and abs(v) < 1:
        return f"{v * 3.6e6:.3g} mas"
    if kind == "flux" and v > 0 and "err" not in low:
        return f"{-2.5 * math.log10(v) + 31.4:.3f} AB mag (if nJy)"
    return ""


def percent(part: float, whole: float) -> str:
    """``part`` as a percentage of ``whole``, never misleading at the ends.

    0 and 100 are exact ("0%", "100%"); a non-zero share never rounds to 0 or
    100 ("<0.01%", ">99.99%"); otherwise 3 significant digits ("4.7%", "70.1%"),
    or 2 decimals near the top so 99.95% doesn't read as 100%."""
    if not whole:
        return ""
    if part <= 0:
        return "0%"
    if part >= whole:
        return "100%"
    p = 100.0 * part / whole
    if p < 0.01:
        return "<0.01%"
    if p > 99.99:
        return ">99.99%"
    if p >= 99.5:
        return f"{p:.2f}%"
    return f"{p:.3g}%"


def human_count(n: int | float | None) -> str:
    if n is None:
        return "?"
    n = int(n)
    for div, suffix in ((1_000_000_000_000, "T"), (1_000_000_000, "B"), (1_000_000, "M"), (1_000, "k")):
        if abs(n) >= div:
            return f"{n / div:.3g}{suffix}"
    return f"{n:,}"


def human_bytes(n: int | float | None) -> str:
    if n is None:
        return "?"
    n = float(n)
    for unit in ("B", "KiB", "MiB", "GiB", "TiB"):
        if abs(n) < 1024 or unit == "TiB":
            return f"{n:.0f} {unit}" if unit == "B" else f"{n:.1f} {unit}"
        n /= 1024
    return f"{n:.1f} TiB"


def short_type(t: pa.DataType) -> str:
    """Compact Arrow type name for column headers."""
    if pa.types.is_dictionary(t):
        return f"dict<{short_type(t.value_type)}>"
    if pa.types.is_timestamp(t):
        return f"ts[{t.unit}{',' + t.tz if t.tz else ''}]"
    s = sanitize(str(t))  # a nested type names its fields
    return {"double": "f64", "float": "f32", "halffloat": "f16", "int64": "i64", "int32": "i32",
            "int16": "i16", "int8": "i8", "uint64": "u64", "uint32": "u32", "uint16": "u16",
            "uint8": "u8", "string": "str", "large_string": "str", "bool": "bool"}.get(s, s)
