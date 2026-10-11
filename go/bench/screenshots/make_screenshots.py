# /// script
# requires-python = ">=3.10"
# dependencies = ["numpy", "pyarrow", "pyte", "rich"]
# ///
"""Regenerate the README screenshots (docs/screenshots/*.png) from the Go pqx.

    uv run go/bench/screenshots/make_screenshots.py [--pqx BIN] [--chrome PATH] [--out DIR]

Writes made-up data (4M taxi trips from tools/make_trips.py, and the synthetic LSST
file from tools/make_demo.py for the sky map) to a temporary directory, runs pqx in
a pty at 150x42 cells with ``--theme tokyo-night`` and a truecolor terminal
(go/bench/pty/ptydrive.py, which emulates the screen with pyte), presses the keys of
each scene, and turns the emulated screen, with its colours and attributes, into an
SVG with Rich's terminal export: the same export, window frame and size as Textual's
screenshots of Python pqx. Headless Chrome renders the SVG to PNG (``--chrome``,
``$CHROME``, or a ``chromium``/``headless_shell``/Playwright install).

Without ``--pqx`` it builds pqx with ``go build`` (Go and a C compiler needed),
labelled with ``--label`` (default 0.3.0), the version the title bar shows.
"""
from __future__ import annotations

import argparse
import glob
import os
import re
import shutil
import subprocess
import sys
import tempfile
import time

from rich.console import Console
from rich.style import Style
from rich.text import Text

HERE = os.path.dirname(os.path.abspath(__file__))
GO_ROOT = os.path.dirname(os.path.dirname(HERE))
REPO = os.path.dirname(GO_ROOT)
sys.path.insert(0, os.path.join(GO_ROOT, "bench", "pty"))
from ptydrive import Session  # noqa: E402

COLS, ROWS = 150, 42
THEME = "tokyo-night"
FILTER = "payment_type = 'card' and trip_distance > 10"
QUERY = "select pickup_zone, count(*) as trips, avg(tip_amount) as avg_tip from t group by 1 order by 2 desc"
SCENES = ("hero", "detail", "sql", "schema", "stats", "plot", "sky")

#: pyte's names for the 16 ANSI colours, as Rich names them
ANSI = {"brown": "yellow", "brightbrown": "bright_yellow"}


def rich_colour(c: str) -> str | None:
    if c == "default":
        return None
    if re.fullmatch(r"[0-9a-fA-F]{6}", c):
        return "#" + c
    c = ANSI.get(c, c)
    return c.replace("bright", "bright_") if c.startswith("bright") and "_" not in c else c


def screen_text(s: Session) -> Text:
    """The emulated screen as Rich text, cell by cell with each cell's style."""
    out = Text()
    buf = s.screen.buffer
    for y in range(s.rows):
        line = buf[y]
        for x in range(s.cols):
            c = line[x]
            if c.data == "":  # the right half of a wide character
                continue
            out.append(c.data, Style(color=rich_colour(c.fg), bgcolor=rich_colour(c.bg), bold=c.bold,
                                     dim=c.blink, reverse=c.reverse, underline=c.underscore,
                                     italic=c.italics))
        if y < s.rows - 1:
            out.append("\n")
    return out


def to_svg(s: Session, title: str) -> str:
    console = Console(width=s.cols, height=s.rows, file=open(os.devnull, "w"), force_terminal=True,
                      color_system="truecolor", record=True, legacy_windows=False, safe_box=False)
    console.print(screen_text(s), end="", crop=True, overflow="crop", no_wrap=True)
    return console.export_svg(title=title)


BLOCKS = set("█▀▄▌▐▖▗▘▙▚▛▜▝▞▟▁▂▃▅▆▇▉▊▋▍▎▏ ")


def close_seams(svg: str) -> str:
    """Stroke runs of block characters in their own colour: browsers leave hairline
    gaps between adjacent glyph runs, which show as a grid over plots."""
    fills = dict(re.findall(r"\.(terminal-\d+-r\d+) \{[^}]*?fill: (#[0-9a-fA-F]{6})", svg))

    def fix(m):
        cls, rest, txt = m.groups()
        if txt and cls in fills and all(c in BLOCKS for c in txt.replace("&#160;", " ")):
            return f'<text class="{cls}"{rest} style="stroke:{fills[cls]};stroke-width:1.5px">{txt}</text>'
        return m.group(0)

    return re.sub(r'<text class="(terminal-\d+-r\d+)"([^>]*)>([^<]*)</text>', fix, svg)


def find_chrome(arg: str | None) -> str:
    for c in (arg, os.environ.get("CHROME")):
        if c:
            return c
    pattern = os.path.expanduser("~/.cache/ms-playwright/chromium_headless_shell-*/*/chrome-headless-shell")
    found = sorted(glob.glob(pattern)) or [p for p in (shutil.which("chromium"), shutil.which("chromium-browser"),
                                                       shutil.which("google-chrome"),
                                                       "/usr/lib64/chromium-browser/headless_shell") if p and os.path.exists(p)]
    if not found:
        sys.exit("no headless Chrome found: pass --chrome or set $CHROME")
    return found[-1]


