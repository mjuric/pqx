"""Write pyformat.json: Python's format(value, spec) on many specs, the check of fmtx's port
of the format-spec mini-language, Decimal formatting and strftime (beyond what fmt.json covers).

    cd <repo root>
    PYTHONPATH=$PWD <python with pqx's dependencies> go/internal/fmtx/testdata/make_pyformat.py

Each record: "v" (a typed value, as in go/testdata/golden/README.md), "spec", and "out" or
"error" ("<exception type>: <message>"). Seeded, so a second run writes the same bytes.
"""
import datetime as dt
import decimal
import json
import os
import random
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, os.path.join(HERE, "..", "..", "..", "testdata", "golden"))
from make_golden import enc  # noqa: E402

rng = random.Random(7)

FLOATS = [0.0, -0.0, 0.1, -0.1, 1.5, 2.5, -2.5, 1 / 3, 2.675, 1e-5, 1e-4, 9.999e-5, 123456.789, 1e15, 1e16,
          1e17, 1.7976931348623157e308, 5e-324, 0.5, 99.95, 999999.5, 1234567.0, -1234567.891, 0.000123456,
          12345678901234567890.0, 0.001, 9.5, 10.5, 1e22, 1e-7]
INTS = [0, 1, -1, 7, 255, -255, 1234567, -1234567, 170000000000000123, -9223372036854775808,
        9223372036854775807, 18446744073709551615, 65, 0x10ffff]
STRS = ["", "abc", "日本語", "x" * 20, "a b"]
DECS = [decimal.Decimal(s) for s in ("0", "-0.30", "1.23456", "12345678901234567890.123", "0E-38", "1.5",
                                     "2.5", "-2.5", "999.995", "0.0000001", "100", "0.00")]
TIMES = [dt.datetime(2026, 1, 2, 3, 4, 5, 123456), dt.datetime(2026, 1, 2, 13, 4, 5, tzinfo=dt.timezone.utc),
         dt.datetime(5, 12, 31, 23, 59, 59), dt.datetime(2024, 12, 30), dt.datetime(2021, 1, 3),
         dt.date(2026, 7, 4), dt.date(1, 1, 1), dt.time(13, 4, 5, 6)]

FILLS = ["", "<", ">", "^", "=", "*<", "*>", "*^", "*=", "0<", "0=", "x^", "é>"]
SIGNS = ["", "-", "+", " "]
WIDTHS = ["", "0", "1", "5", "12", "020", "08"]
SEPS = ["", ",", "_"]
PRECS = ["", ".0", ".1", ".2", ".3", ".6", ".10", ".17", ".25"]
TYPES = ["", "b", "c", "d", "e", "E", "f", "F", "g", "G", "n", "o", "s", "x", "X", "%", "q"]


def rand_spec():
    return (rng.choice(FILLS) + rng.choice(SIGNS) + rng.choice(["", "", "z"]) + rng.choice(["", "", "#"])
            + rng.choice(WIDTHS) + rng.choice(SEPS) + rng.choice(PRECS) + rng.choice(TYPES))


FIXED_SPECS = [",", "_", ",_", "_,", "__", ".", ".f", "10.", "x<", "<<", "=5", "05s", "<05d", "+c", "#c", ",c",
               "_c", ",x", "_x", "_b", "_o", "_X", ",b", ",n", "_n", "z", "zd", "z.1f", "#.0f", "#.0e", "#g",
               "#.3g", "#x", "#o", "#b", "#X", ".3s", ">10.3s", "^7", "\x00<5", "99999999999999999999d",
               ".99999999999999999999f", "٣d", ".٢f", "10ab", "%Y", "%%", "e+", "ff", "-+5", "+-5", "0>5d",
               "0^5d", "0<5d", "*=+12,.3f", "=+012,.3f", "012,d", "09,d", "08,d", "07_x", "012_b", "+012_X",
               "-z,.2%", " .4g", " d", " s", "=s", "+s", "#s", "zs", "z.2f", ".2%", ",.2%", "n", ".3n",
               "c", "5c", "<5c", "0=5c", "=10e", "^+11.2e", "#^+11.2e", "G", ".0g", ".0G", ".0", ".1", ".17",
               "e", ".0e", "E", "F", ".0F", ",.1f", "_.1f", "_g", "%"]


def run(v, spec):
    try:
        return {"out": format(v, spec)}
    except Exception as e:  # noqa: BLE001
        return {"error": f"{type(e).__name__}: {e}"}


recs = []
seen = set()


def add(v, spec):
    key = (repr(v), spec)
    if key in seen:
        return
    seen.add(key)
    recs.append({"v": enc(v), "spec": spec, **run(v, spec)})


specs = FIXED_SPECS + [rand_spec() for _ in range(700)]
for spec in specs:
    for v in FLOATS + INTS + [True, False] + STRS + DECS:
        if rng.random() < 0.35 or spec in FIXED_SPECS:
            add(v, spec)

