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
    assert F.format_value(60800.123456789, "mjd") == "60800.1234568"
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


def test_derived_angles():
    d = F.derived
    # RA-like names: hours
    for name in ("ra", "RA", "raJ2000", "coord_ra", "coordRa", "ra_deg", "ra_icrs"):
        assert d(name, "angle", 180.0) == "12h00m00.000s", name
    # Dec / latitudes: signed degrees, only within ±90
    for name in ("dec", "decl", "decJ2000", "coordDec", "lat", "glat", "elat", "beta", "pickup_lat"):
        assert d(name, "angle", -30.5) == "-30°30′00.00″", name
        assert d(name, "angle", 45.25) == "+45°15′00.00″", name
        assert d(name, "angle", 120.0) == "", name
    # every other longitude: degrees, any range, no hours
    assert d("pickup_lon", "angle", -74.000439) == "-74°00′01.58″"
    for name in ("lon", "glon", "elon", "lambda", "pickup_lon"):
        assert d(name, "angle", 285.5) == "285°30′00.00″", name
        assert d(name, "angle", 12.0) == "12°00′00.00″", name
        assert d(name, "angle", -170.25) == "-170°15′00.00″", name
    assert "h" not in d("lon", "angle", 30.0)


def test_percent():
    assert F.percent(1_290_773, 1_290_773) == "100%"     # was "1e+02%"
    assert F.percent(0, 10) == "0%"
    assert F.percent(350_100, 500_000) == "70%"
    assert F.percent(1, 10**6) == "<0.01%"
    assert F.percent(999_999, 10**6) == ">99.99%"
    assert F.percent(9_995, 10_000) == "99.95%"
    assert F.percent(21, 20_000) == "0.105%"
    assert F.percent(5, 0) == ""


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
    assert plots.DEFAULT_CMAP == "magma"
    assert plots.xterm256("#000000") == 16 and plots.xterm256("#ffffff") == 231
    assert plots.xterm256("#d7af5f") == 179 and plots.xterm256("#808080") == 244
    assert str(plots.density_style("magma", 0.5).color).startswith("Color('color(")
    assert plots.density_style("terminal", 0.9).bold
    assert plots.density_style("terminal", 0.5, accent="cyan").color.name == "cyan"
    assert math.isclose(plots._cell_area_deg2(180, 360, np.arange(180)).sum() * 360, 41252.96, rel_tol=1e-4)


def test_version_comes_from_git():
    import subprocess
    import sys

    import pqx

    # setuptools-scm wrote pqx/_version.py at install time; the fallback means it didn't
    assert pqx.__version__ != "0.0.0.dev0"
    out = subprocess.run([sys.executable, "-m", "pqx", "--version"], capture_output=True, text=True).stdout
    assert out.strip() == f"pqx {pqx.__version__}"


def test_overrides():
    # ints are decimals for fixed-point kinds, significant digits for the others
    assert F.format_value(12.3456789, "angle", override=2) == "12.35"
    assert F.format_value(28561.3, "flux", override=2) == "29000"
    assert F.format_value(28561.3, "flux", override=".2e") == "2.86e+04"
    assert F.format_value(1234567, "int", override=",d") == "1,234,567"
    assert F.format_value(1234567, "int", override=3) == "1234567"  # digits mean nothing for ints
    assert F.format_value(1.5, "float", override=",d") == "1.5"  # a spec that doesn't fit falls back
    assert F.format_value(1.5, "float", override=".3f", raw=True) == "1.5"  # raw wins
    assert F.format_value(None, "float", override=".3f") == F.NULL
    assert F.format_value(float("nan"), "float", override=".3f") == "NaN"

    assert F.step_override(None, "angle", -1) == 5
    assert F.step_override(None, "flux", 1) == 5
    assert F.step_override(0, "mag", -1) == 0 and F.step_override(1, "err", -1) == 1
    assert F.step_override(".3e", "float", 1) == ".4e"
    assert F.step_override(",d", "flux", 1) == 5  # no precision in the spec: start from the default
    assert F.step_override(None, "int", 1) is None and F.step_override(None, "str", 1) is None

    assert F.describe_override(4, "angle") == ".4f"
    assert F.describe_override(4, "flux") == "4 sig"
    assert F.describe_override(".2e", "flux") == ".2e"
    assert F.override_error(".2f", "float", 1.5) is None and F.override_error(",d", "float", 1.5)
    assert F.override_error(",d") is None  # no kind: fine if it suits some column
    assert F.override_error(".2f", "time")  # strftime would echo it back literally
    assert F.override_error("%Y-%m", "time") is None and F.override_error("%Y-%m") is None
    assert F.override_error(4, "int") and F.override_error(4, "flux") is None
    assert F.override_error(18, "flux") and F.override_error(".100000000f")  # would hang the grid
    assert F.override_error(".2f", "bool")
    assert "Unknown format code" in F.override_error(".2q")  # not the timestamp hint
    assert F.override_error("%Y-2026 (UTC+0100)") is None and F.override_error(".100%")


def test_override_edge_cases():
    import decimal

    assert F.format_value(decimal.Decimal("1.23456"), "float", override=3) == "1.23"
    assert F.format_value(1.0, "float", override=10**8) == F.format_value(1.0, "float", override=F.MAX_DIGITS)
    assert F.format_value(True, "bool", override=".2f") == "✓"  # specs don't apply to bools
    assert len(F.format_value("x", "str", override="<200")) == 40  # still truncated to the cell width
    assert F.format_value(dt.datetime(2026, 1, 2), "time", override="%Y-%m") == "2026-01"
