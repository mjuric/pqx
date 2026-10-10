"""Write the golden files in go/testdata/golden/ from Python pqx, the reference implementation.

    cd <repo root>
    PYTHONPATH=$PWD <python with pqx's dependencies> go/testdata/golden/make_golden.py [OUTDIR]

It runs pqx's ``fmt``, ``cells``, ``plots`` and ``data`` on fixed inputs and on the fixtures
in ../fixtures/ (written by ../make_fixtures.py) and writes one JSON file per area. The Go
tests compare their outputs against these. The value encoding is in README.md.

Deterministic: every input is fixed or seeded, DuckDB runs with one thread, and the JSON is
written in a fixed order, so a second run writes the same bytes (with the same versions).
"""
from __future__ import annotations

import datetime as dt
import decimal
import json
import math
import os
import subprocess
import sys
import uuid

import numpy as np
import pyarrow as pa
from rich.text import Text

from pqx import cells as C
from pqx import fmt as F
from pqx import plots as P
from pqx import data as D
from pqx.data import ParquetDataset, View

HERE = os.path.dirname(os.path.abspath(__file__))
FIXTURES = os.path.normpath(os.path.join(HERE, "..", "fixtures"))
REPO = os.path.normpath(os.path.join(HERE, "..", "..", ".."))
sys.path.insert(0, os.path.dirname(HERE))
import make_fixtures as MF  # noqa: E402

#: DuckDB threads for the data goldens: one, so approximate aggregates come out the same each run
THREADS = int(os.environ.get("PQX_GOLDEN_THREADS", "1"))


# ====================================================================== encoding
def frepr(x: float) -> str:
    x = float(x)
    if math.isnan(x):
        return "nan"
    if math.isinf(x):
        return "inf" if x > 0 else "-inf"
    return repr(x)


def _unwrap(t):
    if t is None:
        return None
    if isinstance(t, pa.BaseExtensionType):
        t = t.storage_type
    if pa.types.is_dictionary(t):
        t = t.value_type
    return t


def _iso(y, mo, d, h, mi, s, ns) -> str:
    return f"{y:04d}-{mo:02d}-{d:02d}T{h:02d}:{mi:02d}:{s:02d}.{ns:09d}"


def enc(v, t: pa.DataType | None = None):
    """The typed encoding of a Python value (``t``: the Arrow type it came from, if known)."""
    t = _unwrap(t)
    if v is None:
        return {"t": "null"}
    if isinstance(v, (bool, np.bool_)):
        return {"t": "bool", "v": bool(v)}
    if isinstance(v, (int, np.integer)):
        if t is not None and pa.types.is_unsigned_integer(t) or int(v) > 2**63 - 1:
            return {"t": "uint", "v": str(int(v))}
        return {"t": "int", "v": str(int(v))}
    if isinstance(v, (float, np.floating)):
        f32 = isinstance(v, np.float32) or (t is not None and (pa.types.is_float32(t) or pa.types.is_float16(t)))
        return {"t": "f32" if f32 else "f64", "v": frepr(v)}
    if isinstance(v, str):
        return {"t": "str", "v": v}
    if isinstance(v, (bytes, bytearray, memoryview)):
        return {"t": "bytes", "v": bytes(v).hex()}
    if isinstance(v, decimal.Decimal):
        if not v.is_finite():
            raise TypeError(f"non-finite decimal {v}")
        sign, digits, exp = v.as_tuple()
        unscaled = int("".join(map(str, digits)) or "0")
        if exp > 0:
            unscaled *= 10**exp
        scale = max(0, -exp)
        if sign:
            unscaled = -unscaled
        if t is not None and pa.types.is_decimal(t):
            assert t.scale == scale, (v, t)
            prec = t.precision
        else:
            prec = max(len(str(abs(unscaled))), scale, 1)
        return {"t": "dec", "v": str(unscaled), "scale": scale, "precision": prec}
    if hasattr(v, "nanosecond") and hasattr(v, "value"):  # pandas.Timestamp (ns timestamps)
        tz = None
        if v.tzinfo is not None:
            assert v.utcoffset() == dt.timedelta(0), v
            tz = "UTC"
        ns = int(v.value)
        sec, frac = divmod(ns, 10**9)
        d0 = dt.datetime(1970, 1, 1) + dt.timedelta(seconds=sec)
        unit = t.unit if t is not None and pa.types.is_timestamp(t) else "ns"
        return {"t": "ts", "v": _iso(d0.year, d0.month, d0.day, d0.hour, d0.minute, d0.second, frac),
                "unit": unit, "tz": tz}
    if isinstance(v, dt.datetime):
        tz = None
        if v.tzinfo is not None:
            if v.utcoffset() != dt.timedelta(0):
                raise TypeError(f"timestamp not in UTC: {v!r}")
            tz = "UTC"
        unit = t.unit if t is not None and pa.types.is_timestamp(t) else "us"
        return {"t": "ts", "v": _iso(v.year, v.month, v.day, v.hour, v.minute, v.second, v.microsecond * 1000),
                "unit": unit, "tz": tz}
    if isinstance(v, dt.date):
        return {"t": "date", "v": v.isoformat()}
    if isinstance(v, dt.time):
        if v.tzinfo is not None:
            raise TypeError("time with a zone")
        return {"t": "time", "v": f"{v.hour:02d}:{v.minute:02d}:{v.second:02d}.{v.microsecond * 1000:09d}"}
    if isinstance(v, dt.timedelta):
        ns = ((v.days * 86_400 + v.seconds) * 10**6 + v.microseconds) * 1000
        return {"t": "dur", "v": str(ns)}
    if isinstance(v, uuid.UUID):
        return {"t": "uuid", "v": str(v)}
    if isinstance(v, dict):
        ft = {}
        if t is not None and pa.types.is_struct(t):
            ft = {t.field(i).name: t.field(i).type for i in range(t.num_fields)}
        return {"t": "struct", "v": [[k, enc(x, ft.get(k))] for k, x in v.items()]}
    if isinstance(v, np.ndarray):
        v = v.tolist()
    if isinstance(v, (list, tuple)):
        if t is not None and pa.types.is_map(t):
            return {"t": "map", "v": [[enc(k, t.key_type), enc(x, t.item_type)] for k, x in v]}
        vt = t.value_type if t is not None and (pa.types.is_list(t) or pa.types.is_large_list(t)
                                                 or pa.types.is_fixed_size_list(t)) else None
        return {"t": "list", "v": [enc(x, vt) for x in v]}
    raise TypeError(f"can't encode {type(v).__name__}: {v!r}")


def enc_arrow(arr: pa.Array) -> list:
    """An Arrow array's values, exactly: timestamps, times and durations from their integers
    (Python's datetime and time stop at microseconds)."""
    t = arr.type
    if isinstance(t, pa.BaseExtensionType) and t.extension_name == "arrow.uuid":
        return [enc(None if b is None else uuid.UUID(bytes=b)) for b in arr.storage.to_pylist()]
    if pa.types.is_dictionary(t):
        return enc_arrow(arr.dictionary_decode())
    unit = getattr(t, "unit", None)
    if pa.types.is_timestamp(t) or pa.types.is_time(t) or pa.types.is_duration(t):
        per = {"s": 10**9, "ms": 10**6, "us": 10**3, "ns": 1}[unit]
        ints = arr.view(pa.int64() if t.bit_width == 64 else pa.int32()).to_pylist()
        out = []
        for n in ints:
            if n is None:
                out.append({"t": "null"})
                continue
            ns = n * per
            if pa.types.is_duration(t):
                out.append({"t": "dur", "v": str(ns)})
            elif pa.types.is_time(t):
                sec, frac = divmod(ns, 10**9)
                out.append({"t": "time", "v": f"{sec // 3600:02d}:{sec // 60 % 60:02d}:{sec % 60:02d}.{frac:09d}"})
            else:
                sec, frac = divmod(ns, 10**9)
                d0 = dt.datetime(1970, 1, 1) + dt.timedelta(seconds=sec)
                out.append({"t": "ts", "v": _iso(d0.year, d0.month, d0.day, d0.hour, d0.minute, d0.second, frac),
                            "unit": unit, "tz": "UTC" if t.tz else None})
        return out
    if isinstance(t, pa.BaseExtensionType):
        t = t.storage_type
        arr = arr.storage
    return [enc(v, t) for v in arr.to_pylist()]


def enc_style(s) -> str:
    if s is None:
        return ""
    return str(s) if not isinstance(s, str) else s


