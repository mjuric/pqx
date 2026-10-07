"""Regenerate the README screenshots in this directory.

    python docs/screenshots/make_screenshots.py [--chrome PATH]

Writes made-up data (4M taxi trips, and pqx's synthetic LSST file for the sky
map) to a temporary directory, drives pqx headlessly with Textual's pilot at
150x42 cells in ``--theme tokyo-night``, saves each screen with Textual's SVG
export and renders it to PNG with headless Chrome (``--chrome``, ``$CHROME``,
or the one Playwright installs: ``python -m playwright install chromium``).
"""
from __future__ import annotations

import argparse
import asyncio
import glob
import os
import re
import shutil
import subprocess
import sys
import tempfile

from pqx.app import PlotControls, PqxApp

HERE = os.path.dirname(os.path.abspath(__file__))
SIZE = (150, 42)
THEME = "tokyo-night"
FILTER = "payment_type = 'card' and trip_distance > 10"
QUERY = "select pickup_zone, count(*) as trips, avg(tip_amount) as avg_tip from t group by 1 order by 2 desc"


async def settle(app, pilot, t=0.3):
    await pilot.pause(t)
    for _ in range(400):
        if not [w for w in app.workers if w.state.name in ("PENDING", "RUNNING") and w.group != "footer"]:
            break
        await pilot.pause(0.05)
    await pilot.pause(t)


async def typed(pilot, text):
    await pilot.press(*[{" ": "space"}.get(c, c) for c in text])


async def shoot(path: str, name: str, out: str) -> str:
    app = PqxApp(path, theme=THEME)
    async with app.run_test(size=SIZE) as pilot:
        await settle(app, pilot, 0.6)
        if name == "hero":
            await pilot.press("slash")
            await typed(pilot, FILTER)
            await pilot.press("enter")
            await settle(app, pilot, 1.0)
            await pilot.press("down", "down", "down")
        elif name == "detail":
            await pilot.press("down", "down", "down", "down", "d")
        elif name == "sql":
            await pilot.press("slash")
            await typed(pilot, QUERY)
            await pilot.press("enter")
            await settle(app, pilot, 1.5)
        elif name == "schema":
            await pilot.press("2")
            await settle(app, pilot, 1.0)
            await pilot.press(*["down"] * 5)
        elif name == "stats":
            app.set_current_column("trip_distance", "grid")
            await pilot.press("3")
            await settle(app, pilot, 1.5)
            await pilot.press("L")  # log values
        elif name == "plot":
            await pilot.press("4")
            await settle(app, pilot, 0.5)
            pc = app.query_one(PlotControls)
            pc.set_value("mode", "xy")
            pc.set_value("x", "pickup_lon")
            pc.set_value("y", "pickup_lat")
        elif name == "sky":
            await pilot.press("4")
        await settle(app, pilot, 1.5)
        return app.save_screenshot(f"{name}.svg", out)


BLOCKS = set("█▀▄▌▐▖▗▘▙▚▛▜▝▞▟▁▂▃▅▆▇▉▊▋▍▎▏ ")


def close_seams(svg: str) -> str:
    """Stroke runs of block characters in their own colour: browsers leave hairline
    gaps between adjacent glyph runs, which show as a grid over plots."""
    fills = dict(re.findall(r"\.(terminal-\d+-r\d+) \{[^}]*?fill: (#[0-9a-fA-F]{6})", svg))

    def fix(m):
        cls, rest, txt = m.groups()
        if txt and cls in fills and all(c in BLOCKS for c in txt.replace("&#160;", " ")):
            return f'<text class="{cls}"{rest} style="stroke:{fills[cls]};stroke-width:1.5px">{txt}</text>'
        return m.group(0)

    return re.sub(r'<text class="(terminal-\d+-r\d+)"([^>]*)>([^<]*)</text>', fix, svg)


def find_chrome(arg: str | None) -> str:
    for c in (arg, os.environ.get("CHROME")):
        if c:
            return c
    pattern = os.path.expanduser("~/.cache/ms-playwright/chromium_headless_shell-*/*/chrome-headless-shell")
    found = sorted(glob.glob(pattern)) or [shutil.which(n) for n in ("chromium", "google-chrome") if shutil.which(n)]
    if not found:
        sys.exit("no headless Chrome found: pass --chrome or set $CHROME")
    return found[-1]


def to_png(chrome: str, svg_path: str, png_path: str) -> None:
    svg = close_seams(open(svg_path).read())
    w, h = (int(float(v) + 0.99) for v in re.search(r'viewBox="0 0 ([\d.]+) ([\d.]+)"', svg).groups())
    fixed = svg_path.replace(".svg", ".fixed.svg")
    open(fixed, "w").write(svg)
    subprocess.run([chrome, "--headless", "--no-sandbox", "--hide-scrollbars", f"--screenshot={png_path}",
                    f"--window-size={w},{h}", "--virtual-time-budget=5000", f"file://{fixed}"],
                   check=True, capture_output=True)


def main() -> None:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--chrome")
    a = ap.parse_args()
    chrome = find_chrome(a.chrome)
    with tempfile.TemporaryDirectory() as tmp:
        trips, sso = os.path.join(tmp, "trips.parquet"), os.path.join(tmp, "sso.parquet")
        subprocess.run([sys.executable, os.path.join(HERE, "make_trips.py"), trips, "4000000"], check=True)
        subprocess.run([sys.executable, "-m", "pqx.demo", sso, "--rows", "1000000"], check=True)
        for name in ("hero", "detail", "sql", "schema", "stats", "plot", "sky"):
            asyncio.run(shoot(sso if name == "sky" else trips, name, tmp))
            to_png(chrome, os.path.join(tmp, f"{name}.svg"), os.path.join(HERE, f"{name}.png"))
            print(f"{name}.png", file=sys.stderr)


if __name__ == "__main__":
    main()
