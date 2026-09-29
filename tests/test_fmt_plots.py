import datetime as dt
import math

import numpy as np
import pyarrow as pa

from pqx import fmt as F
from pqx import plots


def test_kinds():
    assert F.kind_for("midpointMjdTai", pa.float64()) == "mjd"
    assert F.kind_for("ra", pa.float64()) == "angle"
    assert F.kind_for("coord_dec", pa.float64()) == "angle"
    assert F.kind_for("raErr", pa.float32()) == "err"
    assert F.kind_for("psfFlux", pa.float32()) == "flux"
    assert F.kind_for("gMag", pa.float32()) == "mag"
    assert F.kind_for("x", pa.float32(), unit="deg") == "angle"
    assert F.kind_for("radius", pa.float64()) == "float"  # not an RA
    assert F.kind_for("diaSourceId", pa.int64()) == "int"
    assert F.kind_for("t", pa.timestamp("ms")) == "time"


def test_format_values():
    assert F.format_value(None, "float") == F.NULL
    assert F.format_value(float("nan"), "float") == "NaN"
    assert F.format_value(60800.123456789, "mjd") == "60800.12346"
    assert F.format_value(12.3456789, "angle") == "12.345679"
    assert F.format_value(21.123456, "mag") == "21.123"
    assert F.format_value(28561.3, "flux") == "28560"
    assert F.format_value(2.14e-5, "err") == "2.14e-05"
    assert F.format_value(170000000000000123, "int") == "170000000000000123"
    assert F.format_value(0.1, "float", raw=True) == "0.1"
    assert F.format_value(b"\x01\x02", "binary").startswith("0x0102")
    assert F.format_value(list(range(30)), "nested").endswith("(30)")
    assert F.format_value(dt.datetime(2026, 1, 1, tzinfo=dt.timezone.utc), "time") == "2026-01-01 00:00:00Z"


def test_derived():
    assert F.derived("midpointMjdTai", "mjd", 60676.0) == "2025-01-01 00:00:00.000"
    assert F.derived("ra", "angle", 180.0) == "12h00m00.000s"
    assert F.derived("dec", "angle", -30.5) == "-30°30′00.00″"
    assert F.derived("raErr", "err", 1e-6).endswith("mas")
    assert F.deg_to_hms(359.99999999) == "00h00m00.000s"
    assert F.shortest(0.10000000149011612, pa.float32()) == 0.1


def test_human():
    assert F.human_count(4_213_882_112) == "4.21B"
    assert F.human_bytes(35.4 * 1024 ** 2) == "35.4 MiB"
    assert F.short_type(pa.dictionary(pa.int32(), pa.string())) == "dict<str>"


def test_skymap_renders():
    g = np.zeros((360, 720), dtype=np.int64)
    g[170:190, 0:40] = 10  # a patch at ra 0-20, dec -5..+5
    t = plots.render_skymap(g, width=80)
    lines = t.plain.split("\n")
    assert len(lines) >= 20
    assert any("█" in ln for ln in lines)
    assert "deg⁻²" in t.plain
    # RA increases to the left: the patch (ra 0..20) sits just left of centre
    row = next(ln for ln in lines if "█" in ln)
    assert row.index("█") < 40
    empty = plots.render_skymap(np.zeros((180, 360), dtype=np.int64), width=40)
    assert "█" not in empty.plain


def test_histogram_and_density_render():
    edges = list(np.linspace(0, 10, 21))
    counts = [0, 1, 5, 10, 50, 100, 50, 10, 5, 1] * 2
    t = plots.render_histogram(edges, counts, width=60, height=8, xlabel="mag")
    assert "100 ┤" in t.plain and "mag" in t.plain
    assert plots.axis_labels([1.7e17, 1.7e17 + 5e5, 1.7e17 + 1e6])[0] != plots.axis_labels(
        [1.7e17, 1.7e17 + 5e5, 1.7e17 + 1e6])[1]
    grid = np.zeros((20, 40), dtype=np.int64)
    grid[5:10, 10:30] = 3
    d = plots.render_density(grid, (0, 1), (0, 1), xlabel="x", ylabel="y")
    assert "count:" in d.plain
    assert plots.sparkline([0, 1, 2, 3], 4) == " ▃▅█"


def test_colormaps():
    for cm in plots.COLORMAPS:
        for dark in (True, False):
            c = plots.cmap_color(cm, 0.5, dark)
            assert c.startswith("#") and len(c) == 7
    assert math.isclose(plots._cell_area_deg2(180, 360, np.arange(180)).sum() * 360, 41252.96, rel_tol=1e-4)