def enc_text(t: Text) -> dict:
    """Rich Text: plain text, base style, spans (adjacent spans of one style merged)."""
    spans: list[list] = []
    for sp in t.spans:
        st = enc_style(sp.style)
        if sp.end <= sp.start:
            continue
        if spans and spans[-1][1] == sp.start and spans[-1][2] == st:
            spans[-1][1] = sp.end
        else:
            spans.append([sp.start, sp.end, st])
    return {"text": t.plain, "style": enc_style(t.style), "spans": spans}


def err(e: BaseException) -> dict:
    return {"error": f"{type(e).__name__}: {e}"}


def run(fn, conv=lambda x: x):
    """``{"out": conv(fn())}`` or ``{"error": ...}``."""
    try:
        return {"out": conv(fn())}
    except Exception as e:  # noqa: BLE001
        return err(e)


class Section(list):
    """Records of one function; ``add`` numbers them."""

    def __init__(self, prefix: str):
        super().__init__()
        self.prefix = prefix

    def add(self, **rec):
        self.append({"id": f"{self.prefix}/{len(self)}", **rec})


def git_describe() -> str:
    try:
        return subprocess.run(["git", "-C", REPO, "describe", "--tags", "--always", "--dirty"],
                              capture_output=True, text=True, check=True).stdout.strip()
    except Exception:  # noqa: BLE001
        return "unknown"


def header(what: str) -> dict:
    from importlib.metadata import version

    import duckdb

    return {
        "file": what,
        "generator": "go/testdata/golden/make_golden.py",
        "encoding": "typed values, see README.md",
        "versions": {
            "pqx_git": git_describe(),
            "python": sys.version.split()[0],
            "pyarrow": pa.__version__,
            "duckdb": duckdb.__version__,
            "numpy": np.__version__,
            "rich": version("rich"),
            "textual": version("textual"),
        },
    }


def dump(path: str, obj: dict) -> None:
    """JSON with one record per line (readable diffs), keys in insertion order."""
    def one(x):
        return json.dumps(x, ensure_ascii=False, allow_nan=False, separators=(",", ":"))

    lines = ["{"]
    items = list(obj.items())
    for k, (name, val) in enumerate(items):
        end = "," if k < len(items) - 1 else ""
        if isinstance(val, list):
            lines.append(f"{one(name)}:[")
            for j, rec in enumerate(val):
                lines.append(one(rec) + ("," if j < len(val) - 1 else ""))
            lines.append("]" + end)
        else:
            lines.append(f"{one(name)}:{one(val)}{end}")
    lines.append("}")
    with open(path, "w", encoding="utf-8") as f:
        f.write("\n".join(lines) + "\n")


# ====================================================================== inputs
UTC = dt.timezone.utc
ESC = "\x1b"

KINDS = ["mjd", "angle", "mag", "flux", "err", "float32", "float", "int", "bool", "time", "binary", "nested", "str"]


def f32(x: float) -> np.float32:
    return np.float32(x)


#: (value, Arrow type hint for the encoding) for format_value and friends
VALUES: list[tuple] = [
    (None, None),
    # floats
    *[(float(x), None) for x in (
        0.0, -0.0, 0.1, 1 / 3, -1 / 3, 1.5, -2.5, 0.5, 2.675, 12.3456789, 60800.123456789, 28561.3, 2.14e-5,
        1e-3, 9.99e-4, 0.00099999, 999999999.0, 999999999.5, 1e9, 1e16, 1e300, -1e-300, 5e-324,
        1.7976931348623157e308, 123456789.123456789, 359.99999999, -0.0000013, -74.000439, 1e-7, 7.0,
        -123456.789, 0.000123456, math.nan, math.inf, -math.inf)],
    # float32 values (exactly representable; the grid gets them as Python floats)
    *[(float(f32(x)), pa.float32()) for x in (0.1, 1 / 3, 21.123456, -1e-7, 3.4e38, 16777217.0)],
    # ints
    *[(x, None) for x in (0, 1, -1, 7, 1234567, -1234567, 170000000000000123, -9223372036854775808,
                          9223372036854775807)],
    (18446744073709551615, pa.uint64()),
    (True, None), (False, None),
    # strings
    *[(s, None) for s in ("", "abc", "x" * 100, "日本語テキスト" * 5, "tab\there\nnew line", ESC + "]0;T\x07title",
                          "a\x9bb\x7f\x00", "abc\u202edcba", "[bold]x[/]", "e\u0301 😀", "  padded  ", "%Y",
                          "1.5")],
    # bytes
    (b"", None), (b"\x01\x02", None), (bytes(range(40)), None), (b"\x1b[2J", None),
    # times
    (dt.datetime(2026, 1, 1, tzinfo=UTC), pa.timestamp("us", "UTC")),
    (dt.datetime(2026, 1, 2, 3, 4, 5), pa.timestamp("us")),
    (dt.datetime(2026, 1, 2, 3, 4, 5, 123456, tzinfo=UTC), pa.timestamp("us", "UTC")),
    (dt.datetime(1858, 11, 17, 0, 0, 0, 1), pa.timestamp("us")),
    (dt.date(2026, 1, 2), None), (dt.date(1, 1, 1), None),
    (dt.time(3, 4, 5), None), (dt.time(3, 4, 5, 500), None), (dt.time(0, 0), None),
    # decimals
    (decimal.Decimal("1.23456"), pa.decimal128(10, 5)), (decimal.Decimal("-0.30"), pa.decimal128(9, 2)),
    (decimal.Decimal("12345678901234567890.123"), pa.decimal128(38, 3)),
    (decimal.Decimal("0E-38"), pa.decimal128(38, 38)),
    # nested
    (list(range(30)), pa.list_(pa.int64())), ([], pa.list_(pa.int64())),
    ([1.5, None, 0.1], pa.list_(pa.float64())), (["a", "b"], pa.list_(pa.string())),
    ([[1, 2], [3]], pa.list_(pa.list_(pa.int64()))), ([ESC + "[2J", "x" * 50], pa.list_(pa.string())),
    ({"a": 1, "b": "x", "c": [1.0, 2.5]}, pa.struct([("a", pa.int64()), ("b", pa.string()),
                                                    ("c", pa.list_(pa.float64()))])),
    ({"k" + ESC + "]0;K": [ESC + "[2J", {"x\x9b": "y\x9d"}]}, None),
    ({"ra": 1.0, "dec": -0.05}, pa.struct([("ra", pa.float64()), ("dec", pa.float64())])),
    ([("k0", 0), ("z", None)], pa.map_(pa.string(), pa.int64())),
]

OVERRIDES = [".2f", ".3e", ",d", ".1%", 4, 0, 2, 18, ">12", "x", "%Y-%m-%d %H:%M", "%H:%M:%S", "+.3g", "_d",
             "^10", "08.3f", "#x", ".0f", "%j", ".3s"]

HOSTILE = MF.evil_values() + [
    "abc", "", "é ✓ 漢字  ", "a\tb\nc", "a\tb\nc\x1b", "\x00\x01\x1f\x7f\x80\x9f\xa0", "\r\n", "\x0b\x0c",
    *[f"x{c}" for c in "\u202a\u202b\u202c\u202d\u202e\u2066\u2067\u2068\u2069\u200e\u200f\u061c\u200b\u200c"
      "\u200d\u2060\ufeff"],
    "\u2028line sep", "\u00ad soft hyphen", "\u2029", "\ue000 private", "\U000e0001 tag",
]

TYPE_SAMPLES = [
    pa.float64(), pa.float32(), pa.float16(), pa.decimal128(10, 2), pa.decimal256(50, 2), pa.int64(), pa.int32(),
    pa.int16(), pa.int8(), pa.uint8(), pa.uint16(), pa.uint32(), pa.uint64(), pa.bool_(), pa.string(),
    pa.large_string(), pa.binary(), pa.large_binary(), pa.binary(4), pa.date32(), pa.date64(),
    pa.time32("s"), pa.time32("ms"), pa.time64("us"), pa.time64("ns"), pa.timestamp("s"), pa.timestamp("ms"),
    pa.timestamp("us"), pa.timestamp("ns"), pa.timestamp("us", "UTC"), pa.timestamp("ms", "+02:00"),
    pa.timestamp("ns", "America/New_York"), pa.duration("ms"), pa.list_(pa.int64()), pa.large_list(pa.float64()),
    pa.list_(pa.int32(), 2), pa.struct([("a", pa.int64()), ("b", pa.string())]),
    pa.struct([pa.field("f" + ESC + "]0;T", pa.int32())]), pa.map_(pa.string(), pa.int64()),
    pa.dictionary(pa.int32(), pa.string()), pa.dictionary(pa.int8(), pa.float64()),
    pa.dictionary(pa.int32(), pa.timestamp("ms", "UTC")), pa.null(),
    pa.list_(pa.struct([("k", pa.string()), ("v", pa.int16())])),
]

