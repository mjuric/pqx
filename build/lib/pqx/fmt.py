"""Astronomy-aware value formatting.

A column gets a *formatter* chosen once from its name, Arrow type and unit;
the grid then calls it per cell. Heuristics (by name / unit, case-insensitive):

* MJD / JD times      → 5 decimals (~1 s); detail view adds the UTC calendar date
* RA / Dec / lon/lat  → 6 decimals (~4 mas); detail view adds sexagesimal
* magnitudes          → 3 decimals
* fluxes              → 4 significant digits
* errors / sigmas     → 3 significant digits
* other floats        → up to 7 (float32) / 10 (float64) significant digits
* ids / integers      → verbatim (no thousands separators — ids are copied around)
"""
from __future__ import annotations

import datetime as dt
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


def format_value(v: Any, kind: str, *, raw: bool = False, width: int = 40) -> str:
    """Plain-text rendering of one value."""
    if v is None:
        return NULL
    if isinstance(v, float):
        if math.isnan(v):
            return "NaN"
        if math.isinf(v):
            return "∞" if v > 0 else "-∞"
        if raw:
            return repr(v)
        if kind == "mjd":
            return _fixed(v, 5)
        if kind == "angle":
            return _fixed(v, 6)
        if kind == "mag":
            return _fixed(v, 3)
        if kind == "flux":
            return _fmt_float(v, 4)
        if kind == "err":
            return _fmt_float(v, 3)
        if kind == "float32":
            return _fmt_float(v, 7)
        return _fmt_float(v, 9)
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
        s = "{" + ", ".join(f"{k}: {format_value(x, _guess_kind(x), raw=raw)}" for k, x in v.items()) + "}"
    elif isinstance(v, (list, tuple)):
        suffix = f" ({len(v)})" if len(v) > 3 else ""
        budget = (width - len(suffix) - 3) if (width and not raw) else 10**9
        items, used = [], 0
        for x in v:
            it = format_value(x, _guess_kind(x), raw=raw, width=0)
            if used + len(it) + 2 > budget:
                items.append("…")
                break
            items.append(it)
            used += len(it) + 2
        return f"[{', '.join(items)}]{suffix}"
    else:
        s = str(v)
    if not raw and width and len(s) > width:
        s = s[: width - 1] + "…"
    return s


def _guess_kind(v: Any) -> str:
    if isinstance(v, float):
        return "float"
    return "str"


class CellFormatter:
    """Per-column formatter producing Rich ``Text`` for the grid."""

    def __init__(self, name: str, typ: pa.DataType, unit: str = ""):
        self.name = name
        self.type = typ
        self.kind = kind_for(name, typ, unit)
        self.right = self.kind in ("mjd", "angle", "mag", "flux", "err", "float", "float32", "int")

    def __call__(self, v: Any, raw: bool = False) -> Text:
        s = format_value(v, self.kind, raw=raw)
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
    s = str(t)
    return {"double": "f64", "float": "f32", "halffloat": "f16", "int64": "i64", "int32": "i32",
            "int16": "i16", "int8": "i8", "uint64": "u64", "uint32": "u32", "uint16": "u16",
            "uint8": "u8", "string": "str", "large_string": "str", "bool": "bool"}.get(s, s)