STRF = ["%Y-%m-%d %H:%M:%S", "%a %A %b %B %h", "%c|%x|%X|%r|%R|%T|%D|%F", "%C %y %Y %G %g %V %U %W %u %w %j",
        "%I %l %k %p %P %e", "%-d %_d %010d %-5d %_5m %^a %#b %#A %^B %#p", "%z %Z %:z %f", "%E %Ey %EY %Oy %OH %Od",
        "%q %+ %L %5 %", "%%%%Y", "%%f", "%10Y %3a %05B %-j", "%n%t", "%EC %Ex %EX %Ec %OC %OY %Ob", "%s"]
for t in TIMES:
    for f in STRF:
        if f == "%s" and isinstance(t, dt.datetime) and t.tzinfo is None:
            continue  # local time: depends on TZ
        if f == "%s":
            continue
        add(t, f)

# every directive with every modifier and some flags and widths (not %s: it depends on the time zone)
for c in [chr(i) for i in range(33, 127)] + ["é", "漢"]:
    if c == "s":
        continue
    for mod in ("", "E", "O"):
        for fl in ("", "-", "_", "0", "^", "#", "5", "-5", "05", "^8", "_3"):
            for t in (TIMES[0], TIMES[1], TIMES[5], TIMES[7]):
                add(t, "%" + fl + mod + c + "|")
for f in ("%1022Y", "%1023Y", "%1024Y", "%2047Y|", "%Y" * 600, "%c" * 120, "%9999999Y", "%%" * 300 + "%1000Y"):
    add(TIMES[0], f)

out = os.path.join(HERE, "pyformat.json")
with open(out, "w", encoding="utf-8") as fh:
    fh.write("[\n" + ",\n".join(json.dumps(r, ensure_ascii=False) for r in recs) + "\n]\n")
print(f"{len(recs)} records -> {out}")

# ------------------------------------------------- pyvalues.json: format_value and friends on random numbers
import math  # noqa: E402

import numpy as np  # noqa: E402

from pqx import fmt as F  # noqa: E402


def near_powers():
    out = []
    for k in range(-4, 11):
        p = 10.0 ** k
        for d in (-3, -2, -1, 0, 1):
            out.append(float(np.nextafter(p, 0)) if d == -1 else p * (1 + d * 1e-16) if d else p)
        out.append(math.nextafter(p, 0))
        out.append(math.nextafter(math.nextafter(p, 0), 0))
        out.append(p - 0.5 * 10.0 ** (k - 9))
        out.append(p - 0.5 * 10.0 ** (k - 3))
    return out


vals = near_powers()
for _ in range(1500):
    e = rng.uniform(-12, 20)
    vals.append(rng.choice((-1, 1)) * 10.0 ** e)
for _ in range(300):
    vals.append(float(np.float32(rng.uniform(-1e6, 1e6))))
for _ in range(200):  # ties in the decimal digits
    vals.append(rng.randint(-10**6, 10**6) / rng.choice((2, 4, 8, 16, 1000, 2000)))
values = []
for v in vals:
    kind = rng.choice(["mjd", "angle", "mag", "flux", "err", "float32", "float"])
    ov = rng.choice([None, None, None, 0, 1, 2, 3, 5, 9, 12, 17, 20])
    raw = rng.random() < 0.15
    values.append({"v": enc(v), "kind": kind, "raw": raw, "override": ov,
                   "out": F.format_value(v, kind, raw=raw, override=ov)})
derived = []
names = ["ra", "dec", "lon", "glat", "mjd", "jd", "raErr", "psfFlux", "lambda"]
for _ in range(3000):
    name = rng.choice(names)
    v = rng.choice([rng.uniform(-400, 400), rng.uniform(-1, 1) * 10 ** rng.uniform(-9, 0),
                    rng.uniform(0, 100000), rng.uniform(2.4e6, 2.5e6), 360 - 10 ** -rng.uniform(5, 12),
                    rng.uniform(0.1, 1e9)])
    unit = rng.choice(["", "deg"])
    kind = F.kind_for(name, __import__("pyarrow").float64(), unit)
    derived.append({"name": name, "kind": kind, "v": enc(v), "unit": unit, "out": F.derived(name, kind, v, unit)})
human = []
for _ in range(500):
    n = rng.choice([rng.randint(-10**13, 10**13), int(10 ** rng.uniform(0, 15))])
    b = 10 ** rng.uniform(0, 16)
    part, whole = rng.randint(0, 10**6), rng.randint(1, 10**6)
    human.append({"n": n, "count": F.human_count(n), "b": b, "bytes": F.human_bytes(b),
                  "part": part, "whole": whole, "percent": F.percent(part, whole)})
out = os.path.join(HERE, "pyvalues.json")
with open(out, "w", encoding="utf-8") as fh:
    json.dump({"format_value": values, "derived": derived, "human": human}, fh, ensure_ascii=False, indent=0)
print(f"{len(values)} + {len(derived)} + {len(human)} records -> {out}")