NAMES = [
    "midpointMjdTai", "mjd", "MJD", "jd", "JD", "jd_utc", "expMidptMJD", "epoch", "obs_tai", "time_utc", "tai",
    "ra", "RA", "Ra", "dec", "DEC", "decl", "raJ2000", "decJ2000", "coord_ra", "coord_dec", "coordRa", "coordDec",
    "ra_deg", "ra_icrs", "RAJ2000", "DEJ2000", "RA_ICRS", "DE_ICRS", "DE", "RAB1950", "lon", "lat", "glon", "glat",
    "elon", "elat", "lambda", "beta", "longitude", "latitude", "pickup_lon", "pickup_lat", "pickup_longitude",
    "pickup_latitude", "raErr", "decErr", "ra_dec_Cov", "sigma_x", "x_unc", "std", "rms", "psfFlux",
    "psfFluxErr", "apFlux", "mag", "gMag", "g_mag", "magnitude", "imag", "MagAB", "radius", "RATIO", "RANGE",
    "DEPTH", "altitude", "alpha", "x", "value", "snr", "id", "diaSourceId", "band", "rate", "decimal",
    "declination", "Dec2", "raw", "rad",
]
UNITS = ["", "deg", "Degrees ", "d", "day", "MJD", "mag", "mag(AB)", "nJy", "Jy", "arcsec"]


# ====================================================================== fmt.json
def gen_fmt() -> dict:
    out: dict = {"header": header("fmt.json")}
    fx = fixture_columns()

    s = Section("kind_for")
    for name, typ, unit, src in fx:
        s.add(name=name, type=str(typ), unit=unit, source=src, **run(lambda: F.kind_for(name, typ, unit)))
    for name in NAMES:
        for typ in (pa.float64(), pa.float32()):
            for unit in UNITS:
                s.add(name=name, type=str(typ), unit=unit, **run(lambda: F.kind_for(name, typ, unit)))
        for typ in TYPE_SAMPLES:
            if typ in (pa.float64(), pa.float32()):
                continue
            s.add(name=name, type=str(typ), unit="", **run(lambda: F.kind_for(name, typ, "")))
    out["kind_for"] = s

    # One record per (value, kind); "cases" are [raw, width, override, output] (output a string, or
    # {"error": ...} if format_value raised).
    s = Section("format_value")
    grid = [(raw, width, None) for raw in (False, True) for width in (0, 8, 40)]
    grid += [(False, 40, o) for o in OVERRIDES]
    grid += [(True, 40, o) for o in (".2f", 4, "%Y")] + [(False, 8, o) for o in (".2f", 4, "%Y", ">12")]
    grid += [(False, 0, o) for o in ("<200", 4)]
    for v, hint in VALUES:
        ev = enc(v, hint)
        for kind in KINDS:
            cases = []
            for raw, width, o in grid:
                r = run(lambda: F.format_value(v, kind, raw=raw, width=width, override=o))
                cases.append([raw, width, o, r["out"] if "out" in r else {"error": r["error"]}])
            s.add(v=ev, kind=kind, cases=cases)
    out["format_value"] = s

    s = Section("format_value_unsafe")
    for v in HOSTILE[:12]:
        for raw in (False, True):
            s.add(v=enc(v), kind="str", raw=raw, width=0, override=None, safe=False,
                  **run(lambda: F.format_value(v, "str", raw=raw, width=0, safe=False)))
    out["format_value_unsafe"] = s

    s = Section("derived")
    dvals = [None, 0.0, -0.0, -1e-9, -0.0000013, 12.0, -30.5, 45.25, 89.999999999, 120.0, 180.0, 285.5,
             359.999999999, 359.99999999, 360.0, 360.0000001, -170.25, -74.000439, 5000.0, 1e20, 60676.0, 60676.5,
             199999.0, 200000.0, 2460676.5, 2400000.0, 1e-6, 0.5, 0.99, 28561.3, -5.0, math.nan, math.inf, 7, True,
             "12.5"]
    for name in NAMES:
        for unit in ("", "deg"):
            kind = F.kind_for(name, pa.float64(), unit)
            for v in dvals:
                s.add(name=name, kind=kind, v=enc(v), unit=unit, **run(lambda: F.derived(name, kind, v, unit)))
    for kind in ("angle", "mjd", "err", "flux"):  # a kind its name wouldn't get
        for v in (0.5, 180.0, -30.5, 60676.0):
            s.add(name="x", kind=kind, v=enc(v), unit="", **run(lambda: F.derived("x", kind, v, "")))
    out["derived"] = s

    s = Section("mjd_to_iso")
    for v in (0.0, 60676.0, 60676.5, 60676.99999999, 60676.9999999999, -1.0, 51544.5, 1e10, -1e10, 2973483.0,
              math.nan, math.inf, 0.0005, 0.0004999, 60800.123456789):
        s.add(v=enc(v), **run(lambda: F.mjd_to_iso(v)))
    out["mjd_to_iso"] = s

    s = Section("deg_to_hms")
    for v in (0.0, -0.0, 180.0, 359.99999999, 359.9999999, 359.999997, 359.9999979, -15.0, 720.5, 1e-9, -1e-9,
              15.0000041666, 0.004166666, 0.0041666, 14.99999999, 1e12, 123.456789):
        s.add(v=enc(v), **run(lambda: F.deg_to_hms(v)))
    out["deg_to_hms"] = s

    s = Section("deg_to_dms")
    for v in (-0.000002, -0.0000013, 10.999999999, -1e-9, 359.999999999, 359.99999, 360.0, -0.0, 0.0,
              89.99999999, -89.999999, 1e6, -170.25, 0.0000013888, 0.000001388888, -30.5, 45.25, 0.00000138889):
        for plus in (True, False):
            s.add(v=enc(v), plus=plus, **run(lambda: F.deg_to_dms(v, plus)))
    out["deg_to_dms"] = s

    step_ovs = [None, 0, 1, 4, 16, 17, ".3e", ".0f", ".17g", ".20f", ",d", ">12", "%Y", "x.5y.7"]
    s = Section("step_override")
    for o in step_ovs:
        for kind in KINDS:
            for delta in (-1, 1):
                s.add(override=o, kind=kind, delta=delta, **run(lambda: F.step_override(o, kind, delta)))
    out["step_override"] = s

    s = Section("describe_override")
    for o in step_ovs:
        for kind in KINDS:
            s.add(override=o, kind=kind, **run(lambda: F.describe_override(o, kind)))
    out["describe_override"] = s

    s = Section("override_error")
    specs = [".2f", ",d", ".3e", ".1%", ">12", "x", "%Y-%m", "%H:%M:%S", ".100000000f", "100000d", "64d", "65d",
             ".64f", ".65f", ".2q", "%Y-2026 (UTC+0100)", ".100%", "", " ", "z.2f", "#x", "=+10.3f", "s", "c",
             "<200", ".2s", "q", 4, 18, 17, 0]
    for o in specs:
        for kind in [None, *KINDS]:
            s.add(value=o, kind=kind, sample=None, **run(lambda: F.override_error(o, kind)))
    for o in (".2f", ",d", "%Y", ">8", ".3s", "x"):
        for sample, kind in ((1.5, "float"), (3, "int"), ("abc", "str"), (dt.datetime(2026, 1, 2), "time"),
                             (dt.date(2026, 1, 2), "time"), (dt.time(1, 2), "time"), (decimal.Decimal("1.5"), "float")):
            s.add(value=o, kind=kind, sample=enc(sample), **run(lambda: F.override_error(o, kind, sample)))
    out["override_error"] = s

    s = Section("default_digits")
    for kind in KINDS:
        s.add(kind=kind, **run(lambda: F.default_digits(kind)))
    out["default_digits"] = s

    s = Section("percent")
    for part, whole in ((1_290_773, 1_290_773), (0, 10), (350_100, 500_000), (1, 10**6), (999_999, 10**6),
                        (9_995, 10_000), (21, 20_000), (5, 0), (-1, 10), (11, 10), (1, 3), (2, 3), (995, 1000),
                        (9_949, 10_000), (1, 10_000), (1, 9_999), (0.5, 1.0), (123, 1000), (1, 1000)):
        s.add(part=part, whole=whole, **run(lambda: F.percent(part, whole)))
    out["percent"] = s

    s = Section("human_count")
    for n in (None, 0, 7, 999, 1000, 1499, 1500, 9999, 999_999, 1_000_000, 4_213_882_112, -5000, 1.5e12, 12345.6,
              10**15, 999_500, 999_499):
        s.add(n=n, **run(lambda: F.human_count(n)))
    out["human_count"] = s

    s = Section("human_bytes")
    for n in (None, 0, 1, 1023, 1024, 1536, 35.4 * 1024**2, 1e15, 2**50 * 5, -2048, 0.4, 1023.6, 1048575, 2**60):
        s.add(n=n, **run(lambda: F.human_bytes(n)))
    out["human_bytes"] = s

    s = Section("short_type")
    seen = set()
    for typ in TYPE_SAMPLES + [t for _, t, _, _ in fx]:
        if str(typ) in seen:
            continue
        seen.add(str(typ))
        s.add(type=str(typ), **run(lambda: F.short_type(typ)))
    out["short_type"] = s

    s = Section("sanitize")
    for v in HOSTILE:
        for keep in (False, True):
            s.add(s=v, keep_ws=keep, out=F.sanitize(v, keep), has_controls=F.has_controls(v, keep))
    out["sanitize"] = s

    s = Section("cell_formatter")  # CellFormatter(name, type, unit, override)(v, raw) on each fixture's first rows
    for path, ds in datasets():
        page = ds.fetch(View(), 0, 3)
        for j, c in enumerate(page.columns):
            info = ds.column(c)
            for o in (None, 2, ".3e"):
                cf = F.CellFormatter(c, info.arrow_type, info.unit, o)
                cases = []
                for r in range(len(page.rows)):
                    v = page.rows[r][j]
                    for raw in (False, True):
                        try:
                            t = cf(v, raw)
                            assert t.plain == cf.plain(v, raw) and not t.spans
                            cases.append({"v": enc(v, page.types[j]), "raw": raw, "plain": t.plain,
                                          "style": enc_style(t.style), "justify": t.justify})
                        except Exception as e:  # noqa: BLE001
                            cases.append({"v": enc(v, page.types[j]), "raw": raw} | err(e))
                s.add(fixture=os.path.basename(path), name=c, type=str(info.arrow_type), unit=info.unit, override=o,
                      kind=cf.kind, right=cf.right, cases=cases)
    out["cell_formatter"] = s
    return out


