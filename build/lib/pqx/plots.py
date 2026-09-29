"""Text-mode plots rendered as Rich ``Text``: sky map, histogram, 2-D density.

The sky map is a port of the Mollweide renderer in acid
(https://github.com/mjuric/acid, ``acid/io/skymap_art.py``) adapted to take an
equirectangular count grid (as produced by a DuckDB ``GROUP BY`` on binned
ra/dec) instead of a HEALPix map. The mechanism is the same:

* each character cell is *inverse*-projected to (lon, lat) and looked up in
  the grid, so cost is independent of the number of rows;
* **area** is carried by glyph shape — 2x2 quadrant sub-cells lit when their
  true covered fraction reaches 0.5, so a half-covered cell draws half-filled;
* **density** is carried by colour — log surface density (deg⁻²) through a
  colormap, with a colorbar legend in physical units;
* the projection limb and equator/central-meridian graticule are a fine
  braille outline, the graticule dimmer than the limb.

RA increases to the left (the sky seen from inside).
"""
from __future__ import annotations

import math

import numpy as np
from rich.style import Style
from rich.text import Text

_SQRT2 = math.sqrt(2.0)
_XEXT = 2.0 * _SQRT2
_YEXT = _SQRT2

_QUADRANT = {
    (0, 0, 0, 0): " ", (1, 0, 0, 0): "▘", (0, 1, 0, 0): "▝", (0, 0, 1, 0): "▖",
    (0, 0, 0, 1): "▗", (1, 1, 0, 0): "▀", (0, 0, 1, 1): "▄", (1, 0, 1, 0): "▌",
    (0, 1, 0, 1): "▐", (1, 0, 0, 1): "▚", (0, 1, 1, 0): "▞", (1, 1, 1, 0): "▛",
    (1, 1, 0, 1): "▜", (1, 0, 1, 1): "▙", (0, 1, 1, 1): "▟", (1, 1, 1, 1): "█",
}
_BRAILLE_DOTS = {(0, 0): 0x01, (0, 1): 0x02, (0, 2): 0x04, (1, 0): 0x08,
                 (1, 1): 0x10, (1, 2): 0x20, (0, 3): 0x40, (1, 3): 0x80}
_TRACE = "·"
_EIGHTHS = " ▁▂▃▄▅▆▇█"

#: Density colormaps. The perceptual ones are emitted as xterm-256 indices (exact
#: cube colours), so they survive terminals and multiplexers without truecolor;
#: ``terminal`` uses only the terminal's own palette (faint / accent / bold).
COLORMAPS = ("magma", "viridis", "inferno", "plasma", "gray", "terminal")
DEFAULT_CMAP = "magma"
DIM = Style(dim=True)
_CMAP_STOPS = {
    "viridis": [(68, 1, 84), (59, 82, 139), (33, 145, 140), (94, 201, 98), (253, 231, 37)],
    "inferno": [(0, 0, 4), (87, 16, 110), (188, 55, 84), (249, 142, 9), (252, 255, 164)],
    "magma": [(0, 0, 4), (81, 18, 124), (183, 55, 121), (252, 137, 97), (252, 253, 191)],
    "plasma": [(13, 8, 135), (126, 3, 168), (204, 71, 120), (248, 149, 64), (240, 249, 33)],
}
_COLOR_FLOOR = 0.22


def cmap_color(cmap: str, frac: float, dark_bg: bool = True) -> str:
    """Hex colour for ``frac`` in [0, 1] of ``cmap`` (low end floored for visibility)."""
    if cmap == "terminal":
        cmap = "gray"
    f = _COLOR_FLOOR + (1.0 - _COLOR_FLOOR) * min(1.0, max(0.0, float(frac)))
    if cmap == "gray":
        g = round(120 + f * 135) if dark_bg else round(210 - f * 190)
        return f"#{g:02x}{g:02x}{g:02x}"
    stops = _CMAP_STOPS[cmap]
    if not dark_bg:  # dark end must stay visible on a light background: flip
        f = 1.0 - f * 0.85
    x = f * (len(stops) - 1)
    i = int(x)
    if i >= len(stops) - 1:
        rgb = stops[-1]
    else:
        t = x - i
        a, b = stops[i], stops[i + 1]
        rgb = tuple(round(a[k] + (b[k] - a[k]) * t) for k in range(3))
    return "#{:02x}{:02x}{:02x}".format(*rgb)


_CUBE = (0, 95, 135, 175, 215, 255)