def to_png(chrome: str, svg: str, svg_path: str, png_path: str) -> None:
    svg = close_seams(svg)
    w, h = (int(float(v) + 0.99) for v in re.search(r'viewBox="0 0 ([\d.]+) ([\d.]+)"', svg).groups())
    with open(svg_path, "w") as f:
        f.write(svg)
    subprocess.run([chrome, "--headless", "--no-sandbox", "--hide-scrollbars", f"--screenshot={png_path}",
                    f"--window-size={w},{h}", "--virtual-time-budget=5000", f"file://{svg_path}"],
                   check=True, capture_output=True)


# ------------------------------------------------------------------ the scenes
def done(s: Session, timeout: float = 60.0) -> None:
    """Until no spinner is on screen and the screen has stopped changing."""
    end = time.perf_counter() + timeout
    while time.perf_counter() < end:
        s.settle(quiet=0.8, timeout=max(1.0, end - time.perf_counter()))
        if not re.search(r"[⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏]", s.text()):
            return
    sys.exit(f"pqx didn't settle:\n{s.text()}")


def step_to(s: Session, rx: str, n: int = 20) -> None:
    """Press → on the focused plot setting until its line matches `rx`."""
    for _ in range(n):
        if re.search(rx, s.text()):
            return
        s.keys("right")
        done(s)
    sys.exit(f"no {rx!r} on screen:\n{s.text()}")


def shoot(pqx: str, path: str, name: str) -> Session:
    env = dict(os.environ, COLORTERM="truecolor")
    s = Session([pqx, "--theme", THEME, path], cols=COLS, rows=ROWS, env=env)
    if not s.wait(r"rows", timeout=60):
        sys.exit(f"pqx didn't start:\n{s.text()}")
    done(s)
    if name == "hero":
        s.keys("/")
        s.type(FILTER, gap=0.01)
        s.keys("enter")
        done(s)
        s.keys("down", "down", "down")
    elif name == "detail":
        s.keys("down", "down", "down", "down", "d")
    elif name == "sql":
        s.keys("/")
        s.type(QUERY, gap=0.01)
        s.keys("enter")
    elif name == "schema":
        s.keys("2")
        done(s)
        s.keys(*["down"] * 5)
    elif name == "stats":
        s.keys(*["right"] * 5)  # trip_distance
        done(s)
        s.keys("3")
        done(s)
        s.keys("L")  # log values
    elif name == "plot":
        s.keys("4")
        done(s)
        step_to(s, r"mode xy ")
        s.keys("tab")
        step_to(s, r"x pickup_lon ")
        s.keys("tab")
        step_to(s, r"y pickup_lat ")
        s.keys("shift+tab", "shift+tab")  # focus back on mode, as Python's screenshot has it
    elif name == "sky":
        s.keys("4")
    done(s)
    return s


def build(label: str, out: str) -> str:
    binary = os.path.join(out, "pqx")
    subprocess.run(["go", "build", "-tags", "duckdb_arrow", "-ldflags", f"-X main.version={label}",
                    "-o", binary, "./cmd/pqx"], cwd=GO_ROOT, check=True)
    return binary


def main() -> None:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--pqx", help="the pqx binary (default: build one)")
    ap.add_argument("--label", default="0.3.0", help="version to build into pqx (default %(default)s)")
    ap.add_argument("--chrome", help="headless Chrome or Chromium")
    ap.add_argument("--out", default=os.path.join(REPO, "docs", "screenshots"), help="where the PNGs go")
    ap.add_argument("--svg", action="store_true", help="keep the SVGs next to the PNGs")
    ap.add_argument("--data", help="keep the generated Parquet files in this directory, and reuse them")
    ap.add_argument("scenes", nargs="*", metavar="SCENE", help=f"which screenshots ({', '.join(SCENES)}; default all)")
    a = ap.parse_args()
    if bad := set(a.scenes) - set(SCENES):
        ap.error(f"unknown scenes: {', '.join(sorted(bad))}")
    chrome = find_chrome(a.chrome)
    with tempfile.TemporaryDirectory() as tmp:
        pqx = a.pqx or build(a.label, tmp)
        data = a.data or tmp
        os.makedirs(data, exist_ok=True)
        trips, sso = os.path.join(data, "trips.parquet"), os.path.join(data, "sso.parquet")
        tools = os.path.join(REPO, "tools")
        if not os.path.exists(trips):
            subprocess.run([sys.executable, os.path.join(tools, "make_trips.py"), trips, "4000000"], check=True)
        if not os.path.exists(sso):
            subprocess.run([sys.executable, os.path.join(tools, "make_demo.py"), sso, "--rows", "1000000"],
                           check=True)
        for name in a.scenes or SCENES:
            path = sso if name == "sky" else trips
            with shoot(pqx, path, name) as s:
                svg = to_svg(s, f"pqx — {os.path.basename(path)}")
            svg_path = os.path.join(a.out if a.svg else tmp, f"{name}.svg")
            to_png(chrome, svg, svg_path, os.path.join(a.out, f"{name}.png"))
            print(f"{name}.png", file=sys.stderr)


if __name__ == "__main__":
    main()