# ====================================================================== cells.json
WIDTH_STRINGS = [
    "", "abc", "a b c", "日本語", "ＡＢ", "ｶﾀｶﾅ", "😀", "👍🏽", "🇺🇸", "👨\u200d👩\u200d👧", "e\u0301", "\u0301",
    "\u1100\u1161\u11a8", "한국어", "a\tb", "\tx", "abcdefgh\tx", "abcdefg\tx", "ab\ncdef", "ab\n日本語\nx",
    "\x1b[31m", "␛]0;x␇", "±·─", "∅", "…", "✓", "✗", "❤️", "❤", "☺", "\u200b", "\u00ad", "x" * 100,
    "tab\there\nnew line", "é ✓ 漢字 😀 e\u0301", "\u2028", "Ω", "ß", "ﬁ", "\U0001F600\U0001F600", "a\u0300\u0301b",
    "\u3000", "〜", "가", "\u0e01\u0e34", "٣", "\u0600", "\U000e0001", "\ufe0f", "a\ufe0fb", "#\ufe0f\u20e3",
]


def gen_cells() -> dict:
    out: dict = {"header": header("cells.json")}
    s = Section("text_width")
    for x in WIDTH_STRINGS + HOSTILE + [F.sanitize(h) for h in HOSTILE]:
        s.add(s=x, width=C.text_width(x), one_cell_per_char=C.one_cell_per_char(x))
    out["text_width"] = s

    s = Section("text_width_formatted")  # what the grid measures: a value formatted for its column
    for v, hint in VALUES:
        for kind in KINDS:
            for raw in (False, True):
                txt = F.format_value(v, kind, raw=raw)
                s.add(v=enc(v, hint), kind=kind, raw=raw, text=txt, width=C.text_width(txt))
    out["text_width_formatted"] = s

    s = Section("widest_candidates")
    utc = UTC
    fixed = [
        (["a", "abcd", None, "ab"], "str", False, 3),
        (["abcd", "日本語"], "str", False, 1),
        ([dt.datetime(2026, 1, 1), dt.datetime(2026, 1, 1, 0, 0, 0, 5, tzinfo=utc), dt.datetime(2026, 1, 2)],
         "time", False, 1),
        ([b"x"], "binary", False, 3),
        ([None, math.nan], "float", False, 3),
        ([], "float", False, 3),
        ([1, 2, 3], "nested", False, 3),
        ([1.5, "x"], "float", False, 3),
        ([3, -7, 100, 0, None], "int", False, 3),
        ([3, -7, 100, 0, None], "int", True, 3),
        ([0.1, 1 / 3, 1e-7, -2.5, math.inf, -math.inf], "float", True, 3),
        ([True, False], "bool", False, 3),
        (["x", "yy", "zzz", "日", "ab"], "str", False, 2),
        ([dt.date(2026, 1, 2), dt.date(1, 1, 1)], "time", False, 3),
    ]
    rng = np.random.default_rng(3)
    for kind in ("float", "mag", "int", "float32", "angle", "err", "flux", "mjd"):
        for scale in (1e-4, 1e-2, 1, 1e3, 1e8):
            v = rng.normal(scale=scale, size=40)
            vals = [int(x) for x in v] if kind == "int" else [float(x) for x in v]
            vals[5] = None
            if kind != "int":
                vals[7] = math.nan
            for raw in (False, True):
                fixed.append((vals, kind, raw, 3))
    for k in (1, 2, 5):
        fixed.append(([float(x) for x in rng.normal(scale=10, size=30)], "float", False, k))
        fixed.append(([f"s{'x' * int(x)}" for x in rng.integers(0, 20, 30)], "str", False, k))
    for vals, kind, raw, k in fixed:
        def conv(r):
            return None if r is None else [enc(x) for x in r]
        s.add(values=[enc(x) for x in vals], kind=kind, raw=raw, k=k,
              **run(lambda: C.widest_candidates(vals, kind, raw, k), conv))
    out["widest_candidates"] = s
    return out