def xterm256(hex_color: str) -> int:
    """Nearest xterm-256 index (6x6x6 cube or 24-step gray ramp) to ``#rrggbb``."""
    r, g, b = (int(hex_color[i:i + 2], 16) for i in (1, 3, 5))

    def near(v):
        return min(range(6), key=lambda k: abs(_CUBE[k] - v))

    ci = 16 + 36 * near(r) + 6 * near(g) + near(b)
    cr, cg, cb = _CUBE[near(r)], _CUBE[near(g)], _CUBE[near(b)]
    gray = max(0, min(23, round((((r + g + b) / 3) - 8) / 10)))
    gv = 8 + 10 * gray
    d_cube = (r - cr) ** 2 + (g - cg) ** 2 + (b - cb) ** 2
    d_gray = (r - gv) ** 2 + (g - gv) ** 2 + (b - gv) ** 2
    return 232 + gray if d_gray < d_cube else ci


def density_style(cmap: str, frac: float, dark_bg: bool = True, accent: str = "blue",
                  dim: Style = DIM) -> Style:
    """Style for a density fraction in [0, 1]: a 256-colour index, or terminal roles."""
    if cmap == "terminal":
        if frac < 1 / 3:
            return dim
        if frac < 2 / 3:
            return Style(color=accent)
        return Style(bold=True)
    return Style(color=f"color({xterm256(cmap_color(cmap, frac, dark_bg))})")


def fmt_density(v: float) -> str:
    if v >= 1e6:
        return f"{v / 1e6:.1f}M"
    if v >= 1e4:
        return f"{v / 1e3:.0f}k"
    if v >= 1e3:
        return f"{v / 1e3:.1f}k"
    if v >= 10:
        return f"{v:.0f}"
    if v >= 1:
        return f"{v:.1f}"
    if v > 0:
        return f"{v:.2g}"
    return "0"


def _inverse_mollweide(x, y):
    t = y / _SQRT2
    valid = np.abs(t) <= 1.0
    theta = np.arcsin(np.clip(t, -1.0, 1.0))
    s = (2.0 * theta + np.sin(2.0 * theta)) / math.pi
    valid &= np.abs(s) <= 1.0
    lat = np.arcsin(np.clip(s, -1.0, 1.0))
    cos_t = np.cos(theta)
    with np.errstate(divide="ignore", invalid="ignore"):
        lon = math.pi * x / (2.0 * _SQRT2 * cos_t)
    valid &= np.isfinite(lon) & (np.abs(lon) <= math.pi + 1e-9)
    return lon, lat, valid