# ====================================================================== plots.json
def gen_plots() -> dict:
    out: dict = {"header": header("plots.json")}
    fracs = [-0.5, 0.0, 0.05, 0.1, 0.2, 0.25, 1 / 3, 0.4, 0.5, 0.6, 2 / 3, 0.75, 0.8, 0.9, 0.99, 1.0, 1.5]

    s = Section("cmap_color")
    for cm in P.COLORMAPS:
        for dark in (True, False):
            for f in fracs:
                s.add(cmap=cm, frac=f, dark_bg=dark, **run(lambda: P.cmap_color(cm, f, dark)))
    out["cmap_color"] = s

    s = Section("xterm256")
    hexes = sorted({r["out"] for r in out["cmap_color"] if "out" in r} | {
        "#000000", "#ffffff", "#d7af5f", "#808080", "#080808", "#eeeeee", "#5f87af", "#123456", "#7f7f7f",
        "#767676", "#8a8a8a", "#ff0000", "#00ff00", "#0000ff", "#2f2f2f", "#303030", "#313131", "#5f5f5f",
        "#606060", "#5e5e5e", "#afafaf", "#b2b2b2", "#c6c6c6", "#e4e4e4", "#f0f0f0", "#fafafa"})
    for h in hexes:
        s.add(hex=h, **run(lambda: P.xterm256(h)))
    out["xterm256"] = s

    s = Section("density_style")
    for cm in P.COLORMAPS:
        for dark in (True, False):
            for f in fracs:
                s.add(cmap=cm, frac=f, dark_bg=dark, accent="blue",
                      **run(lambda: P.density_style(cm, f, dark), enc_style))
    for f in (0.2, 0.5, 0.9):
        s.add(cmap="terminal", frac=f, dark_bg=True, accent="cyan",
              **run(lambda: P.density_style("terminal", f, True, accent="cyan"), enc_style))
    out["density_style"] = s

    s = Section("fmt_density")
    for v in (0.0, -1.0, 1e-5, 0.0123, 0.5, 0.999, 1.0, 1.25, 9.95, 9.96, 10.0, 99.5, 999.0, 999.5, 1000.0, 1234.5,
              9999.0, 9999.9, 10000.0, 15500.0, 999999.0, 1e6, 2.5e7, 1e12):
        s.add(v=v, **run(lambda: P.fmt_density(v)))
    out["fmt_density"] = s

    s = Section("sparkline")
    for counts, width in (([0, 1, 2, 3], 4), ([], 10), ([0, 0, 0], 5), ([5], 8), ([1, 100], 6), (list(range(40)), 20),
                          ([3, 0, 7, 1, 1, 9, 2], 20), ([1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16], 4),
                          ([1, 15, 16], 3), ([7, 7], 3)):
        s.add(counts=counts, width=width, **run(lambda: P.sparkline(counts, width)))
    out["sparkline"] = s

    s = Section("axis_labels")
    for vals in ([0.0, 5.0, 10.0], [1.7e17, 1.7e17 + 5e5, 1.7e17 + 1e6], [0.001, 0.0015, 0.002], [100.0, 150.0, 200.0],
                 [1234.5, 1234.55, 1234.6], [-1.0, 0.0, 1.0], [1e-5, 5e-5, 1e-4], [3.0, 3.0, 3.0],
                 [60800.0, 61000.0, 61200.0], [16.0, 21.0, 26.0], [0.1, 0.2, 0.30000000000000004],
                 [1e6, 1.5e6, 2e6], [999.0, 1000.0, 1001.0], [0.0, 0.0, 1e-300], [123.456, 123.4561, 123.4562],
                 [1.0, 1.0 + 1e-15, 1.0 + 2e-15], [-74.25, -74.0, -73.75]):
        s.add(values=vals, **run(lambda: P.axis_labels(vals)))
    out["axis_labels"] = s

    s = Section("colorbar")
    for cm in P.COLORMAPS:
        for vmin, vmax in ((1.0, 1000.0), (0.5, 0.5), (0.01, 2.5e7), (0.0, 10.0), (1.0, 0.0), (3.0, 4.0)):
            for dark in (True, False):
                s.add(cmap=cm, vmin=vmin, vmax=vmax, dark_bg=dark, unit="deg⁻²", width=72, label="density",
                      accent="blue", **run(lambda: P.colorbar(cm, vmin, vmax, dark, unit="deg⁻²", width=72), enc_text))
    for width, unit, label in ((20, "", "count"), (120, "rows/cell", "count"), (40, "x", "d")):
        s.add(cmap="magma", vmin=1.0, vmax=50.0, dark_bg=True, unit=unit, width=width, label=label, accent="red",
              **run(lambda: P.colorbar("magma", 1.0, 50.0, True, unit=unit, width=width, label=label, accent="red"),
                    enc_text))
    out["colorbar"] = s

    rng = np.random.default_rng(17)
    s = Section("render_histogram")
    hist_cases = [
        (list(np.linspace(0, 10, 21)), [0, 1, 5, 10, 50, 100, 50, 10, 5, 1] * 2),
        (list(np.linspace(16, 26, 41)), [int(x) for x in rng.poisson(200 * np.exp(-((np.arange(40) - 22) / 6) ** 2))]),
        (list(np.linspace(-3, 3, 11)), [1] * 10),
        (list(np.linspace(0, 1, 6)), [0, 0, 1, 0, 0]),
        (list(np.linspace(1.6e9, 1.7e9, 11)), [int(x) for x in rng.integers(0, 10**6, 10)]),
        (list(np.linspace(-2, 4, 61)), [int(x) for x in rng.integers(0, 1000, 60)]),
        ([0.0, 1.0], [123456789]),
        ([], []),
        (list(np.linspace(0.001, 0.002, 5)), [3, 0, 0, 1]),
    ]
    for edges, counts in hist_cases:
        edges = [float(e) for e in edges]
        for width, height in ((70, 12), (40, 5), (120, 20), (12, 3)):
            for log_y in (False, True):
                for log_x, xlabel in ((False, ""), (False, "mag"), (True, "psfFlux")):
                    if width != 70 and (log_x or xlabel):
                        continue
                    s.add(edges=edges, counts=counts, width=width, height=height, color=None, log_y=log_y,
                          xlabel=xlabel, log_x=log_x,
                          **run(lambda: P.render_histogram(edges, counts, width=width, height=height, log_y=log_y,
                                                           xlabel=xlabel, log_x=log_x), enc_text))
        s.add(edges=edges, counts=counts, width=60, height=8, color="cyan", log_y=False, xlabel="x", log_x=False,
              **run(lambda: P.render_histogram(edges, counts, width=60, height=8, color="cyan", xlabel="x"), enc_text))
    out["render_histogram"] = s

    s = Section("render_density")
    dens = []
    g = np.zeros((20, 40), dtype=np.int64)
    g[5:10, 10:30] = 3
    dens.append((g, (0.0, 1.0), (0.0, 1.0)))
    yy, xx = np.mgrid[0:24, 0:60]
    dens.append((rng.poisson(50 * np.exp(-((xx - 30) ** 2 / 200 + (yy - 12) ** 2 / 40))), (16.0, 26.0), (0.0, 500.0)))
    dens.append((rng.poisson(0.3, (10, 16)), (-1.0, 1.0), (1e6, 2e6)))
    dens.append((np.zeros((8, 8), dtype=np.int64), (0.0, 1.0), (0.0, 1.0)))
    dens.append((np.full((6, 10), 5), (60800.0, 61200.0), (-90.0, 90.0)))
    dens.append((rng.poisson(2.0, (9, 13)), (0.0, 1.0), (0.0, 1.0)))  # odd sizes: the last row/column dropped
    for grid, xlim, ylim in dens:
        for cm, dark, xl, yl in (("magma", True, "", ""), ("magma", True, "mag", "snr"), ("viridis", False, "", ""),
                                 ("terminal", True, "x", ""), ("gray", False, "", "y")):
            s.add(grid=np.asarray(grid).tolist(), xlim=list(xlim), ylim=list(ylim), cmap=cm, dark_bg=dark, xlabel=xl,
                  ylabel=yl, accent="blue",
                  **run(lambda: P.render_density(grid, xlim, ylim, cmap=cm, dark_bg=dark, xlabel=xl, ylabel=yl),
                        enc_text))
    out["render_density"] = s

    s = Section("render_skymap")
    sky = []
    g = np.zeros((180, 360), dtype=np.int64)
    g[85:95, 0:20] = 10  # a patch at ra 0-20, dec -5..+5 (as test_skymap_renders, at 1 deg)
    sky.append(("patch_1deg", g))
    g = np.zeros((36, 72), dtype=np.int64)
    g[10:20, 30:50] = rng.integers(1, 100, (10, 20))
    g[0, 0] = 5
    g[35, 71] = 1
    sky.append(("blocks_5deg", g))
    demo = ParquetDataset(os.path.join(FIXTURES, "demo.parquet"), threads=THREADS)
    sky.append(("demo_2deg", demo.sky_counts(View(), "ra", "dec", res_deg=2.0)))
    sky.append(("demo_1deg", demo.sky_counts(View(), "ra", "dec", res_deg=1.0)))
    sky.append(("empty_1deg", np.zeros((180, 360), dtype=np.int64)))
    sky.append(("uniform_10deg", np.full((18, 36), 7)))
    out["skymap_grids"] = [{"id": f"skymap_grids/{name}", "name": name, "grid": np.asarray(g).tolist()}
                           for name, g in sky]
    combos = [(80, None, "magma", True, 0.0, None), (40, None, "magma", True, 0.0, None),
              (120, 30, "viridis", True, 180.0, "demo"), (72, 12, "terminal", True, 0.0, None),
              (60, None, "gray", False, 90.0, "a caption"), (8, 3, "plasma", True, 0.0, None),
              (100, None, "inferno", False, 270.0, None)]
    for name, g in sky:
        for width, height, cm, dark, center, caption in combos:
            s.add(grid=f"skymap_grids/{name}", width=width, height=height, cmap=cm, dark_bg=dark, center=center,
                  caption=caption, accent="blue",
                  **run(lambda: P.render_skymap(g, width=width, height=height, cmap=cm, dark_bg=dark, center=center,
                                                caption=caption), enc_text))
    out["render_skymap"] = s

    s = Section("sky_shape")
    for w, h in ((80, None), (8, None), (3, None), (200, 10), (200, 1), (81, None), (82, None), (100, 100)):
        s.add(width=w, height=h, **run(lambda: P.sky_shape(w, h), list))
    out["sky_shape"] = s
    return out


# ====================================================================== data_*.json
FIXTURE_FILES = ["demo", "odd", "types", "hostile", "units", "casedup", "rowcol", "nulname"]

_DS: dict[str, ParquetDataset] = {}


def dataset(name: str) -> ParquetDataset:
    if name not in _DS:
        ds = ParquetDataset(os.path.join(FIXTURES, name + ".parquet"), threads=THREADS)
        ds._duck_types(wait=True)
        _DS[name] = ds
    return _DS[name]


def datasets():
    for name in FIXTURE_FILES:
        if name == "nulname":
            continue
        yield os.path.join(FIXTURES, name + ".parquet"), dataset(name)


def fixture_columns():
    out = []
    for path, ds in datasets():
        for c in ds.columns:
            out.append((c.name, c.arrow_type, c.unit, os.path.basename(path)))
    return out