def _coarsen(grid: np.ndarray, f: int) -> np.ndarray:
    """Sum ``f x f`` blocks of a (nlat, nlon) grid (dimensions divisible or trimmed)."""
    if f <= 1:
        return grid
    nlat, nlon = grid.shape
    f = int(f)
    while nlat % f or nlon % f:
        f -= 1
    if f <= 1:
        return grid
    return grid.reshape(nlat // f, f, nlon // f, f).sum(axis=(1, 3))


def _lookup(grid: np.ndarray, lon_deg, lat_deg):
    nlat, nlon = grid.shape
    i = np.clip((np.mod(lon_deg, 360.0) / 360.0 * nlon).astype(int), 0, nlon - 1)
    j = np.clip(((lat_deg + 90.0) / 180.0 * nlat).astype(int), 0, nlat - 1)
    return grid[j, i], j


def _cell_area_deg2(nlat: int, nlon: int, j):
    """Area (deg²) of equirectangular cells in latitude row(s) ``j``."""
    dlat = 180.0 / nlat
    lat1 = np.radians(-90.0 + j * dlat)
    lat2 = np.radians(-90.0 + (j + 1) * dlat)
    return np.radians(360.0 / nlon) * (np.sin(lat2) - np.sin(lat1)) * (180.0 / math.pi) ** 2


def sky_shape(width: int, height: int | None = None) -> tuple[int, int]:
    """(w, h) character size for a Mollweide map fitting in width x height."""
    w = max(8, int(width))
    if height is not None:
        w = min(w, max(8, int(height) * 4))
    return w, max(3, round(w / 4.0))


def render_skymap(grid: np.ndarray, *, width: int = 72, height: int | None = None,
                  cmap: str = DEFAULT_CMAP, dark_bg: bool = True, center: float = 0.0,
                  caption: str | None = None, accent: str = "blue", dim: Style = DIM) -> Text:
    """Render a (nlat, nlon) equirectangular count grid as a Mollweide map."""
    width, height = sky_shape(width, height)
    grid = np.asarray(grid)
    nlat, nlon = grid.shape
    res = 180.0 / nlat

    def to_lonlat(x, y):
        lon, lat, valid = _inverse_mollweide(x, y)
        return (np.degrees(lon) + center) % 360.0, np.degrees(lat), valid

    # --- density per character (surface density at ~character resolution)
    fch = max(1, int(round((360.0 / width) / res)))
    cg = _coarsen(grid, fch)
    rr, cc = np.meshgrid(np.arange(height), np.arange(width), indexing="ij")
    sigma = np.zeros((height, width))
    for sy in (1 / 6, 0.5, 5 / 6):
        for sx in (1 / 6, 0.5, 5 / 6):
            x = _XEXT * (1.0 - 2.0 * ((cc + sx) / width))
            y = _YEXT * (1.0 - 2.0 * ((rr + sy) / height))
            lon, lat, valid = to_lonlat(x, y)
            if not valid.any():
                continue
            n, j = _lookup(cg, lon[valid], lat[valid])
            dens = n / _cell_area_deg2(cg.shape[0], cg.shape[1], j)
            sigma[valid] = np.maximum(sigma[valid], dens)
    pos = sigma[sigma > 0]
    smin, smax = (float(pos.min()), float(pos.max())) if pos.size else (0.0, 0.0)
    if smax > smin:
        frac = np.where(sigma > 0, (np.log(np.maximum(sigma, smin)) - math.log(smin)) /
                        (math.log(smax) - math.log(smin)), 0.0)
    else:
        frac = np.where(sigma > 0, 1.0, 0.0)

    # --- coverage per 2x2 sub-cell: occupied fine cells / total in lookup cell
    sw, sh = 2 * width, 2 * height
    fsub = max(1, int(round((360.0 / sw) / res)))
    occ = _coarsen((grid > 0).astype(np.int64), fsub)
    ftrue = grid.shape[0] // occ.shape[0]
    cov_grid = occ / float(ftrue * ftrue)
    sr, sc = np.meshgrid(np.arange(sh), np.arange(sw), indexing="ij")
    x = _XEXT * (1.0 - 2.0 * ((sc + 0.5) / sw))
    y = _YEXT * (1.0 - 2.0 * ((sr + 0.5) / sh))
    lon, lat, svalid = to_lonlat(x, y)
    cov = np.zeros((sh, sw))
    if svalid.any():
        cov[svalid], _ = _lookup(cov_grid, lon[svalid], lat[svalid])
    on = cov >= 0.5

    # --- braille limb + graticule
    bw, bh = 2 * width, 4 * height
    br, bc = np.meshgrid(np.arange(bh), np.arange(bw), indexing="ij")
    bx = _XEXT * (1.0 - 2.0 * ((bc + 0.5) / bw))
    by = _YEXT * (1.0 - 2.0 * ((br + 0.5) / bh))
    blon, blat, bvalid = _inverse_mollweide(bx, by)
    limb = np.zeros((bh, bw), bool)
    grat = np.zeros((bh, bw), bool)
    for c in range(bw):
        col = np.where(bvalid[:, c])[0]
        if col.size:
            limb[col.min(), c] = limb[col.max(), c] = True
    latc = np.where(bvalid, np.degrees(blat), 99.0)
    eq_row = int(np.argmin(np.abs(latc).min(axis=1)))
    grat[eq_row, bvalid[eq_row]] = True
    grat[bvalid[:, bw // 2], bw // 2] = True
    for dlat in (-60, -30, 30, 60):  # faint parallels
        r = int(np.argmin(np.abs(np.where(bvalid, np.degrees(blat) - dlat, 99.0)).min(axis=1)))
        grat[r, bvalid[r] & (np.arange(bw) % 4 == 0)] = True

    limb_st, grat_st = dim, dim
    text = Text(no_wrap=True, overflow="crop")
    for r in range(height):
        for c in range(width):
            y0, x0 = 2 * r, 2 * c
            ch = " "
            if svalid[y0:y0 + 2, x0:x0 + 2].any():
                key = (int(on[y0, x0]), int(on[y0, x0 + 1]), int(on[y0 + 1, x0]), int(on[y0 + 1, x0 + 1]))
                ch = _QUADRANT[key]
                if ch == " " and cov[y0:y0 + 2, x0:x0 + 2].max() > 0:
                    ch = _TRACE
            if ch != " ":
                text.append(ch, density_style(cmap, frac[r, c], dark_bg, accent, dim))
                continue
            code, has_limb = 0, False
            by0, bx0 = 4 * r, 2 * c
            for (dx, dy), bit in _BRAILLE_DOTS.items():
                if limb[by0 + dy, bx0 + dx]:
                    code |= bit
                    has_limb = True
                elif grat[by0 + dy, bx0 + dx]:
                    code |= bit
            if code:
                text.append(chr(0x2800 + code), limb_st if has_limb else grat_st)
            else:
                text.append(" ")
        text.append("\n")

    # --- RA labels under the equator ends / centre
    lbl = [" "] * width
    for s, pos_ in ((f"{(center + 180) % 360:.0f}°", 0), (f"{center % 360:.0f}°", width // 2),
                    (f"{(center - 180) % 360:.0f}°", width - 1)):
        start = max(0, min(width - len(s), pos_ - len(s) // 2))
        lbl[start:start + len(s)] = list(s)
    text.append("".join(lbl) + "\n", dim)
    if smax > 0:
        text.append_text(colorbar(cmap, smin, smax, dark_bg, unit="deg⁻²", width=width, accent=accent, dim=dim))
    if caption:
        text.append("\n" + caption.center(width), dim)
    return text


def colorbar(cmap: str, vmin: float, vmax: float, dark_bg: bool, *, unit: str = "",
             width: int = 72, label: str = "density", accent: str = "blue", dim: Style = DIM) -> Text:
    """A log-scale colour legend: swatches with their values."""
    t = Text(no_wrap=True)
    if vmax <= 0:
        return t
    lo = math.log(max(vmin, 1e-300))
    hi = math.log(vmax)
    vals = [vmax] if vmax <= vmin else [math.exp(lo + (hi - lo) * i / 5) for i in range(6)]
    seen, parts = set(), []
    for v in vals:
        s = fmt_density(v)
        if s not in seen:
            seen.add(s)
            parts.append((v, s))
    vis = f"{label}: " + "  ".join(f"█ {s}" for _, s in parts) + (f"  {unit}" if unit else "")
    pad = max(0, (width - len(vis)) // 2)
    t.append(" " * pad + f"{label}: ", dim)
    for k, (v, s) in enumerate(parts):
        f = 1.0 if vmax <= vmin else (math.log(v) - lo) / (hi - lo)
        t.append("█", density_style(cmap, f, dark_bg, accent, dim))
        t.append(f" {s}" + ("  " if k < len(parts) - 1 else ""))
    if unit:
        t.append(f"  {unit}", dim)
    return t


# ------------------------------------------------------------------ histogram
def _nice_num(v: float, digits: int = 3) -> str:
    if v == 0:
        return "0"
    a = abs(v)
    if a >= 1e6 or a < 1e-3:
        return f"{v:.{digits}g}"
    if a >= 100 and digits <= 3:
        return f"{v:.0f}"
    return f"{v:.{digits}g}"


def axis_labels(values: list[float], fmt=None) -> list[str]:
    """Format tick values with just enough precision that they are all distinct."""
    if fmt is not None:
        return [fmt(v) for v in values]
    for d in range(3, 18):
        labs = [_nice_num(v, d) for v in values]
        if len(set(labs)) == len(set(values)):
            return labs
    return [repr(v) for v in values]


def render_histogram(edges: list[float], counts: list[int], *, width: int = 70, height: int = 12,
                     color: str | None = None, log_y: bool = False, xlabel: str = "",
                     log_x: bool = False, xfmt=None, dim: Style = DIM) -> Text:
    """Vertical bar histogram with eighth-block resolution and labelled axes."""
    t = Text(no_wrap=True, overflow="crop")
    if not counts:
        t.append("(no finite values)", dim + Style(italic=True))
        return t
    counts = np.asarray(counts, dtype=float)
    vals = np.log10(counts + 1) if log_y else counts
    vmax = float(vals.max()) or 1.0
    cmax = int(counts.max())
    ylab = [f"{cmax:,}", f"{int(counts.sum()):,} tot"]
    yw = max(len(s) for s in ylab) + 1
    plot_w = max(10, width - yw - 1)
    nb = len(counts)
    # map bins onto columns (nearest), 1+ columns per bin
    col_bin = np.minimum((np.arange(plot_w) * nb / plot_w).astype(int), nb - 1)
    colv = vals[col_bin]
    levels = colv / vmax * height * 8
    for r in range(height):
        base = (height - 1 - r) * 8
        if r == 0:
            t.append(f"{cmax:>{yw - 1},} ┤", dim)
        elif r == height - 1:
            t.append(" " * (yw - 1) + " ┤", dim)
        else:
            t.append(" " * (yw - 1) + " │", dim)
        row = []
        for lv, cnt in zip(levels, counts[col_bin]):
            k = int(round(min(8, max(0, lv - base))))
            if k == 0 and r == height - 1 and cnt > 0:
                k = 1  # never let a populated bin vanish
            row.append(_EIGHTHS[k])
        t.append("".join(row), Style(color=color) if color else Style())
        t.append("\n")
    t.append(" " * yw + "└" + "─" * plot_w + "\n", dim)
    lo, hi = edges[0], edges[-1]
    mid = (lo + hi) / 2
    if xfmt is None and log_x:
        a, m, b = axis_labels([10 ** lo, 10 ** mid, 10 ** hi])
    else:
        a, m, b = axis_labels([lo, mid, hi], xfmt)
    axis = [" "] * plot_w
    for s, p in ((a, 0), (m, plot_w // 2 - len(m) // 2), (b, plot_w - len(b))):
        p = max(0, min(plot_w - len(s), p))
        axis[p:p + len(s)] = list(s)
    t.append(" " * (yw + 1) + "".join(axis) + "\n", dim)
    note = "log₁₀(count+1) scale" if log_y else ""
    lab = (xlabel + (" (log x)" if log_x else "")).strip()
    footer = "  ".join(s for s in (lab, note) if s)
    if footer:
        t.append(" " * (yw + 1) + footer.center(plot_w), dim + Style(italic=True))
    return t


def sparkline(counts: list[int], width: int = 20) -> str:
    if not counts:
        return ""
    c = np.asarray(counts, float)
    nb = len(c)
    idx = np.minimum((np.arange(width) * nb / width).astype(int), nb - 1)
    v = c[idx]
    m = v.max() or 1.0
    return "".join(" ▁▂▃▄▅▆▇█"[int(round(x / m * 8))] if x > 0 else " " for x in v)


# ------------------------------------------------------------- 2-D density
def render_density(grid: np.ndarray, xlim, ylim, *, cmap: str = DEFAULT_CMAP, dark_bg: bool = True,
                   xlabel: str = "", ylabel: str = "", accent: str = "blue", dim: Style = DIM) -> Text:
    """Render a (2h, 2w) count grid (row 0 = lowest y) as a quadrant-glyph density plot."""
    grid = np.asarray(grid)
    sh, sw = grid.shape
    h, w = sh // 2, sw // 2
    g = grid[::-1]  # row 0 = top
    on = g > 0
    cell = g[: 2 * h, : 2 * w].reshape(h, 2, w, 2).max(axis=(1, 3))
    pos = cell[cell > 0]
    vmin, vmax = (float(pos.min()), float(pos.max())) if pos.size else (0.0, 0.0)
    ylabs = axis_labels([ylim[1], (ylim[0] + ylim[1]) / 2, ylim[0]])
    yw = max(len(s) for s in ylabs)
    t = Text(no_wrap=True, overflow="crop")
    for r in range(h):
        lab = ylabs[0] if r == 0 else ylabs[2] if r == h - 1 else ylabs[1] if r == h // 2 else ""
        t.append(f"{lab:>{yw}} ┤" if lab else " " * yw + " │", dim)
        for c in range(w):
            y0, x0 = 2 * r, 2 * c
            key = (int(on[y0, x0]), int(on[y0, x0 + 1]), int(on[y0 + 1, x0]), int(on[y0 + 1, x0 + 1]))
            ch = _QUADRANT[key]
            if ch == " ":
                t.append(" ")
                continue
            v = cell[r, c]
            f = 1.0 if vmax <= vmin else (math.log(v) - math.log(vmin)) / (math.log(vmax) - math.log(vmin))
            t.append(ch, density_style(cmap, f, dark_bg, accent, dim))
        t.append("\n")
    t.append(" " * yw + " └" + "─" * w + "\n", dim)
    xl = axis_labels([xlim[0], (xlim[0] + xlim[1]) / 2, xlim[1]])
    axis = [" "] * w
    for s, p in ((xl[0], 0), (xl[1], w // 2 - len(xl[1]) // 2), (xl[2], w - len(xl[2]))):
        p = max(0, min(w - len(s), p))
        axis[p:p + len(s)] = list(s)
    t.append(" " * (yw + 2) + "".join(axis) + "\n", dim)
    if xlabel or ylabel:
        t.append(" " * (yw + 2) + f"x: {xlabel}   y: {ylabel}".center(w) + "\n", dim + Style(italic=True))
    if vmax > 0:
        t.append_text(colorbar(cmap, vmin, vmax, dark_bg, unit="rows/cell", width=w + yw + 2, label="count", accent=accent, dim=dim))
    return t