def enc_page(p) -> dict:
    cols = list(zip(*p.rows)) if p.rows else [() for _ in p.columns]
    return {"offset": p.offset, "columns": p.columns, "types": [str(t) for t in p.types],
            "row_numbers": p.row_numbers,
            "rows": [[enc(v, t) for v, t in zip(row, p.types)] for row in p.rows] if cols else []}


def enc_view(v: View) -> dict:
    return {"where": v.where, "order_by": [[c, d] for c, d in v.order_by], "sql": v.sql}


def enc_stats(st, typ=None) -> dict:
    """``typ``: the column's type as DuckDB returns it (the encoding hint for min, max and top)."""
    return {"name": st.name, "count": st.count, "nulls": st.nulls, "nans": st.nans, "distinct": st.distinct,
            "min": enc(st.min, typ), "max": enc(st.max, typ), "mean": enc(st.mean), "std": enc(st.std),
            "quantiles": [[q, enc(v)] for q, v in st.quantiles.items()],
            "top": [[enc(v, typ), n] for v, n in st.top], "sampled": st.sampled}


# Per fixture: views, windows (offset, limit, columns), stats/histogram columns, find_row file rows
PLANS = {
    "demo": {
        "views": [View(), View(where="mag < 20 and band = 'r'"), View(where="band = 'r'"),
                  View(order_by=[("mag", False)]), View(order_by=[("mag", True)]),
                  View(where="mag < 20 and band = 'r'", order_by=[("mag", True)]),
                  View(order_by=[("band", False), ("mag", True)]), View(order_by=[("ssObjectId", False)]),
                  View(where="detector = 7"), View(where="trailLength is null", order_by=[("psfFlux", True)]),
                  View(sql="select band, count(*) as n from t group by band order by band"),
                  View(sql="select diaSourceId, mag, ingestTime from t where mag > 25 order by diaSourceId"),
                  View(where="band = ')' or detector = 3"), View(where="detector = 3 -- )"),
                  View(where="(detector = 3) or (band = 'r')")],
        "windows": [(0, 20, None), (2490, 20, None), (9995, 10, None), (19990, 20, None), (19995, 20, None),
                    (20000, 20, None), (2495, 10, ["diaSourceId", "mag"]), (100, 5, ["band", "ingestTime", "ra"])],
        "stats": ["psfFlux", "ssObjectId", "band", "mag", "ingestTime", "isDipole", "detector", "diaSourceId",
                  "trailLength", "midpointMjdTai"],
        "hist": [("mag", {"bins": 20}), ("ingestTime", {"bins": 10, "temporal": True}),
                 ("psfFlux", {"bins": 10, "log": True}), ("mag", {"bins": 7, "lo": 18.0, "hi": 24.0}),
                 ("detector", {"bins": 40}), ("snr", {"bins": 40, "log": True}), ("dec", {"bins": 30})],
        "sky": [("ra", "dec", 10.0), ("ra", "dec", 30.0)],
        "xy": [("mag", "snr", 20, 10, None, None), ("ra", "dec", 12, 6, (0.0, 360.0), (-90.0, 90.0))],
        "fetch_columns": [([0, 1, 2], ["diaSourceId", "mag"]), ([19999, 0, 2500, 2499], ["band", "ra"]),
                          ([5, 5, 3], ["psfFlux"])],
        "find_rows": [0, 1, 2499, 2500, 12345, 19999],
    },
    "odd": {
        "views": [View(), View(where='"weird name" = 1'), View(where="len(tags) = 2"),
                  View(order_by=[("x", True)]), View(order_by=[("x", False)]), View(where="x > 0"),
                  View(sql="select \"weird name\", count(*) n from t group by 1 order by 1")],
        "windows": [(0, 20, None), (290, 20, None), (950, 100, None), (999, 5, None)],
        "stats": ["x", "weird name", "tags", "pos", "blob", "day", "allnull", "file_row_number"],
        "hist": [("x", {"bins": 20}), ("day", {"bins": 10, "temporal": True}), ("allnull", {"bins": 5})],
        "sky": [],
        "xy": [("x", "weird name", 10, 5, None, None)],
        "fetch_columns": [([0, 299, 300, 999], ["x", "tags"])],
        "find_rows": [0, 1, 500, 999],
    },
    "types": {
        "views": [View(), View(where="i8 > 0"), View(order_by=[("f64", False)]), View(order_by=[("f64", True)]),
                  View(order_by=[("str", False)]), View(order_by=[("dec18_6", True)]),
                  View(order_by=[("ts_ns", True)]), View(where="bool"),
                  View(sql="select * from t"),
                  View(sql="select i64, u64, dec76_10, ts_ns, t64_ns from t order by i64")],
        "windows": [(0, 20, None)],
        "plain_windows": [(0, 20, None), (5, 10, None), (52, 10, None), (0, 5, ["i64", "u64", "dec76_10", "map"])],
        "full": True,  # also every row of the plain view
        "file_table": True,
        "stats": ["i8", "u64", "f16", "f32", "f64", "bool", "str", "dict", "bin", "uuid", "date", "ts_ns", "ts_tz_ny",
                  "t64_ns", "dur_ms", "dec9_2", "dec38_3", "dec76_10", "list", "struct", "map", "null"],
        "hist": [("f64", {"bins": 10}), ("f32", {"bins": 10}), ("i64", {"bins": 8}), ("u64", {"bins": 8}),
                 ("dec18_6", {"bins": 6}), ("date", {"bins": 5, "temporal": True}),
                 ("ts_ns", {"bins": 5, "temporal": True}), ("null", {"bins": 4})],
        "sky": [],
        "xy": [("f32", "i16", 6, 4, None, None)],
        "fetch_columns": [([0, 7, 59, 13], ["i64", "ts_ns", "dec76_10", "map", "uuid"])],
        "find_rows": [0, 10, 21, 59],
    },
    "hostile": {
        "views": [View(), View(order_by=[("s", False)]), View(where="s = 'it''s \"quoted\" \\ back''slash'"),
                  View(where="\"select\" like '%RED%'"), View(where="a = 10"),
                  View(sql="select a, s from t where a < 3 order by a")],
        "windows": [(0, 20, None), (0, 5, ["s", "dir\\"])],
        "stats": ["a", "s", "select", "dir\\"],
        "hist": [("a", {"bins": 4})],
        "sky": [],
        "xy": [],
        "fetch_columns": [([3, 1], ["s"])],
        "file_table": True,
        "find_rows": [0, 3, 19],
    },
    "units": {
        "views": [View(), View(where="mag < 20"), View(order_by=[("ra", False)]),
                  View(order_by=[("midpointMjdTai", True)])],
        "windows": [(0, 20, None), (190, 20, None), (495, 10, None)],
        "stats": ["ra", "dec", "mag", "psfFlux", "decimalDeg", "band", "midpointMjdTai"],
        "hist": [("ra", {"bins": 12}), ("midpointMjdTai", {"bins": 10}), ("decimalDeg", {"bins": 9})],
        "sky": [("ra", "dec", 20.0), ("RAJ2000", "DEJ2000", 45.0)],
        "xy": [("ra", "dec", 8, 4, None, None)],
        "fetch_columns": [([1, 0, 499], ["ra", "dec", "decimalDeg"])],
        "find_rows": [0, 199, 200, 499],
    },
    "casedup": {
        "views": [View(), View(where="x > 1"), View(order_by=[("name", True)]), View(order_by=[("Name", False)])],
        "windows": [(0, 10, None)],
        "stats": ["name", "Name", "x"],
        "hist": [("x", {"bins": 3})],
        "sky": [], "xy": [],
        "fetch_columns": [([2, 0], ["name"])],
        "file_table": True,
        "find_rows": [0, 2],
    },
    "rowcol": {
        "views": [View(), View(where="b > 0"), View(where="b > 0", order_by=[("b", True)])],
        "windows": [(0, 10, None)],
        "stats": ["__pqx_row", "b"],
        "hist": [], "sky": [], "xy": [],
        "fetch_columns": [([1, 0], ["b"])],
        "find_rows": [0, 1],
    },
    "nulname": {
        "views": [View(), View(where="b > 0")],
        "windows": [(0, 10, None)],
        "stats": ["b"],
        "hist": [], "sky": [], "xy": [],
        "fetch_columns": [([0], ["b"])],
        "find_rows": [1],
    },
}


def gen_data(name: str) -> dict:
    plan = PLANS[name]
    out: dict = {"header": header(f"data_{name}.json") | {"fixture": name + ".parquet", "duckdb_threads": THREADS}}
    try:
        ds = dataset(name)
    except Exception as e:  # noqa: BLE001
        out["open"] = [{"id": "open/0", **err(e)}]
        return out
    md = ds.meta
    duck = {}
    try:
        for r in ds.cursor().execute("DESCRIBE t").fetchall():
            duck[r[0]] = r[1]
    except Exception as e:  # noqa: BLE001
        duck = {"__error__": str(e)}
    out["file"] = [{"id": "file/0", "num_rows": ds.num_rows, "num_row_groups": md.num_row_groups,
                    "num_columns": len(ds.columns), "num_leaf_columns": md.num_columns,
                    "created_by": md.created_by, "setup_error": None if ds.setup_error is None else str(ds.setup_error),
                    "has_file_row_number": ds._has_rownum}]
    s = Section("columns")
    for c in ds.columns:
        rec = {"name": c.name, "arrow_type": str(c.arrow_type), "nullable": c.nullable, "unit": c.unit,
               "description": c.description, "short_type": F.short_type(c.arrow_type),
               "kind": F.kind_for(c.name, c.arrow_type, c.unit), "is_numeric": c.is_numeric,
               "is_float": c.is_float, "is_temporal": c.is_temporal, "is_nested": c.is_nested}
        try:
            rec["sql_name"] = ds.sql_name(c.name)
            rec["duckdb_type"] = duck.get(rec["sql_name"])
        except Exception as e:  # noqa: BLE001
            rec |= {"sql_name": None, "duckdb_type": None} | err(e)
        s.add(**rec)
    out["columns"] = s

    s = Section("validate")
    for v in plan["views"] + [View(where="nosuchcolumn > 3"), View(where="mag <")]:
        s.add(view=enc_view(v), **run(lambda: ds.validate(v), lambda r: [[n, str(t)] for n, t in r]))
    out["validate"] = s

    s = Section("count")
    for v in plan["views"]:
        s.add(view=enc_view(v), **run(lambda: ds.count(v)))
    out["count"] = s

    s = Section("fetch")
    for v in plan["views"]:
        for off, lim, cols in plan.get("plain_windows", plan["windows"]) if v.is_trivial else plan["windows"]:
            s.add(view=enc_view(v), offset=off, limit=lim, columns=cols,
                  **run(lambda: ds.fetch(v, off, lim, cols), enc_page))
        if not v.is_trivial:  # the view's last rows
            try:
                n = ds.count(v)
            except Exception:  # noqa: BLE001
                n = None
            if n:
                for off in (max(0, n - 7), max(0, n // 2 - 3)):
                    s.add(view=enc_view(v), offset=off, limit=10, columns=None,
                          **run(lambda: ds.fetch(v, off, 10, None), enc_page))
    if plan.get("full"):
        s.add(view=enc_view(View()), offset=0, limit=ds.num_rows, columns=None,
              **run(lambda: ds.fetch(View(), 0, ds.num_rows), enc_page))
    out["fetch"] = s

    s = Section("fetch_columns")
    for rows, cols in plan["fetch_columns"]:
        s.add(file_rows=rows, columns=cols, **run(lambda: ds.fetch_columns(rows, cols), enc_page))
    out["fetch_columns"] = s

    s = Section("find_row")
    for v in plan["views"]:
        for fr in plan["find_rows"]:
            s.add(view=enc_view(v), file_row=fr, **run(lambda: ds.find_row(v, fr)))
    out["find_row"] = s

    s = Section("fetch_around")
    for v in plan["views"]:
        if not ds.can_fetch_around(v):
            continue
        try:
            n = ds.count(v)
            rn = ds.fetch(v, 0, n, [ds.column_names[0]]).row_numbers
        except Exception:  # noqa: BLE001
            continue
        for pos in sorted({min(p, n - 1) for p in (0, 3, 400, n // 2, n - 1)}):
            fr = rn[pos]
            for offset in sorted({pos, max(0, pos - 150), max(0, pos - 299)}):
                s.add(view=enc_view(v), file_row=fr, pos=pos, offset=offset, limit=300, columns=ds.column_names[:2],
                      **run(lambda: ds.fetch_around(v, fr, pos, offset, 300, ds.column_names[:2]),
                            lambda p: {"offset": p.offset, "row_numbers": p.row_numbers,
                                       "first": [enc(x, t) for x, t in zip(p.rows[0], p.types)] if p.rows else None}))
    out["fetch_around"] = s

    s = Section("column_stats")
    stat_views = [View(), plan["views"][1]] + [v for v in plan["views"] if v.sql.strip()][:1]
    for v in stat_views:
        try:
            rtypes = dict(ds.validate(v))
        except Exception:  # noqa: BLE001
            rtypes = {}
        for c in plan["stats"]:
            if v.sql.strip() and c not in rtypes:
                continue
            s.add(view=enc_view(v), column=c, sample=None,
                  **run(lambda: ds.column_stats(v, c), lambda st: enc_stats(st, rtypes.get(c))))
    if name == "demo":
        rtypes = dict(ds.validate(View()))
        for c in ("mag", "band"):
            s.add(view=enc_view(View()), column=c, sample=5000,
                  **run(lambda: ds.column_stats(View(), c, sample=5000), lambda st: enc_stats(st, rtypes.get(c))))
    out["column_stats"] = s

    s = Section("histogram")
    for v in (View(), plan["views"][1]):
        for c, kw in plan["hist"]:
            s.add(view=enc_view(v), column=c, **kw, **run(lambda: ds.histogram(v, c, **kw),
                                                       lambda r: {"edges": [frepr(e) for e in r[0]], "counts": r[1]}))
    out["histogram"] = s

    s = Section("sky_counts")
    for v in (View(), plan["views"][1]):
        for lon, lat, res in plan["sky"]:
            s.add(view=enc_view(v), lon=lon, lat=lat, res_deg=res,
                  **run(lambda: ds.sky_counts(v, lon, lat, res), lambda g: g.tolist()))
    out["sky_counts"] = s

    s = Section("xy_counts")
    for v in (View(), plan["views"][1]):
        for x, y, nx, ny, xlim, ylim in plan["xy"]:
            s.add(view=enc_view(v), x=x, y=y, nx=nx, ny=ny, xlim=xlim, ylim=ylim,
                  **run(lambda: ds.xy_counts(v, x, y, nx, ny, xlim=xlim, ylim=ylim),
                        lambda r: {"grid": r[0].tolist(), "xlim": [frepr(a) for a in r[1]],
                                   "ylim": [frepr(a) for a in r[2]]}))
    out["xy_counts"] = s

    s = Section("column_chunk_summary")
    try:
        for d in ds.column_chunk_summary():
            s.add(**{k: (enc(v) if k in ("min", "max") else v) for k, v in d.items()})
    except Exception as e:  # noqa: BLE001
        s.add(**err(e))
    out["column_chunk_summary"] = s
    out["row_groups"] = [{"id": f"row_groups/{i}", **d} for i, d in enumerate(ds.row_groups())]
    s = Section("column_encodings")
    for i in range(md.num_columns):
        path = md.schema.column(i).path
        s.add(path=path, out=sorted(ds.column_encodings(path)))
    out["column_encodings"] = s
    out["key_value_metadata"] = [{"id": f"key_value_metadata/{i}", "key": k, "value": v}
                                 for i, (k, v) in enumerate(ds.key_value_metadata().items())]

    s = Section("sample_condition")
    for n in (None, 0, 10, 1000, 5000, ds.num_rows - 1, ds.num_rows, 10**9):
        for slices in (16, 4):
            s.add(sample=n, slices=slices, **run(lambda: ds.sample_condition(n, slices)))
    out["sample_condition"] = s

    # the filter box's hint, as the app builds it from the file's first page (_set_filter_placeholder)
    s = Section("filter_placeholder")
    try:
        from pqx.app import filter_placeholder

        page = ds.fetch(View(), 0, 1)
        lower: dict[str, int] = {}
        for c in ds.column_names:
            lower[c.lower()] = lower.get(c.lower(), 0) + 1
        cols = [(c, t, v) for c, t, v in zip(page.columns, page.types, page.rows[0] if page.rows else ())
                if lower.get(c.lower()) == 1]
        names, types, row = (list(x) for x in zip(*cols)) if cols else ([], [], [])
        s.add(columns=names, types=[str(t) for t in types], row=[enc(v, t) for v, t in zip(row, types)],
              out=filter_placeholder(names, types, tuple(row)))
    except Exception as e:  # noqa: BLE001
        s.add(**err(e))
    out["filter_placeholder"] = s

    if plan.get("file_table"):  # the file's values as PyArrow reads them: the truth behind DuckDB's types
        import pyarrow.parquet as pq

        pf = pq.ParquetFile(ds.path)
        s = Section("file_table")
        for name in pf.schema_arrow.names:
            try:
                col = pf.read(columns=[name]).column(0)
                s.add(name=name, arrow_type=str(col.type), values=enc_arrow(col.combine_chunks()))
            except Exception as e:  # noqa: BLE001 - e.g. PyArrow can't read back a fixed-size list with NULLs
                s.add(name=name, arrow_type=str(pf.schema_arrow.field(name).type), values=None, **err(e))
        out["file_table"] = s

    out["guess_sky_columns"] = [{"id": "guess_sky_columns/0", "names": ds.column_names,
                                 "out": list(D.guess_sky_columns(ds.column_names))}]
    return out


# ====================================================================== data_common.json
def gen_data_common() -> dict:
    out: dict = {"header": header("data_common.json")}
    s = Section("parse_row_spec")
    for text, total in (("1234", 10**6), ("1_000", 10**6), ("1.5M", 10**7), ("2k", 10**6), ("10k", 100),
                        ("50%", 1000), ("0%", 1000), ("100%", 1000), ("150%", 1000), ("-1", 1000), ("-1000", 1000),
                        ("-5000", 1000), ("1,234", 10**6), ("abc", 10), ("", 10), ("  ", 10), ("1e3", 10**6),
                        ("inf", 10), ("nan", 10), ("1g", 10**10), ("1b", 10**10), ("1B", 10**10), ("0.5k", 10**6),
                        ("7", 0), ("-1", 0), ("1.9", 10), ("-0.5", 10), ("%", 10), ("k", 10), ("12 345", 10**6),
                        ("33.3333%", 1000), ("1e400", 10), (" 42 ", 100)):
        s.add(text=text, total=total, **run(lambda: D.parse_row_spec(text, total)))
    out["parse_row_spec"] = s

    pwn = "/tmp/pqx-golden-pwned.txt"
    s = Section("check_select")
    for q in ("select a, count(*) from t group by 1", "with x as (select * from t) select * from x", "from t",
              "pivot t on b using sum(a)", "summarize t", "describe t", "SELECT 1", "select 1;",
              "CREATE TYPE x AS ENUM ('a'); SELECT 1",
              f"SELECT * FROM t WHERE (a > 0); COPY (SELECT 1) TO '{pwn}'; SELECT (1)",
              f"select * from t; COPY (SELECT 1) TO '{pwn}'", "select 1; select 2", "", "insert into t values (1)",
              "drop table t", "attach 'x.db'", "pragma version", "set threads=1", "select * from t -- ;",
              "select ';' as x", "copy t to 'x.csv'", "SELECT * FROM (SELECT 1) LIMIT 0", "explain select 1",
              "values (1), (2)", "select 1 union all select 2", "show tables", "load httpfs", "call pragma_version()"):
        s.add(sql=q, **run(lambda: D.check_select(q)))
    out["check_select"] = s

    s = Section("where_sql")
    for w in ["detector = 3) OR (band = 'r'", "(detector = 3", "detector = 3)", "detector = 3)) OR ((band = 'r'",
              "band = ')' or detector = 3", "detector = 3 -- )", "detector = 3 /* ( */",
              "(detector = 3) or (band = 'r')",
              '"detector" = 3 or "band" = \'(\'', "", "a > 0", "f(x, (y))", "\"a)b\" > 1", "a > 0 -- comment",
              "x IN (1, 2, 3)", "))((", "'unterminated", "$$ ( $$ = x"] + [
                  f"a > 0{')' * k}; COPY (SELECT 1) TO '{pwn}'; {'SELECT * FROM (' * (k - 1)}SELECT 1 AS a WHERE (1"
                  for k in range(1, 5)]:
        s.add(where=w, **run(lambda: D.where_sql(w)))
    out["where_sql"] = s

    s = Section("is_sql_query")
    for q in ("SELECT 1", "  with a as (select 1) select * from a", "selected > 3", "from t", "FROM t", "pivot t",
              "unpivot t", "describe t", "summarize t", "select", "x = 1", "", "   select\n1", "(select 1)",
              "SELECTED", "with_x > 1"):
        s.add(text=q, out=D.is_sql_query(q))
    out["is_sql_query"] = s

    names = ["band", "select", "a b", "Name", "_x1", "1abc", "", "a\"b", "esc" + ESC + "x", "a\x00b", "group",
             "order", "dir\\", "ünïcode", "x-y", "MAG", "t", "value", "count", "file_row_number", "__pqx_row"]
    s = Section("idents")
    for n in names:
        s.add(name=n, quote_ident=run(lambda: D.quote_ident(n)), is_plain_ident=run(lambda: D.is_plain_ident(n)),
              sql_ident=run(lambda: D.sql_ident(n)), sql_column_ref=run(lambda: D.sql_column_ref(n)))
    out["idents"] = s

    s = Section("sql_text_literal")
    for v in MF.evil_values(pwn) + ["plain", "", "it''s", "\x00", "a\x9fb", "日本"]:
        s.add(s=v, out=D.sql_text_literal(v), quote_str=D.quote_str(v))
    out["sql_text_literal"] = s

    s = Section("path_literal")
    for p in ("/data/a*.parquet", "/data/x[1].parquet", "/data/q?.parquet", "/d/it's.parquet", "/d/x[x=a:b].parquet",
              "/d/back\\slash*.parquet", "/plain/file.parquet", "C:\\data\\f[1].parquet"):
        s.add(path=p, out=D.path_literal(p))
    out["path_literal"] = s

    s = Section("guess_sky_columns")
    for names in (["id", "ra", "dec", "raErr"], ["coord_ra", "coord_dec"], ["x", "y"], ["RAJ2000", "DEJ2000"],
                  ["raErr", "decErr"], ["alpha", "delta"], ["glon", "glat", "ra", "dec"], ["Ra", "Dec"],
                  ["lambda", "beta"], ["raRate", "decRate", "ra_deg", "dec_deg"], ["obj_ra", "obj_dec"],
                  ["RA_ICRS", "DE_ICRS"], ["ra"], ["coordRa", "coordDec"], ["ecl_lon", "ecl_lat"]):
        s.add(names=names, out=list(D.guess_sky_columns(names)))
    out["guess_sky_columns"] = s

    from pqx.app import filter_placeholder

    s = Section("filter_placeholder")
    cases = [
        ([], [], None),
        (["a", "s"], [pa.int64(), pa.string()], (5, "x")),
        (["select", "a b", "n", "s"], [pa.int64(), pa.int64(), pa.float64(), pa.string()], (1, 2, 1.5e15, " hi ")),
        (["n", "s"], [pa.float64(), pa.string()], (math.nan, "")),
        (["n", "m", "s"], [pa.float64(), pa.float32(), pa.large_string()],
         (math.inf, float(f32(0.000123456)), "x" * 40)),
        (["i", "s"], [pa.int64(), pa.string()], (12345678901234, ESC + "]0;T\x07ab")),
        (["b", "f"], [pa.bool_(), pa.float64()], (True, 123456.789)),
        (["f"], [pa.float64()], (1.5e12,)),
        (["f"], [pa.float64()], (99999.5,)),
        (["s"], [pa.string()], ("it's",)),
        (["d", "s"], [pa.decimal128(5, 2), pa.dictionary(pa.int32(), pa.string())], (decimal.Decimal("1.50"), "r")),
    ]
    for cols, types, row in cases:
        erow = None if row is None else [enc(v, t) for v, t in zip(row, types)]
        s.add(columns=cols, types=[str(t) for t in types], row=erow,
              **run(lambda: filter_placeholder(cols, types, row)))
    out["filter_placeholder"] = s

    out["keywords"] = [{"id": "keywords/0", "out": sorted(D.sql_keywords())}]
    return out


# ====================================================================== main
def main(argv: list[str]) -> None:
    outdir = argv[1] if len(argv) > 1 else HERE
    os.makedirs(outdir, exist_ok=True)
    for mod in (F, C, P, D):  # this checkout's pqx, not another one on the path
        assert os.path.dirname(mod.__file__) == os.path.join(REPO, "pqx"), (mod.__file__, REPO)
    files = {"fmt.json": gen_fmt, "cells.json": gen_cells, "plots.json": gen_plots,
             "data_common.json": gen_data_common}
    for name in FIXTURE_FILES:
        files[f"data_{name}.json"] = (lambda n: lambda: gen_data(n))(name)
    for fname, gen in files.items():
        obj = gen()
        path = os.path.join(outdir, fname)
        dump(path, obj)
        n = sum(len(v) for k, v in obj.items() if isinstance(v, list))
        print(f"{os.path.getsize(path):>10,}  {n:>7,} records  {fname}")


if __name__ == "__main__":
    main(sys.argv)
